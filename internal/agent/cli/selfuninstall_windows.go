//go:build windows

package cli

// selfUninstaller 返回交给 agent.Options.SelfUninstall 的函数；nil 表示当前进程不是已安装的无窗口版 Agent
// （前台运行、从下载目录直接运行等），此时 Agent 不会声明卸载能力。
func selfUninstaller(env Env, _ common, cfg resolved) func(purge bool) error {
	w, err := realWinOps()
	if err != nil {
		return nil
	}
	return windowsSelfUninstaller(w, env, cfg)
}
