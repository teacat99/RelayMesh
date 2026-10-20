package files

import (
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

var (
	unixRules = pathRules{home: "/home/me"}
	winRules  = pathRules{windows: true, home: `C:\Users\me`}
)

func TestAbsoluteUnix(t *testing.T) {
	ok := map[string]string{
		"/a/b/../c":     "/a/c",
		"/a//b/./c/":    "/a/b/c",
		"/":             "/",
		"~":             "/home/me",
		"~/x/y":         "/home/me/x/y",
		"/../../etc/pw": "/etc/pw",
	}
	for in, want := range ok {
		got, err := unixRules.absolute(in)
		if err != nil || got != want {
			t.Errorf("absolute(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "rel/path", "./x", "~root/x", "~x", `C:\x`} {
		if _, err := unixRules.absolute(in); err == nil || err.Code != termproto.FileErrBadPath {
			t.Errorf("absolute(%q) 应为 bad_path，实际 %v", in, err)
		}
	}
	if _, err := (pathRules{}).absolute("~/x"); err == nil || err.Code != termproto.FileErrBadPath {
		t.Errorf("没有家目录时 ~ 应为 bad_path，实际 %v", err)
	}
}

func TestAbsoluteWindows(t *testing.T) {
	ok := map[string]string{
		`C:\Data\..\x`:  `C:\x`,
		`c:/data/x`:     `C:\data\x`,
		`D:\`:           `D:\`,
		`~\docs`:        `C:\Users\me\docs`,
		`~/docs`:        `C:\Users\me\docs`,
		`C:\a\.\b\\c`:   `C:\a\b\c`,
		`C:\a\..\..\b`:  `C:\b`,
		`C:\Data\sub\f`: `C:\Data\sub\f`,
	}
	for in, want := range ok {
		got, err := winRules.absolute(in)
		if err != nil || got != want {
			t.Errorf("absolute(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		``, `\\server\share\f`, `\\?\C:\x`, `\\.\pipe\x`, `//server/share`, `C:foo`, `foo`, `\rooted`, `/etc/passwd`,
		`C:\a:stream`, `C:\file.txt::$DATA`, `C:\a.`, `C:\a \b`, `C:\dir\x.`, `~user\x`,
	} {
		if _, err := winRules.absolute(in); err == nil || err.Code != termproto.FileErrBadPath {
			t.Errorf("absolute(%q) 应为 bad_path，实际 %v", in, err)
		}
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		rules  pathRules
		root   string
		p      string
		rel    string
		inside bool
	}{
		{unixRules, "/srv/app", "/srv/app", ".", true},
		{unixRules, "/srv/app", "/srv/app/x/y", "x/y", true},
		{unixRules, "/srv/app", "/srv/app2/x", "", false}, // 前缀相同但不是同一个目录
		{unixRules, "/srv/app", "/srv", "", false},
		{unixRules, "/srv/app", "/etc/passwd", "", false},
		{unixRules, "/srv/App", "/srv/app/x", "", false}, // Unix 区分大小写
		{winRules, `C:\Data`, `C:\Data`, ".", true},
		{winRules, `C:\Data`, `c:\DATA\Sub\F.txt`, `Sub\F.txt`, true},
		{winRules, `C:\Data`, `C:\Data2\f`, "", false},
		{winRules, `C:\Data`, `D:\Data\f`, "", false},
	}
	for _, c := range cases {
		rel, ok := c.rules.within(c.root, c.p)
		if ok != c.inside || rel != c.rel {
			t.Errorf("within(%q, %q) = %q, %v; want %q, %v", c.root, c.p, rel, ok, c.rel, c.inside)
		}
	}
}

func TestResolve(t *testing.T) {
	roots := []string{"/srv/app/data", "/srv/app", "/", "relative/ignored", "~/work", ""}
	// 文件系统根、相对路径与空条目被忽略；重叠时取最宽的允许目录
	got, err := unixRules.resolve(roots, "/srv/app/data/x.txt")
	if err != nil || got.Root != "/srv/app" || got.Rel != "data/x.txt" {
		t.Fatalf("resolve = %+v, %v", got, err)
	}
	got, err = unixRules.resolve(roots, "~/work/a/b")
	if err != nil || got.Root != "/home/me/work" || got.Rel != "a/b" {
		t.Fatalf("resolve ~ = %+v, %v", got, err)
	}
	for _, p := range []string{"/etc/passwd", "/srv/appx/f", "/srv/app/../../etc/passwd", "/"} {
		if _, err := unixRules.resolve(roots, p); err == nil || err.Code != termproto.FileErrNotAllowed {
			t.Errorf("resolve(%q) 应为 path_not_allowed，实际 %v", p, err)
		}
	}
	// 只有文件系统根时，什么都不放行
	if _, err := unixRules.resolve([]string{"/"}, "/etc/passwd"); err == nil || err.Code != termproto.FileErrNotAllowed {
		t.Fatalf("根目录不应作为允许目录: %v", err)
	}
	if _, err := winRules.resolve([]string{`C:\`}, `C:\Windows\x`); err == nil || err.Code != termproto.FileErrNotAllowed {
		t.Fatalf("盘符根不应作为允许目录: %v", err)
	}
	if _, err := winRules.resolve(nil, `C:\x`); err == nil || err.Code != termproto.FileErrNotAllowed {
		t.Fatalf("没有允许目录时应拒绝: %v", err)
	}
	// 请求路径里的 .. 先归一再比较：不能借 .. 逃出允许目录
	if _, err := winRules.resolve([]string{`C:\Data`}, `C:\Data\..\Windows\win.ini`); err == nil || err.Code != termproto.FileErrNotAllowed {
		t.Fatalf(".. 逃逸应被拒绝: %v", err)
	}
}

func TestPublicAddr(t *testing.T) {
	public := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111", "2a00:1450:4009::200e"}
	blocked := []string{
		"0.0.0.0", "127.0.0.1", "127.255.255.254", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "100.127.255.255", "192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.7", "203.0.113.9", "224.0.0.1",
		"240.0.0.1", "255.255.255.255", "::", "::1", "::7f00:1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::7f00:1", "64:ff9b::a00:1", "2002:7f00:1::1", "2001::1", "2001:db8::1", "fc00::1", "fd12:3456::1", "fe80::1",
		"fe80::1%eth0", "fec0::1", "ff02::1", "100::1",
	}
	for _, s := range public {
		if a := mustAddr(t, s); !publicAddr(a) {
			t.Errorf("%s 应视为公网地址", s)
		}
	}
	for _, s := range blocked {
		if a := mustAddr(t, s); publicAddr(a) {
			t.Errorf("%s 应视为非公网地址", s)
		}
	}
}
