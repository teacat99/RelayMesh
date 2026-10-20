package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Web 一键升级的本机一侧（方案见 .cursor/planning/2026-10-08-agent-web-upgrade-plan.md）。
//
// Agent 自己只负责下载与校验新版本（internal/agent/webupgrade.go）；真正「替换程序、重启服务、等新版本上线、
// 不行就还原」由一个独立的升级进程完成——就是 `relaymesh-agent upgrade --from <新程序> --auto-rollback …`。
// 难点与远程卸载一样：替换会停止 Agent 自己，所以升级进程不能是 Agent 的子进程（服务管理器停服务时会连进程树一起结束）。
// 因此借 systemd-run 起一个瞬态单元，把它放到 Agent 的 cgroup 之外。
//
// 谁来执行 upgrade：**已安装的（旧）程序**，而不是刚下载的新程序。新程序此刻还没有被证明能在这台机器上稳定运行，
// 而「新版本起不来就还原」这件事不能依赖它自己；旧程序是这台机器上已经跑通的那份代码。
//
// 与远程卸载共用同一套判断：必须是被 systemd 管理的常驻服务（INVOCATION_ID + 对应范围的单元文件 + systemd-run）。
// macOS / Windows 暂不支持，Agent 不声明能力，界面上只给手动路径。

// linuxSelfUpgrader 返回 Linux 上的 agent.Options.SelfUpgrade；不是被 systemd 管理的常驻服务时返回 nil。
func linuxSelfUpgrader(ops selfOps, env Env, cfg resolved) *agent.SelfUpgrade {
	if ops.getenv("INVOCATION_ID") == "" {
		return nil
	}
	user := ops.geteuid() != 0
	if !ops.unitInstalled(user) {
		return nil
	}
	if _, err := ops.lookPath("systemd-run"); err != nil {
		return nil
	}
	dataDir := cfg.dataDir
	return &agent.SelfUpgrade{
		Allowed:  func() bool { return agent.WebUpgradeAllowed(dataDir) },
		AutoMode: func() string { return agent.AutoUpgradeMode(dataDir) },
		// 每次自动升级检查的结论留一份在数据目录里，`auto-upgrade status` 据此在本机展示「上一次检查」。
		AutoChecked: func(n agent.AutoCheckNote) { _ = agent.WriteAutoCheckNote(dataDir, n) },
		// 静默模式要读的设置（时间段、钉住的公钥）与「同一个版本已经试过几次」的记录，都在本机的数据目录里。
		AutoSettings:  func() (agent.AutoSettings, bool) { return agent.ReadAutoSettings(dataDir) },
		AutoAttempts:  func() agent.AutoAttempts { return agent.ReadAutoAttempts(dataDir) },
		AutoAttempted: func(version string) error { return agent.RecordAutoAttempt(dataDir, version, ops.now()) },
		StageDir:      agent.UpgradeDir(dataDir),
		Handoff: func(_ context.Context, newBinary, version string) error {
			exe, err := ops.executable()
			if err != nil {
				return fmt.Errorf("无法确定当前可执行文件: %w", err)
			}
			// 被替换过的二进制，/proc/self/exe 会带上 " (deleted)" 后缀
			exe = strings.TrimSuffix(exe, " (deleted)")
			args := systemdRunUpgradeArgs(ops.now(), user, exe, env.ServiceArgs, newBinary, version, cfg)
			if out, err := ops.run("systemd-run", args...); err != nil {
				return fmt.Errorf("systemd-run 启动升级进程失败: %v %s", err, strings.TrimSpace(out))
			}
			return nil
		},
		Note: func() *termproto.UpgradeNote { return agent.ReadUpgradeNote(dataDir) },
		// 每次握手成功就留下「上线标记」：升级进程靠它确认新版本真的连上了 Hub；连不上就还原旧版本。
		Connected: func(version string) { _ = agent.WriteOnlineMarker(dataDir, version, ops.now()) },
	}
}

// systemdRunUpgradeArgs 拼出 systemd-run 的参数：瞬态单元里执行 `<exe> [前缀] upgrade --from … --auto-rollback …`。
func systemdRunUpgradeArgs(now time.Time, user bool, exe string, prefix []string, newBinary, version string, cfg resolved) []string {
	args := make([]string, 0, 20)
	if user {
		args = append(args, "--user")
	}
	args = append(args,
		"--collect", // 升级进程结束后，瞬态单元不留痕迹（结果写在数据目录里，由 Agent 带给 Hub）
		"--quiet",
		"--unit=relaymesh-agent-upgrade-"+strconv.FormatInt(now.Unix(), 10),
		"--description=RelayMesh Agent Web 升级",
		"--",
		exe,
	)
	args = append(args, prefix...)
	args = append(args, upgradeProcessArgs(user, newBinary, version, cfg)...)
	return args
}

// upgradeProcessArgs 是升级进程的命令行（不含可执行文件与前缀参数）。数据目录显式传入：
// 升级进程的环境（HOME、环境变量）可能与 Agent 服务不同，不能靠它重新推导。
func upgradeProcessArgs(user bool, newBinary, version string, cfg resolved) []string {
	args := []string{"upgrade", "--from", newBinary, "--auto-rollback", "--remove-source", "--expect-version", version}
	if user {
		args = append(args, "--user")
	}
	if cfg.dataDir != "" {
		args = append(args, "--data-dir", cfg.dataDir)
	}
	return args
}
