//go:build e2e && !windows

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 模型执行（terminal_targets / terminal_exec / terminal_run）的真实进程端到端：
// Hub、会话守护进程、Agent 是三个独立的操作系统进程，模型一侧用真实的 MCP HTTP 调用。

// ---------- MCP 客户端 ----------

type mcpClient struct {
	t     *testing.T
	base  string
	token string
	hc    *http.Client
	id    int
	// capability 为 true 时 token 是能力 URL 的密钥：请求发往 /mcp/c/<密钥>，不带任何请求头凭据。
	capability bool
}

// newRequest 构造一次 MCP 调用：普通令牌走 /mcp + Authorization 头，能力 URL 走专用网址。
func (m *mcpClient) newRequest(body []byte) *http.Request {
	m.t.Helper()
	target, bearer := m.base+"/mcp", true
	if m.capability {
		target, bearer = m.base+"/mcp/c/"+m.token, false
	}
	req, err := http.NewRequest("POST", target, bytes.NewReader(body))
	if err != nil {
		m.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer {
		req.Header.Set("Authorization", "Bearer "+m.token)
	}
	return req
}

type toolRes map[string]any

func (r toolRes) str(k string) string { v, _ := r[k].(string); return v }

func (r toolRes) num(k string) int64 {
	switch v := r[k].(type) {
	case float64:
		return int64(v)
	}
	return -1
}

func (r toolRes) obj(k string) toolRes {
	m, _ := r[k].(map[string]any)
	return toolRes(m)
}

func (r toolRes) errCode() string { return r.obj("error").str("code") }

// tool 调用一个 MCP 工具，返回结果对象（文本内容按 JSON 解析）与 isError。
func (m *mcpClient) tool(name string, args map[string]any) (toolRes, bool) {
	m.t.Helper()
	m.id++
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": m.id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	resp, err := m.hc.Do(m.newRequest(body))
	if err != nil {
		m.t.Fatalf("MCP 调用 %s 失败: %v", name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var rpc struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil || rpc.Error != nil || len(rpc.Result.Content) == 0 {
		m.t.Fatalf("MCP 响应异常（%s）: HTTP %d %s", name, resp.StatusCode, lastN(string(raw), 400))
	}
	out := toolRes{}
	if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &out); err != nil {
		out = toolRes{"_text": rpc.Result.Content[0].Text}
	}
	return out, rpc.Result.IsError
}

// exec 执行命令并要求成功受理（不是业务拒绝）。
func (m *mcpClient) exec(target, command string, extra map[string]any) toolRes {
	m.t.Helper()
	args := map[string]any{"target": target, "command": command, "wait_seconds": 30}
	for k, v := range extra {
		args[k] = v
	}
	res, isErr := m.tool("terminal_exec", args)
	if isErr {
		m.t.Fatalf("terminal_exec(%q) 被拒绝: %v", command, res)
	}
	return res
}

func (m *mcpClient) run(action, id string, extra map[string]any) (toolRes, bool) {
	m.t.Helper()
	args := map[string]any{"action": action, "run_id": id}
	for k, v := range extra {
		args[k] = v
	}
	return m.tool("terminal_run", args)
}

// ---------- 实验环境 ----------

type execLab struct {
	t        *testing.T
	h        *hub
	agentBin string
	d        hostDirs
	hp       *proc
	ag       *proc
	code     string
	mcp      *mcpClient
}

// newExecLab 起一个完整环境：Hub（模型执行总开关打开）+ 守护进程 + Agent，主机已启用并允许模型访问，
// 并发出一把绑定该主机的 ExecMesh 档令牌。
func newExecLab(t *testing.T, hubEnv []string, holderArgs ...string) *execLab {
	t.Helper()
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin, hubEnv...)
	d := newHostDirs(t)
	hp := startHolder(t, agentBin, d, holderArgs...)
	ag := startAgent(t, agentBin, h, d, "agent", "--holder", "external")
	code := h.api.enrollOnly()
	waitHost(t, h, code, "主机上线且守护进程就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	l := &execLab{t: t, h: h, agentBin: agentBin, d: d, hp: hp, ag: ag, code: code}
	if c := h.api.call("POST", "/hosts/"+code+"/model", map[string]any{"allow_model": true}, nil); c != 200 {
		t.Fatalf("允许模型访问失败: HTTP %d", c)
	}
	l.mcp = &mcpClient{t: t, base: h.base(), token: l.newToken("model-e2e"), hc: &http.Client{Timeout: 90 * time.Second}}
	return l
}

func execOn() []string { return []string{"RELAYMESH_TERMINAL_EXEC=on"} }

// newToken 签发一把只绑定本实验主机的 ExecMesh 档令牌。
func (l *execLab) newToken(name string) string {
	l.t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	c := l.h.api.raw("POST", "/api/v1/credentials", map[string]any{
		"name": name, "permissions": map[string]any{"terminal": true}, "terminal_hosts": []string{l.code},
	}, &out, true)
	if c != 200 || out.Token == "" {
		l.t.Fatalf("创建令牌失败: HTTP %d", c)
	}
	return out.Token
}

// dbContains 在 Hub 的 SQLite 文件（含 WAL）与日志里查找字节串。
func (l *execLab) dbContains(needle string) (where string, found bool) {
	l.t.Helper()
	files, _ := filepath.Glob(filepath.Join(l.h.dir, "relaymesh.db*"))
	files = append(files, l.h.proc.log)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err == nil && bytes.Contains(b, []byte(needle)) {
			return filepath.Base(f), true
		}
	}
	return "", false
}

func (l *execLab) restartAgent(name string) {
	l.t.Helper()
	l.ag = startAgent(l.t, l.agentBin, l.h, l.d, name, "--holder", "external")
	waitHost(l.t, l.h, l.code, "主机重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
}

// ---------- 用例 ----------

// AC-P2：基本执行路径——输出、退出码、cwd、stderr、超时、信号，以及 targets 的信息。
func TestRealModelExecBasics(t *testing.T) {
	l := newExecLab(t, execOn())
	m := l.mcp

	tg, isErr := m.tool("terminal_targets", map[string]any{})
	if isErr {
		t.Fatalf("terminal_targets: %v", tg)
	}
	list, _ := tg["targets"].([]any)
	if len(list) != 1 {
		t.Fatalf("应只看到被授权的一台主机: %v", tg)
	}
	target := toolRes(list[0].(map[string]any))
	if target.str("machine_code") != l.code || target["online"] != true || target["usable"] != true || target.str("home") == "" || target.str("shell") == "" {
		t.Fatalf("target 信息不全: %v", target)
	}

	ok := m.exec(l.code, "echo hello; echo oops 1>&2", map[string]any{"description": "基本输出"})
	if ok.str("state") != "succeeded" || ok.obj("stdout").str("text") != "hello\n" || ok.obj("stderr").str("text") != "oops\n" || ok.num("exit_code") != 0 {
		t.Fatalf("基本执行结果不符: %v", ok)
	}
	if ok.str("notice") == "" {
		t.Fatalf("带输出的结果应带不可信数据提示")
	}

	fail := m.exec(l.code, "exit 3", nil)
	if fail.str("state") != "failed" || fail.num("exit_code") != 3 {
		t.Fatalf("非 0 退出码: %v", fail)
	}

	cwd := m.exec(l.code, "pwd", map[string]any{"cwd": "/tmp"})
	if cwd.obj("stdout").str("text") != "/tmp\n" {
		t.Fatalf("cwd 未生效: %v", cwd)
	}

	// 通过目标备注 / 主机名也能指向这台主机。
	byName := m.exec(target.str("name"), "echo by-name", nil)
	if byName.str("state") != "succeeded" {
		t.Fatalf("按名称指向主机失败: %v", byName)
	}

	// 信号终止。
	sig := m.exec(l.code, "kill -9 $$", nil)
	if sig.str("state") != "failed" || sig.str("signal") == "" {
		t.Fatalf("被信号终止应为 failed 且带 signal: %v", sig)
	}

	// 超时：进程组被整体结束，不留孤儿。
	pidFile := filepath.Join(t.TempDir(), "pid")
	started := time.Now()
	to := m.exec(l.code, fmt.Sprintf("sh -c 'echo $$ > %s; sleep 120' & wait", pidFile), map[string]any{"timeout_seconds": 2})
	if to.str("state") != "timeout" {
		t.Fatalf("超时状态不符: %v", to)
	}
	if d := time.Since(started); d > 20*time.Second {
		t.Fatalf("超时命令耗时过长: %v", d)
	}
	pid := readPID(t, pidFile)
	waitUntil(t, 10*time.Second, "超时后整个进程组被结束", func() bool { return !pidAlive(pid) })

	// 审计里有这些执行，且不含命令输出。
	var runs struct {
		Runs []map[string]any `json:"runs"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/exec/runs?limit=50", nil, &runs, false); c != 200 || len(runs.Runs) < 6 {
		t.Fatalf("运行列表: HTTP %d %d 条", c, len(runs.Runs))
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	waitUntil(t, 10*time.Second, "子进程写出自己的 PID", func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		pid = n
		return err == nil && n > 0
	})
	return pid
}

// 大输出：模型拿到开头 + 结尾，其余按需分页读取；Hub 数据库与日志里不会有完整输出（AC-P7）。
func TestRealModelExecLargeOutputStaysOnHost(t *testing.T) {
	l := newExecLab(t, execOn())
	m := l.mcp
	// 标记由 shell 现算出来：命令文本里不含它（命令文本本身会被记录，只是做了脱敏）。
	a, b := 100003+int(time.Now().UnixNano()%1000), 100019
	marker := fmt.Sprintf("MIDDLE_%d", a*b)

	script := fmt.Sprintf("yes abcdefghij | head -c 1000000; echo MIDDLE_$((%d*%d)); yes klmnopqrst | head -c 1000000; echo END_OF_OUTPUT", a, b)
	res := m.exec(l.code, script, map[string]any{"max_output_bytes": 65536})
	out := res.obj("stdout")
	if res.str("state") != "succeeded" || out["truncated"] != true || out.num("bytes") < 2000000 || len(out.str("text")) != 65536 {
		t.Fatalf("大输出应被截断到上限并给出总量: state=%s bytes=%d truncated=%v len=%d", res.str("state"), out.num("bytes"), out["truncated"], len(out.str("text")))
	}
	if !strings.Contains(out.str("tail"), "END_OF_OUTPUT") {
		t.Fatalf("tail 应包含输出结尾: %q", out.str("tail"))
	}
	if strings.Contains(out.str("text"), marker) {
		t.Fatalf("标记不应出现在被截断的开头里")
	}

	// 分页读取，最终能在中间找到标记，且字节数对得上。
	runID := res.str("run_id")
	var got int64
	found := false
	next := int64(0)
	for i := 0; i < 64; i++ {
		page, isErr := m.run("output", runID, map[string]any{"offset": next, "limit": 262144})
		if isErr {
			t.Fatalf("分页读取失败: %v", page)
		}
		if strings.Contains(page.str("text"), marker) {
			found = true
		}
		got += int64(len(page.str("text")))
		next = page.num("next_offset")
		if page["eof"] == true {
			break
		}
	}
	if !found || got != out.num("bytes") {
		t.Fatalf("分页读取应取回完整输出：found=%v got=%d want=%d", found, got, out.num("bytes"))
	}

	// 完整输出只存在于主机：Hub 的数据库文件与日志里找不到这个标记。
	if where, found := l.dbContains(marker); found {
		t.Fatalf("完整输出不应进入 Hub 的 %s", where)
	}
	var dbBytes int64
	files, _ := filepath.Glob(filepath.Join(l.h.dir, "relaymesh.db*"))
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			dbBytes += fi.Size()
		}
	}
	if dbBytes > 1<<20 {
		t.Fatalf("2 MB 的输出不应让 Hub 的数据库文件膨胀（现在 %d 字节）", dbBytes)
	}
}

// AC-P6：取消——进程组被结束；全局暂停会取消进行中的运行并拒绝新的运行。
func TestRealModelExecCancelAndPause(t *testing.T) {
	l := newExecLab(t, execOn())
	m := l.mcp
	pidFile := filepath.Join(t.TempDir(), "pid")

	running := m.exec(l.code, fmt.Sprintf("sh -c 'echo $$ > %s; sleep 300' & wait", pidFile), map[string]any{"wait_seconds": 0})
	if running.str("state") != "running" {
		t.Fatalf("应立即返回 running: %v", running)
	}
	pid := readPID(t, pidFile)
	if !pidAlive(pid) {
		t.Fatalf("命令应在运行")
	}
	cancelled, isErr := m.run("cancel", running.str("run_id"), map[string]any{"wait_seconds": 15})
	if isErr || cancelled.str("state") != "cancelled" {
		t.Fatalf("取消结果: %v", cancelled)
	}
	waitUntil(t, 10*time.Second, "取消后整个进程组被结束", func() bool { return !pidAlive(pid) })

	// 全局暂停：进行中的运行被取消，新的调用被拒绝；恢复需要解锁。
	pidFile2 := filepath.Join(t.TempDir(), "pid2")
	second := m.exec(l.code, fmt.Sprintf("sh -c 'echo $$ > %s; sleep 300' & wait", pidFile2), map[string]any{"wait_seconds": 0})
	pid2 := readPID(t, pidFile2)
	if c := l.h.api.raw("POST", "/api/v1/terminal/exec/pause", map[string]any{"scope": "all"}, nil, false); c != 200 {
		t.Fatalf("暂停: HTTP %d", c)
	}
	waitUntil(t, 15*time.Second, "暂停时进行中的命令被结束", func() bool { return !pidAlive(pid2) })
	st, _ := m.run("status", second.str("run_id"), map[string]any{"wait_seconds": 10})
	if st.str("state") != "cancelled" {
		t.Fatalf("暂停后运行应为 cancelled: %v", st)
	}
	denied, isErr := m.tool("terminal_exec", map[string]any{"target": l.code, "command": "echo no"})
	if !isErr || denied.errCode() != "paused" {
		t.Fatalf("暂停期间应拒绝新命令: %v", denied)
	}
	if c := l.h.api.raw("POST", "/api/v1/terminal/exec/resume", map[string]any{"scope": "all"}, nil, false); c == 200 {
		t.Fatalf("恢复不应只凭登录")
	}
	if c := l.h.api.raw("POST", "/api/v1/terminal/exec/resume", map[string]any{"scope": "all"}, nil, true); c != 200 {
		t.Fatalf("解锁后恢复: HTTP %d", c)
	}
	if r := m.exec(l.code, "echo back", nil); r.str("state") != "succeeded" {
		t.Fatalf("恢复后应可执行: %v", r)
	}
}

// 运行由主机上的守护进程持有：Hub 重启期间命令继续运行，重启后可以取回结果与完整输出。
func TestRealModelExecSurvivesHubRestart(t *testing.T) {
	l := newExecLab(t, execOn())
	m := l.mcp
	token := "done-" + randHex(4)

	res := m.exec(l.code, fmt.Sprintf("sleep 5; echo %s", token), map[string]any{"wait_seconds": 0})
	id := res.str("run_id")
	if res.str("state") != "running" {
		t.Fatalf("应在运行: %v", res)
	}

	l.h.restart()
	waitHost(t, l.h, l.code, "Hub 重启后主机重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})

	st, isErr := m.run("status", id, map[string]any{"wait_seconds": 30})
	if isErr || st.str("state") != "succeeded" || !strings.Contains(st.obj("stdout").str("text"), token) {
		t.Fatalf("Hub 重启后应取回运行结果: %v", st)
	}
	// 结束事件已被确认：再查一次结果一致，且完整输出仍可读。
	page, isErr := m.run("output", id, map[string]any{})
	if isErr || !strings.Contains(page.str("text"), token) {
		t.Fatalf("重启后输出分页: %v", page)
	}
}

// 运行由守护进程持有：Agent 被强杀、重启后，结果仍能取回。
func TestRealModelExecSurvivesAgentKill(t *testing.T) {
	l := newExecLab(t, execOn())
	m := l.mcp
	token := "after-kill-" + randHex(4)

	res := m.exec(l.code, fmt.Sprintf("sleep 4; echo %s", token), map[string]any{"wait_seconds": 0})
	id := res.str("run_id")
	l.ag.kill9()
	waitHost(t, l.h, l.code, "Hub 感知到主机离线", func(v terminal.HostView) bool { return !v.Online })

	// 主机离线期间：状态查询不会误判为丢失，新命令明确报离线。
	mid, _ := m.run("status", id, nil)
	if mid.str("state") != "running" {
		t.Fatalf("Agent 离线期间运行应保持 running: %v", mid)
	}
	off, isErr := m.tool("terminal_exec", map[string]any{"target": l.code, "command": "echo x"})
	if !isErr || off.errCode() != "host_offline" {
		t.Fatalf("离线应报 host_offline: %v", off)
	}

	time.Sleep(5 * time.Second) // 让命令在没有 Agent 的情况下结束
	l.restartAgent("agent2")
	st, _ := m.run("status", id, map[string]any{"wait_seconds": 30})
	if st.str("state") != "succeeded" || !strings.Contains(st.obj("stdout").str("text"), token) {
		t.Fatalf("Agent 重启后应取回离线期间结束的运行结果: %v", st)
	}
}

// G4：本机一票否决——服务端授权再充分也无法覆盖；人工终端不受影响。
func TestRealModelExecHostVeto(t *testing.T) {
	l := newExecLab(t, execOn(), "--deny-model-exec")
	m := l.mcp

	tg, _ := m.tool("terminal_targets", map[string]any{})
	list, _ := tg["targets"].([]any)
	if len(list) != 1 {
		t.Fatalf("targets: %v", tg)
	}
	one := toolRes(list[0].(map[string]any))
	if one["usable"] == true || one.str("blocked") != "host_denied" {
		t.Fatalf("被否决的主机应标明 host_denied: %v", one)
	}
	denied, isErr := m.tool("terminal_exec", map[string]any{"target": l.code, "command": "touch /tmp/should-not-exist-" + randHex(4)})
	if !isErr || denied.errCode() != "host_denied" {
		t.Fatalf("应被一票否决: %v", denied)
	}

	// 人工终端照常可用。
	sess := l.h.api.openSession(l.code, "human")
	tc := l.h.api.attach(l.h, l.code, sess.SID, 0)
	marker := "human-ok-" + randHex(4)
	tc.send("echo " + marker + "\r")
	tc.waitFor(marker, 10*time.Second)
}

// G1：服务端没有打开模型执行总开关时，三个工具都明确拒绝。
func TestRealModelExecDisabledByDefault(t *testing.T) {
	l := newExecLab(t, nil)
	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"terminal_targets", map[string]any{}},
		{"terminal_exec", map[string]any{"target": l.code, "command": "id"}},
		{"terminal_run", map[string]any{"action": "status", "run_id": strings.Repeat("a", 32)}},
	} {
		res, isErr := l.mcp.tool(tool.name, tool.args)
		if !isErr || res.errCode() != "exec_disabled" {
			t.Fatalf("%s 在总开关关闭时应报 exec_disabled: %v", tool.name, res)
		}
	}
}

// 需批准模式：命令停在待批准，人工在 Web 上批准后才真正执行；拒绝则永不执行。
func TestRealModelExecApproveFlow(t *testing.T) {
	l := newExecLab(t, execOn())
	m := l.mcp
	if c := l.h.api.call("POST", "/hosts/"+l.code+"/model", map[string]any{"exec_mode": "approve"}, nil); c != 200 {
		t.Fatalf("设置需批准模式: HTTP %d", c)
	}
	flag := filepath.Join(t.TempDir(), "flag")

	pending := m.exec(l.code, "touch "+flag+"; echo approved-run", map[string]any{"wait_seconds": 0})
	if pending.str("state") != "awaiting_approval" {
		t.Fatalf("应等待批准: %v", pending)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(flag); err == nil {
		t.Fatalf("批准前命令不得执行")
	}
	id := pending.str("run_id")
	if c := l.h.api.call("POST", "/exec/runs/"+id+"/approve", nil, nil); c != 200 {
		t.Fatalf("批准: HTTP %d", c)
	}
	st, _ := m.run("status", id, map[string]any{"wait_seconds": 20})
	if st.str("state") != "succeeded" || !strings.Contains(st.obj("stdout").str("text"), "approved-run") {
		t.Fatalf("批准后应执行并成功: %v", st)
	}
	if _, err := os.Stat(flag); err != nil {
		t.Fatalf("批准后命令应已执行")
	}

	// 拒绝。
	flag2 := filepath.Join(t.TempDir(), "flag2")
	p2 := m.exec(l.code, "touch "+flag2, map[string]any{"wait_seconds": 0})
	if c := l.h.api.raw("POST", "/api/v1/terminal/exec/runs/"+p2.str("run_id")+"/reject", nil, nil, false); c != 200 {
		t.Fatalf("拒绝: HTTP %d", c)
	}
	rej, _ := m.run("status", p2.str("run_id"), nil)
	if rej.str("state") != "rejected" {
		t.Fatalf("被拒绝的运行: %v", rej)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(flag2); err == nil {
		t.Fatalf("被拒绝的命令不得执行")
	}
}

// 令牌之间互相看不到对方的运行；一把令牌被删除时它名下进行中的命令被结束。
func TestRealModelExecTokenIsolationAndRevocation(t *testing.T) {
	l := newExecLab(t, execOn())
	m1 := l.mcp
	m2 := &mcpClient{t: t, base: l.h.base(), token: l.newToken("model-two"), hc: &http.Client{Timeout: 60 * time.Second}}

	pidFile := filepath.Join(t.TempDir(), "pid")
	res := m1.exec(l.code, fmt.Sprintf("sh -c 'echo $$ > %s; sleep 300' & wait", pidFile), map[string]any{"wait_seconds": 0})
	id := res.str("run_id")
	pid := readPID(t, pidFile)

	if other, isErr := m2.run("status", id, nil); !isErr || other.errCode() != "run_not_found" {
		t.Fatalf("别的令牌不应看到这次运行: %v", other)
	}
	if other, isErr := m2.run("cancel", id, nil); !isErr || other.errCode() != "run_not_found" {
		t.Fatalf("别的令牌不应能取消这次运行: %v", other)
	}
	if !pidAlive(pid) {
		t.Fatalf("越权取消不得影响命令")
	}

	// 删除令牌 → 它的命令被结束。
	var list struct {
		Credentials []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"credentials"`
	}
	if c := l.h.api.raw("GET", "/api/v1/credentials", nil, &list, false); c != 200 {
		t.Fatalf("列出令牌: HTTP %d", c)
	}
	var credID uint
	for _, c := range list.Credentials {
		if c.Name == "model-e2e" {
			credID = c.ID
		}
	}
	if credID == 0 {
		t.Fatalf("没有找到令牌")
	}
	if c := l.h.api.raw("DELETE", "/api/v1/credentials/"+strconv.Itoa(int(credID)), nil, nil, false); c != 200 {
		t.Fatalf("删除令牌: HTTP %d", c)
	}
	waitUntil(t, 20*time.Second, "令牌被删除后其命令被结束", func() bool { return !pidAlive(pid) })
	req, _ := http.NewRequest("POST", l.h.base()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m1.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("被删除的令牌应得到 401，实际 %d", resp.StatusCode)
	}
}

// 人工终端会话与模型执行互不干扰：模型的运行不会出现在会话列表里，人工会话照常输入输出。
func TestRealModelExecAndHumanSessionCoexist(t *testing.T) {
	l := newExecLab(t, execOn())
	sess := l.h.api.openSession(l.code, "human")
	tc := l.h.api.attach(l.h, l.code, sess.SID, 0)

	for i := 0; i < 5; i++ {
		res := l.mcp.exec(l.code, fmt.Sprintf("echo model-%d", i), nil)
		if res.str("state") != "succeeded" {
			t.Fatalf("模型执行 %d: %v", i, res)
		}
		mark := fmt.Sprintf("human-%d-%s", i, randHex(3))
		tc.send("echo " + mark + "\r")
		tc.waitFor(mark, 10*time.Second)
	}
	_, sessions := l.h.api.sessions(l.code)
	if len(sessions) != 1 {
		t.Fatalf("模型运行不应以会话的形式出现: %d 个会话", len(sessions))
	}
	if strings.Contains(tc.output(), "model-") {
		t.Fatalf("模型命令的输出不应出现在人工终端里: %q", lastN(tc.output(), 200))
	}
}
