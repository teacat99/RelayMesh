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
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const (
	capTestHost   = "RM-AAAA-BBBB-CCCC-DDDD"
	capToolsList  = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	capInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
)

// capRPC 向任意路径发起一次 JSON-RPC 调用，可带请求头与来源地址。
func capRPC(t *testing.T, srv *Server, target string, headers map[string]string, remoteAddr, body string) (int, jsonRPCResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	for k, v := range headers {
		req.Header.Set(k, v)
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

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// newCapability 建一把能力 URL 凭据，返回它与令牌。
func newCapability(t *testing.T, srv *Server, name string, mut func(*model.MCPCredential)) (*model.MCPCredential, string) {
	t.Helper()
	c := &model.MCPCredential{
		Name:          name,
		Capability:    true,
		Permissions:   model.Permissions{Terminal: true},
		TerminalHosts: model.StringList{capTestHost},
	}
	if mut != nil {
		mut(c)
	}
	if err := srv.store.CreateCredential(context.Background(), c); err != nil {
		t.Fatalf("创建能力凭据 %s: %v", name, err)
	}
	return c, c.Token
}

func newOrdinary(t *testing.T, srv *Server, name string, perms model.Permissions) string {
	t.Helper()
	c := &model.MCPCredential{Name: name, Permissions: perms}
	if err := srv.store.CreateCredential(context.Background(), c); err != nil {
		t.Fatalf("创建凭据 %s: %v", name, err)
	}
	return c.Token
}

func capToolNames(resp jsonRPCResponse) []string {
	result, _ := resp.Result.(map[string]any)
	tools, _ := result["tools"].([]any)
	var out []string
	for _, tl := range tools {
		if m, ok := tl.(map[string]any); ok {
			out = append(out, m["name"].(string))
		}
	}
	return out
}

func TestCapabilitySecretFromPath(t *testing.T) {
	cases := []struct {
		path   string
		secret string
		route  bool
	}{
		{"/mcp/c/rmc-abc", "rmc-abc", true},
		{"/mcp/c/rmc-abc/", "rmc-abc", true},
		{"/mcp/c", "", true},
		{"/mcp/c/", "", true},
		{"/mcp/c/a/b", "", true},
		{"/mcp", "", false},
		{"/mcp/", "", false},
		{"/mcp/cc/abc", "", false},
		{"/mcp/token/abc", "", false},
		{"/mcp/abc", "", false},
		{"/other/mcp/c/abc", "", false},
	}
	for _, tc := range cases {
		secret, route := capabilitySecretFromPath(tc.path)
		if secret != tc.secret || route != tc.route {
			t.Errorf("%q: 得到 (%q, %v)，期望 (%q, %v)", tc.path, secret, route, tc.secret, tc.route)
		}
	}
}

func TestCapabilityTokenShape(t *testing.T) {
	tok, err := store.GenerateCapabilityToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "rmc-") || !store.IsCapabilityTokenShape(tok) {
		t.Fatalf("生成的能力令牌形状不对: %q", tok)
	}
	if tok2, _ := store.GenerateCapabilityToken(); tok2 == tok {
		t.Fatal("两次生成的令牌相同")
	}
	for _, bad := range []string{"", "rmc-", "rmc-xyz", strings.ToUpper(tok), tok + "0", tok[:len(tok)-1], "rm-" + tok[4:], "rmc-" + strings.Repeat("g", 64)} {
		if store.IsCapabilityTokenShape(bad) {
			t.Errorf("%q 不应被认作能力令牌", bad)
		}
	}
}

// 能力 URL 基本可用：tools/list 只有终端工具，initialize 给出精简的 ExecMesh 指引。
func TestCapabilityURLWorks(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred, tok := newCapability(t, srv, "chatgpt", nil)
	if !strings.HasPrefix(tok, "rmc-") {
		t.Fatalf("能力令牌缺少专用前缀: %q", tok)
	}

	code, resp := capRPC(t, srv, "/mcp/c/"+tok, nil, "", capToolsList)
	if code != http.StatusOK || resp.Error != nil {
		t.Fatalf("tools/list: %d %#v", code, resp.Error)
	}
	names := capToolNames(resp)
	want := map[string]bool{"terminal_targets": true, "terminal_exec": true, "terminal_run": true}
	if len(names) != len(want) {
		t.Fatalf("工具清单应只含终端工具: %v", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("意外的工具 %q: %v", n, names)
		}
	}

	_, init := capRPC(t, srv, "/mcp/c/"+tok+"/", nil, "", capInitialize)
	text, _ := init.Result.(map[string]any)["instructions"].(string)
	if !strings.Contains(text, "terminal_exec") || strings.Contains(text, "interactive_feedback") {
		t.Fatalf("能力 URL 应收到精简的 ExecMesh 指引: %q", text)
	}

	// 使用被登记（最近使用时间 / 来源）。
	got, err := srv.store.GetCredential(context.Background(), cred.ID)
	if err != nil || got.LastUsedAt == nil {
		t.Fatalf("最近使用未登记: %v %#v", err, got)
	}
}

// 同一把钥匙只有一种出示方式：能力令牌不能放进请求头 / 查询串 / 旧式路径，普通令牌也不能放进能力路径。
func TestCapabilityAndOrdinaryTokensAreNotInterchangeable(t *testing.T) {
	srv := setupTestMCPServer(t)
	_, capTok := newCapability(t, srv, "cap", nil)
	ordTok := newOrdinary(t, srv, "ord", model.Permissions{Terminal: true})

	for name, target := range map[string]struct {
		path    string
		headers map[string]string
	}{
		"请求头":         {"/mcp", bearer(capTok)},
		"查询串":         {"/mcp?token=" + capTok, nil},
		"旧式路径":        {"/mcp/" + capTok, nil},
		"旧式 token 路径": {"/mcp/token/" + capTok, nil},
	} {
		code, resp := capRPC(t, srv, target.path, target.headers, "", capToolsList)
		if code != http.StatusUnauthorized || resp.Error == nil {
			t.Errorf("能力令牌经%s出示应被拒绝: %d %#v", name, code, resp.Error)
		}
		if resp.Error != nil && strings.Contains(resp.Error.Message, capTok) {
			t.Errorf("错误信息不得回显令牌")
		}
	}

	// 普通令牌放进能力路径：形状检查就挡住了，错误是泛化的。
	code, resp := capRPC(t, srv, "/mcp/c/"+ordTok, nil, "", capToolsList)
	if code != http.StatusUnauthorized || resp.Error == nil {
		t.Fatalf("普通令牌放进能力路径应被拒绝: %d %#v", code, resp.Error)
	}
	// 能力路径忽略请求头里的凭据：路径里是别的令牌，请求头里的有效令牌不能替它兜底。
	code, _ = capRPC(t, srv, "/mcp/c/rmc-"+strings.Repeat("0", 64), bearer(ordTok), "", capToolsList)
	if code != http.StatusUnauthorized {
		t.Fatalf("能力路径不得被请求头里的有效令牌兜底: %d", code)
	}
	// 普通令牌照常可用。
	if code, resp := capRPC(t, srv, "/mcp", bearer(ordTok), "", capToolsList); code != http.StatusOK || resp.Error != nil {
		t.Fatalf("普通令牌应照常可用: %d %#v", code, resp.Error)
	}
}

// 能力路径永远不会回退到环境变量令牌或开放访问。
func TestCapabilityRouteNeverFallsBack(t *testing.T) {
	// 库里没有任何凭据且没配环境令牌：普通入口是开放访问，能力路径仍然必须拒绝。
	open := setupTestMCPServer(t)
	open.cfg.ConfigureToken, open.cfg.ExecutionToken = "", ""
	if code, _ := capRPC(t, open, "/mcp", nil, "", capToolsList); code != http.StatusOK {
		t.Fatalf("前置：开放访问下普通入口应可用: %d", code)
	}
	junk := "rmc-" + strings.Repeat("a", 64)
	if code, resp := capRPC(t, open, "/mcp/c/"+junk, nil, "", capToolsList); code != http.StatusUnauthorized || resp.Error == nil {
		t.Fatalf("开放访问下能力路径也必须拒绝: %d %#v", code, resp.Error)
	}

	// 环境变量令牌放进能力路径或请求头都不行。
	srv := setupTestMCPServer(t)
	srv.cfg.MCPToken = "env-mcp-token-0123456789abcdef"
	for _, target := range []string{"/mcp/c/" + srv.cfg.MCPToken, "/mcp/c/" + junk, "/mcp/c"} {
		if code, _ := capRPC(t, srv, target, bearer(srv.cfg.MCPToken), "", capToolsList); code != http.StatusUnauthorized {
			t.Errorf("%s: 环境变量令牌不得兜底能力路径: %d", target, code)
		}
	}
}

// 吊销即时生效；过期、停用、来源白名单都拒绝；重新生成让旧网址立刻失效。
func TestCapabilityLifecycleControls(t *testing.T) {
	srv := setupTestMCPServer(t)
	ctx := context.Background()
	call := func(tok, remote string) int {
		code, _ := capRPC(t, srv, "/mcp/c/"+tok, nil, remote, capToolsList)
		return code
	}

	// 过期。
	past := time.Now().Add(-time.Minute)
	_, expired := newCapability(t, srv, "expired", func(c *model.MCPCredential) { c.ExpiresAt = &past })
	if call(expired, "") != http.StatusUnauthorized {
		t.Error("已过期的能力 URL 应拒绝")
	}
	future := time.Now().Add(time.Hour)
	_, fresh := newCapability(t, srv, "fresh", func(c *model.MCPCredential) { c.ExpiresAt = &future })
	if call(fresh, "") != http.StatusOK {
		t.Error("未过期的能力 URL 应可用")
	}

	// 来源 IP 白名单。
	_, bound := newCapability(t, srv, "bound", func(c *model.MCPCredential) { c.AllowedCIDRs = model.StringList{"10.0.0.0/8"} })
	if call(bound, "10.1.2.3:5555") != http.StatusOK {
		t.Error("白名单内的来源应可用")
	}
	if call(bound, "203.0.113.9:5555") != http.StatusUnauthorized {
		t.Error("白名单外的来源应拒绝")
	}

	// 停用与重新启用。
	cred, tok := newCapability(t, srv, "toggle", nil)
	if _, err := srv.store.UpdateCredential(ctx, cred.ID, map[string]any{"is_active": false}); err != nil {
		t.Fatal(err)
	}
	if call(tok, "") != http.StatusUnauthorized {
		t.Error("已停用的能力 URL 应拒绝")
	}
	if _, err := srv.store.UpdateCredential(ctx, cred.ID, map[string]any{"is_active": true}); err != nil {
		t.Fatal(err)
	}
	if call(tok, "") != http.StatusOK {
		t.Error("重新启用后应可用")
	}

	// 重新生成：旧网址立即失效，新网址可用，新令牌仍是能力形状。
	_, newTok, err := srv.store.RegenerateCredentialToken(ctx, cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !store.IsCapabilityTokenShape(newTok) || newTok == tok {
		t.Fatalf("重新生成的令牌不对: %q", newTok)
	}
	if call(tok, "") != http.StatusUnauthorized {
		t.Error("重新生成后旧网址应立刻失效")
	}
	if call(newTok, "") != http.StatusOK {
		t.Error("重新生成后的新网址应可用")
	}

	// 吊销（删除）：下一次请求立刻拒绝。
	if err := srv.store.DeleteCredential(ctx, cred.ID); err != nil {
		t.Fatal(err)
	}
	if call(newTok, "") != http.StatusUnauthorized {
		t.Error("吊销后应立刻拒绝")
	}
}

// 数据库被手工改成违规配置（例如带了治理权限）时，运行时兜底拒绝。
func TestCapabilityMisconfiguredIsRefusedAtRuntime(t *testing.T) {
	srv := setupTestMCPServer(t)
	cred, tok := newCapability(t, srv, "tampered", nil)
	bad := cred.Permissions
	bad.Feedback = true
	if err := srv.store.DB().Model(&model.MCPCredential{}).Where("id = ?", cred.ID).Update("permissions", bad).Error; err != nil {
		t.Fatal(err)
	}
	code, resp := capRPC(t, srv, "/mcp/c/"+tok, nil, "", capToolsList)
	if code != http.StatusUnauthorized || resp.Error == nil || !strings.Contains(resp.Error.Message, "misconfigured") {
		t.Fatalf("违规配置的能力凭据应被拒绝: %d %#v", code, resp.Error)
	}
}

// 加固：库里有凭据时，无效令牌不再落到「开放访问」。
func TestInvalidTokenIsNotOpenAccessWhenCredentialsExist(t *testing.T) {
	srv := setupTestMCPServer(t)
	srv.cfg.ConfigureToken, srv.cfg.ExecutionToken = "", ""
	// 没有任何凭据：开放访问。
	if code, _ := capRPC(t, srv, "/mcp", bearer("whatever"), "", capToolsList); code != http.StatusOK {
		t.Fatalf("没有凭据时应开放: %d", code)
	}
	newOrdinary(t, srv, "ord", model.AllPermissions())
	for name, hdr := range map[string]map[string]string{"无效令牌": bearer("not-a-real-token"), "缺令牌": nil} {
		if code, resp := capRPC(t, srv, "/mcp", hdr, "", capToolsList); code != http.StatusUnauthorized || resp.Error == nil {
			t.Errorf("%s：库里有凭据时必须拒绝: %d %#v", name, code, resp.Error)
		}
	}
}

// 经能力 URL 的终端调用在审计线索里带通道标记（通过 Caller 传递）。
func TestCapabilityChannelReachesTerminalCaller(t *testing.T) {
	srv := setupTestMCPServer(t)
	fx := &fakeExecutor{}
	srv.SetTerminalExecutor(fx)
	_, capTok := newCapability(t, srv, "chan-cap", nil)
	ordTok := newOrdinary(t, srv, "chan-ord", model.Permissions{Terminal: true})

	call := func(target string, headers map[string]string) {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"terminal_targets","arguments":{}}}`
		if code, resp := capRPC(t, srv, target, headers, "", body); code != http.StatusOK || resp.Error != nil {
			t.Fatalf("%s: %d %#v", target, code, resp.Error)
		}
	}
	call("/mcp/c/"+capTok, nil)
	call("/mcp", bearer(ordTok))
	if len(fx.callers) != 2 || fx.callers[0].Channel != "capability" || fx.callers[1].Channel != "" {
		t.Fatalf("调用通道标记不对: %#v", fx.callers)
	}
}
