package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 守卫：internal/agent 下任何面向使用者的字符串都不能含反引号。
//
// 背景：升级后的提示曾经写成「想换上新版本时执行 `& 'C:\…\relaymesh-agent.exe' holder restart`」，
// 在 PowerShell 里照抄时结尾的反引号（PowerShell 的转义符）被一起带走，得到「未知的 holder 子命令 "restart`"」；
// 在 bash / zsh 里反引号还是命令替换。命令前后多出的任何符号都会被「选到行尾」一起复制。
// 所以提示里的命令一律用 cli.cmdLine 另起一行、缩进、前后不放任何符号。
//
// 只检查解释型字符串（"…"）的值：原始字符串（`…`）按语法不可能含反引号，Windows 命令行、plist 之类的模板不会误报。
// 测试文件不检查。
func TestNoBacktickInUserFacingStrings(t *testing.T) {
	fset := token.NewFileSet()
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || strings.HasPrefix(lit.Value, "`") {
				return true
			}
			val, uerr := strconv.Unquote(lit.Value)
			if uerr == nil && strings.ContainsRune(val, '`') {
				offenders = append(offenders, fset.Position(lit.Pos()).String()+": "+strconv.Quote(val))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("下面这些字符串含反引号。命令请用 cli.cmdLine 另起一行（前后不放任何符号），描述性文字直接去掉反引号：\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
