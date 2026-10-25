package terminal

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/sessionprompt"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 终端启动器预设：登录即可读（随终端偏好一起返回），修改与删除要求刚刚解锁、写审计（不含命令内容）。

const testLauncherHost = "RM-7K2F-9XQ4-M3WD-8ABC"

// launcherBody 是一条最小的「非交互续聊」预设的请求体：交互式「开终端」预设 REST 已经拒绝保存（#245 第一期，见
// TestLauncherSaveRejectsInteractivePresets），所以处理器本身的行为（解锁、审计、校验、数量上限）都用非交互预设来测。
// prompt 给「审计里不出现提示词」这类断言用，空串表示不带。
func launcherBody(name, prompt string) map[string]any {
	run := map[string]any{"workflow_id": "wf-launcher-test"}
	if prompt != "" {
		run["prompt"] = prompt
	}
	return map[string]any{"name": name, "host": testLauncherHost, "dir": "/home/user/RelayMesh", "kind": "headless", "run": run}
}

func launchersOf(t *testing.T, e *testEnv) []any {
	t.Helper()
	p := prefsOf(t, e)
	list, ok := p["launchers"].([]any)
	if !ok {
		t.Fatalf("状态的 prefs 应带 launchers 数组（没有预设时也是空数组）: %v", p)
	}
	return list
}

func TestLaunchersDefaultToEmptyAndStatusCarriesLimits(t *testing.T) {
	e := newTestEnv(t)
	if got := launchersOf(t, e); len(got) != 0 {
		t.Fatalf("默认应没有预设: %v", got)
	}
	lim, _ := prefsOf(t, e)["limits"].(map[string]any)
	if lim["launcher_max"] != float64(store.MaxTerminalLaunchers) || lim["launcher_name_max"] != float64(store.MaxLauncherNameRunes) ||
		lim["launcher_dir_max"] != float64(store.MaxLauncherDirRunes) || lim["launcher_command_max"] != float64(store.MaxLauncherCommandRunes) {
		t.Fatalf("应带预设的取值范围: %v", lim)
	}
	// 非交互续聊预设（#126）：界面靠这几项判断 Hub 认不认识这种预设，并展示默认提示词。
	if lim["launcher_run_prompt_max"] != float64(store.MaxHeadlessPromptRunes) || lim["launcher_run_model_max"] != float64(store.MaxHeadlessModelRunes) ||
		lim["launcher_run_bin_max"] != float64(store.MaxHeadlessBinRunes) || lim["launcher_run_prompt_default"] != sessionprompt.OneLine(sessionprompt.Builtin(sessionprompt.KindResume, true)) {
		t.Fatalf("应带非交互续聊的取值范围与默认提示词: %v", lim)
	}
	// 「最长运行时间」的上限（#141）：界面据此显示输入框与范围提示；旧版 Hub 没有它。
	if lim["launcher_run_max_minutes"] != float64(store.MaxHeadlessRunMinutes) {
		t.Fatalf("应带预设最长运行时间的上限: %v", lim["launcher_run_max_minutes"])
	}
}

func TestLauncherSaveNeedsUnlockAndNeverAuditsThePrompt(t *testing.T) {
	e := newTestEnv(t)
	login := []string{"Authorization", humanAuth(testUser)}
	path := "/api/v1/terminal/launchers/l-1"

	if res := e.call("PUT", path, launcherBody("Cursor CLI", "请继续")); res.Status != http.StatusUnauthorized && res.Status != http.StatusForbidden {
		t.Fatalf("未登录不应能保存: %d %s", res.Status, res.Raw)
	}
	if res := e.call("PUT", path, launcherBody("Cursor CLI", "请继续"), login...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("只有登录、没有解锁不应能保存（预设决定人点下去会运行什么）: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != 0 {
		t.Fatalf("被拒绝的保存不应生效: %v", got)
	}

	hdr := e.admin()
	const secret = "CURSOR_API_KEY=sk-very-secret-token 请继续"
	res := e.call("PUT", path, launcherBody("Cursor CLI", secret), hdr...)
	if res.Status != 200 {
		t.Fatalf("解锁后应可保存: %d %s", res.Status, res.Raw)
	}
	got := launchersOf(t, e)
	if len(got) != 1 {
		t.Fatalf("应保存了 1 条: %v", got)
	}
	first, _ := got[0].(map[string]any)
	run, _ := first["run"].(map[string]any)
	if first["id"] != "l-1" || first["name"] != "Cursor CLI" || first["host"] != testLauncherHost || first["dir"] != "/home/user/RelayMesh" || first["kind"] != "headless" || run["prompt"] != secret {
		t.Fatalf("保存的内容不对: %v", first)
	}
	// 保存的响应本身也带最新的 prefs，界面不必再读一次。
	if p, _ := res.Body["prefs"].(map[string]any); p == nil || len(p["launchers"].([]any)) != 1 {
		t.Fatalf("响应应带最新的 prefs: %s", res.Raw)
	}

	logs := e.auditEvents(EvLauncherSave)
	if len(logs) != 1 || logs[0].Detail != "new id=l-1" || logs[0].MachineCode != testLauncherHost {
		t.Fatalf("应有 1 条新建审计（只记编号与主机）: %+v", logs)
	}
	all, _ := e.store.ListTerminalAudit(t.Context(), 500, 0)
	for _, l := range all {
		if strings.Contains(l.Detail, "sk-very-secret-token") || strings.Contains(l.Detail, "请继续") {
			t.Fatalf("审计里不能出现提示词内容: %+v", l)
		}
	}

	// 再保存同一个编号：是修改，位置不变。
	if res := e.call("PUT", path, launcherBody("Cursor CLI", "接着做"), hdr...); res.Status != 200 {
		t.Fatalf("修改: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != 1 || got[0].(map[string]any)["run"].(map[string]any)["prompt"] != "接着做" {
		t.Fatalf("应覆盖原来的预设而不是新增: %v", got)
	}
	if logs := e.auditEvents(EvLauncherSave); len(logs) != 2 || logs[0].Detail != "update id=l-1" && logs[1].Detail != "update id=l-1" {
		t.Fatalf("修改应再记一条 update 审计: %+v", logs)
	}
}

func TestLauncherSaveValidatesAndNormalizesHostCode(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	put := func(id string, body map[string]any) apiResult {
		return e.call("PUT", "/api/v1/terminal/launchers/"+id, body, hdr...)
	}
	// 非交互续聊预设的最小底稿；extra 里的键覆盖底稿（交互式预设 REST 已拒绝保存，校验与规整都用非交互的来测）。
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": "x", "host": testLauncherHost, "dir": "/home/user/RelayMesh", "kind": "headless", "run": map[string]any{"workflow_id": "wf-launcher-test"}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	bad := []struct {
		name string
		id   string
		body map[string]any
		want string
	}{
		{"名字为空", "l-1", base(map[string]any{"name": " "}), "名字"},
		{"主机不对", "l-1", base(map[string]any{"host": "devhost"}), "主机"},
		{"目录是相对路径", "l-1", base(map[string]any{"dir": "projects"}), "完整路径"},
		{"命令带换行", "l-1", base(map[string]any{"command": "a\nb"}), "一行"},
		{"编号不合法", "a%20b", base(nil), "编号"},
	}
	for _, c := range bad {
		res := put(c.id, c.body)
		if res.Status != http.StatusBadRequest || res.str("code") != "bad_launcher" || !strings.Contains(res.str("error"), c.want) {
			t.Fatalf("%s：应 400 bad_launcher 并说明「%s」: %d %s", c.name, c.want, res.Status, res.Raw)
		}
	}
	if got := launchersOf(t, e); len(got) != 0 {
		t.Fatalf("被拒绝的保存不应生效: %v", got)
	}

	// 机器码写成小写、不带连字符也接受，按规范形式保存。
	loose := strings.ToLower(strings.ReplaceAll(testLauncherHost, "-", ""))
	if res := put("l-2", base(map[string]any{"name": "宽松写法", "host": loose})); res.Status != 200 {
		t.Fatalf("非规范写法的机器码应被规整后保存: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != 1 || got[0].(map[string]any)["host"] != testLauncherHost {
		t.Fatalf("应按规范形式保存机器码: %v", got)
	}

	// 重名（不分大小写）被拒绝，并告诉用户撞了谁。
	res := put("l-3", base(map[string]any{"name": "宽松写法"}))
	if res.Status != http.StatusBadRequest || !strings.Contains(res.str("error"), "已经有一个叫「宽松写法」") {
		t.Fatalf("重名应被拒绝: %d %s", res.Status, res.Raw)
	}
}

func TestLauncherLimitAndDelete(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	login := []string{"Authorization", humanAuth(testUser)}
	for i := 0; i < store.MaxTerminalLaunchers; i++ {
		res := e.call("PUT", fmt.Sprintf("/api/v1/terminal/launchers/n-%d", i), launcherBody(fmt.Sprintf("预设 %d", i), "ls"), hdr...)
		if res.Status != 200 {
			t.Fatalf("第 %d 条应能保存: %d %s", i, res.Status, res.Raw)
		}
	}
	res := e.call("PUT", "/api/v1/terminal/launchers/n-over", launcherBody("超出", "ls"), hdr...)
	if res.Status != http.StatusConflict || res.str("code") != "launcher_limit" {
		t.Fatalf("超过上限应 409 launcher_limit: %d %s", res.Status, res.Raw)
	}
	if res := e.call("PUT", "/api/v1/terminal/launchers/n-3", launcherBody("预设 3", "changed"), hdr...); res.Status != 200 {
		t.Fatalf("到上限后修改已有的应可行: %d %s", res.Status, res.Raw)
	}

	// 删除同样要求解锁。
	if res := e.call("DELETE", "/api/v1/terminal/launchers/n-0", nil, login...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("没有解锁不应能删除: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != store.MaxTerminalLaunchers {
		t.Fatalf("被拒绝的删除不应生效: %d", len(got))
	}
	if res := e.call("DELETE", "/api/v1/terminal/launchers/n-0", nil, hdr...); res.Status != 200 {
		t.Fatalf("解锁后应可删除: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != store.MaxTerminalLaunchers-1 {
		t.Fatalf("应少了一条: %d", len(got))
	}
	logs := e.auditEvents(EvLauncherDelete)
	if len(logs) != 1 || logs[0].Detail != "id=n-0" || logs[0].MachineCode != testLauncherHost {
		t.Fatalf("应有 1 条删除审计: %+v", logs)
	}
	res = e.call("DELETE", "/api/v1/terminal/launchers/n-0", nil, hdr...)
	if res.Status != http.StatusNotFound || res.str("code") != "launcher_not_found" {
		t.Fatalf("重复删除应 404 launcher_not_found: %d %s", res.Status, res.Raw)
	}
	if n := len(e.auditEvents(EvLauncherDelete)); n != 1 {
		t.Fatalf("删除不存在的预设不应再记审计: %d", n)
	}
	// 腾出一个位置后又能新增。
	if res := e.call("PUT", "/api/v1/terminal/launchers/n-over", launcherBody("超出", "ls"), hdr...); res.Status != 200 {
		t.Fatalf("删掉一条后应又能新增: %d %s", res.Status, res.Raw)
	}
}

// 保存预设不改终端偏好（宽度保持、终端限高），改偏好也不动预设。
func TestLaunchersAreIndependentOfOtherPrefs(t *testing.T) {
	e := newTestEnv(t)
	login := []string{"Authorization", humanAuth(testUser)}
	if res := e.call("PUT", "/api/v1/terminal/prefs/display", map[string]int{"fixed_cols": 120}, login...); res.Status != 200 {
		t.Fatalf("设置宽度保持: %d %s", res.Status, res.Raw)
	}
	if res := e.call("PUT", "/api/v1/terminal/launchers/l-1", launcherBody("Cursor CLI", "agent"), e.admin()...); res.Status != 200 {
		t.Fatalf("保存预设: %d %s", res.Status, res.Raw)
	}
	p := prefsOf(t, e)
	if p["fixed_cols"] != float64(120) || len(p["launchers"].([]any)) != 1 {
		t.Fatalf("两类设置应各自保留: %v", p)
	}
	if res := e.call("PUT", "/api/v1/terminal/prefs/display", map[string]int{"fixed_cols": 0}, login...); res.Status != 200 {
		t.Fatalf("关闭宽度保持: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != 1 {
		t.Fatalf("改偏好不应动预设: %v", got)
	}
}

// 交互式「开终端」预设已停用（#245 第一期）：新建与修改都被拒绝，说明与错误码固定、不写审计；非交互预设不受影响。
func TestLauncherSaveRejectsInteractivePresets(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()

	for _, kind := range []string{"", "terminal", " Terminal ", "TERMINAL"} {
		body := map[string]any{"name": "开终端", "host": testLauncherHost, "dir": "/home/user/RelayMesh", "command": "agent"}
		if kind != "" {
			body["kind"] = kind
		}
		res := e.call("PUT", "/api/v1/terminal/launchers/old-1", body, hdr...)
		if res.Status != http.StatusBadRequest || res.str("code") != "launcher_interactive_retired" {
			t.Fatalf("kind=%q：新建交互式预设应 400 launcher_interactive_retired: %d %s", kind, res.Status, res.Raw)
		}
		if msg := res.str("error"); !strings.Contains(msg, "已停用") || !strings.Contains(msg, "非交互续聊") || !strings.Contains(msg, "只能删除") {
			t.Fatalf("kind=%q：应说明已停用、该新建什么、旧预设只能删除: %s", kind, res.Raw)
		}
	}
	if got := launchersOf(t, e); len(got) != 0 {
		t.Fatalf("被拒绝的保存不应生效: %v", got)
	}
	if n := len(e.auditEvents(EvLauncherSave)); n != 0 {
		t.Fatalf("被拒绝的保存不应写审计: %d", n)
	}

	// 拒绝发生在校验之前：连明显不合法的交互式请求也是同一个答复，不会因为别的字段错了换成另一种说法。
	if res := e.call("PUT", "/api/v1/terminal/launchers/old-2", map[string]any{"name": " ", "host": "devhost"}, hdr...); res.str("code") != "launcher_interactive_retired" {
		t.Fatalf("交互式请求不论其余字段都应是 launcher_interactive_retired: %d %s", res.Status, res.Raw)
	}

	// 非交互续聊预设照常新建与修改。
	if res := e.call("PUT", "/api/v1/terminal/launchers/run-1", launcherBody("续聊", ""), hdr...); res.Status != 200 {
		t.Fatalf("非交互预设应照常保存: %d %s", res.Status, res.Raw)
	}
	if res := e.call("PUT", "/api/v1/terminal/launchers/run-1", launcherBody("续聊", "接着做"), hdr...); res.Status != 200 {
		t.Fatalf("非交互预设应照常修改: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != 1 {
		t.Fatalf("应只有那条非交互预设: %v", got)
	}
}

// 旧存档里已有的交互式预设不会因为入口下线而消失：照常读出（旧版本读它也照常），修改被拒绝、原样保留，删除照常（要解锁、写审计）。
// 它们的真正清理是第三期的事（先导出备份、另发确认）。
func TestLegacyInteractivePresetStaysReadableAndOnlyDeletable(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	// 旧存档：REST 已经不收交互式预设了，直接放进 store 当起点。
	legacy := store.TerminalLauncher{ID: "old-1", Name: "旧的开终端", Host: testLauncherHost, Dir: "/home/user/RelayMesh", Command: "agent --resume"}
	if _, _, err := e.store.UpsertTerminalLauncher(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if res := e.call("PUT", "/api/v1/terminal/launchers/run-1", launcherBody("续聊", ""), hdr...); res.Status != 200 {
		t.Fatalf("非交互预设应能与旧预设并存: %d %s", res.Status, res.Raw)
	}

	got := launchersOf(t, e)
	if len(got) != 2 {
		t.Fatalf("旧预设与新预设应并存: %v", got)
	}
	old, _ := got[0].(map[string]any)
	if old["id"] != "old-1" || old["name"] != "旧的开终端" || old["command"] != "agent --resume" || old["kind"] != nil {
		t.Fatalf("旧预设应原样读出（不带 kind、命令保留）: %v", old)
	}

	// 修改旧预设（改名、改命令）被拒绝，原样保留。
	res := e.call("PUT", "/api/v1/terminal/launchers/old-1", map[string]any{"name": "改个名", "host": testLauncherHost, "dir": "/home/user/RelayMesh", "command": "other"}, hdr...)
	if res.Status != http.StatusBadRequest || res.str("code") != "launcher_interactive_retired" {
		t.Fatalf("修改旧的交互式预设应被拒绝: %d %s", res.Status, res.Raw)
	}
	if now, _ := launchersOf(t, e)[0].(map[string]any); now["name"] != "旧的开终端" || now["command"] != "agent --resume" {
		t.Fatalf("被拒绝的修改不应生效: %v", now)
	}

	// 删除照常：要解锁、写审计，只删这一条。
	login := []string{"Authorization", humanAuth(testUser)}
	if res := e.call("DELETE", "/api/v1/terminal/launchers/old-1", nil, login...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("删除旧预设仍要解锁: %d %s", res.Status, res.Raw)
	}
	if res := e.call("DELETE", "/api/v1/terminal/launchers/old-1", nil, hdr...); res.Status != 200 {
		t.Fatalf("解锁后应能删除旧预设: %d %s", res.Status, res.Raw)
	}
	if logs := e.auditEvents(EvLauncherDelete); len(logs) != 1 || logs[0].Detail != "id=old-1" {
		t.Fatalf("应记一条删除审计: %+v", logs)
	}
	if left := launchersOf(t, e); len(left) != 1 || left[0].(map[string]any)["id"] != "run-1" {
		t.Fatalf("只应剩下非交互预设: %v", left)
	}
}
