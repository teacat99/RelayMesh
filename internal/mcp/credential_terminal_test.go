package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// rpcFrom 以指定令牌与来源地址发起一次 JSON-RPC 调用，返回 HTTP 状态码与解码后的响应。
func rpcFrom(t *testing.T, srv *Server, token, remoteAddr, body string) (int, jsonRPCResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var resp jsonRPCResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

func initializeInstructions(t *testing.T, srv *Server, token string) string {
	t.Helper()
	code, resp := rpcFrom(t, srv, token, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if code != http.StatusOK || resp.Error != nil {
		t.Fatalf("initialize 失败: %d %#v", code, resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	text, _ := result["instructions"].(string)
	if text == "" {
		t.Fatal("initialize 没有返回 instructions")
	}
	return text
}

func TestAllPermissionsNeverIncludeTerminal(t *testing.T) {
	all := model.AllPermissions()
	if all.Terminal || all.TerminalFiles || all.HasTerminal() {
		t.Fatalf("AllPermissions 不得隐式带终端权限: %#v", all)
	}
	for _, tool := range []string{"terminal_targets", "terminal_exec", "terminal_run", "terminal_files"} {
		if all.AllowsTool(tool) {
			t.Fatalf("AllPermissions 不得放行 %s", tool)
		}
	}
	if !(model.Permissions{Terminal: true}).AllowsTool("terminal_exec") {
		t.Fatal("Terminal 权限应放行 terminal_exec")
	}
	if (model.Permissions{Terminal: true}).AllowsTool("terminal_files") {
		t.Fatal("Terminal 权限不应放行文件类工具")
	}
	if !(model.Permissions{TerminalFiles: true}).AllowsTool("terminal_files") {
		t.Fatal("TerminalFiles 权限应放行 terminal_files")
	}
	if (model.Permissions{Terminal: true}).AllowsTool("interactive_feedback") {
		t.Fatal("纯终端权限不得放行治理类工具")
	}

	// 只有文件权限：可以看主机清单，但不能执行 / 轮询命令。
	filesOnly := model.Permissions{TerminalFiles: true}
	if !filesOnly.AllowsTool("terminal_targets") || filesOnly.AllowsTool("terminal_exec") || filesOnly.AllowsTool("terminal_run") {
		t.Fatal("只有文件权限时应放行 terminal_targets，且不放行 terminal_exec / terminal_run")
	}
	// 执行权限使用 ExecMesh 兼容命名时，原生的 terminal_targets 不出现（用 targets_list）。
	compatFiles := model.Permissions{Terminal: true, TerminalCompat: true, TerminalFiles: true}
	if compatFiles.AllowsTool("terminal_targets") || !compatFiles.AllowsTool("targets_list") {
		t.Fatal("兼容命名下应使用 targets_list，而不是 terminal_targets")
	}
}

func TestTierForPermissions(t *testing.T) {
	cases := []struct {
		name string
		perm model.Permissions
		want string
	}{
		{"全部治理权限", model.AllPermissions(), TierRelayMesh},
		{"只有反馈", model.Permissions{Feedback: true}, TierRelayMesh},
		{"只有自驾上报", model.Permissions{Execute: true}, TierRelayMesh},
		{"只有终端", model.Permissions{Terminal: true}, TierExecMesh},
		{"终端 + 文件", model.Permissions{Terminal: true, TerminalFiles: true}, TierExecMesh},
		{"治理 + 终端", model.Permissions{Feedback: true, Terminal: true}, TierHybrid},
		{"什么都没有", model.Permissions{}, TierNone},
	}
	for _, tc := range cases {
		if got := TierForPermissions(tc.perm); got != tc.want {
			t.Errorf("%s: 档位 %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

// ExecMesh 档令牌只收到终端指引，不带 RelayMesh 治理文本；混合令牌两者都有；默认档不变。
func TestInitializeInstructionsFollowTier(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	mk := func(name string, perms model.Permissions) string {
		c := &model.MCPCredential{Name: name, Permissions: perms}
		if err := srv.store.CreateCredential(ctx, c); err != nil {
			t.Fatalf("创建 %s: %v", name, err)
		}
		return c.Token
	}
	relay := mk("relay-tier", model.AllPermissions())
	exec := mk("exec-tier", model.Permissions{Terminal: true})
	hybrid := mk("hybrid-tier", model.Permissions{Feedback: true, Sessions: true, Terminal: true})

	relayText := initializeInstructions(t, srv, relay)
	if !strings.Contains(relayText, "interactive_feedback") || strings.Contains(relayText, "terminal_exec") {
		t.Fatal("RelayMesh 档应收到治理指引且不含终端章节")
	}

	execText := initializeInstructions(t, srv, exec)
	if !strings.Contains(execText, "terminal_exec") || !strings.Contains(execText, "ExecMesh") {
		t.Fatal("ExecMesh 档应收到终端指引")
	}
	for _, banned := range []string{"interactive_feedback", "agent-modes", "Workflow Sheet", "C-PLAN"} {
		if strings.Contains(execText, banned) {
			t.Fatalf("ExecMesh 档不应收到 RelayMesh 治理文本，发现 %q", banned)
		}
	}

	hybridText := initializeInstructions(t, srv, hybrid)
	if !strings.Contains(hybridText, "interactive_feedback") || !strings.Contains(hybridText, "terminal_exec") ||
		!strings.Contains(hybridText, "workflow_id") {
		t.Fatal("混合令牌应同时收到治理指引与终端章节")
	}
}

func TestCredentialExpiryIsEnforced(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	past := time.Now().Add(-time.Minute)
	expired := &model.MCPCredential{Name: "expired", Permissions: model.AllPermissions(), ExpiresAt: &past}
	if err := srv.store.CreateCredential(ctx, expired); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	valid := &model.MCPCredential{Name: "valid", Permissions: model.AllPermissions(), ExpiresAt: &future}
	if err := srv.store.CreateCredential(ctx, valid); err != nil {
		t.Fatal(err)
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	if code, resp := rpcFrom(t, srv, expired.Token, "", body); code != http.StatusUnauthorized || resp.Error == nil ||
		!strings.Contains(resp.Error.Message, "expired") {
		t.Fatalf("过期令牌必须 401 且说明原因: %d %#v", code, resp.Error)
	}
	if code, resp := rpcFrom(t, srv, valid.Token, "", body); code != http.StatusOK || resp.Error != nil {
		t.Fatalf("未过期令牌应可用: %d %#v", code, resp.Error)
	}
}

func TestCredentialSourceAllowlistIsEnforced(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	cred := &model.MCPCredential{
		Name:         "lan-only",
		Permissions:  model.AllPermissions(),
		AllowedCIDRs: model.StringList{"192.0.2.0/24", "198.51.100.7"},
	}
	if err := srv.store.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

	for _, ok := range []string{"192.0.2.10:5555", "198.51.100.7:1"} {
		if code, resp := rpcFrom(t, srv, cred.Token, ok, body); code != http.StatusOK || resp.Error != nil {
			t.Fatalf("白名单内来源 %s 应放行: %d %#v", ok, code, resp.Error)
		}
	}
	for _, bad := range []string{"203.0.113.9:1", "198.51.100.8:1", "[2001:db8::1]:1"} {
		code, resp := rpcFrom(t, srv, cred.Token, bad, body)
		if code != http.StatusUnauthorized || resp.Error == nil {
			t.Fatalf("白名单外来源 %s 必须 401: %d %#v", bad, code, resp.Error)
		}
		if strings.Contains(resp.Error.Message, "192.0.2") || strings.Contains(resp.Error.Message, "198.51.100") {
			t.Fatalf("拒绝原因不得回显白名单内容: %q", resp.Error.Message)
		}
	}

	// 接入层放入上下文的 IP 优先于直连地址（模拟经可信代理解析后的客户端 IP）。
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+cred.Token)
	req.RemoteAddr = "203.0.113.9:1" // 直连对端不在白名单
	req = req.WithContext(WithClientIP(req.Context(), "192.0.2.77"))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("上下文里的客户端 IP 应优先: %d %s", w.Code, w.Body.String())
	}
}

func TestIPAllowedByRules(t *testing.T) {
	if !ipAllowedByRules("anything", nil) {
		t.Fatal("规则为空表示不限制")
	}
	if ipAllowedByRules("", []string{"10.0.0.0/8"}) || ipAllowedByRules("garbage", []string{"10.0.0.0/8"}) {
		t.Fatal("无法解析的来源必须拒绝（fail-closed）")
	}
	if !ipAllowedByRules("10.1.2.3", []string{"10.0.0.0/8"}) || ipAllowedByRules("11.1.2.3", []string{"10.0.0.0/8"}) {
		t.Fatal("CIDR 判定错误")
	}
	if !ipAllowedByRules("::1", []string{"::1"}) || !ipAllowedByRules("fd00::5", []string{"fd00::/8"}) {
		t.Fatal("IPv6 判定错误")
	}
	if ipAllowedByRules("10.0.0.1", []string{"", "bad-rule"}) {
		t.Fatal("无效规则不得放行任何来源")
	}
}

func TestCredentialLastUsedIsRecordedAndThrottled(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	cred := &model.MCPCredential{Name: "tracked", Permissions: model.AllPermissions()}
	if err := srv.store.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

	if code, _ := rpcFrom(t, srv, cred.Token, "192.0.2.1:1000", body); code != http.StatusOK {
		t.Fatalf("首次调用失败: %d", code)
	}
	first, err := srv.store.GetCredential(ctx, cred.ID)
	if err != nil || first.LastUsedAt == nil || first.LastUsedIP != "192.0.2.1" {
		t.Fatalf("首次调用应记录最近使用: %#v %v", first, err)
	}

	// 一分钟内再次调用（来源不同）不应再写库
	if code, _ := rpcFrom(t, srv, cred.Token, "192.0.2.2:1000", body); code != http.StatusOK {
		t.Fatalf("再次调用失败: %d", code)
	}
	second, _ := srv.store.GetCredential(ctx, cred.ID)
	if !second.LastUsedAt.Equal(*first.LastUsedAt) || second.LastUsedIP != "192.0.2.1" {
		t.Fatalf("1 分钟内不应重复写入: %v %q", second.LastUsedAt, second.LastUsedIP)
	}

	// 模拟时间流逝后会再次写入
	srv.usageMu.Lock()
	srv.usageLastSeen[cred.ID] = time.Now().Add(-2 * credentialUsageInterval)
	srv.usageMu.Unlock()
	if code, _ := rpcFrom(t, srv, cred.Token, "192.0.2.3:1000", body); code != http.StatusOK {
		t.Fatalf("第三次调用失败: %d", code)
	}
	third, _ := srv.store.GetCredential(ctx, cred.ID)
	if third.LastUsedIP != "192.0.2.3" {
		t.Fatalf("超过间隔后应刷新来源: %q", third.LastUsedIP)
	}
}

// 环境变量令牌、开放访问与本地 stdio 永远不带终端权限；工具总数保持不变。
func TestNonDatabaseSourcesNeverGetTerminalPermission(t *testing.T) {
	srv := setupTestMCPServer(t)
	for _, token := range []string{"", "cfg-token-123456", "exec-token-123456"} {
		code, resp := rpcFrom(t, srv, token, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		if code != http.StatusOK || resp.Error != nil {
			t.Fatalf("tools/list 失败: %d %#v", code, resp.Error)
		}
		raw, _ := json.Marshal(resp.Result)
		if strings.Contains(string(raw), "terminal_") {
			t.Fatalf("令牌 %q 不应看到终端工具: %s", token, raw)
		}
	}
	if LocalStdioCredential().Permissions.HasTerminal() {
		t.Fatal("本地 stdio 不得隐式带终端权限")
	}
}

// 没有任何可用工具的令牌（只带终端权限、终端工具尚未提供，或所有权限都关闭）
// 也必须拿到 "tools": []——MCP 客户端会校验该字段是数组，null 会让整个连接被判为异常。
func TestToolsListReturnsEmptyArrayNotNull(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()

	none := &model.MCPCredential{Name: "no-tools", Permissions: model.Permissions{}}
	if err := srv.store.CreateCredential(ctx, none); err != nil {
		t.Fatal(err)
	}
	code, resp := rpcFrom(t, srv, none.Token, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if code != http.StatusOK || resp.Error != nil {
		t.Fatalf("tools/list 失败: %d %#v", code, resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools 必须是数组而不是 null: %#v", result["tools"])
	}
	if len(tools) != 0 {
		t.Fatalf("无权限令牌不应看到任何工具: %#v", tools)
	}
}
