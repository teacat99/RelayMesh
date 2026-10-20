package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/relsign"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// auto-upgrade enable|disable|status：主机所有者决定「这台主机的 Agent 要不要自己留意新版本 / 自己升级」
// （方案见文档库 design/agent-auto-upgrade.md）。
//
// 与 web-upgrade 同样的信任立场：同意只存在于本机数据目录里的一个文件（auto-upgrade.json），Hub 改不了、也无法远程打开。
// 默认关闭——没有所有者的明确动作，这台机器不会去检查、更不会下载并运行新程序。
// 同意每次都现读，`disable` 之后立刻生效；Agent 也会在几秒内发现它变了，重新握手，让 Web 马上看到新状态，
// 所以改同意不需要重启服务。
//
// 两种模式：
//   - notify：只提示。Agent 定期问 Hub 有没有更新的版本，有就记在日志与 status 里、并显示在 Web 主机卡片上；不下载、不安装；
//   - silent：静默。发现新版本后，在允许的时间段内自己下载、验签、升级。必须有独立于 Hub 的发布签名校验：
//     只有 SHA-256 时，Hub 被攻破就能同时换掉二进制与校验值，所以静默模式需要至少一把可信的发布公钥
//     （编进程序的，或用 --trust-key 钉住的）；一把都没有时拒绝开启，宁可明确告诉所有者「还不行」，
//     也不能让他以为开了静默、其实没有任何保护。

// autoUpgradeOpts 是 enable 的参数。
type autoUpgradeOpts struct {
	Mode string
	// Window 是静默升级允许开始的时间段（HH:MM-HH:MM，主机本地时间）；空 = 不限。只用于静默模式。
	Window string
	// TrustKeys 是要钉住的发布公钥（rmpub1:…）。只用于静默模式。
	TrustKeys []string
}

type autoUpgradeResult struct {
	Action string
	// Mode 是当前同意的模式（notify / silent）；没有同意为空。
	Mode string
	// Window / PinnedKeyIDs / EmbeddedKeyIDs 是静默模式的设置：时间段原文、本机钉住的公钥标识、编进程序的公钥标识。
	Window         string
	PinnedKeyIDs   []string
	EmbeddedKeyIDs []string
	// Problem 非空表示设置文件里有读不懂的内容（静默升级遇到它不会进行）。
	Problem string
	// Attempts 是静默升级的尝试记录（status 时读取）。
	Attempts agent.AutoAttempts
	// Since 是同意写入的时刻（Unix 秒）；没有同意为 0。
	Since    int64
	FilePath string
	DataDir  string
	// Capable 报告这台主机上的 Agent 是否具备自动升级的前提（以 systemd 管理的常驻服务运行）。
	Capable bool
	// Check 是 Agent 最近一次自动升级检查的结果（status 时读取；没检查过或没有同意时为 nil）。
	Check *agent.AutoCheckNote
}

// autoUpgradeHostError 判断这台主机能否使用自动升级；能则返回 nil。
// 前提与 Web 一键升级一致：只有 Linux 上由 systemd 管理的常驻服务才有「自己升级」的能力。
func autoUpgradeHostError(s sysOps, wantUser bool) error {
	if s.goos != "linux" {
		return errors.New("这个系统暂不支持自动升级（目前只有 Linux 上由 systemd 管理的常驻服务支持）；请继续用手动升级（" + "relaymesh-agent upgrade" + "），或在 Web 上用「升级窗口」拿到命令")
	}
	if _, err := s.installedScope(wantUser); err != nil {
		if errors.Is(err, errNothingToUpgrade) {
			return errors.New("没有检测到已安装的 RelayMesh Agent 常驻服务：只有装成常驻服务（install）的 Agent 才能使用自动升级，前台运行的不行")
		}
		return err
	}
	return nil
}

// doAutoUpgrade 执行 enable / disable / status（enable 只带模式；时间段与公钥见 doAutoUpgradeWith）。
func doAutoUpgrade(s sysOps, action, mode, dataDir string, wantUser bool) (autoUpgradeResult, error) {
	return doAutoUpgradeWith(s, action, autoUpgradeOpts{Mode: mode}, dataDir, wantUser)
}

// buildAutoUpgradeConfig 校验 enable 的参数并生成要写入的同意内容；任何一项不合规都不会写文件。
func buildAutoUpgradeConfig(o autoUpgradeOpts) (agent.AutoUpgradeConfig, error) {
	if !termproto.ValidAutoUpgradeMode(o.Mode) {
		return agent.AutoUpgradeConfig{}, errors.New("请用 --mode 指定模式：notify（只提示，不下载）或 silent（静默升级）")
	}
	if o.Mode != termproto.AutoUpgradeSilent {
		if o.Window != "" || len(o.TrustKeys) > 0 {
			return agent.AutoUpgradeConfig{}, errors.New("--window 和 --trust-key 只用于 --mode silent")
		}
		return agent.AutoUpgradeConfig{Mode: o.Mode}, nil
	}
	if _, err := agent.ParseAutoWindow(o.Window); err != nil {
		return agent.AutoUpgradeConfig{}, fmt.Errorf("--window 不对：%w", err)
	}
	cfg := agent.AutoUpgradeConfig{Mode: o.Mode, Window: o.Window}
	for _, text := range o.TrustKeys {
		k, err := relsign.ParsePublicKey(text)
		if err != nil {
			return agent.AutoUpgradeConfig{}, fmt.Errorf("--trust-key 不对：%w", err)
		}
		cfg.TrustKeys = append(cfg.TrustKeys, relsign.FormatPublicKey(k)) // 统一成带前缀的写法再保存
	}
	set := cfg.Settings()
	if set.Problem != "" {
		return agent.AutoUpgradeConfig{}, errors.New(set.Problem)
	}
	if len(set.Keys()) == 0 {
		return agent.AutoUpgradeConfig{}, errors.New("静默模式需要至少一把可信的发布公钥，用来验证新版本的发布签名：这个程序没有编进公钥（自行编译的版本可能如此），" +
			"请用 --trust-key rmpub1:<公钥> 钉住发布流程所用的公钥；或者先用 --mode notify（只提示，不下载）")
	}
	return cfg, nil
}

// doAutoUpgradeWith 执行 enable / disable / status。
func doAutoUpgradeWith(s sysOps, action string, opts autoUpgradeOpts, dataDir string, wantUser bool) (autoUpgradeResult, error) {
	res := autoUpgradeResult{Action: action, FilePath: agent.AutoUpgradePath(dataDir), DataDir: dataDir}
	switch action {
	case "enable":
		cfg, err := buildAutoUpgradeConfig(opts)
		if err != nil {
			return res, err
		}
		// 没装服务就打开同意只会造成「以为开了」的错觉，所以前提不满足时不写文件。
		if err := autoUpgradeHostError(s, wantUser); err != nil {
			return res, err
		}
		if err := agent.WriteAutoUpgrade(dataDir, cfg); err != nil {
			return res, fmt.Errorf("无法写入同意文件 %s: %w", res.FilePath, err)
		}
		// 重新 enable 是所有者明确的动作：之前因为连续失败而暂停的版本，这时重新给它机会。
		_ = agent.ClearAutoAttempts(dataDir)
	case "disable":
		if err := agent.RemoveAutoUpgrade(dataDir); err != nil {
			return res, fmt.Errorf("无法删除同意文件 %s: %w", res.FilePath, err)
		}
		// 检查结果、尝试记录跟着同意走：收回之后不再留着一份会让人误会的旧结果（删不掉也无妨，status 只在已开启时才展示它们）。
		_ = agent.RemoveAutoCheckNote(dataDir)
		_ = agent.ClearAutoAttempts(dataDir)
	case "status":
		// 普通用户查看系统级安装时会得到「需要管理员权限」：服务确实装着，只是这个用户改不了，不能因此说「没有常驻服务」。
		err := autoUpgradeHostError(s, wantUser)
		res.Capable = err == nil || errors.Is(err, errUpgradeNeedRoot)
	default:
		return res, fmt.Errorf("不认识的动作 %q，应为 enable / disable / status", action)
	}
	if cfg, ok := agent.ReadAutoUpgrade(dataDir); ok {
		res.Mode, res.Since = cfg.Mode, cfg.At
		if cfg.Mode == termproto.AutoUpgradeSilent {
			set := cfg.Settings()
			res.Window, res.Problem = set.WindowText, set.Problem
			for _, k := range set.TrustKeys {
				res.PinnedKeyIDs = append(res.PinnedKeyIDs, relsign.KeyID(k))
			}
			for _, k := range (agent.AutoSettings{}).Keys() { // 没有钉住的公钥时，名单就是编进程序的那几把
				res.EmbeddedKeyIDs = append(res.EmbeddedKeyIDs, relsign.KeyID(k))
			}
		}
		if action == "status" {
			if n, ok := agent.ReadAutoCheckNote(dataDir); ok {
				res.Check = &n
			}
			if cfg.Mode == termproto.AutoUpgradeSilent {
				res.Attempts = agent.ReadAutoAttempts(dataDir)
			}
		}
	}
	return res, nil
}

func cmdAutoUpgrade(_ context.Context, args []string, env Env) int {
	// 动作可以在选项之前（auto-upgrade enable --mode notify），也可以在之后。
	action, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, rest = args[0], args[1:]
	}
	var dataDir, mode, window string
	var trustKeys stringList
	var user bool
	fs := newFlagSet("auto-upgrade", env)
	fs.StringVar(&mode, "mode", "", "enable 时的模式：notify（只提示）或 silent（静默升级）")
	fs.StringVar(&window, "window", "", "静默升级允许开始的时间段 HH:MM-HH:MM（主机本地时间，可跨午夜；默认不限）；只用于 enable --mode silent")
	fs.Var(&trustKeys, "trust-key", "钉住一把发布公钥（rmpub1:…，可重复）：静默升级只接受用它们（以及编进程序的公钥）验过签名的新版本；只用于 enable --mode silent")
	fs.StringVar(&dataDir, "data-dir", env.Getenv(envDataDir), "Agent 的数据目录（默认按用户 / 系统区分，与服务使用的一致）")
	fs.BoolVar(&user, "user", false, "针对当前用户级安装的服务")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(env)
			return 0
		}
		env.errorf("参数错误: %v\n", err)
		return 2
	}
	if action == "" && fs.NArg() > 0 {
		action = fs.Arg(0)
	} else if fs.NArg() > 0 {
		env.errorf("参数错误: 多余的参数: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if action == "" {
		action = "status"
	}
	if action != "enable" && action != "disable" && action != "status" && action != "keys" {
		env.errorf("参数错误: 不认识的动作 %q，应为 enable / disable / status / keys\n", action)
		return 2
	}
	if (mode != "" || window != "" || len(trustKeys) > 0) && action != "enable" {
		env.errorf("参数错误: --mode / --window / --trust-key 只用于 enable\n")
		return 2
	}
	if action == "enable" && mode == "" {
		env.errorf("参数错误: enable 需要 --mode：notify（只提示，不下载）或 silent（静默升级）\n")
		return 2
	}
	if action == "keys" {
		// 只读这个程序里编进去的公钥：不碰数据目录，也不需要任何前提（Windows / macOS 上同样能看）。
		printEmbeddedKeys(env)
		return 0
	}
	if dataDir == "" {
		dataDir = agent.DefaultDataDir()
	}
	s, err := realSys()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	res, err := doAutoUpgradeWith(s, action, autoUpgradeOpts{Mode: mode, Window: window, TrustKeys: trustKeys}, dataDir, user)
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}

	switch action {
	case "enable":
		printAutoUpgradeEnabled(env, res)
	case "disable":
		env.printf("✅ 已关闭自动升级：这台主机的 Agent 不会再检查新版本。\n")
		env.printf("（已经在进行中的升级不受影响；同意文件 %s 已删除）\n", res.FilePath)
	default:
		if res.Mode != "" {
			env.printf("自动升级: 已开启 · %s\n", describeAutoUpgradeMode(res.Mode))
			if res.Since > 0 {
				env.printf("同意文件: %s（写入于 %s）\n", res.FilePath, time.Unix(res.Since, 0).Format("2006-01-02 15:04:05"))
			} else {
				env.printf("同意文件: %s\n", res.FilePath)
			}
			if !res.Capable {
				env.printf("⚠ 但没有检测到这台主机上有已安装的 Agent 常驻服务（Linux + systemd）：这个设置不会生效。\n")
			}
			if res.Mode == termproto.AutoUpgradeSilent {
				env.printf("升级时间段: %s\n", describeAutoWindow(res.Window))
				env.printf("可信的发布公钥: %s\n", describeAutoKeys(res))
				if len(res.EmbeddedKeyIDs)+len(res.PinnedKeyIDs) == 0 {
					env.printf("⚠ 没有任何可信的发布公钥：静默升级不会进行。\n")
				}
				if res.Problem != "" {
					env.printf("⚠ 设置有问题，静默升级不会进行：%s\n", res.Problem)
				}
				if line := describeAutoAttempts(res.Attempts, res.Check); line != "" {
					env.printf("升级尝试: %s\n", line)
				}
			}
			if res.Check != nil {
				env.printf("上一次检查: %s\n", describeAutoCheckNote(*res.Check))
				if res.Mode == termproto.AutoUpgradeSilent && res.Check.Install != "" {
					env.printf("静默升级: %s\n", describeAutoInstallNote(*res.Check))
				}
			} else {
				env.printf("上一次检查: 还没有（Agent 连上 Hub 后几十秒内会检查一次，之后每隔几个小时一次）\n")
			}
			env.printf("随时可以收回（立刻生效，不需要重启服务）：%s\n", cmdLine(env.Prog+" auto-upgrade disable"))
		} else {
			env.printf("自动升级: 未开启（默认）\n")
			env.printf("同意文件: %s\n", res.FilePath)
			env.printf("开启提示模式（需要先把 Agent 装成常驻服务）：%s\n", cmdLine(env.Prog+" auto-upgrade enable --mode notify"))
		}
	}
	return 0
}

// printAutoUpgradeEnabled 说明刚开启的自动升级是什么、怎么收回；auto-upgrade enable 与 install --auto-upgrade 共用。
func printAutoUpgradeEnabled(env Env, res autoUpgradeResult) {
	env.printf("✅ 已开启自动升级（%s）\n", describeAutoUpgradeMode(res.Mode))
	env.printf("  同意文件: %s\n", res.FilePath)
	if res.Mode == termproto.AutoUpgradeSilent {
		env.printf("  升级时间段: %s\n", describeAutoWindow(res.Window))
		env.printf("  可信的发布公钥: %s\n\n", describeAutoKeys(res))
		env.printf("Agent 会在连上 Hub 后以及之后每隔几个小时问一次有没有更新的版本；有的话，在允许的时间段内向 Hub 请求下载，\n")
		env.printf("下载后先验证发布签名（对不上就既不运行也不安装），再交给独立的升级进程替换并重启；新版本连不上 Hub 会自动还原。\n")
		env.printf("同一个版本连续失败 2 次就停下，不会无限重试。运行中的会话由守护进程持有，不受 Agent 重启影响。\n")
		env.printf("Hub 的管理员另有一个「总闸」：总闸没打开时 Hub 不批准。\n")
	} else {
		env.printf("\n")
		env.printf("Agent 会在连上 Hub 后以及之后每隔几个小时问一次有没有更新的版本；有的话只记在日志与 auto-upgrade status 的输出里、\n")
		env.printf("并显示在 Web 主机卡片上，不会下载，也不会安装。Hub 的管理员另有一个「总闸」：总闸没打开时 Hub 不回答检查。\n")
	}
	env.printf("随时可以收回（立刻生效，不需要重启服务）：%s\n", cmdLine(env.Prog+" auto-upgrade disable"))
}

// installAutoUpgradeOpts 校验 install 的 --auto-upgrade / --auto-window，在动手安装之前就把不合规的挡住
// （装到一半才发现参数不对，比一开始就拒绝糟糕得多）。两个选项都没给时返回零值，表示安装时不碰自动升级。
func installAutoUpgradeOpts(mode, window, goos string) (autoUpgradeOpts, error) {
	mode, window = strings.ToLower(strings.TrimSpace(mode)), strings.TrimSpace(window)
	if mode == "" && window == "" {
		return autoUpgradeOpts{}, nil
	}
	if mode == "" {
		return autoUpgradeOpts{}, errors.New("--auto-window 需要同时指定 --auto-upgrade silent")
	}
	if !termproto.ValidAutoUpgradeMode(mode) {
		return autoUpgradeOpts{}, errors.New("--auto-upgrade 应为 notify（只提示，不下载）或 silent（静默升级）")
	}
	if mode != termproto.AutoUpgradeSilent && window != "" {
		return autoUpgradeOpts{}, errors.New("--auto-window 只用于 --auto-upgrade silent")
	}
	if goos != "linux" {
		return autoUpgradeOpts{}, fmt.Errorf("这个系统（%s）暂不支持自动升级：目前只有 Linux 上由 systemd 管理的常驻服务支持；去掉 --auto-upgrade 继续安装，之后用 upgrade 命令手动升级", goos)
	}
	if mode == termproto.AutoUpgradeSilent {
		if _, err := agent.ParseAutoWindow(window); err != nil {
			return autoUpgradeOpts{}, fmt.Errorf("--auto-window 不对：%w", err)
		}
		if len((agent.AutoSettings{}).Keys()) == 0 {
			return autoUpgradeOpts{}, errors.New("--auto-upgrade silent 需要这个程序编进了发布公钥（用来验证新版本的发布签名），但这个程序没有（自行编译的版本可能如此）；" +
				"请改用 --auto-upgrade notify，或装好后用 auto-upgrade enable --mode silent --trust-key rmpub1:<公钥> 钉住发布流程所用的公钥")
		}
	}
	return autoUpgradeOpts{Mode: mode, Window: window}, nil
}

// printEmbeddedKeys 列出编进这个程序的可信发布公钥，每行「<标识>  <rmpub1:公钥>」。公钥本来就是公开的；
// 格式是固定的：scripts/sign-agent-dist.sh 靠它从「真正要发布的那个 Agent 程序」里核对签名用的钥匙确实在它的信任名单里。
func printEmbeddedKeys(env Env) {
	keys := (agent.AutoSettings{}).Keys() // 没有钉住的公钥时，名单就是编进程序的那几把
	if len(keys) == 0 {
		env.printf("这个程序没有编进发布公钥（自行编译的版本可能如此）：静默升级只能靠 --trust-key 钉住的公钥。\n")
		return
	}
	env.printf("编进程序的发布公钥（%d 把）：静默升级只接受用它们（以及本机用 --trust-key 钉住的公钥）验过签名的新版本\n", len(keys))
	for _, k := range keys {
		env.printf("  %s  %s\n", relsign.KeyID(k), relsign.FormatPublicKey(k))
	}
}

// describeAutoCheckNote 把一次检查的结果整理成一行人话。
func describeAutoCheckNote(n agent.AutoCheckNote) string {
	what := map[string]string{
		termproto.AutoOfferAvailable:   "Hub 上有新版本 " + n.Latest,
		termproto.AutoOfferUpToDate:    "已是最新" + withLatest(n.Latest),
		termproto.AutoOfferDisabled:    "Hub 的自动升级总闸是关的，没有提示（需要 Hub 管理员在设置里打开）",
		termproto.AutoOfferUnavailable: "Hub 这边没有适用于本机的 Agent 程序，或没有记录版本",
		termproto.AutoOfferNotAllowed:  "Hub 认为这台主机没有开启自动升级（刚改过设置的话，下一次握手后会同步）",
		"no_reply":                     "Hub 没有回答（可能是较旧的版本，不认识自动升级）",
	}[n.Status]
	if what == "" {
		what = n.Status
	}
	s := what
	if n.Current != "" {
		s += "（本机 " + n.Current + "）"
	}
	return time.Unix(n.At, 0).Format("2006-01-02 15:04:05") + " · " + s
}

func withLatest(latest string) string {
	if latest == "" {
		return ""
	}
	return "（Hub 上最新版本 " + latest + "）"
}

// describeAutoWindow 把时间段整理成一句人话。
func describeAutoWindow(window string) string {
	if window == "" {
		return "不限（有新版本就升级）"
	}
	return window + "（主机本地时间；只约束「什么时候开始」，开始之后不会因为时间段结束而中断）"
}

// describeAutoKeys 概括可信公钥名单：编进程序的，加上本机钉住的（只显示标识，不显示完整公钥）。
// 标识与发布签名里写的密钥标识是同一个写法，所有者可以拿它与发布方公布的标识核对。
func describeAutoKeys(res autoUpgradeResult) string {
	return describeKeyGroup("编进程序", res.EmbeddedKeyIDs) + " · " + describeKeyGroup("本机钉住", res.PinnedKeyIDs)
}

func describeKeyGroup(label string, ids []string) string {
	if len(ids) == 0 {
		return label + " 0 把"
	}
	return fmt.Sprintf("%s %d 把（标识 %s）", label, len(ids), strings.Join(ids, "、"))
}

// describeAutoAttempts 说明尝试记录；与最近一次检查里的最新版本对得上、且已经试满时，明确说「已暂停」。
func describeAutoAttempts(a agent.AutoAttempts, check *agent.AutoCheckNote) string {
	if a.Unreadable {
		return "记录读不清，为稳妥起见暂停；重新执行 auto-upgrade enable 可以清除"
	}
	if a.Count == 0 {
		return ""
	}
	s := fmt.Sprintf("%s 已尝试 %d 次", a.Version, a.Count)
	if a.At > 0 {
		s += "（最近一次 " + time.Unix(a.At, 0).Format("2006-01-02 15:04:05") + "）"
	}
	if check != nil && a.Paused(check.Latest) {
		s += "，已暂停：出现更新的版本，或重新执行 auto-upgrade enable，会再试"
	}
	return s
}

// describeAutoInstallNote 把静默升级在最近一轮的决定整理成一句人话。
func describeAutoInstallNote(n agent.AutoCheckNote) string {
	if n.Detail != "" {
		return n.Detail
	}
	return n.Install
}

// describeAutoUpgradeMode 把模式整理成一句人话。
func describeAutoUpgradeMode(mode string) string {
	switch mode {
	case termproto.AutoUpgradeNotify:
		return "提示模式：只记录与显示新版本，不下载、不安装"
	case termproto.AutoUpgradeSilent:
		return "静默模式：发现新版本后，在允许的时间段内自己下载、验签、升级"
	}
	return mode
}
