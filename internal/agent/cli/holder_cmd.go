package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

// holderFlags 是 `holder` 子命令共用的选项。
type holderFlags struct {
	dataDir     string
	socket      string
	logFile     string
	maxSessions int
	drain       bool
	yes         bool
	wait        time.Duration

	recordFlags
	execFlags
	sshFlags
}

func (h *holderFlags) bind(fs *flag.FlagSet, env Env) {
	fs.StringVar(&h.dataDir, "data-dir", env.Getenv(envDataDir), "数据目录（守护进程的状态与套接字放在其下的 holder/ 里）")
	fs.StringVar(&h.socket, "holder-socket", env.Getenv(envHolderSocket), "守护进程的本地套接字路径（默认在数据目录下）")
	fs.StringVar(&h.logFile, "log-file", env.Getenv(envLogFile), "把日志写入该文件（自动轮转）")
	fs.IntVar(&h.maxSessions, "max-sessions", 0, "本机会话数上限（0 表示默认值）")
	fs.BoolVar(&h.drain, "drain", false, "排空：不再接受新会话和新命令，等运行中的会话与模型命令自然结束后再退出（restart / stop）")
	fs.BoolVar(&h.yes, "yes", false, "不再询问，直接执行（restart / stop）")
	fs.DurationVar(&h.wait, "wait", 30*time.Second, "restart / stop 等待守护进程退出（以及被服务管理器拉起）的最长时间")
	h.recordFlags.bind(fs, env)
	h.execFlags.bind(fs, env)
	h.sshFlags.bind(fs, env)
}

func (h *holderFlags) paths() (dataDir, socket string) {
	dataDir = h.dataDir
	if dataDir == "" {
		dataDir = agent.DefaultDataDir()
	}
	socket = h.socket
	if socket == "" {
		socket = holder.SocketPath(dataDir)
	}
	return dataDir, socket
}

func holderUsage(env Env) {
	env.printf(`会话守护进程 —— 持有终端会话，让 Agent 升级 / 重启 / 崩溃都不会打断正在运行的命令

用法:
  %[1]s holder <子命令> [选项]

子命令:
  run       前台运行守护进程（通常由 install 安装的服务单元调用，不需要手动执行）
  status    显示守护进程的版本、持久性与全部会话
  restart   请求守护进程退出：会结束其中全部会话和模型正在运行的命令；由服务管理器（systemd / launchd / 计划任务）负责重新拉起
  stop      同 restart，但不期待被拉起（用于确认它已退出）

选项:
  --data-dir <目录>        数据目录（默认按用户 / 系统区分）
  --holder-socket <路径>   本地套接字路径（默认在数据目录下）
  --max-sessions <N>       本机会话数上限（仅 run）
  --log-file <文件>        日志写入文件（仅 run）
  --no-record              本机一票否决：不录制任何终端会话（仅 run；服务端无法覆盖）
  --record-session-mb <N>  单会话记录上限，MiB（默认 64，仅 run）
  --record-host-mb <N>     本机全部记录的磁盘上限，MiB（默认 512，仅 run）
  --record-retention-days <N>  会话结束后的保留天数（默认 30，仅 run）
  --record-min-free-mb <N> 磁盘剩余空间低于该值时停止录制，MiB（默认 1024，-1 关闭保护，仅 run）
  --deny-model-exec        本机一票否决：禁止模型在本机执行命令（仅 run；服务端无法覆盖，人工终端不受影响）
  --no-ssh-gateway         关闭 SSH 网关（仅 run）。网关默认开启：持有服务端借这台主机建立的 SSH 终端会话，保存目标的登录凭据，并代为执行模型发起的命令
  --ssh-allow <目标>       网关只允许连接这些目标：CIDR / IP / 主机名（可含 * 通配，可重复，仅 run）
  --drain                  排空后再退出：不结束运行中的会话和模型命令，等它们自然结束（期间不再接受新的）（restart / stop）
  --yes                    不再询问（restart / stop）
  --wait <时长>            等待守护进程退出 / 被拉起的最长时间（默认 30s）

说明:
  守护进程只监听本机的 Unix 套接字（权限仅限当前用户），不监听任何网络端口。
  升级二进制后运行中的守护进程仍是旧版本，下次需要时执行 holder restart（会结束其中的会话和模型命令；想等它们自然结束就加 --drain）即可换上新版本。
`, env.Prog)
}

func cmdHolder(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 {
		holderUsage(env)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdHolderRun(ctx, args[1:], env)
	case "status":
		return cmdHolderStatus(ctx, args[1:], env)
	case "restart":
		return cmdHolderStop(ctx, args[1:], env, true)
	case "stop":
		return cmdHolderStop(ctx, args[1:], env, false)
	case "help", "--help", "-h":
		holderUsage(env)
		return 0
	}
	env.errorf("未知的 holder 子命令 %q\n\n", args[0])
	holderUsage(env)
	return 2
}

func parseHolderFlags(name string, args []string, env Env) (holderFlags, bool, int) {
	var hf holderFlags
	fs := newFlagSet(name, env)
	hf.bind(fs, env)
	if err := parse(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			holderUsage(env)
			return hf, false, 0
		}
		env.errorf("参数错误: %v\n", err)
		return hf, false, 2
	}
	return hf, true, 0
}

// cmdHolderRun 前台运行会话守护进程，直到收到停止信号或关停请求。
func cmdHolderRun(ctx context.Context, args []string, env Env) int {
	hf, ok, code := parseHolderFlags("holder run", args, env)
	if !ok {
		return code
	}
	dataDir, socket := hf.paths()
	limits := agent.DefaultLimits()
	if hf.maxSessions < 0 {
		env.errorf("参数错误: --max-sessions 不能为负数\n")
		return 2
	}
	if hf.maxSessions > 0 {
		limits.MaxSessions = hf.maxSessions
	}
	if err := hf.recordFlags.validate(); err != nil {
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	if err := hf.sshFlags.validate(); err != nil {
		env.errorf("参数错误: %v\n", err)
		return 2
	}

	if hf.logFile != "" {
		lf, err := openRotatingFile(hf.logFile, logFileMaxBytes)
		if err != nil {
			env.errorf("无法打开日志文件 %s: %v\n", hf.logFile, err)
			return 1
		}
		defer lf.Close()
		env.Stdout, env.Stderr = lf, lf
	}

	logger := log.New(env.Stderr, "[holder] ", log.LstdFlags)
	env.printf("RelayMesh 会话守护进程 %s\n  数据目录 : %s\n  本地套接字: %s\n\n", env.Version, dataDir, socket)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := holder.Run(ctx, holder.RunOptions{
		DataDir:   dataDir,
		Socket:    socket,
		Version:   env.Version,
		Rev:       env.Rev,
		Limits:    limits,
		Record:    hf.recordFlags.options(dataDir),
		Exec:      hf.execFlags.options(dataDir, true),
		SSH:       hf.sshFlags.options(dataDir, env),
		Logf:      logger.Printf,
		HardenDir: agent.HardenDir,
	})
	switch {
	case errors.Is(err, holder.ErrAlreadyRunning):
		env.errorf("会话守护进程已经在运行（套接字 %s）\n", socket)
		return 1
	case err != nil:
		env.errorf("守护进程运行失败: %v\n", err)
		return 1
	}
	return 0
}

// dialAdmin 以管理端身份连上守护进程。
func dialAdmin(ctx context.Context, socket, version string) (*holder.Client, error) {
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := holder.Dial(dctx, socket)
	if err != nil {
		return nil, err
	}
	return holder.Handshake(dctx, conn, holder.RoleAdmin, version)
}

func describeDialError(env Env, err error, socket string) string {
	if errors.Is(err, holder.ErrNotRunning) {
		msg := fmt.Sprintf("会话守护进程没有运行（套接字 %s）", socket)
		// 最常见的「其实在跑」：系统级安装的守护进程以 root / 管理员运行，普通身份默认去找的是自己目录下的套接字
		if hint := systemHolderHint(env, realInvocation(), socket, pathExists); hint != "" {
			msg += "\n" + hint
		}
		return msg
	}
	var bye *holder.ByeError
	if errors.As(err, &bye) && bye.Reason == holder.ByeVersion {
		return "守护进程与本命令的本地协议版本不兼容，请把两者升级到同一版本"
	}
	return fmt.Sprintf("无法连接会话守护进程: %v", err)
}

func cmdHolderStatus(ctx context.Context, args []string, env Env) int {
	hf, ok, code := parseHolderFlags("holder status", args, env)
	if !ok {
		return code
	}
	_, socket := hf.paths()
	cli, err := dialAdmin(ctx, socket, env.Version)
	if err != nil {
		env.errorf("%s\n", describeDialError(env, err, socket))
		return 1
	}
	defer cli.Conn.Close()
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := holder.Status(sctx, cli)
	if err != nil {
		env.errorf("查询失败: %v\n", err)
		return 1
	}
	printHolderStatus(env, socket, st)
	return 0
}

func printHolderStatus(env Env, socket string, st holder.IPCStatusReply) {
	h := st.Hello
	env.printf("RelayMesh 会话守护进程 %s\n", h.Holder)
	if h.Rev != "" {
		env.printf("  代码指纹 : %s\n", h.Rev)
	}
	env.printf("  进程号   : %d\n", h.PID)
	env.printf("  已运行   : %s\n", sinceLabel(h.Started))
	env.printf("  本地套接字: %s\n", socket)
	if h.Persistent {
		env.printf("  持久性   : 会话可挺过 Agent 的升级 / 重启 / 崩溃\n")
	} else {
		env.printf("  持久性   : 内嵌（不具持久性）\n")
	}
	if st.Controller {
		env.printf("  Agent    : 已连接\n")
	} else {
		env.printf("  Agent    : 未连接（会话仍在运行，Agent 连上后自动接管）\n")
	}
	if st.Draining {
		env.printf("  状态     : 排空中（不再接受新会话和新命令，会话与模型命令都结束后自动退出）\n")
	}
	running := 0
	for _, s := range st.Sessions {
		if s.State == "running" {
			running++
		}
	}
	env.printf("  会话     : %d 个（运行中 %d）\n", len(st.Sessions), running)
	for _, s := range st.Sessions {
		name := s.Name
		if name == "" {
			name = "-"
		}
		env.printf("    %s  %-10s %-8s %s  已附着 %d  %s\n", s.SID[:8], s.State, name, sinceLabel(s.Created), s.Attached, s.Shell)
	}
	// 模型命令（terminal_exec）不在会话表里，单独一行：restart / stop 会终止它们，--drain 会等它们。
	if st.Runs != nil {
		env.printf("  模型命令 : %d 条运行中\n", *st.Runs)
	} else {
		env.printf("  模型命令 : 未统计（这个守护进程版本较旧）\n")
	}
	if len(h.Lost) > 0 {
		env.printf("  此前丢失的会话（%d 个）:\n", len(h.Lost))
		for _, l := range h.Lost {
			env.printf("    %s  %s  （%s）\n", shortSID(l.SID), holder.DescribeLost(l.Reason), sinceLabel(l.At))
		}
	}
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func shortSID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

// describeHolderWork 把运行中的会话数与模型命令数写成一句人话；runs 为 nil（旧守护进程没有这项统计）时只写会话。
func describeHolderWork(sessions int, runs *int) string {
	if runs == nil {
		return fmt.Sprintf("%d 个会话", sessions)
	}
	return fmt.Sprintf("%d 个会话和 %d 条模型命令", sessions, *runs)
}

// staleHolderNote 是对「版本较旧的守护进程」的提醒：它统计不到模型正在运行的命令，排空也不会等它们。
// 升级 Agent 之后第一次重启守护进程，面对的恰好就是这样的旧守护进程，所以必须把话说清楚，
// 而不是让人以为 --drain 也保护了模型命令。
func staleHolderNote(drain bool) string {
	if drain {
		return "注意：这个守护进程版本较旧，它的排空只等会话结束，不会等模型正在运行的命令——会话都结束后它就会退出，并终止还没跑完的命令。\n" +
			"没有运行中的模型命令时可以放心继续；不确定的话，先到 Web 上确认这台主机没有运行中的模型命令，再重启。"
	}
	return "注意：这个守护进程版本较旧，统计不到模型正在运行的命令；如果有，它们会随重启被终止。"
}

// sinceLabel 把一个 Unix 秒时间戳格式化成「N 分钟前」风格的短文本。
func sinceLabel(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天", int(d.Hours()/24))
}

// cmdHolderStop 请求守护进程关停；expectRestart 为 true 时再等它被服务管理器拉起。
func cmdHolderStop(ctx context.Context, args []string, env Env, expectRestart bool) int {
	name := "holder stop"
	if expectRestart {
		name = "holder restart"
	}
	hf, ok, code := parseHolderFlags(name, args, env)
	if !ok {
		return code
	}
	_, socket := hf.paths()
	cli, err := dialAdmin(ctx, socket, env.Version)
	if err != nil {
		env.errorf("%s\n", describeDialError(env, err, socket))
		return 1
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	st, err := holder.Status(sctx, cli)
	cancel()
	if err != nil {
		_ = cli.Conn.Close()
		env.errorf("查询失败: %v\n", err)
		return 1
	}
	oldPID := st.Hello.PID

	running := 0
	for _, s := range st.Sessions {
		if s.State == "running" {
			running++
		}
	}
	// 模型命令不在会话表里，要单独统计；runs 为 nil 表示这个守护进程太旧、没有这个统计。
	runs := 0
	if st.Runs != nil {
		runs = *st.Runs
	}
	if st.Runs == nil {
		env.printf("%s\n", staleHolderNote(hf.drain))
	}
	if !hf.drain && (running > 0 || runs > 0) && !hf.yes {
		env.printf("守护进程里有 %s在运行；继续将结束它们（里面运行的命令会被终止）。\n", describeHolderWork(running, st.Runs))
		env.printf("想等它们自然结束可以加 --drain。\n")
		if !confirm(env, "确定要结束这些会话和模型命令并让守护进程退出吗？") {
			env.errorf("已取消（非交互环境请加 --yes 确认）。\n")
			return 1
		}
	}

	mode := holder.ShutdownNow
	if hf.drain {
		mode = holder.ShutdownDrain
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	err = holder.SendShutdown(wctx, cli.Conn, mode)
	wcancel()
	_ = cli.Conn.Close()
	if err != nil {
		env.errorf("发送关停请求失败: %v\n", err)
		return 1
	}
	if hf.drain {
		env.printf("已请求排空：不再接受新会话和新命令，运行中的 %s结束后守护进程会自动退出。\n", describeHolderWork(running, st.Runs))
		env.printf("想看还剩什么，执行下面这条命令：%s\n", cmdLine(env.Prog+" holder status"))
	} else {
		env.printf("已请求守护进程退出（结束 %s）…\n", describeHolderWork(running, st.Runs))
	}

	// 等旧进程消失（套接字不再响应或进程号变了）。排空可能要很久，不等。
	if hf.drain {
		return 0
	}
	deadline := time.Now().Add(hf.wait)
	gone := false
	kicked := false
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return 1
		}
		next, err := dialAdmin(ctx, socket, env.Version)
		if err != nil {
			// 只有套接字确实没了（文件不存在 / 连接被拒）才算退出：关停过程中守护进程还在监听，
			// 只是拒绝新握手（ByeError）——这时它还在终止会话，新的守护进程此刻启动会因「已在运行」失败。
			if errors.Is(err, holder.ErrNotRunning) {
				gone = true
				if !expectRestart {
					break
				}
				// Windows 由计划任务拉起守护进程，兜底的看门狗触发器每 5 分钟才一次：这里直接启动任务，免得干等。
				// （systemd / launchd 会自己立刻拉起，不需要这一步。）
				if !kicked && runtime.GOOS == "windows" {
					kicked = true
					if task, kerr := kickHolderTask(env); kerr != nil {
						env.errorf("没能立刻启动守护进程的计划任务（%v）；计划任务的看门狗最迟 5 分钟内会把它拉起来。\n", kerr)
					} else {
						env.printf("已让计划任务 %s 立刻启动守护进程…\n", task)
					}
				}
			}
		} else {
			sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			nst, serr := holder.Status(sctx, next)
			cancel()
			_ = next.Conn.Close()
			if serr == nil && nst.Hello.PID != oldPID {
				env.printf("✅ 守护进程已重启（新进程号 %d，版本 %s）。\n", nst.Hello.PID, nst.Hello.Holder)
				return 0
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	switch {
	case !gone:
		env.errorf("守护进程在 %s 内没有退出，请执行下面这条命令检查：%s\n", hf.wait, cmdLine(env.Prog+" holder status"))
		return 1
	case expectRestart:
		manager := "系统服务的状态（用户级安装加 --user；macOS 看 launchd）：" + cmdLine("systemctl status relaymesh-holder")
		if runtime.GOOS == "windows" {
			manager = "任务计划程序里的 RelayMeshHolder 任务（兜底的看门狗触发器最迟 5 分钟内会把它拉起来）。"
		}
		env.printf("守护进程已退出，但在 %s 内没有被重新拉起。若它由服务管理器托管，请检查%s\n"+
			"未托管时请手动运行：%s\n也可以用 install 子命令把它安装为服务（参数与首次安装时相同）。\n", hf.wait, manager, cmdLine(env.Prog+" holder run"))
		return 1
	}
	env.printf("✅ 守护进程已退出。\n")
	return 0
}
