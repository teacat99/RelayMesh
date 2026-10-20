package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// web-upgrade on|off|status：主机所有者决定「是否允许管理员在 Web 上一键升级这台主机的 Agent」。
//
// 这个开关只存在于本机数据目录里的一个文件，Hub 改不了：管理员在 Web 上能点「一键升级」，是因为主机所有者
// 事先在主机上执行过 `web-upgrade on`。默认关闭——没有所有者的明确动作，服务端不能让这台机器下载并运行新程序。
// 开关每次升级请求时现读，`off` 之后立刻生效；Agent 也会在几秒内发现它变了，重新握手，让 Web 马上看到新状态，
// 所以改开关不需要重启服务。

type webUpgradeResult struct {
	Action   string
	Allowed  bool
	FlagPath string
	DataDir  string
	Note     *termproto.UpgradeNote
}

// doWebUpgrade 执行 on / off / status。
func doWebUpgrade(s sysOps, action, dataDir string, wantUser bool) (webUpgradeResult, error) {
	res := webUpgradeResult{Action: action, DataDir: dataDir, FlagPath: agent.WebUpgradeFlagPath(dataDir)}
	switch action {
	case "on":
		// 只有装成常驻服务的 Agent 才有升级能力；没装服务就打开开关只会造成「以为开了」的错觉。
		if s.goos != "linux" {
			return res, errors.New("这个系统暂不支持从 Web 一键升级（目前只有 Linux 上由 systemd 管理的常驻服务支持）；仍可以用 Web 上的「升级窗口」拿到命令，在主机上手动升级")
		}
		if _, err := s.installedScope(wantUser); err != nil {
			if errors.Is(err, errNothingToUpgrade) {
				return res, errors.New("没有检测到已安装的 RelayMesh Agent 常驻服务：只有装成常驻服务（install）的 Agent 才能从 Web 一键升级，前台运行的不行")
			}
			return res, err
		}
		if err := agent.SetWebUpgradeAllowed(dataDir, true); err != nil {
			return res, fmt.Errorf("无法写入开关文件 %s: %w", res.FlagPath, err)
		}
	case "off":
		if err := agent.SetWebUpgradeAllowed(dataDir, false); err != nil {
			return res, fmt.Errorf("无法删除开关文件 %s: %w", res.FlagPath, err)
		}
	case "status":
	default:
		return res, fmt.Errorf("不认识的动作 %q，应为 on / off / status", action)
	}
	res.Allowed = agent.WebUpgradeAllowed(dataDir)
	res.Note = agent.ReadUpgradeNote(dataDir)
	return res, nil
}

func cmdWebUpgrade(_ context.Context, args []string, env Env) int {
	// 动作可以在选项之前（web-upgrade on --data-dir X），也可以在之后。
	action, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, rest = args[0], args[1:]
	}
	var dataDir string
	var user bool
	fs := newFlagSet("web-upgrade", env)
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
	if action != "on" && action != "off" && action != "status" {
		env.errorf("参数错误: 不认识的动作 %q，应为 on / off / status\n", action)
		return 2
	}
	if dataDir == "" {
		dataDir = agent.DefaultDataDir()
	}
	s, err := realSys()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	res, err := doWebUpgrade(s, action, dataDir, user)
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}

	switch action {
	case "on":
		env.printf("✅ 已允许从 Web 升级这台主机的 RelayMesh Agent\n")
		env.printf("  开关文件: %s\n\n", res.FlagPath)
		env.printf("管理员在 Web「主机」页点「升级」时，Agent 会从你配置的服务端下载新版本，校验通过后替换自己并重启服务；\n")
		env.printf("新版本没能连上服务端时会自动还原为旧版本。会话守护进程不会被重启，正在运行的终端会话不受影响。\n")
		env.printf("随时可以收回：%s web-upgrade off（立刻生效，不需要重启服务）。\n", env.Prog)
	case "off":
		env.printf("✅ 已关闭从 Web 升级：管理员不能再通过 Web 升级这台主机的 Agent。\n")
		env.printf("（已经在进行中的升级不受影响；开关文件 %s 已删除）\n", res.FlagPath)
	default:
		if res.Allowed {
			env.printf("从 Web 升级: 已允许（开关文件 %s）\n", res.FlagPath)
			env.printf("收回: %s web-upgrade off\n", env.Prog)
		} else {
			env.printf("从 Web 升级: 未允许（默认）\n")
			env.printf("允许: %s web-upgrade on（需要先把 Agent 装成常驻服务；开关文件 %s）\n", env.Prog, res.FlagPath)
		}
		if n := res.Note; n != nil {
			env.printf("\n上一次升级: %s\n", describeUpgradeNote(*n))
		}
	}
	return 0
}

// describeUpgradeNote 把升级结果整理成一行人话。
func describeUpgradeNote(n termproto.UpgradeNote) string {
	what := map[string]string{
		termproto.UpgradeOutcomeOK:         "成功",
		termproto.UpgradeOutcomeRolledBack: "已回滚",
		termproto.UpgradeOutcomeFailed:     "失败",
	}[n.Outcome]
	s := what
	if n.From != "" || n.To != "" {
		s += fmt.Sprintf("（%s → %s）", orUnknown(n.From), orUnknown(n.To))
	}
	if n.At > 0 {
		s += " " + time.Unix(n.At, 0).Format("2006-01-02 15:04:05")
	}
	if n.Detail != "" {
		s += "：" + n.Detail
	}
	return s
}
