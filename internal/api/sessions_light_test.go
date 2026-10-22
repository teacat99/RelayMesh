package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestFirstLinePreview(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"空串", "", 10, ""},
		{"单行不截", "hello", 10, "hello"},
		{"取首个非空行", "\n\n  \n## 标题\n第二行", 20, "## 标题"},
		{"首行尾随空白去掉", "标题  \t\r\n正文", 20, "标题"},
		{"恰好等于上限不截", "一二三四五", 5, "一二三四五"},
		{"按字符截而不是按字节", "一二三四五六七", 3, "一二三"},
		{"emoji 不被切断", "😀😀😀😀", 2, "😀😀"},
		{"只有空白", " \n\t\n ", 5, ""},
	}
	for _, c := range cases {
		if got := firstLinePreview(c.in, c.max); got != c.want {
			t.Errorf("%s：firstLinePreview(%q, %d) = %q，想要 %q", c.name, c.in, c.max, got, c.want)
		}
	}
}

// rowsOf 按 plan 造一张内存里的会话表：R = 一轮，N = 一条通知；正文大小用 size 控制。
func rowsOf(plan string, size int) []model.FeedbackSession {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows := make([]model.FeedbackSession, 0, len(plan))
	for i, ch := range plan {
		s := model.FeedbackSession{
			ID:           fmt.Sprintf("r%03d", i),
			WorkflowID:   "wf",
			Summary:      "# 标题" + fmt.Sprint(i) + "\n" + strings.Repeat("字", size),
			ResponseText: "回复" + fmt.Sprint(i) + "\n第二行",
			UserMessages: model.StringArray{"快捷回复"},
			CreatedAt:    base.Add(time.Duration(i) * time.Minute),
		}
		if ch == 'N' {
			s.Kind = model.FeedbackKindNotice
		}
		rows = append(rows, s)
	}
	return rows
}

func TestLightCutoffExplicitTail(t *testing.T) {
	// 下标：0R 1N 2R 3R 4N 5N 6R 7R 8N
	rows := rowsOf("RNRRNNRRN", 10)

	// 最近 3 轮 = 下标 3、6、7；第 4 轮（下标 2）之后的东西全保留：下标 3 起
	if got := lightCutoff(rows, 3); got != 3 {
		t.Fatalf("tail=3 的切点应为 3（第 4 新的一轮在下标 2，其后全部保留），得到 %d", got)
	}
	// 最近 4 轮 = 下标 2、3、6、7；第 5 新的一轮是下标 0，切点 1：下标 1 的通知（0 与 2 之间的间隙）要带正文
	if got := lightCutoff(rows, 4); got != 1 {
		t.Fatalf("tail=4 的切点应为 1（保留的第一轮前面的间隙通知要带正文），得到 %d", got)
	}
	// 轮数不够：整张表原样
	for _, tail := range []int{5, 6, 100} {
		if got := lightCutoff(rows, tail); got != 0 {
			t.Fatalf("tail=%d 不足以切（共 5 轮）：切点应为 0，得到 %d", tail, got)
		}
	}
	// 上限保护：超过 lightTailExplicitMax 按上限
	if got := lightCutoff(rows, lightTailExplicitMax+50); got != 0 {
		t.Fatalf("超大 tail 应按上限处理且这里仍不够切：%d", got)
	}
}

func TestLightCutoffNoticesDoNotCountAsRounds(t *testing.T) {
	// 末尾一长串通知：它们不占轮次，也不能把切点往前推
	plan := strings.Repeat("R", 6) + strings.Repeat("N", 200)
	rows := rowsOf(plan, 10)
	if got := lightCutoff(rows, 2); got != 4 {
		t.Fatalf("tail=2：最近 2 轮是下标 4、5，第 3 新的一轮在下标 3，切点应为 4，得到 %d", got)
	}
	if got := lightCutoff(rows, 0); got != 0 {
		t.Fatalf("自动模式下只有 6 轮（不到最少 %d 轮）：整张表原样，得到切点 %d", lightTailMinRounds, got)
	}
}

func TestLightCutoffAutoBudget(t *testing.T) {
	// 每轮很小：保留 lightTailMaxRounds 轮
	small := rowsOf(strings.Repeat("R", 100), 10)
	cut := lightCutoff(small, 0)
	if kept := len(small) - cut; kept != lightTailMaxRounds {
		t.Fatalf("每轮很小应保留 %d 轮，保留了 %d（切点 %d）", lightTailMaxRounds, kept, cut)
	}

	// 每轮很大（单轮 80 KB，两三轮就超预算）：至少保留 lightTailMinRounds 轮
	big := rowsOf(strings.Repeat("R", 100), 80<<10/3) // 一个汉字 3 字节
	cut = lightCutoff(big, 0)
	if kept := len(big) - cut; kept != lightTailMinRounds {
		t.Fatalf("每轮很大应只保留最少的 %d 轮，保留了 %d（切点 %d）", lightTailMinRounds, kept, cut)
	}

	// 中等：保留的正文字节数不超过预算（加上最少轮数的保底）
	mid := rowsOf(strings.Repeat("R", 100), 6<<10/3) // 每轮约 6 KB
	cut = lightCutoff(mid, 0)
	kept := len(mid) - cut
	if kept < lightTailMinRounds || kept >= lightTailMaxRounds {
		t.Fatalf("中等大小应落在最少与最多轮数之间：保留了 %d 轮", kept)
	}
	bytes := 0
	for i := cut; i < len(mid); i++ {
		bytes += heavyBytes(&mid[i])
	}
	if bytes > lightTailBudgetBytes {
		t.Fatalf("保留的正文 %d 字节超过预算 %d", bytes, lightTailBudgetBytes)
	}
	// 再多留一轮就会超预算
	if more := bytes + heavyBytes(&mid[cut-1]); more <= lightTailBudgetBytes {
		t.Fatalf("还能再多留一轮（%d <= %d），切点不是最优", more, lightTailBudgetBytes)
	}
}

func TestLightenSessionsKeepsTailUntouchedAndShrinksOld(t *testing.T) {
	rows := rowsOf("RRRRNRRNRR", 500)
	orig := make([]model.FeedbackSession, len(rows))
	copy(orig, rows)

	n := lightenSessions(rows, 3)
	cut := lightCutoff(orig, 3)
	if n != cut || cut == 0 {
		t.Fatalf("应轻量化 %d 行（切点），实际 %d", cut, n)
	}
	for i := range rows {
		if i >= cut {
			if !reflect.DeepEqual(rows[i], orig[i]) {
				t.Fatalf("切点之后的行必须原样：下标 %d 被改了", i)
			}
			continue
		}
		r := rows[i]
		if !r.BodyOmitted {
			t.Fatalf("下标 %d 应是轻量行", i)
		}
		if strings.Contains(r.Summary, "\n") || strings.Count(r.Summary, "字") != 0 {
			t.Fatalf("轻量行的 summary 应只剩首行预览：%q", r.Summary)
		}
		if r.ResponseText != "回复"+fmt.Sprint(i) {
			t.Fatalf("轻量行的 response_text 应只剩首行：%q", r.ResponseText)
		}
		if r.UserMessages != nil || r.Images != nil {
			t.Fatalf("轻量行不带 user_messages / images")
		}
		// 非正文字段原样
		if r.ID != orig[i].ID || !r.CreatedAt.Equal(orig[i].CreatedAt) || r.Kind != orig[i].Kind || r.WorkflowID != orig[i].WorkflowID {
			t.Fatalf("轻量行的其它字段必须原样：%+v", r)
		}
	}
}

// 把某个工作流的行按 plan 造进库：R = 一轮（已回复），N = 一条通知。返回按创建顺序排列的会话 id。
func seedLightWorkflow(t *testing.T, srv *Server, wf, plan string) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	for i, ch := range plan {
		summary := fmt.Sprintf("# 第 %d 行\n正文第二行 %s", i, strings.Repeat("长", 200))
		switch ch {
		case 'R':
			s, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
				SessionID: fmt.Sprintf("s-%s-%02d", wf, i), WorkflowID: wf, ProjectDirectory: "/tmp/light", Title: "t", Summary: summary, TimeoutSeconds: 60,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: s.ID, ResponseText: fmt.Sprintf("回复 %d\n第二行", i)}); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, s.ID)
		case 'N':
			n, err := srv.store.CreateNoticeSession(ctx, store.CreateSessionInput{WorkflowID: wf, ProjectDirectory: "/tmp/light", Summary: summary})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, n.ID)
		}
	}
	return ids
}

type sessionsPayload struct {
	Sessions []map[string]any `json:"sessions"`
}

func decodeSessions(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var p sessionsPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("响应不是 {sessions:[…]}：%v %s", err, string(body))
	}
	return p.Sessions
}

func TestWorkflowSessionsLightMode(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	const wf = "wf-light"
	ids := seedLightWorkflow(t, srv, wf, "RRNRRRNRRNRR") // 9 轮 + 3 条通知
	base := "/api/v1/workflows/" + wf + "/sessions"

	// 不带参数：与以前一致——每一行都带完整正文，没有 body_omitted
	full := rawGet(srv, base, auth...)
	if full.Code != http.StatusOK {
		t.Fatalf("整表应 200：%d %s", full.Code, full.Body.String())
	}
	fullRows := decodeSessions(t, full.Body.Bytes())
	if len(fullRows) != len(ids) {
		t.Fatalf("整表应有 %d 行，得到 %d", len(ids), len(fullRows))
	}
	for i, r := range fullRows {
		if _, has := r["body_omitted"]; has {
			t.Fatalf("不带 light 的响应不能出现 body_omitted（下标 %d）", i)
		}
		if !strings.Contains(r["summary"].(string), "正文第二行") {
			t.Fatalf("不带 light 的响应每一行都要带完整 summary（下标 %d）：%v", i, r["summary"])
		}
	}

	// light=1&tail=3：行集合与顺序不变，较早的行是轻量行，最近 3 轮之后（含间隙通知）原样
	light := rawGet(srv, base+"?light=1&tail=3", auth...)
	if light.Code != http.StatusOK {
		t.Fatalf("light 应 200：%d %s", light.Code, light.Body.String())
	}
	if light.Header().Get("ETag") == full.Header().Get("ETag") {
		t.Fatalf("轻量表与整表内容不同，ETag 必须不同")
	}
	lightRows := decodeSessions(t, light.Body.Bytes())
	if len(lightRows) != len(fullRows) {
		t.Fatalf("light 不能改变行集合：%d vs %d", len(lightRows), len(fullRows))
	}
	cut := lightCutoff(rowsOfIDs(fullRows), 3)
	if cut == 0 {
		t.Fatal("测试数据应该能切出轻量行")
	}
	for i := range lightRows {
		if lightRows[i]["session_id"] != fullRows[i]["session_id"] {
			t.Fatalf("行顺序必须不变：下标 %d", i)
		}
		omitted, _ := lightRows[i]["body_omitted"].(bool)
		if i < cut {
			if !omitted {
				t.Fatalf("下标 %d 应是轻量行", i)
			}
			if s := lightRows[i]["summary"].(string); strings.Contains(s, "长") || strings.Contains(s, "\n") || !strings.HasPrefix(s, "# 第 ") {
				t.Fatalf("轻量行 summary 应只剩首行：%q", s)
			}
			if rt := lightRows[i]["response_text"].(string); strings.Contains(rt, "\n") {
				t.Fatalf("轻量行 response_text 应只剩首行：%q", rt)
			}
		} else {
			if omitted {
				t.Fatalf("下标 %d 在尾部，不应是轻量行", i)
			}
			if !reflect.DeepEqual(lightRows[i]["summary"], fullRows[i]["summary"]) || !reflect.DeepEqual(lightRows[i]["response_text"], fullRows[i]["response_text"]) {
				t.Fatalf("尾部的行正文必须与整表一致（下标 %d）", i)
			}
		}
	}
	// 轻量响应明显更小
	if light.Body.Len() >= full.Body.Len() {
		t.Fatalf("轻量表应更小：%d >= %d", light.Body.Len(), full.Body.Len())
	}

	// 条件请求同样适用于轻量表
	hit := rawGet(srv, base+"?light=1&tail=3", append([]string{"If-None-Match", light.Header().Get("ETag")}, auth...)...)
	if hit.Code != http.StatusNotModified {
		t.Fatalf("轻量表命中 ETag 应 304：%d", hit.Code)
	}

	// 参数写法：light=true 也行；light=0 / 缺省 = 整表；tail 非法按自动
	if r := rawGet(srv, base+"?light=true&tail=abc", auth...); r.Code != http.StatusOK {
		t.Fatalf("light=true&tail=abc 应 200：%d", r.Code)
	}
	if r := rawGet(srv, base+"?light=0", auth...); r.Body.String() != full.Body.String() {
		t.Fatal("light=0 应与不带参数逐字节一致")
	}
	// 自动模式：只有 9 轮（不到 10 轮）→ 整张表原样，逐字节与不带参数一致
	if r := rawGet(srv, base+"?light=1", auth...); r.Body.String() != full.Body.String() {
		t.Fatal("轮数不足时 light=1 应与整表逐字节一致")
	}
}

// rowsOfIDs 把解析出来的行还原成只带 Kind 的最小结构，用来复用 lightCutoff 算「期望的切点」。
func rowsOfIDs(rows []map[string]any) []model.FeedbackSession {
	out := make([]model.FeedbackSession, len(rows))
	for i, r := range rows {
		if k, _ := r["kind"].(string); k != "" {
			out[i].Kind = k
		}
	}
	return out
}

func TestWorkflowSessionBodies(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	ids := seedLightWorkflow(t, srv, "wf-bodies", "RRNR")
	other := seedLightWorkflow(t, srv, "wf-other", "R")
	base := "/api/v1/workflows/wf-bodies/sessions/bodies"

	// 取两行：正文与整表里的逐字一致，带 updated_at
	fullRows := decodeSessions(t, rawGet(srv, "/api/v1/workflows/wf-bodies/sessions", auth...).Body.Bytes())
	byID := map[string]map[string]any{}
	for _, r := range fullRows {
		byID[r["session_id"].(string)] = r
	}
	w := rawGet(srv, base+"?ids="+ids[0]+","+ids[2], auth...)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200：%d %s", w.Code, w.Body.String())
	}
	got := decodeSessions(t, w.Body.Bytes())
	if len(got) != 2 {
		t.Fatalf("应返回 2 行：%d", len(got))
	}
	for _, r := range got {
		want := byID[r["session_id"].(string)]
		for _, k := range []string{"summary", "response_text", "user_messages", "images", "updated_at"} {
			if !reflect.DeepEqual(r[k], want[k]) {
				t.Fatalf("正文接口的 %s 必须与整表一致（%v）：%v vs %v", k, r["session_id"], r[k], want[k])
			}
		}
		if _, has := r["status"]; has {
			t.Fatalf("正文接口只回正文相关字段，不该带 status")
		}
	}

	// 条件请求
	if hit := rawGet(srv, base+"?ids="+ids[0]+","+ids[2], append([]string{"If-None-Match", w.Header().Get("ETag")}, auth...)...); hit.Code != http.StatusNotModified {
		t.Fatalf("同一批 id 内容没变应 304：%d", hit.Code)
	}

	// 别的工作流的 id、不存在的 id：略过，不越界
	mixed := decodeSessions(t, rawGet(srv, base+"?ids="+ids[1]+","+other[0]+",nope-1", auth...).Body.Bytes())
	if len(mixed) != 1 || mixed[0]["session_id"] != ids[1] {
		t.Fatalf("只应返回本工作流里存在的 id：%v", mixed)
	}
	// 重复 id 去重
	dup := decodeSessions(t, rawGet(srv, base+"?ids="+ids[0]+","+ids[0]+",%20"+ids[0], auth...).Body.Bytes())
	if len(dup) != 1 {
		t.Fatalf("重复 id 应只返回一行：%d", len(dup))
	}

	// 参数错误
	if r := rawGet(srv, base, auth...); r.Code != http.StatusBadRequest {
		t.Fatalf("缺 ids 应 400：%d", r.Code)
	}
	if r := rawGet(srv, base+"?ids=,,", auth...); r.Code != http.StatusBadRequest {
		t.Fatalf("ids 全是空串应 400：%d", r.Code)
	}
	var many []string
	for i := 0; i <= sessionBodiesMaxIDs; i++ {
		many = append(many, fmt.Sprintf("x-%d", i))
	}
	if r := rawGet(srv, base+"?ids="+strings.Join(many, ","), auth...); r.Code != http.StatusBadRequest {
		t.Fatalf("超过 %d 个 id 应 400：%d", sessionBodiesMaxIDs, r.Code)
	}
	// 恰好上限可以
	if r := rawGet(srv, base+"?ids="+strings.Join(many[:sessionBodiesMaxIDs], ","), auth...); r.Code != http.StatusOK {
		t.Fatalf("恰好 %d 个 id 应 200：%d", sessionBodiesMaxIDs, r.Code)
	}

	// 鉴权
	if r := rawGet(srv, base+"?ids="+ids[0]); r.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401：%d", r.Code)
	}
}
