package hookcli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 其余契约测试都在进程内直接调用 RunHook；只有这一条真的 go build 出 cmd/relaymesh-hook 再 exec 它，
// 守的是入口这一层：main 能构建、退出码是 0、标准输出是 hook 约定的 JSON（有提醒时带 additional_context）。
func TestBuiltBinaryEmitsReminder(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试跳过：需要 go build")
	}
	s := newGoSandbox(t)

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(s.tmp, "relaymesh-hook")
	build := exec.Command("go", "build", "-o", bin, "./cmd/relaymesh-hook")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 relaymesh-hook: %v\n%s", err, out)
	}

	f := newFakeMCP(t, map[string]any{"remind": true, "unread": 1, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})

	run := exec.Command(bin, "post-tool-use")
	run.Dir = s.project
	run.Env = append([]string{
		"HOME=" + s.home, "USERPROFILE=" + s.home,
		"TMPDIR=" + s.tmp, "TMP=" + s.tmp, "TEMP=" + s.tmp,
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
	}, tokenEnv()...)
	run.Stdin = strings.NewReader(toolPayload(s.project))
	var stderr strings.Builder
	run.Stderr = &stderr
	out, err := run.Output()
	if err != nil {
		t.Fatalf("运行 relaymesh-hook: %v (stderr=%s)", err, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("标准输出不是 JSON: %q (%v)", out, err)
	}
	if doc["additional_context"] != remindMsg || len(doc) != 1 {
		t.Fatalf("应原样转述服务端的提醒: %v", doc)
	}
	if hits := f.hits(); len(hits) != 1 || hits[0].auth != "Bearer "+testToken {
		t.Fatalf("应带着令牌查询一次收件箱: %+v", hits)
	}
}

// buildHookBinary 把 cmd/relaymesh-hook 构建到沙盒的临时目录里。
func buildHookBinary(t *testing.T, s *goSandbox) string {
	t.Helper()
	if testing.Short() {
		t.Skip("短测试跳过：需要 go build")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(s.tmp, "relaymesh-hook")
	build := exec.Command("go", "build", "-o", bin, "./cmd/relaymesh-hook")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 relaymesh-hook: %v\n%s", err, out)
	}
	return bin
}

// runBinary 用干净的环境真的执行 relaymesh-hook；env 只有 HOME / TMPDIR 之类，外加 extraEnv。
func runBinary(t *testing.T, s *goSandbox, bin string, args []string, stdin string, extraEnv []string) (stdout, stderr string, code int, took time.Duration) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = s.project
	cmd.Env = append([]string{
		"HOME=" + s.home, "USERPROFILE=" + s.home,
		"TMPDIR=" + s.tmp, "TMP=" + s.tmp, "TEMP=" + s.tmp,
		"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8",
	}, extraEnv...)
	cmd.Stdin = strings.NewReader(stdin)
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	start := time.Now()
	err := cmd.Run()
	took = time.Since(start)
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("运行 relaymesh-hook %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return so.String(), se.String(), code, took
}

func waitHits(t *testing.T, f *fakeMCP, n int, within time.Duration) []recorded {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if hits := f.hits(); len(hits) >= n {
			return hits
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s 内没等到 %d 次请求，实际 %d", within, n, len(f.hits()))
	return nil
}

// 真的起进程：父进程不等 Hub，立刻返回；分离出去的子进程在父进程退出之后照样把上报送到。
func TestBuiltBinary_AsyncEventReturnsAtOnceAndTheDetachedChildStillDelivers(t *testing.T) {
	s := newGoSandbox(t)
	bin := buildHookBinary(t, s)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	f.mu.Lock()
	f.delay = 1200 * time.Millisecond // 比父进程该等的久得多；请求在到达时就被记下，不管客户端等没等
	f.mu.Unlock()
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})

	stdout, stderr, code, took := runBinary(t, s, bin, []string{"session-start"}, eventPayload(s.project, "sessionStart", nil), tokenEnv())
	if code != 0 || strings.TrimSpace(stdout) != "{}" {
		t.Fatalf("退出码 %d 标准输出 %q 标准错误 %q", code, stdout, stderr)
	}
	t.Logf("父进程耗时 %s（Hub 要 1.2 秒才回）", took)
	if took > 900*time.Millisecond {
		t.Fatalf("父进程不该等 Hub（Hub 要 1.2 秒才回），实际 %s", took)
	}
	if n := len(f.hits()); n > 1 {
		t.Fatalf("不该多于一次: %d", n)
	}
	hits := waitHits(t, f, 1, 5*time.Second)
	args := hits[0].args()
	if hits[0].auth != "Bearer "+testToken || args["agent_event"] != "session_start" || args["source"] != "hook" ||
		args["conversation_id"] != "conv-7" || args["project_directory"] != s.project {
		t.Fatalf("子进程的上报不对: auth=%q args=%v", hits[0].auth, args)
	}
	if _, ok := args["agent_ts"].(float64); !ok {
		t.Fatalf("要带发送方时间: %v", args)
	}
	raw, _ := json.Marshal(hits[0].body)
	for _, secret := range []string{"someone@example.com", "SECRET", "gen-1"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("泄露 %q: %s", secret, raw)
		}
	}
}

func TestBuiltBinary_BeforeSubmitPromptAlwaysSaysContinue(t *testing.T) {
	s := newGoSandbox(t)
	bin := buildHookBinary(t, s)
	// 没有配置、没有令牌：什么都不做，但提示词必须放行。
	stdout, _, code, _ := runBinary(t, s, bin, []string{"before-submit-prompt"}, eventPayload(s.project, "beforeSubmitPrompt", nil), nil)
	if code != 0 || strings.TrimSpace(stdout) != `{"continue":true}` {
		t.Fatalf("退出码 %d 标准输出 %q", code, stdout)
	}
}

func TestBuiltBinary_ReportSubcommandDeliversAJobFromStdin(t *testing.T) {
	s := newGoSandbox(t)
	bin := buildHookBinary(t, s)
	f := newFakeMCP(t, map[string]any{"unread": 0})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url()})
	job, _ := json.Marshal(reportJob{
		ConfigPath: filepath.Join(s.project, ".cursor", "relaymesh.json"),
		Report:     map[string]any{"agent_event": "thought", "conversation_id": "c9", "prompt": "PROMPT-SECRET"},
	})
	_, _, code, _ := runBinary(t, s, bin, []string{"report"}, string(job), tokenEnv())
	if code != 0 {
		t.Fatalf("退出码 %d", code)
	}
	hits := f.hits()
	if len(hits) != 1 || hits[0].args()["agent_event"] != "thought" || hits[0].auth != "Bearer "+testToken {
		t.Fatalf("应带着令牌发出一次上报: %+v", hits)
	}
	if raw, _ := json.Marshal(hits[0].body); strings.Contains(string(raw), "PROMPT-SECRET") {
		t.Fatal("子进程必须再清洗一遍")
	}
	// 坏输入：静默退出，退出码 0。
	for _, stdin := range []string{"", "bad{", `{"config_path":"/nonexistent"}`} {
		if _, _, code, _ := runBinary(t, s, bin, []string{"report"}, stdin, tokenEnv()); code != 0 {
			t.Fatalf("坏输入 %q 也应静默退出: %d", stdin, code)
		}
	}
	if len(f.hits()) != 1 {
		t.Fatalf("坏输入不该发请求: %d", len(f.hits()))
	}
}

func TestBuiltBinary_InstallFlagsPreviewNoStatusAndUninstall(t *testing.T) {
	s := newGoSandbox(t)
	bin := buildHookBinary(t, s)
	hooksPath := filepath.Join(s.project, ".cursor", "hooks.json")
	base := []string{"install", "--project", s.project, "--workflow", "wf-1", "--command", bin}

	out, se, code, _ := runBinary(t, s, bin, append(append([]string{}, base...), "--preview"), "", nil)
	if code != 0 || !strings.Contains(out, "预览结束") || !strings.Contains(out, "+++ ") {
		t.Fatalf("--preview: %d\n%s%s", code, out, se)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatal("--preview 不能写文件")
	}

	if _, se, code, _ := runBinary(t, s, bin, append(append([]string{}, base...), "--no-status"), "", nil); code != 0 {
		t.Fatalf("--no-status: %d %s", code, se)
	}
	if hooks := hooksOf(t, hooksPath); len(hooks) != 2 {
		t.Fatalf("--no-status 只登记两个核心事件: %v", keysOf(hooks))
	}

	out, se, code, _ = runBinary(t, s, bin, append(append([]string{}, base...), "--preview"), "", nil)
	if code != 0 || !strings.Contains(out, "sessionStart") {
		t.Fatalf("已有核心登记时预览应显示补上状态事件: %d\n%s%s", code, out, se)
	}

	if _, se, code, _ := runBinary(t, s, bin, base, "", nil); code != 0 {
		t.Fatalf("install: %d %s", code, se)
	}
	if got := countOurs(hooksOf(t, hooksPath)); got != len(eventSpecs) {
		t.Fatalf("应登记 %d 条: %d", len(eventSpecs), got)
	}
	out, _, code, _ = runBinary(t, s, bin, []string{"uninstall", "--project", s.project}, "", nil)
	if code != 0 || !strings.Contains(out, "已移除 10 条") {
		t.Fatalf("uninstall: %d %s", code, out)
	}
	if usage, _, code, _ := runBinary(t, s, bin, nil, "", nil); code != 2 || usage != "" {
		t.Fatalf("无参数应返回 2: %d", code)
	}
}
