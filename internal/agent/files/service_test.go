package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("bad test address %q: %v", s, err)
	}
	return a
}

func wantCode(t *testing.T, e *Error, code string) {
	t.Helper()
	if e == nil {
		t.Fatalf("期望错误码 %s，实际成功", code)
	}
	if e.Code != code {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", code, e.Code, e.Msg)
	}
}

func wantOK(t *testing.T, e *Error) {
	t.Helper()
	if e != nil {
		t.Fatalf("不应失败: %s (%s)", e.Code, e.Msg)
	}
}

func newTestService(t *testing.T, cfg Config) *Service {
	t.Helper()
	s := New(cfg)
	t.Cleanup(s.Shutdown)
	return s
}

// layout 建一个允许目录 root 与一个目录外的 outside，返回两者的路径。
func layout(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(root, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, outside
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skipWindows(t *testing.T, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip(why)
	}
}

func TestStat(t *testing.T) {
	root, outside := layout(t)
	write(t, filepath.Join(root, "a.txt"), "hello")
	write(t, filepath.Join(outside, "secret.txt"), "s3cret")
	s := newTestService(t, Config{})
	roots := []string{root}

	st, e := s.Stat(roots, filepath.Join(root, "a.txt"))
	wantOK(t, e)
	if !st.Exists || st.Type != termproto.FileTypeFile || st.Size != 5 || st.MTime == 0 {
		t.Fatalf("stat file = %+v", st)
	}
	st, e = s.Stat(roots, filepath.Join(root, "sub"))
	wantOK(t, e)
	if !st.Exists || st.Type != termproto.FileTypeDir || st.Size != 0 {
		t.Fatalf("stat dir = %+v", st)
	}
	st, e = s.Stat(roots, root)
	wantOK(t, e)
	if !st.Exists || st.Type != termproto.FileTypeDir {
		t.Fatalf("stat root = %+v", st)
	}
	st, e = s.Stat(roots, filepath.Join(root, "nope"))
	wantOK(t, e)
	if st.Exists {
		t.Fatalf("不存在的路径不应报告存在: %+v", st)
	}
	// 允许目录本身不存在：按「不存在」处理，而不是报授权错误
	st, e = s.Stat([]string{filepath.Join(root, "gone")}, filepath.Join(root, "gone", "x"))
	wantOK(t, e)
	if st.Exists {
		t.Fatalf("根不存在时应为 exists=false: %+v", st)
	}
	_, e = s.Stat(roots, filepath.Join(outside, "secret.txt"))
	wantCode(t, e, termproto.FileErrNotAllowed)
	_, e = s.Stat(roots, filepath.Join(root, "..", "outside", "secret.txt"))
	wantCode(t, e, termproto.FileErrNotAllowed)
	_, e = s.Stat(roots, "relative.txt")
	wantCode(t, e, termproto.FileErrBadPath)
}

func TestStatReportsSymlinkWithoutFollowing(t *testing.T) {
	skipWindows(t, "创建符号链接需要特权")
	root, outside := layout(t)
	write(t, filepath.Join(outside, "secret.txt"), "s3cret")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	s := newTestService(t, Config{})
	st, e := s.Stat([]string{root}, filepath.Join(root, "link"))
	wantOK(t, e)
	if !st.Exists || st.Type != termproto.FileTypeSymlink || st.Size != 0 {
		t.Fatalf("symlink 应按链接本身报告（不跟随、不泄露目标大小）: %+v", st)
	}
}

func TestSymlinkEscapeIsDenied(t *testing.T) {
	skipWindows(t, "创建符号链接需要特权")
	root, outside := layout(t)
	write(t, filepath.Join(outside, "secret.txt"), "s3cret")
	write(t, filepath.Join(root, "sub", "inside.txt"), "inside")
	mustSymlink(t, outside, filepath.Join(root, "dirlink"))                               // 目录链接指到外面
	mustSymlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "filelink")) // 文件链接指到外面
	mustSymlink(t, "../outside/secret.txt", filepath.Join(root, "rellink"))               // 相对链接指到外面
	mustSymlink(t, "sub", filepath.Join(root, "okdir"))                                   // 相对链接指向允许目录内部：合法
	mustSymlink(t, filepath.Join(root, "sub"), filepath.Join(root, "abslink"))            // 绝对路径的链接：即便指向内部也一律不跟随（宁严勿松）
	mustSymlink(t, filepath.Join(root, "loop"), filepath.Join(root, "loop"))              // 自指的循环
	s := newTestService(t, Config{})
	roots := []string{root}

	// 目录链接指到外面：经过它的任何访问都被拒绝
	through := filepath.Join(root, "dirlink", "secret.txt")
	_, e := s.Stat(roots, through)
	wantCode(t, e, termproto.FileErrNotAllowed)
	_, _, e = s.Open(roots, through, 0)
	wantCode(t, e, termproto.FileErrNotAllowed)
	_, _, e = s.Hash(context.Background(), roots, through, 0)
	wantCode(t, e, termproto.FileErrNotAllowed)
	// 文件链接指到外面：链接本身在允许目录里，Stat 如实报告为链接；读取与摘要才会被拒绝
	for _, link := range []string{"filelink", "rellink"} {
		p := filepath.Join(root, link)
		st, e := s.Stat(roots, p)
		wantOK(t, e)
		if st.Type != termproto.FileTypeSymlink {
			t.Fatalf("%s: stat = %+v", link, st)
		}
		_, _, e = s.Open(roots, p, 0)
		wantCode(t, e, termproto.FileErrNotAllowed)
		_, _, e = s.Hash(context.Background(), roots, p, 0)
		wantCode(t, e, termproto.FileErrNotAllowed)
	}
	// 绝对路径的符号链接即便指向允许目录内部也不被跟随（os.Root 的固有策略：无法证明它不会被替换成别处）
	_, _, e = s.Open(roots, filepath.Join(root, "abslink", "inside.txt"), 0)
	wantCode(t, e, termproto.FileErrNotAllowed)
	// 指回允许目录内部的相对链接是合法的
	id, size, e := s.Open(roots, filepath.Join(root, "okdir", "inside.txt"), 0)
	wantOK(t, e)
	if size != 6 {
		t.Fatalf("size = %d", size)
	}
	s.Close(id)
	// 循环链接不会卡住，也不会泄露
	_, _, e = s.Open(roots, filepath.Join(root, "loop"), 0)
	if e == nil || (e.Code != termproto.FileErrNotAllowed && e.Code != termproto.FileErrIO) {
		t.Fatalf("循环链接应被拒绝: %v", e)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReadClose(t *testing.T) {
	root, _ := layout(t)
	content := strings.Repeat("0123456789", 5000) // 50000 字节
	p := filepath.Join(root, "big.bin")
	write(t, p, content)
	s := newTestService(t, Config{})
	roots := []string{root}

	id, size, e := s.Open(roots, p, 0)
	wantOK(t, e)
	if size != int64(len(content)) || !termproto.ValidFileHandle(id) {
		t.Fatalf("open = %q, %d", id, size)
	}
	var got []byte
	var off uint64
	for {
		data, eof, total, e := s.Read(id, off, 12345)
		wantOK(t, e)
		if total != uint64(len(content)) {
			t.Fatalf("total = %d", total)
		}
		got = append(got, data...)
		off += uint64(len(data))
		if eof {
			break
		}
		if len(data) == 0 {
			t.Fatal("没有进展")
		}
	}
	if string(got) != content {
		t.Fatalf("内容不一致 (%d vs %d)", len(got), len(content))
	}
	// 越界读取：eof、无数据
	data, eof, _, e := s.Read(id, uint64(len(content))+10, 100)
	wantOK(t, e)
	if !eof || len(data) != 0 {
		t.Fatalf("越界读取 = %d bytes, eof=%v", len(data), eof)
	}
	s.Close(id)
	s.Close(id) // 重复关闭无害
	_, _, _, e = s.Read(id, 0, 10)
	wantCode(t, e, termproto.FileErrBadHandle)
	_, _, _, e = s.Read("nonexistent", 0, 10)
	wantCode(t, e, termproto.FileErrBadHandle)
}

func TestOpenRejectsNonRegularAndOversize(t *testing.T) {
	root, _ := layout(t)
	write(t, filepath.Join(root, "f.txt"), "0123456789")
	s := newTestService(t, Config{})
	roots := []string{root}

	_, _, e := s.Open(roots, filepath.Join(root, "sub"), 0)
	wantCode(t, e, termproto.FileErrIsDir)
	_, _, e = s.Open(roots, root, 0)
	wantCode(t, e, termproto.FileErrIsDir)
	_, _, e = s.Open(roots, filepath.Join(root, "missing"), 0)
	wantCode(t, e, termproto.FileErrNotFound)
	_, _, e = s.Open(roots, filepath.Join(root, "f.txt", "x"), 0)
	wantCode(t, e, termproto.FileErrNotFound) // 上级是文件而不是目录
	_, _, e = s.Open(roots, filepath.Join(root, "f.txt"), 9)
	wantCode(t, e, termproto.FileErrTooLarge)
	id, _, e := s.Open(roots, filepath.Join(root, "f.txt"), 10)
	wantOK(t, e)
	s.Close(id)
}

func TestHash(t *testing.T) {
	root, _ := layout(t)
	content := strings.Repeat("abc", 1<<19) // 约 1.5 MiB，跨多个缓冲区
	p := filepath.Join(root, "h.bin")
	write(t, p, content)
	s := newTestService(t, Config{})
	roots := []string{root}

	sum, size, e := s.Hash(context.Background(), roots, p, 0)
	wantOK(t, e)
	want := sha256.Sum256([]byte(content))
	if sum != hex.EncodeToString(want[:]) || size != int64(len(content)) {
		t.Fatalf("hash = %s, %d", sum, size)
	}
	_, _, e = s.Hash(context.Background(), roots, p, 1000)
	wantCode(t, e, termproto.FileErrTooLarge)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, e = s.Hash(ctx, roots, p, 0)
	wantCode(t, e, termproto.FileErrTimeout)
	_, _, e = s.Hash(context.Background(), roots, filepath.Join(root, "sub"), 0)
	wantCode(t, e, termproto.FileErrIsDir)
}

func TestHandleLimitAndIdleSweep(t *testing.T) {
	root, _ := layout(t)
	p := filepath.Join(root, "f")
	write(t, p, "x")
	s := newTestService(t, Config{HandleIdle: time.Second})
	roots := []string{root}

	var ids []string
	for i := 0; i < maxOpenHandles; i++ {
		id, _, e := s.Open(roots, p, 0)
		wantOK(t, e)
		ids = append(ids, id)
	}
	_, _, e := s.Open(roots, p, 0)
	wantCode(t, e, termproto.FileErrBusy)
	s.sweep(time.Now().Add(500 * time.Millisecond)) // 还没到空闲时间
	if _, _, _, e := s.Read(ids[0], 0, 1); e != nil {
		t.Fatalf("未过期的句柄不应被回收: %v", e)
	}
	s.sweep(time.Now().Add(time.Hour))
	for _, id := range ids {
		_, _, _, e := s.Read(id, 0, 1)
		wantCode(t, e, termproto.FileErrBadHandle)
	}
	id, _, e := s.Open(roots, p, 0) // 回收后可以再开
	wantOK(t, e)
	s.Close(id)
}

func TestShutdownClosesHandlesAndRejectsNewWork(t *testing.T) {
	root, _ := layout(t)
	p := filepath.Join(root, "f")
	write(t, p, "x")
	s := New(Config{})
	id, _, e := s.Open([]string{root}, p, 0)
	wantOK(t, e)
	s.Shutdown()
	s.Shutdown() // 幂等
	_, _, _, e = s.Read(id, 0, 1)
	wantCode(t, e, termproto.FileErrBadHandle)
	_, _, e = s.Open([]string{root}, p, 0)
	wantCode(t, e, termproto.FileErrBusy)
}

func TestHomeExpansionInRoots(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "note.txt"), "hi")
	skipWindows(t, "Windows 的家目录写法在 paths_test 中覆盖")
	s := newTestService(t, Config{Home: home})
	st, e := s.Stat([]string{"~/"}, "~/note.txt")
	wantOK(t, e)
	if !st.Exists || st.Size != 2 {
		t.Fatalf("stat via ~ = %+v", st)
	}
}
