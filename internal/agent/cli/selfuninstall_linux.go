//go:build linux

package cli

import (
	"os"
	"os/exec"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
)

// realSelfOps 构造 Linux 上「判断自己是不是装成服务运行、并把后续动作放到自己进程树之外」所需的真实系统调用。
// 远程卸载与 Web 一键升级共用它。
func realSelfOps() (selfOps, bool) {
	s, err := realSys()
	if err != nil {
		return selfOps{}, false
	}
	return selfOps{
		getenv:     os.Getenv,
		geteuid:    os.Geteuid,
		lookPath:   exec.LookPath,
		executable: os.Executable, // 调用时才取：升级替换过二进制后路径带 " (deleted)"，由调用方处理
		run:        s.run,
		now:        time.Now,
		unitInstalled: func(user bool) bool {
			scope, err := s.scopeFor(user)
			if err != nil {
				return false
			}
			_, err = os.Stat(s.p(scope.unitPath))
			return err == nil
		},
	}, true
}

// selfUninstaller 返回交给 agent.Options.SelfUninstall 的函数；nil 表示这个 Agent 不能被远程卸载
// （前台运行、不是被 systemd 管理、没有 systemd-run 等），此时 Agent 不会声明卸载能力。
func selfUninstaller(env Env, _ common, cfg resolved) func(purge bool) error {
	ops, ok := realSelfOps()
	if !ok {
		return nil
	}
	return linuxSelfUninstaller(ops, env, cfg)
}

// selfUpgrader 返回交给 agent.Options.SelfUpgrade 的能力；nil 表示这个 Agent 不能被 Web 一键升级
// （条件与远程卸载相同：必须是被 systemd 管理的常驻服务），此时 Agent 不会声明升级能力。
func selfUpgrader(env Env, _ common, cfg resolved) *agent.SelfUpgrade {
	ops, ok := realSelfOps()
	if !ok {
		return nil
	}
	return linuxSelfUpgrader(ops, env, cfg)
}
