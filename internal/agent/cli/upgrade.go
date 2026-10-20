package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// upgrade：把已经装好的常驻服务换成「当前正在运行的这个（新）可执行文件」，并只重启 Agent 服务。
//
// 与 install 的区别：
//   - install 会按命令行参数重写服务配置；upgrade 完全不碰服务配置（服务端地址、数据目录、各种开关都保持原样），
//     所以它不需要、也不接受那些参数——对「已经不记得当初装的参数」的主机尤其稳妥。
//   - 会话守护进程不会被重启（那会结束其中正在运行的会话）；它在下次重启时才换上新版本。
//   - 身份（机器码）与启用状态都不变，不需要在 Web 上重新启用。
//
// 典型用法是 Web「主机」里点「升级」得到的命令：脚本从服务端下载新版本并运行它的 `upgrade`。
//
// 另有一种由 Agent 自己发起的用法（Web 一键升级，见 selfupgrade.go）：已安装的旧程序用
// `upgrade --from <下载好的新程序> --auto-rollback --expect-version <版本> …` 替换自己；重启后等新版本真的连上服务端，
// 等不到就把旧程序还原回来再重启——这样一次失败的升级不会让一台远程主机永远失联。

var (
	errUpgradeNeedRoot  = errors.New("这台机器上的服务是系统级安装的，升级需要管理员权限：请用 sudo 运行（已是 root 则直接运行）")
	errNothingToUpgrade = errors.New("没有检测到已安装的 RelayMesh Agent 常驻服务，无法升级。首次接入请使用 Web「接入主机」里的安装命令（install）")
)

const (
	// defaultOnlineTimeout 是等新版本连上服务端的默认上限。新进程通常几秒内就连上；服务启动失败时 systemd 每 5 秒重试一次，
	// 90 秒足够它连续失败多次，也不会让一次失败的升级拖太久。
	defaultOnlineTimeout = 90 * time.Second
	// rollbackOnlineTimeout 是还原之后等旧版本重新上线的上限。
	rollbackOnlineTimeout = 60 * time.Second
	// onlinePollInterval 是等上线时查看标记的间隔。
	onlinePollInterval = time.Second
	// prevSuffix 是升级时留下的旧程序副本的后缀：自动回滚靠它，管理员需要时也可以手动换回。
	prevSuffix = ".prev"
)

type upgradeResult struct {
	BinPath  string
	UnitPath string
	User     bool
	Commands []string
	// State 是重启后服务状态的整理结果（只有 Linux 取得到）。
	State string
	// HolderUnit 表示这台机器装了独立的会话守护进程服务。
	HolderUnit bool
	Notes      []string
	// PrevPath 是留下的旧程序副本（只有 --auto-rollback 才留）。
	PrevPath string
	// Confirmed 表示新版本已确认连上服务端（只有 --auto-rollback 才会等）。
	Confirmed bool
}

// upgradeOptions 是 upgrade 的可选行为。零值就是原来的「手动升级」：用当前运行的程序、重启后不等上线、不回滚。
type upgradeOptions struct {
	// wantUser 只升级当前用户级安装的服务。
	wantUser bool
	// from 是新程序的路径；空表示用当前正在运行的这个。
	from string
	// autoRollback 为 true 时：替换前先留一份旧程序；重启后等新版本上线（数据目录里的上线标记），
	// 超时就还原旧程序并重启。
	autoRollback bool
	// expect 是期望的新版本号，autoRollback 靠它识别「新版本上线了」。
	expect string
	// dataDir 是 Agent 的数据目录（上线标记与结果文件在这里）；autoRollback 时必填。
	dataDir string
	// timeout 是等新版本上线的上限。
	timeout time.Duration
	// removeSource 为 true 时，升级结束后删除 from——它只是下载的暂存文件。只允许用于数据目录的 upgrade/ 下的文件。
	removeSource bool
}

// validate 检查选项之间的搭配；goos 是要升级的系统。
func (o *upgradeOptions) validate(goos string) error {
	if o.expect != "" && !o.autoRollback {
		return errors.New("--expect-version 只与 --auto-rollback 合用")
	}
	if o.removeSource && o.from == "" {
		return errors.New("--remove-source 需要同时给出 --from")
	}
	if o.from != "" {
		abs, err := filepath.Abs(o.from)
		if err != nil {
			return fmt.Errorf("--from 的路径无效: %w", err)
		}
		o.from = abs
	}
	if o.removeSource {
		// 只允许删 Agent 自己下载的暂存文件，免得手滑指向别的文件时把它删掉。
		if o.dataDir == "" {
			o.dataDir = agent.DefaultDataDir()
		}
		stage, err := filepath.Abs(agent.UpgradeDir(o.dataDir))
		if err != nil || filepath.Dir(o.from) != stage {
			return fmt.Errorf("--remove-source 只能删除数据目录的暂存目录 %s 里的文件", agent.UpgradeDir(o.dataDir))
		}
	}
	if !o.autoRollback {
		return nil
	}
	if goos != "linux" {
		return errors.New("--auto-rollback 目前只支持 Linux（systemd）")
	}
	if !termproto.ValidAgentVersion(o.expect) {
		return errors.New("--auto-rollback 需要用 --expect-version 给出新版本号（它是判断新版本是否真的上线的依据）")
	}
	if o.dataDir == "" {
		o.dataDir = agent.DefaultDataDir()
	}
	if o.timeout <= 0 {
		o.timeout = defaultOnlineTimeout
	}
	return nil
}

// installedScope 找出已经装了服务的范围。
// wantUser 为 true 时只看当前用户级；否则 root 先看系统级再看用户级，普通用户看用户级——
// 只发现了系统级安装时明确提示需要管理员权限，而不是笼统地说「没装」。
func (s sysOps) installedScope(wantUser bool) (serviceScope, error) {
	if _, ok := s.systemScope(); !ok {
		return serviceScope{}, errNoSystem
	}
	installed := func(sc serviceScope) bool {
		_, err := os.Stat(s.p(sc.unitPath))
		return err == nil
	}
	system, _ := s.systemScope()
	systemInstalled := installed(system)

	if !wantUser && s.euid == 0 && systemInstalled {
		return system, nil
	}
	if user, err := s.scopeFor(true); err == nil && installed(user) {
		return user, nil
	}
	if !wantUser && systemInstalled {
		return serviceScope{}, errUpgradeNeedRoot
	}
	return serviceScope{}, errNothingToUpgrade
}

// systemctlArgs 拼出 systemctl 的命令行（用户级服务带 --user）。
func systemctlArgs(scope serviceScope, args ...string) []string {
	argv := []string{"systemctl"}
	if scope.user {
		argv = append(argv, "--user")
	}
	return append(argv, args...)
}

// waitOnline 等 Agent 在数据目录里留下满足 accept 的上线标记，最多等 timeout；等到返回 true。
func (s sysOps) waitOnline(dataDir string, timeout time.Duration, accept func(agent.OnlineMarker) bool) bool {
	attempts := int(timeout / onlinePollInterval)
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		if m, ok := agent.ReadOnlineMarker(dataDir); ok && accept(m) {
			return true
		}
		s.wait(onlinePollInterval)
	}
	m, ok := agent.ReadOnlineMarker(dataDir)
	return ok && accept(m)
}

// doUpgrade 是手动升级：用当前运行的程序替换已安装的可执行文件并重启 Agent 服务（Linux systemd / macOS launchd）。
func doUpgrade(env Env, s sysOps, wantUser bool) (upgradeResult, error) {
	return doUpgradeWith(env, s, upgradeOptions{wantUser: wantUser})
}

// doUpgradeWith 替换已安装的可执行文件并重启 Agent 服务；选项见 upgradeOptions。
func doUpgradeWith(env Env, s sysOps, o upgradeOptions) (upgradeResult, error) {
	var res upgradeResult
	if err := o.validate(s.goos); err != nil {
		return res, err
	}
	if o.removeSource {
		// 暂存的新程序用完即删，不论成败。validate 已把它限定在数据目录的暂存目录里；这里再确认一次它不是已安装的程序，
		// 免得任何一个疏漏把正在用的程序删掉。
		defer func() {
			if a, errA := os.Stat(o.from); errA == nil && res.BinPath != "" {
				if b, errB := os.Stat(s.p(res.BinPath)); errB == nil && os.SameFile(a, b) {
					return
				}
			}
			_ = os.Remove(o.from)
		}()
	}
	scope, err := s.installedScope(o.wantUser)
	if err != nil {
		return res, err
	}
	res.User, res.UnitPath = scope.user, scope.unitPath
	res.BinPath = filepath.Join(scope.binDir, installedName(env))

	// 先确认服务配置确实指向我们要替换的文件：否则可能换掉了一个没人用的文件，服务却还跑着旧版本。
	unit, err := os.ReadFile(s.p(scope.unitPath))
	if err != nil {
		return res, fmt.Errorf("读取服务配置 %s 失败: %w", scope.unitPath, err)
	}
	if !strings.Contains(string(unit), res.BinPath) {
		return res, fmt.Errorf("服务配置 %s 里引用的程序路径不是 %s，无法确定该替换哪个文件，所以没有做任何改动。"+
			"请重新安装（参数与首次安装时相同）：%s", scope.unitPath, res.BinPath, cmdLine(env.Prog+" install …"))
	}

	// 新程序从哪来：默认是当前正在运行的这个；--from 指定一个下载好的文件。
	src := s.exe
	if o.from != "" {
		src = o.from
		if _, err := os.Stat(src); err != nil {
			return res, fmt.Errorf("找不到要换上的新程序 %s: %w", src, err)
		}
	}
	if a, errA := os.Stat(src); errA == nil {
		if b, errB := os.Stat(s.p(res.BinPath)); errB == nil && os.SameFile(a, b) {
			if o.from != "" {
				return res, errors.New("--from 指向的就是已安装的程序本身，没有新版本可以换上")
			}
			return res, errors.New("当前运行的就是已安装的程序本身，没有新版本可以换上：请先获取新版本的 " + installedName(env) +
				"（例如用 Web「主机」里「升级」给出的命令），再用新版本执行 upgrade")
		}
	}

	// 旧版本号：自动升级由已安装的旧程序执行，所以 env.Version 就是升级前的版本。
	fromVersion := ""
	if o.from != "" {
		fromVersion = env.Version
	}
	note := func(outcome, detail string) {
		if !o.autoRollback {
			return
		}
		_ = agent.WriteUpgradeNote(o.dataDir, termproto.UpgradeNote{
			Outcome: outcome, From: fromVersion, To: o.expect, At: time.Now().Unix(), Detail: detail,
		})
	}

	if o.autoRollback {
		// 先留一份旧程序：新版本起不来时靠它还原。留不下来就不要开始——此时什么都还没改。
		res.PrevPath = res.BinPath + prevSuffix
		if err := installBinary(s.p(res.BinPath), s.p(res.PrevPath)); err != nil {
			note(termproto.UpgradeOutcomeFailed, "无法保存旧版本，升级已放弃: "+err.Error())
			return res, fmt.Errorf("无法把旧程序保存为 %s，升级已放弃（没有改动任何东西）: %w", res.PrevPath, err)
		}
		// 清掉旧标记：升级之后才出现的标记才算数。
		agent.RemoveOnlineMarker(o.dataDir)
	}

	if err := installBinary(src, s.p(res.BinPath)); err != nil {
		note(termproto.UpgradeOutcomeFailed, "替换程序失败，旧版本没有被改动: "+err.Error())
		return res, fmt.Errorf("替换 %s 失败: %w", res.BinPath, err)
	}
	if _, err := os.Stat(s.p(scope.holderUnitPath)); err == nil {
		res.HolderUnit = true
	}

	// 只重启 Agent。守护进程不动：它里面有正在运行的会话。
	var cmds []svcCmd
	switch s.goos {
	case "linux":
		cmds = append(cmds, svcCmd{argv: systemctlArgs(scope, "restart", systemdUnitName), tag: "agent-restart"})
	case "darwin":
		domain, target := launchdDomainTarget(s, scope, launchdLabel)
		cmds = append(cmds,
			svcCmd{argv: []string{"launchctl", "bootout", target}, tag: "agent-bootout", ignoreErr: true}, // 未加载时会报错，属正常
			svcCmd{argv: []string{"launchctl", "bootstrap", domain, scope.unitPath}, tag: "agent-bootstrap"},
			svcCmd{argv: []string{"launchctl", "enable", target}, tag: "agent-enable"},
			svcCmd{argv: []string{"launchctl", "kickstart", "-k", target}, tag: "agent-restart"},
		)
	}
	restartFailure := ""
	for _, cmd := range cmds {
		res.Commands = append(res.Commands, strings.Join(cmd.argv, " "))
		out, err := s.run(cmd.argv[0], cmd.argv[1:]...)
		if err == nil || cmd.ignoreErr {
			continue
		}
		msg := fmt.Sprintf("可执行文件已经换成新版本，但下面这条命令执行失败：%s\n失败原因: %v\n%s", cmdLine(strings.Join(cmd.argv, " ")), err, strings.TrimSpace(out))
		if hint := serviceFailureHint(env.Prog, s.goos, scope.user, out); hint != "" {
			msg += "\n\n" + hint
		}
		if !o.autoRollback {
			return res, errors.New(msg + "\n\n服务还在运行旧版本（或已停止）；排除问题后可手动重启服务，或重新执行升级。")
		}
		restartFailure = msg
		break
	}

	if o.autoRollback {
		return o.settle(env, s, scope, res, fromVersion, restartFailure, note)
	}

	if s.goos == "linux" {
		// 给新进程一点时间起来，再看一眼状态：能立刻发现「换上去就崩」的情况，而不是报喜不报忧。
		s.wait(2 * time.Second)
		argv := systemctlArgs(scope, "is-active", systemdUnitName)
		out, _ := s.run(argv[0], argv[1:]...)
		res.State = describeSystemdState(out)
		if strings.TrimSpace(out) != "active" {
			journal := "journalctl -u " + systemdUnitName + " -n 50"
			if scope.user {
				journal = "journalctl --user -u " + systemdUnitName + " -n 50"
			}
			res.Notes = append(res.Notes, "重启后服务的状态是 "+res.State+"，不是运行中：请查看日志："+cmdLine(journal))
		}
	}
	return res, nil
}

// settle 是自动回滚模式下重启之后的收尾：等新版本真的连上服务端；等不到（或根本没能重启）就还原旧程序。
func (o upgradeOptions) settle(env Env, s sysOps, scope serviceScope, res upgradeResult, fromVersion, restartFailure string, note func(outcome, detail string)) (upgradeResult, error) {
	if restartFailure == "" {
		onNew := func(m agent.OnlineMarker) bool { return m.Version == o.expect }
		if s.waitOnline(o.dataDir, o.timeout, onNew) {
			res.Confirmed = true
			res.State = "新版本已连上服务端"
			note(termproto.UpgradeOutcomeOK, "")
			return res, nil
		}
	}

	reason := fmt.Sprintf("新版本 %s 没有在 %d 秒内连上服务端", o.expect, int(o.timeout/time.Second))
	if restartFailure != "" {
		reason = "替换后无法重启服务"
	}
	// 还原旧程序。先把结果写好再重启：旧版本上线后的第一次握手就会把「已回滚及原因」带给 Hub。
	if err := installBinary(s.p(res.PrevPath), s.p(res.BinPath)); err != nil {
		note(termproto.UpgradeOutcomeFailed, reason+"，而且还原旧版本失败: "+err.Error())
		return res, fmt.Errorf("%s；还原旧版本也失败了（%v）。旧程序保存在 %s，请手动换回并重启服务", reason, err, res.PrevPath)
	}
	rolledBack := fmt.Sprintf("%s，已还原为旧版本 %s", reason, orUnknown(fromVersion))
	note(termproto.UpgradeOutcomeRolledBack, rolledBack)
	agent.RemoveOnlineMarker(o.dataDir)

	// 新版本若在 systemd 里反复崩溃，可能已经触发启动频率限制；先清掉失败状态，否则重启会被拒绝。
	reset := systemctlArgs(scope, "reset-failed", systemdUnitName)
	res.Commands = append(res.Commands, strings.Join(reset, " "))
	_, _ = s.run(reset[0], reset[1:]...)
	restart := systemctlArgs(scope, "restart", systemdUnitName)
	res.Commands = append(res.Commands, strings.Join(restart, " "))
	if out, err := s.run(restart[0], restart[1:]...); err != nil {
		note(termproto.UpgradeOutcomeFailed, reason+"，已还原旧版本，但重启服务失败: "+err.Error())
		return res, fmt.Errorf("%s；旧版本已还原，但重启服务失败: %v\n%s", reason, err, strings.TrimSpace(out))
	}

	onOld := func(m agent.OnlineMarker) bool {
		if fromVersion != "" {
			return m.Version == fromVersion
		}
		return m.Version != o.expect
	}
	if !s.waitOnline(o.dataDir, rollbackOnlineTimeout, onOld) {
		note(termproto.UpgradeOutcomeFailed, reason+"；已还原旧版本，但它也没有连上服务端")
		return res, fmt.Errorf("%s；已还原旧版本，但它也没有在 %d 秒内连上服务端：请在主机上查看日志：%s",
			reason, int(rollbackOnlineTimeout/time.Second), cmdLine("journalctl -u "+systemdUnitName+" -n 50"))
	}
	return res, fmt.Errorf("%s，已还原为旧版本并确认它重新连上了服务端", reason)
}

func orUnknown(v string) string {
	if v == "" {
		return "（版本未知）"
	}
	return v
}

func cmdUpgrade(ctx context.Context, args []string, env Env) int {
	var o upgradeOptions
	fs := newFlagSet("upgrade", env)
	fs.BoolVar(&o.wantUser, "user", false, "升级当前用户级安装的服务（默认：root 先找系统级，再找当前用户级）")
	fs.StringVar(&o.from, "from", "", "用这个文件作为新版本，而不是当前正在运行的程序（Web 一键升级使用；通常不需要手动给）")
	fs.BoolVar(&o.autoRollback, "auto-rollback", false, "替换后等新版本真的连上服务端，等不到就还原旧程序并重启（需要 --expect-version；仅 Linux）")
	fs.StringVar(&o.expect, "expect-version", "", "新版本的版本号，与 --auto-rollback 合用")
	fs.StringVar(&o.dataDir, "data-dir", env.Getenv(envDataDir), "Agent 的数据目录（与 --auto-rollback / --remove-source 合用；默认按用户 / 系统区分）")
	fs.DurationVar(&o.timeout, "online-timeout", defaultOnlineTimeout, "与 --auto-rollback 合用：等新版本连上服务端的时间上限")
	fs.BoolVar(&o.removeSource, "remove-source", false, "升级结束后删除 --from 指定的暂存文件（只能用于数据目录的暂存目录）")
	if err := parse(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(env)
			return 0
		}
		env.errorf("参数错误: %v\n（upgrade 通常只认 --user；服务端地址、数据目录等配置保持安装时的样子，不需要再给）\n", err)
		return 2
	}
	if err := o.validate(runtime.GOOS); err != nil {
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	s, err := realSys()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	if s.goos == "windows" {
		if o.from != "" || o.autoRollback {
			env.errorf("参数错误: Windows 暂不支持 --from / --auto-rollback\n")
			return 2
		}
		return upgradeWindows(ctx, env, o.wantUser)
	}
	res, err := doUpgradeWith(env, s, o)
	if err != nil {
		env.errorf("升级失败: %v\n", err)
		return 1
	}
	if res.Confirmed {
		env.printf("✅ 已升级 RelayMesh Agent 到 %s，新版本已连上服务端\n", o.expect)
	} else {
		env.printf("✅ 已升级 RelayMesh Agent 到 %s 并重启服务\n", env.Version)
	}
	env.printf("  可执行文件: %s\n", res.BinPath)
	env.printf("  服务配置 : %s（未改动）\n", res.UnitPath)
	if res.PrevPath != "" {
		env.printf("  旧版本   : %s（需要时可手动换回并重启服务）\n", res.PrevPath)
	}
	if res.State != "" {
		env.printf("  服务状态 : %s\n", res.State)
	}
	env.printf("机器码与启用状态不变，不需要在 Web 上重新启用；几秒后主机列表里的版本会更新。\n")
	for _, n := range res.Notes {
		env.printf("\n⚠ %s\n", n)
	}
	if res.HolderUnit && o.from == "" {
		dataDir := o.dataDir
		if dataDir == "" {
			dataDir = agent.DefaultDataDir()
		}
		reportHolderVersion(ctx, env, s.invocation(), res.BinPath, holder.SocketPath(dataDir), "升级")
	}
	return 0
}
