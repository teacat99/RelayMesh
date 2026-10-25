package terminal

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// Windows 实机核对用例表（#178 / #244）。
//
// 背景：Windows 上 Cursor 的命令行程序通常是 agent.cmd 这样的批处理垫片，PowerShell 调用它会经过 cmd.exe；
// 命令的写法（单引号字面量、提示词里的符号换成全角、末尾反斜杠补空格、目录末尾分隔符去掉）能不能让「智能体程序真正拿到的参数」
// 与本意一字不差，只有在真的 Windows 上才能定论。这张表给 scripts/e2e-terminal/winhost/headless-check.mjs 用：
//   - hub 用例：命令由 Hub 的构建器（headlessCmd.build）拼出，一个字节都不手写；expect 是手写的「到达程序的参数」（契约）；
//   - raw 用例：不经构建器的对照，证明被拦住的写法在这台主机上确实有危害（也能看出拦得是否多余）。
// 表本身只在设了 RM_WINHOST_DUMP 与 RM_WINHOST_ROOT 时导出；平时只跑下面的一致性检查，防止表与构建器脱节。

const (
	winLiveChat = "3f1c9a52-7d4e-4b8a-9c1d-0178a5e2f6b3"
	// winLiveSampleRoot 是例行检查用的示例根目录（形状与真实根目录一致）。
	winLiveSampleRoot = `C:\Users\u\AppData\Local\Temp\rm-e2e-winhost178-0123abcd`
)

var winLiveRootRe = regexp.MustCompile(`^[A-Za-z]:\\[^"%^&|<>!*?\r\n]+\\rm-e2e-winhost178-[0-9a-f]{8}$`)

type winLiveGen struct {
	Unit string `json:"unit"`
	N    int    `json:"n"`
}

type winLiveCase struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // hub / raw
	Note string `json:"note"`
	// Command 是整条命令；有 Gen 时改由主机端拼：Prefix + "'" + Unit×N + "'"（只用于长度上限一档，避免把一万字符的提示词塞进命令）。
	Command string      `json:"command,omitempty"`
	Prefix  string      `json:"prefix,omitempty"`
	Gen     *winLiveGen `json:"gen,omitempty"`
	// Expect 是应当到达程序的参数（不含程序名）；GenLast 为真时最后一个参数是 Gen 生成的那串。
	Expect     []string          `json:"expect,omitempty"`
	GenLast    bool              `json:"genLast,omitempty"`
	ExpectExit string            `json:"expectExit,omitempty"` // "0" / "nonzero"
	PathDir    string            `json:"pathdir,omitempty"`    // 运行前把 <根>\<PathDir> 放进 PATH 前面（裸命令名用例）
	SetEnv     map[string]string `json:"setenv,omitempty"`
	NoFiles    []string          `json:"nofiles,omitempty"`    // 运行后这些文件（相对根目录）必须不存在
	ProbeFiles []string          `json:"probefiles,omitempty"` // 只报告是否存在（raw 对照）
	Show       bool              `json:"show,omitempty"`       // 总是打印收到的参数
}

// winLiveArgv 按命令的结构拼出应当到达程序的参数；dir 与 prompt 由用例手写（处理后的样子）。
func winLiveArgv(c headlessCmd, dir, prompt string) []string {
	a := []string{"-p"}
	switch {
	case c.Continue:
		a = append(a, "--continue")
	case !c.New:
		a = append(a, "--resume", c.ChatID)
	}
	a = append(a, "--approve-mcps", "--trust")
	if c.Model != "" {
		a = append(a, "--model", c.Model)
	}
	if c.Posture == store.HeadlessPostureForce {
		a = append(a, "--force")
	}
	return append(a, "--workspace", dir, "--output-format", "stream-json", prompt)
}

func winLiveCases(t *testing.T, root string) []winLiveCase {
	t.Helper()
	bin := func(sub string) string { return root + `\` + sub + `\agent.cmd` }
	var out []winLiveCase
	hub := func(id, note string, c headlessCmd, wantDir, wantPrompt string, mut func(*winLiveCase)) {
		t.Helper()
		c.Windows = true
		if c.ChatID == "" && !c.New && !c.Continue {
			c.ChatID = winLiveChat
		}
		cmd, err := c.build()
		if err != nil {
			t.Fatalf("%s: 构建器拒绝了用例: %v", id, err)
		}
		lc := winLiveCase{ID: id, Kind: "hub", Note: note, Command: cmd, Expect: winLiveArgv(c, wantDir, wantPrompt), ExpectExit: "0"}
		if mut != nil {
			mut(&lc)
		}
		out = append(out, lc)
	}
	raw := func(id, note, command string, mut func(*winLiveCase)) {
		lc := winLiveCase{ID: id, Kind: "raw", Note: note, Command: command, Show: true}
		if mut != nil {
			mut(&lc)
		}
		out = append(out, lc)
	}
	work, workDir := root+`\work`, root+`\work dir`
	plain := bin("plain")

	hub("c01-plain", "基线：无空格的盘符路径，英文提示词", headlessCmd{Bin: plain, Dir: work, Prompt: "hello world"}, work, "hello world", nil)
	hub("c02-bin-space", "可执行文件路径里有空格（Windows 的安装路径常见）", headlessCmd{Bin: bin("with space"), Dir: work, Prompt: "hello"}, work, "hello", nil)
	hub("c03-bin-parens", "可执行文件路径里有空格与括号，目录有空格",
		headlessCmd{Bin: bin("prog files (x86)"), Dir: workDir, Prompt: "hello again"}, workDir, "hello again", nil)
	hub("c04-dir-quote", "新开对话；目录名里有单引号与空格",
		headlessCmd{Bin: plain, Dir: root + `\o'brien dir`, New: true, Prompt: "你好 hello"}, root+`\o'brien dir`, "你好 hello", nil)
	hub("c05-chinese", "接着最近的对话；中文提示词（含全角标点，不含要换的符号）",
		headlessCmd{Bin: plain, Dir: work, Continue: true, Prompt: "请继续上一轮的任务：检查测试结果，并写一份简短的汇报。"}, work,
		"请继续上一轮的任务：检查测试结果，并写一份简短的汇报。", func(c *winLiveCase) { c.Show = true })
	hub("c06-symbols", "带模型与放开全部命令；提示词里 % ^ & | < > ! 双引号 单引号 弯引号，末尾是反斜杠",
		headlessCmd{Bin: plain, Dir: work, Model: "gpt-5", Posture: store.HeadlessPostureForce,
			Prompt: "把 %PATH% 里的 ^ 改成 & 再 | 管道 < 左 > 右 ! 叹号 \"双引号\" 'single' ‘curly’ end\\"},
		work, "把 ％PATH％ 里的 ＾ 改成 ＆ 再 ｜ 管道 ＜ 左 ＞ 右 ！ 叹号 '双引号' 'single' ‘curly’ end\\ ",
		func(c *winLiveCase) { c.Show = true })
	hub("c07-nospace-symbols", "没有空白的提示词里夹着全部语法符号（PowerShell 是否给它包引号随版本而异，所以单列一条）",
		headlessCmd{Bin: plain, Dir: work, Prompt: "a&b|c<d>e^f%g!h"}, work, "a＆b｜c＜d＞e＾f％g！h", func(c *winLiveCase) { c.Show = true })
	hub("c08-trailing-backslash", "目录末尾带反斜杠（去掉），提示词末尾是反斜杠（补一个空格）",
		headlessCmd{Bin: plain, Dir: work + `\`, Prompt: `ends with slash\`}, work, `ends with slash\ `, func(c *winLiveCase) { c.Show = true })
	hub("c09-bare-agent-cmd", "裸命令名 agent.cmd，靠 PATH 找到", headlessCmd{Bin: "agent.cmd", Dir: work, Prompt: "hello via path"}, work, "hello via path",
		func(c *winLiveCase) { c.PathDir = "plain" })
	hub("c10-bare-agent", "默认的命令名 agent（没有扩展名），靠 PATH 与 PATHEXT 找到 agent.cmd",
		headlessCmd{Bin: "agent", Dir: work, Prompt: "hello default name"}, work, "hello default name", func(c *winLiveCase) { c.PathDir = "plain" })
	hub("c11-exit-code", "程序以非零退出码结束：整条命令的退出码也必须非零", headlessCmd{Bin: plain, Dir: work, Prompt: "EXIT7 please"}, work, "EXIT7 please",
		func(c *winLiveCase) { c.ExpectExit = "nonzero" })
	hub("c12-emoji", "提示词里有 BMP 之外的字符（UTF-16 代理对）", headlessCmd{Bin: plain, Dir: work, Prompt: "表情🙂测试 ok"}, work, "表情🙂测试 ok", func(c *winLiveCase) { c.Show = true })

	// 长度上限一档：构建器接受的最长命令（整条 ≤ winCmdLineMax）。提示词由主机端用 Unit×N 生成，命令里的前缀取自构建器。
	probe := headlessCmd{Windows: true, Bin: plain, ChatID: winLiveChat, Dir: work, Prompt: "x"}
	probeCmd, err := probe.build()
	if err != nil {
		t.Fatalf("长度用例的前缀构建失败: %v", err)
	}
	prefix := strings.TrimSuffix(probeCmd, "'x'")
	if prefix == probeCmd {
		t.Fatalf("长度用例的前缀取不出来: %q", probeCmd)
	}
	const unit = "abcdefghij"
	n := (winCmdLineMax - len(utf16.Encode([]rune(prefix))) - 2) / len(unit)
	longPrompt := strings.Repeat(unit, n)
	longCmd, err := headlessCmd{Windows: true, Bin: plain, ChatID: winLiveChat, Dir: work, Prompt: longPrompt}.build()
	if err != nil || longCmd != prefix+"'"+longPrompt+"'" {
		t.Fatalf("长度用例：构建器拼出的命令与前缀 + 提示词不一致（err=%v）", err)
	}
	lc := winLiveCase{ID: "c13-long", Kind: "hub", Note: "构建器接受的最长命令（整条恰好是 winCmdLineMax 个字符）", Prefix: prefix, Gen: &winLiveGen{Unit: unit, N: n},
		Expect: winLiveArgv(headlessCmd{ChatID: winLiveChat}, work, "")[:9], GenLast: true, ExpectExit: "0"}
	out = append(out, lc)

	// 构建器的防线（对照）：同一段文字，经构建器处理后不再被 cmd.exe 当成语法。
	hub("p1-amp-nospace", "对照 n1：没有空白的参数里夹着 &，经构建器换成全角后不能再起第二条命令",
		headlessCmd{Bin: plain, Dir: work, Prompt: "x&echo PWN3>" + root + `\pwn3.txt`}, work, "x＆echo PWN3＞"+root+`\pwn3.txt`,
		func(c *winLiveCase) { c.NoFiles, c.Show = []string{"pwn3.txt"}, true })
	hub("p2-quote-parity", "对照 n2：含双引号与 & 的提示词，经构建器处理后引号不会错位",
		headlessCmd{Bin: plain, Dir: work, Prompt: `a " & echo PWN4 > ` + root + `\pwn4.txt & " b`}, work, `a ' ＆ echo PWN4 ＞ `+root+`\pwn4.txt ＆ ' b`,
		func(c *winLiveCase) { c.NoFiles, c.Show = []string{"pwn4.txt"}, true })
	hub("p3-percent-env", "对照 n3：%变量% 经构建器换成全角后不会被展开", headlessCmd{Bin: plain, Dir: work, Prompt: "%RM178_MARK%"}, work, "％RM178_MARK％",
		func(c *winLiveCase) { c.SetEnv, c.Show = map[string]string{"RM178_MARK": "expanded-by-cmd"}, true })

	// 不经构建器的对照：被拦住的写法在这台主机上到底会怎样。
	rawBin := "& '" + plain + "' -p "
	raw("n1-amp-nospace", "没有空白的参数里的 & 会不会被 cmd.exe 当成命令分隔符（pwn1.txt 出现 = 会）", rawBin+"'x&echo PWN1>"+root+`\pwn1.txt'`,
		func(c *winLiveCase) { c.ProbeFiles = []string{"pwn1.txt"} })
	raw("n2-quote-parity", "参数里的双引号会不会让引号错位、& 跑到引号外面（pwn2.txt 出现 = 会）", rawBin+`'a " & echo PWN2 > `+root+`\pwn2.txt & " b'`,
		func(c *winLiveCase) { c.ProbeFiles = []string{"pwn2.txt"} })
	raw("n3-percent-env", "参数里的 %变量% 会不会被 cmd.exe 展开", rawBin+"'%RM178_MARK%'",
		func(c *winLiveCase) { c.SetEnv = map[string]string{"RM178_MARK": "expanded-by-cmd"} })
	raw("n4-over-limit", "超过 8191 个字符的命令会怎样（实测：直接失败，不是悄悄截断）", "",
		func(c *winLiveCase) {
			c.Prefix, c.Gen, c.GenLast = rawBin, &winLiveGen{Unit: "0123456789", N: 900}, true
			c.Expect, c.Show = []string{"-p"}, false
		})
	return out
}

// 表与构建器不脱节；更要紧的是：hub 用例拼出的命令里（去掉开头的调用运算符 &）不能再有 cmd.exe 会当成语法的字符，
// 期望到达程序的参数里也不能有——这是「提示词里的符号不会变成命令」这条安全声明的例行证据。
func TestWinLiveCasesStayInSyncWithBuilder(t *testing.T) {
	cases := winLiveCases(t, winLiveSampleRoot)
	if len(cases) < 18 {
		t.Fatalf("用例太少: %d", len(cases))
	}
	seen := map[string]bool{}
	hubs := 0
	for _, c := range cases {
		if seen[c.ID] {
			t.Errorf("用例编号重复: %s", c.ID)
		}
		seen[c.ID] = true
		if c.Kind != "hub" {
			continue
		}
		hubs++
		body := strings.TrimPrefix(c.Command+c.Prefix, "& ")
		if strings.ContainsAny(body, "\"%^&|<>!") {
			t.Errorf("%s: 构建器拼出的命令里还有 cmd 语法字符: %q", c.ID, c.Command+c.Prefix)
		}
		for i, a := range c.Expect {
			if strings.ContainsAny(a, "\"%^&|<>!") {
				t.Errorf("%s: 期望到达程序的第 %d 个参数里还有 cmd 语法字符: %q", c.ID, i, a)
			}
		}
		if c.Gen == nil && len(c.Expect) < 8 {
			t.Errorf("%s: 期望参数太少: %v", c.ID, c.Expect)
		}
	}
	if hubs < 14 {
		t.Errorf("hub 用例太少: %d", hubs)
	}
}

// TestDumpWindowsLiveCheckCases 把用例表导出成 JSON，交给 scripts/e2e-terminal/winhost/headless-check.mjs 生成主机上要跑的命令。
func TestDumpWindowsLiveCheckCases(t *testing.T) {
	file, root := os.Getenv("RM_WINHOST_DUMP"), os.Getenv("RM_WINHOST_ROOT")
	if file == "" || root == "" {
		t.Skip("设了 RM_WINHOST_DUMP（输出文件）与 RM_WINHOST_ROOT（主机上的临时目录）才导出；headless-check.mjs 会替你设")
	}
	if !winLiveRootRe.MatchString(root) {
		t.Fatalf("RM_WINHOST_ROOT 形状不对（要 <盘符>:\\...\\rm-e2e-winhost178-<8 位十六进制>）: %q", root)
	}
	data, err := json.Marshal(map[string]any{"root": root, "cases": winLiveCases(t, root)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
