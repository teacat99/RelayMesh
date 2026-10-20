//go:build e2e && !windows

package e2e

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 能力 URL 凭据的真实进程端到端（AC-P23 中 Capability URL 的安全验收清单）：一个只会「填一个网址」的 MCP 连接器，用 /mcp/c/<密钥> 连上真实的 Hub、
// 守护进程与 Agent。检查：只有终端工具、密钥只在自己的网址上有效（请求头 / 查询串 / 旧式路径一律不认）、
// 管理面（REST / SSE）一律 403、Hub 日志里没有密钥、吊销 / 过期 / 来源白名单 / 重新生成即时生效、违规配置在创建时被拒绝。

type capCred struct {
	id     int
	secret string
	path   string
}

// newCapabilityCred 经 REST（已解锁）创建一把能力 URL 凭据；extra 里的键覆盖默认值。返回 HTTP 状态码。
func (l *execLab) newCapabilityCred(name string, extra map[string]any) (capCred, int) {
	l.t.Helper()
	body := map[string]any{
		"name":           name,
		"capability":     true,
		"permissions":    map[string]any{"terminal": true},
		"terminal_hosts": []string{l.code},
	}
	for k, v := range extra {
		body[k] = v
	}
	var out struct {
		Token          string `json:"token"`
		CapabilityPath string `json:"capability_path"`
		Credential     struct {
			ID int `json:"id"`
		} `json:"credential"`
	}
	code := l.h.api.raw("POST", "/api/v1/credentials", body, &out, true)
	return capCred{id: out.Credential.ID, secret: out.Token, path: out.CapabilityPath}, code
}

func (l *execLab) capClient(secret string) *mcpClient {
	return &mcpClient{t: l.t, base: l.h.base(), token: secret, capability: true, hc: &http.Client{Timeout: 90 * time.Second}}
}

// postMCP 向任意地址发一次 tools/list，返回状态码与响应体。
func postMCP(t *testing.T, url string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(b)
}

func (l *execLab) capCallCode(secret string) int {
	l.t.Helper()
	code, _ := postMCP(l.t, l.h.base()+"/mcp/c/"+secret, nil)
	return code
}

func TestRealCapabilityURLEndToEnd(t *testing.T) {
	l := newExecLab(t, execOn())
	base := l.h.base()

	cred, code := l.newCapabilityCred("cap-e2e", nil)
	if code != 200 || !strings.HasPrefix(cred.secret, "rmc-") || cred.path != "/mcp/c/"+cred.secret || cred.id == 0 {
		t.Fatalf("创建能力 URL 凭据: HTTP %d %+v", code, cred)
	}
	m := l.capClient(cred.secret)

	// 1) 只有终端工具、精简的终端指引，治理类工具不可用。
	ins := m.instructions()
	for _, must := range []string{"terminal_targets", "terminal_exec", "terminal_run"} {
		if !strings.Contains(ins, must) {
			t.Fatalf("能力 URL 的指引缺少 %s:\n%s", must, ins)
		}
	}
	if strings.Contains(ins, "interactive_feedback") || strings.Contains(ins, "workflow_id") {
		t.Fatalf("能力 URL 的指引不应含 RelayMesh 治理文本:\n%s", ins)
	}
	if got := strings.Join(m.toolNames(), ","); got != "terminal_exec,terminal_run,terminal_targets" {
		t.Fatalf("能力 URL 的 tools/list = %s", got)
	}
	for _, tool := range []string{"interactive_feedback", "list_sessions", "workflow_context", "task_board"} {
		if msg := m.callDenied(tool, map[string]any{"title": "x", "summary": "y"}); msg == "" {
			t.Fatalf("%s 应被拒绝", tool)
		}
	}

	// 2) 真实执行一条命令。
	ok := m.exec(l.code, "echo via-capability", map[string]any{"thread": "capability-e2e", "description": "AC-P23"})
	if ok.str("state") != "succeeded" || ok.obj("stdout").str("text") != "via-capability\n" || ok.num("exit_code") != 0 {
		t.Fatalf("经能力 URL 执行的结果不符: %v", ok)
	}

	// 3) Web 可见：线程里有这把凭据的运行；审计的明细里带通道标记 channel=capability。
	var threads struct {
		Threads []struct {
			CredentialName string `json:"credential_name"`
			Thread         string `json:"thread"`
			Total          int    `json:"total"`
		} `json:"threads"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/exec/threads", nil, &threads, false); c != 200 {
		t.Fatalf("线程聚合: HTTP %d", c)
	}
	seen := false
	for _, th := range threads.Threads {
		if th.CredentialName == "cap-e2e" && th.Thread == "capability-e2e" && th.Total >= 1 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("Web 里看不到能力 URL 凭据的运行: %+v", threads.Threads)
	}
	var audit struct {
		Logs []struct {
			Event  string `json:"event"`
			Actor  string `json:"actor"`
			Detail string `json:"detail"`
		} `json:"logs"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/audit?limit=200", nil, &audit, true); c != 200 {
		t.Fatalf("读取审计: HTTP %d", c)
	}
	marked := false
	for _, e := range audit.Logs {
		if e.Event == terminal.EvExecStart && e.Actor == "cap-e2e" && strings.Contains(e.Detail, "channel=capability") {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("审计的 exec_start 应带 channel=capability: %+v", audit.Logs)
	}

	// 4) 密钥只在自己的网址上有效：请求头 / 查询串 / 旧式路径都不认，且错误信息不回显密钥。
	for name, tc := range map[string]struct {
		url     string
		headers map[string]string
	}{
		"请求头":         {base + "/mcp", map[string]string{"Authorization": "Bearer " + cred.secret}},
		"查询串":         {base + "/mcp?token=" + cred.secret, nil},
		"旧式路径":        {base + "/mcp/" + cred.secret, nil},
		"旧式 token 路径": {base + "/mcp/token/" + cred.secret, nil},
	} {
		code, body := postMCP(t, tc.url, tc.headers)
		if code != http.StatusUnauthorized {
			t.Fatalf("能力密钥经「%s」出示应 401，实际 %d %s", name, code, lastN(body, 200))
		}
		if strings.Contains(body, cred.secret) {
			t.Fatalf("经「%s」的拒绝信息回显了密钥: %s", name, body)
		}
	}
	// 普通令牌放进能力网址同样被拒；环境变量 MCP 令牌也不能兜底。
	ordinary := l.newToken("ordinary-e2e")
	if code, _ := postMCP(t, base+"/mcp/c/"+ordinary, nil); code != http.StatusUnauthorized {
		t.Fatalf("普通令牌放进能力网址应 401，实际 %d", code)
	}
	if code, _ := postMCP(t, base+"/mcp/c/"+strings.Repeat("0", 64), map[string]string{"Authorization": "Bearer " + ordinary}); code != http.StatusUnauthorized {
		t.Fatalf("能力网址不应被请求头里的有效令牌兜底，实际 %d", code)
	}

	// 5) 管理面（REST / SSE）一律 403 mcp_token_forbidden，查询串写法同样不成功。
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/v1/sessions"},
		{"GET", "/api/v1/credentials"},
		{"GET", "/api/v1/terminal/hosts"},
		{"GET", "/api/v1/terminal/exec/threads"},
		{"GET", "/api/v1/events"},
		{"GET", "/sse/events"},
	} {
		c, body := httpCode(t, rt.method, base+rt.path, cred.secret)
		if c != http.StatusForbidden || !strings.Contains(body, "mcp_token_forbidden") {
			t.Fatalf("能力密钥访问 %s %s 应 403 mcp_token_forbidden，实际 %d %s", rt.method, rt.path, c, lastN(body, 200))
		}
	}
	if c, _ := httpCode(t, "GET", base+"/api/v1/events?token="+cred.secret, ""); c < 400 {
		t.Fatalf("查询串写法访问 SSE 不应成功: %d", c)
	}

	// 6) 凭据列表只给脱敏令牌，并标出这是能力 URL 凭据。
	var list struct {
		Credentials []struct {
			ID         int    `json:"id"`
			Token      string `json:"token"`
			Capability bool   `json:"capability"`
		} `json:"credentials"`
	}
	if c := l.h.api.raw("GET", "/api/v1/credentials", nil, &list, false); c != 200 {
		t.Fatalf("列出凭据: HTTP %d", c)
	}
	listed := false
	for _, c := range list.Credentials {
		if c.ID == cred.id {
			listed = true
			if !c.Capability || c.Token == cred.secret || !strings.Contains(c.Token, "*") {
				t.Fatalf("列表里的能力凭据应脱敏并带标记: %+v", c)
			}
		}
	}
	if !listed {
		t.Fatal("列表里找不到能力凭据")
	}

	// 7) 吊销（删除）后，下一次请求立刻被拒绝。
	if c := l.h.api.raw("DELETE", "/api/v1/credentials/"+strconv.Itoa(cred.id), nil, nil, false); c != 200 {
		t.Fatalf("吊销凭据: HTTP %d", c)
	}
	if code := l.capCallCode(cred.secret); code != http.StatusUnauthorized {
		t.Fatalf("吊销后应立刻 401，实际 %d", code)
	}

	// 8) 整个过程里，Hub 的日志（访问日志与恢复日志）没有出现过密钥，但留下了脱敏后的请求行。
	logBytes, err := os.ReadFile(l.h.proc.log)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logBytes)
	if strings.Contains(logText, cred.secret) {
		t.Fatalf("Hub 日志泄露了能力 URL 的密钥:\n%s", lastN(logText, 600))
	}
	if !strings.Contains(logText, "/mcp/c/***") {
		t.Fatalf("Hub 日志里应有脱敏后的请求行 /mcp/c/***:\n%s", lastN(logText, 600))
	}
}

func TestRealCapabilityURLControls(t *testing.T) {
	l := newExecLab(t, execOn())

	// 创建时就拒绝的违规配置：治理权限、全部主机、没有主机；只凭登录（未解锁）也不能创建带终端权限的凭据。
	for name, extra := range map[string]map[string]any{
		"带治理权限": {"permissions": map[string]any{"terminal": true, "feedback": true}},
		"全部主机":  {"terminal_hosts": []string{"*"}},
		"没有主机":  {"terminal_hosts": []string{}},
		"没有终端位": {"permissions": map[string]any{"feedback": true}},
	} {
		if _, code := l.newCapabilityCred("bad-"+name, extra); code != http.StatusBadRequest {
			t.Errorf("%s：创建应 400，实际 %d", name, code)
		}
	}
	if c := l.h.api.raw("POST", "/api/v1/credentials", map[string]any{
		"name": "no-unlock", "capability": true, "permissions": map[string]any{"terminal": true}, "terminal_hosts": []string{l.code},
	}, nil, false); c != http.StatusForbidden {
		t.Errorf("未解锁创建能力凭据应 403，实际 %d", c)
	}

	// 来源白名单：把允许范围设成别处，本机回环地址的请求必须被拒绝。
	bound, code := l.newCapabilityCred("cap-bound", map[string]any{"allowed_cidrs": []string{"203.0.113.0/24"}})
	if code != 200 {
		t.Fatalf("创建带来源白名单的凭据: HTTP %d", code)
	}
	if c := l.capCallCode(bound.secret); c != http.StatusUnauthorized {
		t.Fatalf("来源不在白名单内应 401，实际 %d", c)
	}
	// 把白名单放宽到回环地址（放宽需要解锁），随即可用。
	if c := l.h.api.raw("PUT", "/api/v1/credentials/"+strconv.Itoa(bound.id), map[string]any{"allowed_cidrs": []string{"127.0.0.0/8"}}, nil, true); c != 200 {
		t.Fatalf("改来源白名单: HTTP %d", c)
	}
	if c := l.capCallCode(bound.secret); c != http.StatusOK {
		t.Fatalf("来源在白名单内应可用，实际 %d", c)
	}

	// 到期：到期前可用，到期后被拒绝。
	expiring, code := l.newCapabilityCred("cap-expiring", map[string]any{"expires_at": time.Now().Add(4 * time.Second).UTC().Format(time.RFC3339Nano)})
	if code != 200 {
		t.Fatalf("创建带到期时间的凭据: HTTP %d", code)
	}
	if c := l.capCallCode(expiring.secret); c != http.StatusOK {
		t.Fatalf("到期前应可用，实际 %d", c)
	}
	waitUntil(t, 15*time.Second, "凭据到期后被拒绝", func() bool { return l.capCallCode(expiring.secret) == http.StatusUnauthorized })

	// 停用 → 拒绝；重新启用需要解锁。
	toggle, code := l.newCapabilityCred("cap-toggle", nil)
	if code != 200 {
		t.Fatalf("创建凭据: HTTP %d", code)
	}
	if c := l.h.api.raw("PUT", "/api/v1/credentials/"+strconv.Itoa(toggle.id), map[string]any{"is_active": false}, nil, false); c != 200 {
		t.Fatalf("停用: HTTP %d", c)
	}
	if c := l.capCallCode(toggle.secret); c != http.StatusUnauthorized {
		t.Fatalf("停用后应 401，实际 %d", c)
	}
	if c := l.h.api.raw("PUT", "/api/v1/credentials/"+strconv.Itoa(toggle.id), map[string]any{"is_active": true}, nil, true); c != 200 {
		t.Fatalf("重新启用: HTTP %d", c)
	}
	if c := l.capCallCode(toggle.secret); c != http.StatusOK {
		t.Fatalf("重新启用后应可用，实际 %d", c)
	}

	// 重新生成：旧网址立即失效，新网址可用，且新密钥仍是能力形状。
	var regen struct {
		Token          string `json:"token"`
		CapabilityPath string `json:"capability_path"`
	}
	if c := l.h.api.raw("POST", "/api/v1/credentials/"+strconv.Itoa(toggle.id)+"/regenerate", nil, &regen, true); c != 200 {
		t.Fatalf("重新生成: HTTP %d", c)
	}
	if !strings.HasPrefix(regen.Token, "rmc-") || regen.Token == toggle.secret || regen.CapabilityPath != "/mcp/c/"+regen.Token {
		t.Fatalf("重新生成的结果不对: %+v", regen)
	}
	if c := l.capCallCode(toggle.secret); c != http.StatusUnauthorized {
		t.Fatalf("重新生成后旧网址应立即失效，实际 %d", c)
	}
	if c := l.capCallCode(regen.Token); c != http.StatusOK {
		t.Fatalf("重新生成后的新网址应可用，实际 %d", c)
	}

	// 能力凭据不可改成放宽的形状：加治理权限、改成全部主机都被拒绝。
	if c := l.h.api.raw("PUT", "/api/v1/credentials/"+strconv.Itoa(toggle.id), map[string]any{"permissions": map[string]any{"terminal": true, "feedback": true}}, nil, true); c != http.StatusBadRequest {
		t.Errorf("给能力凭据加治理权限应 400，实际 %d", c)
	}
	if c := l.h.api.raw("PUT", "/api/v1/credentials/"+strconv.Itoa(toggle.id), map[string]any{"terminal_hosts": []string{"*"}}, nil, true); c != http.StatusBadRequest {
		t.Errorf("把能力凭据改成全部主机应 400，实际 %d", c)
	}

	// 主机授权被收紧（清空）后，这把凭据看不到任何主机。
	if c := l.h.api.raw("PUT", "/api/v1/credentials/"+strconv.Itoa(toggle.id), map[string]any{"terminal_hosts": []string{}}, nil, false); c != 200 {
		t.Fatalf("清空主机授权: HTTP %d", c)
	}
	res, isErr := l.capClient(regen.Token).tool("terminal_targets", map[string]any{})
	if isErr {
		t.Fatalf("terminal_targets: %v", res)
	}
	if targets, _ := res["targets"].([]any); len(targets) != 0 {
		t.Fatalf("清空主机授权后不应看到任何主机: %v", res)
	}

	// 全程没有泄露任何密钥到 Hub 日志。
	logBytes, err := os.ReadFile(l.h.proc.log)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{bound.secret, expiring.secret, toggle.secret, regen.Token} {
		if strings.Contains(string(logBytes), secret) {
			t.Fatalf("Hub 日志泄露了能力密钥 %s…", secret[:8])
		}
	}
}
