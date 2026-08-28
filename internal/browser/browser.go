package browser

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// OpenURL 尝试在系统默认浏览器中打开指定的目标 URL
// 全面支持 macOS、Windows、Linux 以及 WSL/WSL2 环境
func OpenURL(targetURL string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", targetURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	case "linux":
		if isWSL() {
			// WSL 环境下优先尝试 wslview，随后回退至 cmd.exe
			if _, err := exec.LookPath("wslview"); err == nil {
				cmd = exec.Command("wslview", targetURL)
			} else {
				cmd = exec.Command("cmd.exe", "/c", "start", strings.ReplaceAll(targetURL, "&", "^&"))
			}
		} else {
			cmd = exec.Command("xdg-open", targetURL)
		}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	if cmd != nil {
		if err := cmd.Start(); err != nil {
			return err
		}
		// 分离运行，不阻塞主服务协程
		go func() {
			_ = cmd.Wait()
		}()
	}
	return nil
}

// OpenURLAsync 异步延迟并在后台打开浏览器
// 仅限定在本地原生桌面/WSL环境运行；若在容器、远程或非本地环境则静默跳过
func OpenURLAsync(targetURL string, delay time.Duration) {
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		if isContainer() {
			// 容器环境内部无本地桌面，静默跳过
			return
		}
		if !isLocalDesktopOrWSL() {
			// 纯远程无头环境，静默跳过
			return
		}
		log.Printf("🌐 [RelayMesh] 本地原生运行：正在为您唤醒默认浏览器打开 Web 控制台: %s", targetURL)
		if err := OpenURL(targetURL); err != nil {
			log.Printf("提示: 本地浏览器唤醒未响应 (%v)，请直接在浏览器中访问: %s", err, targetURL)
		}
	}()
}

// isLocalDesktopOrWSL 检查是否处于本地桌面交互环境或 WSL 宿主环境
func isLocalDesktopOrWSL() bool {
	if isWSL() {
		return true
	}
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	case "linux":
		// Linux 桌面环境需具备 DISPLAY 或 WAYLAND_DISPLAY
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	default:
		return false
	}
}

// isWSL 判断是否运行在 WSL / WSL2 环境下
func isWSL() bool {
	if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/version"); err == nil {
		lower := strings.ToLower(string(data))
		return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
	}
	return false
}

// isContainer 判断是否运行在 Docker / Kubernetes 容器内
func isContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		return strings.Contains(content, "docker") || strings.Contains(content, "kubepods") || strings.Contains(content, "containerd")
	}
	return false
}
