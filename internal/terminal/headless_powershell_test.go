package terminal

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// ---------- psQuote ----------

func TestPsQuoteMatrix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"plain", "'plain'"},
		{"it's", "'it''s'"},
		{"''", "''''''"}, // 两个单引号 → 各自翻倍，外面再包一层
		{"John’s", "'John’’s'"},
		{"‘a’", "'‘‘a’’'"},
		{"‚x‛", "'‚‚x‛‛'"},
		{"$HOME $(calc) `x`", "'$HOME $(calc) `x`'"},
		{"a;b&c|d>e<f", "'a;b&c|d>e<f'"},
		{`C:\Program Files\x`, `'C:\Program Files\x'`},
		{"中文 路径", "'中文 路径'"},
		{"#hash @at [br] {cb} %pct ^caret !bang", "'#hash @at [br] {cb} %pct ^caret !bang'"},
		{`say "hi"`, `'say "hi"'`}, // 双引号在 PowerShell 单引号字面量里是普通字符
	}
	for _, c := range cases {
		if got := psQuote(c.in); got != c.want {
			t.Errorf("psQuote(%q) = %s，期望 %s", c.in, got, c.want)
		}
	}
}

// ---------- winNeutralize ----------

func TestWinNeutralize(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		count int
	}{
		{"普通中文提示词 abc 123", "普通中文提示词 abc 123", 0},
		{`workflow_id "relaymesh-continuation"`, "workflow_id 'relaymesh-continuation'", 0}, // 双引号换成单引号，不算被换的符号
		{"100%", "100％", 1},
		{"a^b", "a＾b", 1},
		{"R&D", "R＆D", 1},
		{"a|b", "a｜b", 1},
		{"<服务器名>", "＜服务器名＞", 2},
		{"好!", "好！", 1},
		{`%PATH% & "x" | y > z < w ^ v !`, `％PATH％ ＆ 'x' ｜ y ＞ z ＜ w ＾ v ！`, 8},
		{`ends with backslash C:\dir\`, `ends with backslash C:\dir\ `, 0}, // 末尾反斜杠后补一个空格
		{`only\`, `only\ `, 0},
		{`mid\dle`, `mid\dle`, 0},
		{"全角已经是全角：％＆｜", "全角已经是全角：％＆｜", 0},
		{"$HOME `x` ; # ( ) { } [ ] ~ * ?", "$HOME `x` ; # ( ) { } [ ] ~ * ?", 0},
	}
	for _, c := range cases {
		got, n := winNeutralize(c.in)
		if got != c.want || n != c.count {
			t.Errorf("winNeutralize(%q) = (%q, %d)，期望 (%q, %d)", c.in, got, n, c.want, c.count)
		}
		// 处理后的文本里不能再有 cmd 会当成语法的字符。
		if strings.ContainsAny(got, "\"%^&|<>!") {
			t.Errorf("winNeutralize(%q) 的结果里还有 cmd 语法字符: %q", c.in, got)
		}
	}
	if winPromptNote(0) != "" || !strings.Contains(winPromptNote(3), "3 个符号") {
		t.Errorf("winPromptNote 文案不对: %q / %q", winPromptNote(0), winPromptNote(3))
	}
}

// ---------- buildWindows ----------

func winCmd(mut func(c *headlessCmd)) headlessCmd {
	c := headlessCmd{Windows: true, Bin: "agent", ChatID: "chat-9", Dir: `C:\work\app`, Prompt: "接着干"}
	if mut != nil {
		mut(&c)
	}
	return c
}

func TestBuildWindowsExactCommands(t *testing.T) {
	cases := []struct {
		name string
		c    headlessCmd
		want string
	}{
		{"续聊·放开全部命令·带模型",
			winCmd(func(c *headlessCmd) { c.Posture, c.Model = store.HeadlessPostureForce, "m-1" }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --model 'm-1' --force --workspace 'C:\work\app' --output-format stream-json '接着干'`},
		{"续聊·白名单·不带模型",
			winCmd(nil),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\work\app' --output-format stream-json '接着干'`},
		{"新会话",
			winCmd(func(c *headlessCmd) { c.ChatID, c.New = "", true }),
			`& 'agent' -p --approve-mcps --trust --workspace 'C:\work\app' --output-format stream-json '接着干'`},
		{"最近会话",
			winCmd(func(c *headlessCmd) { c.ChatID, c.Continue = "", true }),
			`& 'agent' -p --continue --approve-mcps --trust --workspace 'C:\work\app' --output-format stream-json '接着干'`},
		{"可执行文件是带空格的盘符路径",
			winCmd(func(c *headlessCmd) { c.Bin = `C:\Users\张三\AppData\Local\Cursor Agent\agent.cmd` }),
			`& 'C:\Users\张三\AppData\Local\Cursor Agent\agent.cmd' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\work\app' --output-format stream-json '接着干'`},
		{"目录里有单引号",
			winCmd(func(c *headlessCmd) { c.Dir = `C:\Users\O'Brien\proj` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\Users\O''Brien\proj' --output-format stream-json '接着干'`},
		{"目录里有弯引号",
			winCmd(func(c *headlessCmd) { c.Dir = `C:\Users\John’s\proj` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\Users\John’’s\proj' --output-format stream-json '接着干'`},
		{"目录有空格与中文",
			winCmd(func(c *headlessCmd) { c.Dir = `D:\我的 项目\RelayMesh (副本)` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'D:\我的 项目\RelayMesh (副本)' --output-format stream-json '接着干'`},
		{"目录末尾的反斜杠被去掉",
			winCmd(func(c *headlessCmd) { c.Dir = `C:\my dir\` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\my dir' --output-format stream-json '接着干'`},
		{"盘符根目录保持原样",
			winCmd(func(c *headlessCmd) { c.Dir = `C:\` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\' --output-format stream-json '接着干'`},
		{"UNC 目录",
			winCmd(func(c *headlessCmd) { c.Dir = `\\srv\share\proj\` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace '\\srv\share\proj' --output-format stream-json '接着干'`},
		{"正斜杠目录原样（Windows 认两种分隔符）",
			winCmd(func(c *headlessCmd) { c.Dir = `C:/work/app` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:/work/app' --output-format stream-json '接着干'`},
		{"提示词：单引号翻倍，$ 反引号 ; # 原样",
			winCmd(func(c *headlessCmd) { c.Prompt = "it's $HOME `x`; # 注释" }),
			"& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\\work\\app' --output-format stream-json 'it''s $HOME `x`; # 注释'"},
		{"提示词：cmd 语法字符换成全角，双引号换成单引号",
			winCmd(func(c *headlessCmd) { c.Prompt = `a"b%c^d&e|f<g>h!i` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\work\app' --output-format stream-json 'a''b％c＾d＆e｜f＜g＞h！i'`},
		{"提示词：末尾反斜杠后补空格",
			winCmd(func(c *headlessCmd) { c.Prompt = `见 C:\logs\` }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\work\app' --output-format stream-json '见 C:\logs\ '`},
		{"提示词：弯引号翻倍",
			winCmd(func(c *headlessCmd) { c.Prompt = "‘引用’" }),
			`& 'agent' -p --resume chat-9 --approve-mcps --trust --workspace 'C:\work\app' --output-format stream-json '‘‘引用’’'`},
	}
	for _, c := range cases {
		got, err := c.c.build()
		if err != nil {
			t.Errorf("%s：不该报错: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s：\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestBuildWindowsRejects(t *testing.T) {
	cases := []struct {
		name string
		c    headlessCmd
		want string
	}{
		{"可执行文件是 cmd", winCmd(func(c *headlessCmd) { c.Bin = "cmd" }), "可执行文件名不合法"},
		{"可执行文件是 powershell.exe 路径", winCmd(func(c *headlessCmd) { c.Bin = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` }), "可执行文件名不合法"},
		{"可执行文件路径有 &", winCmd(func(c *headlessCmd) { c.Bin = `C:\R&D\agent.cmd` }), "可执行文件名不合法"},
		{"可执行文件是 POSIX 路径", winCmd(func(c *headlessCmd) { c.Bin = "/usr/bin/agent" }), "可执行文件名不合法"},
		{"可执行文件是 ~ 路径", winCmd(func(c *headlessCmd) { c.Bin = "~/.local/bin/agent" }), "可执行文件名不合法"},
		{"可执行文件是前缀命令", winCmd(func(c *headlessCmd) { c.Bin = "set" }), "可执行文件名不合法"},
		{"目录是 POSIX 路径", winCmd(func(c *headlessCmd) { c.Dir = "/work/app" }), "Windows 主机上的绝对路径"},
		{"目录是相对路径", winCmd(func(c *headlessCmd) { c.Dir = `work\app` }), "Windows 主机上的绝对路径"},
		{"目录是盘符相对路径", winCmd(func(c *headlessCmd) { c.Dir = `C:work` }), "Windows 主机上的绝对路径"},
		{"目录有 &", winCmd(func(c *headlessCmd) { c.Dir = `C:\R&D` }), "工作目录里不能有「&」"},
		{"目录有引号", winCmd(func(c *headlessCmd) { c.Dir = "C:\\a\"b" }), "工作目录里不能有"},
		{"目录有 %", winCmd(func(c *headlessCmd) { c.Dir = `C:\%TEMP%` }), "工作目录里不能有「%」"},
		{"目录有换行", winCmd(func(c *headlessCmd) { c.Dir = "C:\\a\nb" }), "工作目录里不能有换行"},
		{"目录有 NUL", winCmd(func(c *headlessCmd) { c.Dir = "C:\\a\x00b" }), "工作目录里不能有换行"},
		{"对话编号不合法", winCmd(func(c *headlessCmd) { c.ChatID = "bad id" }), "对话编号不合法"},
		{"对话编号注入", winCmd(func(c *headlessCmd) { c.ChatID = "c1'; calc; '" }), "对话编号不合法"},
		{"模型名注入", winCmd(func(c *headlessCmd) { c.Model = "x'; calc; '" }), "模型名不合法"},
		{"提示词为空", winCmd(func(c *headlessCmd) { c.Prompt = "  " }), "唤醒词为空或含换行"},
		{"提示词有换行", winCmd(func(c *headlessCmd) { c.Prompt = "a\nb" }), "唤醒词为空或含换行"},
		{"提示词有回车", winCmd(func(c *headlessCmd) { c.Prompt = "a\rb" }), "唤醒词为空或含换行"},
		{"提示词有 NUL", winCmd(func(c *headlessCmd) { c.Prompt = "a\x00b" }), "唤醒词为空或含换行"},
		{"新会话又给了对话编号", winCmd(func(c *headlessCmd) { c.New = true }), "新开对话不能同时指定对话编号"},
		{"最近会话又给了对话编号", winCmd(func(c *headlessCmd) { c.Continue = true }), "接着最近的对话不能同时指定对话编号"},
		{"新会话与最近会话同时选", winCmd(func(c *headlessCmd) { c.ChatID, c.New, c.Continue = "", true, true }), "只能选一个"},
	}
	for _, c := range cases {
		got, err := c.c.build()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：应拒绝且信息含 %q，得到 (%q, %v)", c.name, c.want, got, err)
		}
		if got != "" {
			t.Errorf("%s：拒绝时不该返回命令: %q", c.name, got)
		}
	}
}

// 提示词再长也不会绕过 cmd.exe 的一行 8191 字符上限：太长就在 Hub 这边明说，不等到主机上才报「命令行太长」（实测超出是直接失败，不是悄悄截断）。
func TestBuildWindowsRejectsTooLongCommand(t *testing.T) {
	long := winCmd(func(c *headlessCmd) { c.Prompt = strings.Repeat("字", 8000) })
	if got, err := long.build(); err == nil || !strings.Contains(err.Error(), "命令太长") {
		t.Fatalf("超长命令应被拒绝: (%d 字节, %v)", len(got), err)
	}
	ok := winCmd(func(c *headlessCmd) { c.Prompt = strings.Repeat("字", 3000) })
	got, err := ok.build()
	if err != nil {
		t.Fatalf("3000 字的提示词应放行: %v", err)
	}
	if n := len(utf16.Encode([]rune(got))); n > winCmdLineMax {
		t.Fatalf("放行的命令不该超过上限: %d", n)
	}
	// 一个 emoji 占两个 UTF-16 单元：按单元数而不是字符数算。
	emoji := winCmd(func(c *headlessCmd) { c.Prompt = strings.Repeat("😀", 4000) })
	if _, err := emoji.build(); err == nil || !strings.Contains(err.Error(), "命令太长") {
		t.Fatalf("4000 个 emoji（8000 个 UTF-16 单元）应被拒绝: %v", err)
	}
}

// 上限值是在真实 Windows 主机上实测之后定的（docs/terminal-operations.md §8.10）：改它之前先看那里的数据，不要凭感觉调。
// 同时钉住边界：恰好等于上限的放行，多一个字符就拒绝，错误信息里带着上限值。
func TestWindowsCmdLineMaxIsMeasured(t *testing.T) {
	if winCmdLineMax != 7000 {
		t.Fatalf("winCmdLineMax = %d：这个值来自 #244 的实机核对（假垫片转交参数的那一行约 205 个字符，给 cmd.exe 的命令行 7983 个字符能跑、8083 个字符就失败），要改请先更新 docs §8.10 的记录与这条测试", winCmdLineMax)
	}
	base, err := winCmd(func(c *headlessCmd) { c.Prompt = "x" }).build()
	if err != nil {
		t.Fatal(err)
	}
	k := winCmdLineMax - len(utf16.Encode([]rune(base))) + 1 // 提示词长 k 个 ASCII 字符时整条命令恰好等于上限
	exact, err := winCmd(func(c *headlessCmd) { c.Prompt = strings.Repeat("a", k) }).build()
	if err != nil {
		t.Fatalf("恰好等于上限的命令应放行: %v", err)
	}
	if n := len(utf16.Encode([]rune(exact))); n != winCmdLineMax {
		t.Fatalf("构造的命令长度 %d，期望恰好 %d", n, winCmdLineMax)
	}
	_, err = winCmd(func(c *headlessCmd) { c.Prompt = strings.Repeat("a", k+1) }).build()
	if err == nil || !strings.Contains(err.Error(), "命令太长") || !strings.Contains(err.Error(), "7000") {
		t.Fatalf("多一个字符应被拒绝，且信息里写明上限 7000: %v", err)
	}
}

// 两种平台共有的检查（互斥、编号、模型、提示词）必须同样的信息：Windows 的拼装是照着 POSIX 的抄的，这条守住它们不漂移。
func TestBuildErrorsAreSameOnBothPlatforms(t *testing.T) {
	shared := []func(c *headlessCmd){
		func(c *headlessCmd) { c.ChatID, c.New, c.Continue = "", true, true },
		func(c *headlessCmd) { c.New = true },
		func(c *headlessCmd) { c.Continue = true },
		func(c *headlessCmd) { c.ChatID = "bad id" },
		func(c *headlessCmd) { c.ChatID = "" },
		func(c *headlessCmd) { c.Model = "bad model" },
		func(c *headlessCmd) { c.Prompt = "" },
		func(c *headlessCmd) { c.Prompt = "a\nb" },
	}
	for i, mut := range shared {
		posix := headlessCmd{Bin: "agent", ChatID: "chat-9", Dir: "/work/app", Prompt: "x"}
		mut(&posix)
		win := headlessCmd{Windows: true, Bin: "agent", ChatID: "chat-9", Dir: `C:\work\app`, Prompt: "x"}
		mut(&win)
		_, pe := posix.build()
		_, we := win.build()
		if pe == nil || we == nil || pe.Error() != we.Error() {
			t.Errorf("共有检查 #%d 两个平台的结果不同：posix=%v windows=%v", i, pe, we)
		}
	}
}

// ---------- Linux / macOS 的命令一个字节都没变 ----------

// legacyPosixBuild 是 #178 之前 headlessCmd.build 的原样拷贝（只改了名字）：对照用，不要随产品代码改动。
func legacyPosixBuild(c headlessCmd) (string, error) {
	switch {
	case !headlessBinRe.MatchString(c.Bin) || store.IsHeadlessPrefixCmd(c.Bin):
		return "", errors.New("可执行文件名不合法")
	case c.New && c.Continue:
		return "", errors.New("新开对话与接着最近的对话只能选一个")
	case (c.New || c.Continue) && c.ChatID != "":
		if c.Continue {
			return "", errors.New("接着最近的对话不能同时指定对话编号")
		}
		return "", errors.New("新开对话不能同时指定对话编号")
	case !c.New && !c.Continue && !headlessChatRe.MatchString(c.ChatID):
		return "", errors.New("对话编号不合法")
	case !strings.HasPrefix(c.Dir, "/") || strings.ContainsAny(c.Dir, "\x00\r\n"):
		return "", errors.New("工作目录必须是主机上的绝对路径")
	case c.Model != "" && !headlessModelRe.MatchString(c.Model):
		return "", errors.New("模型名不合法")
	case strings.TrimSpace(c.Prompt) == "" || strings.ContainsAny(c.Prompt, "\x00\r\n"):
		return "", errors.New("唤醒词为空或含换行")
	}
	parts := []string{c.Bin, "-p"}
	switch {
	case c.Continue:
		parts = append(parts, "--continue")
	case !c.New:
		parts = append(parts, "--resume", c.ChatID)
	}
	parts = append(parts, "--approve-mcps", "--trust")
	if c.Model != "" {
		parts = append(parts, "--model", shellQuote(c.Model))
	}
	if c.Posture == store.HeadlessPostureForce {
		parts = append(parts, "--force")
	}
	parts = append(parts, "--workspace", shellQuote(c.Dir), "--output-format", "stream-json", shellQuote(c.Prompt))
	return strings.Join(parts, " "), nil
}

func TestPosixBuildIsByteIdenticalToBeforeWindowsSupport(t *testing.T) {
	bins := []string{"agent", "/usr/local/bin/agent", "~/.local/bin/cursor-agent", "agent.cmd", "bash", "sudo", "a b", "", "cmd", "C:\\x\\agent.cmd"}
	chats := []string{"", "chat-9", "e33e27ef-0fbb-46e4-9446-5dee56ae61fa", "bad id"}
	modes := []struct{ n, c bool }{{false, false}, {true, false}, {false, true}, {true, true}}
	dirs := []string{"/work/app", "/work/it's here", "/", "relative", "/a\nb", `C:\x`, "", "/中文 目录/x"}
	postures := []string{"", store.HeadlessPostureAllowlist, store.HeadlessPostureForce}
	models := []string{"", "m-1", "bad model", "vendor/model:v1.5+x"}
	prompts := []string{"hi", "it's", "  ", "a\nb", `a"b`, "x$(y)`z`", `C:\path\`, "‘弯’", "100% & 50% | x > y < z ^ !", "多\x00字节"}

	n := 0
	for _, bin := range bins {
		for _, chat := range chats {
			for _, m := range modes {
				for _, dir := range dirs {
					for _, posture := range postures {
						for _, mdl := range models {
							for _, prompt := range prompts {
								c := headlessCmd{Bin: bin, ChatID: chat, New: m.n, Continue: m.c, Dir: dir, Posture: posture, Prompt: prompt, Model: mdl}
								got, gerr := c.build()
								want, werr := legacyPosixBuild(c)
								if got != want || (gerr == nil) != (werr == nil) || (gerr != nil && gerr.Error() != werr.Error()) {
									t.Fatalf("POSIX 拼装变了：%+v\n got (%q, %v)\nwant (%q, %v)", c, got, gerr, want, werr)
								}
								n++
							}
						}
					}
				}
			}
		}
	}
	t.Logf("对照了 %d 种输入组合", n)
}

// ---------- 目录工具 ----------

func TestCleanHeadlessDirAndSameDir(t *testing.T) {
	clean := []struct{ in, want string }{
		{"/work/app", "/work/app"},
		{"/work//app/./x/..", "/work/app"},
		{"/", "/"},
		{`C:\work\app`, `C:\work\app`},
		{`c:/work/app/`, `c:\work\app`},
		{`C:\work\\app\.\x\..`, `C:\work\app`},
		{`C:\`, `C:\`},
		{`C:/`, `C:\`},
		{`C:\..`, `C:\`},
		{`\\srv\share`, `\\srv\share`},
		{`\\srv\share\a\b\`, `\\srv\share\a\b`},
		{`//srv/share/a`, "/srv/share/a"}, // 以 / 开头的就是 POSIX 绝对路径（正斜杠的 UNC 不当成 Windows 路径）
		{"relative/x", ""},
		{`C:work`, ""},
		{"", ""},
	}
	for _, c := range clean {
		if got := cleanHeadlessDir(c.in); got != c.want {
			t.Errorf("cleanHeadlessDir(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}

	same := []struct {
		a, b string
		want bool
	}{
		{"/work/app", "/work/app/", true},
		{"/work/app", "/work/./app", true},
		{"/work/app", "/Work/app", false}, // POSIX 区分大小写
		{`C:\work\app`, `c:/WORK/app/`, true},
		{`C:\work\app`, `C:\work\app2`, false},
		{`\\srv\share\a`, `\\SRV\Share\A`, true},
		{`C:\work`, "/work", false},
		{"/work", `C:\work`, false},
		{"relative", "relative", false},
		{"", "", false},
		{`C:\work`, "", false},
	}
	for _, c := range same {
		if got := sameDir(c.a, c.b); got != c.want {
			t.Errorf("sameDir(%q, %q) = %v，期望 %v", c.a, c.b, got, c.want)
		}
	}

	for in, want := range map[string]string{
		"/work/app":        "app",
		"/":                "会话",
		`C:\work\app`:      "app",
		`C:\work\app\`:     "app",
		`C:\`:              "会话",
		`\\srv\share`:      "会话",
		`\\srv\share\proj`: "proj",
		`D:\我的 项目`:         "我的 项目",
	} {
		if got := launcherDirBase(in); got != want {
			t.Errorf("launcherDirBase(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestHeadlessDirChecks(t *testing.T) {
	for _, c := range []struct {
		dir     string
		windows bool
		ok      bool
	}{
		{"/work/app", false, true},
		{`C:\work`, false, false}, // Windows 写法放在 POSIX 主机上不行
		{"relative", false, false},
		{"/a\nb", false, false},
		{`C:\work`, true, true},
		{`\\srv\share\x`, true, true},
		{"/work/app", true, false}, // POSIX 写法放在 Windows 主机上不行
		{`C:\R&D`, true, false},
		{`C:\100%`, true, false},
	} {
		if err := headlessDirOK(c.dir, c.windows); (err == nil) != c.ok {
			t.Errorf("headlessDirOK(%q, windows=%v) = %v，期望 ok=%v", c.dir, c.windows, err, c.ok)
		}
	}
	for dir, want := range map[string]bool{
		"/work": true, `C:\work`: true, `\\srv\share`: true, "relative": false, "": false, "/a\nb": false, `C:work`: false, "~/x": false,
	} {
		if got := headlessDirShapeOK(dir); got != want {
			t.Errorf("headlessDirShapeOK(%q) = %v，期望 %v", dir, got, want)
		}
	}
}
