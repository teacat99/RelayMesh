package mcp

import "fmt"

// formatHookInstallHint 是回执末尾的 hook: 提示（不含令牌）。
func formatHookInstallHint(baseURL, workflowID string) string {
	baseURL = trimSlash(baseURL)
	cmd := fmt.Sprintf("curl -fsSL %q | RELAYMESH_HOOK_INSTALL_BASE=%q sh -s -- --workflow %q", baseURL+"/hooks/cursor/install.sh", baseURL, workflowID)
	return fmt.Sprintf(
		"hook: 编辑器 hook 能在你工作时实时送达追加指令、并在顶栏显示真实运行状态（异步对话也依赖它）。在项目根目录执行安装：%s ；装好后运行 relaymesh-hook status 确认，会话页收件箱芯片会显示「hook 刚刚」。",
		cmd,
	)
}

func trimSlash(u string) string {
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}
