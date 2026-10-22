package hookcli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 契约测试会改进程级 HOME/TMPDIR，必须串行（Go 默认并行跑同包测试）。

const testToken = "test-token-4f9a1c"

const (
	urlSecret    = "rm-url-secret-123456"
	capSecret    = "rmc-cap-secret-123456"
	opaqueSecret = "9f8e7d6c5b4a32100123"
)

const (
	remindMsg = "inbox: 有 1 条追加指令还没读——请先调用 workflow_context(action: \"inbox\", workflow_id: \"wf-1\") 读取全文。"
	stopMsg   = "在你准备结束这一轮之前：用户有 2 条追加指令你还没读到。"
)

func tokenEnv() []string { return []string{"RELAYMESH_TOKEN=" + testToken} }

// isolatedEnv 是每次运行 hook 前要先清掉的环境变量：令牌，以及决定客户端类型 / 版本 / 项目目录的那些。
// 测试可能就跑在 Cursor 的终端或 CLI 里，不清掉的话上报内容会随跑测试的机器而变。
func isolatedEnv() []string {
	return append([]string{"RELAYMESH_TOKEN"}, clientEnvVars...)
}

type recorded struct {
	auth        string
	contentType string
	path        string
	query       string
	body        map[string]any
}

func (r recorded) args() map[string]any {
	params, _ := r.body["params"].(map[string]any)
	args, _ := params["arguments"].(map[string]any)
	return args
}

type fakeMCP struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []recorded
	peek    map[string]any
	status  int
	delay   time.Duration
	raw     string
	isError bool
}

func newFakeMCP(t *testing.T, peek map[string]any) *fakeMCP {
	t.Helper()
	f := &fakeMCP{peek: peek}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMCP) url() string { return f.srv.URL + "/mcp" }

func (f *fakeMCP) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{
		auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"),
		path: r.URL.Path, query: r.URL.RawQuery, body: body,
	})
	status, delay, raw, isErr, peek := f.status, f.delay, f.raw, f.isError, f.peek
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if raw != "" {
		_, _ = w.Write([]byte(raw))
		return
	}
	text, _ := json.Marshal(peek)
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}
	if isErr {
		res["isError"] = true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": res})
}

func (f *fakeMCP) hits() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

type goSandbox struct {
	t       *testing.T
	home    string
	project string
	tmp     string

	spawnMu sync.Mutex
	spawned []reportJob
}

func sanitizeTestName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func hookInstallRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("/tmp", "rm-hook-"+sanitizeTestName(t.Name()))
	if len(root) > 200 {
		root = root[:200]
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func newGoSandbox(t *testing.T) *goSandbox {
	t.Helper()
	root := hookInstallRoot(t)
	s := &goSandbox{
		t: t, home: filepath.Join(root, "home"), project: filepath.Join(root, "home", "proj"), tmp: filepath.Join(root, "tmp"),
	}
	for _, d := range []string{s.home, s.project, s.tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 异步上报的替身：把「交给分离子进程的任务」记下来，不真的起进程
	// （测试二进制不能当 relaymesh-hook 去 exec——它会把整套测试再跑一遍）。真分离见 binary_smoke_test.go。
	oldSpawn := spawnReport
	spawnReport = func(job reportJob) error {
		s.spawnMu.Lock()
		defer s.spawnMu.Unlock()
		s.spawned = append(s.spawned, job)
		return nil
	}
	t.Cleanup(func() { spawnReport = oldSpawn })
	return s
}

// reports 返回目前为止被交给分离子进程的任务（不清空）。
func (s *goSandbox) reports() []reportJob {
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	return append([]reportJob(nil), s.spawned...)
}

// flush 模拟那些子进程：把记下的任务逐个交给 RunReport 发出去并清空记录，返回处理了几个。
func (s *goSandbox) flush(extraEnv []string) int {
	s.t.Helper()
	s.spawnMu.Lock()
	jobs := s.spawned
	s.spawned = nil
	s.spawnMu.Unlock()
	for _, job := range jobs {
		raw, _ := json.Marshal(job)
		s.runReport(raw, extraEnv)
	}
	return len(jobs)
}

// runReport 以子进程的方式执行一个任务（工作目录是临时目录，环境按 extraEnv）。
func (s *goSandbox) runReport(stdin []byte, extraEnv []string) int {
	s.t.Helper()
	goHookTestMu.Lock()
	defer goHookTestMu.Unlock()
	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	_ = os.Chdir(s.tmp)
	defer pushEnv(envPairs(extraEnv, map[string]string{
		"HOME": s.home, "USERPROFILE": s.home,
		"TMPDIR": s.tmp, "TMP": s.tmp, "TEMP": s.tmp,
		"LANG": "C.UTF-8",
	}), isolatedEnv()...)()
	return RunReport(stdin)
}

func (s *goSandbox) writeJSON(path string, v any) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *goSandbox) config(cfg map[string]any) {
	s.writeJSON(filepath.Join(s.project, ".cursor", "relaymesh.json"), cfg)
}

func (s *goSandbox) mcpJSON(servers map[string]any) {
	s.writeJSON(filepath.Join(s.home, ".cursor", "mcp.json"), map[string]any{"mcpServers": servers})
}

type goResult struct {
	out  map[string]any
	raw  string
	took time.Duration
}

func (r goResult) json(t *testing.T) map[string]any {
	t.Helper()
	if r.out != nil {
		return r.out
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.raw)), &m); err != nil {
		t.Fatalf("标准输出不是 JSON: %q (%v)", r.raw, err)
	}
	return m
}

var goHookTestMu sync.Mutex

// envPairs 把 "K=V" 形式的额外环境变量并入基础集合（额外的覆盖基础的）。
func envPairs(extra []string, base map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for _, e := range extra {
		if i := strings.IndexByte(e, '='); i > 0 {
			out[e[:i]] = e[i+1:]
		}
	}
	return out
}

// pushEnv 先清掉 unsets 里的变量、再设置 sets，并返回恢复函数——把每个被动过的变量
// 还原到「原来的值，或原来就没设置」。契约测试必须靠它改进程级环境（HOME / TMPDIR 等），
// 否则这些值会泄漏到同包后面的测试里（曾导致 t.TempDir() 指向已被清理的目录而随机失败）。
func pushEnv(sets map[string]string, unsets ...string) func() {
	type saved struct {
		val string
		ok  bool
	}
	prev := map[string]saved{}
	remember := func(k string) {
		if _, done := prev[k]; done {
			return
		}
		v, ok := os.LookupEnv(k)
		prev[k] = saved{v, ok}
	}
	for _, k := range unsets {
		remember(k)
		_ = os.Unsetenv(k)
	}
	for k, v := range sets {
		remember(k)
		_ = os.Setenv(k, v)
	}
	return func() {
		for k, sv := range prev {
			if sv.ok {
				_ = os.Setenv(k, sv.val)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}
}

func (s *goSandbox) run(dir, stdin string, extraEnv []string, subcmd string) goResult {
	s.t.Helper()
	goHookTestMu.Lock()
	defer goHookTestMu.Unlock()
	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		s.t.Fatal(err)
	}
	defer pushEnv(envPairs(extraEnv, map[string]string{
		"HOME": s.home, "USERPROFILE": s.home,
		"TMPDIR": s.tmp, "TMP": s.tmp, "TEMP": s.tmp,
		"LANG": "C.UTF-8",
	}), isolatedEnv()...)()
	start := time.Now()
	out := RunHook(subcmd, []byte(stdin))
	took := time.Since(start)
	b, _ := json.Marshal(out)
	return goResult{out: out, raw: string(b), took: took}
}

func toolPayload(root string) string {
	b, _ := json.Marshal(map[string]any{"hook_event_name": "postToolUse", "tool_name": "Shell", "workspace_roots": []string{root}})
	return string(b)
}

func stopPayload(root, status string, loop int) string {
	b, _ := json.Marshal(map[string]any{"hook_event_name": "stop", "status": status, "loop_count": loop, "workspace_roots": []string{root}})
	return string(b)
}

func assertNoSecret(t *testing.T, text, what string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Fatalf("%s里不能出现令牌 %q:\n%s", what, secret, text)
		}
	}
}

func itoaJSON(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (s *goSandbox) runStatus(dir string, extraEnv []string) (stdout string, code int) {
	s.t.Helper()
	goHookTestMu.Lock()
	defer goHookTestMu.Unlock()
	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	_ = os.Chdir(dir)
	defer pushEnv(envPairs(extraEnv, map[string]string{
		"HOME": s.home, "USERPROFILE": s.home, "TMPDIR": s.tmp,
	}), "RELAYMESH_TOKEN")()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code = RunStatus(dir, false)
	_ = w.Close()
	os.Stdout = old
	var buf strings.Builder
	_, _ = io.Copy(&buf, r)
	return buf.String(), code
}

func (s *goSandbox) runInstallCLI(dir string, extraEnv []string, opts InstallOptions) (stdout, stderr string, code int) {
	s.t.Helper()
	goHookTestMu.Lock()
	defer goHookTestMu.Unlock()
	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	_ = os.Chdir(dir)
	opts.Project = dir
	defer pushEnv(envPairs(extraEnv, map[string]string{
		"HOME": s.home, "USERPROFILE": s.home,
	}), "RELAYMESH_TOKEN")()
	oldOut, oldErr := os.Stdout, os.Stderr
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	os.Stdout, os.Stderr = ow, ew
	code = RunInstall(opts)
	_ = ow.Close()
	_ = ew.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var so, se strings.Builder
	_, _ = io.Copy(&so, or)
	_, _ = io.Copy(&se, er)
	return so.String(), se.String(), code
}
