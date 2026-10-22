//go:build !windows

package holder

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
)

// DefaultShell 构造运行 Agent 的那个账户的登录 shell 的启动规格。
//
// Agent 通常由 systemd / launchd 以 root 拉起，此时环境变量极其精简（甚至没有 HOME、SHELL），
// 所以 shell 的选择与基础环境不能依赖继承：优先取 /etc/passwd 里该账户的 shell，
// 其次 $SHELL，最后回落到系统里确实存在的 shell。
func DefaultShell(rows, cols uint16) (ptyhost.Spec, error) {
	name, home, pwShell := passwdEntry(os.Geteuid())
	if home == "" {
		home = os.Getenv("HOME")
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	shell := pickShell(pwShell, os.Getenv("SHELL"))
	if shell == "" {
		return ptyhost.Spec{}, fmt.Errorf("找不到可用的 shell（已检查账户 shell、$SHELL、/bin/bash、/bin/sh）")
	}
	dir := home
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir = "/"
	}
	return ptyhost.Spec{
		Path: shell,
		Args: loginArgs(shell),
		Env:  buildEnv(os.Environ(), shell, home, name),
		Dir:  dir,
		Rows: rows,
		Cols: cols,
	}, nil
}

// passwdEntry 读取 /etc/passwd 中 uid 对应账户的名称、家目录与 shell。
// macOS 的普通账户不在 /etc/passwd 里，读取失败时返回空值由调用方回落。
func passwdEntry(uid int) (name, home, shell string) {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return "", "", ""
	}
	defer f.Close()
	want := strconv.Itoa(uid)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[2] == want {
			return fields[0], fields[5], fields[6]
		}
	}
	return "", "", ""
}

// pickShell 从候选里挑第一个真实可用的 shell。
func pickShell(preferred ...string) string {
	candidates := append([]string(nil), preferred...)
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/bin/zsh")
	}
	candidates = append(candidates, "/bin/bash", "/usr/bin/bash", "/bin/sh", "/usr/bin/sh")
	for _, c := range candidates {
		if usableShell(c) {
			return c
		}
	}
	return ""
}

func usableShell(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	switch filepath.Base(path) {
	case "nologin", "false", "true", "sync", "halt", "shutdown":
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// loginArgs 让常见 shell 以登录 shell 启动，得到用户熟悉的 profile 环境。
func loginArgs(shell string) []string {
	switch filepath.Base(shell) {
	case "bash", "zsh", "sh", "dash", "ash", "ksh", "mksh", "fish":
		return []string{"-l"}
	}
	return nil
}

// buildEnv 在继承环境的基础上补齐终端会话必需的变量，并去掉 Agent 自身的配置变量。
func buildEnv(base []string, shell, home, user string) []string {
	env := make(map[string]string, len(base)+8)
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || strings.HasPrefix(k, "RELAYMESH_") {
			continue
		}
		env[k] = v
	}
	setDefault := func(k, v string) {
		if v != "" && env[k] == "" {
			env[k] = v
		}
	}
	env["TERM"] = "xterm-256color"
	env["COLORTERM"] = "truecolor"
	env["SHELL"] = shell
	setDefault("HOME", home)
	setDefault("USER", user)
	setDefault("LOGNAME", user)
	setDefault("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	// 浏览器发来的永远是 UTF-8：会话必须运行在确实可用的 UTF-8 locale 下，否则 readline 会把中文字节当 Meta 键。
	ensureUTF8(env)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
