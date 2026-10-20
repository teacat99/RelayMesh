package agentrev

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// repoRoot 从当前测试目录向上找到模块根（含 go.mod）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到模块根目录（go.mod）")
		}
		dir = parent
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeModule 建一个只含两个目录的小模块：a 与 b 参与指纹，other 不参与。
func fakeModule(t *testing.T) (root string, dirs []string) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n\ngo 1.25\n")
	writeFile(t, root, "go.sum", "")
	writeFile(t, root, "a/a.go", "package a\n\nfunc A() {}\n")
	writeFile(t, root, "b/b.go", "package b\n\nfunc B() {}\n")
	writeFile(t, root, "other/o.go", "package other\n")
	return root, []string{"a", "b"}
}

func mustCompute(t *testing.T, root string, dirs []string) string {
	t.Helper()
	rev, err := ComputeDirs(root, dirs)
	if err != nil {
		t.Fatalf("ComputeDirs: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(rev) {
		t.Fatalf("指纹格式不对: %q", rev)
	}
	return rev
}

// 同一份内容算两次得到同一个值；换到另一个目录（内容相同）也一样——指纹只看相对路径与内容。
func TestComputeIsStableAcrossCheckouts(t *testing.T) {
	r1, dirs := fakeModule(t)
	r2, _ := fakeModule(t)
	a, b, c := mustCompute(t, r1, dirs), mustCompute(t, r1, dirs), mustCompute(t, r2, dirs)
	if a != b || a != c {
		t.Fatalf("同一份内容的指纹应一致: %s %s %s", a, b, c)
	}
}

// Agent 侧的代码、新增文件、模块文件、平台专属文件变了，指纹必须变。
func TestComputeChangesWhenAgentSideCodeChanges(t *testing.T) {
	changes := []struct {
		name  string
		apply func(t *testing.T, root string)
	}{
		{"改一行源码", func(t *testing.T, r string) { writeFile(t, r, "a/a.go", "package a\n\nfunc A() { println(1) }\n") }},
		{"新增源文件", func(t *testing.T, r string) { writeFile(t, r, "b/extra.go", "package b\n") }},
		{"新增平台专属源文件", func(t *testing.T, r string) { writeFile(t, r, "b/x_windows.go", "package b\n") }},
		{"go.mod 变了", func(t *testing.T, r string) { writeFile(t, r, "go.mod", "module example.com/m\n\ngo 1.26\n") }},
		{"go.sum 变了", func(t *testing.T, r string) { writeFile(t, r, "go.sum", "example.com/x v1.0.0 h1:abc=\n") }},
	}
	for _, c := range changes {
		t.Run(c.name, func(t *testing.T) {
			root, dirs := fakeModule(t)
			before := mustCompute(t, root, dirs)
			c.apply(t, root)
			if after := mustCompute(t, root, dirs); after == before {
				t.Fatalf("%s 之后指纹应变化，仍是 %s", c.name, before)
			}
		})
	}
}

// 测试文件、清单之外的目录、子目录里的文件、非 .go 文件变了，指纹不变——否则服务端热更时无关改动也会让主机亮灯。
func TestComputeIgnoresEverythingOutsideTheBinary(t *testing.T) {
	ignored := []struct {
		name  string
		apply func(t *testing.T, root string)
	}{
		{"测试文件", func(t *testing.T, r string) { writeFile(t, r, "a/a_test.go", "package a\n\nfunc TestX() {}\n") }},
		{"清单之外的目录", func(t *testing.T, r string) { writeFile(t, r, "other/o.go", "package other\n\nvar X = 1\n") }},
		{"清单目录的子目录", func(t *testing.T, r string) { writeFile(t, r, "a/sub/s.go", "package sub\n") }},
		{"文档", func(t *testing.T, r string) { writeFile(t, r, "a/README.md", "# a\n") }},
	}
	for _, c := range ignored {
		t.Run(c.name, func(t *testing.T) {
			root, dirs := fakeModule(t)
			before := mustCompute(t, root, dirs)
			c.apply(t, root)
			if after := mustCompute(t, root, dirs); after != before {
				t.Fatalf("%s 变化不应影响指纹: %s → %s", c.name, before, after)
			}
		})
	}
}

// Windows 检出（CRLF）与 Linux 检出必须得到同一个指纹。
func TestComputeNormalizesLineEndings(t *testing.T) {
	lf, dirs := fakeModule(t)
	crlf, _ := fakeModule(t)
	writeFile(t, crlf, "a/a.go", "package a\r\n\r\nfunc A() {}\r\n")
	writeFile(t, crlf, "go.mod", "module example.com/m\r\n\r\ngo 1.25\r\n")
	if a, b := mustCompute(t, lf, dirs), mustCompute(t, crlf, dirs); a != b {
		t.Fatalf("换行风格不应影响指纹: %s vs %s", a, b)
	}
}

// 文件内容在两个文件之间挪动边界，哈希必须不同（路径与长度参与哈希）。
func TestComputeSeparatesFiles(t *testing.T) {
	r1, dirs := fakeModule(t)
	r2, _ := fakeModule(t)
	writeFile(t, r1, "a/a.go", "package a\n// x")
	writeFile(t, r1, "b/b.go", "yz\npackage b\n")
	writeFile(t, r2, "a/a.go", "package a\n// xy")
	writeFile(t, r2, "b/b.go", "z\npackage b\n")
	if a, b := mustCompute(t, r1, dirs), mustCompute(t, r2, dirs); a == b {
		t.Fatalf("不同的文件切分不应得到同一个指纹: %s", a)
	}
}

func TestComputeErrors(t *testing.T) {
	if _, err := Compute(t.TempDir()); err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("不是模块根目录应报错: %v", err)
	}
	root, _ := fakeModule(t)
	if _, err := ComputeDirs(root, []string{"a", "missing"}); err == nil {
		t.Fatal("清单里的目录不存在应报错，而不是悄悄少算一个目录")
	}
	writeFile(t, root, "empty/e_test.go", "package empty\n")
	if _, err := ComputeDirs(root, []string{"empty"}); err == nil {
		t.Fatal("目录里没有任何非测试源文件应报错")
	}
}

// 真实仓库能算出合法的指纹，并且重复计算结果一致。
func TestRepoRevIsComputable(t *testing.T) {
	root := repoRoot(t)
	a, err := Compute(root)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	b, _ := Compute(root)
	if a != b || !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("真实仓库的指纹不合法或不稳定: %q %q", a, b)
	}
}

// goBinary 找 go 命令；找不到时跳过依赖它的测试。
func goBinary(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	t.Skip("找不到 go 命令")
	return ""
}

// 防漏：Agent 在任一发布平台上依赖的本模块包，必须全部在 Dirs 里，Dirs 里也不能有 Agent 并不依赖的目录。
// 新增了依赖（例如 Agent 开始 import 一个新的 internal 包）而没更新清单，这里会失败并说出缺哪一个。
func TestDirsCoverAgentClosureOnEveryPlatform(t *testing.T) {
	root := repoRoot(t)
	goBin := goBinary(t)
	const module = "github.com/teacat99/RelayMesh/v2"
	targets := [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}}

	closure := map[string]bool{}
	for _, tg := range targets {
		cmd := exec.Command(goBin, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./cmd/relaymesh-agent")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+tg[0], "GOARCH="+tg[1], "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list（%s/%s）失败: %v", tg[0], tg[1], err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			switch {
			case line == module:
				closure["."] = true
			case strings.HasPrefix(line, module+"/"):
				closure[strings.TrimPrefix(line, module+"/")] = true
			}
		}
	}

	listed := map[string]bool{}
	for _, d := range Dirs {
		if listed[d] {
			t.Errorf("Dirs 里重复了 %s", d)
		}
		listed[d] = true
	}
	var missing, extra []string
	for d := range closure {
		if !listed[d] {
			missing = append(missing, d)
		}
	}
	for d := range listed {
		if !closure[d] {
			extra = append(extra, d)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("Agent 依赖了这些包，但它们不在 agentrev.Dirs 里（指纹会漏掉它们的改动）：%v\n请把它们加进 internal/agentrev/agentrev.go 的 Dirs", missing)
	}
	if len(extra) > 0 {
		t.Errorf("agentrev.Dirs 里有 Agent 并不依赖的目录（它们的改动会让指纹无谓变化）：%v\n请从 Dirs 里删掉", extra)
	}
}

// 指纹只覆盖 .go 源文件：目录里有 go:embed 的资源、汇编或 C 源文件，就必须先扩展计算方式，否则它们的改动会被漏掉。
func TestDirsHaveNoInputsTheFingerprintCannotSee(t *testing.T) {
	root := repoRoot(t)
	embed := regexp.MustCompile(`(?m)^\s*//go:embed\b`)
	foreign := map[string]bool{".s": true, ".c": true, ".cc": true, ".cpp": true, ".h": true, ".m": true, ".syso": true, ".swig": true}
	for _, d := range Dirs {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil {
			t.Fatalf("读取 %s: %v", d, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				continue
			}
			if foreign[strings.ToLower(filepath.Ext(name))] {
				t.Errorf("%s/%s：指纹看不到这种文件，请先扩展 agentrev 的计算方式", d, name)
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d), name))
			if err != nil {
				t.Fatal(err)
			}
			if embed.Match(data) {
				t.Errorf("%s/%s 用了 go:embed：被嵌入的资源不在指纹里，请先扩展 agentrev 的计算方式", d, name)
			}
		}
	}
}
