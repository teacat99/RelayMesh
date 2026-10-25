//go:build windows

package ptyhost

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Windows 上的 ConPTY 不支持读期限，所以用后台协程持续收集输出，测试按条件轮询。

type collector struct {
	mu   sync.Mutex
	buf  strings.Builder
	done chan struct{}
}

func collect(term Terminal) *collector {
	c := &collector{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		b := make([]byte, 4096)
		for {
			n, err := term.Read(b)
			if n > 0 {
				c.mu.Lock()
				c.buf.Write(b[:n])
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *collector) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (c *collector) waitFor(t *testing.T, sub string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(c.text(), sub) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("等待输出 %q 超时，已收到: %q", sub, c.text())
}

// queryUntil 向终端写入 query，并等待 want 出现；每隔 retryEvery 仍没出现就重发一次，总共最多等 total。
//
// 为什么要重发：Windows PowerShell 刚启动时，写得太早的输入可能落在它初始化控制台的那一刻而被丢掉
// （托管 runner 上冷启动还可能慢到十几秒），表现为「等不到输出」，并不是被测代码的问题。重发是安全的：
// 这类查询只会多打印几行结果，期望的输出只要出现一次就算通过。
func (c *collector) queryUntil(t *testing.T, term Terminal, query, want string, total, retryEvery time.Duration) {
	t.Helper()
	deadline := time.Now().Add(total)
	for {
		if _, err := term.Write([]byte(query)); err != nil {
			t.Fatal(err)
		}
		retryAt := time.Now().Add(retryEvery)
		for time.Now().Before(retryAt) {
			if strings.Contains(c.text(), want) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待输出 %q 超时（重发查询仍无结果），已收到: %q", want, c.text())
		}
	}
}

func mustSpawnWin(t *testing.T, spec Spec) (Terminal, *collector) {
	t.Helper()
	term, err := NewSpawner().Spawn(spec)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { term.Kill(); _ = term.Close() })
	return term, collect(term)
}

func waitExit(t *testing.T, term Terminal, d time.Duration) ExitStatus {
	t.Helper()
	ch := make(chan ExitStatus, 1)
	go func() { ch <- term.Wait() }()
	select {
	case st := <-ch:
		return st
	case <-time.After(d):
		t.Fatal("子进程未在预期时间内退出")
		return ExitStatus{}
	}
}

func TestWindowsSpawnOutputAndExitCode(t *testing.T) {
	term, out := mustSpawnWin(t, Spec{Path: `C:\Windows\System32\cmd.exe`, Args: []string{"/c", "echo hello-conpty & exit 3"}})
	out.waitFor(t, "hello-conpty", 10*time.Second)
	if st := waitExit(t, term, 10*time.Second); st.Code != 3 {
		t.Fatalf("退出码应为 3: %+v", st)
	}
}

func TestWindowsWriteReachesChild(t *testing.T) {
	term, out := mustSpawnWin(t, Spec{Path: `C:\Windows\System32\cmd.exe`, Args: []string{"/q"}})
	if _, err := term.Write([]byte("echo roundtrip-ok\r\n")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "roundtrip-ok", 10*time.Second)
	if _, err := term.Write([]byte("exit 5\r\n")); err != nil {
		t.Fatal(err)
	}
	if st := waitExit(t, term, 10*time.Second); st.Code != 5 {
		t.Fatalf("退出码应为 5: %+v", st)
	}
}

func TestWindowsInitialSizeAndResize(t *testing.T) {
	term, out := mustSpawnWin(t, Spec{
		Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		Args: []string{"-NoProfile", "-NoLogo"},
		Rows: 30, Cols: 100,
	})
	query := "'W=' + $Host.UI.RawUI.WindowSize.Width + ' H=' + $Host.UI.RawUI.WindowSize.Height\r\n"

	// 先等提示符出现再发查询：冷启动慢的 runner 上 PowerShell 需要十几秒才就绪，输入写得太早还可能被它的初始化吞掉
	// （这个用例在公开库 CI 上出现过「只收到 ConPTY 的初始化序列，一直没有提示符与回显」的间歇失败）。
	start := time.Now()
	out.waitFor(t, "PS ", 90*time.Second)
	t.Logf("PowerShell 提示符出现用了 %v", time.Since(start).Round(10*time.Millisecond))

	out.queryUntil(t, term, query, "W=100 H=30", 30*time.Second, 5*time.Second)
	if err := term.Resize(40, 132); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	out.queryUntil(t, term, query, "W=132 H=40", 30*time.Second, 5*time.Second)
}

func TestWindowsKillTerminatesChildPromptly(t *testing.T) {
	term, _ := mustSpawnWin(t, Spec{Path: `C:\Windows\System32\cmd.exe`, Args: []string{"/c", "ping -n 60 127.0.0.1 >nul"}})
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	term.Kill()
	waitExit(t, term, 10*time.Second)
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("Kill 后应尽快退出，实际 %v", d)
	}
}

func TestWindowsCloseUnblocksRead(t *testing.T) {
	term, err := NewSpawner().Spawn(Spec{Path: `C:\Windows\System32\cmd.exe`, Args: []string{"/c", "ping -n 60 127.0.0.1 >nul"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Kill() })
	readDone := make(chan error, 1)
	go func() {
		b := make([]byte, 1024)
		for {
			if _, err := term.Read(b); err != nil {
				readDone <- err
				return
			}
		}
	}()
	time.Sleep(500 * time.Millisecond)
	_ = term.Close()
	select {
	case <-readDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Close 应解除阻塞中的 Read")
	}
}

func TestWindowsSpawnFailure(t *testing.T) {
	if _, err := NewSpawner().Spawn(Spec{Path: `C:\definitely\not\here.exe`}); err == nil {
		t.Fatal("可执行文件不存在时应返回错误")
	}
}

func TestWindowsEnvAndDirApplied(t *testing.T) {
	dir := t.TempDir()
	env := append(os.Environ(), "RM_TEST_VAR=xyz-123")
	term, out := mustSpawnWin(t, Spec{
		Path: `C:\Windows\System32\cmd.exe`,
		Args: []string{"/c", "echo VAR=%RM_TEST_VAR% & cd"},
		Env:  env,
		Dir:  dir,
	})
	out.waitFor(t, "VAR=xyz-123", 10*time.Second)
	out.waitFor(t, dir, 10*time.Second)
	waitExit(t, term, 10*time.Second)
}

func TestWindowsUnicodeRoundTrip(t *testing.T) {
	term, out := mustSpawnWin(t, Spec{
		Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		Args: []string{"-NoProfile", "-NoLogo"},
	})
	if _, err := term.Write([]byte("Write-Output ('U=' + '中文终端')\r\n")); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "U=中文终端", 15*time.Second)
	if _, err := term.Write([]byte("exit\r\n")); err != nil {
		t.Fatal(err)
	}
	waitExit(t, term, 10*time.Second)
}
