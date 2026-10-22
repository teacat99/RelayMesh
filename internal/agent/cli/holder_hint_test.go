package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

// 「重启会话守护进程」的提示命令：照抄就能用——命令不在 PATH 里时用完整路径、系统级安装加 sudo、
// Windows 用 PowerShell 的调用运算符；命令失败时能认出「其实是系统级安装、套接字在别处」。

func ivOf(goos string, elevated bool, sudoUser string, onPath bool) invocation {
	return invocation{
		goos:          goos,
		elevated:      elevated,
		sudoUser:      sudoUser,
		defaultSocket: "/default/holder.sock",
		onPath:        func(string, string) bool { return onPath },
	}
}

func envProg(prog string) Env {
	e := Env{Prog: prog}
	return e
}

func TestHolderCommand(t *testing.T) {
	const winExe = `C:\Program Files\RelayMesh Agent\relaymesh-agent.exe`
	cases := []struct {
		name      string
		iv        invocation
		prog      string
		installed string
		socket    string
		want      string
	}{
		{"命令在 PATH 里：用命令名", ivOf("linux", false, "", true), "relaymesh-agent", "/home/u/.local/bin/relaymesh-agent", "", "relaymesh-agent holder restart"},
		{"用户级安装不在 PATH 里（常见情形）：用完整路径", ivOf("linux", false, "", false), "relaymesh-agent", "/home/u/.local/bin/relaymesh-agent", "", "/home/u/.local/bin/relaymesh-agent holder restart"},
		{"root 登录、不在 PATH 里：完整路径，不加 sudo", ivOf("linux", true, "", false), "relaymesh-agent", "/root/.local/bin/relaymesh-agent", "", "/root/.local/bin/relaymesh-agent holder restart"},
		{"经 sudo 做的系统级安装（常见情形）：加 sudo", ivOf("linux", true, "alice", true), "relaymesh-agent", "/usr/local/bin/relaymesh-agent", "", "sudo relaymesh-agent holder restart"},
		{"经 sudo 且不在 PATH 里：sudo + 完整路径", ivOf("linux", true, "alice", false), "relaymesh-agent", "/usr/local/bin/relaymesh-agent", "", "sudo /usr/local/bin/relaymesh-agent holder restart"},
		{"macOS 同理", ivOf("darwin", true, "alice", true), "relaymesh-agent", "/usr/local/bin/relaymesh-agent", "", "sudo relaymesh-agent holder restart"},
		{"套接字就是默认位置：不带 --holder-socket", ivOf("linux", false, "", true), "relaymesh-agent", "", "/default/holder.sock", "relaymesh-agent holder restart"},
		{"套接字不在默认位置：带上它", ivOf("linux", false, "", true), "relaymesh-agent", "", "/data/rm/holder/holder.sock", "relaymesh-agent holder restart --holder-socket /data/rm/holder/holder.sock"},
		{"带空格的路径要加引号", ivOf("linux", false, "", false), "relaymesh-agent", "/opt/my tools/relaymesh-agent", "/data/a b/holder.sock", "'/opt/my tools/relaymesh-agent' holder restart --holder-socket '/data/a b/holder.sock'"},
		{"主程序的子命令形态（relaymesh agent）", ivOf("linux", false, "", false), "relaymesh agent", "/usr/local/bin/relaymesh", "", "/usr/local/bin/relaymesh agent holder restart"},
		{"不知道安装位置：按命令名写", ivOf("linux", false, "", false), "relaymesh-agent", "", "", "relaymesh-agent holder restart"},
		{"Windows 不在 PATH 里：PowerShell 调用运算符 + 完整路径", ivOf("windows", false, "", false), "relaymesh-agent", winExe, "", "& '" + winExe + "' holder restart"},
		{"Windows 管理员也不加 sudo", ivOf("windows", true, "alice", false), "relaymesh-agent", winExe, "", "& '" + winExe + "' holder restart"},
		{"Windows 在 PATH 里：用命令名", ivOf("windows", false, "", true), "relaymesh-agent", winExe, "", "relaymesh-agent holder restart"},
		{"Windows 路径里的单引号要写两个", ivOf("windows", false, "", false), "relaymesh-agent", `C:\O'Neil\relaymesh-agent.exe`, "", `& 'C:\O''Neil\relaymesh-agent.exe' holder restart`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.iv.holderCommand(envProg(c.prog), c.installed, c.socket, "restart")
			if got != c.want {
				t.Fatalf("命令不对:\n  得到 %q\n  期望 %q", got, c.want)
			}
		})
	}
}

func TestHolderCommandSubcommands(t *testing.T) {
	iv := ivOf("linux", true, "alice", true)
	for _, sub := range []string{"restart", "stop", "status"} {
		got := iv.holderCommand(envProg("relaymesh-agent"), "", "", sub)
		if got != "sudo relaymesh-agent holder "+sub {
			t.Fatalf("%s: %q", sub, got)
		}
	}
}

// assertCommandOwnLine 断言 text 里有一行恰好是「四格缩进 + cmd」，并且整段文字没有任何反引号。
// 这是提示能被照抄的前提：命令前后只要多出一个字符（反引号、引号、句号……），选中复制后就可能是一条坏命令（见 cmdLine）。
func assertCommandOwnLine(t *testing.T, text, cmd string) {
	t.Helper()
	if strings.ContainsRune(text, '`') {
		t.Fatalf("提示里不该出现反引号:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "    "+cmd {
			return
		}
	}
	t.Fatalf("命令 %q 没有独占一行（四格缩进，前后没有任何字符）:\n%s", cmd, text)
}

// assertCommandLineEndsWith 断言 text 的最后一行是「四格缩进 + 以 tail 结尾的命令」，并且整段文字没有任何反引号。
// 用在命令里带有环境相关路径、没法写死整条命令的场景。
func assertCommandLineEndsWith(t *testing.T, text, tail string) {
	t.Helper()
	if strings.ContainsRune(text, '`') {
		t.Fatalf("提示里不该出现反引号:\n%s", text)
	}
	lines := strings.Split(text, "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "    ") || !strings.HasSuffix(last, tail) {
		t.Fatalf("最后一行应是缩进的命令（以 %q 结尾），得到 %q:\n%s", tail, last, text)
	}
}

func TestRestartHintSentence(t *testing.T) {
	iv := ivOf("linux", true, "alice", true)
	got := iv.restartHint(envProg("relaymesh-agent"), "/usr/local/bin/relaymesh-agent", "")
	assertCommandOwnLine(t, got, "sudo relaymesh-agent holder restart")
	if !strings.HasSuffix(got, "sudo relaymesh-agent holder restart") {
		t.Fatalf("命令之后不该再跟任何文字: %q", got)
	}
	// 重启的后果要说全：会话 + 模型正在运行的命令；--drain 是等它们自然结束
	for _, want := range []string{"会结束其中的全部会话和模型正在运行的命令", "--drain", "自然结束"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示里缺少 %q: %s", want, got)
		}
	}
	// Windows 的系统级安装要提醒用管理员 PowerShell——说明在命令前面，命令之后不再有文字
	const winCmd = "& 'C:\\Program Files\\RelayMesh Agent\\relaymesh-agent.exe' holder restart"
	win := ivOf("windows", true, "", false).restartHint(envProg("relaymesh-agent"), `C:\Program Files\RelayMesh Agent\relaymesh-agent.exe`, "")
	assertCommandOwnLine(t, win, winCmd)
	if lead, at := strings.Index(win, "以管理员身份运行"), strings.Index(win, winCmd); lead < 0 || at < 0 || lead > at {
		t.Fatalf("Windows 系统级安装的提示不对（管理员说明要写在命令前面）: %s", win)
	}
	if !strings.HasSuffix(win, winCmd) {
		t.Fatalf("命令之后不该再跟任何文字: %q", win)
	}
	// Windows 用户级安装不需要管理员
	user := ivOf("windows", false, "", false).restartHint(envProg("relaymesh-agent"), `C:\Users\a\AppData\Local\Programs\RelayMesh Agent\relaymesh-agent.exe`, "")
	if strings.Contains(user, "管理员") {
		t.Fatalf("用户级安装不该要求管理员: %s", user)
	}
}

func TestSystemHolderHint(t *testing.T) {
	const sysSocket = "/var/lib/relaymesh-agent/holder/holder.sock"
	userSocket := "/home/alice/.local/state/relaymesh-agent/holder/holder.sock"
	unit := "/etc/systemd/system/" + systemdHolderUnitName
	has := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, x := range paths {
				if x == p {
					return true
				}
			}
			return false
		}
	}
	env := envProg("relaymesh-agent")
	// 当前身份默认会去连的套接字（普通用户：自己目录下的那个）
	userIv := func(goos string, elevated bool) invocation {
		iv := ivOf(goos, elevated, "", true)
		iv.defaultSocket = userSocket
		return iv
	}

	t.Run("普通用户 + 系统级安装：说明原因并给出 sudo 命令（常见情形）", func(t *testing.T) {
		got := systemHolderHint(env, userIv("linux", false), userSocket, has(unit))
		for _, want := range []string{"系统级服务", sysSocket} {
			if !strings.Contains(got, want) {
				t.Fatalf("提示里缺少 %q: %s", want, got)
			}
		}
		assertCommandOwnLine(t, got, "sudo relaymesh-agent holder restart")
		assertCommandOwnLine(t, got, "sudo relaymesh-agent holder status")
	})
	t.Run("只有数据目录也算装过系统级", func(t *testing.T) {
		if got := systemHolderHint(env, userIv("linux", false), userSocket, has("/var/lib/relaymesh-agent")); got == "" {
			t.Fatal("系统数据目录存在时也该提示")
		}
	})
	t.Run("已经是 root：不加提示（没有运行就是真没运行）", func(t *testing.T) {
		if got := systemHolderHint(env, userIv("linux", true), userSocket, has(unit)); got != "" {
			t.Fatalf("root 不该有提示: %s", got)
		}
	})
	t.Run("没装系统级：不加提示", func(t *testing.T) {
		if got := systemHolderHint(env, userIv("linux", false), userSocket, has()); got != "" {
			t.Fatalf("没装系统级不该有提示: %s", got)
		}
	})
	t.Run("本来连的就是系统级套接字：不误导", func(t *testing.T) {
		if got := systemHolderHint(env, func() invocation { iv := userIv("linux", false); iv.defaultSocket = sysSocket; return iv }(), sysSocket, has(unit)); got != "" {
			t.Fatalf("已经在连系统级套接字，不该再让人加 sudo: %s", got)
		}
	})
	t.Run("Windows 普通窗口：让用管理员 PowerShell", func(t *testing.T) {
		t.Setenv("ProgramData", `C:\ProgramData`)
		const winUserSocket = `C:\Users\a\AppData\Local\RelayMesh\Agent\holder\holder.sock`
		_, dir := systemHolderPaths("windows")
		got := systemHolderHint(env, func() invocation { iv := userIv("windows", false); iv.defaultSocket = winUserSocket; return iv }(), winUserSocket, has(dir))
		if !strings.Contains(got, "以管理员身份运行") {
			t.Fatalf("Windows 的提示不对: %s", got)
		}
	})
	t.Run("使用者明确指了别的套接字：不替他猜", func(t *testing.T) {
		if got := systemHolderHint(env, userIv("linux", false), "/tmp/custom.sock", has(unit)); got != "" {
			t.Fatalf("明确指了套接字，不该提示 sudo: %s", got)
		}
	})
	t.Run("不认识的系统：不加提示", func(t *testing.T) {
		if got := systemHolderHint(env, userIv("plan9", false), userSocket, has(unit)); got != "" {
			t.Fatalf("不认识的系统不该有提示: %s", got)
		}
	})
}

func TestDescribeDialErrorKeepsBaseMessage(t *testing.T) {
	got := describeDialError(envProg("relaymesh-agent"), holder.ErrNotRunning, "/x/holder.sock")
	if !strings.HasPrefix(got, "会话守护进程没有运行（套接字 /x/holder.sock）") {
		t.Fatalf("基本提示被改坏了: %s", got)
	}
	if got := describeDialError(envProg("relaymesh-agent"), errors.New("boom"), "/x"); !strings.Contains(got, "无法连接会话守护进程: boom") {
		t.Fatalf("其他错误的提示不对: %s", got)
	}
}

func TestKickWindowsHolderTask(t *testing.T) {
	runsOf := func(f *fakeWin, verb string) []string {
		var out []string
		for _, c := range f.cmds {
			if strings.HasPrefix(c, "schtasks "+verb+" ") {
				out = append(out, c)
			}
		}
		return out
	}

	t.Run("管理员：先启动系统级的守护进程任务", func(t *testing.T) {
		f := newFakeWin(t, true)
		if _, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{}), f.w); err != nil {
			t.Fatal(err)
		}
		f.cmds = nil
		task, err := kickWindowsHolderTask(f.w)
		if err != nil {
			t.Fatal(err)
		}
		if task != winHolderTaskName {
			t.Fatalf("任务名不对: %s", task)
		}
		if runs := runsOf(f, "/Run"); len(runs) != 1 || !strings.Contains(runs[0], winHolderTaskName) {
			t.Fatalf("应只启动守护进程任务一次: %v", f.cmds)
		}
	})

	t.Run("普通用户：只看用户级任务", func(t *testing.T) {
		f := newFakeWin(t, false)
		if _, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{user: true}), f.w); err != nil {
			t.Fatal(err)
		}
		f.cmds = nil
		task, err := kickWindowsHolderTask(f.w)
		if err != nil {
			t.Fatal(err)
		}
		if task != "RelayMeshHolder-HOST_alice" {
			t.Fatalf("任务名不对: %s", task)
		}
	})

	t.Run("没装：返回「没有找到」且不启动任何东西", func(t *testing.T) {
		f := newFakeWin(t, true)
		_, err := kickWindowsHolderTask(f.w)
		if !errors.Is(err, errNoHolderTask) {
			t.Fatalf("应返回 errNoHolderTask，实际 %v", err)
		}
		if runs := runsOf(f, "/Run"); len(runs) != 0 {
			t.Fatalf("不该启动任何任务: %v", runs)
		}
	})

	t.Run("启动失败：带着任务名与原因返回", func(t *testing.T) {
		f := newFakeWin(t, true)
		if _, err := doInstallWindows(testEnv(t), winHolderCfg(t, common{}), f.w); err != nil {
			t.Fatal(err)
		}
		f.failOn["schtasks /Run"] = errors.New("exit status 1")
		f.outOn["schtasks /Run"] = "拒绝访问"
		task, err := kickWindowsHolderTask(f.w)
		if err == nil || task != winHolderTaskName || !strings.Contains(err.Error(), "拒绝访问") {
			t.Fatalf("应带任务名与原因失败: %q %v", task, err)
		}
	})
}
