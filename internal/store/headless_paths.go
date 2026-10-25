package store

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Windows 主机上的非交互续聊：目录与可执行文件的写法（#178，Q106 方案 B）。
//
// 预设保存时 Hub 不一定知道目标主机的系统（预设可以先建后改主机），所以保存时 POSIX 与 Windows 两种写法都接受，
// 运行前再按主机系统逐项核对（internal/terminal 的 headlessCmd）。
//
// Windows 上命令经 PowerShell 拼出，而 agent 往往是一个 agent.cmd 批处理垫片：PowerShell 把参数交给 cmd.exe 时，
// 双引号、% ^ & | < > ! 会被当成 cmd 自己的语法（引号错位就能夹带命令），所以目录与可执行文件的路径里一律不许出现它们；
// 提示词里的这些字符不拒绝、而是换成全角（见 internal/terminal/headless_powershell.go），免得一个符号就让运行失败。

var (
	// winDriveRe 是盘符绝对路径（C:\… 或 C:/…）的开头；winUNCRe 是 UNC 路径（\\服务器\共享\…）的开头。
	winDriveRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	winUNCRe   = regexp.MustCompile(`^\\\\[^\\/]+[\\/]+[^\\/]+`)
	// winBareBinRe 是 Windows 上「命令名」写法的可执行文件（agent、agent.cmd、cursor-agent）：没有路径分隔符、空白与 ~，不以点开头。
	winBareBinRe = regexp.MustCompile(`^[A-Za-z0-9_+-][A-Za-z0-9_.+-]*$`)
)

// WindowsCmdHazards 是 cmd.exe 会当成语法的字符：它们不能出现在 Windows 的目录与可执行文件路径里。
const WindowsCmdHazards = "\"%^&|<>!"

// windowsPathBad 是 Windows 路径里再不许出现的字符：上面那些，加上文件名里本来就非法的通配符。
const windowsPathBad = WindowsCmdHazards + "*?"

// IsWindowsAbsPath 判断一个路径是不是 Windows 的绝对路径：盘符（C:\work、C:/work）或 UNC（\\服务器\共享\…）。
func IsWindowsAbsPath(p string) bool {
	return winDriveRe.MatchString(p) || winUNCRe.MatchString(p)
}

// WindowsPathProblem 检查一个 Windows 路径里有没有不该出现的字符；没问题返回空串，有就返回一句话（可直接接在「目录」「可执行文件」后面）。
// 它不判断是不是绝对路径（见 IsWindowsAbsPath）。
func WindowsPathProblem(p string) string {
	for i, r := range p {
		switch {
		case unicode.IsControl(r):
			return "里不能有换行或控制字符"
		case strings.ContainsRune(windowsPathBad, r):
			return fmt.Sprintf("里不能有「%c」：这些字符（%s）会被 cmd.exe 当成命令语法，或者路径里本来就不允许", r, windowsPathBadList())
		case r == ':' && i != 1:
			return "里的冒号只能出现在盘符后面（如 C:\\work）"
		}
	}
	return ""
}

// windowsPathBadList 把不许出现的字符列成「" % ^ & | < > ! * ?」这样的一行，给错误信息用。
func windowsPathBadList() string {
	parts := make([]string, 0, len(windowsPathBad))
	for _, r := range windowsPathBad {
		parts = append(parts, string(r))
	}
	return strings.Join(parts, " ")
}

// windowsShellNames 是 Windows 上不能当作「可执行文件」的 shell 与子系统入口（不分大小写，可带 .exe）：
// 非交互续聊要的是 agent 本身，不是一个能再解释命令的外壳。
var windowsShellNames = map[string]bool{
	"cmd": true, "powershell": true, "pwsh": true, "wsl": true,
	"bash": true, "sh": true, "zsh": true, "fish": true,
}

// windowsShellBase 取可执行文件（命令名或路径）的最后一段，去掉 .exe，转小写。
func windowsShellBase(bin string) string {
	base := bin
	if i := strings.LastIndexAny(base, `\/`); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(strings.ToLower(base), ".exe")
}

// isWindowsShellBin 判断一个 Windows 可执行文件（命令名或路径）指的是不是 shell。
func isWindowsShellBin(bin string) bool { return windowsShellNames[windowsShellBase(bin)] }

// isWindowsOnlyShellBin 判断是不是 Windows 才有的 shell 入口（cmd / powershell / pwsh / wsl）。预设保存时不知道主机系统，
// 连 POSIX 写法的 cmd、pwsh 也一并拒绝（本来就不会是 agent）；bash、sh 这类 POSIX shell 在 POSIX 写法里仍按一直以来的规则只拒绝光秃秃的命令名。
func isWindowsOnlyShellBin(bin string) bool {
	switch windowsShellBase(bin) {
	case "cmd", "powershell", "pwsh", "wsl":
		return true
	}
	return false
}

// ValidWindowsBin 判断一个可执行文件的写法能不能用在 Windows 主机上：命令名（agent、agent.cmd），或盘符 / UNC 绝对路径
// （路径里可以有空格，如 C:\Users\张三\AppData\Local\cursor-agent\agent.cmd）。不能是 shell，路径里没有 cmd 语法字符。
func ValidWindowsBin(bin string) bool {
	if bin == "" || bin != strings.TrimSpace(bin) || isWindowsShellBin(bin) {
		return false
	}
	if IsWindowsAbsPath(bin) {
		return WindowsPathProblem(bin) == "" && !strings.HasSuffix(bin, `\`) && !strings.HasSuffix(bin, "/")
	}
	return winBareBinRe.MatchString(bin) && !IsHeadlessPrefixCmd(bin)
}

// ValidHeadlessBin 判断可执行文件的写法是否合法，不分主机系统（预设保存时用）：POSIX 的命令名或路径（一直以来的规则），
// 或 Windows 的命令名、盘符 / UNC 路径。不管哪种，都不能是 cd、env、sudo、bash 这类前缀命令，也不能是 cmd / powershell 这类 shell。
func ValidHeadlessBin(bin string) bool {
	if headlessBinRe.MatchString(bin) {
		return !IsHeadlessPrefixCmd(bin) && !isWindowsOnlyShellBin(bin)
	}
	return ValidWindowsBin(bin)
}
