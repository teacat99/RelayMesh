//go:build !windows

package holder

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
)

// fakeLocale 模拟 `locale` 命令：working 里的名字得到 UTF-8，其余名字（未安装）回落到 ANSI_X3.4-1968。
type fakeLocale struct {
	mu        sync.Mutex
	missing   bool // 没有 locale 命令（Alpine / musl）
	working   map[string]bool
	installed []string
	calls     map[string]int
}

func (f *fakeLocale) Charmap(name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
	if f.missing {
		return "", false
	}
	if f.working[name] {
		return "UTF-8", true
	}
	return "ANSI_X3.4-1968", true
}

func (f *fakeLocale) Installed() []string {
	if f.missing {
		return nil
	}
	return f.installed
}

func useFakeLocale(t *testing.T, f *fakeLocale) {
	t.Helper()
	localeMu.Lock()
	old := localeTools
	localeTools = f
	localeMu.Unlock()
	resetLocaleCache()
	t.Cleanup(func() {
		localeMu.Lock()
		localeTools = old
		localeMu.Unlock()
		resetLocaleCache()
	})
}

// 现代发行版：C.UTF-8 与 en_US.UTF-8 都在。
func modernLocale() *fakeLocale {
	return &fakeLocale{
		working:   map[string]bool{"C.UTF-8": true, "C.utf8": true, "en_US.UTF-8": true, "zh_CN.UTF-8": true},
		installed: []string{"C", "C.utf8", "POSIX", "en_US.utf8", "zh_CN.utf8"},
	}
}

// CentOS 7 一类的老系统：没有 C.UTF-8，只有生成过的 en_US.utf8。
func legacyLocale() *fakeLocale {
	return &fakeLocale{
		working:   map[string]bool{"en_US.UTF-8": true, "en_US.utf8": true},
		installed: []string{"C", "POSIX", "en_US.utf8"},
	}
}

func envOf(base ...string) map[string]string {
	m := map[string]string{}
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestEnsureUTF8(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("用例按 Linux 的候选顺序（C.UTF-8 优先）编写；macOS 优先 en_US.UTF-8")
	}
	cases := []struct {
		name string
		tool *fakeLocale
		env  []string
		want map[string]string // 期望出现的变量（空字符串表示不应设置）
	}{
		{"什么都没设：补 LANG，并把字符类别钉在 LC_CTYPE", modernLocale(), nil,
			map[string]string{"LANG": "C.UTF-8", "LC_CTYPE": "C.UTF-8", "LC_ALL": ""}},
		{"什么都没设且没有 C.UTF-8：改用 en_US.UTF-8", legacyLocale(), nil,
			map[string]string{"LANG": "en_US.UTF-8", "LC_CTYPE": "en_US.UTF-8"}},
		{"LANG=C：只覆盖字符类别，LANG 原样保留", modernLocale(), []string{"LANG=C"},
			map[string]string{"LANG": "C", "LC_CTYPE": "C.UTF-8"}},
		{"LANG=POSIX", modernLocale(), []string{"LANG=POSIX"},
			map[string]string{"LANG": "POSIX", "LC_CTYPE": "C.UTF-8"}},
		{"LANG 没有字符集后缀（en_US）", modernLocale(), []string{"LANG=en_US"},
			map[string]string{"LANG": "en_US", "LC_CTYPE": "C.UTF-8"}},
		{"LANG 是 GBK", modernLocale(), []string{"LANG=zh_CN.GBK"},
			map[string]string{"LANG": "zh_CN.GBK", "LC_CTYPE": "C.UTF-8"}},
		{"LANG 写着 UTF-8 但本机没有生成：当作不可用", legacyLocale(), []string{"LANG=zh_CN.UTF-8"},
			map[string]string{"LANG": "zh_CN.UTF-8", "LC_CTYPE": "en_US.UTF-8"}},
		// 可用的 LANG：LANG 原样保留，字符类别钉成同一个值——登录脚本之后即使把 LANG 改回 C，字符类别也不会跟着变。
		{"可用的 LANG：保留，字符类别钉成同一个值", modernLocale(), []string{"LANG=zh_CN.UTF-8"},
			map[string]string{"LANG": "zh_CN.UTF-8", "LC_CTYPE": "zh_CN.UTF-8", "LC_ALL": ""}},
		{"LC_CTYPE 已经是可用的 UTF-8：不动（LANG 不受影响）", modernLocale(), []string{"LC_CTYPE=en_US.UTF-8", "LANG=zh_CN.UTF-8"},
			map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "zh_CN.UTF-8", "LC_ALL": ""}},
		{"LC_CTYPE 可用而 LANG=C：只要字符类别可用就不动", modernLocale(), []string{"LC_CTYPE=en_US.UTF-8", "LANG=C"},
			map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "C"}},
		{"LC_ALL=C 会盖过其他变量，只能改它", modernLocale(), []string{"LC_ALL=C", "LANG=zh_CN.UTF-8"},
			map[string]string{"LC_ALL": "C.UTF-8", "LANG": "zh_CN.UTF-8", "LC_CTYPE": ""}},
		{"LC_ALL 是可用的 UTF-8：不动", modernLocale(), []string{"LC_ALL=en_US.UTF-8"},
			map[string]string{"LC_ALL": "en_US.UTF-8", "LC_CTYPE": "", "LANG": ""}},
		{"LC_CTYPE=POSIX 盖过 LANG=UTF-8", modernLocale(), []string{"LC_CTYPE=POSIX", "LANG=en_US.UTF-8"},
			map[string]string{"LC_CTYPE": "C.UTF-8", "LANG": "en_US.UTF-8"}},
		{"没有 locale 命令（musl）：LANG=C 仍然被纠正", &fakeLocale{missing: true}, []string{"LANG=C"},
			map[string]string{"LANG": "C", "LC_CTYPE": "C.UTF-8"}},
		{"没有 locale 命令：相信 UTF-8 的名字，并钉住字符类别", &fakeLocale{missing: true}, []string{"LANG=zh_CN.UTF-8"},
			map[string]string{"LANG": "zh_CN.UTF-8", "LC_CTYPE": "zh_CN.UTF-8"}},
		{"没有 locale 命令且什么都没设：补 C.UTF-8", &fakeLocale{missing: true}, nil,
			map[string]string{"LANG": "C.UTF-8", "LC_CTYPE": "C.UTF-8"}},
		{"没有 C.UTF-8 与 en_US，退而求其次用任意已安装的 UTF-8", &fakeLocale{
			working:   map[string]bool{"de_DE.utf8": true},
			installed: []string{"C", "POSIX", "de_DE.iso88591", "de_DE.utf8"},
		}, nil, map[string]string{"LANG": "de_DE.utf8", "LC_CTYPE": "de_DE.utf8"}},
		{"一个 UTF-8 locale 都没有：退回通用名字（尽力而为）", &fakeLocale{installed: []string{"C", "POSIX"}}, []string{"LANG=C"},
			map[string]string{"LANG": "C", "LC_CTYPE": "C.UTF-8"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useFakeLocale(t, c.tool)
			env := envOf(c.env...)
			ensureUTF8(env)
			for k, want := range c.want {
				if env[k] != want {
					t.Errorf("%s = %q，期望 %q（完整环境 %v）", k, env[k], want, env)
				}
			}
		})
	}
}

func TestLocaleProbeIsCachedPerName(t *testing.T) {
	f := modernLocale()
	useFakeLocale(t, f)
	for i := 0; i < 5; i++ {
		ensureUTF8(envOf("LANG=zh_CN.UTF-8"))
		ensureUTF8(envOf("LANG=C"))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls["zh_CN.UTF-8"] != 1 || f.calls["C.UTF-8"] != 1 {
		t.Errorf("同一个名字只应探测一次: %v", f.calls)
	}
}

func TestBuildEnvForcesUTF8ForCLocale(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("用例按 Linux 的候选顺序编写")
	}
	useFakeLocale(t, modernLocale())
	env := envMap(buildEnv([]string{"LANG=C", "HOME=/h", "PATH=/bin"}, "/bin/bash", "/h", "u"))
	if env["LANG"] != "C" || env["LC_CTYPE"] != "C.UTF-8" {
		t.Fatalf("LANG=C 的主机上应补 LC_CTYPE=C.UTF-8: %v", env)
	}
	run := envMap(execEnv(buildEnv([]string{"LC_ALL=C"}, "/bin/bash", "/h", "u")))
	if run["LC_ALL"] != "C.UTF-8" {
		t.Fatalf("模型执行用的环境同样应是 UTF-8: %v", run)
	}
}

// ---------- 真实 shell + 真实 PTY：这才是中文输入的实际路径 ----------

type ptyCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *ptyCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func startCapture(t *testing.T, term ptyhost.Terminal) *ptyCapture {
	t.Helper()
	c := &ptyCapture{}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := term.Read(b)
			if n > 0 {
				c.mu.Lock()
				c.buf.Write(b[:n])
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *ptyCapture) waitFor(t *testing.T, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(c.String(), want) {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}

// 回归：宿主 locale 是 C（LANG=C）时，经 buildEnv 补成 UTF-8 后，bash 里输入中文必须原样回显并执行。
// 修复前 readline 会把 UTF-8 字节当 Meta 键：命令行被改写，输出里根本不会出现完整的「你好中文」。
func TestRealBashAcceptsChineseInputUnderCLocale(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("没有 bash")
	}
	resetLocaleCache()
	// 前提：这台机器上确实存在某个 UTF-8 locale（否则无从验证）。
	pick := pickUTF8Locale()
	if cm, ok := (sysLocaleTool{}).Charmap(pick); !ok || !strings.EqualFold(cm, "UTF-8") {
		t.Skipf("本机没有可用的 UTF-8 locale（候选 %q，charmap=%q）", pick, cm)
	}
	home := t.TempDir()
	env := buildEnv([]string{"LANG=C", "HOME=" + home, "PATH=" + os.Getenv("PATH")}, bash, home, "tester")
	env = append(env, "PS1=READY> ")
	term, err := ptyhost.NewSpawner().Spawn(ptyhost.Spec{
		Path: bash, Args: []string{"--noprofile", "--norc", "-i"}, Env: env, Dir: home, Rows: 24, Cols: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopShell(term) })
	out := startCapture(t, term)
	if !out.waitFor(t, "READY> ", 5*time.Second) {
		t.Fatalf("shell 没有出现提示符: %q", out.String())
	}
	typeChineseEcho(t, term)
	if !out.waitFor(t, "[你好中文]", 5*time.Second) {
		t.Fatalf("中文没有原样到达命令: %q", out.String())
	}
	if strings.Contains(out.String(), `\344`) {
		t.Fatalf("输入行里出现了八进制转义（readline 把字节当成了非打印字符）: %q", out.String())
	}
}

// stopShell 终止 shell 并等它真的退出：交互式 bash 被挂断时还会往 HOME 里写 .bash_history，
// 不等的话会和 t.TempDir() 的清理抢着删目录（“directory not empty”）。最多等 3 秒，免得测试被卡住。
func stopShell(term ptyhost.Terminal) {
	term.Kill()
	done := make(chan struct{})
	go func() { term.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	_ = term.Close()
}

// typeChineseEcho 按浏览器输入法的节奏输入 `printf '[%s]\n' 你好中文` 并回车：每个词是一次独立写入。
// 非 UTF-8 的 locale 下，readline 对「写入末尾那个高位字节」会按 Meta 键分派（0xBD → M-= 会列出补全、
// 0xA0 → M-空格 会设标记），中间的字节反而因为后面还有输入而被直接插入——所以必须分次写、并让 bash 来得及处理。
func typeChineseEcho(t *testing.T, term ptyhost.Terminal) {
	t.Helper()
	if _, err := term.Write([]byte("printf '[%s]\\n' ")); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"你", "好", "中", "文"} {
		time.Sleep(80 * time.Millisecond)
		if _, err := term.Write([]byte(word)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := term.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
}

// 回归（2026-10-08，两台 Debian 主机实测）：Debian 安装器给 root 的 ~/.profile 写了 LANG=C 与 LANGUAGE=C。
// 会话是登录 shell，脚本在我们设好环境「之后」才执行，LANG 被改回 C，readline 又回到 C locale，
// 于是在 shell 提示符下输入中文失效（全屏程序不受影响）。把字符类别钉在 LC_CTYPE 上之后，脚本改不动它。
// 这个用例走真实的登录 shell（-l，与生产的 loginArgs 一致）+ 真实 PTY：只设 LANG 的旧做法会让它失败。
func TestRealLoginShellKeepsChineseInputWhenProfileResetsLang(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("没有 bash")
	}
	resetLocaleCache()
	pick := pickUTF8Locale()
	if cm, ok := (sysLocaleTool{}).Charmap(pick); !ok || !strings.EqualFold(cm, "UTF-8") {
		t.Skipf("本机没有可用的 UTF-8 locale（候选 %q，charmap=%q）", pick, cm)
	}
	home := t.TempDir()
	// 提示符也写在 ~/.profile 里：登录 shell 会先读 /etc/profile（Debian 会借 bash.bashrc 重设 PS1），~/.profile 最后执行。
	profile := "PS1='READY> '\n" +
		"unset HISTFILE\n" +
		"# 以下两行与 Debian 安装器写在 root 的 ~/.profile 里的内容一致\n" +
		"LANG=C\n" +
		"LANGUAGE=C\n"
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	// 宿主（systemd / 守护进程）的环境本身就是 UTF-8：profile 把它改坏，正是要验证的场景。
	env := buildEnv([]string{"LANG=" + pick, "HOME=" + home, "PATH=" + os.Getenv("PATH")}, bash, home, "tester")
	args := append(loginArgs(bash), "-i")
	term, err := ptyhost.NewSpawner().Spawn(ptyhost.Spec{Path: bash, Args: args, Env: env, Dir: home, Rows: 24, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopShell(term) })
	out := startCapture(t, term)
	if !out.waitFor(t, "READY> ", 8*time.Second) {
		t.Fatalf("登录 shell 没有出现提示符: %q", out.String())
	}
	// 先确认 profile 真的把 LANG 改成了 C（否则这个用例没有在测它想测的东西）。
	if _, err := term.Write([]byte("echo \"[$LANG|$LC_CTYPE]\"\r")); err != nil {
		t.Fatal(err)
	}
	if !out.waitFor(t, "[C|", 5*time.Second) {
		t.Fatalf("前提不成立：~/.profile 应把 LANG 改成 C: %q", out.String())
	}
	typeChineseEcho(t, term)
	if !out.waitFor(t, "[你好中文]", 5*time.Second) {
		t.Fatalf("profile 把 LANG 改回 C 之后，中文没有原样到达命令: %q", out.String())
	}
	if strings.Contains(out.String(), `\344`) {
		t.Fatalf("输入行里出现了八进制转义（readline 把字节当成了非打印字符）: %q", out.String())
	}
}
