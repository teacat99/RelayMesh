package hygiene

import (
	"bytes"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// skipDirs 是不属于 Go 源码的目录（依赖、构建产物、会话与工具目录、前端工程）。
var skipDirs = map[string]bool{
	".git":            true,
	".cursor":         true,
	".agents":         true,
	".playwright-mcp": true,
	"node_modules":    true,
	"frontend":        true,
	"web":             true,
	"data":            true,
	"bin":             true,
	"certs":           true,
	"vendor":          true,
}

// TestGoSourcesAreGofmtClean 保证仓库里的 Go 文件都与 gofmt 的输出一致。
//
// 背景：第十批评估过历史遗留的 10 个不干净文件，差异全部是空白与对齐
// （结构体字段标签对齐、常量对齐、多出来的缩进层级、文件末尾的空行），没有任何语义内容；
// 清理之后用这个测试防止再次出现。失败时会列出需要处理的文件，执行
// `gofmt -w <文件>` 即可。
func TestGoSourcesAreGofmtClean(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("找不到仓库根目录的 go.mod（%s），跳过: %v", root, err)
	}

	var dirty []string
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		out, err := format.Source(src)
		if err != nil {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s 无法解析: %v", rel, err)
			return nil
		}
		if !bytes.Equal(src, out) {
			rel, _ := filepath.Rel(root, path)
			dirty = append(dirty, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("一个 Go 文件都没检查到，目录过滤可能写错了")
	}
	if len(dirty) > 0 {
		sort.Strings(dirty)
		t.Errorf("有 %d 个 Go 文件不是 gofmt 干净的，请执行 `gofmt -w %s`：\n  %s",
			len(dirty), strings.Join(dirty, " "), strings.Join(dirty, "\n  "))
	}
}
