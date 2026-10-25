package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/notify"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 「指派久无人接」的一次性提醒（#180 B3d）：后台扫描 → 通知闸 → 浏览器（SSE notify）与 ntfy 两条通道。
// 到点的判断与「只提醒一次」由 store 的测试覆盖，这里验证真实装配下的这条链路、两种推迟（免打扰 / 关掉开关）、
// 阈值设置、每遍上限、文案，以及后台协程本身（启停、实验用的时间单位、内部出错不带走进程）。

type remindFix struct {
	t    *testing.T
	g    *gateRig
	ctx  context.Context
	mgr  store.WorkActor
	base time.Time // 所有指派都不早于它；扫描时用 base + 偏移当「现在」，不必真等
}

// newRemindFix 建好一块任务板：wf-a 是派发者，另外的工作流各有自己的目录，都在同一个项目里。
func newRemindFix(t *testing.T, workflows ...string) *remindFix {
	t.Helper()
	g := newGateRig(t)
	ctx := context.Background()
	// 先在两个别的（独立的）任务板上各垫一个任务：让任务的数据库编号（任务页地址里的 ?task=）与板内序号（文案里的 #N）错开。
	// 只有一块板、从头建任务时两者恰好相等，深链用错了编号就测不出来。
	for _, wf := range []string{"wf-pad1", "wf-pad2"} {
		if _, err := g.st.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: "/pad/" + wf, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.st.CreateWorkItem(ctx, store.WorkItemInput{WorkflowID: wf, Title: "垫一个"},
			store.WorkActor{Kind: model.WorkActorAgent, WorkflowID: wf, Manager: true, Policy: store.WorkPolicyEnforce}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := g.st.CreateProject(ctx, store.ProjectInput{Name: "主项目", Slug: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for _, wf := range append([]string{"wf-a"}, workflows...) {
		dir := "/code/" + wf
		if _, err := g.st.AddProjectDir(ctx, p.ID, "h", dir, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := g.st.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: dir, Title: "t", Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	return &remindFix{t: t, g: g, ctx: ctx, base: time.Now(),
		mgr: store.WorkActor{Kind: model.WorkActorAgent, WorkflowID: "wf-a", Manager: true, Policy: store.WorkPolicyEnforce}}
}

func (f *remindFix) assign(title, to string) *model.WorkItem {
	f.t.Helper()
	res, err := f.g.st.CreateWorkItem(f.ctx, store.WorkItemInput{WorkflowID: "wf-a", Title: title, Assignee: to}, f.mgr)
	if err != nil {
		f.t.Fatal(err)
	}
	if res.Item.AssignedAt != nil && res.Item.AssignedAt.After(f.base) {
		f.base = *res.Item.AssignedAt
	}
	if uint(res.Item.Seq) == res.Item.ID {
		f.t.Fatalf("测试前提：数据库编号应与板内序号错开: id=%d seq=%d", res.Item.ID, res.Item.Seq)
	}
	return res.Item
}

// scanAt 以「base 之后 d」为现在扫一遍，返回发出的提醒条数。
func (f *remindFix) scanAt(d time.Duration) int {
	f.t.Helper()
	r := f.g.srv.boardRemind
	r.now = func() time.Time { return f.base.Add(d) }
	return r.scan(f.ctx)
}

func (f *remindFix) stalePayloads() []notify.Payload {
	f.t.Helper()
	var out []notify.Payload
	for _, p := range f.g.notifies() {
		if p.Kind == notify.KindBoardStale {
			out = append(out, p)
		}
	}
	return out
}

func (f *remindFix) setPrefs(body map[string]any) {
	f.t.Helper()
	f.g.setPrefs(body)
	drainSSE(f.g.events) // 偏好更新自己会广播一条 notify_prefs_updated，与本测试无关
}

// 整条链路：到点才提醒；同一个承接者的合成一条（多个编号），单个指派直达详情；闸的裁决是「给用户的、不穿透」；
// 再扫一遍不重复；ntfy 默认不推。
func TestBoardRemindPublishesThroughGateOnce(t *testing.T) {
	f := newRemindFix(t, "wf-b", "wf-c")
	i1 := f.assign("接口改造", "wf-b")
	i2 := f.assign("文档补全", "wf-b")
	i3 := f.assign("单个指派", "wf-c")

	if n := f.scanAt(time.Hour); n != 0 {
		t.Fatalf("才过 1 小时（阈值 2 小时），不该提醒: %d", n)
	}
	if got := f.stalePayloads(); len(got) != 0 {
		t.Fatalf("没到点不该有通知: %+v", got)
	}

	if n := f.scanAt(3 * time.Hour); n != 2 {
		t.Fatalf("wf-b、wf-c 各一条，共 2 条: %d", n)
	}
	got := f.stalePayloads()
	if len(got) != 2 {
		t.Fatalf("浏览器通道应收到 2 条: %+v", got)
	}
	byLink := map[string]notify.Payload{}
	for _, p := range got {
		byLink[p.DeepLink] = p
		if p.Title != "指派久无人接" || p.Conversation != "" || p.Penetrate || !p.Sound || !p.Badge {
			t.Errorf("应是不穿透、不属于任何会话、有声音与角标的通知: %+v", p)
		}
	}
	multi, ok := byLink["/work/wf-b?quick=offered"]
	if !ok || !strings.Contains(multi.Preview, "2 个指派（#"+fmt.Sprint(i1.Seq)+" #"+fmt.Sprint(i2.Seq)+"）派给 wf-b，已超过 2 小时没人接") {
		t.Fatalf("wf-b 的两个指派应合成一条并打开「待接取」筛选: %+v", byLink)
	}
	single, ok := byLink[fmt.Sprintf("/work/wf-c?task=%d", i3.ID)]
	if !ok || !strings.Contains(single.Preview, fmt.Sprintf("#%d「单个指派」派给 wf-c，已超过 2 小时没人接", i3.Seq)) {
		t.Fatalf("wf-c 的单个指派应直达它的详情: %+v", byLink)
	}

	// 再扫一遍、很久以后再扫：都不重复。
	for _, d := range []time.Duration{3 * time.Hour, 100 * time.Hour} {
		if n := f.scanAt(d); n != 0 {
			t.Fatalf("同一个指派只提醒一次（%v）: %d", d, n)
		}
	}
	if got := f.stalePayloads(); len(got) != 0 {
		t.Fatalf("没有新通知: %+v", got)
	}
	// ntfy 这一类默认不推送：什么也不该发出去。
	time.Sleep(300 * time.Millisecond)
	if posts := f.g.fake.snapshot(); len(posts) != 0 {
		t.Fatalf("ntfy 默认不推这一类: %+v", posts)
	}
}

// 在 ntfy 设置里打开这一类之后，同一条提醒也会推到手机；标题、优先级、点击地址都按表来。
func TestBoardRemindPushesToNtfyWhenEnabled(t *testing.T) {
	f := newRemindFix(t, "wf-b")
	if _, err := f.g.st.UpdateNtfyConfig(f.ctx, func(c ntfy.Config) ntfy.Config {
		c.PriorityByKind = map[string]int{ntfy.KindBoardStale: 2}
		return c
	}); err != nil {
		t.Fatal(err)
	}
	item := f.assign("接口改造", "wf-b")

	if n := f.scanAt(3 * time.Hour); n != 1 {
		t.Fatalf("应提醒 1 条: %d", n)
	}
	posts := f.g.fake.waitFor(t, 1)
	b := posts[0].Body
	if b["title"] != "指派久无人接" || b["priority"] != float64(2) {
		t.Fatalf("标题 / 优先级不对: %+v", b)
	}
	if msg, _ := b["message"].(string); !strings.Contains(msg, fmt.Sprintf("#%d「接口改造」派给 wf-b", item.Seq)) {
		t.Fatalf("正文不对: %+v", b)
	}
	if click, _ := b["click"].(string); click != fmt.Sprintf("https://rm.example.com/work/wf-b?task=%d", item.ID) {
		t.Fatalf("点击应直达任务: %+v", b)
	}
}

// 免打扰、关掉开关：这一遍什么都不做，也不打标记；解除后的下一遍把还没人接的补上，不会被悄悄吞掉。
func TestBoardRemindDeferredByDNDAndSwitch(t *testing.T) {
	f := newRemindFix(t, "wf-b")
	f.assign("接口改造", "wf-b")

	f.setPrefs(map[string]any{"dnd": true})
	if n := f.scanAt(3 * time.Hour); n != 0 {
		t.Fatalf("免打扰时不提醒: %d", n)
	}
	f.setPrefs(map[string]any{"dnd": false, "board_stale_off": true})
	if n := f.scanAt(3 * time.Hour); n != 0 {
		t.Fatalf("提醒开关关着时不提醒: %d", n)
	}
	if got := f.stalePayloads(); len(got) != 0 {
		t.Fatalf("推迟期间不该有通知: %+v", got)
	}

	f.setPrefs(map[string]any{"board_stale_off": false})
	if n := f.scanAt(5 * time.Hour); n != 1 {
		t.Fatalf("解除后应把推迟的补上: %d", n)
	}
	if got := f.stalePayloads(); len(got) != 1 || !strings.Contains(got[0].Preview, "已超过 2 小时没人接，最久已等 5 小时") {
		t.Fatalf("补发的那条应写明最久已等多久（等了两倍阈值以上）: %+v", got)
	}
	if n := f.scanAt(6 * time.Hour); n != 0 {
		t.Fatalf("补过之后不再提醒: %d", n)
	}
}

// 阈值设置：改成 4 小时后，3 小时时不提醒、5 小时时提醒；文案里写的是设置的阈值；改回默认后恢复 2 小时。
func TestBoardRemindFollowsThresholdSetting(t *testing.T) {
	f := newRemindFix(t, "wf-b", "wf-c")
	f.assign("甲", "wf-b")

	f.setPrefs(map[string]any{"board_stale_minutes": 240})
	if n := f.scanAt(3 * time.Hour); n != 0 {
		t.Fatalf("阈值 4 小时，3 小时时不该提醒: %d", n)
	}
	if n := f.scanAt(5 * time.Hour); n != 1 {
		t.Fatalf("5 小时时应提醒: %d", n)
	}
	if got := f.stalePayloads(); len(got) != 1 || !strings.Contains(got[0].Preview, "已超过 4 小时没人接") {
		t.Fatalf("文案应写设置的阈值: %+v", got)
	}

	// 90 分钟写成分钟；0 恢复默认。
	f.assign("乙", "wf-c")
	f.setPrefs(map[string]any{"board_stale_minutes": 90})
	if n := f.scanAt(6*time.Hour + 2*time.Hour); n != 1 {
		t.Fatalf("新指派应提醒: %d", n)
	}
	if got := f.stalePayloads(); len(got) != 1 || !strings.Contains(got[0].Preview, "已超过 90 分钟没人接") {
		t.Fatalf("90 分钟应写成分钟: %+v", got)
	}
	f.setPrefs(map[string]any{"board_stale_minutes": 0})
	prefs, _ := f.g.st.GetNotifyPrefs(f.ctx)
	if prefs.BoardStaleMinutes != 0 || prefs.BoardStaleAfterMinutes() != 120 {
		t.Fatalf("0 应恢复默认 120: %+v", prefs)
	}
}

// 一遍最多 5 条，其余留给下一遍；每个承接者仍只提醒一次。
func TestBoardRemindCapsPerScan(t *testing.T) {
	wfs := []string{"wf-1", "wf-2", "wf-3", "wf-4", "wf-5", "wf-6", "wf-7"}
	f := newRemindFix(t, wfs...)
	for _, wf := range wfs {
		f.assign("给 "+wf, wf)
	}
	if n := f.scanAt(3 * time.Hour); n != 5 {
		t.Fatalf("第一遍最多 5 条: %d", n)
	}
	if got := f.stalePayloads(); len(got) != 5 {
		t.Fatalf("应有 5 条通知: %d", len(got))
	}
	if n := f.scanAt(3 * time.Hour); n != 2 {
		t.Fatalf("第二遍补上剩下的 2 条: %d", n)
	}
	if n := f.scanAt(3 * time.Hour); n != 0 {
		t.Fatalf("没有剩余了: %d", n)
	}
}

// 偏好接口：默认值、局部修改、边界、越界拒绝（不悄悄收到边界上）、生效值与出厂默认随响应给出。
func TestNotifyPrefsAPIBoardStale(t *testing.T) {
	srv, st := newNtfyTestServer(t, nil)
	events := listenSSE(t, srv)
	put := func(body any) (int, map[string]any, string) {
		return ntfyCall(t, srv, http.MethodPut, "/api/v1/notify/prefs", body)
	}

	// 默认：不写字段，生效值 120、出厂默认 120。
	code, out, raw := ntfyCall(t, srv, http.MethodGet, "/api/v1/notify/prefs", nil)
	prefs := notifyPrefsOf(t, out)
	if code != http.StatusOK || prefs["board_stale_after_minutes"] != float64(120) || prefs["board_stale_default_minutes"] != float64(120) {
		t.Fatalf("默认应带生效值与出厂默认 120: %d %s", code, raw)
	}
	for _, k := range []string{"board_stale_off", "board_stale_minutes"} {
		if _, has := prefs[k]; has {
			t.Fatalf("没改过的设置不该写进输出（%s）: %s", k, raw)
		}
	}

	// 改阈值：存的是用户选的值，生效值同步；免打扰与预览模式不受影响；广播给其他标签页。
	code, out, raw = put(map[string]any{"board_stale_minutes": 90})
	prefs = notifyPrefsOf(t, out)
	if code != http.StatusOK || prefs["board_stale_minutes"] != float64(90) || prefs["board_stale_after_minutes"] != float64(90) || prefs["dnd"] != false || prefs["ntfy_preview"] != "auto" {
		t.Fatalf("只改阈值: %d %s", code, raw)
	}
	if !sawEvent(drainSSE(events), "notify_prefs_updated") {
		t.Fatal("修改后应广播 notify_prefs_updated")
	}
	// 关提醒：阈值保持。
	code, out, raw = put(map[string]any{"board_stale_off": true})
	prefs = notifyPrefsOf(t, out)
	if code != http.StatusOK || prefs["board_stale_off"] != true || prefs["board_stale_minutes"] != float64(90) {
		t.Fatalf("只关开关时阈值应保持: %d %s", code, raw)
	}
	// 边界合法：30 与 1440；0 恢复默认并从输出里消失。
	for _, m := range []int{30, 1440} {
		if code, out, raw = put(map[string]any{"board_stale_minutes": m}); code != http.StatusOK || notifyPrefsOf(t, out)["board_stale_after_minutes"] != float64(m) {
			t.Fatalf("边界值 %d 应被接受: %d %s", m, code, raw)
		}
	}
	code, out, raw = put(map[string]any{"board_stale_minutes": 0, "board_stale_off": false})
	prefs = notifyPrefsOf(t, out)
	if code != http.StatusOK || prefs["board_stale_after_minutes"] != float64(120) {
		t.Fatalf("0 应恢复默认: %d %s", code, raw)
	}
	for _, k := range []string{"board_stale_off", "board_stale_minutes"} {
		if _, has := prefs[k]; has {
			t.Fatalf("恢复默认后不该再输出 %s: %s", k, raw)
		}
	}

	// 越界、类型不对：400，且不落库、不广播。
	put(map[string]any{"board_stale_minutes": 45})
	drainSSE(events)
	for _, bad := range []any{
		map[string]any{"board_stale_minutes": 29},
		map[string]any{"board_stale_minutes": 1441},
		map[string]any{"board_stale_minutes": -1},
		map[string]any{"board_stale_minutes": 100000},
		map[string]any{"board_stale_minutes": "两小时"},
		map[string]any{"board_stale_minutes": 90.5},
		map[string]any{"board_stale_off": "yes"},
	} {
		if code, _, raw := put(bad); code != http.StatusBadRequest {
			t.Errorf("%v 应返回 400: %d %s", bad, code, raw)
		}
	}
	got, _ := st.GetNotifyPrefs(context.Background())
	if got.BoardStaleMinutes != 45 || got.BoardStaleOff {
		t.Fatalf("被拒绝的请求不应改变偏好: %+v", got)
	}
	if sawEvent(drainSSE(events), "notify_prefs_updated") {
		t.Fatal("被拒绝的请求不应广播")
	}
}

func TestBoardStaleText(t *testing.T) {
	one := store.BoardStaleOffer{Seqs: []int{12}, Title: "接口改造"}
	many := store.BoardStaleOffer{Seqs: []int{3, 5, 9}}
	long := store.BoardStaleOffer{Seqs: []int{1, 2, 3, 4, 5, 6, 7, 8}}
	h2 := 2 * time.Hour
	for _, c := range []struct {
		name    string
		who     string
		o       store.BoardStaleOffer
		minutes int
		waited  time.Duration
		after   time.Duration
		want    string
	}{
		{"单个有标题", "后端组", one, 120, 2*time.Hour + time.Minute, h2, "#12「接口改造」派给 后端组，已超过 2 小时没人接"},
		{"单个没标题（空格）", "后端组", store.BoardStaleOffer{Seqs: []int{12}}, 120, 3 * time.Hour, h2, "#12 派给 后端组，已超过 2 小时没人接"},
		{"多个", "后端组", many, 120, 3 * time.Hour, h2, "3 个指派（#3 #5 #9）派给 后端组，已超过 2 小时没人接"},
		{"多个超过上限写等", "后端组", long, 120, 3 * time.Hour, h2, "8 个指派（#1 #2 #3 #4 #5 #6 等）派给 后端组，已超过 2 小时没人接"},
		{"阈值写分钟", "后端组", one, 90, 100 * time.Minute, 90 * time.Minute, "#12「接口改造」派给 后端组，已超过 90 分钟没人接"},
		{"阈值整小时", "后端组", one, 360, 7 * time.Hour, 6 * time.Hour, "#12「接口改造」派给 后端组，已超过 6 小时没人接"},
		{"等了两倍阈值：补最久已等（小时）", "后端组", one, 120, 5 * time.Hour, h2, "#12「接口改造」派给 后端组，已超过 2 小时没人接，最久已等 5 小时"},
		{"等了两倍阈值：补最久已等（天）", "后端组", many, 120, 75 * time.Hour, h2, "3 个指派（#3 #5 #9）派给 后端组，已超过 2 小时没人接，最久已等 3 天"},
		{"差一点两倍：不补", "后端组", one, 120, 4*time.Hour - time.Second, h2, "#12「接口改造」派给 后端组，已超过 2 小时没人接"},
		{"恰好两倍：补", "后端组", one, 120, 4 * time.Hour, h2, "#12「接口改造」派给 后端组，已超过 2 小时没人接，最久已等 4 小时"},
		{"标题折成一行并截断", "后端组", store.BoardStaleOffer{Seqs: []int{7}, Title: "第一行\n第二行 " + strings.Repeat("长", 40)}, 120, 3 * time.Hour, h2,
			"#7「第一行 第二行 " + strings.Repeat("长", 19) + "…」派给 后端组，已超过 2 小时没人接"},
	} {
		if got := boardStaleText(c.who, c.o, c.minutes, c.waited, c.after); got != c.want {
			t.Errorf("%s：\n得到 %q\n期望 %q", c.name, got, c.want)
		}
	}
	// 正文最终进通知闸（≤120 字）：最长的情形也不能被截掉「派给谁、等了多久」。
	worst := boardStaleText("一个很长很长的会话身份标签", store.BoardStaleOffer{Seqs: []int{99999}, Title: strings.Repeat("长", 60)}, 120, 100*time.Hour, h2)
	if n := len([]rune(worst)); n > 120 {
		t.Errorf("提醒正文超过了通知的 120 字上限: %d %q", n, worst)
	}
}

// 实验里把「一分钟」缩短（RELAYMESH_BOARD_STALE_UNIT_MS）之后：阈值、扫描间隔和文案里的「已等多久」都按同一个尺度换算，
// 写出来的数字和设置里的分钟数对得上，不会出现「已超过 2 小时没人接，最久已等 不到 1 分钟」。
func TestBoardRemindUnitScalesWaitText(t *testing.T) {
	f := newRemindFix(t, "wf-b")
	f.assign("接口改造", "wf-b")
	r := f.g.srv.boardRemind
	r.unit = 100 * time.Millisecond // 一「分钟」= 100 毫秒：2 小时 = 120「分钟」= 12 秒

	if n := f.scanAt(11 * time.Second); n != 0 {
		t.Fatalf("才过 11 秒（阈值 12 秒），不该提醒: %d", n)
	}
	// 等了 30 秒 = 300「分钟」= 5 小时，超过两倍阈值：补「最久已等 5 小时」
	if n := f.scanAt(30 * time.Second); n != 1 {
		t.Fatalf("到点应提醒 1 条: %d", n)
	}
	got := f.stalePayloads()
	if len(got) != 1 || !strings.Contains(got[0].Preview, "已超过 2 小时没人接，最久已等 5 小时") {
		t.Fatalf("文案里的已等时间应按缩短后的分钟换算: %+v", got)
	}
}

func TestBoardReminderInMinutes(t *testing.T) {
	real := &boardReminder{unit: time.Minute}
	if got := real.inMinutes(90 * time.Minute); got != 90*time.Minute {
		t.Errorf("生产里（一分钟就是一分钟）原样返回: %v", got)
	}
	if got := (&boardReminder{}).inMinutes(7 * time.Second); got != 7*time.Second {
		t.Errorf("没有设置 unit 时原样返回: %v", got)
	}
	fast := &boardReminder{unit: 100 * time.Millisecond}
	if got := fast.inMinutes(6 * time.Second); got != 60*time.Minute {
		t.Errorf("100 毫秒一「分钟」：6 秒 = 60 分钟: %v", got)
	}
	if got := fast.inMinutes(0); got != 0 {
		t.Errorf("0 还是 0: %v", got)
	}
}

func TestBoardStaleHelpers(t *testing.T) {
	// 深链：单个直达详情，多个打开筛选；工作流编号里的特殊字符要转义。
	// ?task= 是数据库编号（这里 ID=40），不是文案里的 #N（Seqs=4）。
	if got := boardStaleLink(store.BoardStaleOffer{AssigneeWorkflowID: "wf-b", Seqs: []int{4}, ID: 40}); got != "/work/wf-b?task=40" {
		t.Errorf("单个深链: %q", got)
	}
	// 万一单个指派没带数据库编号：宁可打开「待接取」筛选，也不拿 #N 去冒充数据库编号
	if got := boardStaleLink(store.BoardStaleOffer{AssigneeWorkflowID: "wf-b", Seqs: []int{4}}); got != "/work/wf-b?quick=offered" {
		t.Errorf("缺数据库编号时的深链: %q", got)
	}
	if got := boardStaleLink(store.BoardStaleOffer{AssigneeWorkflowID: "wf b/中", Seqs: []int{4, 5}}); got != "/work/wf%20b%2F%E4%B8%AD?quick=offered" {
		t.Errorf("多个深链: %q", got)
	}
	// 幂等键：同样的输入同样的键；承接者 / 编号 / 时刻任何一个不同就是另一个键。
	now := time.Unix(1_700_000_000, 5)
	base := store.BoardStaleOffer{BoardKey: "w:x", AssigneeWorkflowID: "wf-b", Seqs: []int{1, 2}}
	id := boardStaleEventID(base, now)
	if !strings.HasPrefix(id, "bst:") || len(id) != len("bst:")+20 || boardStaleEventID(base, now) != id {
		t.Errorf("幂等键格式 / 稳定性不对: %q", id)
	}
	for name, other := range map[string]string{
		"承接者": boardStaleEventID(store.BoardStaleOffer{BoardKey: "w:x", AssigneeWorkflowID: "wf-c", Seqs: []int{1, 2}}, now),
		"编号":  boardStaleEventID(store.BoardStaleOffer{BoardKey: "w:x", AssigneeWorkflowID: "wf-b", Seqs: []int{1, 3}}, now),
		"任务板": boardStaleEventID(store.BoardStaleOffer{BoardKey: "w:y", AssigneeWorkflowID: "wf-b", Seqs: []int{1, 2}}, now),
		"时刻":  boardStaleEventID(base, now.Add(time.Nanosecond)),
	} {
		if other == id {
			t.Errorf("%s不同时幂等键应不同", name)
		}
	}
	// 等了多久的写法。
	for d, want := range map[time.Duration]string{
		30 * time.Second: "不到 1 分钟", 59 * time.Minute: "59 分钟", time.Hour: "1 小时",
		47 * time.Hour: "47 小时", 48 * time.Hour: "2 天", 80 * time.Hour: "3 天",
	} {
		if got := boardWaitText(d); got != want {
			t.Errorf("boardWaitText(%v) = %q，期望 %q", d, got, want)
		}
	}
	for m, want := range map[int]string{30: "30 分钟", 60: "1 小时", 90: "90 分钟", 120: "2 小时", 1440: "24 小时"} {
		if got := boardThresholdText(m); got != want {
			t.Errorf("boardThresholdText(%d) = %q，期望 %q", m, got, want)
		}
	}
}

// 后台协程：实验用的时间单位（1 分钟缩成几毫秒）下到点就提醒、只提醒一次；停下后不再扫描；启停可重复；内部出错不带走进程。
func TestBoardReminderLoop(t *testing.T) {
	f := newRemindFix(t, "wf-b")
	// 单位 10ms：默认阈值 120 分钟 = 1.2 秒；扫描间隔取下限 20ms。
	r := newBoardReminder(f.g.st, f.g.srv.boardRemind.notifier, 10)
	if r.unit != 10*time.Millisecond {
		t.Fatalf("单位应是 10ms: %v", r.unit)
	}
	f.assign("接口改造", "wf-b")
	r.start()
	r.start() // 重复调用无副作用
	t.Cleanup(r.stop)

	time.Sleep(300 * time.Millisecond)
	if got := f.stalePayloads(); len(got) != 0 {
		t.Fatalf("还没到阈值（1.2 秒），不该提醒: %+v", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	var got []notify.Payload
	for len(got) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		got = f.stalePayloads()
	}
	if len(got) != 1 || !strings.Contains(got[0].Preview, "接口改造") {
		t.Fatalf("到点后应收到 1 条提醒: %+v", got)
	}
	time.Sleep(300 * time.Millisecond)
	if again := f.stalePayloads(); len(again) != 0 {
		t.Fatalf("只该提醒一次: %+v", again)
	}

	// 停下后，新到点的指派不再被扫描；停两次也没事。
	r.stop()
	r.stop()
	f.assign("停下之后", "wf-b")
	time.Sleep(1600 * time.Millisecond)
	if again := f.stalePayloads(); len(again) != 0 {
		t.Fatalf("停下后不该再提醒: %+v", again)
	}

	// 空对象与缺依赖都是安全的空操作。
	(*boardReminder)(nil).start()
	(*boardReminder)(nil).stop()
	newBoardReminder(nil, nil, 0).start()
}

// stop() 恰好落在一遍扫描中间时，这一遍照常做完：认领是写事务，被取消的事务会连带丢掉它的数据库连接——
// 文件库无所谓，内存库整个丢失，停下之后别的操作就报 no such table（TestBoardReminderLoop 曾因此偶发失败，约每三四次整包测试一次）。
func TestBoardReminderScanIsNotAbortedByStop(t *testing.T) {
	f := newRemindFix(t, "wf-b")
	f.assign("接口改造", "wf-b")
	r := newBoardReminder(f.g.st, f.g.srv.boardRemind.notifier, 0)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 「现在」取三小时之后（越过默认的 2 小时阈值），并在取到「现在」的这一刻——认领之前——取消父上下文，模拟 stop() 落在扫描中间。
	r.now = func() time.Time {
		cancel()
		return f.base.Add(3 * time.Hour)
	}
	r.safeScan(parent)

	deadline := time.Now().Add(5 * time.Second)
	var got []notify.Payload
	for len(got) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		got = f.stalePayloads()
	}
	if len(got) != 1 || !strings.Contains(got[0].Preview, "接口改造") {
		t.Fatalf("父上下文在扫描中被取消，这一遍也该做完并发出 1 条提醒: %+v", got)
	}
	// 数据库还在：没有因为取消而丢掉连接（内存库丢了会报 no such table）。
	if _, err := f.g.st.ListWorkItems(f.ctx, store.WorkItemFilter{WorkflowID: "wf-a"}); err != nil {
		t.Fatalf("扫描之后数据库应仍然可用: %v", err)
	}
}

func TestBoardReminderSurvivesPanics(t *testing.T) {
	f := newRemindFix(t, "wf-b")
	r := f.g.srv.boardRemind
	r.now = func() time.Time { panic("boom") }
	r.safeScan(f.ctx) // 不该 panic
	if r.unit != time.Minute {
		t.Fatalf("没配置实验单位时应是真实的一分钟: %v", r.unit)
	}
}
