package cli

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

// Windows 的常驻运行：用「计划任务」托管 Agent，而不是实现一个 Windows 服务。
//
//   - 系统级（管理员执行）：以 SYSTEM 账户在开机时启动，运行在 0 号会话，没有桌面也没有窗口，
//     注销后继续运行——等同 Linux 上 root 身份的 systemd 服务，Web 终端里的 Shell 也是 SYSTEM 身份。
//   - 用户级（--user，无需管理员）：当前用户登录时启动，以该用户的身份与环境运行，注销后停止。
//
// 选择计划任务的原因：不需要写 SCM 协议代码；系统级 / 用户级共用同一套逻辑，用户级可以在无管理员权限下完整验证；
// 「看门狗」重复触发器（每 5 分钟一次、实例已存在则忽略）负责在进程被杀或崩溃后把它拉起来——
// 实测 RestartOnFailure 对被强杀的进程不生效，而重复触发器可靠。
//
// 计划任务启动的是一份「图形界面子系统」副本（见 pe.go），因此没有控制台窗口；日志写入文件（--log-file）。
// 本文件只含与平台无关的逻辑与流程，真实的系统调用由 winOps 注入，所以可以在任何平台上做单元测试。

// winOps 是 Windows 安装流程对系统的全部依赖。
type winOps struct {
	elevated    bool
	username    string // DOMAIN\user，用于登录触发器与任务主体
	progFiles   string // %ProgramFiles%
	localApp    string // %LocalAppData%
	programData string // %ProgramData%
	exe         string // 当前可执行文件
	tempDir     string
	// run 执行外部命令并返回合并输出（已转成 UTF-8）。
	run func(name string, args ...string) (string, error)
	// procs 返回正在运行、映像路径等于 image 的进程号。
	procs func(image string) []int
	// sleep 可在测试里替换。
	sleep func(time.Duration)
	// now 用作触发器的起始时间。
	now func() time.Time
	// deferCleanup 在当前进程退出后删除 paths 里的文件，并尝试删除（已空的）dirs。
	// 用于卸载时被自己占用的可执行文件：正在运行的 exe 只能改名不能删除，退出后再清掉才不会留垃圾。nil 表示不支持。
	deferCleanup func(paths, dirs []string) error
	// removeFile 删除或移开（被占用时）文件；nil 时用 removeOrMoveAside，测试里替换以模拟被占用。
	removeFile func(path string) (removed, moved bool)
}

func (w winOps) wait(d time.Duration) {
	if w.sleep != nil {
		w.sleep(d)
		return
	}
	time.Sleep(d)
}

func (w winOps) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

const (
	winSystemTaskName = "RelayMeshAgent"
	winHolderTaskName = "RelayMeshHolder"
	winWatchdogEvery  = "PT5M"
)

// winScope 描述安装到哪里。
type winScope struct {
	user           bool
	taskName       string
	holderTaskName string // 会话守护进程的任务（与 Agent 任务相互独立）
	binDir         string
	dataDir        string // 默认数据目录（身份、已信任服务器、日志）
}

var errWindowsNeedsAdmin = errors.New("安装系统级服务需要管理员权限：请「以管理员身份运行」PowerShell 后重试；\n" +
	"如果只想为当前用户安装（登录后自动启动，不需要管理员），请加 --user")

var errWindowsNeedsAdminHighest = errors.New("以「当前用户 + 最高权限」安装需要管理员权限来创建计划任务：请「以管理员身份运行」PowerShell 后重试；\n" +
	"不需要管理员权限的话，去掉 --highest（仍以当前用户身份运行，但没有管理员令牌）")

// errWindowsHighestDeleteHint 附在删除计划任务失败的错误后面：最高权限的任务只有管理员令牌才删得掉，
// 普通 PowerShell 里会得到「拒绝访问」，用户很容易误以为是别的问题。
const errWindowsHighestDeleteHint = "\n提示：以「最高权限」创建的计划任务需要在「以管理员身份运行」的 PowerShell 中才能删除，请以管理员身份重试。"

// scope 决定安装 / 卸载的范围。安装：--user，或者没有管理员权限时必须显式 --user；
// 卸载与状态：没有管理员权限时默认处理用户级。
func (w winOps) scope(user bool, forUninstall bool) (winScope, error) {
	if !user && !w.elevated {
		if !forUninstall {
			return winScope{}, errWindowsNeedsAdmin
		}
		user = true
	}
	return w.scopeOf(user)
}

// scopeOf 推导指定范围的任务名与目录，不检查权限。
func (w winOps) scopeOf(user bool) (winScope, error) {
	if user {
		if w.username == "" {
			return winScope{}, errors.New("无法确定当前 Windows 用户")
		}
		if w.localApp == "" {
			return winScope{}, errors.New("无法确定 %LocalAppData%")
		}
		return winScope{
			user:           true,
			taskName:       winSystemTaskName + "-" + sanitizeTaskPart(w.username),
			holderTaskName: winHolderTaskName + "-" + sanitizeTaskPart(w.username),
			binDir:         filepath.Join(w.localApp, "Programs", "RelayMesh Agent"),
			dataDir:        filepath.Join(w.localApp, "RelayMesh", "Agent"),
		}, nil
	}
	if w.progFiles == "" || w.programData == "" {
		return winScope{}, errors.New("无法确定 %ProgramFiles% / %ProgramData%")
	}
	return winScope{
		taskName:       winSystemTaskName,
		holderTaskName: winHolderTaskName,
		binDir:         filepath.Join(w.progFiles, "RelayMesh Agent"),
		dataDir:        filepath.Join(w.programData, "RelayMesh", "Agent"),
	}, nil
}

// sanitizeTaskPart 把 DOMAIN\user 之类的字符串变成可用于任务名的片段。
func sanitizeTaskPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ---------- 命令行参数与 XML ----------

// windowsQuoteArg 按 CommandLineToArgvW 的规则引用一个参数，使其能被原样解析回来。
func windowsQuoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			backslashes++
			b.WriteByte(c)
		case '"':
			// 引号前的反斜杠要加倍，再用一个反斜杠转义引号本身。
			b.WriteString(strings.Repeat(`\`, backslashes+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			backslashes = 0
			b.WriteByte(c)
		}
	}
	// 结尾的反斜杠紧挨着收尾引号，必须加倍，否则会把引号转义掉。
	b.WriteString(strings.Repeat(`\`, backslashes))
	b.WriteByte('"')
	return b.String()
}

func windowsJoinArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = windowsQuoteArg(a)
	}
	return strings.Join(q, " ")
}

// taskSpec 是渲染计划任务 XML 所需的全部信息。
type taskSpec struct {
	Description string
	System      bool   // true：SYSTEM 账户 + 开机触发；false：指定用户 + 登录触发
	User        string // DOMAIN\user（仅用户级）
	Highest     bool   // 仅用户级：以最高权限运行（管理员账户登录时带完整管理员令牌，且无 UAC 弹窗）；false 为最低权限
	OnDemand    bool   // true：没有任何触发器，只能被 /Run 手动启动（远程卸载用的一次性任务）
	Command     string // 要启动的可执行文件（绝对路径）
	Arguments   string // 已按 Windows 规则引用好的参数串
	Start       time.Time
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// renderTaskXML 生成任务计划程序 1.2 版的任务定义。
//
// 要点（每一条都是实测或文档确认过的）：
//   - ExecutionTimeLimit=PT0S：默认 72 小时后会被强行结束，常驻进程必须关掉；
//   - MultipleInstancesPolicy=IgnoreNew：重复触发时若进程还在，直接忽略，不会起第二个；
//   - Priority=5：默认的 7 是「低于正常」，会让 Shell 及其子进程整体变慢；
//   - 不设 RestartOnFailure：实测对被强杀的进程不触发，改由重复触发器兜底；
//   - 用户级默认用 InteractiveToken + LeastPrivilege：无需管理员，也无需保存密码；
//   - 用户级 + Highest 用 InteractiveToken + HighestAvailable：仍是该用户的身份与环境，登录时由任务计划程序直接给出
//     完整的管理员令牌（不弹 UAC，也不保存密码）；账户本身不是管理员时「最高可用」就是普通令牌，不会越权。
//     创建这种任务需要管理员令牌。
func renderTaskXML(s taskSpec) string {
	var b strings.Builder
	start := s.Start.Format("2006-01-02T15:04:05")
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-16\"?>\n")
	b.WriteString("<Task version=\"1.2\" xmlns=\"http://schemas.microsoft.com/windows/2004/02/mit/task\">\n")
	fmt.Fprintf(&b, "  <RegistrationInfo>\n    <Description>%s</Description>\n  </RegistrationInfo>\n", xmlText(s.Description))

	if !s.OnDemand { // 没有 <Triggers> 的任务只能被手动 /Run 启动
		b.WriteString("  <Triggers>\n")
		if s.System {
			b.WriteString("    <BootTrigger>\n      <Enabled>true</Enabled>\n    </BootTrigger>\n")
		} else {
			fmt.Fprintf(&b, "    <LogonTrigger>\n      <Enabled>true</Enabled>\n      <UserId>%s</UserId>\n    </LogonTrigger>\n", xmlText(s.User))
		}
		fmt.Fprintf(&b, "    <TimeTrigger>\n      <Repetition>\n        <Interval>%s</Interval>\n      </Repetition>\n      <StartBoundary>%s</StartBoundary>\n      <Enabled>true</Enabled>\n    </TimeTrigger>\n", winWatchdogEvery, start)
		b.WriteString("  </Triggers>\n")
	}

	b.WriteString("  <Principals>\n    <Principal id=\"Author\">\n")
	if s.System {
		b.WriteString("      <UserId>S-1-5-18</UserId>\n      <RunLevel>HighestAvailable</RunLevel>\n")
	} else {
		runLevel := "LeastPrivilege"
		if s.Highest {
			runLevel = "HighestAvailable"
		}
		fmt.Fprintf(&b, "      <UserId>%s</UserId>\n      <LogonType>InteractiveToken</LogonType>\n      <RunLevel>%s</RunLevel>\n", xmlText(s.User), runLevel)
	}
	b.WriteString("    </Principal>\n  </Principals>\n")

	b.WriteString("  <Settings>\n")
	b.WriteString("    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n")
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n")
	b.WriteString("    <AllowHardTerminate>true</AllowHardTerminate>\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\n")
	b.WriteString("    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>\n")
	b.WriteString("    <AllowStartOnDemand>true</AllowStartOnDemand>\n")
	b.WriteString("    <Enabled>true</Enabled>\n")
	b.WriteString("    <Hidden>false</Hidden>\n")
	b.WriteString("    <WakeToRun>false</WakeToRun>\n")
	b.WriteString("    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\n")
	b.WriteString("    <Priority>5</Priority>\n")
	b.WriteString("  </Settings>\n")

	fmt.Fprintf(&b, "  <Actions Context=\"Author\">\n    <Exec>\n      <Command>%s</Command>\n      <Arguments>%s</Arguments>\n    </Exec>\n  </Actions>\n", xmlText(s.Command), xmlText(s.Arguments))
	b.WriteString("</Task>\n")
	return b.String()
}

// encodeUTF16LEBOM 把文本编码为带 BOM 的 UTF-16LE——schtasks /XML 对这个编码最稳妥。
func encodeUTF16LEBOM(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+len(u)*2)
	out = append(out, 0xFF, 0xFE)
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// ---------- 安装 ----------

type winInstallResult struct {
	MachineCode string
	User        bool
	Highest     bool // 当前用户 + 最高权限（仅 User 为 true 时可能为 true）
	TaskName    string
	BinPath     string // 控制台版：status / uninstall 等命令行操作用
	TaskBinPath string // 无窗口版：计划任务实际启动的那个
	DataDir     string
	LogPath     string
	Started     bool // 启动任务后确实看到了进程
	Commands    []string

	// 会话守护进程（独立任务、独立映像）：升级 Agent 不会碰它，终端会话得以保留。
	WithHolder        bool
	HolderTaskName    string
	HolderTaskBinPath string
	HolderLogPath     string
	HolderStarted     bool
	Notes             []string
}

func (w winOps) schtasks(res *winInstallResult, args ...string) (string, error) {
	if res != nil {
		res.Commands = append(res.Commands, "schtasks "+strings.Join(args, " "))
	}
	return w.run("schtasks", args...)
}

// waitGone 等待映像对应的进程全部退出（替换文件前要先释放占用）。按次数而不是墙上时钟计时，测试里不会真的等。
func (w winOps) waitGone(image string, total time.Duration) bool {
	if w.procs == nil {
		return true
	}
	const step = 200 * time.Millisecond
	for i := 0; ; i++ {
		if len(w.procs(image)) == 0 {
			return true
		}
		if time.Duration(i)*step >= total {
			return false
		}
		w.wait(step)
	}
}

// doInstallWindows 安装计划任务并启动。所有步骤幂等，重复执行即「更新配置并重启」。
func doInstallWindows(env Env, c common, w winOps) (winInstallResult, error) {
	var res winInstallResult
	if c.temp {
		return res, errors.New("--temp 是临时身份，不能安装为常驻服务（常驻服务需要稳定的机器码）")
	}
	scope, err := w.scope(c.user, false)
	if err != nil {
		return res, err
	}
	// 「当前用户 + 最高权限」：创建 HighestAvailable 的计划任务本身需要管理员令牌；
	// 在产生任何副作用（身份、文件、任务）之前先拦下，别让用户装到一半才发现。
	highest := c.highest && scope.user
	if c.highest && !scope.user {
		return res, errors.New("--highest 需要与 --user 合用：系统级安装本来就以 SYSTEM 最高权限运行")
	}
	if highest && !w.elevated {
		return res, errWindowsNeedsAdminHighest
	}
	res.User, res.Highest, res.TaskName = scope.user, highest, scope.taskName

	// Windows 上一律显式写入数据目录：用户级任务以非提升令牌运行，而安装者可能是提升的管理员，
	// 两边各自推导的默认目录会不一致，导致任务用的身份不是安装时打印出来的那个。
	dataDir := c.dataDir
	if dataDir == "" {
		dataDir = scope.dataDir
	}
	res.DataDir = dataDir
	logPath := c.logFile
	if logPath == "" {
		logPath = filepath.Join(dataDir, "agent.log")
	}
	res.LogPath = logPath

	id, err := agent.LoadOrCreateIdentity(dataDir)
	if err != nil {
		return res, fmt.Errorf("无法加载身份: %w", err)
	}
	res.MachineCode = id.MachineCode()

	name := installedName(env)
	res.BinPath = filepath.Join(scope.binDir, name+".exe")
	res.TaskBinPath = filepath.Join(scope.binDir, name+"w.exe")
	withHolder := c.wantsHolder()
	res.WithHolder = withHolder
	if withHolder {
		res.HolderTaskName = scope.holderTaskName
		res.HolderTaskBinPath = filepath.Join(scope.binDir, name+"-holderw.exe")
		res.HolderLogPath = siblingLogFile(logPath, "holder")
	}

	// 先停掉旧的 Agent 实例，否则正在运行的可执行文件无法被替换。守护进程不在此列：它有自己的映像文件。
	_, _ = w.schtasks(&res, "/End", "/TN", scope.taskName)
	if !w.waitGone(res.TaskBinPath, 8*time.Second) {
		return res, fmt.Errorf("旧的 Agent 进程没有退出，无法替换 %s；请在任务管理器里结束它后重试", res.TaskBinPath)
	}
	cleanStaleBinaries(scope.binDir)
	if err := installBinary(w.exe, res.BinPath); err != nil {
		return res, fmt.Errorf("复制可执行文件到 %s 失败: %w", res.BinPath, err)
	}
	if err := copyAsGUI(res.BinPath, res.TaskBinPath); err != nil {
		return res, fmt.Errorf("生成无窗口版本失败: %w", err)
	}

	cc := c
	cc.dataDir = dataDir
	cc.logFile = logPath
	desc := "RelayMesh 终端 Agent：让 RelayMesh 的 Web 终端连接这台主机（"
	holderDesc := "RelayMesh 会话守护进程：持有终端会话，让 Agent 升级 / 重启不打断正在运行的命令（"
	if scope.user {
		desc += "当前用户"
		holderDesc += "当前用户"
		if highest {
			desc += "，最高权限"
			holderDesc += "，最高权限"
		}
	} else {
		desc += "系统"
		holderDesc += "系统"
	}
	desc += "）"
	holderDesc += "）"

	holderRunning := false
	if withHolder {
		holderRunning = w.procs != nil && len(w.procs(res.HolderTaskBinPath)) > 0
		if holderRunning {
			// 正在运行的守护进程持有着它的映像文件（也不该被打断）：保留当前版本，新版本留待 holder restart。
			iv := w.invocation()
			res.Notes = append(res.Notes, "会话守护进程正在运行，安装保留了它当前的可执行文件与启动参数（替换它会结束其中的会话）。"+
				"想换上新版本 / 新配置时，在合适的时机"+iv.restartAction(env, res.BinPath, ""))
		} else if err := copyAsGUI(res.BinPath, res.HolderTaskBinPath); err != nil {
			return res, fmt.Errorf("生成守护进程的无窗口版本失败: %w", err)
		}
		holderArgv := append(append([]string(nil), env.ServiceArgs...), cc.holderServiceArgs()...)
		if err := w.createTask(&res, scope.holderTaskName, taskSpec{
			Description: holderDesc,
			System:      !scope.user,
			User:        w.username,
			Highest:     highest,
			Command:     res.HolderTaskBinPath,
			Arguments:   windowsJoinArgs(holderArgv),
			Start:       w.clock(),
		}); err != nil {
			return res, err
		}
		if !holderRunning {
			if out, err := w.schtasks(&res, "/Run", "/TN", scope.holderTaskName); err != nil {
				return res, fmt.Errorf("守护进程计划任务已创建，但立即启动失败: %v\n%s", err, strings.TrimSpace(out))
			}
			res.HolderStarted = w.waitStarted(res.HolderTaskBinPath)
		}
	}

	argv := append(append([]string(nil), env.ServiceArgs...), cc.agentServiceArgs(withHolder)...)
	if err := w.createTask(&res, scope.taskName, taskSpec{
		Description: desc,
		System:      !scope.user,
		User:        w.username,
		Highest:     highest,
		Command:     res.TaskBinPath,
		Arguments:   windowsJoinArgs(argv),
		Start:       w.clock(),
	}); err != nil {
		return res, err
	}
	if out, err := w.schtasks(&res, "/Run", "/TN", scope.taskName); err != nil {
		return res, fmt.Errorf("计划任务已创建，但立即启动失败: %v\n%s", err, strings.TrimSpace(out))
	}
	// 给进程几秒钟出现；拿不到也不算安装失败（之后登录 / 开机 / 看门狗触发都会拉起它）。
	res.Started = w.waitStarted(res.TaskBinPath)
	return res, nil
}

// waitStarted 等映像对应的进程出现；procs 不可用时视为已启动。
func (w winOps) waitStarted(image string) bool {
	for i := 0; i < 20; i++ {
		if w.procs == nil || len(w.procs(image)) > 0 {
			return true
		}
		w.wait(250 * time.Millisecond)
	}
	return false
}

// createTask 渲染任务定义并用 schtasks /Create /F 注册（覆盖同名任务，不影响已在运行的实例）。
func (w winOps) createTask(res *winInstallResult, taskName string, spec taskSpec) error {
	if err := os.MkdirAll(w.tempDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(w.tempDir, "relaymesh-agent-task-*.xml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(encodeUTF16LEBOM(renderTaskXML(spec))); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if out, err := w.schtasks(res, "/Create", "/TN", taskName, "/XML", tmpName, "/F"); err != nil {
		hint := ""
		if !w.elevated && !spec.System {
			// 同名任务此前若是以「最高权限」创建的，普通令牌覆盖不了（拒绝访问）
			hint = "\n提示：如果这个任务此前是以「最高权限」安装的，修改它需要在「以管理员身份运行」的 PowerShell 中执行。"
		}
		return fmt.Errorf("创建计划任务失败（%s）: %v\n%s%s", taskName, err, strings.TrimSpace(out), hint)
	}
	return nil
}

// cleanStaleBinaries 清理上次卸载 / 升级时因文件被占用而移开的旧文件。
func cleanStaleBinaries(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".old") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// ---------- 卸载与状态 ----------

// removeOrMoveAside 删除文件；正在运行的可执行文件删不掉但可以改名，改名后下次安装 / 卸载时再清理。
func removeOrMoveAside(path string) (removed bool, moved bool) {
	if _, err := os.Stat(path); err != nil {
		return false, false
	}
	if err := os.Remove(path); err == nil {
		return true, false
	}
	if err := os.Rename(path, path+".old"); err == nil {
		return true, true
	}
	return false, false
}

func doUninstallWindows(env Env, c common, purge bool, cfg resolved, w winOps) (uninstallResult, error) {
	var res uninstallResult
	scope, err := w.scope(c.user, true)
	if err != nil {
		return res, err
	}
	name := installedName(env)
	conBin := filepath.Join(scope.binDir, name+".exe")
	guiBin := filepath.Join(scope.binDir, name+"w.exe")
	holderBin := filepath.Join(scope.binDir, name+"-holderw.exe")

	// 先 Agent 后守护进程：先让 Agent 断开，再结束持有会话的守护进程。
	for _, t := range []struct{ task, image string }{
		{scope.taskName, guiBin},
		{scope.holderTaskName, holderBin},
	} {
		if _, err := w.run("schtasks", "/Query", "/TN", t.task); err != nil {
			continue
		}
		res.Found = true
		res.Commands = append(res.Commands, "schtasks /End /TN "+t.task, "schtasks /Delete /TN "+t.task+" /F")
		_, _ = w.run("schtasks", "/End", "/TN", t.task)
		w.waitGone(t.image, 8*time.Second)
		if out, err := w.run("schtasks", "/Delete", "/TN", t.task, "/F"); err != nil {
			hint := ""
			if !w.elevated && scope.user {
				// 用户级任务可能是以「最高权限」创建的：普通令牌删不掉，会得到「拒绝访问」
				hint = errWindowsHighestDeleteHint
			}
			return res, fmt.Errorf("删除计划任务失败: %v\n%s%s", err, strings.TrimSpace(out), hint)
		}
		res.Removed = append(res.Removed, "计划任务 "+t.task)
	}
	// 远程卸载（Web 上删除主机时勾选了「同时卸载设备端」）是在专用的一次性任务里运行本命令的：
	// 它得在这里删掉自己的任务定义。正在运行的实例不受影响，会继续完成后面的文件清理。
	oneShot := scope.taskName + winUninstallTaskSuffix
	if _, err := w.run("schtasks", "/Query", "/TN", oneShot); err == nil {
		_, _ = w.run("schtasks", "/Delete", "/TN", oneShot, "/F")
	}
	// 守护进程不是由计划任务直接管理的情形（例如任务已被手动删除，但进程还在）：仍要结束，否则映像文件删不掉。
	if w.procs != nil && len(w.procs(holderBin)) > 0 {
		_, _ = w.run("schtasks", "/End", "/TN", scope.holderTaskName)
		w.waitGone(holderBin, 8*time.Second)
	}

	remove := w.removeFile
	if remove == nil {
		remove = removeOrMoveAside
	}
	var movedAside []string
	for _, p := range []string{guiBin, holderBin, conBin} {
		if removed, moved := remove(p); removed {
			res.Found = true
			if moved {
				movedAside = append(movedAside, p+".old")
			} else {
				res.Removed = append(res.Removed, p)
			}
		}
	}
	cleanStaleBinaries(scope.binDir)
	_ = os.Remove(scope.binDir) // 只有目录已空才会成功

	if purge {
		dataDir := c.dataDir
		if dataDir == "" {
			dataDir = scope.dataDir
		}
		if err := agent.RemoveIdentity(dataDir); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, agent.IdentityPath(dataDir))
		res.Removed = append(res.Removed, holder.PurgeState(dataDir)...)
		for _, p := range []string{
			filepath.Join(cfg.cfgDir, "known_servers"),
			filepath.Join(dataDir, "known_servers"),
			filepath.Join(dataDir, "agent.log"),
			filepath.Join(dataDir, "agent.log.1"),
			filepath.Join(dataDir, "agent-holder.log"),
			filepath.Join(dataDir, "agent-holder.log.1"),
		} {
			if err := os.Remove(p); err == nil {
				res.Removed = append(res.Removed, p)
			}
		}
		_ = os.Remove(dataDir)
		_ = os.Remove(filepath.Dir(dataDir)) // RelayMesh 上级目录，空了才会成功
	}

	// 被占用的可执行文件（典型情形：直接运行已安装目录里的 exe 来卸载自己）只能改名；
	// 能安排退出后清理就不留垃圾，否则留待下次安装 / 卸载时清理。
	if len(movedAside) > 0 {
		scheduled := w.deferCleanup != nil && w.deferCleanup(movedAside, []string{scope.binDir}) == nil
		for _, p := range movedAside {
			if scheduled {
				res.Removed = append(res.Removed, strings.TrimSuffix(p, ".old")+"（正被占用，将在本命令退出后自动清理）")
			} else {
				res.Removed = append(res.Removed, strings.TrimSuffix(p, ".old")+"（正被占用，已改名为 .old，下次安装或卸载时清理）")
			}
		}
		res.Deferred = scheduled
	}
	return res, nil
}

// reportWindows 打印两种范围各自的安装与运行状态。
func reportWindows(env Env, w winOps) {
	found := false
	for _, user := range []bool{false, true} {
		scope, err := w.scopeOf(user)
		if err != nil {
			continue
		}
		label := "系统级任务"
		if scope.user {
			label = "用户级任务"
		}
		name := installedName(env)
		state := func(image string) string {
			if w.procs == nil {
				return "未知"
			}
			if pids := w.procs(image); len(pids) > 0 {
				return fmt.Sprintf("运行中（PID %s）", joinInts(pids))
			}
			return "未运行"
		}
		if _, err := w.run("schtasks", "/Query", "/TN", scope.taskName); err == nil {
			found = true
			if scope.user && w.taskIsHighest(scope.taskName) {
				label = "用户级任务（最高权限）"
			}
			env.printf("  %s : 已安装（计划任务 %s），%s\n", label, scope.taskName, state(filepath.Join(scope.binDir, name+"w.exe")))
			env.printf("           日志 %s\n", filepath.Join(scope.dataDir, "agent.log"))
		}
		if _, err := w.run("schtasks", "/Query", "/TN", scope.holderTaskName); err == nil {
			found = true
			env.printf("  会话守护进程 : 已安装（计划任务 %s），%s\n", scope.holderTaskName, state(filepath.Join(scope.binDir, name+"-holderw.exe")))
			env.printf("           日志 %s\n", filepath.Join(scope.dataDir, "agent-holder.log"))
		}
	}
	if !found {
		env.printf("  常驻任务 : 未安装（可用 %s install --server … 安装）\n", env.Prog)
	}
}

// taskIsHighest 读出任务定义，判断它是否以「最高权限」运行。读不到就当作否——这只影响状态输出里的一个标注。
func (w winOps) taskIsHighest(taskName string) bool {
	out, err := w.run("schtasks", "/Query", "/TN", taskName, "/XML")
	return err == nil && strings.Contains(out, "<RunLevel>HighestAvailable</RunLevel>")
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, ", ")
}
