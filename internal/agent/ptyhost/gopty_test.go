//go:build !windows

package ptyhost

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustSpawn(t *testing.T, spec Spec) Terminal {
	t.Helper()
	term, err := NewSpawner().Spawn(spec)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { term.Kill(); _ = term.Close() })
	return term
}

// readAll 读到 EOF / EIO（从端全部关闭）或超时为止。
func readAll(t *testing.T, term Terminal, timeout time.Duration) string {
	t.Helper()
	_ = term.SetReadDeadline(time.Now().Add(timeout))
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := term.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			return out.String()
		}
	}
}

func TestSpawnOutputAndExitCode(t *testing.T) {
	term := mustSpawn(t, Spec{Path: "/bin/sh", Args: []string{"-c", "printf 'hello-pty'; exit 3"}})
	out := readAll(t, term, 5*time.Second)
	if !strings.Contains(out, "hello-pty") {
		t.Fatalf("输出缺失: %q", out)
	}
	st := term.Wait()
	if st.Code != 3 || st.Signal != "" {
		t.Fatalf("退出状态不符: %+v", st)
	}
	if term.Pid() <= 0 {
		t.Fatal("pid 应大于 0")
	}
}

// 子进程退出后，读端应当在排空残余输出后以错误结束（父进程已关闭从端），而不是永远阻塞。
func TestReadEndsAfterChildExit(t *testing.T) {
	term := mustSpawn(t, Spec{Path: "/bin/sh", Args: []string{"-c", "printf tail-data"}})
	_ = term.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got bytes.Buffer
	buf := make([]byte, 256)
	var lastErr error
	for {
		n, err := term.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			lastErr = err
			break
		}
	}
	if !strings.Contains(got.String(), "tail-data") {
		t.Fatalf("残余输出丢失: %q", got.String())
	}
	if errors.Is(lastErr, os.ErrDeadlineExceeded) {
		t.Fatalf("读端应在子进程退出后自然结束，而不是等到超时: %v", lastErr)
	}
}

func TestInitialSizeAndResize(t *testing.T) {
	// 进程启动时读到的就应是请求的尺寸；随后的 Resize 也应生效。
	term := mustSpawn(t, Spec{Path: "/bin/sh", Args: []string{"-c", "stty size; read x; stty size"}, Rows: 30, Cols: 100})
	_ = term.SetReadDeadline(time.Now().Add(5 * time.Second))
	first := readUntil(t, term, "30 100")
	if !strings.Contains(first, "30 100") {
		t.Fatalf("初始尺寸不符: %q", first)
	}
	if err := term.Resize(12, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	rest := readUntil(t, term, "12 40")
	if !strings.Contains(rest, "12 40") {
		t.Fatalf("Resize 后尺寸不符: %q", rest)
	}
}

func readUntil(t *testing.T, term Terminal, want string) string {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, 1024)
	for !strings.Contains(out.String(), want) {
		n, err := term.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return out.String()
}

func TestWriteReachesChild(t *testing.T) {
	term := mustSpawn(t, Spec{Path: "/bin/cat"})
	if _, err := term.Write([]byte("ping-line\n")); err != nil {
		t.Fatal(err)
	}
	_ = term.SetReadDeadline(time.Now().Add(5 * time.Second))
	out := readUntil(t, term, "ping-line")
	if !strings.Contains(out, "ping-line") {
		t.Fatalf("输入未被回显: %q", out)
	}
}

func TestKillTerminatesProcessGroup(t *testing.T) {
	// shell 启动一个忽略 SIGHUP 的后台子进程；Kill 必须连它一起结束（宽限期后 SIGKILL）。
	term := mustSpawn(t, Spec{Path: "/bin/sh", Args: []string{"-c", "trap '' HUP; sleep 300 & wait"}})
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	term.Kill()
	done := make(chan ExitStatus, 1)
	go func() { done <- term.Wait() }()
	select {
	case st := <-done:
		if st.Code != -1 || st.Signal == "" {
			t.Fatalf("应为信号终止: %+v", st)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Kill 后进程没有在期限内退出")
	}
	if time.Since(start) > 6*time.Second {
		t.Fatalf("终止过慢: %v", time.Since(start))
	}
}

func TestCloseUnblocksRead(t *testing.T) {
	term, err := NewSpawner().Spawn(Spec{Path: "/bin/sh", Args: []string{"-c", "sleep 300"}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Kill()
	errc := make(chan error, 1)
	go func() {
		_, err := term.Read(make([]byte, 16))
		errc <- err
	}()
	time.Sleep(200 * time.Millisecond)
	_ = term.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Close 之后 Read 应返回错误")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close 没有解除阻塞的 Read")
	}
}

func TestSpawnFailure(t *testing.T) {
	if _, err := NewSpawner().Spawn(Spec{Path: "/nonexistent/definitely-not-here"}); err == nil {
		t.Fatal("不存在的程序应返回错误")
	}
	if _, err := NewSpawner().Spawn(Spec{}); err == nil {
		t.Fatal("空路径应返回错误")
	}
}

func TestEnvAndDirApplied(t *testing.T) {
	dir := t.TempDir()
	term := mustSpawn(t, Spec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo V=$RM_TEST_VAR; pwd"},
		Env:  []string{"RM_TEST_VAR=xyz", "PATH=/usr/bin:/bin"},
		Dir:  dir,
	})
	out := readAll(t, term, 5*time.Second)
	if !strings.Contains(out, "V=xyz") || !strings.Contains(out, dir) {
		t.Fatalf("环境或工作目录未生效: %q", out)
	}
}

// 伪终端必须带 IUTF8：浏览器发来的是 UTF-8，否则不带 readline 的程序（cat、read）里退格只删中文的一个字节。
func TestSpawnEnablesIUTF8(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("stty 对 iutf8 的输出只在 Linux 上稳定")
	}
	term := mustSpawn(t, Spec{Path: "/bin/sh", Args: []string{"-c", "stty -a"}, Env: []string{"PATH=/usr/bin:/bin"}})
	out := readAll(t, term, 5*time.Second)
	if !strings.Contains(out, " iutf8") || strings.Contains(out, "-iutf8") {
		t.Fatalf("新建的伪终端应打开 iutf8: %q", out)
	}
}

// 读期限必须真正生效：这是进程退出后「有界排空」的基础。
func TestReadDeadlineWorks(t *testing.T) {
	term := mustSpawn(t, Spec{Path: "/bin/sh", Args: []string{"-c", "sleep 300"}})
	if err := term.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	start := time.Now()
	_, err := term.Read(make([]byte, 16))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("应返回期限错误，得到 %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("期限过了很久才返回: %v", d)
	}
}
