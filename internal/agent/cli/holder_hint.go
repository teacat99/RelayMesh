package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

// 「重启会话守护进程」的提示命令。
//
// 升级 / 安装结束时会告诉管理员「想换上新版本，执行 holder restart」。原先一律印成裸的 `relaymesh-agent holder restart`，
// 在不少主机上照抄并不能用：
//   - 命令不在 PATH 里：用户级安装放在 ~/.local/bin，登录 shell 不一定把它加进 PATH（「command not found」）；
//     Windows 装在 Program Files 下，从来不在 PATH 里（PowerShell 报「无法识别为 cmdlet……」）；
//   - 系统级安装的守护进程以 root / 管理员身份运行，套接字在系统数据目录里；普通用户直接执行时，命令会去自己目录下
//     找套接字，报「会话守护进程没有运行」，其实它一直在跑。
//
// 这里把「可以直接复制执行」的命令拼出来：必要时用完整路径、加 sudo、用 PowerShell 的调用运算符，
// 套接字不在默认位置时带上 --holder-socket；命令失败时也能认出「这是系统级安装」并给出对应的做法。

// invocation 是拼命令用到的外部事实；生产环境用 realInvocation，测试直接构造。
type invocation struct {
	goos string
	// elevated 表示当前进程是 root / 管理员。
	elevated bool
	// sudoUser 是经 sudo 运行时的原用户（SUDO_USER）；空表示不是经 sudo 运行。
	sudoUser string
	// defaultSocket 是以「当前这个身份」执行 holder 子命令、不带任何选项时会去连的套接字。
	defaultSocket string
	// onPath 报告命令名 name 在当前 PATH 里是否就解析到已安装的程序 installed。
	onPath func(name, installed string) bool
}

func realInvocation() invocation {
	return invocation{
		goos:          runtime.GOOS,
		elevated:      agent.IsElevated(),
		sudoUser:      os.Getenv("SUDO_USER"),
		defaultSocket: holder.SocketPath(agent.DefaultDataDir()),
		onPath:        lookPathSame,
	}
}

// invocation 用 sysOps 里（可注入的）系统类别与权限状态，其余取真实环境。
func (s sysOps) invocation() invocation {
	iv := realInvocation()
	if s.goos != "" {
		iv.goos = s.goos
	}
	iv.elevated = s.euid == 0
	return iv
}

// invocation 同理，Windows 版取 winOps 里的权限状态。
func (w winOps) invocation() invocation {
	iv := realInvocation()
	iv.goos = "windows"
	iv.elevated = w.elevated
	return iv
}

// lookPathSame 报告命令名 name 在 PATH 里解析到的文件是否就是 installed。
func lookPathSame(name, installed string) bool {
	found, err := exec.LookPath(name)
	if err != nil {
		return false
	}
	a, errA := os.Stat(found)
	b, errB := os.Stat(installed)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// shellQuote 按 POSIX shell 的规则给参数加引号；只含安全字符时原样返回。
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./:@%+=,", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// psQuote 按 PowerShell 的规则给字符串加单引号（内部的单引号写两个）。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// holderCommand 拼出 `<程序> holder <sub>` 的可复制命令（sub 为 restart / stop / status）。
//   - installed 是已安装程序的绝对路径；命令名在 PATH 里就能找到它时用命令名，否则用完整路径；空串表示不知道，按命令名写；
//   - socket 是守护进程实际使用的套接字；与默认值不同才带 --holder-socket；空串表示就是默认位置；
//   - 经 sudo 运行的系统级安装前面加 sudo（执行这条命令的人多半不是 root）；Windows 的完整路径要用 `& '…'`（PowerShell 调用运算符）。
func (iv invocation) holderCommand(env Env, installed, socket, sub string) string {
	words := strings.Fields(env.Prog)
	if len(words) == 0 {
		words = []string{"relaymesh-agent"}
	}
	head := words[0]
	callOp := false
	quote := shellQuote
	if iv.goos == "windows" {
		quote = psQuote
	}
	if installed != "" && (iv.onPath == nil || !iv.onPath(head, installed)) {
		head = quote(installed)
		callOp = iv.goos == "windows"
	}
	parts := append([]string{head}, words[1:]...)
	parts = append(parts, "holder", sub)
	if socket != "" && socket != iv.defaultSocket {
		parts = append(parts, "--holder-socket", quote(socket))
	}
	line := strings.Join(parts, " ")
	if callOp {
		line = "& " + line
	}
	if iv.goos != "windows" && iv.elevated && iv.sudoUser != "" {
		line = "sudo " + line
	}
	return line
}

// cmdLine 把一条可以直接复制的命令排成「独占一行、缩进四格、前后不放任何符号」的样子（前面自带换行）。
//
// 提示文案里的命令不能用反引号、引号或括号包起来，也不能让句号、分号紧贴在它后面：
// 反引号在 PowerShell 里是转义符、在 bash / zsh 里是命令替换，一旦和命令一起被选中复制，命令就废了
// （曾经照抄升级提示，得到的是「未知的 holder 子命令 "restart`"」）；其他符号也会随「选到行尾」一起被带走。
// 所以命令永远自己占一行，需要补充的说明写在它前面。
// no_backtick_test.go 里的守卫测试会拦住 internal/agent 下任何字符串字面量里的反引号。
func cmdLine(cmd string) string { return "\n    " + cmd }

// elevationLead 是执行 holder 命令还需要的身份说明，写成接在上一句后面的前置短语（只有 Windows 的系统级安装需要，
// 其余已经体现在 sudo 前缀里）；不需要时为空。它必须放在命令前面：命令之后不能再跟任何文字（见 cmdLine）。
func (iv invocation) elevationLead() string {
	if iv.goos == "windows" && iv.elevated {
		return "，在「以管理员身份运行」的 PowerShell 里"
	}
	return ""
}

// restartAction 是「怎么重启守护进程」的后半句：身份说明 + 执行下面这条命令 + 重启的后果。调用方在前面接上「想……时，在合适的时机」之类的开头。
// 重启会结束守护进程里的全部终端会话，也包括模型通过 terminal_exec 发起、正在运行的命令（它们同样由守护进程持有）；
// 加 --drain 则等这两类都自然结束后再退出。
func (iv invocation) restartAction(env Env, installed, socket string) string {
	cmd := iv.holderCommand(env, installed, socket, "restart")
	return iv.elevationLead() + "执行下面这条命令（会结束其中的全部会话和模型正在运行的命令；想等它们自然结束，就在命令末尾加 --drain）：" + cmdLine(cmd)
}

// restartHint 是升级 / 安装结束时的整句提示：怎么让守护进程换上新版本。
func (iv invocation) restartHint(env Env, installed, socket string) string {
	return "想换上新版本时" + iv.restartAction(env, installed, socket)
}

// systemHolderPaths 返回系统级安装的守护进程服务单元与数据目录（Linux / macOS 有单元文件，Windows 只有数据目录）。
func systemHolderPaths(goos string) (unit, dataDir string) {
	switch goos {
	case "linux":
		return "/etc/systemd/system/" + systemdHolderUnitName, "/var/lib/relaymesh-agent"
	case "darwin":
		return "/Library/LaunchDaemons/" + launchdHolderLabel + ".plist", "/Library/Application Support/RelayMesh Agent"
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return "", filepath.Join(pd, "RelayMesh", "Agent")
	}
	return "", ""
}

// systemHolderHint 在「没找到会话守护进程」时补一句最常见的原因：这台机器上的守护进程是系统级安装的（以 root / 管理员运行），
// 它的套接字在系统数据目录里，而当前身份默认去找的是自己目录下的那个——所以明明在跑却报「没有运行」。
// 不是这种情形（已经是 root / 管理员、没装成系统级、使用者明确指了别的套接字、本来连的就是系统级套接字）返回空串。
// exists 判断路径是否存在。
func systemHolderHint(env Env, iv invocation, socket string, exists func(string) bool) string {
	if iv.elevated || socket != iv.defaultSocket {
		return ""
	}
	unit, dataDir := systemHolderPaths(iv.goos)
	if dataDir == "" {
		return ""
	}
	if !((unit != "" && exists(unit)) || exists(dataDir)) {
		return ""
	}
	sysSocket := holder.SocketPath(dataDir)
	if socket == sysSocket {
		return ""
	}
	if iv.goos == "windows" {
		return fmt.Sprintf("这台机器上装的是系统级服务：会话守护进程以系统身份运行，套接字在 %s，普通权限的窗口连不上它。"+
			"请「以管理员身份运行」PowerShell 后重新执行同一条命令。", sysSocket)
	}
	return fmt.Sprintf("这台机器上装的是系统级服务：会话守护进程以 root 身份运行，套接字在 %s，普通用户连不上它（默认只会去找自己目录下的套接字）。"+
		"请加 sudo 重新执行，例如：%s\n只想查看时用：%s", sysSocket,
		cmdLine("sudo "+iv.holderCommand(env, "", "", "restart")), cmdLine("sudo "+iv.holderCommand(env, "", "", "status")))
}

var errNoHolderTask = errors.New("没有找到会话守护进程的计划任务")

// kickWindowsHolderTask 在守护进程退出之后立刻启动它的计划任务。
// 计划任务本来也会拉起它（看门狗触发器每 5 分钟一次），但要等；这里不等，让 holder restart 在 Windows 上也是「立刻换上」。
// 管理员先看系统级任务再看用户级；普通用户只看用户级（它连得上的也只有用户级守护进程）。返回被启动的任务名。
func kickWindowsHolderTask(w winOps) (string, error) {
	scopes := []bool{true}
	if w.elevated {
		scopes = []bool{false, true}
	}
	for _, user := range scopes {
		sc, err := w.scopeOf(user)
		if err != nil {
			continue
		}
		if _, err := w.run("schtasks", "/Query", "/TN", sc.holderTaskName); err != nil {
			continue
		}
		if out, err := w.run("schtasks", "/Run", "/TN", sc.holderTaskName); err != nil {
			return sc.holderTaskName, fmt.Errorf("启动计划任务 %s 失败: %v %s", sc.holderTaskName, err, strings.TrimSpace(out))
		}
		return sc.holderTaskName, nil
	}
	return "", errNoHolderTask
}
