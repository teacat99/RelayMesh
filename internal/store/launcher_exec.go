package store

import (
	"fmt"
	"strings"
	"unicode"
)

// 启动器「命令退出即结束会话」的命令分类；与 frontend/src/lib/terminal/launcherExec.ts 一致。

var launcherShellBuiltins = map[string]struct{}{
	".": {}, ":": {}, "alias": {}, "bg": {}, "bind": {}, "break": {}, "builtin": {}, "caller": {},
	"cd": {}, "command": {}, "compgen": {}, "complete": {}, "compopt": {}, "continue": {}, "declare": {},
	"dirs": {}, "disown": {}, "echo": {}, "enable": {}, "eval": {}, "exec": {}, "exit": {}, "export": {},
	"false": {}, "fc": {}, "fg": {}, "getopts": {}, "hash": {}, "help": {}, "history": {}, "jobs": {},
	"kill": {}, "let": {}, "local": {}, "logout": {}, "mapfile": {}, "popd": {}, "printf": {}, "pushd": {},
	"pwd": {}, "read": {}, "readarray": {}, "readonly": {}, "return": {}, "set": {}, "shift": {}, "shopt": {},
	"source": {}, "suspend": {}, "test": {}, "times": {}, "trap": {}, "true": {}, "type": {}, "typeset": {},
	"ulimit": {}, "umask": {}, "unalias": {}, "unset": {}, "wait": {},
}

// ClassifyLauncherExec 判断启动命令能否用 exec 启动；可以时返回包装后的 exec 片段（不含 cd）。
func ClassifyLauncherExec(command string) (execLine string, err error) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return "", nil
	}
	if strings.ContainsAny(cmd, "|&;<>(){}`$") {
		return "", fmt.Errorf("%w：命令里含有 shell 运算符、重定向或命令替换，不能开启「命令退出即结束会话」", ErrInvalidLauncher)
	}
	if strings.Contains(cmd, "$(") || strings.Contains(cmd, "${") {
		return "", fmt.Errorf("%w：命令里含有命令替换，不能开启「命令退出即结束会话」", ErrInvalidLauncher)
	}
	for _, op := range []string{"&&", "||", ";;"} {
		if strings.Contains(cmd, op) {
			return "", fmt.Errorf("%w：命令是复合语句，不能开启「命令退出即结束会话」", ErrInvalidLauncher)
		}
	}
	words, err := splitLauncherShellWords(cmd)
	if err != nil {
		return "", err
	}
	if len(words) == 0 {
		return "", nil
	}
	var env []string
	i := 0
	for i < len(words) && isLauncherEnvAssignment(words[i]) {
		env = append(env, words[i])
		i++
	}
	if i >= len(words) {
		return "", fmt.Errorf("%w：只有环境变量赋值、没有实际命令，不能开启「命令退出即结束会话」", ErrInvalidLauncher)
	}
	if _, ok := launcherShellBuiltins[strings.ToLower(words[i])]; ok {
		return "", fmt.Errorf("%w：「%s」是 shell 内建命令，不能开启「命令退出即结束会话」", ErrInvalidLauncher, words[i])
	}
	rest := strings.Join(words[i:], " ")
	if len(env) == 0 {
		return "exec " + rest, nil
	}
	return "exec env " + strings.Join(env, " ") + " " + rest, nil
}

func validateLauncherExecCommand(command string) error {
	_, err := ClassifyLauncherExec(command)
	return err
}

func isLauncherEnvAssignment(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	name := w[:eq]
	for _, r := range name {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	if name[0] >= '0' && name[0] <= '9' {
		return false
	}
	return true
}

func splitLauncherShellWords(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	inSingle := false
	escaped := false
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		switch {
		case r == '\\' && !inSingle:
			escaped = true
		case r == '\'':
			b.WriteRune(r)
			inSingle = !inSingle
		case !inSingle && unicode.IsSpace(r):
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		return nil, fmt.Errorf("%w：命令末尾的反斜杠不完整，不能开启「命令退出即结束会话」", ErrInvalidLauncher)
	}
	if inSingle {
		return nil, fmt.Errorf("%w：命令里的单引号没有配对，不能开启「命令退出即结束会话」", ErrInvalidLauncher)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out, nil
}
