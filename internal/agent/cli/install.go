package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// sysOps 把「安装服务」依赖的系统环境收拢到一处：生产用真实值，测试注入假值，
// 这样安装逻辑（路径、单元内容、命令序列、幂等性）无需真的改动宿主机就能被验证。
type sysOps struct {
	goos string
	euid int
	home string
	// root 是所有绝对路径的前缀；生产为空，测试指向临时目录。
	root string
	exe  string
	// userName 是执行安装的用户名（Linux 用户级安装开启 linger 时要用）。
	userName string
	// run 执行外部命令并返回合并输出。
	run func(name string, args ...string) (string, error)
	// sleep 在需要等一等服务起来时使用；为 nil 时真的睡眠，测试里替换成空函数。
	sleep func(time.Duration)
}

func (s sysOps) wait(d time.Duration) {
	if s.sleep != nil {
		s.sleep(d)
		return
	}
	time.Sleep(d)
}

// currentUserName 取当前用户名：优先系统账户信息，取不到时退回环境变量（容器里常常没有 passwd 条目）。
func currentUserName() string {
	if u, err := osuser.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, k := range []string{"USER", "LOGNAME"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func realSys() (sysOps, error) {
	exe, err := os.Executable()
	if err != nil {
		return sysOps{}, fmt.Errorf("无法确定当前可执行文件: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, _ := os.UserHomeDir()
	return sysOps{
		goos:     runtime.GOOS,
		euid:     os.Geteuid(),
		home:     home,
		exe:      exe,
		userName: currentUserName(),
		run: func(name string, args ...string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
	}, nil
}

func (s sysOps) p(path string) string {
	if s.root == "" {
		return path
	}
	return filepath.Join(s.root, path)
}

// serviceScope 描述服务装在哪里。
type serviceScope struct {
	user     bool
	binDir   string
	unitPath string
	logPath  string // launchd 的日志文件

	// 会话守护进程的单元（与 Agent 单元是两个独立的服务）。
	holderUnitPath string
	holderLogPath  string
}

var (
	errNeedRoot = errors.New("安装为系统服务需要管理员权限：请用 sudo 运行（已是 root 则直接运行），或加 --user 仅为当前用户安装（Linux 上会自动开启 linger，让服务在退出登录后继续运行）")
	errNoSystem = errors.New("当前系统暂不支持自动安装常驻服务")
)

// systemScope 是系统级服务的固定位置（与当前用户是谁无关）；不支持的系统返回 false。
// 安装用它决定装到哪里，升级用它判断「这台机器上是不是装成了系统级」。
func (s sysOps) systemScope() (serviceScope, bool) {
	switch s.goos {
	case "linux":
		return serviceScope{
			binDir:         "/usr/local/bin",
			unitPath:       "/etc/systemd/system/" + systemdUnitName,
			holderUnitPath: "/etc/systemd/system/" + systemdHolderUnitName,
		}, true
	case "darwin":
		return serviceScope{
			binDir:         "/usr/local/bin",
			unitPath:       "/Library/LaunchDaemons/" + launchdLabel + ".plist",
			logPath:        "/var/log/relaymesh-agent.log",
			holderUnitPath: "/Library/LaunchDaemons/" + launchdHolderLabel + ".plist",
			holderLogPath:  "/var/log/relaymesh-holder.log",
		}, true
	}
	return serviceScope{}, false
}

// scopeFor 决定服务的安装位置。
func (s sysOps) scopeFor(wantUser bool) (serviceScope, error) {
	switch s.goos {
	case "linux":
		if s.euid == 0 && !wantUser {
			sc, _ := s.systemScope()
			return sc, nil
		}
		if !wantUser {
			return serviceScope{}, errNeedRoot
		}
		if s.home == "" {
			return serviceScope{}, errors.New("无法确定当前用户的主目录")
		}
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" || s.root != "" {
			cfg = filepath.Join(s.home, ".config")
		}
		return serviceScope{
			user:           true,
			binDir:         filepath.Join(s.home, ".local", "bin"),
			unitPath:       filepath.Join(cfg, "systemd", "user", systemdUnitName),
			holderUnitPath: filepath.Join(cfg, "systemd", "user", systemdHolderUnitName),
		}, nil
	case "darwin":
		if s.euid == 0 && !wantUser {
			sc, _ := s.systemScope()
			return sc, nil
		}
		if s.home == "" {
			return serviceScope{}, errors.New("无法确定当前用户的主目录")
		}
		return serviceScope{
			user:           true,
			binDir:         filepath.Join(s.home, ".local", "bin"),
			unitPath:       filepath.Join(s.home, "Library", "LaunchAgents", launchdLabel+".plist"),
			logPath:        filepath.Join(s.home, "Library", "Logs", "relaymesh-agent.log"),
			holderUnitPath: filepath.Join(s.home, "Library", "LaunchAgents", launchdHolderLabel+".plist"),
			holderLogPath:  filepath.Join(s.home, "Library", "Logs", "relaymesh-holder.log"),
		}, nil
	}
	return serviceScope{}, errNoSystem
}

// installedName 是服务使用的可执行文件名：独立二进制叫 relaymesh-agent，主程序子命令则是 relaymesh。
func installedName(env Env) string {
	if len(env.ServiceArgs) > 0 {
		return "relaymesh"
	}
	return "relaymesh-agent"
}

type installResult struct {
	MachineCode string
	BinPath     string
	UnitPath    string
	User        bool
	Commands    []string

	// WithHolder 表示装了独立的会话守护进程服务（会话能挺过 Agent 的升级 / 重启 / 崩溃）。
	WithHolder     bool
	HolderUnitPath string
	// Notes 是需要告诉管理员的补充说明（例如守护进程的配置有变化，但没有自动重启以免结束会话）。
	Notes []string

	// Linger 是 Linux 用户级安装对 linger 的处理结果（见 linger.go）；LingerUser 是对应的用户，LingerDetail 是失败原因。
	Linger       lingerState
	LingerUser   string
	LingerDetail string
}

// svcCmd 是一条服务管理命令。tag 用来在失败回滚时判断「哪些步骤已经生效」。
type svcCmd struct {
	argv      []string
	tag       string
	ignoreErr bool
}

// unitBackup 记录一份服务配置文件安装前的状态，任何一步失败都据此回滚，不留下半成品。
type unitBackup struct {
	path  string // 未加 root 前缀的绝对路径
	prev  []byte
	had   bool
	wrote bool
}

func newUnitBackup(s sysOps, path string) unitBackup {
	prev, err := os.ReadFile(s.p(path))
	return unitBackup{path: path, prev: prev, had: err == nil}
}

func (b *unitBackup) write(s sysOps, content string) error {
	if err := writeFileAtomic(s.p(b.path), []byte(content), 0o644); err != nil {
		return err
	}
	b.wrote = true
	return nil
}

// restore 撤销本次对该文件的改动：安装前存在则写回，否则删除。
func (b unitBackup) restore(s sysOps) error {
	switch {
	case b.had && b.wrote:
		return writeFileAtomic(s.p(b.path), b.prev, 0o644)
	case !b.had && b.wrote:
		_ = os.Remove(s.p(b.path))
	}
	return nil
}

func (r *installResult) addNote(msg string) { r.Notes = append(r.Notes, msg) }

// doInstall 执行安装：复制二进制、写服务单元、启用并（重）启动。所有步骤都是幂等的，重复执行即「更新配置并重启」。
//
// 默认装两个相互独立的服务：会话守护进程（持有终端会话）与 Agent（连接 Hub）。重装 / 升级只重启 Agent，
// 绝不会自动重启守护进程——那会结束正在运行的会话；守护进程配置有变化时只提示，由管理员选择时机。
// `--holder embedded` 则只装一个服务，会话与 Agent 同进程、随它结束。
func doInstall(env Env, c common, cfg resolved, s sysOps) (installResult, error) {
	var res installResult
	if c.temp {
		return res, errors.New("--temp 是临时身份，不能安装为常驻服务（常驻服务需要稳定的机器码）")
	}
	scope, err := s.scopeFor(c.user)
	if err != nil {
		return res, err
	}
	res.User, res.UnitPath = scope.user, scope.unitPath
	withHolder := cfg.holderMode != agent.HolderEmbedded
	res.WithHolder = withHolder
	if withHolder {
		res.HolderUnitPath = scope.holderUnitPath
	}

	if s.goos == "linux" && !scope.user {
		if _, err := os.Stat(s.p("/run/systemd/system")); err != nil {
			return res, errors.New("未检测到正在运行的 systemd。容器 / 未启用 systemd 的 WSL 里请改用前台运行（参数与安装时相同）：" +
				cmdLine(env.Prog+" run …") + "\n或自行用 supervisor / nohup 托管")
		}
	}

	agentB := newUnitBackup(s, scope.unitPath)
	holderB := newUnitBackup(s, scope.holderUnitPath)
	if !withHolder && holderB.had {
		return res, errors.New("这台机器上已经安装了会话守护进程服务；改成内嵌模式会让它里面的会话失去托管。" +
			"请先卸载（会结束守护进程里的会话和模型正在运行的命令）：" + cmdLine(env.Prog+" uninstall") + "\n然后再重新安装；或者保持默认的守护进程模式")
	}

	// 先确定身份：让机器码在服务启动之前就已固定，安装结束时可以直接告诉管理员。
	id, err := agent.LoadOrCreateIdentity(cfg.dataDir)
	if err != nil {
		return res, fmt.Errorf("无法加载身份: %w", err)
	}
	res.MachineCode = id.MachineCode()

	res.BinPath = filepath.Join(scope.binDir, installedName(env))
	if err := installBinary(s.exe, s.p(res.BinPath)); err != nil {
		return res, fmt.Errorf("复制可执行文件到 %s 失败: %w", res.BinPath, err)
	}

	agentArgv := append([]string{res.BinPath}, env.ServiceArgs...)
	agentArgv = append(agentArgv, c.agentServiceArgs(withHolder)...)
	holderArgv := append([]string{res.BinPath}, env.ServiceArgs...)
	holderArgv = append(holderArgv, c.holderServiceArgs()...)

	var agentContent, holderContent string
	switch s.goos {
	case "linux":
		if withHolder {
			agentContent, err = renderSystemdAgentUnit(agentArgv, scope.user)
			if err == nil {
				holderContent, err = renderSystemdHolderUnit(holderArgv, scope.user)
			}
		} else {
			agentContent, err = renderSystemdUnit(agentArgv, scope.user)
		}
	case "darwin":
		agentContent, err = renderLaunchdPlist(agentArgv, scope.logPath)
		if err == nil && withHolder {
			holderContent, err = renderLaunchdHolderPlist(holderArgv, scope.holderLogPath)
		}
	}
	if err != nil {
		return res, err
	}
	for _, dir := range []string{filepath.Dir(scope.unitPath), filepath.Dir(scope.logPath), filepath.Dir(scope.holderLogPath)} {
		if dir == "." || dir == "" {
			continue
		}
		if err := os.MkdirAll(s.p(dir), 0o755); err != nil {
			return res, err
		}
	}
	if withHolder {
		if err := holderB.write(s, holderContent); err != nil {
			return res, fmt.Errorf("写入 %s 失败: %w", scope.holderUnitPath, err)
		}
	}
	if err := agentB.write(s, agentContent); err != nil {
		_ = holderB.restore(s)
		return res, fmt.Errorf("写入 %s 失败: %w", scope.unitPath, err)
	}
	if withHolder && holderB.had && string(holderB.prev) != holderContent {
		iv := s.invocation()
		res.addNote("会话守护进程的服务配置有变化，但没有自动重启它（那会结束其中的全部会话和模型正在运行的命令）。" +
			"已写入的新配置会在它下次启动时使用；想让新配置立刻生效时，在合适的时机" + iv.restartAction(env, res.BinPath, ""))
	}

	var cmds []svcCmd
	switch s.goos {
	case "linux":
		base := []string{"systemctl"}
		if scope.user {
			base = append(base, "--user")
		}
		sc := func(tag string, args ...string) svcCmd {
			return svcCmd{argv: append(append([]string(nil), base...), args...), tag: tag}
		}
		cmds = append(cmds, sc("reload", "daemon-reload"))
		if withHolder {
			cmds = append(cmds, sc("holder-enable", "enable", systemdHolderUnitName))
			// start 对已在运行的守护进程是空操作——升级不会动它，会话因此得以保留。
			cmds = append(cmds, sc("holder-start", "start", systemdHolderUnitName))
		}
		cmds = append(cmds, sc("agent-enable", "enable", systemdUnitName), sc("agent-restart", "restart", systemdUnitName))
	case "darwin":
		domain, target := launchdDomainTarget(s, scope, launchdLabel)
		if withHolder {
			_, holderTarget := launchdDomainTarget(s, scope, launchdHolderLabel)
			// 已经载入的守护进程保持不动：bootout / kickstart -k 都会结束它的会话。
			if _, err := s.run("launchctl", "print", holderTarget); err != nil {
				cmds = append(cmds,
					svcCmd{argv: []string{"launchctl", "bootstrap", domain, scope.holderUnitPath}, tag: "holder-bootstrap"},
					svcCmd{argv: []string{"launchctl", "enable", holderTarget}, tag: "holder-enable"},
					svcCmd{argv: []string{"launchctl", "kickstart", holderTarget}, tag: "holder-start"},
				)
			}
		}
		cmds = append(cmds,
			svcCmd{argv: []string{"launchctl", "bootout", target}, tag: "agent-bootout", ignoreErr: true}, // 未加载时会报错，属正常
			svcCmd{argv: []string{"launchctl", "bootstrap", domain, scope.unitPath}, tag: "agent-bootstrap"},
			svcCmd{argv: []string{"launchctl", "enable", target}, tag: "agent-enable"},
			svcCmd{argv: []string{"launchctl", "kickstart", "-k", target}, tag: "agent-restart"},
		)
	}
	var done []string
	for _, cmd := range cmds {
		res.Commands = append(res.Commands, strings.Join(cmd.argv, " "))
		out, err := s.run(cmd.argv[0], cmd.argv[1:]...)
		if err == nil {
			done = append(done, cmd.tag)
			continue
		}
		if cmd.ignoreErr {
			continue
		}
		note := rollbackInstall(s, scope, agentB, holderB, done)
		msg := fmt.Sprintf("下面这条命令执行失败：%s\n失败原因: %v\n%s", cmdLine(strings.Join(cmd.argv, " ")), err, strings.TrimSpace(out))
		if hint := serviceFailureHint(env.Prog, s.goos, scope.user, out); hint != "" {
			msg += "\n\n" + hint
		}
		return res, errors.New(msg + "\n\n" + note)
	}
	// 服务已经装好并在运行：最后才处理 linger，这样安装失败回滚时不会留下对系统设置的改动。
	res.Linger, res.LingerUser, res.LingerDetail = ensureLinger(c, cfg, s, scope)
	return res, nil
}

// launchdDomainTarget 返回 launchctl 的域与服务目标（系统级 system，用户级 gui/<uid>）。
func launchdDomainTarget(s sysOps, scope serviceScope, label string) (domain, target string) {
	domain = "system"
	if scope.user {
		domain = fmt.Sprintf("gui/%d", s.euid)
	}
	return domain, domain + "/" + label
}

// rollbackInstall 在服务管理命令失败后撤销本次对单元文件的改动，并尽力让服务管理器回到安装前的状态，
// 避免「安装失败却留下一份指向不可用配置的单元」。done 是已经成功执行的步骤标记。返回一句给管理员看的说明。
// 已复制到位的可执行文件不回滚：它无害，而且重试安装时可以直接复用。
func rollbackInstall(s sysOps, scope serviceScope, agentB, holderB unitBackup, done []string) string {
	has := func(tag string) bool {
		for _, d := range done {
			if d == tag {
				return true
			}
		}
		return false
	}
	note := "本次安装未生效，已撤销写入的服务配置（可执行文件已保留，可直接重试）。"
	if agentB.had {
		note = "本次安装未生效，已恢复安装前的服务配置（服务本身可能需要手动重启）。"
	}
	if err := agentB.restore(s); err != nil {
		return fmt.Sprintf("本次安装未生效，但无法恢复原有的服务配置 %s：%v", scope.unitPath, err)
	}
	if err := holderB.restore(s); err != nil {
		return fmt.Sprintf("本次安装未生效，但无法恢复原有的守护进程服务配置 %s：%v", scope.holderUnitPath, err)
	}
	switch s.goos {
	case "linux":
		if len(done) > 0 { // 第一条命令就失败说明服务管理器根本没联上，没有需要撤销的载入
			base := []string{}
			if scope.user {
				base = append(base, "--user")
			}
			// 本次新装的守护进程（此前没有）已经被启动：它里面不可能有用户的会话，停用并清理。
			if has("holder-start") && !holderB.had {
				_, _ = s.run("systemctl", append(append([]string(nil), base...), "disable", "--now", systemdHolderUnitName)...)
			}
			_, _ = s.run("systemctl", append(append([]string(nil), base...), "daemon-reload")...)
		}
	case "darwin":
		domain, target := launchdDomainTarget(s, scope, launchdLabel)
		_, holderTarget := launchdDomainTarget(s, scope, launchdHolderLabel)
		if has("holder-bootstrap") && !holderB.had {
			_, _ = s.run("launchctl", "bootout", holderTarget)
		}
		if has("agent-bootstrap") { // bootstrap 已成功：先卸掉刚载入的新任务
			_, _ = s.run("launchctl", "bootout", target)
		}
		if agentB.had { // 旧任务在 bootout 时已被卸载，重新载入以恢复原有服务
			_, _ = s.run("launchctl", "bootstrap", domain, scope.unitPath)
		}
	}
	return note
}

// describeSystemdState 把 `systemctl is-active` 的输出整理成人话。
// 总线连不上时输出的是一行英文报错，直接当「状态」展示会误导成服务本身出了问题。
func describeSystemdState(out string) string {
	out = strings.TrimSpace(out)
	switch {
	case out == "":
		return "未知"
	case strings.Contains(out, "Failed to connect to bus"):
		return "无法确定（当前会话连不上用户级 systemd，与服务本身无关）"
	}
	gloss := map[string]string{"active": "运行中", "inactive": "未运行", "failed": "已失败", "activating": "启动中", "deactivating": "停止中"}
	if g, ok := gloss[out]; ok {
		return out + "（" + g + "）"
	}
	return out
}

// serviceFailureHint 针对最常见、最难自行诊断的失败给出可操作的提示。
func serviceFailureHint(prog, goos string, user bool, out string) string {
	if goos == "linux" && user && (strings.Contains(out, "Failed to connect to bus") || strings.Contains(out, "XDG_RUNTIME_DIR")) {
		return "提示：当前会话没有连上用户级 systemd（常见于 sudo / su 切换了用户、没有走 PAM 的 ssh 登录，或 WSL 里 WSLg 遮住了 /run/user）。可以这样处理：\n" +
			"  · 以目标用户正常登录（桌面或带 PAM 的 ssh）后再安装，并确认下面这条命令有输出：\n" +
			"        echo $XDG_RUNTIME_DIR\n" +
			"  · 需要开机即运行、无需登录时，先执行：\n" +
			"        loginctl enable-linger $USER\n" +
			"  · 改用系统级安装：去掉 --user，并用 sudo 执行同样的 " + prog + " install 命令；\n" +
			"  · 或先前台运行（参数与安装时相同，可配合 tmux / nohup 托管）：\n" +
			"        " + prog + " run …"
	}
	return ""
}

// installBinary 原子地把 src 复制到 dst（先写同目录临时文件再重命名，升级时不会留下半截可执行文件）。
func installBinary(src, dst string) error {
	if a, errA := os.Stat(src); errA == nil {
		if b, errB := os.Stat(dst); errB == nil && os.SameFile(a, b) {
			return nil // 已经在目标位置
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		cleanup()
		return err
	}
	return nil
}

// writeFileAtomic 先写同目录临时文件再原子替换。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ---------- uninstall ----------

type uninstallResult struct {
	Removed  []string
	Commands []string
	Found    bool
	Deferred bool // Windows：有文件被占用，已安排在本进程退出后清理
	// LingerReleased 表示关闭了安装时由安装器开启的 linger；LingerNote 是没能关闭时要告诉使用者的话。
	LingerReleased bool
	LingerNote     string
	// lingerPending 是等到最后一步才关闭 linger 的用户名（空表示不用动）。
	lingerPending string
}

// doUninstall 停止并移除服务（Agent 与会话守护进程）。默认保留身份（重装后机器码不变）；purge 才删除。
// 先停 Agent 再停守护进程：守护进程停止时会话随之结束，那是卸载的预期结果。
// 安装时由安装器开启的 linger 在所有清理结束之后才还回去（原因见 disableLinger）；本来就开着的不动。
func doUninstall(env Env, c common, purge bool, cfg resolved, s sysOps) (uninstallResult, error) {
	res, err := doUninstallCore(env, c, purge, cfg, s)
	if res.lingerPending != "" {
		cmd, note := disableLinger(s, res.lingerPending)
		res.Commands = append(res.Commands, cmd)
		res.LingerReleased = note == ""
		res.LingerNote = note
		res.lingerPending = ""
	}
	return res, err
}

func doUninstallCore(env Env, c common, purge bool, cfg resolved, s sysOps) (uninstallResult, error) {
	var res uninstallResult
	scope, err := s.scopeFor(c.user)
	if err != nil {
		return res, err
	}
	exists := func(path string) bool {
		if path == "" {
			return false
		}
		_, err := os.Stat(s.p(path))
		return err == nil
	}
	agentFound, holderFound := exists(scope.unitPath), exists(scope.holderUnitPath)
	res.Found = agentFound || holderFound

	var cmds [][]string
	switch s.goos {
	case "linux":
		base := []string{"systemctl"}
		if scope.user {
			base = append(base, "--user")
		}
		if agentFound {
			cmds = append(cmds, append(append([]string(nil), base...), "disable", "--now", systemdUnitName))
		}
		if holderFound {
			cmds = append(cmds, append(append([]string(nil), base...), "disable", "--now", systemdHolderUnitName))
		}
	case "darwin":
		domain := "system"
		if scope.user {
			domain = fmt.Sprintf("gui/%d", s.euid)
		}
		if agentFound {
			cmds = append(cmds, []string{"launchctl", "bootout", domain + "/" + launchdLabel})
		}
		if holderFound {
			cmds = append(cmds, []string{"launchctl", "bootout", domain + "/" + launchdHolderLabel})
		}
	}
	for _, argv := range cmds {
		res.Commands = append(res.Commands, strings.Join(argv, " "))
		// 服务可能本来就没在运行：停止失败不应阻止清理文件。
		_, _ = s.run(argv[0], argv[1:]...)
	}

	for _, found := range []struct {
		ok   bool
		path string
	}{{agentFound, scope.unitPath}, {holderFound, scope.holderUnitPath}} {
		if !found.ok {
			continue
		}
		if err := os.Remove(s.p(found.path)); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, found.path)
	}
	if res.Found && s.goos == "linux" {
		base := []string{"systemctl"}
		if scope.user {
			base = append(base, "--user")
		}
		argv := append(base, "daemon-reload")
		res.Commands = append(res.Commands, strings.Join(argv, " "))
		_, _ = s.run(argv[0], argv[1:]...)
	}

	bin := filepath.Join(scope.binDir, installedName(env))
	if err := os.Remove(s.p(bin)); err == nil {
		res.Removed = append(res.Removed, bin)
		res.Found = true
	}

	// 取走安装时留下的 linger 记号（要在清理数据目录之前，否则目录里的记号会让它清不空）；关闭 linger 本身排在最后，见 doUninstall。
	res.lingerPending = takeLingerMarker(cfg, s, scope)

	if purge {
		if err := agent.RemoveIdentity(cfg.dataDir); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, agent.IdentityPath(cfg.dataDir))
		for _, p := range holder.PurgeState(cfg.dataDir) {
			res.Removed = append(res.Removed, p)
		}
		known := filepath.Join(cfg.cfgDir, "known_servers")
		if err := os.Remove(known); err == nil {
			res.Removed = append(res.Removed, known)
		}
		// 清空后连目录一并移除，卸载不留空壳。os.Remove 只删空目录：目录里若还有别的文件
		// （比如用户把 --data-dir 指到了自己的目录）会原样保留，符号链接只删链接本身。
		for _, dir := range []string{cfg.dataDir, cfg.cfgDir} {
			if dir != "" && os.Remove(dir) == nil {
				res.Removed = append(res.Removed, dir)
			}
		}
	}
	return res, nil
}

// ---------- 命令入口 ----------

func cmdInstall(ctx context.Context, args []string, env Env) int {
	var c common
	var autoMode, autoWindow string
	fs := newFlagSet("install", env)
	c.bind(fs, env)
	fs.StringVar(&autoMode, "auto-upgrade", "", "装好后同时开启自动升级：notify（只提示新版本）或 silent（在允许的时间段内自己下载、验签、升级）；仅 Linux 的 systemd 常驻服务，之后可用 auto-upgrade 命令修改或收回")
	fs.StringVar(&autoWindow, "auto-window", "", "配合 --auto-upgrade silent：允许开始升级的时间段 HH:MM-HH:MM（主机本地时间，可跨午夜；默认不限）")
	if err := parse(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(env)
			return 0
		}
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	if err := c.settle(fs); err != nil {
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	cfg, err := c.resolve(env, false)
	if err != nil {
		env.errorf("配置错误: %v\n", err)
		return 2
	}
	s, err := realSys()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	if err := c.checkHighest(s.goos); err != nil {
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	autoOpts, err := installAutoUpgradeOpts(autoMode, autoWindow, s.goos)
	if err != nil {
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	if s.goos == "windows" {
		return installWindows(ctx, env, c, cfg)
	}
	res, err := doInstall(env, c, cfg, s)
	if err != nil {
		env.errorf("安装失败: %v\n", err)
		return 1
	}
	env.printf("✅ 已安装并启动 RelayMesh Agent 服务\n")
	env.printf("  机器码   : %s\n", res.MachineCode)
	env.printf("  可执行文件: %s\n", res.BinPath)
	env.printf("  服务配置 : %s\n", res.UnitPath)
	if res.WithHolder {
		env.printf("  会话守护进程: %s\n", res.HolderUnitPath)
		env.printf("    （独立服务：升级 / 重启 Agent 不会打断正在运行的终端会话）\n")
	} else {
		env.printf("  会话     : 内嵌模式——会话与 Agent 同进程，Agent 重启会结束全部会话（界面会标注「重启会中断」）\n")
	}
	for _, n := range res.Notes {
		env.printf("\n⚠ %s\n", n)
	}
	if res.WithHolder {
		reportHolderVersion(ctx, env, s.invocation(), res.BinPath, cfg.holderSocket, "安装")
	}
	autoFailed := false
	if autoOpts.Mode != "" {
		autoFailed = !enableAutoUpgradeAfterInstall(env, s, autoOpts, cfg.dataDir, c.user)
	}
	env.printf("\n下一步：在 RelayMesh Web 的「接入主机」里核对上面的机器码，确认后点击「启用」。这台主机之前已经启用过的话（升级、重装都沿用同一个机器码），不需要再启用。\n")
	switch s.goos {
	case "linux":
		if res.User {
			env.printf("查看日志: journalctl --user -u %s -f", systemdUnitName)
			if res.WithHolder {
				env.printf("（守护进程: -u %s）", systemdHolderUnitName)
			}
			env.printf("\n")
			reportLinger(env, res)
		} else {
			env.printf("查看日志: journalctl -u %s -f", systemdUnitName)
			if res.WithHolder {
				env.printf("（守护进程: -u %s）", systemdHolderUnitName)
			}
			env.printf("\n")
		}
	case "darwin":
		scope, _ := s.scopeFor(c.user)
		env.printf("查看日志: tail -f %s", scope.logPath)
		if res.WithHolder {
			env.printf("（守护进程: %s）", scope.holderLogPath)
		}
		env.printf("\n")
	}
	if autoFailed {
		return 1 // 服务已经装好并在运行；只是请求的自动升级没能开启——用非零退出码让脚本与使用者都注意到
	}
	return 0
}

// enableAutoUpgradeAfterInstall 在服务装好之后开启自动升级（同意文件写在服务使用的数据目录里）。
// 失败不回滚已装好的服务：打印原因与可以直接执行的补救命令，成功返回 true。
func enableAutoUpgradeAfterInstall(env Env, s sysOps, opts autoUpgradeOpts, dataDir string, wantUser bool) bool {
	res, err := doAutoUpgradeWith(s, "enable", opts, dataDir, wantUser)
	if err != nil {
		retry := env.Prog + " auto-upgrade enable --mode " + opts.Mode
		if opts.Window != "" {
			retry += " --window " + opts.Window
		}
		if wantUser {
			retry += " --user"
		}
		if dataDir != agent.DefaultDataDir() {
			retry += " --data-dir " + dataDir
		}
		env.printf("\n⚠ 服务已经装好，但没能开启自动升级：%v\n", err)
		env.printf("  装好之后可以随时重试：%s\n", cmdLine(retry))
		return false
	}
	env.printf("\n")
	printAutoUpgradeEnabled(env, res)
	return true
}

// reportLinger 把 Linux 用户级安装对 linger 的处理结果告诉使用者：成功时一句话说明效果，没成功时给出可以直接执行的命令。
func reportLinger(env Env, res installResult) {
	switch res.Linger {
	case lingerEnabled:
		env.printf("已为用户 %s 开启 linger：退出登录后服务继续运行，开机后无需登录也会启动（卸载时会一并关闭）。\n", res.LingerUser)
	case lingerAlready:
		env.printf("linger 已处于开启状态：退出登录后服务继续运行，开机后无需登录也会启动。\n")
	case lingerSkipped:
		env.printf("提示: 按 --no-linger 没有开启 linger，用户级服务会在你退出登录后停止；需要常驻时执行：%s\n", cmdLine(lingerManualCmd(res.LingerUser)))
	case lingerFailed:
		why := res.LingerDetail
		if why == "" {
			why = "原因未知"
		}
		env.printf("\n⚠ 没能自动开启 linger（%s）：用户级服务会在你退出登录后停止。\n", why)
		env.printf("  请手动执行下面这条命令（提示需要权限时在前面加 sudo）：%s\n  也可以改用系统级安装。\n", cmdLine(lingerManualCmd(res.LingerUser)))
	}
}

// reportHolderVersion 在升级安装后提示：运行中的守护进程仍是旧版本（为保住会话，不会自动重启它）。
// action 是「安装」或「升级」，只用于措辞。
func reportHolderVersion(ctx context.Context, env Env, iv invocation, installed, socket, action string) {
	cli, err := dialAdmin(ctx, socket, env.Version)
	if err != nil {
		return
	}
	defer cli.Conn.Close()
	if v := cli.Hello.Holder; v != "" && v != env.Version {
		// 指纹一致：守护进程与本次安装 / 升级的程序跑的是同一份 Agent 代码，版本号不同只是构建号不同，重启它换不来任何变化。
		// 此前这里一律提示「想换上新版本时执行 holder restart」，让人白白担心要不要结束会话。
		if same, known := termproto.CompareAgentRev(cli.Hello.Rev, env.Rev); known && same {
			env.printf("\n运行中的会话守护进程是 %s 构建（本次%s的是 %s），两者的 Agent 代码完全一致，不需要重启守护进程。\n", v, action, env.Version)
			return
		}
		env.printf("\n提示：运行中的会话守护进程是 %s 版本（本次%s的是 %s）。为保住正在运行的会话，%s没有重启它；\n", v, action, env.Version, action)
		env.printf("%s\n", iv.restartHint(env, installed, socket))
	}
}

func cmdUninstall(ctx context.Context, args []string, env Env) int {
	var c common
	var purge, yes bool
	fs := newFlagSet("uninstall", env)
	c.bind(fs, env)
	fs.BoolVar(&purge, "purge", false, "同时删除身份文件与已信任服务器记录（机器码将永久丢失）")
	fs.BoolVar(&yes, "yes", false, "会话守护进程里还有运行中的会话时不再询问，直接卸载（会结束这些会话）")
	if err := parse(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(env)
			return 0
		}
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	// uninstall 不需要 --server，所以只取目录。
	cfg := resolved{dataDir: c.dataDir, cfgDir: c.configDir}
	if cfg.dataDir == "" {
		cfg.dataDir = agent.DefaultDataDir()
	}
	if cfg.cfgDir == "" {
		cfg.cfgDir = agent.DefaultConfigDir()
	}
	s, err := realSys()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	if !confirmEndSessions(ctx, env, c, cfg, yes) {
		env.errorf("已取消卸载。\n")
		return 1
	}
	if s.goos == "windows" {
		return uninstallWindows(ctx, env, c, purge, cfg)
	}
	res, err := doUninstall(env, c, purge, cfg, s)
	if err != nil {
		env.errorf("卸载失败: %v\n", err)
		return 1
	}
	if !res.Found && !purge && !res.LingerReleased && res.LingerNote == "" {
		env.printf("没有发现已安装的 RelayMesh Agent 服务。\n")
		return 0
	}
	for _, p := range res.Removed {
		env.printf("已删除: %s\n", p)
	}
	if res.LingerReleased {
		env.printf("已关闭安装时开启的 linger。\n")
	}
	if res.LingerNote != "" {
		env.printf("⚠ %s\n", res.LingerNote)
	}
	if !purge {
		env.printf("已保留身份（机器码不变）；如需一并删除请加 --purge。\n")
	}
	return 0
}

// confirmEndSessions 卸载会停止会话守护进程，其中运行中的会话会被结束：先告知，交互式终端里要求确认。
// 非交互环境（脚本 / 服务管理器）只告知不阻塞——明确执行 uninstall 就是要停掉它们。
func confirmEndSessions(ctx context.Context, env Env, c common, cfg resolved, yes bool) bool {
	socket := c.holderSocket
	if socket == "" {
		socket = holder.SocketPath(cfg.dataDir)
	}
	cli, err := dialAdmin(ctx, socket, env.Version)
	if err != nil {
		return true
	}
	defer cli.Conn.Close()
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st, err := holder.Status(sctx, cli)
	if err != nil {
		return true
	}
	running := 0
	for _, sess := range st.Sessions {
		if sess.State == "running" {
			running++
		}
	}
	if running == 0 {
		return true
	}
	env.printf("会话守护进程里有 %d 个运行中的终端会话；卸载会停止守护进程，这些会话里的命令会被终止。\n", running)
	if yes || !isInteractive(env.Stdin) {
		return true
	}
	return confirm(env, "确定要卸载并结束这些会话吗？")
}

func cmdStatus(ctx context.Context, args []string, env Env) int {
	var c common
	fs := newFlagSet("status", env)
	c.bind(fs, env)
	if err := parse(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(env)
			return 0
		}
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	dataDir := c.dataDir
	if dataDir == "" {
		dataDir = agent.DefaultDataDir()
	}
	env.printf("RelayMesh Agent %s\n", env.Version)
	code, path, err := peekMachineCode(dataDir)
	switch {
	case err == nil:
		env.printf("  机器码 : %s\n  身份   : %s\n", code, path)
	case errors.Is(err, agent.ErrNoIdentity):
		env.printf("  机器码 : （尚未生成；首次运行或执行 %s id 时生成）\n", env.Prog)
	default:
		env.errorf("  身份   : 读取失败: %v\n", err)
	}
	s, err := realSys()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	if s.goos == "windows" {
		return statusWindows(ctx, env)
	}
	reportService(env, s)
	return 0
}

// reportService 报告两种范围（系统 / 用户）下服务的安装与运行状态。
func reportService(env Env, s sysOps) {
	found := false
	for _, user := range []bool{false, true} {
		scope, err := s.scopeFor(user)
		if err != nil {
			continue
		}
		if _, err := os.Stat(s.p(scope.unitPath)); err != nil {
			continue
		}
		found = true
		label := "系统服务"
		if scope.user {
			label = "用户服务"
		}
		state := "未知"
		switch s.goos {
		case "linux":
			args := []string{"is-active", systemdUnitName}
			if scope.user {
				args = append([]string{"--user"}, args...)
			}
			out, _ := s.run("systemctl", args...)
			state = describeSystemdState(out)
		case "darwin":
			domain := "system"
			if scope.user {
				domain = fmt.Sprintf("gui/%d", s.euid)
			}
			if _, err := s.run("launchctl", "print", domain+"/"+launchdLabel); err == nil {
				state = "已加载"
			} else {
				state = "未加载"
			}
		}
		env.printf("  %s : 已安装（%s），状态 %s\n", label, scope.unitPath, state)
		if _, err := os.Stat(s.p(scope.holderUnitPath)); err == nil {
			hstate := "未知"
			switch s.goos {
			case "linux":
				args := []string{"is-active", systemdHolderUnitName}
				if scope.user {
					args = append([]string{"--user"}, args...)
				}
				out, _ := s.run("systemctl", args...)
				hstate = describeSystemdState(out)
			case "darwin":
				domain := "system"
				if scope.user {
					domain = fmt.Sprintf("gui/%d", s.euid)
				}
				if _, err := s.run("launchctl", "print", domain+"/"+launchdHolderLabel); err == nil {
					hstate = "已加载"
				} else {
					hstate = "未加载"
				}
			}
			env.printf("  会话守护进程 : 已安装（%s），状态 %s\n", scope.holderUnitPath, hstate)
		}
	}
	if !found {
		env.printf("  常驻服务 : 未安装（可用 %s install --server … 安装）\n", env.Prog)
	}
}
