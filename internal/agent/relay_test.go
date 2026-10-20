package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func TestParseHolderMode(t *testing.T) {
	cases := map[string]HolderMode{
		"":         HolderAuto,
		"auto":     HolderAuto,
		"external": HolderExternal,
		"embedded": HolderEmbedded,
	}
	for in, want := range cases {
		got, err := ParseHolderMode(in)
		if err != nil || got != want {
			t.Errorf("ParseHolderMode(%q) = %q, %v；期望 %q", in, got, err, want)
		}
	}
	if _, err := ParseHolderMode("daemon"); err == nil {
		t.Error("未知取值应报错")
	}
}

func TestNewRequiresSocketForExternalAndAuto(t *testing.T) {
	target, _ := ParseServer("ws://127.0.0.1:1")
	for _, mode := range []HolderMode{HolderExternal, HolderAuto} {
		_, err := New(Options{Server: target, Trust: TrustOptions{Insecure: true}, Identity: mustIdentity(t), Holder: mode})
		if err == nil {
			t.Errorf("%s 模式缺少套接字路径应报错", mode)
		}
	}
	if _, err := New(Options{Server: target, Trust: TrustOptions{Insecure: true}, Identity: mustIdentity(t), Holder: "weird", HolderSocket: "/x"}); err == nil {
		t.Error("未知模式应报错")
	}
	// 零值（库与测试的默认）是内嵌：不会去碰机器上真实的守护进程。
	a, err := New(Options{Server: target, Trust: TrustOptions{Insecure: true}, Identity: mustIdentity(t), Spawner: &ptyhost.FakeSpawner{}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Manager() == nil || a.SessionsOutliveAgent() {
		t.Fatal("默认应为内嵌模式：有会话管理器，且会话不会比 Agent 活得更久")
	}
}

func TestAutoFallsBackToEmbeddedWithoutHolder(t *testing.T) {
	target, _ := ParseServer("ws://127.0.0.1:1")
	dir, err := os.MkdirTemp("", "rmh")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	var logs []string
	a, err := New(Options{
		Server: target, Trust: TrustOptions{Insecure: true}, Identity: mustIdentity(t),
		Holder: HolderAuto, HolderSocket: holder.SocketPath(dir), Spawner: &ptyhost.FakeSpawner{},
		Logf: func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.resolveHolder(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Manager() == nil || a.SessionsOutliveAgent() {
		t.Fatal("没有守护进程时 auto 应退回内嵌")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "内嵌") {
		t.Fatalf("退回内嵌必须写日志说明: %v", logs)
	}
}

func TestAutoUsesRunningHolderWithoutSupersedingController(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 AF_UNIX 行为由专门的实机验证覆盖")
	}
	dir, err := os.MkdirTemp("", "rmh")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ready := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- holder.Run(ctx, holder.RunOptions{DataDir: dir, Version: "9.9-test", Spawner: &ptyhost.FakeSpawner{},
			Ready: func(s string) { ready <- s }})
	}()
	var socket string
	select {
	case socket = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("守护进程没有就绪")
	}
	defer func() { cancel(); <-done }()

	// 已经有一个控制端连着（模拟另一个正在运行的 Agent）。
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	conn, err := holder.Dial(cctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := holder.Handshake(cctx, conn, holder.RoleController, "other-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Conn.Close()

	target, _ := ParseServer("ws://127.0.0.1:1")
	a, err := New(Options{Server: target, Trust: TrustOptions{Insecure: true}, Identity: mustIdentity(t), Holder: HolderAuto, HolderSocket: socket})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.resolveHolder(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.SessionsOutliveAgent() || a.Manager() != nil {
		t.Fatal("检测到守护进程时应使用它，而不是内嵌")
	}
	// 探测只是管理端身份：原来的控制端不能被踢掉。
	rctx, rcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer rcancel()
	for {
		typ, data, err := existing.Conn.Read(rctx)
		if err != nil {
			break // 超时：没有被踢，符合预期
		}
		if typ == holder.MsgText && strings.Contains(string(data), holder.TIPCBye) {
			t.Fatalf("探测不应取代已有的控制端: %s", data)
		}
	}
}

func TestHolderReadyFrameFitsAndOrdersLostSessions(t *testing.T) {
	var h holder.IPCHello
	h.Holder, h.Persistent, h.BootID, h.Started = "1.7.0", true, "boot", 123
	h.Caps = holder.DefaultCaps()
	// 大量、很长的丢失记录：帧必须装得下 Hub 的读取上限，且优先保留最近的。
	for i := 0; i < 64; i++ {
		h.Lost = append(h.Lost, holder.Lost{
			SID:    fmt.Sprintf("%032x", i+1),
			Name:   strings.Repeat("名", 200),
			Cwd:    strings.Repeat("/很长的目录", 60) + "<&>",
			Reason: holder.LostHolderExit,
			At:     int64(1000 + i),
		})
	}
	msg, dropped := holderReadyFrame(h)
	data, err := termproto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxHolderFrame || len(data) >= termproto.MaxControlFrame {
		t.Fatalf("holder 帧过大: %d 字节", len(data))
	}
	if len(msg.Lost)+dropped != 64 {
		t.Fatalf("保留 + 舍弃应等于总数: %d + %d", len(msg.Lost), dropped)
	}
	for i := 1; i < len(msg.Lost); i++ {
		if msg.Lost[i-1].At < msg.Lost[i].At {
			t.Fatal("应按时间从新到旧保留")
		}
	}
	if msg.State != termproto.HolderReady || !msg.Persistent || msg.Version != "1.7.0" {
		t.Fatalf("基本字段不符: %+v", msg)
	}
	if msg.Lost[0].At != 1063 {
		t.Fatalf("最新的记录应排在最前，得到 At=%d", msg.Lost[0].At)
	}
}

// 守护进程的代码指纹随 ready 帧上报给 Hub；来自守护进程的文本不可信，不合规的指纹被丢弃。
func TestHolderReadyFrameCarriesRev(t *testing.T) {
	msg, _ := holderReadyFrame(holder.IPCHello{Holder: "1.7.0", Persistent: true, Rev: "72ae54bd8799"})
	if msg.Rev != "72ae54bd8799" {
		t.Fatalf("ready 帧应带守护进程的代码指纹: %+v", msg)
	}
	msg, _ = holderReadyFrame(holder.IPCHello{Holder: "1.7.0", Persistent: true, Rev: "../../etc/passwd"})
	if msg.Rev != "" {
		t.Fatalf("不合规的指纹应被丢弃: %q", msg.Rev)
	}
	msg, _ = holderReadyFrame(holder.IPCHello{Holder: "1.7.0", Persistent: true})
	if msg.Rev != "" {
		t.Fatalf("旧版本守护进程没有指纹: %q", msg.Rev)
	}
}

func TestHolderReadyFrameSanitizesPeerText(t *testing.T) {
	h := holder.IPCHello{Holder: "1.7.0\x1b[31m", Persistent: true, Lost: []holder.Lost{
		{SID: "not-a-session-id", Reason: holder.LostHolderExit},
		{SID: strings.Repeat("a", 32), Name: "x\x1b]0;evil\x07y", Reason: "made-up-reason"},
	}}
	msg, _ := holderReadyFrame(h)
	if strings.ContainsRune(msg.Version, 0x1b) {
		t.Errorf("版本里的控制字符应被去除: %q", msg.Version)
	}
	if len(msg.Lost) != 1 {
		t.Fatalf("非法 SID 的记录应被丢弃: %+v", msg.Lost)
	}
	if strings.ContainsRune(msg.Lost[0].Name, 0x1b) || strings.ContainsRune(msg.Lost[0].Name, 0x07) {
		t.Errorf("名字里的控制字符应被去除: %q", msg.Lost[0].Name)
	}
	if msg.Lost[0].Reason != termproto.LostUnknown {
		t.Errorf("未知原因应归一为 unknown: %q", msg.Lost[0].Reason)
	}
}

func TestClassifyHolderError(t *testing.T) {
	cases := []struct {
		err    error
		reason string
		slow   bool
	}{
		{&holder.ByeError{Reason: holder.ByeVersion}, termproto.HolderReasonVersion, true},
		{&holder.ByeError{Reason: holder.ByeSuperseded}, termproto.HolderReasonSuperseded, true},
		{holder.ErrNotRunning, termproto.HolderReasonUnreachable, false},
		{errors.New("boom"), termproto.HolderReasonUnreachable, false},
	}
	for _, tc := range cases {
		reason, wait := classifyHolderError(tc.err)
		if reason != tc.reason || (wait > 0) != tc.slow {
			t.Errorf("classifyHolderError(%v) = %q, %v；期望 %q slow=%v", tc.err, reason, wait, tc.reason, tc.slow)
		}
	}
}

func TestHolderReadyFrameCarriesExecInfoAndVeto(t *testing.T) {
	h := holder.IPCHello{
		Holder: "9.9.9", Persistent: true, Caps: append(holder.DefaultCaps(), holder.CapExec),
		DenyExec: true, Exec: &termproto.ExecInfo{Shell: "bash", Home: "/home/u", Sep: "/"},
	}
	msg, _ := holderReadyFrame(h)
	if !msg.DenyExec || msg.Exec == nil || msg.Exec.Shell != "bash" || msg.Exec.Home != "/home/u" || msg.Exec.Sep != "/" {
		t.Fatalf("exec 信息与否决应原样上报: %+v", msg)
	}
	if !holder.HasCap(msg.Caps, holder.CapExec) {
		t.Fatalf("caps 应含 exec: %v", msg.Caps)
	}

	// 来自守护进程的文本不可信：路径分隔符被限定、超长内容被截断。
	h.Exec = &termproto.ExecInfo{Shell: strings.Repeat("x", 500), Home: "/h", Sep: "??"}
	msg, _ = holderReadyFrame(h)
	if msg.Exec == nil || len(msg.Exec.Shell) > 64 || (msg.Exec.Sep != "/" && msg.Exec.Sep != "\\" && msg.Exec.Sep != "") {
		t.Fatalf("exec 信息应被净化: %+v", msg.Exec)
	}
}
