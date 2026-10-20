//go:build !windows

package holder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestBuildEnvFillsMinimalServiceEnvironment(t *testing.T) {
	// systemd / launchd 拉起的进程环境极其精简（甚至没有 HOME 与 PATH）。
	env := envMap(buildEnv([]string{"FOO=bar"}, "/bin/bash", "/root", "root"))
	for k, want := range map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "SHELL": "/bin/bash",
		"HOME": "/root", "USER": "root", "LOGNAME": "root", "FOO": "bar",
	} {
		if env[k] != want {
			t.Errorf("%s = %q，期望 %q", k, env[k], want)
		}
	}
	if env["PATH"] == "" {
		t.Error("应补全 PATH")
	}
	if env["LANG"] == "" {
		t.Error("应补全 UTF-8 的 LANG，否则中文会乱码")
	}
}

func TestBuildEnvKeepsUserChoicesAndDropsAgentConfig(t *testing.T) {
	base := []string{
		"HOME=/home/alice", "USER=alice", "PATH=/custom/bin", "LANG=zh_CN.UTF-8", "TERM=dumb",
		"RELAYMESH_TERMINAL=off", "RELAYMESH_JWT_SECRET=should-never-leak", "RELAYMESH_AGENT_SERVER=x",
		"malformed-entry", "=novalue",
	}
	env := envMap(buildEnv(base, "/bin/zsh", "/ignored", "ignored"))
	if env["HOME"] != "/home/alice" || env["USER"] != "alice" || env["PATH"] != "/custom/bin" || env["LANG"] != "zh_CN.UTF-8" {
		t.Errorf("不应覆盖已有的用户环境: %v", env)
	}
	if env["TERM"] != "xterm-256color" {
		t.Errorf("TERM 必须与 xterm.js 匹配，得到 %q", env["TERM"])
	}
	for k := range env {
		if strings.HasPrefix(k, "RELAYMESH_") {
			t.Errorf("Agent 自身的配置 / 凭据变量不应泄漏进用户的 shell: %s", k)
		}
	}
	if _, ok := env["malformed-entry"]; ok {
		t.Error("格式错误的条目应被丢弃")
	}
}

func TestBuildEnvIsDeterministicAndSorted(t *testing.T) {
	a := buildEnv([]string{"Z=1", "A=2", "M=3"}, "/bin/sh", "/h", "u")
	b := buildEnv([]string{"M=3", "Z=1", "A=2"}, "/bin/sh", "/h", "u")
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatal("环境变量输出应与输入顺序无关")
	}
	for i := 1; i < len(a); i++ {
		if a[i-1] > a[i] {
			t.Fatalf("应按键排序: %v", a)
		}
	}
}

func TestLoginArgs(t *testing.T) {
	for _, sh := range []string{"/bin/bash", "/usr/bin/zsh", "/bin/sh", "/usr/local/bin/fish"} {
		if got := loginArgs(sh); len(got) != 1 || got[0] != "-l" {
			t.Errorf("loginArgs(%s) = %v", sh, got)
		}
	}
	if got := loginArgs("/usr/bin/unknown-shell"); got != nil {
		t.Errorf("未知 shell 不应带参数: %v", got)
	}
}

func TestDefaultShellProducesUsableSpec(t *testing.T) {
	spec, err := DefaultShell(40, 120)
	if err != nil {
		t.Skipf("本环境没有可用 shell: %v", err)
	}
	if st, err := os.Stat(spec.Path); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("shell %q 不可执行: %v", spec.Path, err)
	}
	if !filepath.IsAbs(spec.Path) {
		t.Fatalf("shell 路径应为绝对路径: %q", spec.Path)
	}
	if st, err := os.Stat(spec.Dir); err != nil || !st.IsDir() {
		t.Fatalf("工作目录 %q 不存在", spec.Dir)
	}
	if spec.Rows != 40 || spec.Cols != 120 {
		t.Fatalf("初始尺寸没有传入: %+v", spec)
	}
	env := envMap(spec.Env)
	if env["TERM"] != "xterm-256color" || env["SHELL"] != spec.Path {
		t.Fatalf("环境不对: TERM=%q SHELL=%q", env["TERM"], env["SHELL"])
	}
}

func TestPickShellPrefersUsableCandidates(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good-sh")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExec := filepath.Join(dir, "not-exec")
	if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pickShell("", "/does/not/exist", notExec, good); got != good {
		t.Fatalf("应跳过不存在 / 不可执行的候选，得到 %q", got)
	}
	if got := pickShell(); got == "" {
		t.Skip("本环境没有任何系统 shell")
	}
}
