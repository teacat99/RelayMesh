//go:build windows

package cli

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// 助手进程：由 TestShellExecuteWaitPropagatesExitCode 经 ShellExecuteEx 再次启动本测试二进制。
func TestShellExecuteHelperProcess(t *testing.T) {
	if os.Getenv("RM_SHELLEXEC_HELPER") != "1" {
		t.Skip("仅作为助手进程运行")
	}
	os.Exit(7)
}

// 用不会触发 UAC 的 "open" 动词验证：ShellExecuteEx 的等待与退出码透传这条管线。
// 真正的 "runas" 需要用户在 UAC 弹窗里确认，无法在自动化里执行。
func TestShellExecuteWaitPropagatesExitCode(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RM_SHELLEXEC_HELPER", "1")
	dir, _ := os.Getwd()
	code, err := shellExecuteWait("open", exe, "-test.run=TestShellExecuteHelperProcess$", dir)
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Fatalf("退出码 = %d，期望 7", code)
	}
}

func TestShellExecuteWaitReportsLaunchFailure(t *testing.T) {
	_, err := shellExecuteWait("open", `C:\definitely\not\here\relaymesh-missing.exe`, "", "")
	if err == nil {
		t.Fatal("启动不存在的程序应当报错")
	}
	if errors.Is(err, errElevationDeclined) {
		t.Fatalf("启动失败不应被当成用户拒绝: %v", err)
	}
}

func TestDeclinedUACMapsToSentinel(t *testing.T) {
	// ERROR_CANCELLED（1223）是 UAC 里选「否」时 ShellExecuteEx 给出的错误码。
	if err := classifyShellExecuteError(syscall.Errno(1223)); !errors.Is(err, errElevationDeclined) {
		t.Fatalf("1223 应映射为用户拒绝，得到 %v", err)
	}
	other := classifyShellExecuteError(syscall.Errno(5)) // ERROR_ACCESS_DENIED
	if errors.Is(other, errElevationDeclined) || !errors.Is(other, syscall.Errno(5)) {
		t.Fatalf("其他错误应原样保留: %v", other)
	}
}

func TestIsTerminalFileRejectsNUL(t *testing.T) {
	nul, err := os.Open("NUL")
	if err != nil {
		t.Fatal(err)
	}
	defer nul.Close()
	if isTerminalFile(nul) {
		t.Fatal("NUL 不是终端：重定向到 NUL 的脚本不该被当成交互式（否则会卡在 UAC 或提示上）")
	}
	if isInteractive(nul) {
		t.Fatal("isInteractive(NUL) 应为假")
	}
}

func TestOwnsConsoleDoesNotPanic(t *testing.T) {
	// go test 与 go 工具共用控制台，结果可真可假；这里只确保系统调用可用。
	_ = ownsConsoleImpl()
}
