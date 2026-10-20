package holder

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// shortDataDir 返回一个路径足够短的数据目录：AF_UNIX 路径有长度上限，t.TempDir() 在某些环境里会超出。
func shortDataDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rmh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func fakeRunOptions(dataDir string) (RunOptions, *ptyhost.FakeSpawner) {
	spawn := &ptyhost.FakeSpawner{}
	return RunOptions{
		DataDir: dataDir,
		Version: "1.0-test",
		Spawner: spawn,
		Shell: func(rows, cols uint16) (ptyhost.Spec, error) {
			return ptyhost.Spec{Path: "/bin/fakeshell", Rows: rows, Cols: cols}, nil
		},
	}, spawn
}

// startRun 在后台运行守护进程，等它开始监听。返回套接字路径、取消函数与「等待 Run 返回」的函数（可重复调用）。
func startRun(t *testing.T, opts RunOptions) (socket string, ctxCancel context.CancelFunc, wait func() error) {
	t.Helper()
	ready := make(chan string, 1)
	opts.Ready = func(s string) { ready <- s }
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var result error
	go func() {
		result = Run(ctx, opts)
		close(finished)
	}()
	select {
	case socket = <-ready:
	case <-finished:
		cancel()
		t.Fatalf("Run 提前返回: %v", result)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("守护进程没有在 5 秒内就绪")
	}
	wait = func() error {
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("Run 没有在 10 秒内返回")
		}
		return result
	}
	t.Cleanup(func() { cancel(); _ = wait() })
	return socket, cancel, wait
}

func dialRole(t *testing.T, socket, role string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	cli, err := Handshake(ctx, conn, role, "test")
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	t.Cleanup(func() { _ = cli.Conn.Close() })
	return cli
}

// ---------- 帧化与连接 ----------

func TestStreamConnRoundTripAndPing(t *testing.T) {
	a, b := NewPipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		typ, data, err := b.Read(ctx)
		if err != nil {
			return
		}
		_ = b.Write(ctx, typ, append([]byte("echo:"), data...))
	}()
	if err := a.Write(ctx, MsgText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	typ, data, err := a.Read(ctx)
	if err != nil || typ != MsgText || string(data) != "echo:hi" {
		t.Fatalf("往返不符: %v %v %q", typ, err, data)
	}

	// 对端的读取循环会自动回应 ping（即使上层此刻没有在读）。
	go func() { _, _, _ = b.Read(ctx) }()
	pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
	defer pcancel()
	if err := a.Ping(pctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// 两端在同一时刻互相 ping 必须都能得到应答。
//
// 守护进程与 Agent 各有一个 15 秒的心跳定时器，在内嵌模式下两个定时器几乎同时触发；进程内管道（net.Pipe）
// 是同步的——写入要等对端读走才返回。如果读循环自己去写 pong，两端的读循环会互相等对方来读而卡死，
// 而它们占着的写锁又让后续的 ping 一直排队，直到 45 秒的 PongTimeout 把连接判死。
// 真实套接字有内核缓冲，不会暴露这个问题，所以必须用进程内管道专门覆盖。
func TestStreamConnSimultaneousPing(t *testing.T) {
	a, b := NewPipe()
	defer a.Close()
	defer b.Close()
	for i := 0; i < 400; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, c := range []Conn{a, b} {
			c := c
			go func() {
				<-start
				errs <- c.Ping(ctx)
			}()
		}
		close(start)
		for k := 0; k < 2; k++ {
			if err := <-errs; err != nil {
				cancel()
				t.Fatalf("第 %d 轮同时互 ping 失败：%v", i, err)
			}
		}
		cancel()
	}
}

// 写权的等待要能被上下文打断：一个写不出去的写者不应让其他写者无限期排队。
func TestStreamConnWriteWaitHonorsContext(t *testing.T) {
	a, b := NewPipe()
	defer a.Close()
	defer b.Close()
	// b 没有消费者：它的入队列（inboundQueue 条）写满后，读循环再读走一条就停在入队处，之后的写入会一直阻塞。
	// 因此前 inboundQueue+1 条写入都能立刻完成，再往后一条就会卡住。
	frame := make([]byte, 256)
	setup, setupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer setupCancel()
	for i := 0; i < inboundQueue+1; i++ {
		if err := a.Write(setup, MsgBinary, frame); err != nil {
			t.Fatalf("第 %d 条写入应当立刻完成：%v", i, err)
		}
	}

	// 第一个写者卡在管道写入上，并一直占着写权（它自己的期限是 5 秒）。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Write(ctx, MsgBinary, frame)
	}()
	time.Sleep(100 * time.Millisecond)

	// 第二个写者只愿等 200 毫秒：应在自己的期限内返回，而不是被前一个写者拖到 5 秒。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	begin := time.Now()
	err := a.Write(ctx, MsgBinary, frame)
	if err == nil {
		t.Fatal("对端不读取时写入不应成功")
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Fatalf("排队的写者应在自己的期限（200ms）内返回，实际等了 %v", d)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应因上下文超时返回，得到 %v", err)
	}
}

func TestStreamConnBinaryAndLargeMessages(t *testing.T) {
	a, b := NewPipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	big := make([]byte, MaxIPCMessage)
	for i := range big {
		big[i] = byte(i)
	}
	errc := make(chan error, 1)
	go func() { errc <- a.Write(ctx, MsgBinary, big) }()
	typ, data, err := b.Read(ctx)
	if err != nil || typ != MsgBinary || len(data) != len(big) || data[12345] != big[12345] {
		t.Fatalf("最大尺寸的二进制帧应能完整传递: %v %v %d", typ, err, len(data))
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if err := a.Write(ctx, MsgText, make([]byte, MaxIPCMessage+1)); err == nil {
		t.Fatal("超过上限的帧应被拒绝")
	}
}

func TestStreamConnCloseUnblocksRead(t *testing.T) {
	a, b := NewPipe()
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := a.Read(context.Background())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = a.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("关闭后读取应返回错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 应唤醒阻塞的 Read")
	}
	// 对端关闭 → 本端读到错误。
	c, d := NewPipe()
	_ = d.Close()
	if _, _, err := c.Read(context.Background()); err == nil {
		t.Fatal("对端关闭后读取应返回错误")
	}
}

func TestStreamConnReadHonorsContext(t *testing.T) {
	a, b := NewPipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := a.Read(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应因上下文超时返回，得到 %v", err)
	}
}

// ---------- 套接字 ----------

func TestSocketPathFallbackForLongDataDir(t *testing.T) {
	short := "/var/lib/relaymesh-agent"
	if got := SocketPath(short); got != filepath.Join(short, "holder", socketFileName) {
		t.Fatalf("短路径应直接放在数据目录下: %s", got)
	}
	long := "/" + strings.Repeat("deep/", 30) + "data"
	p := SocketPath(long)
	if len(p) > maxSocketPath {
		t.Fatalf("路径过长（%d）：%s", len(p), p)
	}
	if p != SocketPath(long) {
		t.Fatal("同一个数据目录必须总是算出同一个路径（守护进程与 Agent 要对得上）")
	}
	if p == SocketPath(long+"2") {
		t.Fatal("不同的数据目录不能共用同一个套接字")
	}
}

func TestListenDialRoundTrip(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, _ := fakeRunOptions(dataDir)
	socket, cancel, wait := startRun(t, opts)

	cli := dialRole(t, socket, RoleAdmin)
	ctx, c2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer c2()
	st, err := Status(ctx, cli)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hello.Holder != "1.0-test" || !st.Hello.Persistent || st.Controller {
		t.Fatalf("状态不符: %+v", st)
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("取消上下文应干净退出: %v", err)
	}
	// 用目录列表而不是 os.Stat 判断：Windows 上对 AF_UNIX 套接字文件 Stat 会报错，那样会让断言空转。
	entries, err := os.ReadDir(filepath.Dir(socket))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == filepath.Base(socket) {
			t.Fatal("退出后套接字文件应被清理")
		}
	}
}

func TestListenRefusesSecondInstance(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, _ := fakeRunOptions(dataDir)
	startRun(t, opts)

	opts2, _ := fakeRunOptions(dataDir)
	err := Run(context.Background(), opts2)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("第二个实例应被拒绝并返回 ErrAlreadyRunning，得到 %v", err)
	}
}

// 第二个实例必须在「唯一实例检查」处就被拒绝，而不是先去动状态文件和终端记录、再因「已经在运行」退出：
// 否则它会把第一个实例里仍在运行的会话在记录里标成「守护进程退出」，并用自己的空状态覆盖状态文件。
// （真实事故：在装有正式 Agent 的机器上误起了第二个守护进程，正在使用的会话被标成了已结束。）
func TestSecondInstanceLeavesRunningSessionsUntouched(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, _ := fakeRunOptions(dataDir)
	opts.Record.MinFree = -1 // 不受测试机磁盘余量影响
	socket, _, _ := startRun(t, opts)
	sid := openOver(t, dialRole(t, socket, RoleController))

	statePath := filepath.Join(StateDir(dataDir), stateFileName)
	metaPath := filepath.Join(RecordDir(dataDir), "sessions", sid, "meta.json")
	readState := func() (persistedState, bool) {
		raw, err := os.ReadFile(statePath)
		if err != nil {
			return persistedState{}, false
		}
		var st persistedState
		return st, json.Unmarshal(raw, &st) == nil
	}

	// 等第一个实例把会话落盘：状态文件（防抖之后异步保存）与记录元数据。
	var before persistedState
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, ok := readState()
		_, metaErr := os.Stat(metaPath)
		if ok && len(st.Sessions) == 1 && metaErr == nil {
			before = st
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("第一个实例没有把会话落盘: state=%+v meta=%v", st, metaErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	opts2, _ := fakeRunOptions(dataDir)
	opts2.Record.MinFree = -1
	if err := Run(context.Background(), opts2); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("第二个实例应被拒绝并返回 ErrAlreadyRunning，得到 %v", err)
	}

	// 运行中的会话：记录里不能出现「已结束」。
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("元数据应仍是有效的 JSON: %v\n%s", err, raw)
	}
	if _, ended := meta["ended_ms"]; ended || meta["ended_reason"] != nil {
		t.Fatalf("第二个实例不应把运行中的会话标成已结束: %s", raw)
	}
	// 状态文件：仍是第一个实例写的那份（启动时刻与会话列表都没变，也不是「正常关停」）。
	after, ok := readState()
	if !ok || after.Started != before.Started || len(after.Sessions) != 1 || after.Sessions[0].SID != sid || after.Clean {
		t.Fatalf("第二个实例不应覆盖状态文件: before=%+v after=%+v", before, after)
	}
	// 第一个实例照常服务。
	dialRole(t, socket, RoleAdmin)
}

func TestStaleSocketFileIsReplaced(t *testing.T) {
	dataDir := shortDataDir(t)
	socket := SocketPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("leftover from a crashed holder"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, _ := fakeRunOptions(dataDir)
	got, _, _ := startRun(t, opts)
	if got != socket {
		t.Fatalf("套接字路径不符: %s", got)
	}
	dialRole(t, socket, RoleAdmin) // 能连上说明残留文件已被替换
}

func TestDialReportsNotRunning(t *testing.T) {
	dataDir := shortDataDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Dial(ctx, SocketPath(dataDir)); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("目录都不存在应返回 ErrNotRunning，得到 %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(SocketPath(dataDir)), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(ctx, SocketPath(dataDir)); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("没有套接字应返回 ErrNotRunning，得到 %v", err)
	}
}

func TestSocketDirIsTightenedAndSymlinkRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix 权限位语义")
	}
	dataDir := shortDataDir(t)
	dir := StateDir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts, _ := fakeRunOptions(dataDir)
	startRun(t, opts)
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("目录权限应被收紧为仅本人可访问，实际 %o", st.Mode().Perm())
	}

	// 符号链接形式的目录一律拒绝：不跟随到别处。
	base := shortDataDir(t)
	target := filepath.Join(base, "real")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := checkSocketDir(link); err == nil {
		t.Fatal("符号链接目录应被拒绝")
	}
}

// ---------- 运行循环 ----------

func TestRunShutdownNowTerminatesAndRecordsStop(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, spawn := fakeRunOptions(dataDir)
	socket, _, wait := startRun(t, opts)

	ctl := dialRole(t, socket, RoleController)
	sid := openOver(t, ctl)
	adm := dialRole(t, socket, RoleAdmin)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := SendShutdown(ctx, adm.Conn, ShutdownNow); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatalf("关停请求应让 Run 干净返回: %v", err)
	}
	if !spawn.Terminals()[0].IsClosed() {
		t.Fatal("关停应终止会话的终端")
	}

	// 重启之后能解释「这个会话为什么没了」。
	opts2, _ := fakeRunOptions(dataDir)
	socket2, _, _ := startRun(t, opts2)
	cli := dialRole(t, socket2, RoleAdmin)
	if len(cli.Hello.Lost) != 1 || cli.Hello.Lost[0].SID != sid || cli.Hello.Lost[0].Reason != LostHolderStopped {
		t.Fatalf("重启后应报告 holder_stopped: %+v", cli.Hello.Lost)
	}
}

func TestRunDrainExitsOnlyWhenSessionsEnd(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, spawn := fakeRunOptions(dataDir)
	socket, _, wait := startRun(t, opts)
	ctl := dialRole(t, socket, RoleController)
	openOver(t, ctl)

	adm := dialRole(t, socket, RoleAdmin)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := SendShutdown(ctx, adm.Conn, ShutdownDrain); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- wait() }()
	select {
	case <-exited:
		t.Fatal("还有运行中的会话，排空不应退出")
	case <-time.After(1200 * time.Millisecond):
	}
	// 最后一个会话自然结束 → 守护进程自动退出。
	spawn.Terminals()[0].Exit(ptyhost.ExitStatus{})
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("排空完成应干净退出: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("会话结束后守护进程应自动退出")
	}
}

// openOver 在控制端连接上新建一个会话，返回 SID。
func openOver(t *testing.T, cli *Client) string {
	t.Helper()
	p := &peer{t: t, client: cli}
	p.expect(termproto.TSessions)
	info := p.open("t")
	return info.SID
}

func TestRunStopsOnContextCancelAndSavesCleanState(t *testing.T) {
	dataDir := shortDataDir(t)
	opts, _ := fakeRunOptions(dataDir)
	socket, cancel, wait := startRun(t, opts)
	openOver(t, dialRole(t, socket, RoleController))

	cancel()
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(StateDir(dataDir), stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Clean || len(st.Sessions) != 1 {
		t.Fatalf("有意停止应落盘 clean 标记并列出当时的会话: %+v", st)
	}
}

func dialRaw(socket string) (net.Conn, error) { return net.Dial("unix", socket) }
