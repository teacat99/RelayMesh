package store

import (
	"errors"
	"strings"
	"testing"
)

func TestIsWindowsAbsPath(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`C:\work`, true},
		{`c:\work\项目`, true},
		{`C:/work`, true},
		{`C:\`, true},
		{`D:\Program Files (x86)\x`, true},
		{`\\srv\share`, true},
		{`\\srv\share\dir`, true},
		{`\\srv/share`, true},
		{`\\srv`, false},    // 只有服务器名，没有共享名
		{`\\\share`, false}, // 服务器名为空
		{`C:work`, false},   // 盘符相对路径
		{`C:`, false},       // 没有分隔符
		{`/home/me`, false}, // POSIX
		{`~/work`, false},   // 家目录写法
		{`work\sub`, false}, // 相对路径
		{``, false},
		{`1:\x`, false}, // 盘符必须是字母
		{`CC:\x`, false},
	} {
		if got := IsWindowsAbsPath(c.in); got != c.want {
			t.Errorf("IsWindowsAbsPath(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestWindowsPathProblem(t *testing.T) {
	ok := []string{
		`C:\work`, `C:\Program Files (x86)\Cursor`, `C:\Users\O'Brien\proj`, `C:\用户\项目 一`, `\\srv\share\a b`,
		`C:\a.b\c-d_e+f`, `C:\路径\‘弯引号’`, `D:\#hash\@at\[br]\{cb}`, `C:\a,b;c=d`, `C:\$HOME\x`, "C:\\back`tick",
	}
	for _, p := range ok {
		if got := WindowsPathProblem(p); got != "" {
			t.Errorf("WindowsPathProblem(%q) 应没问题，得到 %q", p, got)
		}
	}
	bad := map[string]string{
		"C:\\a\"b":    "「\"」",
		`C:\100%`:     "「%」",
		`C:\a^b`:      "「^」",
		`C:\R&D`:      "「&」",
		`C:\a|b`:      "「|」",
		`C:\a<b`:      "「<」",
		`C:\a>b`:      "「>」",
		`C:\hello!`:   "「!」",
		`C:\*.txt`:    "「*」",
		`C:\a?b`:      "「?」",
		"C:\\a\nb":    "控制字符",
		"C:\\a\x00b":  "控制字符",
		"C:\\a\tb":    "控制字符",
		`C:\a:b`:      "冒号",
		`\\srv\sh:re`: "冒号",
	}
	for p, want := range bad {
		got := WindowsPathProblem(p)
		if got == "" || !strings.Contains(got, want) {
			t.Errorf("WindowsPathProblem(%q) 应报含 %q，得到 %q", p, want, got)
		}
	}
}

func TestValidHeadlessBin(t *testing.T) {
	valid := []string{
		// 一直以来的 POSIX 写法
		"agent", "cursor-agent", "~/.local/bin/agent", "/usr/local/bin/agent", "./agent", "a.b_c+d-e", "agent.cmd",
		"/bin/bash", "/usr/bin/zsh", // POSIX 路径形式的 shell 名字，一直是放行的（只拒绝光秃秃的 bash / sh）；这次没有收紧它们
		// Windows
		`C:\Users\张三\AppData\Local\cursor-agent\agent.cmd`, `C:\Program Files\Cursor Agent\agent.exe`, `C:/tools/agent.cmd`,
		`\\srv\share\bin\agent.cmd`, `D:\agent`,
	}
	for _, b := range valid {
		if !ValidHeadlessBin(b) {
			t.Errorf("ValidHeadlessBin(%q) 应合法", b)
		}
	}
	invalid := []string{
		"", " ", "agent --x", "agent;rm", "agent&x", "a b", "$(x)", "`x`", "agent\n", "'agent'", `"agent"`,
		// 前缀命令与 shell（POSIX 一直拒绝的）
		"cd", "env", "sudo", "bash", "sh", "exec", "time", "nohup",
		// Windows 的 shell 与子系统入口，不分大小写、带不带 .exe、是不是路径
		"cmd", "CMD", "cmd.exe", "powershell", "PowerShell.exe", "pwsh", "pwsh.exe", "wsl", "wsl.exe",
		`C:\Windows\System32\cmd.exe`, `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, `C:\Program Files\PowerShell\7\pwsh.exe`,
		`C:\Program Files\Git\bin\bash.exe`, `C:\tools\sh.exe`,
		// 路径里有 cmd 语法字符、不是绝对路径、以分隔符结尾、首尾空白
		`C:\R&D\agent.cmd`, `C:\a"b\agent.cmd`, `C:\100%\agent.cmd`, `C:\a^b\agent.cmd`, `C:\a|b\agent.cmd`, `C:\hello!\agent.cmd`,
		`C:\bin\`, `C:/bin/`, ` C:\bin\agent.cmd`, "C:\\bin\\agent.cmd ", `bin\agent.cmd`, `C:agent.cmd`, `\\srv`,
	}
	for _, b := range invalid {
		if ValidHeadlessBin(b) {
			t.Errorf("ValidHeadlessBin(%q) 应不合法", b)
		}
	}
}

func TestValidWindowsBin(t *testing.T) {
	for _, b := range []string{"agent", "agent.cmd", "cursor-agent", `C:\x\agent.cmd`, `C:\Program Files\x y\agent.cmd`, `\\h\s\agent.cmd`} {
		if !ValidWindowsBin(b) {
			t.Errorf("ValidWindowsBin(%q) 应合法", b)
		}
	}
	// POSIX 写法在 Windows 主机上不能用：没有 ~ 与 / 开头的路径、没有 ./ 相对写法。
	for _, b := range []string{"~/.local/bin/agent", "/usr/bin/agent", "./agent", "../agent", "a/b", `a\b`, "", "cmd", "time", "cd", "set"} {
		if ValidWindowsBin(b) {
			t.Errorf("ValidWindowsBin(%q) 应不合法", b)
		}
	}
}

// 预设保存：目录与可执行文件按「POSIX 或 Windows 都接受」规整；不合法的 Windows 写法要给出面向用户的原因。
func TestHeadlessLauncherWindowsFields(t *testing.T) {
	mutate := func(f func(l *TerminalLauncher)) TerminalLauncher {
		l := okHeadless("h-win", "续聊")
		run := *l.Run
		l.Run = &run
		f(&l)
		return l
	}
	cases := []struct {
		name string
		l    TerminalLauncher
		want string // 错误信息应含的片段；空 = 应合法
	}{
		{"Windows 目录合法", mutate(func(l *TerminalLauncher) { l.Dir = `C:\work\项目 一` }), ""},
		{"Windows 目录（正斜杠）合法", mutate(func(l *TerminalLauncher) { l.Dir = `C:/work/proj` }), ""},
		{"UNC 目录合法", mutate(func(l *TerminalLauncher) { l.Dir = `\\srv\share\proj` }), ""},
		{"Windows 可执行文件路径合法", mutate(func(l *TerminalLauncher) {
			l.Dir, l.Run.Bin = `C:\work`, `C:\Users\me\AppData\Local\cursor-agent\agent.cmd`
		}), ""},
		{"Windows 命令名合法", mutate(func(l *TerminalLauncher) { l.Dir, l.Run.Bin = `C:\work`, "agent.cmd" }), ""},
		{"Windows 目录有 &", mutate(func(l *TerminalLauncher) { l.Dir = `C:\R&D` }), "「&」"},
		{"Windows 目录有引号", mutate(func(l *TerminalLauncher) { l.Dir = "C:\\a\"b" }), "「\"」"},
		{"Windows 目录有 %", mutate(func(l *TerminalLauncher) { l.Dir = `C:\100%` }), "「%」"},
		{"盘符相对路径不算绝对", mutate(func(l *TerminalLauncher) { l.Dir = `C:work` }), "完整路径"},
		{"相对路径", mutate(func(l *TerminalLauncher) { l.Dir = `work\sub` }), "完整路径"},
		{"UNC 路径缺共享名", mutate(func(l *TerminalLauncher) { l.Dir = `\\srv` }), "绝对路径"},
		{"Windows 可执行文件路径有 &", mutate(func(l *TerminalLauncher) { l.Run.Bin = `C:\R&D\agent.cmd` }), "可执行文件"},
		{"可执行文件是 cmd", mutate(func(l *TerminalLauncher) { l.Run.Bin = "cmd.exe" }), "shell"},
		{"可执行文件是 PowerShell 路径", mutate(func(l *TerminalLauncher) { l.Run.Bin = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` }), "shell"},
	}
	for _, c := range cases {
		err := c.l.Normalize().Validate()
		if c.want == "" {
			if err != nil {
				t.Errorf("%s：应合法，得到 %v", c.name, err)
			}
			continue
		}
		if err == nil || !errors.Is(err, ErrInvalidLauncher) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：应拒绝且信息含 %q，得到 %v", c.name, c.want, err)
		}
	}
}
