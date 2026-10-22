package hookcli

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// hook v2 的安装契约：登记全部事件、--no-status、--preview、写入前备份、卸载。

func hooksOf(t *testing.T, path string) map[string]any {
	t.Helper()
	doc := loadJSON(path)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatalf("%s 里没有 hooks", path)
	}
	return hooks
}

func countOurs(hooks map[string]any) int {
	n := 0
	for _, raw := range hooks {
		lst, _ := raw.([]any)
		for _, e := range lst {
			if em, ok := e.(map[string]any); ok && isOurHookCommand(str(em["command"])) {
				n++
			}
		}
	}
	return n
}

func backupsOf(t *testing.T, path string) []string {
	t.Helper()
	m, _ := filepath.Glob(path + backupInfix + "*")
	sort.Strings(m)
	return m
}

func TestInstall_RegistersEveryEvent(t *testing.T) {
	s := newGoSandbox(t)
	bin := fakeBinary(t, s.tmp)
	so, se, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin})
	if code != 0 {
		t.Fatalf("install: %d\n%s%s", code, so, se)
	}
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	hooks := hooksOf(t, hooksPath)
	for _, spec := range eventSpecs {
		lst, _ := hooks[spec.Cursor].([]any)
		if len(lst) != 1 {
			t.Fatalf("%s 应恰好登记一条: %v", spec.Cursor, lst)
		}
		entry := lst[0].(map[string]any)
		if entry["command"] != bin+" "+spec.Sub || entry["timeout"] != float64(5) {
			t.Fatalf("%s 的登记不对: %v", spec.Cursor, entry)
		}
		if _, has := entry["loop_limit"]; has != (spec.Cursor == "stop") {
			t.Fatalf("只有 stop 带 loop_limit: %s %v", spec.Cursor, entry)
		}
	}
	if len(hooks) != len(eventSpecs) {
		t.Fatalf("不该登记别的事件: %v", keysOf(hooks))
	}
	if !strings.Contains(so, "8 个状态事件") || !strings.Contains(so, "不会上报提示词") {
		t.Fatalf("安装输出应说明登记了什么、不上报什么:\n%s", so)
	}
	if len(backupsOf(t, hooksPath)) != 0 {
		t.Fatal("首次创建没有可备份的原文件")
	}
	// 令牌不落盘。
	cfg, _ := os.ReadFile(filepath.Join(s.project, ".cursor", "relaymesh.json"))
	raw, _ := os.ReadFile(hooksPath)
	assertNoSecret(t, string(cfg)+string(raw), "安装产物", testToken, urlSecret)
}

func TestInstall_UpgradesAV1InstallAndBacksItUp(t *testing.T) {
	s := newGoSandbox(t)
	bin := fakeBinary(t, s.tmp)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	v1 := map[string]any{
		"version": 1,
		"hooks": map[string]any{
			"postToolUse": []any{map[string]any{"command": bin + " post-tool-use", "timeout": 5}},
			"stop":        []any{map[string]any{"command": bin + " stop", "timeout": 5, "loop_limit": 1}},
			"preToolUse":  []any{map[string]any{"command": ".cursor/hooks/halt-guard.sh", "matcher": "Shell"}},
		},
	}
	s.writeJSON(hooksPath, v1)
	before, _ := os.ReadFile(hooksPath)

	_, se, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin})
	if code != 0 {
		t.Fatalf("install: %d %s", code, se)
	}
	hooks := hooksOf(t, hooksPath)
	if got := countOurs(hooks); got != len(eventSpecs) {
		t.Fatalf("应有 %d 条我们的登记，实际 %d", len(eventSpecs), got)
	}
	pre, _ := hooks["preToolUse"].([]any)
	if len(pre) != 2 || pre[0].(map[string]any)["command"] != ".cursor/hooks/halt-guard.sh" || pre[0].(map[string]any)["matcher"] != "Shell" {
		t.Fatalf("别人的 preToolUse 登记必须原样保留并排在前面: %v", pre)
	}
	baks := backupsOf(t, hooksPath)
	if len(baks) != 1 {
		t.Fatalf("应备份一份原文件: %v", baks)
	}
	if got, _ := os.ReadFile(baks[0]); !bytes.Equal(got, before) {
		t.Fatal("备份必须逐字节等于改动前的文件")
	}
	// 再装一次：没有变化，也不再备份。
	if _, _, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin}); code != 0 {
		t.Fatal("第二次 install")
	}
	if n := len(backupsOf(t, hooksPath)); n != 1 {
		t.Fatalf("没有变化就不该再备份: %d", n)
	}
}

func TestInstall_NoStatusKeepsOnlyTheInboxEvents(t *testing.T) {
	s := newGoSandbox(t)
	bin := fakeBinary(t, s.tmp)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")

	if _, se, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, NoStatus: true}); code != 0 {
		t.Fatalf("install --no-status: %d %s", code, se)
	}
	hooks := hooksOf(t, hooksPath)
	if len(hooks) != 2 || hooks["postToolUse"] == nil || hooks["stop"] == nil {
		t.Fatalf("只应登记 postToolUse 与 stop: %v", keysOf(hooks))
	}

	// 补上状态事件。
	if _, _, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin}); code != 0 {
		t.Fatal("补装")
	}
	if got := countOurs(hooksOf(t, hooksPath)); got != len(eventSpecs) {
		t.Fatalf("补装后应有 %d 条，实际 %d", len(eventSpecs), got)
	}

	// 用户在 preToolUse 上还有自己的 hook；再用 --no-status 装一次，我们的状态事件摘掉、别人的留着。
	doc := loadJSON(hooksPath)
	pre := doc["hooks"].(map[string]any)["preToolUse"].([]any)
	doc["hooks"].(map[string]any)["preToolUse"] = append([]any{map[string]any{"command": "keep.sh"}}, pre...)
	if err := writeJSONAtomic(hooksPath, doc); err != nil {
		t.Fatal(err)
	}
	if _, _, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, NoStatus: true}); code != 0 {
		t.Fatal("--no-status 复装")
	}
	hooks = hooksOf(t, hooksPath)
	if got := countOurs(hooks); got != 2 {
		t.Fatalf("应只剩 2 条我们的登记，实际 %d: %v", got, keysOf(hooks))
	}
	left, _ := hooks["preToolUse"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["command"] != "keep.sh" {
		t.Fatalf("别人的 preToolUse 要原样保留: %v", left)
	}
}

func TestInstall_PreviewWritesNothingAndShowsTheDiff(t *testing.T) {
	s := newGoSandbox(t)
	bin := fakeBinary(t, s.tmp)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	s.writeJSON(hooksPath, map[string]any{
		"version": 1,
		"hooks": map[string]any{
			"preToolUse":  []any{map[string]any{"command": ".cursor/hooks/halt-guard.sh", "matcher": "Shell"}},
			"postToolUse": []any{map[string]any{"command": "python3 .cursor/hooks/relaymesh-inbox.py post-tool-use"}},
		},
	})
	before, _ := os.ReadFile(hooksPath)

	so, se, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, Preview: true})
	if code != 0 {
		t.Fatalf("preview: %d %s", code, se)
	}
	after, _ := os.ReadFile(hooksPath)
	if !bytes.Equal(before, after) {
		t.Fatal("预览不能改 hooks.json")
	}
	if _, err := os.Stat(filepath.Join(s.project, ".cursor", "relaymesh.json")); !os.IsNotExist(err) {
		t.Fatal("预览不能写 relaymesh.json")
	}
	if n := len(backupsOf(t, hooksPath)); n != 0 {
		t.Fatalf("预览不该产生备份: %d", n)
	}
	for _, want := range []string{"[预演]", "--- " + hooksPath + "（当前）", "+++ " + hooksPath + "（安装后）", "（文件不存在）", "预览结束：没有写入任何文件"} {
		if !strings.Contains(so, want) {
			t.Fatalf("预览输出缺 %q:\n%s", want, so)
		}
	}
	var added, removed []string
	for _, line := range strings.Split(so, "\n") {
		switch {
		case strings.HasPrefix(line, "+ ") && !strings.HasPrefix(line, "+++ "):
			added = append(added, line)
		case strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "--- "):
			removed = append(removed, line)
		}
	}
	joinedAdded := strings.Join(added, "\n")
	for _, spec := range eventSpecs {
		if spec.Core {
			continue
		}
		// 事件键本身可能早就有了（preToolUse 上有别人的登记），但我们的命令一定是新增行。
		if !strings.Contains(joinedAdded, bin+" "+spec.Sub) {
			t.Fatalf("差异里应有新增的 %s:\n%s", spec.Cursor, so)
		}
	}
	if strings.Contains(strings.Join(removed, "\n"), "halt-guard") {
		t.Fatalf("别人的登记不该出现在删除行里:\n%s", so)
	}
	if !strings.Contains(strings.Join(removed, "\n"), "relaymesh-inbox.py") || !strings.Contains(joinedAdded, bin+" post-tool-use") {
		t.Fatalf("Python 旧登记应显示为被 Go 二进制替换:\n%s", so)
	}

	// 预览与 --dry-run 一样不动任何文件；--dry-run 不打印逐行差异。
	so, _, code = s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, DryRun: true})
	if code != 0 || strings.Contains(so, "+++ ") {
		t.Fatalf("dry-run 不打印差异: %d\n%s", code, so)
	}
}

func TestInstall_PreviewOfAnUpToDateInstallSaysSo(t *testing.T) {
	s := newGoSandbox(t)
	bin := fakeBinary(t, s.tmp)
	if _, _, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin}); code != 0 {
		t.Fatal("install")
	}
	so, _, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, Preview: true})
	if code != 0 || !strings.Contains(so, "已经是最新状态") {
		t.Fatalf("%d\n%s", code, so)
	}
}

func TestInstall_KeepsOnlyTheLatestBackups(t *testing.T) {
	s := newGoSandbox(t)
	bin := fakeBinary(t, s.tmp)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	// 来回切换 --no-status，每次都改动 hooks.json，每次改动前都备份。
	for i := 0; i < 7; i++ {
		_, se, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin, NoStatus: i%2 == 0})
		if code != 0 {
			t.Fatalf("第 %d 次 install: %s", i, se)
		}
	}
	baks := backupsOf(t, hooksPath)
	if len(baks) != keepBackups {
		t.Fatalf("只应保留最近 %d 份备份，实际 %d: %v", keepBackups, len(baks), baks)
	}
}

func TestInstall_BacksUpRelaymeshJSONToo(t *testing.T) {
	s := newGoSandbox(t)
	bin := fakeBinary(t, s.tmp)
	cfgPath := filepath.Join(s.project, ".cursor", "relaymesh.json")
	s.config(map[string]any{"workflow_id": "old-wf", "mcp_url": "http://old/mcp"})
	before, _ := os.ReadFile(cfgPath)
	if _, se, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin}); code != 0 {
		t.Fatalf("install: %s", se)
	}
	baks := backupsOf(t, cfgPath)
	if len(baks) != 1 {
		t.Fatalf("relaymesh.json 改动前也要备份: %v", baks)
	}
	if got, _ := os.ReadFile(baks[0]); !bytes.Equal(got, before) {
		t.Fatal("备份内容应是改动前的 relaymesh.json")
	}
	if got := loadJSON(cfgPath); got["workflow_id"] != "wf-1" {
		t.Fatalf("应写入新工作流: %v", got)
	}
}

func TestUninstall_BacksUpAndRemovesEveryOfOurEvents(t *testing.T) {
	s := newGoSandbox(t)
	bin := fakeBinary(t, s.tmp)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	if _, _, code := s.runInstallCLI(s.project, nil, InstallOptions{Workflow: "wf-1", Command: bin}); code != 0 {
		t.Fatal("install")
	}
	doc := loadJSON(hooksPath)
	hooks := doc["hooks"].(map[string]any)
	hooks["preToolUse"] = append([]any{map[string]any{"command": "keep.sh"}}, hooks["preToolUse"].([]any)...)
	hooks["sessionEnd"] = append(hooks["sessionEnd"].([]any), map[string]any{"command": "other-end.sh"})
	if err := writeJSONAtomic(hooksPath, doc); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(hooksPath)

	if code := RunUninstall(s.project, false, false); code != 0 {
		t.Fatal("uninstall")
	}
	hooks = hooksOf(t, hooksPath)
	if got := countOurs(hooks); got != 0 {
		t.Fatalf("应移除全部 %d 条登记，还剩 %d", len(eventSpecs), got)
	}
	if len(hooks) != 2 {
		t.Fatalf("只剩别人的两个事件: %v", keysOf(hooks))
	}
	if pre, _ := hooks["preToolUse"].([]any); len(pre) != 1 || pre[0].(map[string]any)["command"] != "keep.sh" {
		t.Fatalf("别人的 preToolUse 要保留: %v", pre)
	}
	if end, _ := hooks["sessionEnd"].([]any); len(end) != 1 || end[0].(map[string]any)["command"] != "other-end.sh" {
		t.Fatalf("别人的 sessionEnd 要保留: %v", end)
	}
	baks := backupsOf(t, hooksPath)
	if len(baks) == 0 {
		t.Fatal("卸载前也要备份")
	}
	if got, _ := os.ReadFile(baks[len(baks)-1]); !bytes.Equal(got, before) {
		t.Fatal("备份应是卸载前的内容")
	}

	// 没有我们的登记可移除：不动文件、不备份。
	n := len(baks)
	if code := RunUninstall(s.project, false, false); code != 0 {
		t.Fatal("二次 uninstall")
	}
	if len(backupsOf(t, hooksPath)) != n {
		t.Fatal("没有改动就不该再备份")
	}
}

func TestBackupFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	if _, err := backupFile(path, time.Now()); err == nil {
		t.Fatal("原文件不存在应报错")
	}
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 15, 30, 12, 0, time.Local)
	first, err := backupFile(path, now)
	if err != nil || filepath.Base(first) != "hooks.json.bak-20261014-153012" {
		t.Fatalf("备份文件名: %q %v", first, err)
	}
	if st, _ := os.Stat(first); st.Mode().Perm() != 0o600 {
		t.Fatalf("备份应保留原文件权限: %v", st.Mode())
	}
	second, err := backupFile(path, now)
	if err != nil || filepath.Base(second) != "hooks.json.bak-20261014-153012-1" {
		t.Fatalf("同一秒内重名要加序号: %q %v", second, err)
	}
	// 只留最近 keepBackups 份，且删的是最旧的。
	for i := 1; i <= 5; i++ {
		if _, err := backupFile(path, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	baks := backupsOf(t, path)
	if len(baks) != keepBackups {
		t.Fatalf("应留 %d 份: %v", keepBackups, baks)
	}
	if !strings.HasSuffix(baks[len(baks)-1], "153512") {
		t.Fatalf("应保留最新的: %v", baks)
	}
	// 其他文件不受影响。
	other := filepath.Join(dir, "hooks.json.keep")
	_ = os.WriteFile(other, []byte("x"), 0o644)
	pruneBackups(path, 1)
	if _, err := os.Stat(other); err != nil {
		t.Fatal("只能清理自己的备份")
	}
}

func TestLineDiff(t *testing.T) {
	lines := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "line" + itoaJSON(int64(i+1))
		}
		return out
	}
	join := func(l []string) string { return strings.Join(l, "\n") }

	if got := lineDiff(lines(5), lines(5), 2); join(got) != "（没有差异）" {
		t.Fatalf("相同内容: %v", got)
	}
	// 中间插入一行：前后各两行上下文。
	b := append(append(append([]string{}, lines(9)[:4]...), "NEW"), lines(9)[4:]...)
	got := lineDiff(lines(9), b, 2)
	want := "  line3\n  line4\n+ NEW\n  line5\n  line6"
	if join(got) != want {
		t.Fatalf("插入:\n%s\n想要:\n%s", join(got), want)
	}
	// 删除一行。
	var del []string
	for i, l := range lines(9) {
		if i != 4 {
			del = append(del, l)
		}
	}
	got = lineDiff(lines(9), del, 1)
	if join(got) != "  line4\n- line5\n  line6" {
		t.Fatalf("删除: %v", got)
	}
	// 两处相距很远的改动：中间用 @@ 隔开，远处的行不显示。
	a := lines(30)
	c := append([]string{}, a...)
	c[2] = "CHANGED-A"
	c[25] = "CHANGED-B"
	got = lineDiff(a, c, 1)
	text := join(got)
	if strings.Count(text, "@@") != 1 || strings.Contains(text, "line15") ||
		!strings.Contains(text, "- line3\n+ CHANGED-A") || !strings.Contains(text, "- line26\n+ CHANGED-B") {
		t.Fatalf("两处改动:\n%s", text)
	}
	// 新文件：全是新增行。
	got = lineDiff(nil, lines(3), 2)
	if join(got) != "+ line1\n+ line2\n+ line3" {
		t.Fatalf("新文件: %v", got)
	}
	// 太大的输入不做逐行比较。
	big1, big2 := make([]string, 2100), make([]string, 2100)
	for i := range big1 {
		big1[i], big2[i] = "a"+itoaJSON(int64(i)), "b"+itoaJSON(int64(i))
	}
	if got := lineDiff(big1, big2, 2); len(got) != 1 || !strings.Contains(got[0], "略去") {
		t.Fatalf("过大应降级: %v", got)
	}
}

func TestDescribeRegistered(t *testing.T) {
	cases := []struct {
		name        string
		core, extra []string
		want        string
	}{
		{"都没登记", nil, nil, "未登记"},
		{"只有核心", []string{"postToolUse", "stop"}, nil, "已登记 postToolUse、stop；状态事件 0/8（运行 install 可补上）"},
		{"全部", []string{"postToolUse", "stop"}, []string{"sessionStart", "sessionEnd", "beforeSubmitPrompt", "afterAgentThought", "preToolUse", "afterAgentResponse", "postToolUseFailure", "preCompact"}, "已登记 postToolUse、stop；状态事件 8/8"},
		{"缺一部分", []string{"postToolUse", "stop"}, []string{"sessionStart", "preToolUse"}, "已登记 postToolUse、stop；状态事件 2/8（sessionStart、preToolUse；运行 install 可补齐）"},
		{"只剩状态事件", nil, []string{"sessionStart"}, "只登记了状态事件；状态事件 1/8（sessionStart；运行 install 可补齐）"},
	}
	for _, c := range cases {
		if got := describeRegistered(c.core, c.extra); got != c.want {
			t.Fatalf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestStatus_ReportsWhichEventsAreRegistered(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"enabled": true, "hooks_enabled": true, "unread": 0, "delivered": 0})
	bin := fakeBinary(t, s.tmp)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	if _, se, code := s.runInstallCLI(s.project, tokenEnv(), InstallOptions{Workflow: "wf-1", Command: bin, McpURL: f.url(), NoStatus: true}); code != 0 {
		t.Fatalf("install: %s", se)
	}
	out, _ := s.runStatus(s.project, tokenEnv())
	if !strings.Contains(out, "项目级 hooks.json：已登记 postToolUse、stop；状态事件 0/8（运行 install 可补上）") {
		t.Fatalf("只装核心事件时的状态:\n%s", out)
	}
	if _, se, code := s.runInstallCLI(s.project, tokenEnv(), InstallOptions{Workflow: "wf-1", Command: bin, McpURL: f.url()}); code != 0 {
		t.Fatalf("install: %s", se)
	}
	out, _ = s.runStatus(s.project, tokenEnv())
	if !strings.Contains(out, "项目级 hooks.json：已登记 postToolUse、stop；状态事件 8/8") || !strings.Contains(out, "用户级 hooks.json：未登记") {
		t.Fatalf("全装时的状态:\n%s", out)
	}
	doc := loadJSON(hooksPath)
	delete(doc["hooks"].(map[string]any), "preToolUse")
	delete(doc["hooks"].(map[string]any), "preCompact")
	if err := writeJSONAtomic(hooksPath, doc); err != nil {
		t.Fatal(err)
	}
	out, _ = s.runStatus(s.project, tokenEnv())
	if !strings.Contains(out, "状态事件 6/8（sessionStart、sessionEnd、beforeSubmitPrompt、afterAgentThought、afterAgentResponse、postToolUseFailure；运行 install 可补齐）") {
		t.Fatalf("缺两个事件时的状态:\n%s", out)
	}
}

func TestMain_UsageListsEveryEventSubcommand(t *testing.T) {
	text := usageLine()
	for _, spec := range eventSpecs {
		if !strings.Contains(text, spec.Sub) {
			t.Fatalf("用法里应列出 %s:\n%s", spec.Sub, text)
		}
	}
	if code := Main([]string{"relaymesh-hook"}); code != 2 {
		t.Fatalf("缺子命令应返回 2: %d", code)
	}
	if code := Main([]string{"relaymesh-hook", "nope"}); code != 2 {
		t.Fatalf("未知子命令应返回 2: %d", code)
	}
}
