//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modShell32                = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW       = modShell32.NewProc("ShellExecuteExW")
	procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100 // 同步完成：否则未初始化 COM 的调用线程上，ShellExecuteEx 可能转到后台线程，hProcess 来不及填
	seeMaskFlagNoUI       = 0x00000400 // 只抑制错误弹窗；UAC 确认本身仍会出现

	coSFalse = 1 // CoInitializeEx：该线程已经初始化过（仍需配对 CoUninitialize）
)

// shellExecuteInfo 对应 SHELLEXECUTEINFOW。
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

// classifyShellExecuteError：UAC 里选「否」时 ShellExecuteEx 失败并给出 ERROR_CANCELLED，这不是故障，调用方应当回退到当前权限。
func classifyShellExecuteError(err error) error {
	if errors.Is(err, windows.ERROR_CANCELLED) {
		return errElevationDeclined
	}
	return fmt.Errorf("ShellExecuteEx: %w", err)
}

// shellExecuteWait 用给定动词启动程序并等待它退出，返回退出码。verb 为 "runas" 时会触发 UAC。
func shellExecuteWait(verb, file, params, dir string) (uint32, error) {
	// 按微软文档的建议：在固定的 OS 线程上以 STA 初始化 COM 再调用 ShellExecuteEx（外壳扩展可能用到 COM）。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil || errors.Is(err, syscall.Errno(coSFalse)) {
		defer windows.CoUninitialize()
	}

	ptr := func(s string) (*uint16, error) {
		if s == "" {
			return nil, nil
		}
		return windows.UTF16PtrFromString(s)
	}
	v, err := ptr(verb)
	if err != nil {
		return 0, err
	}
	f, err := ptr(file)
	if err != nil {
		return 0, err
	}
	p, err := ptr(params)
	if err != nil {
		return 0, err
	}
	d, err := ptr(dir)
	if err != nil {
		return 0, err
	}
	sei := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI, lpVerb: v, lpFile: f, lpParameters: p, lpDirectory: d, nShow: windows.SW_SHOWNORMAL}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	r, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if r == 0 {
		return 0, classifyShellExecuteError(callErr)
	}
	if sei.hProcess == 0 {
		return 0, errors.New("没有拿到子进程句柄")
	}
	defer windows.CloseHandle(sei.hProcess)
	if ev, err := windows.WaitForSingleObject(sei.hProcess, windows.INFINITE); err != nil || ev != windows.WAIT_OBJECT_0 {
		return 0, fmt.Errorf("等待子进程失败（%d，%v）", ev, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.hProcess, &code); err != nil {
		return 0, err
	}
	return code, nil
}

func relaunchElevatedImpl(_ Env, args []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	dir, _ := os.Getwd()
	code, err := shellExecuteWait("runas", exe, windowsJoinArgs(args), dir)
	return int(code), err
}

// ownsConsoleImpl：附着在当前控制台的进程只有自己时，窗口是为它单独开的（双击启动或 UAC 提权子进程）。
func ownsConsoleImpl() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
