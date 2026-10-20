//go:build windows

package holder

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
	// maxWindowsCmdLine 是 CreateProcess 命令行长度的实际上限（32767 个 UTF-16 单元），留出余量。
	maxWindowsCmdLine = 30000
)

// resolveExecShell 解析模型执行使用的 shell：自动选择时依次尝试 pwsh、powershell、cmd。
//
// 输出统一按 UTF-8 产生（PowerShell 设置控制台输出编码，cmd 先切到 65001 代码页），避免在中文系统上出现乱码。
func resolveExecShell(name string) (execSpec, error) {
	var cands []string
	switch name {
	case "":
		cands = []string{"pwsh", "powershell", "cmd"}
	case "pwsh", "powershell", "cmd":
		cands = []string{name}
	default:
		return execSpec{}, errBadShell
	}
	var path, picked string
	for _, c := range cands {
		if p, err := exec.LookPath(c + ".exe"); err == nil {
			path, picked = p, c
			break
		}
	}
	if path == "" {
		return execSpec{}, errNoShell
	}

	home := os.Getenv("USERPROFILE")
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "RELAYMESH_") {
			env = append(env, kv)
		}
	}
	env = append(env, "NO_COLOR=1")

	spec := execSpec{Path: path, Name: picked, Env: env, Home: home, Sep: `\`}
	switch picked {
	case "cmd":
		spec.CmdLine = func(path, command string) string {
			// /s 让 cmd 只剥掉最外层的一对引号，里面的内容原样执行；先切换到 UTF-8 代码页。
			return `"` + path + `" /d /s /c "chcp 65001 >nul & ` + command + `"`
		}
	default: // pwsh / powershell：用 -EncodedCommand 传脚本，彻底绕开命令行引号转义问题。
		spec.Args = func(command string) []string {
			return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-OutputFormat", "Text", "-EncodedCommand", encodePowerShell(command)}
		}
	}
	return spec, nil
}

// encodePowerShell 把脚本包装后编码为 -EncodedCommand 所需的 UTF-16LE Base64。
// 包装做三件事：输出编码改为 UTF-8、关闭进度条、让进程退出码反映最后一条命令的结局
// （PowerShell 默认把任何非 0/1 的原生命令退出码折叠成 1）。
func encodePowerShell(command string) string {
	script := "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8;$OutputEncoding=[System.Text.Encoding]::UTF8;" +
		"$ProgressPreference='SilentlyContinue';\n" + command +
		"\nif($?){exit 0}elseif($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0){exit $LASTEXITCODE}else{exit 1}"
	u := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// checkCommandLength 在启动前检查命令行长度：超出 Windows 的上限时给出明确的错误，而不是一个含糊的启动失败。
func checkCommandLength(spec execSpec, command string) error {
	var n int
	if spec.CmdLine != nil {
		n = len(spec.CmdLine(spec.Path, command))
	} else {
		n = len(spec.Path) + 2
		for _, a := range spec.Args(command) {
			n += len(a) + 1
		}
	}
	if n > maxWindowsCmdLine {
		return errCommandTooLong
	}
	return nil
}

// prepareRunCmd 让进程不弹出控制台窗口，并放进新的进程组。
func prepareRunCmd(c *exec.Cmd, spec execSpec, command string) {
	sa := &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNewProcessGroup | createNoWindow}
	if spec.CmdLine != nil {
		sa.CmdLine = spec.CmdLine(spec.Path, command)
	}
	c.SysProcAttr = sa
}

// terminateTree 终止进程树。Windows 没有 SIGTERM 的对应物，所以第一次就强制结束（/T 连同子进程）。
func terminateTree(pid int, _ bool) {
	if pid <= 0 {
		return
	}
	kill := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe"), "/PID", strconv.Itoa(pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	_ = kill.Run()
}

// exitDetail 解析进程的结局。Windows 上没有「被信号终止」的概念。
func exitDetail(ps *os.ProcessState) (code int, signal string, signaled bool) {
	return ps.ExitCode(), "", false
}
