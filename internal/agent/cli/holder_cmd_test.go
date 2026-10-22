package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 选项解析 ----------

func TestResolveHolderDefaultsAndValidation(t *testing.T) {
	env := testEnv(t)
	base := common{server: "https://relay.example.com", dataDir: t.TempDir(), configDir: t.TempDir()}

	r, err := base.resolve(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.holderMode != agent.HolderAuto {
		t.Fatalf("run 的默认应为 auto，得到 %q", r.holderMode)
	}
	if want := holder.SocketPath(base.dataDir); r.holderSocket != want {
		t.Fatalf("默认套接字应由数据目录推算: %s != %s", r.holderSocket, want)
	}

	explicit := base
	explicit.holderSocket = "/run/custom.sock"
	explicit.holderMode = "external"
	r, err = explicit.resolve(env, false)
	if err != nil || r.holderSocket != "/run/custom.sock" || r.holderMode != agent.HolderExternal {
		t.Fatalf("显式值应原样保留: %+v %v", r, err)
	}

	bad := base
	bad.holderMode = "daemon"
	if _, err := bad.resolve(env, false); err == nil {
		t.Fatal("未知模式应报错")
	}
}

func TestTemporaryIdentityForcesEmbedded(t *testing.T) {
	env := testEnv(t)
	c := common{server: "https://relay.example.com", dataDir: t.TempDir(), configDir: t.TempDir(), temp: true}
	r, err := c.resolve(env, false)
	if err != nil || r.holderMode != agent.HolderEmbedded {
		t.Fatalf("临时身份必须内嵌: %q %v", r.holderMode, err)
	}
	c.holderMode = "external"
	if _, err := c.resolve(env, false); err == nil {
		t.Fatal("临时身份 + external 会产生无人接管的孤儿会话，必须拒绝")
	}
}

func TestHolderServiceArgs(t *testing.T) {
	c := common{
		server: "https://relay.example.com", pins: stringList{"sha256:aa"},
		dataDir: "/var/lib/x", maxSessions: 4, logFile: `/var/log/agent.log`, holderSocket: "/run/x.sock",
	}
	agentArgs := strings.Join(c.agentServiceArgs(true), " ")
	if !strings.HasSuffix(agentArgs, "--holder external --holder-socket /run/x.sock") {
		t.Errorf("双服务模式下 Agent 只用守护进程: %s", agentArgs)
	}
	if got := strings.Join(c.agentServiceArgs(false), " "); !strings.HasSuffix(got, "--holder embedded") {
		t.Errorf("单服务模式要明确写出 embedded: %s", got)
	}
	holderArgs := strings.Join(c.holderServiceArgs(), " ")
	want := "holder run --data-dir /var/lib/x --holder-socket /run/x.sock --max-sessions 4 --log-file /var/log/agent-holder.log"
	if holderArgs != want {
		t.Errorf("\n got %s\nwant %s", holderArgs, want)
	}
	for _, leak := range []string{"--server", "--pin", "https://"} {
		if strings.Contains(holderArgs, leak) {
			t.Errorf("守护进程不需要也不该拿到服务端信息: %s", holderArgs)
		}
	}
	if got := strings.Join((&common{}).holderServiceArgs(), " "); got != "holder run" {
		t.Errorf("最小参数: %s", got)
	}
}

func TestSiblingLogFile(t *testing.T) {
	cases := map[string]string{
		"/var/log/agent.log":        "/var/log/agent-holder.log",
		`C:\logs\relaymesh.log`:     `C:\logs\relaymesh-holder.log`,
		"/var/log/noext":            "/var/log/noext-holder",
		"/var/log/a.b/agent.log.gz": "/var/log/a.b/agent.log-holder.gz",
	}
	for in, want := range cases {
		if got := siblingLogFile(in, "holder"); got != want {
			t.Errorf("siblingLogFile(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// ---------- holder 子命令 ----------

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rmh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type runningHolder struct {
	dataDir string
	socket  string
	spawn   *ptyhost.FakeSpawner
	cancel  context.CancelFunc
	done    chan error
}

func startHolderForCLI(t *testing.T) *runningHolder {
	t.Helper()
	return startHolderForCLIWithRev(t, "")
}

// startHolderForCLIWithRev 与 startHolderForCLI 相同，但守护进程带着给定的「Agent 侧代码指纹」（空表示旧版本 / 非发布构建）。
func startHolderForCLIWithRev(t *testing.T, rev string) *runningHolder {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 AF_UNIX 行为由专门的实机验证覆盖")
	}
	dir := shortDir(t)
	spawn := &ptyhost.FakeSpawner{}
	ready := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	rh := &runningHolder{dataDir: dir, spawn: spawn, cancel: cancel, done: make(chan error, 1)}
	go func() {
		rh.done <- holder.Run(ctx, holder.RunOptions{
			DataDir: dir, Version: "1.7.0-cli-test", Rev: rev, Spawner: spawn,
			Shell: func(rows, cols uint16) (ptyhost.Spec, error) {
				return ptyhost.Spec{Path: "/bin/fakeshell", Rows: rows, Cols: cols}, nil
			},
			Ready: func(s string) { ready <- s },
		})
	}()
	select {
	case rh.socket = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("守护进程没有就绪")
	}
	t.Cleanup(func() { cancel(); <-rh.done })
	return rh
}

// openSession 通过控制端连接在守护进程里新建一个会话（Hub 的 Agent 在真实环境里做同样的事）。
func (rh *runningHolder) openSession(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := holder.Dial(ctx, rh.socket)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := holder.Handshake(ctx, conn, holder.RoleController, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Conn.Close() })
	raw, _ := termproto.Marshal(termproto.Open{T: termproto.TOpen, Req: "r", Name: "build", Rows: 24, Cols: 80})
	if err := cli.Conn.Write(ctx, holder.MsgText, raw); err != nil {
		t.Fatal(err)
	}
	for {
		typ, data, err := cli.Conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == holder.MsgText && strings.Contains(string(data), `"t":"opened"`) {
			return
		}
	}
}

// startModelCommand 通过控制端连接让守护进程运行一条模型命令（Hub 的 Agent 在真实环境里做同样的事）。
// 模型命令由守护进程里的 ExecManager 持有，不在会话表里——这正是排空与确认提示曾经漏掉它们的原因。
func (rh *runningHolder) startModelCommand(t *testing.T, n int, cmd string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("模型命令的 shell 约定在 Windows 上另有实机验证")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := holder.Dial(ctx, rh.socket)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := holder.Handshake(ctx, conn, holder.RoleController, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Conn.Close() })
	raw, _ := termproto.Marshal(termproto.Run{T: termproto.TRun, ID: fmt.Sprintf("%032x", n), Cmd: cmd})
	if err := cli.Conn.Write(ctx, holder.MsgText, raw); err != nil {
		t.Fatal(err)
	}
	for {
		typ, data, err := cli.Conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == holder.MsgText && strings.Contains(string(data), `"t":"run_started"`) {
			return
		}
	}
}

func TestHolderStatusShowsModelCommands(t *testing.T) {
	rh := startHolderForCLI(t)
	rh.startModelCommand(t, 1, "sleep 30")

	code, env := runCLI(t, []string{"holder", "status", "--data-dir", rh.dataDir}, nil)
	if code != 0 || !strings.Contains(out(env), "模型命令 : 1 条运行中") {
		t.Fatalf("status 应列出运行中的模型命令: %d\n%s%s", code, out(env), errOut(env))
	}
}

// 回归（#73）：守护进程里只有模型命令、没有会话时，restart / stop 以前不问一声就把命令杀了。
func TestHolderStopRequiresConfirmationWhenOnlyModelCommandsRunning(t *testing.T) {
	rh := startHolderForCLI(t)
	rh.startModelCommand(t, 2, "sleep 30")

	code, env := runCLI(t, []string{"holder", "stop", "--data-dir", rh.dataDir}, nil)
	if code != 1 || !strings.Contains(errOut(env), "--yes") || !strings.Contains(out(env), "1 条模型命令") {
		t.Fatalf("只有模型命令在跑、又没确认时必须拒绝并说明有几条: %d\n%s%s", code, out(env), errOut(env))
	}
	select {
	case <-rh.done:
		t.Fatal("被拒绝的 stop 不应让守护进程退出")
	case <-time.After(200 * time.Millisecond):
	}
}

// 回归（#73）：holder stop --drain 要等模型命令跑完才退出，而不是只等会话。
func TestHolderDrainWaitsForModelCommands(t *testing.T) {
	rh := startHolderForCLI(t)
	rh.startModelCommand(t, 3, "sleep 2")

	code, env := runCLI(t, []string{"holder", "stop", "--drain", "--data-dir", rh.dataDir}, nil)
	if code != 0 || !strings.Contains(out(env), "1 条模型命令") || !strings.Contains(out(env), "holder status") {
		t.Fatalf("排空应说明在等什么、并告诉怎么查看进度: %d\n%s%s", code, out(env), errOut(env))
	}
	select {
	case <-rh.done:
		t.Fatal("还有运行中的模型命令，排空不应退出")
	case <-time.After(1200 * time.Millisecond):
	}
	select {
	case err := <-rh.done:
		rh.done <- err
	case <-time.After(6 * time.Second):
		t.Fatal("命令结束后守护进程应自动退出")
	}
}

func TestStaleHolderNoteAndStatusWithoutRunsField(t *testing.T) {
	drain, plain := staleHolderNote(true), staleHolderNote(false)
	if !strings.Contains(drain, "不会等模型正在运行的命令") || !strings.Contains(plain, "会随重启被终止") {
		t.Fatalf("旧守护进程的提醒不对:\n%s\n%s", drain, plain)
	}
	for _, s := range []string{drain, plain} {
		if strings.ContainsRune(s, '`') {
			t.Fatalf("提醒里不该有反引号: %s", s)
		}
	}
	// 旧守护进程的状态应答没有 runs 字段：status 要如实说「未统计」，而不是显示 0 条。
	env := testEnv(t)
	printHolderStatus(env, "/x/holder.sock", holder.IPCStatusReply{Hello: holder.IPCHello{Holder: "1.0.0"}})
	if o := out(env); !strings.Contains(o, "模型命令 : 未统计") || strings.Contains(o, "0 条") {
		t.Fatalf("没有 runs 字段时不能显示成 0 条:\n%s", o)
	}
	if got := describeHolderWork(2, nil); got != "2 个会话" {
		t.Fatalf("旧守护进程只写会话: %q", got)
	}
	n := 1
	if got := describeHolderWork(2, &n); got != "2 个会话和 1 条模型命令" {
		t.Fatalf("describeHolderWork = %q", got)
	}
}

func TestHolderStatusCommand(t *testing.T) {
	rh := startHolderForCLI(t)
	rh.openSession(t)

	code, env := runCLI(t, []string{"holder", "status", "--data-dir", rh.dataDir}, nil)
	if code != 0 {
		t.Fatalf("退出码 = %d\n%s%s", code, out(env), errOut(env))
	}
	o := out(env)
	for _, want := range []string{"1.7.0-cli-test", "持久性", "会话     : 1 个", "build", "running"} {
		if !strings.Contains(o, want) {
			t.Errorf("状态输出缺少 %q:\n%s", want, o)
		}
	}
}

// 守护进程的代码指纹：status 显示它（没有指纹的旧版本 / 非发布构建不显示这一行）。
func TestHolderStatusShowsRev(t *testing.T) {
	const rev = "72ae54bd8799"
	rh := startHolderForCLIWithRev(t, rev)
	code, env := runCLI(t, []string{"holder", "status", "--data-dir", rh.dataDir}, nil)
	if code != 0 || !strings.Contains(out(env), "代码指纹 : "+rev) {
		t.Fatalf("status 应显示守护进程的代码指纹: %d\n%s%s", code, out(env), errOut(env))
	}

	plain := startHolderForCLI(t)
	code, env = runCLI(t, []string{"holder", "status", "--data-dir", plain.dataDir}, nil)
	if code != 0 || strings.Contains(out(env), "代码指纹") {
		t.Fatalf("没有指纹时不应显示这一行: %d\n%s%s", code, out(env), errOut(env))
	}
}

// #124 的现场：升级后版本号不同，但守护进程与新程序的 Agent 代码一模一样。此时不该再提示「想换上新版本时执行 holder restart」，
// 那会让人白白担心要不要结束会话；指纹不同、或有一边没有指纹时仍按原样提示。
func TestReportHolderVersionUsesRev(t *testing.T) {
	const rev = "72ae54bd8799"
	report := func(t *testing.T, holderRev, installedRev string) string {
		t.Helper()
		rh := startHolderForCLIWithRev(t, holderRev)
		env := testEnv(t)
		env.Version, env.Rev = "1.8.0-newer-build", installedRev // 与守护进程的 1.7.0-cli-test 不同
		reportHolderVersion(context.Background(), env, ivOf("linux", false, "", true), "/usr/local/bin/relaymesh-agent", rh.socket, "升级")
		return out(env)
	}

	t.Run("指纹一致：不需要重启，也不给 holder restart 命令", func(t *testing.T) {
		o := report(t, rev, rev)
		if !strings.Contains(o, "不需要重启守护进程") || !strings.Contains(o, "1.7.0-cli-test") || !strings.Contains(o, "1.8.0-newer-build") {
			t.Fatalf("应说明代码一致、无需重启，并带上两个版本号:\n%s", o)
		}
		if strings.Contains(o, "holder restart") {
			t.Fatalf("代码一致时不应再给出 holder restart:\n%s", o)
		}
	})
	t.Run("指纹不同：照旧提示重启", func(t *testing.T) {
		o := report(t, rev, "0123456789ab")
		if !strings.Contains(o, "没有重启它") || !strings.Contains(o, "holder restart") || strings.Contains(o, "不需要重启") {
			t.Fatalf("代码确实变了应提示 holder restart:\n%s", o)
		}
	})
	t.Run("守护进程没有指纹（旧版本）：照旧提示重启", func(t *testing.T) {
		o := report(t, "", rev)
		if !strings.Contains(o, "holder restart") || strings.Contains(o, "不需要重启") {
			t.Fatalf("旧守护进程无法确认代码一致，应提示 holder restart:\n%s", o)
		}
	})
	t.Run("本次程序没有指纹（非发布构建）：照旧提示重启", func(t *testing.T) {
		o := report(t, rev, "")
		if !strings.Contains(o, "holder restart") || strings.Contains(o, "不需要重启") {
			t.Fatalf("无法确认代码一致，应提示 holder restart:\n%s", o)
		}
	})
	t.Run("版本号一样：什么都不说", func(t *testing.T) {
		rh := startHolderForCLIWithRev(t, rev)
		env := testEnv(t)
		env.Version, env.Rev = "1.7.0-cli-test", rev
		reportHolderVersion(context.Background(), env, ivOf("linux", false, "", true), "/usr/local/bin/relaymesh-agent", rh.socket, "升级")
		if o := out(env); o != "" {
			t.Fatalf("版本号一致不应有任何提示:\n%s", o)
		}
	})
}

func TestHolderStatusWhenNotRunning(t *testing.T) {
	dir := shortDir(t)
	code, env := runCLI(t, []string{"holder", "status", "--data-dir", dir}, nil)
	if code != 1 || !strings.Contains(errOut(env), "没有运行") {
		t.Fatalf("没有守护进程应明确说明: %d %q", code, errOut(env))
	}
}

func TestHolderStopRequiresConfirmationWhenSessionsRunning(t *testing.T) {
	rh := startHolderForCLI(t)
	rh.openSession(t)

	// 非交互且没有 --yes：不动手，说明原因。
	code, env := runCLI(t, []string{"holder", "stop", "--data-dir", rh.dataDir}, nil)
	if code != 1 || !strings.Contains(errOut(env), "--yes") {
		t.Fatalf("有运行中的会话又没确认时必须拒绝: %d %q", code, errOut(env))
	}
	select {
	case <-rh.done:
		t.Fatal("被拒绝的 stop 不应让守护进程退出")
	case <-time.After(200 * time.Millisecond):
	}

	code, env = runCLI(t, []string{"holder", "stop", "--yes", "--data-dir", rh.dataDir}, nil)
	if code != 0 || !strings.Contains(out(env), "已退出") {
		t.Fatalf("--yes 之后应停止成功: %d\n%s%s", code, out(env), errOut(env))
	}
	select {
	case err := <-rh.done:
		rh.done <- err
	case <-time.After(5 * time.Second):
		t.Fatal("守护进程应已退出")
	}
}

func TestHolderRestartReportsWhenNobodyRelaunchesIt(t *testing.T) {
	rh := startHolderForCLI(t)
	code, env := runCLI(t, []string{"holder", "restart", "--yes", "--wait", "1500ms", "--data-dir", rh.dataDir}, nil)
	if code != 1 || !strings.Contains(out(env), "没有被重新拉起") {
		t.Fatalf("没有服务管理器时 restart 要如实说明没被拉起: %d\n%s%s", code, out(env), errOut(env))
	}
}

func TestHolderUsageAndUnknownSubcommand(t *testing.T) {
	code, env := runCLI(t, []string{"holder"}, nil)
	if code != 2 || !strings.Contains(out(env), "会话守护进程") {
		t.Fatalf("无子命令应打印用法: %d %q", code, out(env))
	}
	code, env = runCLI(t, []string{"holder", "frobnicate"}, nil)
	if code != 2 || !strings.Contains(errOut(env), "未知") {
		t.Fatalf("未知子命令: %d %q", code, errOut(env))
	}
	code, _ = runCLI(t, []string{"holder", "--help"}, nil)
	if code != 0 {
		t.Fatalf("--help 退出码 = %d", code)
	}
}

func TestHolderRunRefusesSecondInstance(t *testing.T) {
	rh := startHolderForCLI(t)
	code, env := runCLI(t, []string{"holder", "run", "--data-dir", rh.dataDir}, nil)
	if code != 1 || !strings.Contains(errOut(env), "已经在运行") {
		t.Fatalf("第二个实例应被拒绝: %d %q", code, errOut(env))
	}
}

func TestUsageMentionsHolder(t *testing.T) {
	_, env := runCLI(t, []string{"help"}, nil)
	o := out(env)
	for _, want := range []string{"holder", "--holder", "会话守护进程"} {
		if !strings.Contains(o, want) {
			t.Errorf("帮助里应提到 %q", want)
		}
	}
}
