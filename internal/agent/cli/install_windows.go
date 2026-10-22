//go:build windows

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
)

// Windows 的真实系统调用：schtasks、进程枚举、OEM 代码页解码。流程逻辑在 wintask.go，可跨平台测试。

func winEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func realWinOps() (winOps, error) {
	exe, err := os.Executable()
	if err != nil {
		return winOps{}, fmt.Errorf("无法确定当前可执行文件: %w", err)
	}
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username // Windows 上形如 DOMAIN\user
	}
	home, _ := os.UserHomeDir()
	return winOps{
		elevated:    agent.IsElevated(),
		username:    name,
		progFiles:   winEnv("ProgramFiles", `C:\Program Files`),
		localApp:    winEnv("LocalAppData", filepath.Join(home, "AppData", "Local")),
		programData: winEnv("ProgramData", `C:\ProgramData`),
		exe:         exe,
		tempDir:     os.TempDir(),
		run:         runOEM,
		procs:       runningImages,
		sleep:       time.Sleep,
		now:         time.Now,

		deferCleanup: spawnCleanup,
	}, nil
}

// spawnCleanup 启动一个隐藏、脱离本进程的 cmd：等本进程退出后删除文件并尝试删除空目录。
// 正在运行的 exe 无法被删除，卸载「自己」时只能这样收尾。
func spawnCleanup(paths, dirs []string) error {
	var b strings.Builder
	b.WriteString("ping -n 3 127.0.0.1 >nul")
	for _, p := range paths {
		fmt.Fprintf(&b, ` & del /F /Q "%s"`, p)
	}
	for _, d := range dirs {
		fmt.Fprintf(&b, ` & rmdir "%s" 2>nul`, d)
	}
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe /D /S /C "` + b.String() + `"`,
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// runOEM 执行命令并把输出按 OEM 代码页解码为 UTF-8：schtasks 等系统工具在管道里按 OEM 代码页输出
// （中文系统是 GBK），直接当 UTF-8 展示会是乱码。
func runOEM(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return decodeOEM(out), err
}

var procGetOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

func decodeOEM(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	cp := uint32(1) // CP_OEMCP；GetOEMCP 失败时的兜底
	if r, _, _ := procGetOEMCP.Call(); r != 0 {
		cp = uint32(r)
	}
	n, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n <= 0 {
		return string(b)
	}
	buf := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(cp, 0, &b[0], int32(len(b)), &buf[0], n); err != nil {
		return string(b)
	}
	return windows.UTF16ToString(buf)
}

// runningImages 返回映像完整路径等于 image 的所有「其他」进程的 PID（路径不区分大小写；不含本进程自己）。
func runningImages(image string) []int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	base := strings.ToLower(filepath.Base(image))
	want := strings.ToLower(filepath.Clean(image))
	self := uint32(os.Getpid())
	var pids []int
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if strings.ToLower(windows.UTF16ToString(pe.ExeFile[:])) != base || pe.ProcessID == self {
			// 本进程自己不算「别的实例」：远程卸载时，卸载命令就是以已安装的无窗口版运行的
			continue
		}
		if full, ok := processImagePath(pe.ProcessID); ok && strings.ToLower(filepath.Clean(full)) == want {
			pids = append(pids, int(pe.ProcessID))
		}
	}
	return pids
}

func processImagePath(pid uint32) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buf[:size]), true
}

// ---------- 命令入口 ----------

func installWindows(_ context.Context, env Env, c common, _ resolved) int {
	w, err := realWinOps()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	res, err := doInstallWindows(env, c, w)
	if err != nil {
		env.errorf("安装失败: %v\n", err)
		return 1
	}
	env.printf("✅ 已安装 RelayMesh Agent（Windows 计划任务）\n")
	env.printf("  机器码    : %s\n", res.MachineCode)
	switch {
	case res.User && res.Highest:
		env.printf("  范围      : 当前用户 + 最高权限（登录 Windows 时自动启动，注销后停止；以你的账户身份与环境运行，并带完整管理员令牌，无 UAC 弹窗）\n")
	case res.User:
		env.printf("  范围      : 当前用户（登录 Windows 时自动启动，注销后停止）\n")
	default:
		env.printf("  范围      : 系统级（开机自动启动，以 SYSTEM 账户运行，注销后继续运行）\n")
	}
	env.printf("  计划任务  : %s\n", res.TaskName)
	env.printf("  可执行文件: %s\n", res.BinPath)
	env.printf("  日志      : %s\n", res.LogPath)
	if res.Started {
		env.printf("  状态      : 运行中\n")
	} else {
		env.printf("  状态      : 计划任务已创建，但几秒内没有看到 Agent 进程；请查看上面的日志文件，或运行下面这条命令：%s\n", cmdLine(env.Prog+" status"))
	}
	if res.WithHolder {
		holderState := "运行中"
		if !res.HolderStarted {
			holderState = "已创建任务（本次未重新启动，或几秒内没有看到进程；见上面的提示 / 日志）"
		}
		env.printf("  会话守护进程: 计划任务 %s，%s\n", res.HolderTaskName, holderState)
		env.printf("  守护进程日志: %s\n", res.HolderLogPath)
		env.printf("  终端会话由守护进程持有：升级 / 重启 Agent 不会打断正在运行的命令。\n")
	} else {
		env.printf("  会话持有  : 与 Agent 同进程（不具持久性：Agent 重启会结束终端会话）\n")
	}
	for _, n := range res.Notes {
		env.printf("提示：%s\n", n)
	}
	env.printf("\n下一步：在 RelayMesh Web 的「接入主机」里核对上面的机器码，确认后点击「启用」。这台主机之前已经启用过的话（升级、重装都沿用同一个机器码），不需要再启用。\n")
	switch {
	case !res.User:
		env.printf("提示：系统级服务里打开的 Shell 是 SYSTEM 身份，权限等同本机最高；若希望以自己的账户运行，请改用 --user 安装（需要管理员权限时加 --highest）。\n")
	case res.Highest:
		env.printf("提示：这个任务的 Shell 拥有管理员令牌；修改或卸载它也需要「以管理员身份运行」的 PowerShell。\n")
	}
	return 0
}

func uninstallWindows(_ context.Context, env Env, c common, purge bool, cfg resolved) int {
	w, err := realWinOps()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	res, err := doUninstallWindows(env, c, purge, cfg, w)
	if err != nil {
		env.errorf("卸载失败: %v\n", err)
		return 1
	}
	if !res.Found && !purge {
		env.printf("没有发现已安装的 RelayMesh Agent 常驻任务。\n")
		return 0
	}
	for _, p := range res.Removed {
		env.printf("已删除: %s\n", p)
	}
	if !purge {
		env.printf("已保留身份（机器码不变）；如需一并删除请加 --purge。\n")
	}
	return 0
}

func upgradeWindows(_ context.Context, env Env, user bool) int {
	w, err := realWinOps()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	res, err := doUpgradeWindows(env, w, user)
	if err != nil {
		env.errorf("升级失败: %v\n", err)
		return 1
	}
	env.printf("✅ 已升级 RelayMesh Agent 到 %s 并重新启动计划任务\n", env.Version)
	env.printf("  计划任务  : %s（定义未改动）\n", res.TaskName)
	env.printf("  可执行文件: %s\n", res.BinPath)
	if res.Started {
		env.printf("  状态      : 运行中\n")
	} else {
		env.printf("  状态      : 计划任务已启动，但几秒内没有看到 Agent 进程；请查看日志，或运行下面这条命令：%s\n", cmdLine(env.Prog+" status"))
	}
	switch res.HolderState {
	case "kept":
		env.printf("  会话守护进程: 正在运行，没有动它（终端会话不受影响）\n")
	case "refreshed":
		env.printf("  会话守护进程: 当前没在运行，已换上新版本的程序\n")
	}
	for _, n := range res.Notes {
		env.printf("提示：%s\n", n)
	}
	env.printf("机器码与启用状态不变，不需要在 Web 上重新启用；几秒后主机列表里的版本会更新。\n")
	return 0
}

func statusWindows(_ context.Context, env Env) int {
	w, err := realWinOps()
	if err != nil {
		env.errorf("%v\n", err)
		return 1
	}
	reportWindows(env, w)
	return 0
}

// kickHolderTask 在守护进程退出后立刻启动它的计划任务（见 kickWindowsHolderTask）。
func kickHolderTask(Env) (string, error) {
	w, err := realWinOps()
	if err != nil {
		return "", err
	}
	return kickWindowsHolderTask(w)
}
