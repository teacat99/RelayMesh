package hookcli

import "time"

const (
	configRel       = ".cursor/relaymesh.json"
	defaultInterval = 15.0
	httpTimeout     = 1500 * time.Millisecond
	defaultTokenEnv = "RELAYMESH_TOKEN"
	// pythonHookName 是历史上 Python 版 hook 的脚本名。Python 版已移除，但老项目的 hooks.json 里可能还留着它的登记：
	// install 时把它迁移成 Go 二进制的登记，uninstall 时一并清理，所以仍然要认得它。
	pythonHookName       = "relaymesh-inbox.py"
	binaryName           = "relaymesh-hook"
	hookInstallHintEvery = 6 * time.Hour
)
