package terminal

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf16"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// Windows 主机上的非交互运行（#178，Q106 方案 B）。
//
// 命令走和 Linux / macOS 同一条通道（ExecMesh 的运行），只是命令改成 PowerShell 的写法，并明确让 Agent 用 PowerShell 执行
// （Agent 在 Windows 上本来就用 -EncodedCommand 传脚本，并统一了 UTF-8 输出与退出码，见 internal/agent/holder/runshell_windows.go）：
//
//	& '<agent>' -p [--resume <对话编号> | --continue] --approve-mcps --trust [--model '<模型>'] [--force] --workspace '<目录>' --output-format stream-json '<提示词>'
//
// 参数全部放进 PowerShell 的单引号字面量：里面没有变量展开、转义和子表达式，唯一要处理的是单引号本身（psQuote）。
//
// 这里多出来一类 POSIX 上没有的风险：Cursor 的命令行程序在 Windows 上通常是 agent.cmd 这样的批处理垫片，
// PowerShell 调用 .cmd 时会经过 cmd.exe，而 PowerShell 不会转义参数里自带的双引号——
// 于是参数里的 " % ^ & | < > ! 会被 cmd.exe 当成它自己的语法：引号错位后 & 跑到引号外面，cmd.exe 就会执行第二条命令；%变量% 会被展开。
// 这两点在真实的 Windows 主机上实测过（Windows PowerShell 5.1.26100 与 PowerShell 7.6.6 各一台，脚本 scripts/e2e-terminal/winhost/headless-check.mjs，结果见 docs §8.10）：
// 夹着 " 与 & 的参数真的夹带出了第二条命令，%变量% 真的被展开（两个版本都是）；而没有空白、含 & 与 > 的参数，这两个版本都会自己包上双引号，没有出事——
// 是否包引号随 PowerShell 版本而异（更老的版本没有实测），所以不赌它，一律处理。
// 所以：目录与可执行文件路径里一律不许出现这些字符（校验见 store.WindowsPathProblem，保存与运行时各查一遍）；
// 提示词里的这些字符不拒绝（常驻规则、检查点文字里常有），而是换成长得一样的全角字符 / 单引号（winNeutralize），预览里会说明。

// isWindowsOS 判断主机自报的系统是不是 Windows（Agent 握手时自报，Windows 是 windows）。
func isWindowsOS(os string) bool { return strings.Contains(strings.ToLower(os), "windows") }

// psQuote 把任意文本变成 PowerShell 里的一个单引号字面量。除了 ASCII 的 '，排版用的弯引号（‘ ’ ‚ ‛）也会被 PowerShell 当成单引号，
// 只翻倍 ' 会让字符串提前结束，所以四种弯引号同样翻倍（与前端 lib/terminal/launcher.ts 的 psQuote 同一条规则，已在 PowerShell 7 与 Windows PowerShell 5.1 上实测）。
func psQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for _, r := range s {
		b.WriteRune(r)
		switch r {
		case '\'', '\u2018', '\u2019', '\u201a', '\u201b':
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// winSymbols 把提示词里 cmd.exe 会当成语法的符号换成对应的全角字符（看起来一样、cmd 不认）。双引号不在这里：它换成单引号（见 winNeutralize）。
var winSymbols = strings.NewReplacer(
	"%", "\uff05", "^", "\uff3e", "&", "\uff06", "|", "\uff5c", "<", "\uff1c", ">", "\uff1e", "!", "\uff01",
)

// winSymbolChars 是 winSymbols 处理的符号，给提示文案用。
const winSymbolChars = "% ^ & | < > !"

// winNeutralize 把提示词变成可以安全交给 cmd.exe 的样子：双引号换成单引号（提示词里的 "workflow_id" 变成 'workflow_id'，智能体照样读得懂，
// 而全角引号会被它原样抄进工具参数里），% ^ & | < > ! 换成全角；末尾的反斜杠后面补一个空格（参数以反斜杠结尾时，它会和 PowerShell 补上的收尾引号一起被当成转义）。
// 返回处理后的文本，和被换成全角的符号个数（只数 % ^ & | < > !；引号换成单引号是常态，不算，也不提示）。
func winNeutralize(s string) (string, int) {
	n := 0
	for _, r := range s {
		if strings.ContainsRune("%^&|<>!", r) {
			n++
		}
	}
	out := winSymbols.Replace(strings.ReplaceAll(s, `"`, "'"))
	if strings.HasSuffix(out, `\`) {
		out += " "
	}
	return out, n
}

// winPromptNote 是提示词被换了符号时给预览与运行回执的一句说明；没有被换就返回空串。
func winPromptNote(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("提示词里有 %d 个符号（%s）在 Windows 上会被 cmd.exe 当成命令语法，已换成全角形式（如 ％）；双引号会换成单引号。内容不变，只是字形不同", n, winSymbolChars)
}

// winCmdLineMax 是 Windows 上整条命令的长度上限（UTF-16 单元数）。cmd.exe 的一行命令最多 8191 个字符；实测（Windows 11 的 cmd.exe、假的 agent.cmd）
// 超出时不是悄悄截断，而是直接失败：退出码 1，cmd.exe 报「命令行太长」或「命令语法不正确」。
// 要算的不只是我们拼的这一行：agent.cmd 自己转交参数的那一行（可执行文件路径加选项，假垫片的那一行约 205 个字符）加上全部参数也不能超过 8191——
// 给 cmd.exe 的命令行 7983 个字符能跑，8083 个字符就失败，远没到 8191。真垫片那一行多长我们看不到，所以上限留出一千多字符的余量，
// 超过就在 Hub 这边明说，不等到主机上才报一句莫名其妙的错。ExecMesh 的命令上限是 24 KiB（字节）：中文每字 3 字节，7000 个中文字符约 21 KB，也在它之内。
// 改这个值之前先看 docs/terminal-operations.md §8.10 的实测记录（TestWindowsCmdLineMaxIsMeasured 会提醒）。
const winCmdLineMax = 7000

// winTrimDir 去掉目录末尾多余的分隔符（C:\work\ → C:\work）；盘符根目录（C:\）保持原样。
// 末尾的反斜杠和后面的收尾引号会被当成转义（"C:\my dir\"），所以带空格的目录不能以它结尾。
func winTrimDir(dir string) string {
	d := dir
	for len(d) > 3 && (strings.HasSuffix(d, `\`) || strings.HasSuffix(d, "/")) {
		d = d[:len(d)-1]
	}
	return d
}

// headlessDirOK 按主机系统检查工作目录的写法（Windows 主机要盘符 / UNC 路径，其余主机要 / 开头的路径），不合格返回面向用户的原因。
func headlessDirOK(dir string, windows bool) error {
	if !windows {
		if !strings.HasPrefix(dir, "/") || strings.ContainsAny(dir, "\x00\r\n") {
			return errors.New("工作目录必须是主机上的绝对路径")
		}
		return nil
	}
	if !store.IsWindowsAbsPath(dir) {
		return errors.New("工作目录必须是 Windows 主机上的绝对路径（盘符路径如 C:\\work，或 \\\\服务器\\共享\\… 形式）")
	}
	if why := store.WindowsPathProblem(dir); why != "" {
		return errors.New("工作目录" + why)
	}
	return nil
}

// headlessDirShapeOK 在还不知道主机系统时检查工作目录的写法：POSIX 的绝对路径（/ 开头）或 Windows 的盘符 / UNC 路径都行，
// 含 NUL、换行的不行。知道主机系统之后再用 headlessDirOK 精确核对。
func headlessDirShapeOK(dir string) bool {
	return !strings.ContainsAny(dir, "\x00\r\n") && (strings.HasPrefix(dir, "/") || store.IsWindowsAbsPath(dir))
}

// winPathParts 把 Windows 绝对路径拆成「根」与其后的各级目录名：/ 当 \ 用，重复的分隔符合并，. 与 .. 已处理。
// 根是盘符（C:）或 UNC 的「\\服务器\共享」；不是绝对路径返回 ok=false。
func winPathParts(p string) (root string, rest []string, ok bool) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "/", `\`)
	if !store.IsWindowsAbsPath(p) {
		return "", nil, false
	}
	elems := strings.FieldsFunc(p, func(r rune) bool { return r == '\\' })
	if strings.HasPrefix(p, `\\`) {
		if len(elems) < 2 {
			return "", nil, false
		}
		root, elems = `\\`+elems[0]+`\`+elems[1], elems[2:]
	} else {
		root, elems = elems[0], elems[1:]
	}
	for _, e := range elems {
		switch e {
		case ".":
		case "..":
			if len(rest) > 0 {
				rest = rest[:len(rest)-1]
			}
		default:
			rest = append(rest, e)
		}
	}
	return root, rest, true
}

// cleanHeadlessDir 把主机上的绝对路径规整成可比较、可展示的写法：POSIX 路径用 path.Clean；Windows 路径统一用反斜杠，
// 合并重复分隔符、处理 . 与 ..（盘符根写成 C:\，UNC 根写成 \\服务器\共享）。不是绝对路径返回空串。
func cleanHeadlessDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if strings.HasPrefix(dir, "/") {
		return path.Clean(dir)
	}
	root, rest, ok := winPathParts(dir)
	if !ok {
		return ""
	}
	if strings.HasPrefix(root, `\\`) {
		return strings.Join(append([]string{root}, rest...), `\`)
	}
	return root + `\` + strings.Join(rest, `\`)
}

// buildWindows 拼出 Windows 主机上非交互运行的一整行 PowerShell 命令（headlessCmd.Windows 为真时 build 走这里）。
// 与 POSIX 的拼法逐项对应：同样的互斥检查、同样的校验顺序与提示，只有可执行文件、目录的写法与引号规则不同。
func (c headlessCmd) buildWindows() (string, error) {
	derr := headlessDirOK(c.Dir, true)
	switch {
	case !store.ValidWindowsBin(c.Bin):
		return "", errors.New("可执行文件名不合法：Windows 主机上请写命令名（agent）或盘符路径（C:\\Users\\你\\AppData\\Local\\cursor-agent\\agent.cmd），路径里不能有 \" % ^ & | < > ! * ?，也不能是 cmd、powershell 这类 shell")
	case c.New && c.Continue:
		return "", errors.New("新开对话与接着最近的对话只能选一个")
	case (c.New || c.Continue) && c.ChatID != "":
		if c.Continue {
			return "", errors.New("接着最近的对话不能同时指定对话编号")
		}
		return "", errors.New("新开对话不能同时指定对话编号")
	case !c.New && !c.Continue && !headlessChatRe.MatchString(c.ChatID):
		return "", errors.New("对话编号不合法")
	case derr != nil:
		return "", derr
	case c.Model != "" && !headlessModelRe.MatchString(c.Model):
		return "", errors.New("模型名不合法")
	case strings.TrimSpace(c.Prompt) == "" || strings.ContainsAny(c.Prompt, "\x00\r\n"):
		return "", errors.New("唤醒词为空或含换行")
	}
	prompt, _ := winNeutralize(c.Prompt)
	parts := []string{"&", psQuote(c.Bin), "-p"}
	switch {
	case c.Continue:
		parts = append(parts, "--continue")
	case !c.New:
		parts = append(parts, "--resume", c.ChatID)
	}
	parts = append(parts, "--approve-mcps", "--trust")
	if c.Model != "" {
		parts = append(parts, "--model", psQuote(c.Model))
	}
	if c.Posture == store.HeadlessPostureForce {
		parts = append(parts, "--force")
	}
	parts = append(parts, "--workspace", psQuote(winTrimDir(c.Dir)), "--output-format", "stream-json", psQuote(prompt))
	cmd := strings.Join(parts, " ")
	if n := len(utf16.Encode([]rune(cmd))); n > winCmdLineMax {
		return "", fmt.Errorf("命令太长（%d 个字符）：Windows 上一行命令不能超过 %d 个字符（cmd.exe 一行最多 8191 个字符，连 agent.cmd 自己转交参数的那一行也算在里面，超出会直接失败）。请缩短预设的提示词，或把常驻规则、检查点的下一步写短一些", n, winCmdLineMax)
	}
	return cmd, nil
}

// windowsShell 取 Windows 主机上运行非交互命令要用的 shell：守护进程上报了 pwsh / powershell 就用它；
// 上报的是 cmd（机器上既没有 pwsh 也没有 powershell）不能用——命令是 PowerShell 写法；没上报（旧版守护进程、Agent 不在线）按 powershell 算
// （Windows 自带；真缺的话运行时会失败并写明原因）。agent 为 nil 表示主机离线，预览时同样按 powershell。
func windowsShell(a *agentConn) (string, error) {
	if a == nil {
		return "powershell", nil
	}
	_, _, info := a.execSupport()
	if info == nil {
		return "powershell", nil
	}
	switch sh := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(info.Shell)), ".exe"); sh {
	case "pwsh", "powershell":
		return sh, nil
	case "cmd":
		return "", errors.New("这台 Windows 主机上没有找到 PowerShell（Agent 只找到了 cmd），非交互运行需要 PowerShell：请安装 PowerShell 7（pwsh）后重启 Agent 的会话守护进程")
	default:
		return "powershell", nil
	}
}
