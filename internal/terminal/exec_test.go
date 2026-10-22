package terminal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 假主机：在假 Agent 之上模拟守护进程的 run / run_read 行为 ----------

const execToken = "rm-0123abcd4567ef890123abcd4567ef890123abcd4567ef89"

func withExec() envOption { return func(o *Options, _ *testEnv) { o.ExecEnabled = true } }

type hostSim struct {
	a *fakeAgent

	mu      sync.Mutex
	runs    []termproto.Run
	cancels []termproto.RunCancel
	acks    []string
	syncs   int
	reads   []termproto.RunRead
	outputs map[string]map[string][]byte // 运行 ID → 流 → 完整输出
	onRun   func(s *hostSim, m termproto.Run)
}

func attachSim(a *fakeAgent) *hostSim {
	s := &hostSim{a: a, outputs: map[string]map[string][]byte{}}
	a.extra = s.handle
	return s
}

func (s *hostSim) handle(t string, data []byte) {
	switch t {
	case termproto.TRun:
		var m termproto.Run
		_ = json.Unmarshal(data, &m)
		s.mu.Lock()
		s.runs = append(s.runs, m)
		hook := s.onRun
		s.mu.Unlock()
		if hook != nil {
			hook(s, m)
		}
	case termproto.TRunCancel:
		var m termproto.RunCancel
		_ = json.Unmarshal(data, &m)
		s.mu.Lock()
		s.cancels = append(s.cancels, m)
		s.mu.Unlock()
	case termproto.TRunAck:
		var m termproto.RunAck
		_ = json.Unmarshal(data, &m)
		s.mu.Lock()
		s.acks = append(s.acks, m.ID)
		s.mu.Unlock()
	case termproto.TRunsSync:
		s.mu.Lock()
		s.syncs++
		s.mu.Unlock()
	case termproto.TRunRead:
		var m termproto.RunRead
		_ = json.Unmarshal(data, &m)
		s.mu.Lock()
		s.reads = append(s.reads, m)
		s.mu.Unlock()
		s.serveRead(m)
	}
}

func (s *hostSim) setOutput(id, stream string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outputs[id] == nil {
		s.outputs[id] = map[string][]byte{}
	}
	s.outputs[id][stream] = data
}

func (s *hostSim) serveRead(m termproto.RunRead) {
	s.mu.Lock()
	data, ok := s.outputs[m.ID][m.Stream]
	s.mu.Unlock()
	if !ok {
		s.a.send(termproto.RunReadFail{T: termproto.TRunReadFail, Req: m.Req, Reason: termproto.RunReadNotFound})
		return
	}
	stream, _ := termproto.ParseStream(m.Stream)
	off := int(m.Off)
	if off > len(data) {
		off = len(data)
	}
	limit := m.Max
	if limit <= 0 {
		limit = termproto.DefaultRunRead
	}
	end := min(len(data), off+limit)
	pos := off
	for {
		n := min(end-pos, termproto.MaxRunDataFrame)
		chunk := data[pos : pos+n]
		pos += n
		flags := termproto.RunFlagEOF
		if pos < len(data) {
			flags = 0
		}
		last := pos >= end
		if last {
			flags |= termproto.RunFlagLast
		}
		s.a.sendBinary(termproto.EncodeRunData(termproto.RunChunk{
			Req: m.Req, Flags: flags, Stream: stream, Off: uint64(pos - n), Next: uint64(pos), Total: uint64(len(data)), Data: chunk,
		}))
		if last {
			return
		}
	}
}

func (s *hostSim) started(id string) {
	s.a.send(termproto.RunStarted{T: termproto.TRunStarted, ID: id, PID: 4242, Shell: "bash", Cwd: "/home/test", Started: time.Now().UnixMilli()})
}

func intp(v int) *int { return &v }

// end 报告运行结束；小输出直接内联，调用方想模拟大输出时用 endFull。
func (s *hostSim) end(id, state string, code *int, stdout, stderr string) {
	e := termproto.RunEnd{
		T: termproto.TRunEnd, ID: id, State: state, Code: code, DurMS: 12,
		OutBytes: int64(len(stdout)), ErrBytes: int64(len(stderr)),
		Out: []byte(stdout), Err: []byte(stderr),
		PrevHead: []byte(stdout + stderr),
	}
	s.a.send(e)
}

func (s *hostSim) endFull(e termproto.RunEnd) {
	e.T = termproto.TRunEnd
	s.a.send(e)
}

func (s *hostSim) reportHolder(denyExec, withExec bool) {
	caps := []string{"policy", "drain", "lost"}
	h := termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test", DenyExec: denyExec}
	if withExec {
		caps = append(caps, "exec")
		h.Exec = &termproto.ExecInfo{Shell: "bash", Home: "/home/test", Sep: "/"}
	}
	h.Caps = caps
	s.a.send(h)
}

func (s *hostSim) runCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

func (s *hostSim) lastRun() termproto.Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[len(s.runs)-1]
}

func (s *hostSim) cancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cancels)
}

func (s *hostSim) hasAck(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.acks {
		if a == id {
			return true
		}
	}
	return false
}

// ---------- 夹具 ----------

type execFixture struct {
	env *testEnv
	a   *fakeAgent
	sim *hostSim
	adm []string
	svc *ExecService
}

func newExecFixture(t *testing.T, opts ...envOption) *execFixture {
	t.Helper()
	env := newTestEnv(t, append([]envOption{withExec()}, opts...)...)
	a := env.enrollAgent(testMeta("exec-host"), "build box")
	sim := attachSim(a)
	f := &execFixture{env: env, a: a, sim: sim, adm: env.admin(), svc: env.hub.Exec()}
	sim.reportHolder(false, true)
	eventually(t, 2*time.Second, "Hub 识别主机的 exec 能力", func() bool {
		ag := env.hub.onlineAgent(a.code)
		if ag == nil {
			return false
		}
		ok, _, _ := ag.execSupport()
		return ok
	})
	f.setModel(t, map[string]any{"allow_model": true})
	return f
}

func (f *execFixture) setModel(t *testing.T, body map[string]any) apiResult {
	t.Helper()
	res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.a.code+"/model", body, f.adm...)
	if res.Status != 200 {
		t.Fatalf("设置模型访问失败: %d %s", res.Status, res.Raw)
	}
	return res
}

func (f *execFixture) caller(mut func(c *termexec.Caller)) termexec.Caller {
	cred := &model.MCPCredential{
		ID: 7, Name: "cursor-main", IsActive: true,
		Permissions:   model.Permissions{Terminal: true},
		TerminalHosts: model.StringList{f.a.code},
	}
	c := termexec.Caller{
		CredentialID: 7, CredentialName: "cursor-main", Credential: cred, Permissions: cred.Permissions,
		TokenString: execToken, Source: "db_credential", RemoteIP: "10.0.0.5", UserAgent: "TestClient/1.0",
	}
	if mut != nil {
		mut(&c)
	}
	return c
}

func (f *execFixture) exec(t *testing.T, c termexec.Caller, req termexec.ExecRequest) (*termexec.RunResult, error) {
	t.Helper()
	res, err := f.svc.Exec(context.Background(), c, req)
	if err != nil {
		return nil, err
	}
	return res.(*termexec.RunResult), nil
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	te, ok := err.(*termexec.Error)
	if !ok || te.Code != code {
		t.Fatalf("期望错误码 %q，得到 %v", code, err)
	}
}

func (f *execFixture) dbRun(t *testing.T, id string) *model.TerminalRun {
	t.Helper()
	rec, err := f.env.store.GetTerminalRun(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("读取运行记录 %s: %v %v", id, rec, err)
	}
	return rec
}

func (f *execFixture) audits(t *testing.T) []model.TerminalAuditLog {
	t.Helper()
	logs, err := f.env.store.ListTerminalAudit(context.Background(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	return logs
}

func (f *execFixture) auditCount(t *testing.T, event string) int {
	n := 0
	for _, l := range f.audits(t) {
		if l.Event == event {
			n++
		}
	}
	return n
}

// completeWith 让假主机对每个 run 立即成功结束。
func (f *execFixture) completeWith(stdout string) {
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.end(m.ID, termproto.RunExited, intp(0), stdout, "")
	}
}

func waitSet(sec int) termexec.ExecRequest {
	return termexec.ExecRequest{WaitSet: true, WaitSeconds: sec}
}

// ---------- 用例 ----------

func TestExecHappyPathAutoMode(t *testing.T) {
	f := newExecFixture(t)
	f.completeWith("hello\n")

	req := waitSet(5)
	req.Target, req.Command, req.Description, req.Thread = "build box", "echo hello", "say hi", "demo"
	res, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.RunSucceeded || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("结果 = %+v", res)
	}
	if res.Stdout == nil || res.Stdout.Text != "hello\n" || res.Stdout.Truncated || res.Notice == "" {
		t.Fatalf("stdout = %+v notice=%q", res.Stdout, res.Notice)
	}
	if res.Mode != "auto" || res.Thread != "demo" || res.Target != f.a.code {
		t.Fatalf("元数据 = %+v", res)
	}

	rec := f.dbRun(t, res.RunID)
	if rec.State != model.RunSucceeded || rec.ActorKind != "model" || rec.CredentialName != "cursor-main" ||
		rec.ClientName != "TestClient/1.0" || rec.Tools != "echo" || rec.Description != "say hi" || rec.EndedAt == nil || rec.StartedAt == nil {
		t.Fatalf("记录 = %+v", rec)
	}
	eventually(t, 2*time.Second, "Hub 向主机确认结束事件", func() bool { return f.sim.hasAck(res.RunID) })
	if f.svc.liveRun(res.RunID) != nil {
		t.Fatalf("终态后不应留在活动索引里")
	}

	var started *model.TerminalAuditLog
	for _, l := range f.audits(t) {
		if l.Event == EvExecStart {
			cp := l
			started = &cp
		}
	}
	if started == nil || started.ActorKind != "model" || started.RunID != res.RunID || started.Actor != "cursor-main" || started.MachineCode != f.a.code {
		t.Fatalf("exec_start 审计 = %+v", started)
	}
	if strings.Contains(started.Detail, "echo") {
		t.Fatalf("审计不得包含命令文本: %q", started.Detail)
	}
}

func TestExecDoesNotStoreOutputAndRedactsSecrets(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.endFull(termproto.RunEnd{
			ID: m.ID, State: termproto.RunExited, Code: intp(0), OutBytes: 40,
			Out:      []byte("password=SuperSecretValue and more text\n"),
			PrevHead: []byte("password=SuperSecretValue and more text\n"),
			PrevTail: []byte("\x1b[31mtail with token=abcdef123456\x1b[0m"),
		})
	}
	req := waitSet(5)
	req.Target, req.Command = f.a.code, "deploy --password hunter2 https://u:pw1234@host/x"
	res, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	// 模型自己看得到命令的真实输出（这是它要的结果）……
	if !strings.Contains(res.Stdout.Text, "SuperSecretValue") {
		t.Fatalf("模型应拿到真实输出: %+v", res.Stdout)
	}
	// ……但落库的命令与预览都已脱敏、去控制序列，且没有任何字段保存完整输出。
	rec := f.dbRun(t, res.RunID)
	blob, _ := json.Marshal(rec)
	for _, secret := range []string{"hunter2", "pw1234", "SuperSecretValue", "abcdef123456", "\\u001b"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("数据库记录泄露了 %q: %s", secret, blob)
		}
	}
	if !strings.Contains(rec.Command, "deploy") || !strings.Contains(rec.PreviewTail, "tail with") {
		t.Fatalf("脱敏不应吞掉无关内容: %+v", rec)
	}
}

func TestExecGatePipeline(t *testing.T) {
	t.Run("G1 总开关关闭时静默拒绝且不写审计", func(t *testing.T) {
		env := newTestEnv(t) // 未启用 ExecEnabled
		c := termexec.Caller{CredentialID: 1, Credential: &model.MCPCredential{ID: 1, Permissions: model.Permissions{Terminal: true}}, Permissions: model.Permissions{Terminal: true}, TokenString: execToken}
		for name, call := range map[string]func() error{
			"targets": func() error { _, err := env.hub.Exec().Targets(context.Background(), c, ""); return err },
			"exec": func() error {
				_, err := env.hub.Exec().Exec(context.Background(), c, termexec.ExecRequest{Target: "x", Command: "id"})
				return err
			},
			"run": func() error {
				_, err := env.hub.Exec().Run(context.Background(), c, termexec.RunRequest{RunID: strings.Repeat("a", 32)})
				return err
			},
		} {
			wantCode(t, call(), termexec.CodeDisabled)
			_ = name
		}
		logs, _ := env.store.ListTerminalAudit(context.Background(), 50, 0)
		for _, l := range logs {
			if l.Event == EvExecDenied {
				t.Fatalf("G1 关闭时不应写 exec_denied 审计: %+v", l)
			}
		}
	})

	f := newExecFixture(t)
	f.completeWith("ok\n")
	base := termexec.ExecRequest{Target: f.a.code, Command: "id"}
	deniedBefore := f.auditCount(t, EvExecDenied)

	t.Run("令牌强度不足", func(t *testing.T) {
		_, err := f.exec(t, f.caller(func(c *termexec.Caller) { c.TokenString = "short" }), base)
		wantCode(t, err, termexec.CodeWeakToken)
	})
	t.Run("没有终端权限位或不是数据库令牌", func(t *testing.T) {
		_, err := f.exec(t, f.caller(func(c *termexec.Caller) { c.Permissions = model.Permissions{Feedback: true} }), base)
		wantCode(t, err, termexec.CodeNotAllowed)
		_, err = f.exec(t, f.caller(func(c *termexec.Caller) { c.Credential = nil }), base)
		wantCode(t, err, termexec.CodeNotAllowed)
	})
	t.Run("白名单之外的主机等同不存在", func(t *testing.T) {
		_, err := f.exec(t, f.caller(func(c *termexec.Caller) { c.Credential.TerminalHosts = model.StringList{strings.Repeat("0", 16)} }), base)
		wantCode(t, err, termexec.CodeHostNotFound)
	})
	t.Run("通配白名单", func(t *testing.T) {
		res, err := f.exec(t, f.caller(func(c *termexec.Caller) { c.Credential.TerminalHosts = model.StringList{"*"} }), withTarget(waitSet(5), f.a.code, "id"))
		if err != nil || res.State != model.RunSucceeded {
			t.Fatalf("通配应放行: %v %+v", err, res)
		}
	})
	t.Run("G2 管理员未允许模型访问", func(t *testing.T) {
		f.setModel(t, map[string]any{"allow_model": false})
		_, err := f.exec(t, f.caller(nil), base)
		wantCode(t, err, termexec.CodeHostOff)
		f.setModel(t, map[string]any{"allow_model": true})
	})
	t.Run("G5 单机暂停与全局暂停", func(t *testing.T) {
		if r := f.env.call("POST", "/api/v1/terminal/exec/pause", map[string]any{"scope": "host", "machine_code": f.a.code}, "Authorization", humanAuth(testUser)); r.Status != 200 {
			t.Fatalf("暂停单机: %d %s", r.Status, r.Raw)
		}
		_, err := f.exec(t, f.caller(nil), base)
		wantCode(t, err, termexec.CodePaused)
		// 恢复需要解锁
		if r := f.env.call("POST", "/api/v1/terminal/exec/resume", map[string]any{"scope": "host", "machine_code": f.a.code}, "Authorization", humanAuth(testUser)); r.Status == 200 {
			t.Fatalf("恢复不应只靠登录")
		}
		if r := f.env.call("POST", "/api/v1/terminal/exec/resume", map[string]any{"scope": "host", "machine_code": f.a.code}, f.adm...); r.Status != 200 {
			t.Fatalf("恢复单机: %d %s", r.Status, r.Raw)
		}
		if r := f.env.call("POST", "/api/v1/terminal/exec/pause", map[string]any{"scope": "all"}, "Authorization", humanAuth(testUser)); r.Status != 200 {
			t.Fatalf("全局暂停: %d %s", r.Status, r.Raw)
		}
		_, err = f.exec(t, f.caller(nil), base)
		wantCode(t, err, termexec.CodePaused)
		if r := f.env.call("POST", "/api/v1/terminal/exec/resume", map[string]any{"scope": "all"}, f.adm...); r.Status != 200 {
			t.Fatalf("全局恢复: %d %s", r.Status, r.Raw)
		}
	})
	t.Run("只读模式（主机设置或令牌上限）", func(t *testing.T) {
		f.setModel(t, map[string]any{"exec_mode": "readonly"})
		_, err := f.exec(t, f.caller(nil), base)
		wantCode(t, err, termexec.CodeReadonly)
		f.setModel(t, map[string]any{"exec_mode": ""})
		_, err = f.exec(t, f.caller(func(c *termexec.Caller) { c.Credential.TerminalModeCap = model.TerminalModeReadonly }), base)
		wantCode(t, err, termexec.CodeReadonly)
		// 只读仍允许 terminal_targets 查看
		out, err := f.svc.Targets(context.Background(), f.caller(func(c *termexec.Caller) { c.Credential.TerminalModeCap = model.TerminalModeReadonly }), "")
		if err != nil {
			t.Fatal(err)
		}
		tg := out.(*termexec.TargetsResult).Targets
		if len(tg) != 1 || tg[0].Usable || tg[0].Blocked != termexec.CodeReadonly || tg[0].Mode != "readonly" {
			t.Fatalf("targets = %+v", tg)
		}
	})
	t.Run("令牌上限不能放宽主机设置", func(t *testing.T) {
		f.setModel(t, map[string]any{"exec_mode": "approve"})
		if m := effectiveMode("approve", ""); m != "approve" {
			t.Fatalf("mode = %s", m)
		}
		if m := effectiveMode("approve", "auto"); m != "approve" {
			t.Fatalf("令牌写 auto 不应放宽主机的 approve，得到 %s", m)
		}
		if m := effectiveMode("auto", "approve"); m != "approve" {
			t.Fatalf("令牌上限应收紧，得到 %s", m)
		}
		f.setModel(t, map[string]any{"exec_mode": ""})
	})
	t.Run("被拒绝的请求都有审计（限流）", func(t *testing.T) {
		if got := f.auditCount(t, EvExecDenied); got <= deniedBefore {
			t.Fatalf("拒绝没有写审计: before=%d after=%d", deniedBefore, got)
		}
	})
	t.Run("目标歧义时要求机器码", func(t *testing.T) {
		hosts := []model.TerminalHost{{MachineCode: "aaaaaaaaaaaaaaaa", Hostname: "web"}, {MachineCode: "bbbbbbbbbbbbbbbb", Remark: "WEB"}}
		_, terr := resolveTarget(hosts, "web")
		if terr == nil || terr.Code != termexec.CodeHostNotFound || !strings.Contains(terr.Message, "aaaaaaaaaaaaaaaa") {
			t.Fatalf("歧义应报错并列出机器码: %+v", terr)
		}
		if h, terr := resolveTarget(hosts, "AAAAAAAAAAAAAAAA"); terr != nil || h.MachineCode != "aaaaaaaaaaaaaaaa" {
			t.Fatalf("机器码应不区分大小写: %v %v", h, terr)
		}
		if _, terr := resolveTarget(hosts[:1], ""); terr != nil {
			t.Fatalf("只有一台可见主机时允许省略 target")
		}
		if _, terr := resolveTarget(hosts, ""); terr == nil || terr.Code != termexec.CodeBadRequest {
			t.Fatalf("多台主机必须指定 target")
		}
	})
}

func withTarget(r termexec.ExecRequest, target, cmd string) termexec.ExecRequest {
	r.Target, r.Command = target, cmd
	return r
}

func TestExecHostGates(t *testing.T) {
	t.Run("本机一票否决（守护进程上报 deny_exec）", func(t *testing.T) {
		f := newExecFixture(t)
		f.sim.reportHolder(true, true)
		eventually(t, 2*time.Second, "Hub 看到 deny_exec", func() bool {
			_, denied, _ := f.env.hub.onlineAgent(f.a.code).execSupport()
			return denied
		})
		_, err := f.exec(t, f.caller(nil), withTarget(waitSet(1), f.a.code, "id"))
		wantCode(t, err, termexec.CodeHostDenied)
		if f.sim.runCount() != 0 {
			t.Fatalf("被否决的请求不应下发到主机")
		}
	})
	t.Run("旧版守护进程（无 exec 能力）", func(t *testing.T) {
		f := newExecFixture(t)
		f.sim.reportHolder(false, false)
		eventually(t, 2*time.Second, "Hub 看到 exec 能力消失", func() bool {
			ok, _, _ := f.env.hub.onlineAgent(f.a.code).execSupport()
			return !ok
		})
		_, err := f.exec(t, f.caller(nil), withTarget(waitSet(1), f.a.code, "id"))
		wantCode(t, err, termexec.CodeHostLegacy)
	})
	t.Run("主机离线", func(t *testing.T) {
		f := newExecFixture(t)
		f.a.close()
		eventually(t, 3*time.Second, "Hub 注销离线 Agent", func() bool { return f.env.hub.onlineAgent(f.a.code) == nil })
		_, err := f.exec(t, f.caller(nil), withTarget(waitSet(1), f.a.code, "id"))
		wantCode(t, err, termexec.CodeHostOffline)
	})
	t.Run("主机同样可以在执行时返回 run_failed（Agent 侧否决）", func(t *testing.T) {
		f := newExecFixture(t)
		f.sim.onRun = func(s *hostSim, m termproto.Run) {
			s.a.send(termproto.RunFailed{T: termproto.TRunFailed, ID: m.ID, Reason: termproto.RunFailDenied})
		}
		res, err := f.exec(t, f.caller(nil), withTarget(waitSet(5), f.a.code, "id"))
		if err != nil {
			t.Fatal(err)
		}
		if res.State != model.RunDenied || res.Reason != termexec.CodeHostDenied || res.Message == "" {
			t.Fatalf("res = %+v", res)
		}
		if f.dbRun(t, res.RunID).State != model.RunDenied {
			t.Fatalf("库里的状态应为 denied")
		}
	})
}

func TestExecLimitsAndClamping(t *testing.T) {
	f := newExecFixture(t)
	f.completeWith("x")

	t.Run("目录白名单", func(t *testing.T) {
		c := f.caller(func(c *termexec.Caller) { c.Credential.TerminalLimits.AllowedDirs = []string{"/srv/app", "C:\\Work"} })
		req := withTarget(waitSet(3), f.a.code, "ls")
		req.Cwd = "/srv/app/sub/../src"
		if res, err := f.exec(t, c, req); err != nil || res.State != model.RunSucceeded {
			t.Fatalf("白名单内的目录应放行: %v %+v", err, res)
		}
		req.Cwd = "/srv/application"
		_, err := f.exec(t, c, req)
		wantCode(t, err, termexec.CodeLimit)
		req.Cwd = "/srv/app/../etc"
		_, err = f.exec(t, c, req)
		wantCode(t, err, termexec.CodeLimit)
		req.Cwd = ""
		_, err = f.exec(t, c, req) // 默认家目录不在白名单内
		wantCode(t, err, termexec.CodeLimit)
	})
	t.Run("dirAllowed", func(t *testing.T) {
		cases := []struct {
			cwd     string
			allowed []string
			want    bool
		}{
			{"/srv/app", []string{"/srv/app"}, true},
			{"/srv/app/", []string{"/srv/app"}, true},
			{"/srv/app/x", []string{"/srv/app/"}, true},
			{"/srv/apple", []string{"/srv/app"}, false},
			{"/srv/app/../x", []string{"/srv/app"}, false},
			{"/anything", []string{"/"}, true},
			{"c:\\work\\p", []string{"C:\\Work"}, true},
			{"D:\\work", []string{"C:\\Work"}, false},
		}
		for _, tc := range cases {
			if got := dirAllowed(tc.cwd, tc.allowed); got != tc.want {
				t.Errorf("dirAllowed(%q,%v) = %v, want %v", tc.cwd, tc.allowed, got, tc.want)
			}
		}
	})
	t.Run("参数被令牌限额调整并如实告知", func(t *testing.T) {
		c := f.caller(func(c *termexec.Caller) {
			c.Credential.TerminalLimits = model.TerminalLimits{MaxWaitSeconds: 2, MaxTimeoutSeconds: 10, MaxOutputBytes: 2048}
		})
		req := withTarget(waitSet(100), f.a.code, "id")
		req.TimeoutSeconds, req.MaxOutputBytes = 999, 100000
		res, err := f.exec(t, c, req)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Adjusted) != 3 {
			t.Fatalf("应记录 3 项调整: %v", res.Adjusted)
		}
		if got := f.sim.lastRun().TimeoutSec; got != 10 {
			t.Fatalf("下发给主机的超时 = %d, want 10", got)
		}
	})
	// 回归（#74）：超出系统上限的参数曾被悄悄收进范围——模型传了 30 天的超时，实际只有 7 天，且毫不知情。
	t.Run("超出系统上限的参数被收进范围并如实告知", func(t *testing.T) {
		req := withTarget(waitSet(100000), f.a.code, "id")
		req.TimeoutSeconds, req.MaxOutputBytes = 30*24*3600, 10<<20
		res, err := f.exec(t, f.caller(nil), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Adjusted) != 3 {
			t.Fatalf("应记录 3 项调整（wait / timeout / max_output）: %v", res.Adjusted)
		}
		joined := strings.Join(res.Adjusted, "\n")
		for _, want := range []string{"wait_seconds=100000", "已调整为 120", "timeout_seconds=2592000", strconv.Itoa(termproto.MaxRunTimeoutSec), "max_output_bytes=10485760", strconv.Itoa(execMaxOutput)} {
			if !strings.Contains(joined, want) {
				t.Errorf("adjusted 缺少 %q:\n%s", want, joined)
			}
		}
		if got := f.sim.lastRun().TimeoutSec; got != termproto.MaxRunTimeoutSec {
			t.Fatalf("下发给主机的超时 = %d, want %d", got, termproto.MaxRunTimeoutSec)
		}
	})
	t.Run("恰好等于上限的参数不算调整", func(t *testing.T) {
		req := withTarget(waitSet(execMaxWait), f.a.code, "id")
		req.TimeoutSeconds, req.MaxOutputBytes = termproto.MaxRunTimeoutSec, execMaxOutput
		res, err := f.exec(t, f.caller(nil), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Adjusted) != 0 {
			t.Fatalf("上限本身是合法值，不该被报告成调整: %v", res.Adjusted)
		}
		if got := f.sim.lastRun().TimeoutSec; got != termproto.MaxRunTimeoutSec {
			t.Fatalf("7 天的超时应原样下发: %d", got)
		}
	})
	t.Run("max_output_bytes 低于下限也如实告知", func(t *testing.T) {
		req := withTarget(waitSet(3), f.a.code, "id")
		req.MaxOutputBytes = 100
		res, err := f.exec(t, f.caller(nil), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Adjusted) != 1 || !strings.Contains(res.Adjusted[0], "max_output_bytes=100") || !strings.Contains(res.Adjusted[0], "已调整为 "+strconv.Itoa(execMinOutput)) {
			t.Fatalf("过小的 max_output_bytes 应被报告: %v", res.Adjusted)
		}
	})
	t.Run("负数一直是被直接拒绝的（不是悄悄收进范围）", func(t *testing.T) {
		req := withTarget(waitSet(3), f.a.code, "id")
		req.TimeoutSeconds = -5
		if _, err := f.exec(t, f.caller(nil), req); err == nil || !strings.Contains(err.Error(), "不能为负数") {
			t.Fatalf("负的超时应被拒绝: %v", err)
		}
	})
	t.Run("默认参数", func(t *testing.T) {
		if _, err := f.exec(t, f.caller(nil), withTarget(waitSet(3), f.a.code, "id")); err != nil {
			t.Fatal(err)
		}
		if got := f.sim.lastRun().TimeoutSec; got != termproto.DefaultRunTimeoutSec {
			t.Fatalf("默认超时 = %d", got)
		}
	})
	t.Run("非法参数", func(t *testing.T) {
		for name, req := range map[string]termexec.ExecRequest{
			"空命令":      {Target: f.a.code, Command: "  "},
			"超长命令":     {Target: f.a.code, Command: strings.Repeat("a", termproto.MaxRunCommandBytes+1)},
			"含 NUL":    {Target: f.a.code, Command: "a\x00b"},
			"无效 UTF8":  {Target: f.a.code, Command: "a\xffb"},
			"未知 shell": {Target: f.a.code, Command: "id", Shell: "fish"},
			"负数超时":     {Target: f.a.code, Command: "id", TimeoutSeconds: -1},
			"cwd 含换行":  {Target: f.a.code, Command: "id", Cwd: "/a\n/b"},
		} {
			_, err := f.exec(t, f.caller(nil), req)
			if te, ok := err.(*termexec.Error); !ok || te.Code != termexec.CodeBadRequest {
				t.Errorf("%s: 期望 bad_request，得到 %v", name, err)
			}
		}
	})
	t.Run("并发上限", func(t *testing.T) {
		f.sim.onRun = nil // 运行不结束
		c := f.caller(func(c *termexec.Caller) { c.Credential.TerminalLimits.MaxConcurrent = 1 })
		first, err := f.exec(t, c, withTarget(waitSet(0), f.a.code, "sleep 100"))
		if err != nil || first.State != model.RunRunning {
			t.Fatalf("第一个应在运行: %v %+v", err, first)
		}
		_, err = f.exec(t, c, withTarget(waitSet(0), f.a.code, "sleep 100"))
		wantCode(t, err, termexec.CodeLimit)
		f.sim.started(first.RunID)
		f.sim.end(first.RunID, termproto.RunExited, intp(0), "", "")
		eventually(t, 2*time.Second, "第一个运行结束释放名额", func() bool { return f.svc.liveRun(first.RunID) == nil })
		if _, err := f.exec(t, c, withTarget(waitSet(0), f.a.code, "sleep 100")); err != nil {
			t.Fatalf("名额释放后应放行: %v", err)
		}
	})
}

func TestExecLongRunningStatusCancelAndOwnership(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) } // 只开始，不结束

	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(1), f.a.code, "sleep 300"))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.RunRunning || res.Hint == "" || res.Stdout != nil {
		t.Fatalf("等待超时应返回 running 与提示: %+v", res)
	}
	eventually(t, 2*time.Second, "run_started 生效", func() bool {
		lr := f.svc.liveRun(res.RunID)
		return lr != nil && lr.snapshot().StartedAt != nil
	})

	run := func(c termexec.Caller, r termexec.RunRequest) (any, error) {
		return f.svc.Run(context.Background(), c, r)
	}

	// 别的令牌看不到这次运行，表现与不存在一致，也无法取消。
	other := f.caller(func(c *termexec.Caller) { c.CredentialID = 8; c.Credential.ID = 8; c.CredentialName = "other" })
	_, err = run(other, termexec.RunRequest{Action: "status", RunID: res.RunID})
	wantCode(t, err, termexec.CodeNotFound)
	_, err = run(other, termexec.RunRequest{Action: "cancel", RunID: res.RunID})
	wantCode(t, err, termexec.CodeNotFound)
	if f.sim.cancelCount() != 0 {
		t.Fatalf("越权取消不应触达主机")
	}

	// status 带等待：到期仍在运行。
	st, err := run(f.caller(nil), termexec.RunRequest{Action: "status", RunID: res.RunID, WaitSeconds: 1})
	if err != nil || st.(*termexec.RunResult).State != model.RunRunning {
		t.Fatalf("status = %+v %v", st, err)
	}

	// cancel：Hub 向主机发取消；主机回报 cancelled 后调用返回终态。
	go func() {
		eventually(t, 3*time.Second, "主机收到取消", func() bool { return f.sim.cancelCount() > 0 })
		f.sim.end(res.RunID, termproto.RunCancelled, nil, "partial\n", "")
	}()
	cr, err := run(f.caller(nil), termexec.RunRequest{Action: "cancel", RunID: res.RunID})
	if err != nil {
		t.Fatal(err)
	}
	if got := cr.(*termexec.RunResult); got.State != model.RunCancelled {
		t.Fatalf("cancel 结果 = %+v", got)
	}
	if rec := f.dbRun(t, res.RunID); rec.State != model.RunCancelled {
		t.Fatalf("库中状态 = %s", rec.State)
	}
	// 取消一个已结束的运行是无害的。
	again, err := run(f.caller(nil), termexec.RunRequest{Action: "cancel", RunID: res.RunID})
	if err != nil || again.(*termexec.RunResult).State != model.RunCancelled {
		t.Fatalf("重复取消: %+v %v", again, err)
	}
	if f.auditCount(t, EvExecCancel) != 1 {
		t.Fatalf("取消审计条数 = %d", f.auditCount(t, EvExecCancel))
	}
}

func TestExecLargeOutputIsPagedFromHost(t *testing.T) {
	f := newExecFixture(t)
	big := bytes.Repeat([]byte("0123456789abcdef"), 16*1024) // 256 KiB
	errText := "warn: something\n"
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.setOutput(m.ID, "stdout", big)
		s.setOutput(m.ID, "stderr", []byte(errText))
		s.endFull(termproto.RunEnd{
			ID: m.ID, State: termproto.RunExited, Code: intp(2), OutBytes: int64(len(big)), ErrBytes: int64(len(errText)),
			Err: []byte(errText), PrevHead: big[:512], PrevTail: big[len(big)-512:],
		})
	}
	req := waitSet(5)
	req.Target, req.Command, req.MaxOutputBytes = f.a.code, "gen", 64<<10
	res, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.RunFailed || res.ExitCode == nil || *res.ExitCode != 2 {
		t.Fatalf("非 0 退出码应为 failed: %+v", res)
	}
	out := res.Stdout
	if out == nil || !out.Truncated || len(out.Text) != 64<<10 || out.NextOffset != 64<<10 || out.Bytes != int64(len(big)) {
		t.Fatalf("stdout = truncated=%v len=%d next=%d bytes=%d", out.Truncated, len(out.Text), out.NextOffset, out.Bytes)
	}
	if len(out.Tail) != execTailBytes || out.Tail != string(big[len(big)-execTailBytes:]) {
		t.Fatalf("tail 应是输出最后 %d 字节，实际长度 %d", execTailBytes, len(out.Tail))
	}
	if res.Stderr == nil || res.Stderr.Text != errText || res.Stderr.Truncated {
		t.Fatalf("stderr = %+v", res.Stderr)
	}
	if res.Hint == "" {
		t.Fatalf("截断时应提示如何继续读取")
	}

	// 用 output 分页读完整个输出。
	var got []byte
	next := int64(0)
	for i := 0; i < 20; i++ {
		page, err := f.svc.Run(context.Background(), f.caller(nil), termexec.RunRequest{Action: "output", RunID: res.RunID, Offset: next, Limit: 100 << 10})
		if err != nil {
			t.Fatal(err)
		}
		p := page.(*termexec.OutputPage)
		if p.Offset != next || p.TotalBytes != int64(len(big)) {
			t.Fatalf("page = %+v", p)
		}
		got = append(got, p.Text...)
		next = p.NextOffset
		if p.EOF {
			break
		}
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("分页读取的内容与原输出不一致（got %d bytes）", len(got))
	}
	// stderr 也可读；非法流名被拒绝。
	if _, err := f.svc.Run(context.Background(), f.caller(nil), termexec.RunRequest{Action: "output", RunID: res.RunID, Stream: "stderr"}); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Run(context.Background(), f.caller(nil), termexec.RunRequest{Action: "output", RunID: res.RunID, Stream: "fd3"})
	wantCode(t, err, termexec.CodeBadRequest)
}

func TestExecOutputPagingKeepsMultibyteBoundaries(t *testing.T) {
	f := newExecFixture(t)
	text := strings.Repeat("你好世界，这是一段中文输出。\n", 4000) // 多字节字符，页边界必然落在字符中间
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.setOutput(m.ID, "stdout", []byte(text))
		s.endFull(termproto.RunEnd{ID: m.ID, State: termproto.RunExited, Code: intp(0), OutBytes: int64(len(text))})
	}
	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(5), f.a.code, "gen"))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	next := int64(0)
	for i := 0; i < 400; i++ {
		page, err := f.svc.Run(context.Background(), f.caller(nil), termexec.RunRequest{Action: "output", RunID: res.RunID, Offset: next, Limit: 1025})
		if err != nil {
			t.Fatal(err)
		}
		p := page.(*termexec.OutputPage)
		if strings.Contains(p.Text, "\uFFFD") {
			t.Fatalf("第 %d 页出现替换字符：页边界没有对齐字符", i)
		}
		if p.NextOffset <= next && !p.EOF {
			t.Fatalf("分页没有前进: %+v", p)
		}
		sb.WriteString(p.Text)
		next = p.NextOffset
		if p.EOF {
			break
		}
	}
	if sb.String() != text {
		t.Fatalf("多字节分页拼接结果与原文不一致（%d vs %d）", sb.Len(), len(text))
	}
}

func TestExecApproveRejectAndExpiry(t *testing.T) {
	f := newExecFixture(t, withFakeClock())
	f.setModel(t, map[string]any{"exec_mode": "approve"})
	f.completeWith("approved output\n")

	submit := func(cmd string) *termexec.RunResult {
		res, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, cmd))
		if err != nil {
			t.Fatal(err)
		}
		if res.State != model.RunAwaitingApproval || res.Mode != "approve" || res.Hint == "" {
			t.Fatalf("需批准模式应停在 awaiting_approval: %+v", res)
		}
		return res
	}

	// 批准 → 下发 → 完成
	r1 := submit("echo one")
	if f.sim.runCount() != 0 {
		t.Fatalf("批准前不得下发")
	}
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+r1.RunID+"/approve", nil, "Authorization", humanAuth(testUser)); r.Status == 200 {
		t.Fatalf("批准必须解锁")
	}
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+r1.RunID+"/approve", nil, f.adm...); r.Status != 200 {
		t.Fatalf("批准失败: %d %s", r.Status, r.Raw)
	}
	eventually(t, 3*time.Second, "批准后命令下发并完成", func() bool { return f.dbRun(t, r1.RunID).State == model.RunSucceeded })
	rec := f.dbRun(t, r1.RunID)
	if rec.ApprovedBy != testUser || rec.ApprovedAt == nil {
		t.Fatalf("应记录批准人: %+v", rec)
	}
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+r1.RunID+"/approve", nil, f.adm...); r.Status != 409 || r.Body["code"] != "not_awaiting" {
		t.Fatalf("重复批准一个已结束的运行应 409 not_awaiting，得到 %d %s", r.Status, r.Raw)
	}
	// 人点取消的瞬间命令刚好结束：幂等地返回最终记录，不报「不存在」。
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+r1.RunID+"/cancel", nil, "Authorization", humanAuth(testUser)); r.Status != 200 {
		t.Fatalf("取消一个刚结束的运行应幂等成功，得到 %d %s", r.Status, r.Raw)
	}
	if rec := f.dbRun(t, r1.RunID); rec.State != model.RunSucceeded {
		t.Fatalf("幂等取消不得改写最终状态: %s", rec.State)
	}
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+r1.RunID+"/reject", nil, "Authorization", humanAuth(testUser)); r.Status != 409 {
		t.Fatalf("拒绝一个已成功的运行应 409，得到 %d %s", r.Status, r.Raw)
	}
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+strings.Repeat("ab", 16)+"/cancel", nil, "Authorization", humanAuth(testUser)); r.Status != 404 {
		t.Fatalf("取消一个根本不存在的运行应 404，得到 %d", r.Status)
	}

	// 拒绝（登录即可，越快越好）
	r2 := submit("echo two")
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+r2.RunID+"/reject", nil, "Authorization", humanAuth(testUser)); r.Status != 200 {
		t.Fatalf("拒绝失败: %d %s", r.Status, r.Raw)
	}
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+r2.RunID+"/reject", nil, "Authorization", humanAuth(testUser)); r.Status != 200 {
		t.Fatalf("重复拒绝一个已被拒绝的运行应幂等成功: %d %s", r.Status, r.Raw)
	}
	if rec := f.dbRun(t, r2.RunID); rec.State != model.RunRejected || rec.DenyReason != "rejected_by_user" {
		t.Fatalf("rec = %+v", rec)
	}
	st, _ := f.svc.Run(context.Background(), f.caller(nil), termexec.RunRequest{Action: "status", RunID: r2.RunID})
	if got := st.(*termexec.RunResult); got.State != model.RunRejected || got.Message == "" {
		t.Fatalf("模型应看到拒绝原因: %+v", got)
	}

	// 超时未批准 → 作废
	r3 := submit("echo three")
	f.env.clock.Advance(execApprovalTTL + time.Minute)
	f.svc.maintain(f.env.clock.Now())
	if rec := f.dbRun(t, r3.RunID); rec.State != model.RunRejected || rec.DenyReason != "approval_timeout" {
		t.Fatalf("rec = %+v", rec)
	}

	// 模型撤回待批准的命令
	r4 := submit("echo four")
	cr, err := f.svc.Run(context.Background(), f.caller(nil), termexec.RunRequest{Action: "cancel", RunID: r4.RunID})
	if err != nil || cr.(*termexec.RunResult).State != model.RunCancelled {
		t.Fatalf("撤回: %+v %v", cr, err)
	}
	if f.sim.runCount() != 1 {
		t.Fatalf("只有被批准的那一条应下发过，实际 %d", f.sim.runCount())
	}

	// 批准时重新检查闸门：等待期间管理员收回了授权。
	r5 := submit("echo five")
	f.setModel(t, map[string]any{"allow_model": false})
	if r := f.env.call("POST", "/api/v1/terminal/exec/runs/"+r5.RunID+"/approve", nil, f.adm...); r.Status == 200 {
		t.Fatalf("授权被收回后批准不应成功")
	}
	if rec := f.dbRun(t, r5.RunID); !model.IsFinalRunState(rec.State) {
		t.Fatalf("收回授权后待批准项应终结: %s", rec.State)
	}
	if f.sim.runCount() != 1 {
		t.Fatalf("收回授权后不得下发")
	}
}

func TestExecIdempotencyKey(t *testing.T) {
	f := newExecFixture(t)
	f.completeWith("once\n")
	req := withTarget(waitSet(5), f.a.code, "echo once")
	req.IdempotencyKey = "retry-1"

	first, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.exec(t, f.caller(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.RunID != first.RunID || f.sim.runCount() != 1 {
		t.Fatalf("同一幂等键不得重复执行: run1=%s run2=%s sent=%d", first.RunID, second.RunID, f.sim.runCount())
	}
	if len(second.Warnings) == 0 {
		t.Fatalf("应提示幂等命中")
	}
	// 另一个令牌用同一个键是另一次独立的执行。
	other := f.caller(func(c *termexec.Caller) { c.CredentialID = 9; c.Credential.ID = 9 })
	third, err := f.exec(t, other, req)
	if err != nil || third.RunID == first.RunID || f.sim.runCount() != 2 {
		t.Fatalf("幂等键按令牌隔离: %v %+v sent=%d", err, third, f.sim.runCount())
	}
}

func TestExecPauseCancelsActiveRuns(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300"))
	if err != nil {
		t.Fatal(err)
	}
	if r := f.env.call("POST", "/api/v1/terminal/exec/pause", map[string]any{"scope": "all"}, "Authorization", humanAuth(testUser)); r.Status != 200 {
		t.Fatalf("暂停: %d %s", r.Status, r.Raw)
	}
	eventually(t, 3*time.Second, "暂停时主机收到取消", func() bool { return f.sim.cancelCount() > 0 })
	f.sim.mu.Lock()
	reason := f.sim.cancels[0].Reason
	f.sim.mu.Unlock()
	if reason != "pause" {
		t.Fatalf("取消原因 = %q", reason)
	}
	f.sim.end(res.RunID, termproto.RunCancelled, nil, "", "")
	eventually(t, 2*time.Second, "运行终结", func() bool { return f.dbRun(t, res.RunID).State == model.RunCancelled })

	// 暂停状态持久化（重启后保持）。
	if on, _ := f.env.store.GetSystemFlag(context.Background(), store.SettingTerminalExecPaused); !on {
		t.Fatalf("全局暂停应写入持久开关")
	}
	st := f.env.call("GET", "/api/v1/terminal/exec/status", nil, "Authorization", humanAuth(testUser))
	if st.Body["paused"] != true || st.Body["enabled"] != true || st.Body["available"] != true {
		t.Fatalf("status = %s", st.Raw)
	}
	// 另起一个服务实例模拟重启：暂停状态被恢复。
	fresh := newExecService(f.env.hub, true)
	fresh.start()
	if !fresh.isPaused() {
		t.Fatalf("重启后应保持暂停")
	}
}

func TestExecRevokingHostAccessCancelsRuns(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	if _, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300")); err != nil {
		t.Fatal(err)
	}
	f.setModel(t, map[string]any{"allow_model": false})
	eventually(t, 3*time.Second, "收回授权时取消运行", func() bool { return f.sim.cancelCount() > 0 })
	if f.auditCount(t, EvHostModel) == 0 {
		t.Fatalf("修改主机模型设置应写审计")
	}
}

func TestExecReconcileAfterReconnect(t *testing.T) {
	f := newExecFixture(t, withFakeClock())
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	stay, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300"))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 301"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{stay.RunID, gone.RunID} {
		id := id
		eventually(t, 2*time.Second, "主机确认运行已开始", func() bool {
			lr := f.svc.liveRun(id)
			return lr != nil && lr.snapshot().StartedAt != nil
		})
	}
	strangerID := strings.Repeat("ab", 16)

	// 主机重连后的清单：stay 仍在运行；gone 不在了；还多出一个 Hub 不认识的运行。
	f.sim.a.send(termproto.Runs{T: termproto.TRuns, Running: []termproto.RunBrief{{ID: stay.RunID, Started: 1}, {ID: strangerID, Started: 1}}})
	eventually(t, 2*time.Second, "不认识的运行被要求取消", func() bool {
		f.sim.mu.Lock()
		defer f.sim.mu.Unlock()
		for _, c := range f.sim.cancels {
			if c.ID == strangerID {
				return true
			}
		}
		return false
	})
	// 宽限期内还没有结束事件 → 不立刻判 lost；超过宽限仍无事件 → lost。
	eventually(t, 2*time.Second, "对账登记宽限", func() bool {
		f.svc.mu.Lock()
		defer f.svc.mu.Unlock()
		_, ok := f.svc.lostWatch[gone.RunID]
		return ok
	})
	f.svc.maintain(f.env.clock.Now())
	if f.svc.liveRun(gone.RunID) == nil {
		t.Fatalf("宽限期内不应判 lost")
	}
	f.env.clock.Advance(execLostGrace + time.Second)
	f.svc.maintain(f.env.clock.Now())
	if rec := f.dbRun(t, gone.RunID); rec.State != model.RunLost || rec.DenyReason != "not_on_host" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec := f.dbRun(t, stay.RunID); rec.State != model.RunRunning {
		t.Fatalf("仍在主机上运行的不应受影响: %s", rec.State)
	}
	if f.auditCount(t, EvRunLost) != 1 {
		t.Fatalf("run_lost 审计 = %d", f.auditCount(t, EvRunLost))
	}

	// 离线期间已结束的运行，由补报的 run_end 完成；未知运行的 run_end 只确认、不影响任何状态。
	f.sim.end(stay.RunID, termproto.RunExited, intp(0), "late\n", "")
	f.sim.end(strangerID, termproto.RunExited, intp(0), "", "")
	eventually(t, 2*time.Second, "补报的结束事件完成运行", func() bool { return f.dbRun(t, stay.RunID).State == model.RunSucceeded })
	eventually(t, 2*time.Second, "未知运行的结束事件被确认", func() bool { return f.sim.hasAck(strangerID) })
}

func TestExecRunEventsFromOtherHostsAreIgnored(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300"))
	if err != nil {
		t.Fatal(err)
	}
	// 第二台主机冒充上报这条运行的结束事件。
	b := f.env.enrollAgent(testMeta("other-host"), "other")
	sb := attachSim(b)
	sb.end(res.RunID, termproto.RunExited, intp(0), "forged", "")
	eventually(t, 2*time.Second, "冒充事件被确认并忽略", func() bool { return sb.hasAck(res.RunID) })
	if rec := f.dbRun(t, res.RunID); rec.State != model.RunRunning {
		t.Fatalf("其他主机不能终结这次运行: %s", rec.State)
	}
}

func TestExecRestartRestoresAndRejectsAwaiting(t *testing.T) {
	f := newExecFixture(t)
	ctx := context.Background()
	mk := func(id, state string) {
		if err := f.env.store.CreateTerminalRun(ctx, &model.TerminalRun{RunID: id, ThreadKey: "t", Target: f.a.code, State: state, CredentialID: 7, Command: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	runningID, awaitingID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	mk(runningID, model.RunRunning)
	mk(awaitingID, model.RunAwaitingApproval)

	fresh := newExecService(f.env.hub, true)
	fresh.start()
	if fresh.liveRun(runningID) == nil {
		t.Fatalf("运行中的记录应被恢复到活动索引")
	}
	if fresh.liveRun(awaitingID) != nil {
		t.Fatalf("待批准项不能恢复（命令明文已不存在）")
	}
	if rec := f.dbRun(t, awaitingID); rec.State != model.RunRejected || rec.DenyReason != "hub_restart" {
		t.Fatalf("待批准项应作废: %+v", rec)
	}
}

func TestExecTargetsReport(t *testing.T) {
	f := newExecFixture(t)
	out, err := f.svc.Targets(context.Background(), f.caller(func(c *termexec.Caller) {
		c.Credential.TerminalLimits = model.TerminalLimits{MaxConcurrent: 2, AllowedDirs: []string{"/srv"}}
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*termexec.TargetsResult)
	if len(res.Targets) != 1 {
		t.Fatalf("targets = %+v", res.Targets)
	}
	tg := res.Targets[0]
	if tg.MachineCode != f.a.code || tg.Name != "build box" || !tg.Online || !tg.Usable || tg.Shell != "bash" || tg.Home != "/home/test" || tg.PathSep != "/" || tg.Mode != "auto" {
		t.Fatalf("target = %+v", tg)
	}
	if res.Limits == nil || res.Limits.MaxConcurrent != 2 || len(res.Limits.AllowedDirs) != 1 {
		t.Fatalf("limits = %+v", res.Limits)
	}
	// 按名称过滤。
	out, _ = f.svc.Targets(context.Background(), f.caller(nil), "BUILD BOX")
	if n := len(out.(*termexec.TargetsResult).Targets); n != 1 {
		t.Fatalf("按备注过滤 = %d", n)
	}
	out, _ = f.svc.Targets(context.Background(), f.caller(nil), "nonexistent")
	if n := len(out.(*termexec.TargetsResult).Targets); n != 0 {
		t.Fatalf("不存在的过滤应为空")
	}
	// 没有授权主机的令牌看到空列表和提示，而不是错误。
	out, err = f.svc.Targets(context.Background(), f.caller(func(c *termexec.Caller) { c.Credential.TerminalHosts = nil }), "")
	if err != nil || len(out.(*termexec.TargetsResult).Targets) != 0 || out.(*termexec.TargetsResult).Hint == "" {
		t.Fatalf("空授权: %+v %v", out, err)
	}
}

func TestExecHybridWarningsAndThreadGrouping(t *testing.T) {
	f := newExecFixture(t)
	f.completeWith("ok")
	hybrid := f.caller(func(c *termexec.Caller) {
		c.Permissions = model.Permissions{Terminal: true, Feedback: true}
		c.Credential.Permissions = c.Permissions
	})
	req := withTarget(waitSet(3), f.a.code, "id")

	missing, err := f.exec(t, hybrid, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Warnings) != 2 {
		t.Fatalf("混合令牌缺少 workflow_id 与 description 应各给一条警告: %v", missing.Warnings)
	}
	req.WorkflowID, req.Description = "wf-1", "check id"
	a, _ := f.exec(t, hybrid, req)
	req.Thread = "ignored-in-hybrid"
	b, _ := f.exec(t, hybrid, req)
	if len(a.Warnings) != 0 {
		t.Fatalf("参数齐全不应有警告: %v", a.Warnings)
	}
	ra, rb := f.dbRun(t, a.RunID), f.dbRun(t, b.RunID)
	if ra.ThreadKey != rb.ThreadKey || ra.WorkflowID != "wf-1" {
		t.Fatalf("同一工作流 × 同一主机应归入同一线程: %s vs %s", ra.ThreadKey, rb.ThreadKey)
	}
	if f.dbRun(t, missing.RunID).ThreadKey == ra.ThreadKey {
		t.Fatalf("缺少 workflow_id 的运行应落入「未归属」线程")
	}
}

func TestExecHostSettingsRESTAndViews(t *testing.T) {
	f := newExecFixture(t)
	url := "/api/v1/terminal/hosts/" + f.a.code + "/model"

	if r := f.env.call("POST", url, map[string]any{"allow_model": true}, "Authorization", humanAuth(testUser)); r.Status == 200 {
		t.Fatalf("修改模型访问必须解锁")
	}
	if r := f.env.call("POST", url, map[string]any{}, f.adm...); r.Status != 400 {
		t.Fatalf("空修改应 400，得到 %d", r.Status)
	}
	if r := f.env.call("POST", url, map[string]any{"exec_mode": "root"}, f.adm...); r.Status != 400 {
		t.Fatalf("非法模式应 400，得到 %d", r.Status)
	}
	r := f.env.call("POST", url, map[string]any{"exec_mode": "approve"}, f.adm...)
	host, _ := r.Body["host"].(map[string]any)
	if r.Status != 200 || host["exec_mode"] != "approve" || host["allow_model"] != true {
		t.Fatalf("响应 = %d %s", r.Status, r.Raw)
	}
	list := f.env.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	hosts, _ := list.Body["hosts"].([]any)
	h0, _ := hosts[0].(map[string]any)
	holder, _ := h0["holder"].(map[string]any)
	if h0["allow_model"] != true || h0["exec_mode"] != "approve" || holder["exec"] == nil || holder["deny_exec"] == true {
		t.Fatalf("主机视图缺少模型访问字段: %s", list.Raw)
	}
}

func TestExecRESTRejectsNonHumanCallers(t *testing.T) {
	f := newExecFixture(t)
	for _, p := range []string{"/api/v1/terminal/exec/status", "/api/v1/terminal/exec/runs", "/api/v1/terminal/exec/events"} {
		if r := f.env.call("GET", p, nil, "Authorization", "Bearer mcp-"+execToken); r.Status == 200 {
			t.Fatalf("MCP 令牌不应能访问 %s", p)
		}
		if r := f.env.call("GET", p, nil); r.Status == 200 {
			t.Fatalf("未登录不应能访问 %s", p)
		}
	}
}

func TestExecEventStreamAndRunList(t *testing.T) {
	f := newExecFixture(t)
	f.completeWith("streamed\n")

	req, _ := http.NewRequestWithContext(context.Background(), "GET", f.env.srv.URL+"/api/v1/terminal/exec/events", nil)
	req.Header.Set("Authorization", humanAuth(testUser))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("事件流响应 = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitLine := func(substr string) string {
		deadline := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("事件流提前结束，等待 %q", substr)
				}
				if strings.Contains(l, substr) {
					return l
				}
			case <-deadline:
				t.Fatalf("等待事件 %q 超时", substr)
			}
		}
	}
	waitLine("event: pause")

	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(5), f.a.code, "echo streamed"))
	if err != nil {
		t.Fatal(err)
	}
	line := waitLine(`"state":"succeeded"`)
	if !strings.Contains(line, res.RunID) || strings.Contains(line, "idempotency") {
		t.Fatalf("run 事件应带运行元数据且不含幂等键: %s", line)
	}

	list := f.env.call("GET", "/api/v1/terminal/exec/runs?target="+f.a.code+"&state=succeeded", nil, "Authorization", humanAuth(testUser))
	runs, _ := list.Body["runs"].([]any)
	if list.Status != 200 || len(runs) != 1 {
		t.Fatalf("列表 = %d %s", list.Status, list.Raw)
	}
	one := f.env.call("GET", "/api/v1/terminal/exec/runs/"+res.RunID, nil, "Authorization", humanAuth(testUser))
	if one.Status != 200 {
		t.Fatalf("详情 = %d %s", one.Status, one.Raw)
	}
	if strings.Contains(one.Raw, "idempotency") {
		t.Fatalf("响应不应暴露幂等键字段: %s", one.Raw)
	}
	if r := f.env.call("GET", "/api/v1/terminal/exec/runs/"+strings.Repeat("0", 32), nil, "Authorization", humanAuth(testUser)); r.Status != 404 {
		t.Fatalf("不存在的运行应 404，得到 %d", r.Status)
	}
	if r := f.env.call("GET", "/api/v1/terminal/exec/runs/not-an-id", nil, "Authorization", humanAuth(testUser)); r.Status != 400 {
		t.Fatalf("非法 ID 应 400，得到 %d", r.Status)
	}
}

func TestExecThreadsAggregation(t *testing.T) {
	f := newExecFixture(t)
	f.completeWith("ok\n")
	human := []string{"Authorization", humanAuth(testUser)}

	run := func(c termexec.Caller, thread, desc, cmd string) *termexec.RunResult {
		req := withTarget(waitSet(5), f.a.code, cmd)
		req.Thread, req.Description = thread, desc
		res, err := f.exec(t, c, req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	other := func(c *termexec.Caller) {
		c.CredentialID, c.CredentialName = 8, "chatgpt-ops"
		cred := *c.Credential
		cred.ID, cred.Name = 8, "chatgpt-ops"
		c.Credential = &cred
	}
	run(f.caller(nil), "", "查看磁盘", "df -h")
	run(f.caller(nil), "", "查看内存", "free -m")
	run(f.caller(nil), "deploy", "发布", "echo deploy")
	run(f.caller(other), "", "别人的调用", "uptime")

	r := f.env.call("GET", "/api/v1/terminal/exec/threads", nil, human...)
	threads, _ := r.Body["threads"].([]any)
	if r.Status != 200 || len(threads) != 3 {
		t.Fatalf("应有 3 个线程（凭据 × 主机 × thread），得到 %d: %s", len(threads), r.Raw)
	}
	byTitle := map[string]map[string]any{}
	for _, it := range threads {
		m := it.(map[string]any)
		cn, _ := m["credential_name"].(string)
		th, _ := m["thread"].(string) // 空线程标签不输出该字段
		byTitle[cn+"/"+th] = m
	}
	main := byTitle["cursor-main/"]
	if main == nil || main["total"].(float64) != 2 || main["active"].(float64) != 0 || main["last_title"] != "查看内存" || main["target"] != f.a.code {
		t.Fatalf("默认线程汇总不对: %v", main)
	}
	if byTitle["cursor-main/deploy"] == nil || byTitle["chatgpt-ops/"] == nil {
		t.Fatalf("缺少线程: %s", r.Raw)
	}
	if main["key"] == byTitle["cursor-main/deploy"]["key"] || main["key"] == byTitle["chatgpt-ops/"]["key"] {
		t.Fatalf("不同线程不应共用线程键")
	}
	if threads[0].(map[string]any)["credential_name"] != "chatgpt-ops" {
		t.Fatalf("应按最近一次运行倒序: %s", r.Raw)
	}

	// 待批准的运行计入 active 与 awaiting。
	f.setModel(t, map[string]any{"exec_mode": "approve"})
	pending := run(f.caller(nil), "deploy", "等待批准", "systemctl restart x")
	if pending.State != model.RunAwaitingApproval {
		t.Fatalf("状态 = %s", pending.State)
	}
	r = f.env.call("GET", "/api/v1/terminal/exec/threads?target="+f.a.code+"&credential=7&limit=10", nil, human...)
	threads, _ = r.Body["threads"].([]any)
	var dep map[string]any
	for _, it := range threads {
		if m := it.(map[string]any); m["thread"] == "deploy" {
			dep = m
		}
	}
	if len(threads) != 2 || dep == nil || dep["active"].(float64) != 1 || dep["awaiting"].(float64) != 1 || dep["total"].(float64) != 2 || dep["last_state"] != model.RunAwaitingApproval {
		t.Fatalf("过滤后的汇总不对: %s", r.Raw)
	}

	// 线程键可以直接拿去过滤运行列表。
	r = f.env.call("GET", "/api/v1/terminal/exec/runs?thread="+dep["key"].(string), nil, human...)
	if runs, _ := r.Body["runs"].([]any); len(runs) != 2 {
		t.Fatalf("按线程键过滤应得到 2 条: %s", r.Raw)
	}

	// 参数校验、认证。
	if r := f.env.call("GET", "/api/v1/terminal/exec/threads?credential=abc", nil, human...); r.Status != 400 {
		t.Fatalf("非法 credential 应 400，得到 %d", r.Status)
	}
	if r := f.env.call("GET", "/api/v1/terminal/exec/threads", nil); r.Status != 401 {
		t.Fatalf("未登录应 401，得到 %d", r.Status)
	}
}

func TestExecEventStreamGatesLiveOutput(t *testing.T) {
	f := newExecFixture(t)
	open := func(headers ...string) (<-chan string, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", f.env.srv.URL+"/api/v1/terminal/exec/events", nil)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			cancel()
			t.Fatalf("事件流 = %d", resp.StatusCode)
		}
		lines := make(chan string, 64)
		go func() {
			defer close(lines)
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				lines <- sc.Text()
			}
		}()
		return lines, func() { cancel(); resp.Body.Close() }
	}
	waitFor := func(lines <-chan string, substr string) bool {
		deadline := time.After(3 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					return false
				}
				if strings.Contains(l, substr) {
					return true
				}
			case <-deadline:
				return false
			}
		}
	}

	locked, closeLocked := open("Authorization", humanAuth(testUser))
	defer closeLocked()
	unlocked, closeUnlocked := open(f.adm...)
	defer closeUnlocked()
	for name, ch := range map[string]<-chan string{"未解锁": locked, "已解锁": unlocked} {
		if !waitFor(ch, "event: pause") {
			t.Fatalf("%s的订阅没有收到初始事件", name)
		}
	}
	eventually(t, 2*time.Second, "两个订阅者都登记了", func() bool { return f.env.hub.exec.bus.count() == 2 })

	bus := f.env.hub.exec.bus
	bus.publishOut(strings.Repeat("a", 32), "stdout", 0, []byte("secret-live-output"))
	bus.publishPause(true)
	if !waitFor(unlocked, "c2VjcmV0LWxpdmUtb3V0cHV0") { // 片段在 JSON 里是 base64
		t.Fatalf("已解锁的订阅者应收到实时输出")
	}
	// 未解锁者：状态事件照常，实时输出片段被丢弃（片段在 JSON 里是 base64）。
	sawOut := false
	deadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case l, ok := <-locked:
			if !ok {
				break drain
			}
			if strings.Contains(l, "event: out") || strings.Contains(l, "c2VjcmV0LWxpdmUtb3V0cHV0") {
				sawOut = true
			}
			if strings.Contains(l, `"paused":true`) {
				break drain
			}
		case <-deadline:
			t.Fatalf("未解锁的订阅者没有收到后续的状态事件")
		}
	}
	if sawOut {
		t.Fatalf("未解锁的订阅者不应收到实时输出片段")
	}

	// 主动上锁之后，已建立的流也不再下发实时输出。
	if r := f.env.call("POST", "/api/v1/terminal/lock", nil, f.adm...); r.Status != 200 {
		t.Fatalf("上锁: %d %s", r.Status, r.Raw)
	}
	bus.publishOut(strings.Repeat("b", 32), "stdout", 0, []byte("after-lock-output"))
	bus.publishPause(false)
	for {
		select {
		case l := <-unlocked:
			if strings.Contains(l, "event: out") || strings.Contains(l, "YWZ0ZXItbG9jay1vdXRwdXQ=") {
				t.Fatalf("上锁后不应再下发实时输出: %s", l)
			}
			if strings.Contains(l, `"paused":false`) {
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("上锁后的状态事件没有到达")
		}
	}
}

func TestExecHumanOutputRead(t *testing.T) {
	f := newExecFixture(t)
	body := []byte("line one\nline two\n")
	f.sim.onRun = func(s *hostSim, m termproto.Run) {
		s.started(m.ID)
		s.setOutput(m.ID, "stdout", body)
		s.endFull(termproto.RunEnd{ID: m.ID, State: termproto.RunExited, Code: intp(0), OutBytes: int64(len(body)), Out: body})
	}
	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(5), f.a.code, "x"))
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/v1/terminal/exec/runs/" + res.RunID + "/output?stream=stdout&offset=5"
	if r := f.env.call("GET", url, nil, "Authorization", humanAuth(testUser)); r.Status == 200 {
		t.Fatalf("读取完整输出必须解锁")
	}
	r := f.env.call("GET", url, nil, f.adm...)
	if r.Status != 200 || r.str("text") != "one\nline two\n" || r.Body["eof"] != true {
		t.Fatalf("输出 = %d %s", r.Status, r.Raw)
	}
	if f.auditCount(t, EvExecRead) == 0 {
		t.Fatalf("人类读取完整输出应写审计")
	}
	// 主机离线时读不到。
	f.a.close()
	eventually(t, 3*time.Second, "Agent 离线", func() bool { return f.env.hub.onlineAgent(f.a.code) == nil })
	if r := f.env.call("GET", url, nil, f.adm...); r.Status != 409 {
		t.Fatalf("主机离线应返回 409，得到 %d %s", r.Status, r.Raw)
	}
}

func TestExecDisablingHostCancelsRunsAndDeleteRemovesHistory(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	res, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300"))
	if err != nil {
		t.Fatal(err)
	}
	if r := f.env.call("POST", "/api/v1/terminal/hosts/"+f.a.code+"/disable", nil, f.adm...); r.Status != 200 {
		t.Fatalf("停用: %d %s", r.Status, r.Raw)
	}
	eventually(t, 3*time.Second, "停用主机时取消模型运行", func() bool { return f.sim.cancelCount() > 0 })
	if r := f.env.call("DELETE", "/api/v1/terminal/hosts/"+f.a.code, nil, f.adm...); r.Status != 200 {
		t.Fatalf("删除: %d %s", r.Status, r.Raw)
	}
	if rec, _ := f.env.store.GetTerminalRun(context.Background(), res.RunID); rec != nil {
		t.Fatalf("删除主机应同时删除它的模型运行记录")
	}
}

func TestExecGateClosingCancelsAndDropsStreams(t *testing.T) {
	f := newExecFixture(t)
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }
	if _, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "sleep 300")); err != nil {
		t.Fatal(err)
	}
	f.env.creds.set("", "", nil) // 清空 Web 密码 → 总闸关闭
	eventually(t, 3*time.Second, "总闸关闭前先通知主机取消", func() bool { return f.sim.cancelCount() > 0 })
	_, err := f.exec(t, f.caller(nil), withTarget(waitSet(0), f.a.code, "id"))
	wantCode(t, err, termexec.CodeDisabled)
}

// 令牌被删除 / 重新生成 / 停用 / 收紧之后，它名下进行中的运行要被取消；仍然被允许的不受影响。
func TestExecMCPCredentialChangeCancelsRuns(t *testing.T) {
	f := newExecFixture(t)
	ctx := context.Background()
	f.sim.onRun = func(s *hostSim, m termproto.Run) { s.started(m.ID) }

	newCred := func(name string) *model.MCPCredential {
		c := &model.MCPCredential{Name: name, Permissions: model.Permissions{Terminal: true}, TerminalHosts: model.StringList{f.a.code}}
		if err := f.env.store.CreateCredential(ctx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	startRun := func(c *model.MCPCredential) *termexec.RunResult {
		caller := f.caller(func(x *termexec.Caller) {
			x.CredentialID, x.CredentialName, x.Credential = c.ID, c.Name, c
		})
		res, err := f.exec(t, caller, withTarget(waitSet(0), f.a.code, "sleep 300"))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	cancelled := func(n int) bool { return f.sim.cancelCount() >= n }

	// 仍然被允许：不取消。
	keep := newCred("keep")
	startRun(keep)
	f.env.hub.MCPCredentialChanged(keep.ID, "updated")
	time.Sleep(150 * time.Millisecond)
	if f.sim.cancelCount() != 0 {
		t.Fatalf("更新后仍被允许的令牌不应取消它的运行")
	}

	// 删除 / 重新生成：无条件取消。
	for i, reason := range []string{"deleted", "regenerated"} {
		c := newCred("c-" + reason)
		startRun(c)
		f.env.hub.MCPCredentialChanged(c.ID, reason)
		eventually(t, 3*time.Second, reason+" 取消运行", func() bool { return cancelled(i + 1) })
	}
	base := f.sim.cancelCount()

	// 更新后不再被允许：停用、移出主机授权、收紧为只读、收回终端权限。
	mutations := map[string]func(c *model.MCPCredential){
		"停用":   func(c *model.MCPCredential) { c.IsActive = false },
		"移出主机": func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"RM-ZZZZ-ZZZZ-ZZZZ-ZZZZ"} },
		"只读":   func(c *model.MCPCredential) { c.TerminalModeCap = model.TerminalModeReadonly },
		"收回权限": func(c *model.MCPCredential) { c.Permissions.Terminal = false },
	}
	n := base
	for name, mutate := range mutations {
		c := newCred("m-" + name)
		startRun(c)
		cp := *c
		mutate(&cp)
		if _, err := f.env.store.UpdateCredential(ctx, c.ID, map[string]any{
			"is_active": cp.IsActive, "terminal_hosts": []string(cp.TerminalHosts), "terminal_mode_cap": cp.TerminalModeCap, "permissions": cp.Permissions,
		}); err != nil {
			t.Fatalf("%s: 更新令牌失败: %v", name, err)
		}
		f.env.hub.MCPCredentialChanged(c.ID, "updated")
		n++
		eventually(t, 3*time.Second, name+" 后取消运行", func() bool { return cancelled(n) })
	}
}

func TestCredentialStillAllows(t *testing.T) {
	const code = "RM-AAAA-BBBB-CCCC-DDDD"
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	base := func() *model.MCPCredential {
		return &model.MCPCredential{IsActive: true, Permissions: model.Permissions{Terminal: true}, TerminalHosts: model.StringList{code}}
	}
	cases := []struct {
		name string
		mut  func(c *model.MCPCredential)
		want bool
	}{
		{"正常", func(c *model.MCPCredential) {}, true},
		{"通配主机", func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"*"} }, true},
		{"未过期", func(c *model.MCPCredential) { c.ExpiresAt = &future }, true},
		{"已过期", func(c *model.MCPCredential) { c.ExpiresAt = &past }, false},
		{"已停用", func(c *model.MCPCredential) { c.IsActive = false }, false},
		{"没有终端位", func(c *model.MCPCredential) { c.Permissions.Terminal = false }, false},
		{"主机不在授权内", func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"RM-1111-2222-3333-4444"} }, false},
		{"没有任何主机授权", func(c *model.MCPCredential) { c.TerminalHosts = nil }, false},
		{"只读上限", func(c *model.MCPCredential) { c.TerminalModeCap = model.TerminalModeReadonly }, false},
		{"需批准上限仍可用", func(c *model.MCPCredential) { c.TerminalModeCap = model.TerminalModeApprove }, true},
	}
	for _, tc := range cases {
		c := base()
		tc.mut(c)
		if got := credentialStillAllows(c, code, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
