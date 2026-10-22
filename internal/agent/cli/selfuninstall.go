package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 远程卸载：管理员在 Web 上删除主机并勾选「同时卸载设备端」时，Hub 向在线的 Agent 发一帧 uninstall，
// Agent 在本机另起一个卸载进程，效果与管理员在主机上手动执行 `relaymesh-agent uninstall` 完全一致——
// 复用同一条经过测试的卸载流程，不另写一套删除逻辑。
//
// 难点只有一个：卸载会停止 Agent 自己，所以卸载进程不能是 Agent 的子进程——服务管理器停止服务时会连同
// 它的进程树一起结束，卸载做到一半就被杀掉。因此要借服务管理器本身把卸载进程放到 Agent 的进程树之外：
//   - Linux：systemd-run 起一个瞬态单元（独立 cgroup，不受 Agent 单元停止的影响）；
//   - Windows：创建一个没有触发器的一次性计划任务并 /Run（任务计划程序启动的进程不是 Agent 的子进程）；
//   - macOS 等其他系统：暂不支持，Agent 不声明能力，界面上不会提供「同时卸载」，只给出手动命令。
//
// 只有「确认自己是以常驻服务安装并运行」的 Agent 才会声明能力（agent.Options.SelfUninstall 非 nil）：
// 前台运行、临时身份、容器里等场景不会被误卸载别的东西。

// selfOps 是 Linux 判断与启动卸载进程所需的系统调用，注入以便测试。
type selfOps struct {
	getenv     func(string) string
	geteuid    func() int
	lookPath   func(string) (string, error)
	executable func() (string, error)
	run        func(name string, args ...string) (string, error)
	now        func() time.Time
	// unitInstalled 报告指定范围（用户 / 系统）的 systemd 服务单元文件是否存在。
	unitInstalled func(user bool) bool
}

// linuxSelfUninstaller 返回 Linux 上的 agent.Options.SelfUninstall；不是被 systemd 管理的常驻服务时返回 nil。
func linuxSelfUninstaller(ops selfOps, env Env, cfg resolved) func(purge bool) error {
	// systemd 会给它启动的服务进程设置 INVOCATION_ID。单凭它还不够：从某个服务（例如会话守护进程）里打开的终端也会继承，
	// 所以还要求对应范围的服务单元文件确实存在——两者同时满足才认为「这个 Agent 是装成服务运行的」。
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
	return func(purge bool) error {
		exe, err := ops.executable()
		if err != nil {
			return fmt.Errorf("无法确定当前可执行文件: %w", err)
		}
		// 升级替换过的二进制，/proc/self/exe 会带上 " (deleted)" 后缀
		exe = strings.TrimSuffix(exe, " (deleted)")
		args := systemdRunArgs(ops.now(), user, exe, env.ServiceArgs, purge, cfg)
		if out, err := ops.run("systemd-run", args...); err != nil {
			return fmt.Errorf("systemd-run 启动卸载进程失败: %v %s", err, strings.TrimSpace(out))
		}
		return nil
	}
}

// systemdRunArgs 拼出 systemd-run 的参数：瞬态单元里执行 `<exe> [前缀] uninstall --yes [--user] [--purge] --data-dir … --config-dir …`。
func systemdRunArgs(now time.Time, user bool, exe string, prefix []string, purge bool, cfg resolved) []string {
	args := make([]string, 0, 16)
	if user {
		args = append(args, "--user")
	}
	args = append(args,
		"--collect", // 卸载进程结束后，瞬态单元不留痕迹
		"--quiet",
		"--unit=relaymesh-agent-uninstall-"+strconv.FormatInt(now.Unix(), 10),
		"--description=RelayMesh Agent 远程卸载",
		"--",
		exe,
	)
	args = append(args, prefix...)
	args = append(args, uninstallArgs(user, purge, cfg)...)
	return args
}

// uninstallArgs 是卸载进程的命令行（不含可执行文件与前缀参数）。数据目录与配置目录显式传入：
// 卸载进程的环境（HOME、环境变量）可能与 Agent 服务不同，不能靠它重新推导。
func uninstallArgs(user, purge bool, cfg resolved) []string {
	args := []string{"uninstall", "--yes"}
	if user {
		args = append(args, "--user")
	}
	if purge {
		args = append(args, "--purge")
	}
	if cfg.dataDir != "" {
		args = append(args, "--data-dir", cfg.dataDir)
	}
	if cfg.cfgDir != "" {
		args = append(args, "--config-dir", cfg.cfgDir)
	}
	return args
}

// ---------- Windows ----------

// winUninstallTaskSuffix 是远程卸载专用的一次性计划任务相对于 Agent 任务名的后缀。
// 卸载流程（doUninstallWindows）会顺手把这个任务删掉。
const winUninstallTaskSuffix = "-Uninstall"

// windowsSelfUninstaller 返回 Windows 上的 agent.Options.SelfUninstall；当前进程不是已安装的无窗口版 Agent 时返回 nil。
func windowsSelfUninstaller(w winOps, env Env, cfg resolved) func(purge bool) error {
	name := installedName(env)
	for _, user := range []bool{false, true} {
		scope, err := w.scopeOf(user)
		if err != nil {
			continue
		}
		if sameWinPath(w.exe, filepath.Join(scope.binDir, name+"w.exe")) {
			return func(purge bool) error { return w.startUninstallTask(env, scope, purge, cfg) }
		}
	}
	return nil
}

func sameWinPath(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// startUninstallTask 创建一个没有触发器的一次性计划任务（身份与 Agent 任务一致：系统级为 SYSTEM，用户级为该用户，
// 最高权限的用户级任务同样以最高权限，否则删不掉它）并立即运行。任务运行的是已安装的无窗口版 `uninstall`。
func (w winOps) startUninstallTask(env Env, scope winScope, purge bool, cfg resolved) error {
	guiBin := filepath.Join(scope.binDir, installedName(env)+"w.exe")
	argv := append(append([]string(nil), env.ServiceArgs...), uninstallArgs(scope.user, purge, cfg)...)
	task := scope.taskName + winUninstallTaskSuffix
	spec := taskSpec{
		Description: "RelayMesh Agent 卸载（Web 端删除主机时触发，运行一次，卸载流程结束时自行删除）",
		System:      !scope.user,
		User:        w.username,
		Highest:     scope.user && w.taskIsHighest(scope.taskName),
		OnDemand:    true,
		Command:     guiBin,
		Arguments:   windowsJoinArgs(argv),
		Start:       w.now(),
	}
	if err := w.createTask(nil, task, spec); err != nil {
		return err
	}
	if out, err := w.schtasks(nil, "/Run", "/TN", task); err != nil {
		_, _ = w.schtasks(nil, "/Delete", "/TN", task, "/F") // 别留下一个永远不会被运行的任务
		return errors.New("启动卸载任务失败: " + err.Error() + " " + strings.TrimSpace(out))
	}
	return nil
}
