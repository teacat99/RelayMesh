package terminal

import (
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 非交互运行的公共部分（headless_run.go）的纯函数测试：引号转义、拼命令、结束时写在聊天里的那句话。
// 它们原先和会话守护的非交互唤醒测试放在一个文件里（#245 第二期拆开）。

func TestHeadlessShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "'plain'",
		"a b":         "'a b'",
		"it's":        `'it'\''s'`,
		"$(rm -rf)":   "'$(rm -rf)'",
		"`x`;&|<>":    "'`x`;&|<>'",
		"":            "''",
		"中文【唤醒】":      "'中文【唤醒】'",
		"a'; touch b": `'a'\''; touch b'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestBuildHeadlessCommand(t *testing.T) {
	// 非交互运行命令的基本形态（续聊一个已知对话、不带 --model）：headlessCmd.build 是预设运行 / 会话页运行 / 自动续跑共用的拼法。
	buildHeadlessCommand := func(bin, chatID, dir, posture, prompt string) (string, error) {
		return headlessCmd{Bin: bin, ChatID: chatID, Dir: dir, Posture: posture, Prompt: prompt}.build()
	}
	prompt := "【续聊】请继续"
	got, err := buildHeadlessCommand("agent", "chat-9_x", "/work/app", store.HeadlessPostureAllowlist, prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := "agent -p --resume chat-9_x --approve-mcps --trust --workspace '/work/app' --output-format stream-json '【续聊】请继续'"
	if got != want {
		t.Fatalf("命令不对：\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "--force") {
		t.Fatal("allowlist 姿态不能带 --force")
	}
	forced, _ := buildHeadlessCommand("agent", "c1", "/w", store.HeadlessPostureForce, prompt)
	if !strings.Contains(forced, " --trust --force --workspace ") {
		t.Fatalf("force 姿态应带 --force: %s", forced)
	}
	for name, args := range map[string][5]string{
		"二进制含空格": {"a b", "c1", "/w", "allowlist", "p"},
		"对话编号注入": {"agent", "c1; rm -rf /", "/w", "allowlist", "p"},
		"目录不是绝对": {"agent", "c1", "w", "allowlist", "p"},
		"目录含换行":  {"agent", "c1", "/w\nx", "allowlist", "p"},
		"提示词为空":  {"agent", "c1", "/w", "allowlist", " "},
		"提示词换行":  {"agent", "c1", "/w", "allowlist", "a\nb"},
	} {
		if _, err := buildHeadlessCommand(args[0], args[1], args[2], args[3], args[4]); err == nil {
			t.Errorf("%s：应该拒绝", name)
		}
	}
	// 提示词里的单引号（正常不会有，守住最后一道）不能逃出引号。
	q, _ := buildHeadlessCommand("agent", "c1", "/w", "allowlist", "x'; touch /tmp/pwn; echo '")
	if !strings.Contains(q, `'x'\''; touch /tmp/pwn; echo '\'''`) {
		t.Fatalf("单引号没有被转义: %s", q)
	}
}

func TestRunEndTextCoversEveryTerminalState(t *testing.T) {
	zero := 0
	two := 2
	for state, want := range map[string]string{
		model.RunSucceeded: "成功",
		model.RunFailed:    "失败",
		model.RunTimedOut:  "达到运行时长上限",
		model.RunCancelled: "取消",
		model.RunRejected:  "拒绝",
		model.RunDenied:    "闸门",
		model.RunLost:      "丢失",
	} {
		got := runEndText("非交互续聊", model.TerminalRun{State: state, DurationMS: 12_000, ExitCode: &zero})
		if !strings.Contains(got, want) {
			t.Errorf("%s 的文字 %q 应含 %q", state, got, want)
		}
	}
	if got := runEndText("非交互续聊", model.TerminalRun{State: model.RunFailed, ExitCode: &two}); !strings.Contains(got, "退出码 2") {
		t.Errorf("失败应带退出码: %q", got)
	}
	if got := runEndText("非交互续聊", model.TerminalRun{State: model.RunSucceeded, DurationMS: 12_345}); !strings.Contains(got, "12 秒") {
		t.Errorf("应带用时: %q", got)
	}
	// 到点被强制结束：写「达到运行时长上限」并带上这次运行自己的上限（#141）；旧记录没有超时值就不写括号。
	for timeoutSec, want := range map[int]string{
		1800:                       "达到运行时长上限（30 分钟），已被终止",
		2 * 3600:                   "达到运行时长上限（2 小时），已被终止",
		termproto.MaxRunTimeoutSec: "达到运行时长上限（7 天），已被终止",
		0:                          "达到运行时长上限，已被终止",
	} {
		got := runEndText("预设「续聊」的非交互续聊", model.TerminalRun{State: model.RunTimedOut, TimeoutSec: timeoutSec})
		if !strings.Contains(got, want) || strings.Contains(got, "超时被终止") {
			t.Errorf("TimeoutSec=%d 的结束行 %q 应含 %q", timeoutSec, got, want)
		}
	}
}
