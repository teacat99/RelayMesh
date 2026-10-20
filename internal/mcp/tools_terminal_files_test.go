package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
)

// fakeFileExecutor 在 fakeExecutor 之上加上文件能力，记录 MCP 层交给 Hub 的参数。
type fakeFileExecutor struct {
	fakeExecutor
	fmu     sync.Mutex
	files   []termexec.FileRequest
	fcaller []termexec.Caller
	fileRes any
	fileErr error
}

func (f *fakeFileExecutor) Files(_ context.Context, c termexec.Caller, r termexec.FileRequest) (any, error) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.fcaller = append(f.fcaller, c)
	f.files = append(f.files, r)
	return f.fileRes, f.fileErr
}

func newFilesCredential(t *testing.T, srv *Server, name string, perms model.Permissions) *model.MCPCredential {
	t.Helper()
	c := &model.MCPCredential{Name: name, Permissions: perms, TerminalLimits: model.TerminalLimits{AllowedPaths: []string{"/srv/data"}}}
	if err := srv.store.CreateCredential(context.Background(), c); err != nil {
		t.Fatalf("创建令牌 %s: %v", name, err)
	}
	return c
}

func TestTerminalFilesToolVisibility(t *testing.T) {
	srv := setupTestMCPServer(t)
	relay := newTerminalCredential(t, srv, "relay", model.Permissions{Feedback: true, Sessions: true})
	exec := newTerminalCredential(t, srv, "exec", model.Permissions{Terminal: true})
	filesOnly := newFilesCredential(t, srv, "files-only", model.Permissions{TerminalFiles: true})
	execFiles := newFilesCredential(t, srv, "exec-files", model.Permissions{Terminal: true, TerminalFiles: true})
	compatFiles := newFilesCredential(t, srv, "compat-files", model.Permissions{Terminal: true, TerminalCompat: true, TerminalFiles: true})
	hybridFiles := newFilesCredential(t, srv, "hybrid-files", model.Permissions{Feedback: true, Sessions: true, TerminalFiles: true})

	for name, tok := range map[string]string{"RelayMesh 档": relay.Token, "仅执行": exec.Token} {
		if toolNames(t, srv, tok)["terminal_files"] != nil {
			t.Fatalf("%s不应看到 terminal_files", name)
		}
	}

	// 只带文件权限：除了 terminal_files，还有只读的 terminal_targets（模型靠它得知主机与允许目录），但没有执行类工具。
	if got := keys(toolNames(t, srv, filesOnly.Token)); strings.Join(got, ",") != "terminal_files,terminal_targets" {
		t.Fatalf("只带文件权限的令牌应恰好有 terminal_files 与 terminal_targets: %v", got)
	}
	if desc, _ := toolNames(t, srv, filesOnly.Token)["terminal_targets"]["description"].(string); !strings.Contains(desc, "This token cannot run commands") || !strings.Contains(desc, "allowed_paths") {
		t.Fatalf("文件档的 terminal_targets 说明应讲清不能执行命令、并提到允许目录: %q", desc)
	}
	if desc, _ := toolNames(t, srv, execFiles.Token)["terminal_targets"]["description"].(string); strings.Contains(desc, "This token cannot run commands") {
		t.Fatalf("有执行权限的令牌应看到普通的 terminal_targets 说明: %q", desc)
	}
	if got := keys(toolNames(t, srv, execFiles.Token)); strings.Join(got, ",") != "terminal_exec,terminal_files,terminal_run,terminal_targets" {
		t.Fatalf("执行 + 文件: %v", got)
	}
	ct := toolNames(t, srv, compatFiles.Token)
	if ct["terminal_files"] == nil || ct["exec"] == nil || ct["terminal_exec"] != nil {
		t.Fatalf("兼容命名 + 文件: %v", keys(ct))
	}
	ht := toolNames(t, srv, hybridFiles.Token)
	if ht["terminal_files"] == nil || ht["interactive_feedback"] == nil || ht["terminal_exec"] != nil {
		t.Fatalf("混合档 + 仅文件: %v", keys(ht))
	}

	tf := toolNames(t, srv, filesOnly.Token)["terminal_files"]
	if got := strings.Join(requiredOf(tf), ","); got != "action,path" {
		t.Fatalf("terminal_files.required = %s", got)
	}
	schema, _ := tf["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	for _, p := range []string{"action", "target", "path", "url", "sha256", "overwrite"} {
		if props[p] == nil {
			t.Fatalf("terminal_files 缺少参数 %s", p)
		}
	}
	action, _ := props["action"].(map[string]any)
	if enum, _ := action["enum"].([]any); len(enum) != 4 {
		t.Fatalf("action 枚举 = %v", action["enum"])
	}
	if desc, _ := tf["description"].(string); !strings.Contains(desc, "allowed_paths") || !strings.Contains(desc, "one-time URL") || !strings.Contains(desc, "whole_fs") {
		t.Fatalf("描述应说明范围（含整个文件系统的例外）与一次性地址: %q", desc)
	}
	if props["path"] == nil || !strings.Contains(props["path"].(map[string]any)["description"].(string), "whole_fs") {
		t.Fatalf("path 参数的说明应提到 limits.whole_fs 的例外: %v", props["path"])
	}
}

// 兼容命名的 target_info：整个文件系统令牌的限额回显里有 whole_fs，且不列出被保留着的目录设置。
func TestCompatTargetDetailEchoesWholeFS(t *testing.T) {
	res := &termexec.TargetsResult{
		Targets: []termexec.Target{{MachineCode: "RM-AAAA-BBBB-CCCC-DDDD", Name: "web"}},
		Limits:  &termexec.Limits{WholeFS: true},
	}
	out, err := compatTargetDetail(res)
	if err != nil {
		t.Fatal(err)
	}
	d := out.(compatTargetDetailOut)
	if !d.Target.Limits.WholeFS || len(d.Target.Limits.AllowedPaths) != 0 {
		t.Fatalf("limits = %+v", d.Target.Limits)
	}
	res.Limits = &termexec.Limits{AllowedPaths: []string{"/srv/data"}}
	out, _ = compatTargetDetail(res)
	if d := out.(compatTargetDetailOut); d.Target.Limits.WholeFS || len(d.Target.Limits.AllowedPaths) != 1 {
		t.Fatalf("目录令牌的回显 = %+v", d.Target.Limits)
	}
}

// 没有文件权限的令牌即使知道工具名也调不了（tools/list 与 tools/call 由同一个权限位决定）。
func TestTerminalFilesCallRequiresPermission(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeFileExecutor{fileRes: termexec.FileStatResult{Path: "/srv/data/a"}}
	srv.SetTerminalExecutor(fx)
	exec := newTerminalCredential(t, srv, "exec", model.Permissions{Terminal: true})

	code, resp := rpcFrom(t, srv, exec.Token, "198.51.100.7:4040",
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"terminal_files","arguments":{"action":"stat","path":"/srv/data/a"}}}`)
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Error == nil {
		if res, _ := resp.Result.(map[string]any); res["isError"] != true {
			t.Fatalf("没有文件权限的令牌不应调用成功: %#v", resp.Result)
		}
	}
	if len(fx.files) != 0 {
		t.Fatalf("没有权限时不得到达执行器: %+v", fx.files)
	}
}

func TestTerminalFilesCallReachesExecutorWithParsedArguments(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeFileExecutor{fileRes: termexec.FileStatResult{Target: "web", Path: "/srv/data/a", Exists: true, Type: "file", Size: 3}}
	srv.SetTerminalExecutor(fx)
	cred := newFilesCredential(t, srv, "files", model.Permissions{Terminal: true, TerminalFiles: true})

	text, isErr := callTool(t, srv, cred.Token, "terminal_files",
		`{"action":"push_url","target":"web","path":"/srv/data/a.tgz","url":"https://example.com/a.tgz?sig=1","sha256":"ABC","overwrite":"true"}`)
	if isErr || !strings.Contains(text, `"exists": true`) {
		t.Fatalf("结果 = %v %s", isErr, text)
	}
	got := fx.files[0]
	want := termexec.FileRequest{Action: "push_url", Target: "web", Path: "/srv/data/a.tgz", URL: "https://example.com/a.tgz?sig=1", SHA256: "ABC", Overwrite: true}
	if got != want {
		t.Fatalf("交给执行器的参数 = %+v\n期望 %+v", got, want)
	}
	c := fx.fcaller[0]
	if c.CredentialID != cred.ID || c.CredentialName != "files" || c.TokenString != cred.Token || c.Credential == nil ||
		!c.Permissions.TerminalFiles || len(c.Credential.TerminalLimits.AllowedPaths) != 1 || c.RemoteIP == "" {
		t.Fatalf("Caller = %+v", c)
	}

	// overwrite 的各种写法
	for raw, want := range map[string]bool{`true`: true, `"true"`: true, `1`: true, `false`: false, `"false"`: false, `0`: false, `null`: false} {
		callTool(t, srv, cred.Token, "terminal_files", `{"action":"stat","path":"/srv/data/a","overwrite":`+raw+`}`)
		if r := fx.files[len(fx.files)-1]; r.Overwrite != want {
			t.Fatalf("overwrite=%s 应解析为 %v: %+v", raw, want, r)
		}
	}
	// 无法解析的参数 -> 带稳定错误码的 bad_request，不会到达执行器
	n := len(fx.files)
	text, isErr = callTool(t, srv, cred.Token, "terminal_files", `{"action":"stat","path":"/srv/data/a","overwrite":"maybe"}`)
	if !isErr || !strings.Contains(text, `"code": "bad_request"`) || len(fx.files) != n {
		t.Fatalf("坏参数 = %v %s", isErr, text)
	}
}

// pull_prepare 的下载地址在 Hub 里只是相对路径；MCP 层按本次请求的来源补全成绝对地址。
func TestTerminalFilesPullURLIsAbsolute(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeFileExecutor{fileRes: termexec.FilePullResult{Target: "web", Path: "/srv/data/a", URL: "/files/dl/TICKET", Method: "GET", ExpiresIn: 120, Size: 10}}
	srv.SetTerminalExecutor(fx)
	cred := newFilesCredential(t, srv, "files", model.Permissions{TerminalFiles: true})

	text, isErr := callTool(t, srv, cred.Token, "terminal_files", `{"action":"pull_prepare","target":"web","path":"/srv/data/a"}`)
	if isErr {
		t.Fatalf("pull_prepare: %s", text)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("结果不是 JSON: %v %s", err, text)
	}
	if out.URL != "http://example.com/files/dl/TICKET" {
		t.Fatalf("下载地址应补全为本次请求的来源: %q", out.URL)
	}
}

func TestTerminalFilesErrorsAndAvailability(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred := newFilesCredential(t, srv, "files", model.Permissions{TerminalFiles: true})

	// 没有注入执行器 / 执行器不支持文件能力：明确告知不可用，而不是 panic 或静默。
	for name, ex := range map[string]termexec.Executor{"未注入": nil, "不支持文件": &fakeExecutor{}} {
		srv.SetTerminalExecutor(ex)
		text, isErr := callTool(t, srv, cred.Token, "terminal_files", `{"action":"stat","path":"/srv/data/a"}`)
		if !isErr || !strings.Contains(text, termexec.CodeFilesOff) {
			t.Fatalf("%s: %v %s", name, isErr, text)
		}
	}

	// 业务错误带稳定错误码，并标记 isError。
	fx := &fakeFileExecutor{fileErr: &termexec.Error{Code: termexec.CodePathDenied, Message: "outside"}}
	srv.SetTerminalExecutor(fx)
	text, isErr := callTool(t, srv, cred.Token, "terminal_files", `{"action":"stat","path":"/etc/passwd"}`)
	if !isErr || !strings.Contains(text, `"code": "path_not_allowed"`) {
		t.Fatalf("业务错误 = %v %s", isErr, text)
	}
}

func TestTerminalFilesInstructions(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	filesOnly := srv.InstructionsForPermissions(ctx, model.Permissions{TerminalFiles: true})
	if filesOnly != DefaultTerminalFilesInstructions {
		t.Fatal("只带文件权限的令牌应收到文件专用指引")
	}
	for _, must := range []string{"terminal_files", "pull_prepare", "push_url", "allowed_paths", "auto", "whole_fs"} {
		if !strings.Contains(filesOnly, must) {
			t.Fatalf("文件指引缺少 %q", must)
		}
	}
	for _, mustNot := range []string{"interactive_feedback", "terminal_exec", "terminal_run", "wait_seconds"} {
		if strings.Contains(filesOnly, mustNot) {
			t.Fatalf("文件专用指引不应出现 %q（该令牌没有这些工具）", mustNot)
		}
	}

	// 执行 + 文件：原指引末尾追加文件一节；没有文件权限的令牌指引保持不变。
	execFiles := srv.InstructionsForPermissions(ctx, model.Permissions{Terminal: true, TerminalFiles: true})
	if !strings.HasPrefix(execFiles, DefaultTerminalInstructions) || !strings.Contains(execFiles, "pull_prepare") {
		t.Fatal("执行 + 文件的指引 = 原执行指引 + 文件一节")
	}
	if srv.InstructionsForPermissions(ctx, model.Permissions{Terminal: true}) != DefaultTerminalInstructions {
		t.Fatal("没有文件权限的令牌指引不应变化")
	}
	compat := srv.InstructionsForPermissions(ctx, model.Permissions{Terminal: true, TerminalCompat: true, TerminalFiles: true})
	if !strings.HasPrefix(compat, DefaultTerminalCompatInstructions) || !strings.Contains(compat, "pull_prepare") || strings.Contains(compat, "terminal_exec") || strings.Contains(compat, "terminal_targets") {
		t.Fatalf("兼容命名 + 文件的指引不应出现原生工具名:\n%s", compat)
	}

	// 混合档：治理文本 + (终端一节) + 文件一节；仅文件的混合令牌没有终端执行一节。
	hybrid := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true, Terminal: true, TerminalFiles: true})
	if !strings.Contains(hybrid, "interactive_feedback") || !strings.Contains(hybrid, "terminal_exec") || !strings.Contains(hybrid, "pull_prepare") {
		t.Fatal("混合 + 执行 + 文件的指引应三者兼备")
	}
	hybridFilesOnly := srv.InstructionsForPermissions(ctx, model.Permissions{Feedback: true, TerminalFiles: true})
	if !strings.Contains(hybridFilesOnly, "interactive_feedback") || !strings.Contains(hybridFilesOnly, "pull_prepare") || strings.Contains(hybridFilesOnly, "terminal_exec") {
		t.Fatal("混合 + 仅文件的指引不应出现执行工具")
	}
}

func TestTierForFilesOnlyPermissions(t *testing.T) {
	if got := TierForPermissions(model.Permissions{TerminalFiles: true}); got != TierExecMesh {
		t.Fatalf("仅文件权限的档位 = %s", got)
	}
	if got := TierForPermissions(model.Permissions{Feedback: true, TerminalFiles: true}); got != TierHybrid {
		t.Fatalf("治理 + 文件的档位 = %s", got)
	}
}
