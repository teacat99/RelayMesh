//go:build !windows

package holder

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// resolveExecShell 解析模型执行使用的 shell 与环境。
//
// 与交互终端一样不能依赖继承来的环境（Agent 常由 systemd 以精简环境拉起）：HOME / USER / PATH / LANG 都会补齐。
// 区别在于：这是非交互运行，所以 TERM=dumb 且关闭颜色，避免输出里混入转义序列；
// 仍以登录 shell 方式启动（-l -c），这样 ~/.profile 里设置的 PATH 等与用户平时所见一致。
func resolveExecShell(name string) (execSpec, error) {
	uname, home, _ := passwdEntry(os.Geteuid())
	if home == "" {
		home = os.Getenv("HOME")
	}
	if uname == "" {
		uname = os.Getenv("USER")
	}

	var path string
	switch name {
	case "":
		if path = findShell("bash"); path == "" {
			path = findShell("sh")
		}
		if path == "" {
			return execSpec{}, errNoShell
		}
	case "bash", "sh", "zsh":
		if path = findShell(name); path == "" {
			return execSpec{}, errNoShell
		}
	default:
		return execSpec{}, errBadShell
	}

	env := execEnv(buildEnv(os.Environ(), path, home, uname))
	login := loginArgs(path)
	return execSpec{
		Path: path,
		Name: filepath.Base(path),
		Args: func(command string) []string {
			args := append([]string(nil), login...)
			return append(args, "-c", command)
		},
		Env:  env,
		Home: home,
		Sep:  "/",
	}, nil
}

// execEnv 把交互终端的环境改成适合非交互运行的样子。
func execEnv(base []string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "TERM=") || strings.HasPrefix(kv, "COLORTERM=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM=dumb", "NO_COLOR=1")
}

// findShell 在常见位置与 PATH 中找一个可用的 shell（返回绝对路径）。
func findShell(name string) string {
	for _, dir := range []string{"/bin", "/usr/bin", "/usr/local/bin", "/opt/homebrew/bin"} {
		if p := filepath.Join(dir, name); usableShell(p) {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil && usableShell(p) {
		return p
	}
	return ""
}

// prepareRunCmd 让命令在自己的进程组里运行：取消 / 超时时可以连同它派生的子进程一起终止。
func prepareRunCmd(c *exec.Cmd, _ execSpec, _ string) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateTree 向整个进程组发信号：force=false 为 SIGTERM（让进程有机会收尾），true 为 SIGKILL。
func terminateTree(pid int, force bool) {
	if pid <= 1 {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-pid, sig)
}

// exitDetail 解析进程的结局：退出码，或终止它的信号名。
func exitDetail(ps *os.ProcessState) (code int, signal string, signaled bool) {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, signalName(ws.Signal()), true
	}
	return ps.ExitCode(), "", false
}

func signalName(sig syscall.Signal) string {
	names := map[syscall.Signal]string{
		syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT", syscall.SIGILL: "SIGILL",
		syscall.SIGABRT: "SIGABRT", syscall.SIGFPE: "SIGFPE", syscall.SIGKILL: "SIGKILL", syscall.SIGBUS: "SIGBUS",
		syscall.SIGSEGV: "SIGSEGV", syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
	}
	if n, ok := names[sig]; ok {
		return n
	}
	return "SIG" + itoa(int(sig))
}

// checkCommandLength 在 Unix 上无需额外检查（命令作为独立参数传递，只受内核 ARG_MAX 约束，远大于 MaxRunCommandBytes）。
func checkCommandLength(execSpec, string) error { return nil }
