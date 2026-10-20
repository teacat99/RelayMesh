package cli

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// fakeElevation 把平台相关的钩子替换成可控的假实现，并在测试结束时还原。
type fakeElevation struct {
	calls      [][]string
	code       int
	err        error
	ownConsole bool
}

func installFakeElevation(t *testing.T, goosValue string, elevated, interactive bool) *fakeElevation {
	t.Helper()
	f := &fakeElevation{}
	oldGoos, oldRelaunch, oldOwns, oldElev, oldInter := goos, relaunchElevated, ownsConsole, isElevated, isInteractive
	t.Cleanup(func() {
		goos, relaunchElevated, ownsConsole, isElevated, isInteractive = oldGoos, oldRelaunch, oldOwns, oldElev, oldInter
	})
	goos = goosValue
	isElevated = func() bool { return elevated }
	isInteractive = func(io.Reader) bool { return interactive }
	ownsConsole = func() bool { return f.ownConsole }
	relaunchElevated = func(_ Env, args []string) (int, error) {
		f.calls = append(f.calls, args)
		return f.code, f.err
	}
	return f
}

func TestShouldElevate(t *testing.T) {
	base := elevationInput{goos: "windows", temp: true, interactive: true}
	if !shouldElevate(base) {
		t.Fatal("Windows 上未提权的交互式临时运行应当请求提升")
	}
	cases := map[string]func(*elevationInput){
		"不是 Windows":   func(in *elevationInput) { in.goos = "linux" },
		"持久身份":         func(in *elevationInput) { in.temp = false },
		"已经是管理员":       func(in *elevationInput) { in.elevated = true },
		"非交互（脚本 / 服务）": func(in *elevationInput) { in.interactive = false },
		"显式拒绝提升":       func(in *elevationInput) { in.noElevate = true },
		"已是提权后的子进程":    func(in *elevationInput) { in.relaunched = true },
	}
	for name, mutate := range cases {
		in := base
		mutate(&in)
		if shouldElevate(in) {
			t.Errorf("%s：不应请求提升", name)
		}
	}
}

func TestRelaunchArgsAreExplicit(t *testing.T) {
	env := testEnv(t)
	env.ServiceArgs = []string{"agent"}
	c := common{
		server:      "https://relay.example.com",
		pins:        stringList{"sha256:aa", "sha256:bb"},
		insecure:    true,
		noTOFU:      true,
		maxSessions: 3,
		configDir:   `C:\cfg dir`,
		temp:        true,
	}
	want := []string{"agent", "run", "--temp", "--server", "https://relay.example.com",
		"--pin", "sha256:aa", "--pin", "sha256:bb", "--insecure", "--no-tofu", "--max-sessions", "3",
		"--config-dir", `C:\cfg dir`, "--relaunched"}
	if got := relaunchArgs(env, c); !reflect.DeepEqual(got, want) {
		t.Fatalf("重启参数不对:\n got %q\nwant %q", got, want)
	}
	minimal := relaunchArgs(testEnv(t), common{server: "s"})
	if want := []string{"run", "--temp", "--server", "s", "--relaunched"}; !reflect.DeepEqual(minimal, want) {
		t.Fatalf("最小参数不对: %q", minimal)
	}
}

func TestMaybeElevateOutcomes(t *testing.T) {
	c := common{server: "https://relay.example.com", temp: true}

	t.Run("成功并透传子进程退出码", func(t *testing.T) {
		f := installFakeElevation(t, "windows", false, true)
		f.code = 7
		env := testEnv(t)
		code, handled := maybeElevate(env, c, true)
		if !handled || code != 7 || len(f.calls) != 1 {
			t.Fatalf("code=%d handled=%v calls=%v", code, handled, f.calls)
		}
		if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "UAC") || !strings.Contains(out, "--no-elevate") {
			t.Fatalf("提升前应告知用户将弹出 UAC 与跳过方式:\n%s", out)
		}
		if last := f.calls[0][len(f.calls[0])-1]; last != "--relaunched" {
			t.Fatalf("子进程必须带 --relaunched 防止递归: %q", f.calls[0])
		}
	})

	t.Run("用户拒绝后以当前权限继续", func(t *testing.T) {
		f := installFakeElevation(t, "windows", false, true)
		f.err = errElevationDeclined
		env := testEnv(t)
		code, handled := maybeElevate(env, c, true)
		if handled || code != 0 {
			t.Fatalf("拒绝后应继续运行: code=%d handled=%v", code, handled)
		}
		if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "已取消提升权限") {
			t.Fatalf("缺少回退提示:\n%s", out)
		}
	})

	t.Run("调用失败同样回退且提示原因", func(t *testing.T) {
		f := installFakeElevation(t, "windows", false, true)
		f.err = errors.New("boom")
		env := testEnv(t)
		if _, handled := maybeElevate(env, c, true); handled {
			t.Fatal("失败后不应视为已处理")
		}
		if out := env.Stderr.(*syncBuffer).String(); !strings.Contains(out, "boom") {
			t.Fatalf("应说明失败原因:\n%s", out)
		}
	})

	t.Run("不满足条件时完全不调用", func(t *testing.T) {
		for name, tc := range map[string]struct {
			goos        string
			elevated    bool
			interactive bool
			c           common
		}{
			"linux":        {"linux", false, true, c},
			"已提权":          {"windows", true, true, c},
			"非交互":          {"windows", false, false, c},
			"持久身份":         {"windows", false, true, common{server: c.server}},
			"--no-elevate": {"windows", false, true, common{server: c.server, temp: true, noElevate: true}},
			"--relaunched": {"windows", false, true, common{server: c.server, temp: true, relaunched: true}},
		} {
			f := installFakeElevation(t, tc.goos, tc.elevated, tc.interactive)
			if _, handled := maybeElevate(testEnv(t), tc.c, tc.interactive); handled || len(f.calls) != 0 {
				t.Errorf("%s：不应提升 (handled=%v calls=%v)", name, handled, f.calls)
			}
		}
	})
}

func wizardEnv(t *testing.T, input string) Env {
	t.Helper()
	env := testEnv(t)
	env.Stdin = strings.NewReader(input)
	return env
}

func TestRunWizard(t *testing.T) {
	validPin := "sha256:" + strings.Repeat("ab", 32)
	cases := []struct {
		name  string
		input string
		want  []string
		ok    bool
	}{
		{"https 地址无需指纹", "https://relay.example.com\n\n", []string{"--temp", "--server", "https://relay.example.com"}, true},
		{"缺协议时补 https", "relay.example.com\n\n", []string{"--temp", "--server", "https://relay.example.com"}, true},
		{"接受合法指纹", "https://relay.example.com\n" + validPin + "\n", []string{"--temp", "--server", "https://relay.example.com", "--pin", validPin}, true},
		{"指纹格式不对则忽略", "https://relay.example.com\nsha256:zz\n", []string{"--temp", "--server", "https://relay.example.com"}, true},
		{"先输错再输对", "ftp://x\nrelay.example.com\n\n", []string{"--temp", "--server", "https://relay.example.com"}, true},
		{"明文需显式确认", "http://10.0.0.5:18775\ny\n", []string{"--temp", "--server", "http://10.0.0.5:18775", "--insecure"}, true},
		{"明文不确认则取消", "http://10.0.0.5:18775\n\n", nil, false},
		{"空输入取消", "\n", nil, false},
		{"EOF 取消", "", nil, false},
		{"三次不合法后放弃", "ftp://a\nftp://b\nftp://c\n", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := runWizard(wizardEnv(t, tc.input))
			if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q ok=%v, want %q ok=%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRunNoArgs(t *testing.T) {
	t.Run("非 Windows 仍显示用法", func(t *testing.T) {
		installFakeElevation(t, "linux", false, true)
		env := wizardEnv(t, "relay.example.com\n")
		if code := Run(context.Background(), nil, env); code != 2 {
			t.Fatalf("code=%d", code)
		}
		if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "用法:") || strings.Contains(out, "临时连接向导") {
			t.Fatalf("应显示用法而不是向导:\n%s", out)
		}
	})

	t.Run("Windows 但 stdin 不是终端时显示用法", func(t *testing.T) {
		installFakeElevation(t, "windows", false, false)
		env := wizardEnv(t, "relay.example.com\n")
		if code := Run(context.Background(), nil, env); code != 2 {
			t.Fatalf("code=%d", code)
		}
		if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "用法:") {
			t.Fatalf("应显示用法:\n%s", out)
		}
	})

	t.Run("Windows 双击：向导 → 提权重启 → 暂停", func(t *testing.T) {
		f := installFakeElevation(t, "windows", false, true)
		f.ownConsole = true
		env := wizardEnv(t, "relay.example.com\n\n\n")
		if code := Run(context.Background(), nil, env); code != 0 {
			t.Fatalf("code=%d\n%s", code, env.Stdout.(*syncBuffer).String())
		}
		want := []string{"run", "--temp", "--server", "https://relay.example.com", "--relaunched"}
		if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], want) {
			t.Fatalf("提权子进程参数不对: %q", f.calls)
		}
		if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "按回车键关闭窗口") {
			t.Fatalf("独占控制台窗口时退出前应暂停:\n%s", out)
		}
	})

	t.Run("向导被取消也要暂停以便看清提示", func(t *testing.T) {
		f := installFakeElevation(t, "windows", false, true)
		f.ownConsole = true
		env := wizardEnv(t, "\n\n")
		if code := Run(context.Background(), nil, env); code != 2 {
			t.Fatalf("code=%d", code)
		}
		if len(f.calls) != 0 {
			t.Fatal("取消后不应尝试提权")
		}
		if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "已取消") || !strings.Contains(out, "按回车键关闭窗口") {
			t.Fatalf("\n%s", out)
		}
	})

	t.Run("只配了环境变量时不进向导", func(t *testing.T) {
		f := installFakeElevation(t, "windows", false, true)
		env := wizardEnv(t, "x\n")
		dataDir, cfgDir := t.TempDir(), t.TempDir() // 不能落到真实用户目录
		env.Getenv = func(k string) string {
			switch k {
			case envServer:
				return "https://relay.example.com"
			case envDataDir:
				return dataDir
			case envConfigDir:
				return cfgDir
			}
			return ""
		}
		// 没有 --temp，持久身份不会提权；这里只验证没有进入向导（不会读取 stdin 的 "x"）。
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = Run(ctx, nil, env)
		if out := env.Stdout.(*syncBuffer).String(); strings.Contains(out, "临时连接向导") {
			t.Fatalf("不应进入向导:\n%s", out)
		}
		if len(f.calls) != 0 {
			t.Fatal("持久身份不应提权")
		}
	})
}

func TestRelaunchedChildPausesOnFailure(t *testing.T) {
	f := installFakeElevation(t, "windows", true, true)
	f.ownConsole = true
	env := wizardEnv(t, "\n")
	code := Run(context.Background(), []string{"run", "--relaunched", "--temp", "--server", "ftp://bad"}, env)
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "按回车键关闭窗口") {
		t.Fatalf("提权子进程出错退出前必须暂停，否则窗口一闪而过:\n%s", out)
	}
	if len(f.calls) != 0 {
		t.Fatal("子进程不应再次提权")
	}
}

func TestPauseBeforeCloseOnlyWhenOwningInteractiveConsole(t *testing.T) {
	for _, tc := range []struct {
		own, interactive bool
		want             bool
	}{{true, true, true}, {true, false, false}, {false, true, false}, {false, false, false}} {
		f := installFakeElevation(t, "windows", false, tc.interactive)
		f.ownConsole = tc.own
		env := wizardEnv(t, "\n")
		pauseBeforeClose(env)
		got := strings.Contains(env.Stdout.(*syncBuffer).String(), "按回车键关闭窗口")
		if got != tc.want {
			t.Errorf("own=%v interactive=%v: 暂停=%v，期望 %v", tc.own, tc.interactive, got, tc.want)
		}
	}
}

func TestPinPattern(t *testing.T) {
	good := "sha256:" + strings.Repeat("0f", 32)
	for in, want := range map[string]bool{
		good:                                true,
		strings.ToUpper(good):               true,
		"sha256:" + strings.Repeat("a", 63): false,
		"sha1:" + strings.Repeat("a", 64):   false,
		"sha256:" + strings.Repeat("g", 64): false,
		"":                                  false,
	} {
		if got := pinPattern(in); got != want {
			t.Errorf("pinPattern(%q)=%v，期望 %v", in, got, want)
		}
	}
}
