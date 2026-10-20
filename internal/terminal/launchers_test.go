package terminal

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 终端启动器预设：登录即可读（随终端偏好一起返回），修改与删除要求刚刚解锁、写审计（不含命令内容）。

const testLauncherHost = "RM-7K2F-9XQ4-M3WD-8ABC"

func launcherBody(name, command string) map[string]string {
	return map[string]string{"name": name, "host": testLauncherHost, "dir": "/home/user/RelayMesh", "command": command}
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
		lim["launcher_run_bin_max"] != float64(store.MaxHeadlessBinRunes) || lim["launcher_run_prompt_default"] != launcherDefaultPrompt {
		t.Fatalf("应带非交互续聊的取值范围与默认提示词: %v", lim)
	}
	// 「最长运行时间」的上限（#141）：界面据此显示输入框与范围提示；旧版 Hub 没有它。
	if lim["launcher_run_max_minutes"] != float64(store.MaxHeadlessRunMinutes) {
		t.Fatalf("应带预设最长运行时间的上限: %v", lim["launcher_run_max_minutes"])
	}
}

func TestLauncherSaveNeedsUnlockAndNeverAuditsTheCommand(t *testing.T) {
	e := newTestEnv(t)
	login := []string{"Authorization", humanAuth(testUser)}
	path := "/api/v1/terminal/launchers/l-1"

	if res := e.call("PUT", path, launcherBody("Cursor CLI", "agent")); res.Status != http.StatusUnauthorized && res.Status != http.StatusForbidden {
		t.Fatalf("未登录不应能保存: %d %s", res.Status, res.Raw)
	}
	if res := e.call("PUT", path, launcherBody("Cursor CLI", "agent"), login...); res.Status != http.StatusForbidden || res.str("code") != "unlock_required" {
		t.Fatalf("只有登录、没有解锁不应能保存（预设决定人点下去会运行什么）: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != 0 {
		t.Fatalf("被拒绝的保存不应生效: %v", got)
	}

	hdr := e.admin()
	const secret = "CURSOR_API_KEY=sk-very-secret-token agent"
	res := e.call("PUT", path, launcherBody("Cursor CLI", secret), hdr...)
	if res.Status != 200 {
		t.Fatalf("解锁后应可保存: %d %s", res.Status, res.Raw)
	}
	got := launchersOf(t, e)
	if len(got) != 1 {
		t.Fatalf("应保存了 1 条: %v", got)
	}
	first, _ := got[0].(map[string]any)
	if first["id"] != "l-1" || first["name"] != "Cursor CLI" || first["host"] != testLauncherHost || first["dir"] != "/home/user/RelayMesh" || first["command"] != secret {
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
		if strings.Contains(l.Detail, "sk-very-secret-token") || strings.Contains(l.Detail, "agent") {
			t.Fatalf("审计里不能出现命令内容: %+v", l)
		}
	}

	// 再保存同一个编号：是修改，位置不变。
	if res := e.call("PUT", path, launcherBody("Cursor CLI", "agent --resume"), hdr...); res.Status != 200 {
		t.Fatalf("修改: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != 1 || got[0].(map[string]any)["command"] != "agent --resume" {
		t.Fatalf("应覆盖原来的预设而不是新增: %v", got)
	}
	if logs := e.auditEvents(EvLauncherSave); len(logs) != 2 || logs[0].Detail != "update id=l-1" && logs[1].Detail != "update id=l-1" {
		t.Fatalf("修改应再记一条 update 审计: %+v", logs)
	}
}

func TestLauncherSaveValidatesAndNormalizesHostCode(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	put := func(id string, body map[string]string) apiResult {
		return e.call("PUT", "/api/v1/terminal/launchers/"+id, body, hdr...)
	}

	bad := []struct {
		name string
		id   string
		body map[string]string
		want string
	}{
		{"名字为空", "l-1", map[string]string{"name": " ", "host": testLauncherHost}, "名字"},
		{"主机不对", "l-1", map[string]string{"name": "x", "host": "devhost"}, "主机"},
		{"目录是相对路径", "l-1", map[string]string{"name": "x", "host": testLauncherHost, "dir": "projects"}, "完整路径"},
		{"命令带换行", "l-1", map[string]string{"name": "x", "host": testLauncherHost, "command": "a\nb"}, "一行"},
		{"编号不合法", "a%20b", map[string]string{"name": "x", "host": testLauncherHost}, "编号"},
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
	if res := put("l-2", map[string]string{"name": "宽松写法", "host": loose}); res.Status != 200 {
		t.Fatalf("非规范写法的机器码应被规整后保存: %d %s", res.Status, res.Raw)
	}
	if got := launchersOf(t, e); len(got) != 1 || got[0].(map[string]any)["host"] != testLauncherHost {
		t.Fatalf("应按规范形式保存机器码: %v", got)
	}

	// 重名（不分大小写）被拒绝，并告诉用户撞了谁。
	res := put("l-3", map[string]string{"name": "宽松写法", "host": testLauncherHost})
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
