//go:build e2e && !windows

package e2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 混沌场景（方案第 17 节）：网络抖动、客户端磁盘写满。
// Agent 被杀与 Hub 重启已由 exec_real_test.go 的 SurvivesAgentKill / SurvivesHubRestart 覆盖。

// ---------- 一个可以断线、拒绝新连接、卡住数据的 TCP 代理 ----------

type tcpProxy struct {
	ln       net.Listener
	target   string
	mu       sync.Mutex
	cond     *sync.Cond
	conns    map[net.Conn]struct{}
	stalled  bool
	refusing bool
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	p.cond = sync.NewCond(&p.mu)
	go p.accept()
	t.Cleanup(func() {
		_ = ln.Close()
		p.stall(false)
		p.cut()
	})
	return p
}

func (p *tcpProxy) addr() string { return p.ln.Addr().String() }

func (p *tcpProxy) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		refuse := p.refusing
		p.mu.Unlock()
		if refuse {
			_ = c.Close()
			continue
		}
		up, err := net.DialTimeout("tcp", p.target, 3*time.Second)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		p.conns[c], p.conns[up] = struct{}{}, struct{}{}
		p.mu.Unlock()
		go p.pipe(up, c)
		go p.pipe(c, up)
	}
}

func (p *tcpProxy) pipe(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			p.mu.Lock()
			for p.stalled {
				p.cond.Wait()
			}
			p.mu.Unlock()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// stall 为真时，已经读到的数据被扣住不转发（连接保持着）；恢复后原样补发——数据延迟但不丢。
func (p *tcpProxy) stall(on bool) {
	p.mu.Lock()
	p.stalled = on
	p.mu.Unlock()
	p.cond.Broadcast()
}

// refuse 为真时，新来的连接被立刻关闭（模拟网络不可达）。
func (p *tcpProxy) refuse(on bool) {
	p.mu.Lock()
	p.refusing = on
	p.mu.Unlock()
}

// cut 关闭当前所有连接。
func (p *tcpProxy) cut() {
	p.mu.Lock()
	conns := p.conns
	p.conns = map[net.Conn]struct{}{}
	p.mu.Unlock()
	for c := range conns {
		_ = c.Close()
	}
}

// ---------- 网络抖动 ----------

func TestRealChaosNetworkFlap(t *testing.T) {
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin, execOn()...)
	d := newHostDirs(t)
	hp := startHolder(t, agentBin, d)
	px := newTCPProxy(t, fmt.Sprintf("127.0.0.1:%d", h.httpsPort))
	ag := startProc(t, "agent", agentBin, nil, "run", "--server", "https://"+px.addr(), "--pin", h.pin,
		"--data-dir", d.data, "--config-dir", d.cfg, "--log-file", filepath.Join(d.logs, "agent.log"), "--holder", "external")
	code := h.api.enrollOnly()
	ready := func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" }
	waitHost(t, h, code, "主机上线", ready)
	if c := h.api.call("POST", "/hosts/"+code+"/model", map[string]any{"allow_model": true}, nil); c != 200 {
		t.Fatalf("允许模型访问失败: HTTP %d", c)
	}
	l := &execLab{t: t, h: h, agentBin: agentBin, d: d, hp: hp, ag: ag, code: code}
	l.mcp = &mcpClient{t: t, base: h.base(), token: l.newToken("chaos"), hc: &http.Client{Timeout: 90 * time.Second}}
	m := l.mcp
	offline := func(v terminal.HostView) bool { return !v.Online }

	// 前置：一个人工会话（里面有一个前台作业）+ 一个跑 20 秒的模型运行；运行每执行一次就往计数文件里追加一行。
	sess := h.api.openSession(code, "chaos")
	tc := h.api.attach(h, code, sess.SID, 0)
	tc.send("echo ALIVE_$((20+1)); sleep 600 &\n")
	tc.waitFor("ALIVE_21", 10*time.Second)
	counter := filepath.Join(t.TempDir(), "exec-count")
	key := "chaos-" + randHex(4)
	slow := m.exec(code, fmt.Sprintf("sleep 20; echo X >> %s; echo CHAOS_DONE_$((6*7))", counter),
		map[string]any{"wait_seconds": 0, "idempotency_key": key})
	if slow.str("run_id") == "" || slow.str("state") != "running" {
		t.Fatalf("长运行应处于 running: %v", slow)
	}

	// 1) 断线 + 4 秒内拒绝新连接：Agent 重连后主机恢复上线。
	px.refuse(true)
	px.cut()
	waitHost(t, h, code, "断线后主机下线", offline)
	time.Sleep(4 * time.Second)
	px.refuse(false)
	waitHost(t, h, code, "网络恢复后主机重新上线", ready)

	// 2) 卡顿 6 秒（数据被扣住但不丢）：期间新提交的运行在恢复后恰好执行一次。
	counter2 := filepath.Join(t.TempDir(), "exec-count-2")
	key2 := "chaos2-" + randHex(4)
	px.stall(true)
	type submitted struct {
		res toolRes
		err bool
	}
	got := make(chan submitted, 1)
	go func() {
		// 不能在非测试协程里用会 Fatal 的辅助函数，这里直接发请求。
		mc := &mcpClient{t: t, base: h.base(), token: m.token, hc: &http.Client{Timeout: 60 * time.Second}}
		res, isErr := mc.tool("terminal_exec", map[string]any{
			"target": code, "command": fmt.Sprintf("echo Y >> %s; echo STALLED_OK_$((8*8))", counter2),
			"wait_seconds": 30, "idempotency_key": key2,
		})
		got <- submitted{res, isErr}
	}()
	time.Sleep(6 * time.Second)
	px.stall(false)
	var second submitted
	select {
	case second = <-got:
	case <-time.After(60 * time.Second):
		t.Fatal("卡顿恢复后，新运行的调用没有返回")
	}
	if second.err {
		t.Fatalf("卡顿期间提交的运行应在恢复后成功（或明确失败），而不是被拒绝: %v", second.res)
	}
	t.Logf("卡顿期间提交的运行: state=%s", second.res.str("state"))

	// 3) 再断一次，这次不拒绝新连接，Agent 应当很快重连。
	px.cut()
	waitHost(t, h, code, "再次断线后主机下线", offline)
	waitHost(t, h, code, "再次重连后主机上线", ready)

	// 结果：长运行成功完成且只执行了一次；卡顿期间提交的运行也只执行了一次。
	st, isErr := m.run("status", slow.str("run_id"), map[string]any{"wait_seconds": 60})
	if isErr || st.str("state") != "succeeded" || !strings.Contains(st.obj("stdout").str("text"), "CHAOS_DONE_42") {
		t.Fatalf("长运行应在网络抖动后正常完成: %v", st)
	}
	if b, _ := os.ReadFile(counter); strings.Count(string(b), "X") != 1 {
		t.Fatalf("长运行应恰好执行一次，计数文件内容: %q", b)
	}
	st2, isErr := m.run("status", second.res.str("run_id"), map[string]any{"wait_seconds": 30})
	if isErr || st2.str("state") != "succeeded" || !strings.Contains(st2.obj("stdout").str("text"), "STALLED_OK_64") {
		t.Fatalf("卡顿期间提交的运行应完成: %v", st2)
	}
	if b, _ := os.ReadFile(counter2); strings.Count(string(b), "Y") != 1 {
		t.Fatalf("卡顿期间提交的运行应恰好执行一次，计数文件内容: %q", b)
	}
	// 同一个幂等键再发一次：返回的是原来那个运行，命令不会再执行。
	again := m.exec(code, fmt.Sprintf("sleep 20; echo X >> %s; echo CHAOS_DONE_$((6*7))", counter),
		map[string]any{"wait_seconds": 5, "idempotency_key": key})
	if again.str("run_id") != slow.str("run_id") {
		t.Fatalf("相同幂等键应返回原运行 %s，得到 %s", slow.str("run_id"), again.str("run_id"))
	}
	time.Sleep(500 * time.Millisecond)
	if b, _ := os.ReadFile(counter); strings.Count(string(b), "X") != 1 {
		t.Fatalf("幂等重试不应让命令再执行一次，计数文件内容: %q", b)
	}

	// 人工会话：重新附着，会话与后台作业都还在。
	tc2 := h.api.attach(h, code, sess.SID, 0)
	tc2.send("jobs | grep -c sleep; echo STILL_$((5*5))\n")
	tc2.waitFor("STILL_25", 10*time.Second)
	if !strings.Contains(tc2.output(), "ALIVE_21") {
		t.Fatalf("重新附着应回放此前的输出: %q", lastN(tc2.output(), 300))
	}
	for name, p := range map[string]*proc{"守护进程": hp, "Agent": ag, "Hub": h.proc} {
		if !p.alive() {
			t.Errorf("%s 进程在网络抖动中退出了", name)
		}
	}
}

// ---------- 客户端磁盘写满 ----------

// fillDisk 往 dir 里写文件直到磁盘写满，返回写入的文件路径。
func fillDisk(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "zz-filler")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	chunk := make([]byte, 64<<10)
	for {
		if _, err := f.Write(chunk); err != nil {
			return path
		}
	}
}

func TestRealChaosHostDiskFull(t *testing.T) {
	if out, err := exec.Command("sudo", "-n", "true").CombinedOutput(); err != nil {
		t.Skipf("需要免密 sudo 来挂载一个很小的 tmpfs: %v %s", err, out)
	}
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin, execOn()...)
	d := newHostDirs(t)
	// 主机数据目录换成只有 6 MiB 的 tmpfs；清理按后进先出：先停进程，再卸载，最后删目录。
	mountOpts := fmt.Sprintf("size=6m,mode=0700,uid=%d,gid=%d", os.Getuid(), os.Getgid())
	if out, err := exec.Command("sudo", "-n", "mount", "-t", "tmpfs", "-o", mountOpts, "tmpfs", d.data).CombinedOutput(); err != nil {
		t.Skipf("挂载 tmpfs 失败: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("sudo", "-n", "umount", "-l", d.data).Run() })

	hp := startHolder(t, agentBin, d)
	ag := startAgent(t, agentBin, h, d, "agent", "--holder", "external")
	code := h.api.enrollOnly()
	waitHost(t, h, code, "主机上线", func(v terminal.HostView) bool { return v.Online && v.Holder != nil && v.Holder.State == "ready" })
	if c := h.api.call("POST", "/hosts/"+code+"/model", map[string]any{"allow_model": true}, nil); c != 200 {
		t.Fatalf("允许模型访问失败: HTTP %d", c)
	}
	l := &execLab{t: t, h: h, agentBin: agentBin, d: d, hp: hp, ag: ag, code: code}
	l.mcp = &mcpClient{t: t, base: h.base(), token: l.newToken("disk-full"), hc: &http.Client{Timeout: 90 * time.Second}}
	m := l.mcp

	sess := h.api.openSession(code, "disk")
	tc := h.api.attach(h, code, sess.SID, 0)
	tc.send("echo BEFORE_$((1+1))\n")
	tc.waitFor("BEFORE_2", 10*time.Second)

	okRun := func(command, want string) (toolRes, bool) {
		r, e := m.tool("terminal_exec", map[string]any{"target": code, "command": command, "wait_seconds": 30})
		return r, !e && r.str("state") == "succeeded" && strings.Contains(r.obj("stdout").str("text"), want)
	}

	// 阶段一：磁盘彻底写满，没有任何可回收的旧运行——命令照样要能跑（INV-R3）：
	// 这时输出只在内存里保留，结果照常回到模型手里；人工终端也不受影响。
	filler := fillDisk(t, d.data)
	r1, ok1 := okRun("echo FULL_$((4*4))", "FULL_16")
	if !ok1 {
		t.Fatalf("磁盘写满时命令也应能运行并带回结果: %v", r1)
	}
	if page, isErr := m.run("output", r1.str("run_id"), nil); isErr || !strings.Contains(page.str("text"), "FULL_16") {
		t.Fatalf("只在内存里保留的输出应仍可读取: %v", page)
	}
	tc.send("echo DURING_$((7*7))\n")
	tc.waitFor("DURING_49", 10*time.Second)
	for name, p := range map[string]*proc{"守护进程": hp, "Agent": ag, "Hub": h.proc} {
		if !p.alive() {
			t.Fatalf("%s 进程在磁盘写满时退出了", name)
		}
	}
	if err := os.Remove(filler); err != nil {
		t.Fatal(err)
	}

	// 阶段二：输出远大于剩余空间（头部配额 8 MiB > tmpfs 6 MiB），保存输出时写满；运行本身照常结束。
	res, isErr := m.tool("terminal_exec", map[string]any{
		"target": code, "command": "yes abcdefghij | head -c 40000000; echo BIG_DONE_$((3*3))", "wait_seconds": 60,
	})
	if isErr || res.str("state") != "succeeded" {
		t.Fatalf("保存输出写满磁盘时，运行本身应照常结束: isErr=%v %v", isErr, res)
	}
	bigID := res.str("run_id")

	// 阶段三：磁盘被上一个运行的输出和别的东西占满。新的运行会先回收旧的、Hub 已确认的运行腾出空间，然后正常落盘。
	filler = fillDisk(t, d.data)
	r3, ok3 := okRun("echo RECLAIMED_$((5*5))", "RECLAIMED_25")
	if !ok3 {
		t.Fatalf("回收旧运行后新的运行应正常: %v", r3)
	}
	if _, err := os.Stat(filepath.Join(d.data, "runs", bigID)); err == nil {
		t.Fatalf("旧的大输出运行应已被回收，目录却还在")
	}
	if _, isErr := m.run("output", bigID, nil); !isErr {
		t.Fatalf("被回收的运行不应还能读到输出")
	}
	if page, isErr := m.run("output", r3.str("run_id"), nil); isErr || !strings.Contains(page.str("text"), "RECLAIMED_25") {
		t.Fatalf("新运行的输出应已正常落盘并可读取: %v", page)
	}
	if err := os.Remove(filler); err != nil {
		t.Fatal(err)
	}
	tc.send("echo AFTER_$((9*9))\n")
	tc.waitFor("AFTER_81", 10*time.Second)
}
