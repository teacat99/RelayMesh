package holder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 夹具 ----------

type hostEnv struct {
	host  *Host
	spawn *ptyhost.FakeSpawner
}

func newHostEnv(t *testing.T, persistent bool, stateDir string) *hostEnv {
	t.Helper()
	spawn := &ptyhost.FakeSpawner{}
	h, err := NewHost(HostOptions{
		Manager: ManagerOptions{
			Spawner: spawn,
			Shell: func(rows, cols uint16) (ptyhost.Spec, error) {
				return ptyhost.Spec{Path: "/bin/fakeshell", Rows: rows, Cols: cols}, nil
			},
		},
		Version:    "1.0-test",
		Persistent: persistent,
		StateDir:   stateDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Start()
	t.Cleanup(func() { h.Close(false) })
	return &hostEnv{host: h, spawn: spawn}
}

// peer 是测试里扮演控制端 / 管理端的一端。
type peer struct {
	t      *testing.T
	client *Client
}

func (e *hostEnv) dial(t *testing.T, role string) *peer {
	t.Helper()
	c, s := NewPipe()
	go func() { _ = e.host.Serve(context.Background(), s) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, err := Handshake(ctx, c, role, "test-client")
	if err != nil {
		t.Fatalf("Handshake(%s): %v", role, err)
	}
	t.Cleanup(func() { _ = cli.Conn.Close() })
	return &peer{t: t, client: cli}
}

func (p *peer) sendJSON(v any) {
	p.t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.client.Conn.Write(ctx, MsgText, raw); err != nil {
		p.t.Fatalf("写入失败: %v", err)
	}
}

func (p *peer) sendBinary(b []byte) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.client.Conn.Write(ctx, MsgBinary, b); err != nil {
		p.t.Fatalf("写入失败: %v", err)
	}
}

// next 读取下一帧。
func (p *peer) next(timeout time.Duration) (MsgType, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.client.Conn.Read(ctx)
}

// expect 读取帧直到出现类型为 typ 的文本帧，期间跳过其他帧。
func (p *peer) expect(typ string) []byte {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mt, data, err := p.next(time.Until(deadline))
		if err != nil {
			p.t.Fatalf("等待 %q 帧时连接出错: %v", typ, err)
		}
		if mt != MsgText {
			continue
		}
		if t, _ := termproto.ParseType(data); t == typ {
			return data
		}
	}
	p.t.Fatalf("等待 %q 帧超时", typ)
	return nil
}

// expectOutput 读取帧直到收到某通道的输出帧。
func (p *peer) expectOutput(ch uint32) (uint64, []byte) {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mt, data, err := p.next(time.Until(deadline))
		if err != nil {
			p.t.Fatalf("等待输出帧时连接出错: %v", err)
		}
		if mt != MsgBinary {
			continue
		}
		c, off, payload, err := termproto.DecodeOutput(data)
		if err == nil && c == ch {
			return off, payload
		}
	}
	p.t.Fatal("等待输出帧超时")
	return 0, nil
}

func (p *peer) open(name string) termproto.SessionInfo {
	p.t.Helper()
	p.sendJSON(termproto.Open{T: termproto.TOpen, Req: "r1", Name: name, Rows: 24, Cols: 80})
	var m termproto.Opened
	if err := json.Unmarshal(p.expect(termproto.TOpened), &m); err != nil {
		p.t.Fatal(err)
	}
	return m.Session
}

func (p *peer) attach(ch uint32, sid string, since uint64) termproto.Attached {
	p.t.Helper()
	p.sendJSON(termproto.Attach{T: termproto.TAttach, Ch: ch, SID: sid, Since: since, Rows: 24, Cols: 80})
	var m termproto.Attached
	if err := json.Unmarshal(p.expect(termproto.TAttached), &m); err != nil {
		p.t.Fatal(err)
	}
	return m
}

// ---------- 用例：会话与数据面 ----------

func TestControllerOpenAttachAndStream(t *testing.T) {
	e := newHostEnv(t, false, "")
	p := e.dial(t, RoleController)
	p.expect(termproto.TSessions) // 握手之后立即推送一份全量清单

	info := p.open("work")
	if !termproto.ValidSessionID(info.SID) {
		t.Fatalf("会话 ID 不合法: %q", info.SID)
	}
	att := p.attach(1, info.SID, 0)
	if att.SID != info.SID || att.Ch != 1 {
		t.Fatalf("attached 不符: %+v", att)
	}

	term := e.spawn.Terminals()[0]
	term.Emit([]byte("hello"))
	off, payload := p.expectOutput(1)
	if off != 0 || string(payload) != "hello" {
		t.Fatalf("输出帧不符: off=%d %q", off, payload)
	}

	p.sendBinary(termproto.EncodeInput(1, []byte("ls\n")))
	eventually(t, "输入到达终端", func() bool { return string(term.Input()) == "ls\n" })
}

func TestSessionsSurviveControllerDisconnect(t *testing.T) {
	e := newHostEnv(t, true, "")
	p := e.dial(t, RoleController)
	info := p.open("keep")
	p.attach(1, info.SID, 0)
	e.spawn.Terminals()[0].Emit([]byte("before"))
	p.expectOutput(1)

	// 控制端（Agent）走了：会话不能受影响。
	_ = p.client.Conn.Close()
	time.Sleep(100 * time.Millisecond)
	if n := e.host.Manager().Count(); n != 1 {
		t.Fatalf("控制端断开后会话应继续存在，实际 %d", n)
	}
	if e.spawn.Terminals()[0].IsClosed() {
		t.Fatal("控制端断开不应关闭终端")
	}
	e.spawn.Terminals()[0].Emit([]byte("-while-away"))

	// 新的控制端接上：清单里有这个会话，从头附着能回放离开期间的输出。
	p2 := e.dial(t, RoleController)
	var sessions termproto.Sessions
	if err := json.Unmarshal(p2.expect(termproto.TSessions), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Sessions) != 1 || sessions.Sessions[0].SID != info.SID {
		t.Fatalf("新控制端应看到原会话: %+v", sessions.Sessions)
	}
	p2.attach(7, info.SID, 0)
	var got []byte
	for len(got) < len("before-while-away") {
		_, payload := p2.expectOutput(7)
		got = append(got, payload...)
	}
	if string(got) != "before-while-away" {
		t.Fatalf("回放内容不符: %q", got)
	}
}

func TestNewControllerSupersedesOld(t *testing.T) {
	e := newHostEnv(t, true, "")
	first := e.dial(t, RoleController)
	first.expect(termproto.TSessions)

	second := e.dial(t, RoleController)
	second.expect(termproto.TSessions)

	var bye IPCBye
	if err := json.Unmarshal(first.expect(TIPCBye), &bye); err != nil || bye.Reason != ByeSuperseded {
		t.Fatalf("旧控制端应收到 superseded: %+v %v", bye, err)
	}
	// 新的控制端正常工作。
	info := second.open("after-takeover")
	if info.SID == "" {
		t.Fatal("新控制端应能开会话")
	}
}

func TestAdminDoesNotDisturbController(t *testing.T) {
	e := newHostEnv(t, true, "")
	ctl := e.dial(t, RoleController)
	ctl.expect(termproto.TSessions)
	info := ctl.open("one")

	adm := e.dial(t, RoleAdmin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := Status(ctx, adm.client)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Controller || len(st.Sessions) != 1 || st.Sessions[0].SID != info.SID {
		t.Fatalf("状态应反映控制端与会话: %+v", st)
	}
	if st.Hello.Holder != "1.0-test" || !st.Hello.Persistent {
		t.Fatalf("hello 不符: %+v", st.Hello)
	}
	// 管理端连接不会取代控制端：控制端仍能收发。
	ctl.attach(1, info.SID, 0)
}

func TestVersionMismatchIsRejectedWithBye(t *testing.T) {
	e := newHostEnv(t, false, "")
	c, s := NewPipe()
	go func() { _ = e.host.Serve(context.Background(), s) }()
	defer c.Close()

	raw, _ := json.Marshal(IPCOpen{T: TIPCOpen, V: IPCVersion + 1, Role: RoleController})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Write(ctx, MsgText, raw); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var bye IPCBye
	if json.Unmarshal(data, &bye) != nil || bye.T != TIPCBye || bye.Reason != ByeVersion {
		t.Fatalf("版本不一致应得到 version 告别: %s", data)
	}
}

func TestBadOpenIsRejected(t *testing.T) {
	e := newHostEnv(t, false, "")
	c, s := NewPipe()
	go func() { _ = e.host.Serve(context.Background(), s) }()
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Write(ctx, MsgText, []byte(`{"t":"open","req":"x"}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var bye IPCBye
	if json.Unmarshal(data, &bye) != nil || bye.Reason != ByeBadRequest {
		t.Fatalf("第一帧不是 ipc_open 应被拒绝: %s", data)
	}
}

func TestUnknownRoleIsRejected(t *testing.T) {
	e := newHostEnv(t, false, "")
	c, s := NewPipe()
	go func() { _ = e.host.Serve(context.Background(), s) }()
	defer c.Close()
	raw, _ := json.Marshal(IPCOpen{T: TIPCOpen, V: IPCVersion, Role: "root"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Write(ctx, MsgText, raw)
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var bye IPCBye
	if json.Unmarshal(data, &bye) != nil || bye.Reason != ByeBadRequest {
		t.Fatalf("未知角色应被拒绝: %s", data)
	}
}

// ---------- 用例：关停 ----------

func TestShutdownRequestsOnlyHonoredByExternalHolder(t *testing.T) {
	embedded := newHostEnv(t, false, "")
	embedded.host.RequestShutdown(ShutdownNow)
	select {
	case m := <-embedded.host.ShutdownRequests():
		t.Fatalf("内嵌模式不应响应关停请求: %s", m)
	case <-time.After(50 * time.Millisecond):
	}

	external := newHostEnv(t, true, "")
	adm := external.dial(t, RoleAdmin)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := SendShutdown(ctx, adm.client.Conn, ShutdownNow); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-external.host.ShutdownRequests():
		if m != ShutdownNow {
			t.Fatalf("关停方式不符: %s", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("外置守护进程应收到关停请求")
	}
}

func TestDrainRefusesNewSessionsButKeepsOldOnes(t *testing.T) {
	e := newHostEnv(t, true, "")
	ctl := e.dial(t, RoleController)
	ctl.expect(termproto.TSessions)
	info := ctl.open("old")

	e.host.RequestShutdown(ShutdownDrain)
	if !e.host.Draining() {
		t.Fatal("应进入排空状态")
	}
	ctl.sendJSON(termproto.Open{T: termproto.TOpen, Req: "r2", Rows: 24, Cols: 80})
	var failed termproto.OpenFailed
	if err := json.Unmarshal(ctl.expect(termproto.TOpenFailed), &failed); err != nil || failed.Req != "r2" {
		t.Fatalf("排空期间应拒绝新会话: %+v %v", failed, err)
	}
	// 老会话照常可用。
	if att := ctl.attach(1, info.SID, 0); att.SID != info.SID {
		t.Fatalf("排空期间老会话应可附着: %+v", att)
	}
	if e.host.Manager().RunningCount() != 1 {
		t.Fatal("排空不应结束运行中的会话")
	}
}

func TestCloseNotifiesControllerAndTerminatesSessions(t *testing.T) {
	e := newHostEnv(t, true, t.TempDir())
	ctl := e.dial(t, RoleController)
	ctl.expect(termproto.TSessions)
	ctl.open("doomed")

	e.host.Close(true)
	var bye IPCBye
	if err := json.Unmarshal(ctl.expect(TIPCBye), &bye); err != nil || bye.Reason != ByeShutdown {
		t.Fatalf("关停时应通知控制端: %+v %v", bye, err)
	}
	if e.host.Manager().Count() != 0 && !e.spawn.Terminals()[0].IsClosed() {
		t.Fatal("关停应终止全部会话")
	}
	// 关停之后的新连接被拒绝。
	c, s := NewPipe()
	go func() { _ = e.host.Serve(context.Background(), s) }()
	defer c.Close()
	raw, _ := json.Marshal(IPCOpen{T: TIPCOpen, V: IPCVersion, Role: RoleController})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Write(ctx, MsgText, raw)
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var b2 IPCBye
	if json.Unmarshal(data, &b2) != nil || b2.Reason != ByeShutdown {
		t.Fatalf("已关停的 Host 应拒绝新连接: %s", data)
	}
}

// ---------- 用例：状态与「丢失的会话」 ----------

func writeState(t *testing.T, dir string, st persistedState) {
	t.Helper()
	st.Version = stateVersion
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestComputeLostReasons(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	sess := []persistedSession{{SID: sidA, Name: "a", Cwd: "/srv/app", Created: 100}, {SID: sidB, Name: "b"}}
	bootA, bootB := "boot-A", "boot-B"

	cases := []struct {
		name  string
		old   *persistedState
		cur   string
		wants string
	}{
		{"守护进程崩溃或被杀", &persistedState{BootID: bootA, Sessions: sess}, bootA, LostHolderExit},
		{"守护进程被有意停止", &persistedState{BootID: bootA, Clean: true, Sessions: sess}, bootA, LostHolderStopped},
		{"主机重启优先于其他原因", &persistedState{BootID: bootA, Clean: true, Sessions: sess}, bootB, LostHostReboot},
		{"主机重启（异常退出）", &persistedState{BootID: bootA, Sessions: sess}, bootB, LostHostReboot},
	}
	for _, tc := range cases {
		got := computeLost(tc.old, tc.cur, 0, now)
		if len(got) != 2 {
			t.Fatalf("%s: 应有 2 条记录，实际 %d", tc.name, len(got))
		}
		for _, l := range got {
			if l.Reason != tc.wants {
				t.Errorf("%s: 原因 = %s，期望 %s", tc.name, l.Reason, tc.wants)
			}
			if l.At != now.Unix() {
				t.Errorf("%s: 发现时间应为当前时间", tc.name)
			}
		}
		if got[0].Cwd != "/srv/app" || got[0].Name != "a" {
			t.Errorf("%s: 应保留名字与最后的工作目录: %+v", tc.name, got[0])
		}
	}

	if computeLost(nil, bootA, 0, now) != nil {
		t.Fatal("没有旧状态时不应有丢失记录")
	}
}

func TestComputeLostMergesOldRecordsWithTTLAndCap(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fresh := Lost{SID: sidA, Reason: LostHolderExit, At: now.Add(-time.Hour).Unix()}
	stale := Lost{SID: sidB, Reason: LostHolderExit, At: now.Add(-lostTTL - time.Hour).Unix()}
	old := &persistedState{
		BootID:   "b",
		Sessions: []persistedSession{{SID: sidC}},
		Lost:     []Lost{fresh, stale, {SID: sidC, Reason: LostHostReboot, At: now.Add(-time.Hour).Unix()}},
	}
	got := computeLost(old, "b", 0, now)
	bySID := map[string]Lost{}
	for _, l := range got {
		bySID[l.SID] = l
	}
	if _, ok := bySID[sidB]; ok {
		t.Error("超过保留期的旧记录应被丢弃")
	}
	if _, ok := bySID[sidA]; !ok {
		t.Error("未过期的旧记录应保留")
	}
	if bySID[sidC].Reason != LostHolderExit {
		t.Errorf("同一会话以新的记录为准，实际 %s", bySID[sidC].Reason)
	}

	many := &persistedState{BootID: "b"}
	for i := 0; i < maxLostEntries+20; i++ {
		many.Sessions = append(many.Sessions, persistedSession{SID: sidN(i)})
	}
	if got := computeLost(many, "b", 0, now); len(got) != maxLostEntries {
		t.Fatalf("记录数应被限制为 %d，实际 %d", maxLostEntries, len(got))
	}
}

func TestSameBoot(t *testing.T) {
	if !sameBoot("x", 0, "x", 0) || sameBoot("x", 0, "y", 0) {
		t.Error("有启动标识时按标识比较")
	}
	if !sameBoot("", 1000, "", 1003) || sameBoot("", 1000, "", 1100) {
		t.Error("没有标识时按启动时间比较，允许几秒误差")
	}
	if !sameBoot("", 0, "", 0) || !sameBoot("x", 0, "", 0) {
		t.Error("信息不足时宁可假定相同")
	}
}

func TestHostReportsLostSessionsAfterUncleanExit(t *testing.T) {
	dir := t.TempDir()
	bootID, bootTime := bootIdentity()
	writeState(t, dir, persistedState{
		BootID: bootID, BootTime: bootTime, PID: 4242, Started: 1,
		Sessions: []persistedSession{{SID: sidA, Name: "build", Cwd: "/srv/app", Created: 5}},
	})
	e := newHostEnv(t, true, dir)

	hello := e.host.Hello()
	if len(hello.Lost) != 1 || hello.Lost[0].SID != sidA || hello.Lost[0].Reason != LostHolderExit {
		t.Fatalf("hello 应报告意外退出丢失的会话: %+v", hello.Lost)
	}
	if hello.Lost[0].Name != "build" || hello.Lost[0].Cwd != "/srv/app" {
		t.Fatalf("丢失记录应保留名字与工作目录: %+v", hello.Lost[0])
	}
	if !hello.Persistent || hello.V != IPCVersion || !HasCap(hello.Caps, CapLost) {
		t.Fatalf("hello 其余字段不符: %+v", hello)
	}
	// Hub 靠这个能力位判断守护进程是否带上了「字符类别钉在 LC_CTYPE」的修复（旧守护进程不会声明它）。
	if !HasCap(hello.Caps, CapCharset) {
		t.Fatalf("hello 应声明 %q 能力: %v", CapCharset, hello.Caps)
	}
}

func TestHostCloseCleanThenRestartReportsHolderStopped(t *testing.T) {
	dir := t.TempDir()
	first := newHostEnv(t, true, dir)
	ctl := first.dial(t, RoleController)
	ctl.expect(termproto.TSessions)
	info := ctl.open("long-running")
	first.host.Close(true)

	second := newHostEnv(t, true, dir)
	lost := second.host.Hello().Lost
	if len(lost) != 1 || lost[0].SID != info.SID || lost[0].Reason != LostHolderStopped {
		t.Fatalf("有意停止后重启应报告 holder_stopped: %+v", lost)
	}
	if lost[0].Name != "long-running" {
		t.Fatalf("丢失记录应带会话名: %+v", lost[0])
	}
}

func TestHostStateFileIsPrivateAndAtomic(t *testing.T) {
	dir := t.TempDir()
	e := newHostEnv(t, true, dir)
	e.dial(t, RoleController).open("x")
	e.host.saveNow(false)

	st, err := os.Stat(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("状态文件权限 = %o，应为 0600", st.Mode().Perm())
	}
	// 新开会话会触发一次异步保存，可能与上面的保存交错：等所有写入结束后目录里只应剩状态文件本身。
	waitUntilTrue(t, 2*time.Second, "写入结束后目录里不应残留临时文件", func() bool {
		entries, _ := os.ReadDir(dir)
		for _, ent := range entries {
			if ent.Name() != stateFileName {
				return false
			}
		}
		return true
	})
}

func waitUntilTrue(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

func TestNewSessionIsPersistedImmediately(t *testing.T) {
	// 崩溃时要能如实报告「有哪些会话」：新开的会话不能等一整个去抖间隔才落盘。
	dir := t.TempDir()
	e := newHostEnv(t, true, dir)
	info := e.dial(t, RoleController).open("fresh")

	store := newStateStore(dir)
	waitUntilTrue(t, stateSaveDebounce/2, "新会话应在远小于去抖间隔的时间内写入状态文件", func() bool {
		st, err := store.load()
		if err != nil || st == nil {
			return false
		}
		for _, s := range st.Sessions {
			if s.SID == info.SID {
				return true
			}
		}
		return false
	})
}

func TestFinalStateIsNotOverwrittenByLateSaves(t *testing.T) {
	dir := t.TempDir()
	e := newHostEnv(t, true, dir)
	e.dial(t, RoleController).open("x")
	e.host.Close(true)
	// Close 之后到来的事件保存 / 周期保存都不能把「有意停止」改写成「意外退出」。
	for i := 0; i < 5; i++ {
		e.host.scheduleSave()
		e.host.saveNow(false)
	}
	time.Sleep(150 * time.Millisecond)
	st, err := newStateStore(dir).load()
	if err != nil || st == nil || !st.Clean {
		t.Fatalf("最终状态应保持 clean=true: %+v err=%v", st, err)
	}
}

func TestCorruptStateIsIgnoredAndKept(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newHostEnv(t, true, dir)
	if len(e.host.Hello().Lost) != 0 {
		t.Fatal("损坏的状态不应产生丢失记录")
	}
	if _, err := os.Stat(filepath.Join(dir, stateFileName+".bad")); err != nil {
		t.Fatalf("损坏的状态应保留副本以便排查: %v", err)
	}
}

func TestPolicyIsPersistedAndReappliedOnRestart(t *testing.T) {
	dir := t.TempDir()
	first := newHostEnv(t, true, dir)
	first.host.ApplyPolicy(termproto.Policy{Terminal: true, MaxSessions: 1})
	first.host.Close(true)

	second := newHostEnv(t, true, dir)
	// Hub 还没连上，上一份策略就已经生效：第二个会话被限额拒绝。
	if _, err := second.host.Manager().Open("a", 24, 80); err != nil {
		t.Fatal(err)
	}
	if _, err := second.host.Manager().Open("b", 24, 80); err == nil {
		t.Fatal("持久化的策略（最多 1 个会话）应在 Hub 离线时继续生效")
	}
}

func TestPurgeStateRemovesOnlyHolderFiles(t *testing.T) {
	data := t.TempDir()
	dir := StateDir(data)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{stateFileName, stateFileName + ".bad", "other.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed := PurgeState(data)
	if len(removed) != 2 {
		t.Fatalf("应只删除两个状态文件: %v", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err != nil {
		t.Fatal("不属于守护进程的文件必须保留")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("目录里还有别的文件时目录应保留")
	}
}

// ---------- 小工具 ----------

const (
	sidA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sidC = "cccccccccccccccccccccccccccccccc"
)

func sidN(i int) string {
	const hex = "0123456789abcdef"
	b := []byte("00000000000000000000000000000000")
	for k := 0; k < 8 && i > 0; k++ {
		b[31-k] = hex[i%16]
		i /= 16
	}
	return string(b)
}
