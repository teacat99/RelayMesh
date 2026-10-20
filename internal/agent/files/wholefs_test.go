package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「允许整个文件系统」：Roots 里只有 Hub 才会放进来的哨兵 termproto.FileRootAll。

func TestResolveWholeFS(t *testing.T) {
	whole := []string{termproto.FileRootAll}
	cases := []struct {
		name  string
		rules pathRules
		roots []string
		p     string
		root  string
		rel   string
	}{
		{"unix 普通路径", unixRules, whole, "/etc/passwd", "/", "etc/passwd"},
		{"unix 根目录本身", unixRules, whole, "/", "/", "."},
		{"unix .. 先归一再解析", unixRules, whole, "/srv/app/../../etc/x", "/", "etc/x"},
		{"unix ~ 展开", unixRules, whole, "~/work/a", "/", "home/me/work/a"},
		{"哨兵与其它目录并存时哨兵胜出", unixRules, []string{"/srv/app", termproto.FileRootAll, "/home/me"}, "/var/log/syslog", "/", "var/log/syslog"},
		{"win 任一盘符：C", winRules, whole, `C:\Windows\win.ini`, `C:\`, `Windows\win.ini`},
		{"win 任一盘符：D", winRules, whole, `D:\data\x`, `D:\`, `data\x`},
		{"win 盘符根本身（大小写与斜杠归一）", winRules, whole, `d:/`, `D:\`, "."},
		{"win ~ 展开", winRules, whole, `~\docs`, `C:\`, `Users\me\docs`},
	}
	for _, c := range cases {
		got, err := c.rules.resolve(c.roots, c.p)
		if err != nil {
			t.Errorf("%s: resolve(%q) 失败: %v", c.name, c.p, err)
			continue
		}
		if got.Root != c.root || got.Rel != c.rel || !got.Whole {
			t.Errorf("%s: resolve(%q) = %+v，期望 Root=%q Rel=%q Whole=true", c.name, c.p, got, c.root, c.rel)
		}
	}
}

// 整个文件系统放开的是「目录边界」，不是路径的写法：请求路径本身仍须是规范的绝对路径。
func TestWholeFSStillRequiresCanonicalAbsolutePaths(t *testing.T) {
	whole := []string{termproto.FileRootAll}
	for _, p := range []string{"", "rel/path", "./x", "~root/x", "~x", `C:\x`} {
		if _, err := unixRules.resolve(whole, p); err == nil || err.Code != termproto.FileErrBadPath {
			t.Errorf("unix resolve(%q) 应为 bad_path，实际 %v", p, err)
		}
	}
	for _, p := range []string{``, `\\server\share\f`, `\\?\C:\x`, `\\.\pipe\x`, `C:foo`, `foo`, `\rooted`, `/etc/passwd`, `C:\a:stream`} {
		if _, err := winRules.resolve(whole, p); err == nil || err.Code != termproto.FileErrBadPath {
			t.Errorf("windows resolve(%q) 应为 bad_path，实际 %v", p, err)
		}
	}
}

// 哨兵必须恰好是 "*"：看起来像它的条目（通配符、带空白、带前缀）只是普通的允许目录写法——
// 要么无法解析而被丢弃，要么是名字里恰好带 * 的真实目录，都放行不了目录之外的任何路径。
func TestWholeFSSentinelIsExactMatchOnly(t *testing.T) {
	for _, root := range []string{"/*", "*/", "**", " *", "* ", "/srv/*", "*.txt"} {
		if _, err := unixRules.resolve([]string{root}, "/etc/passwd"); err == nil || err.Code != termproto.FileErrNotAllowed {
			t.Errorf("root %q 不应放行任何路径: %v", root, err)
		}
	}
}

func wholeRoots() []string { return []string{termproto.FileRootAll} }

// volumeRoot 是测试目录所在卷的根：Unix 上是 "/"，Windows 上是 "C:\" 这样的盘符根。
func volumeRoot(p string) string { return filepath.VolumeName(p) + string(filepath.Separator) }

// 整个文件系统：任何位置都可读（含卷根本身）。这一组在所有平台上跑——包括真实的 Windows，不涉及符号链接。
func TestWholeFSReadsAnywhere(t *testing.T) {
	root, outside := layout(t)
	write(t, filepath.Join(outside, "secret.txt"), "s3cret")
	write(t, filepath.Join(root, "sub", "inside.txt"), "inside")
	s := newTestService(t, Config{})

	// 对照：只允许 root 时，目录之外是越界
	_, _, e := s.Open([]string{root}, filepath.Join(outside, "secret.txt"), 0)
	wantCode(t, e, termproto.FileErrNotAllowed)

	// 整个文件系统：直接读目录之外的文件
	whole := wholeRoots()
	st, e := s.Stat(whole, filepath.Join(outside, "secret.txt"))
	wantOK(t, e)
	if !st.Exists || st.Type != termproto.FileTypeFile || st.Size != 6 {
		t.Fatalf("stat = %+v", st)
	}
	id, size, e := s.Open(whole, filepath.Join(outside, "secret.txt"), 0)
	wantOK(t, e)
	if size != 6 {
		t.Fatalf("size = %d", size)
	}
	s.Close(id)
	hash, _, e := s.Hash(context.Background(), whole, filepath.Join(outside, "secret.txt"), 0)
	wantOK(t, e)
	if hash != sum("s3cret") {
		t.Fatalf("hash = %q", hash)
	}
	// 目录、卷根本身都是目录；打开目录是 is_directory
	for _, d := range []string{outside, volumeRoot(outside)} {
		st, e = s.Stat(whole, d)
		wantOK(t, e)
		if !st.Exists || st.Type != termproto.FileTypeDir {
			t.Fatalf("stat(%q) = %+v", d, st)
		}
		_, _, e = s.Open(whole, d, 0)
		wantCode(t, e, termproto.FileErrIsDir)
	}
	// 不存在的路径仍是 Exists=false 而不是错误
	st, e = s.Stat(whole, filepath.Join(outside, "nope"))
	wantOK(t, e)
	if st.Exists {
		t.Fatalf("不存在的路径: %+v", st)
	}
	// 路径写法仍须规范：相对路径照旧 bad_path
	_, e = s.Stat(whole, "relative.txt")
	wantCode(t, e, termproto.FileErrBadPath)
}

// 整个文件系统：符号链接照常解析（os.Root 会把绝对路径的链接当作逃逸，所以这个模式不用它）。仅 Unix。
func TestWholeFSFollowsSymlinks(t *testing.T) {
	skipWindows(t, "创建符号链接在 Windows 上需要额外权限")
	root, outside := layout(t)
	write(t, filepath.Join(outside, "secret.txt"), "s3cret")
	mustSymlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "abslink")) // 绝对路径的链接，指到允许目录之外
	mustSymlink(t, outside, filepath.Join(root, "dirlink"))
	s := newTestService(t, Config{})

	// 对照：只允许 root 时，经链接逃出去是越界
	_, _, e := s.Open([]string{root}, filepath.Join(root, "abslink"), 0)
	wantCode(t, e, termproto.FileErrNotAllowed)

	whole := wholeRoots()
	for _, p := range []string{filepath.Join(root, "abslink"), filepath.Join(root, "dirlink", "secret.txt")} {
		id, size, e := s.Open(whole, p, 0)
		wantOK(t, e)
		if size != 6 {
			t.Fatalf("%s: size = %d", p, size)
		}
		s.Close(id)
	}
	// Stat 仍按链接本身报告（不跟随）
	st, e := s.Stat(whole, filepath.Join(root, "abslink"))
	wantOK(t, e)
	if st.Type != termproto.FileTypeSymlink {
		t.Fatalf("symlink 应按链接本身报告: %+v", st)
	}
}

// 整个文件系统下的 push_url：能写到任意位置（含 Windows 的另一目录），「不覆盖 / 不留临时文件 / 目标是目录」的保护照旧。
// 所有平台都跑；符号链接目标的保护在下一个用例（仅 Unix）。
func TestWholeFSPushURLWritesAnywhere(t *testing.T) {
	srv := serve(t, body("payload-data"))
	e := newPushEnv(t, allowLocal())
	e.roots = wholeRoots()
	dest := filepath.Join(e.outside, "out.bin") // 在「允许目录」(e.root) 之外

	res, err := e.s.PushURL(context.Background(), e.roots, dest, srv.URL, PushOpts{})
	wantOK(t, err)
	if res.Size != 12 || res.SHA256 != sum("payload-data") {
		t.Fatalf("result = %+v", res)
	}
	if b, rerr := os.ReadFile(dest); rerr != nil || string(b) != "payload-data" {
		t.Fatalf("file = %q, %v", b, rerr)
	}
	if n := names(t, e.outside); len(n) != 1 || n[0] != "out.bin" {
		t.Fatalf("目录里不应有残留的临时文件: %v", n)
	}
	// 未允许覆盖：保持原样
	_, err = e.s.PushURL(context.Background(), e.roots, dest, srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrExists)
	// 允许覆盖：替换
	srv2 := serve(t, body("v2"))
	_, err = e.s.PushURL(context.Background(), e.roots, dest, srv2.URL, PushOpts{Overwrite: true})
	wantOK(t, err)
	if b, _ := os.ReadFile(dest); string(b) != "v2" {
		t.Fatalf("覆盖后内容 = %q", b)
	}
	// 目标是目录 / 目标的上级目录不存在 / 目标是卷根本身
	_, err = e.s.PushURL(context.Background(), e.roots, e.outside, srv.URL, PushOpts{Overwrite: true})
	wantCode(t, err, termproto.FileErrIsDir)
	_, err = e.s.PushURL(context.Background(), e.roots, filepath.Join(e.outside, "no", "such", "f"), srv.URL, PushOpts{})
	wantCode(t, err, termproto.FileErrNotFound)
	_, err = e.s.PushURL(context.Background(), e.roots, volumeRoot(e.outside), srv.URL, PushOpts{Overwrite: true})
	wantCode(t, err, termproto.FileErrIsDir)
	for _, n := range names(t, e.outside) {
		if strings.HasPrefix(n, ".rmh-") {
			t.Fatalf("残留临时文件: %v", names(t, e.outside))
		}
	}
}

// 整个文件系统下 push_url 的另一条保护：目标是符号链接时不经链接写入。仅 Unix。
func TestWholeFSPushURLRefusesSymlinkTarget(t *testing.T) {
	skipWindows(t, "创建符号链接在 Windows 上需要额外权限")
	srv := serve(t, body("payload-data"))
	e := newPushEnv(t, allowLocal())
	e.roots = wholeRoots()
	dest := filepath.Join(e.outside, "out.bin") // 在「允许目录」(e.root) 之外
	_, err := e.s.PushURL(context.Background(), e.roots, dest, srv.URL, PushOpts{})
	wantOK(t, err)

	link := filepath.Join(e.outside, "link.bin")
	mustSymlink(t, dest, link)
	srv2 := serve(t, body("v2"))
	_, err = e.s.PushURL(context.Background(), e.roots, link, srv2.URL, PushOpts{Overwrite: true})
	wantCode(t, err, termproto.FileErrIsSymlink)
	if b, _ := os.ReadFile(dest); string(b) != "payload-data" {
		t.Fatalf("链接目标不应被改写: %q", b)
	}
}
