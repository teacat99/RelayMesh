package hookcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// isOurHookCommand 判断 hooks.json 里的一条命令是不是我们登记的：Go 二进制，或历史上 Python 版留下的脚本。
func isOurHookCommand(cmd string) bool {
	cmd = strings.ToLower(cmd)
	return strings.Contains(cmd, pythonHookName) || strings.Contains(cmd, binaryName)
}

// hookEntry 生成登记到 hooks.json 的一条记录（事件见 eventSpecs）。
func hookEntry(eventKey, command string) map[string]any {
	spec := specForCursor(eventKey)
	if spec == nil {
		return map[string]any{"command": strings.TrimSpace(command)}
	}
	entry := map[string]any{"command": strings.TrimSpace(command + " " + spec.Sub)}
	for k, v := range spec.Extra {
		entry[k] = v
	}
	return entry
}

// mergeHooks 把我们的事件登记并入 hooks.json 文档；返回文档有没有变化。
// status 为 false 时只登记收件箱需要的 postToolUse / stop，并把先前登记过的状态事件摘掉（别人的登记原样保留）。
func mergeHooks(doc map[string]any, command string, status bool) bool {
	before, _ := json.Marshal(doc)
	doc["version"] = float64(1)
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		doc["hooks"] = hooks
	}
	for _, spec := range eventSpecs {
		key := spec.Cursor
		if !spec.Core && !status {
			stripOurs(hooks, key)
			continue
		}
		lst, _ := hooks[key].([]any)
		if lst == nil {
			lst = []any{}
		}
		entry := hookEntry(key, command)
		replaced := false
		for i, e := range lst {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if isOurHookCommand(str(em["command"])) {
				lst[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			lst = append(lst, entry)
		}
		hooks[key] = lst
	}
	after, _ := json.Marshal(doc)
	return string(before) != string(after)
}

// stripOurs 摘掉 hooks[key] 里我们登记的条目（别人的原样保留）；列表空了就删掉这个键。返回摘掉了几条。
func stripOurs(hooks map[string]any, key string) int {
	lst, ok := hooks[key].([]any)
	if !ok {
		return 0
	}
	removed := 0
	var kept []any
	for _, e := range lst {
		em, ok := e.(map[string]any)
		if ok && isOurHookCommand(str(em["command"])) {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	if removed == 0 {
		return 0
	}
	if len(kept) == 0 {
		delete(hooks, key)
	} else {
		hooks[key] = kept
	}
	return removed
}

func stripHooks(doc map[string]any) int {
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	keys := make([]string, 0, len(hooks))
	for key := range hooks {
		keys = append(keys, key)
	}
	removed := 0
	for _, key := range keys {
		removed += stripOurs(hooks, key)
	}
	return removed
}

func selfBinary() (string, error) {
	ex, err := os.Executable()
	if err != nil {
		return "", err
	}
	ex, err = filepath.Abs(ex)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(ex); err != nil || st.IsDir() {
		return "", fmt.Errorf("找不到本二进制")
	}
	return ex, nil
}

// InstallOptions 控制 install 子命令。
type InstallOptions struct {
	Workflow  string
	McpURL    string
	McpServer string
	TokenEnv  string
	Interval  *float64
	Project   string
	UserScope bool
	DryRun    bool
	Preview   bool // 预览：含 DryRun 的全部语义（不写任何文件），并打印 hooks.json / relaymesh.json 的逐行改动
	NoStatus  bool // 只登记收件箱需要的 postToolUse / stop，不登记状态事件（并摘掉先前登记过的状态事件）
	Command   string
}

// registeredText 描述这次登记了哪些事件（给计划与完成提示用）。
func registeredText(status bool) string {
	if !status {
		return "只登记 postToolUse 与 stop，不含状态事件"
	}
	n := 0
	for _, spec := range eventSpecs {
		if !spec.Core {
			n++
		}
	}
	return fmt.Sprintf("登记 postToolUse、stop 与 %d 个状态事件", n)
}

func RunInstall(opts InstallOptions) int {
	if opts.McpURL != "" && urlTokenForm(opts.McpURL) != "" {
		fmt.Fprintln(os.Stderr, "--mcp-url 里带着令牌：令牌不能写进 .cursor/relaymesh.json（它在项目目录里，可能被提交）。\n请去掉令牌再试，或者干脆不填 --mcp-url：运行时从 ~/.cursor/mcp.json 读取。")
		return 1
	}
	home, _ := os.UserHomeDir()
	project := opts.Project
	if project == "" {
		project, _ = os.Getwd()
	}
	project, _ = filepath.Abs(project)

	if !opts.UserScope && strings.TrimSpace(opts.Workflow) == "" {
		fmt.Fprintln(os.Stderr, "项目级安装需要 --workflow <workflow_id>。")
		return 1
	}

	base := filepath.Join(project, ".cursor")
	if opts.UserScope {
		base = filepath.Join(home, ".cursor")
	}

	cmdPath := strings.TrimSpace(opts.Command)
	if cmdPath == "" {
		ex, err := selfBinary()
		if err != nil {
			fmt.Fprintln(os.Stderr, "找不到本二进制：请把 relaymesh-hook 放到 PATH 或指定 --command。")
			return 1
		}
		cmdPath = ex
	} else if st, err := os.Stat(cmdPath); err != nil || st.IsDir() {
		fmt.Fprintf(os.Stderr, "指定的 --command 路径不存在：%s\n", cmdPath)
		return 1
	}

	hooksPath := filepath.Join(base, "hooks.json")
	cfgPath := filepath.Join(project, configRel)

	existingHooks, err := readJSONStrict(hooksPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	newHooks := cloneDoc(existingHooks)
	status := !opts.NoStatus
	hooksChanged := mergeHooks(newHooks, cmdPath, status)
	dry := opts.DryRun || opts.Preview

	var plan, diffs []string
	if hooksChanged {
		verb := "更新"
		if existingHooks == nil {
			verb = "创建"
		}
		plan = append(plan, fmt.Sprintf("%s %s（%s）", verb, hooksPath, registeredText(status)))
		if opts.Preview {
			diffs = append(diffs, renderDiff(hooksPath, existingHooks, newHooks))
		}
	}

	var existingCfg, newCfg map[string]any
	cfgChanged := false
	if !opts.UserScope {
		existingCfg, err = readJSONStrict(cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		newCfg = cloneDoc(existingCfg)
		newCfg["workflow_id"] = opts.Workflow
		if opts.McpURL != "" {
			newCfg["mcp_url"] = opts.McpURL
		}
		if opts.McpServer != "" {
			newCfg["mcp_server"] = opts.McpServer
		}
		if opts.TokenEnv != "" {
			newCfg["token_env"] = opts.TokenEnv
		}
		if opts.Interval != nil {
			newCfg["interval_seconds"] = *opts.Interval
		}
		cfgChanged = existingCfg == nil || !sameDoc(existingCfg, newCfg)
		if cfgChanged {
			if existingCfg == nil {
				plan = append(plan, fmt.Sprintf("创建 %s（工作流 %s；不含任何令牌）", cfgPath, opts.Workflow))
			} else {
				plan = append(plan, fmt.Sprintf("更新 %s（工作流 %s；不含任何令牌）", cfgPath, opts.Workflow))
			}
			if opts.Preview {
				diffs = append(diffs, renderDiff(cfgPath, existingCfg, newCfg))
			}
		}
	}

	if len(plan) == 0 {
		fmt.Println("已经是最新状态，没有需要改动的。")
		return 0
	}
	for _, line := range plan {
		if dry {
			fmt.Println("[预演] " + line)
		} else {
			fmt.Println(line)
		}
	}
	if opts.Preview {
		for _, d := range diffs {
			fmt.Print(d)
		}
		fmt.Printf("预览结束：没有写入任何文件。确认无误后去掉 --preview 再运行一次；写入前会把已有的文件备份成「原文件名.bak-时间」（各保留最近 %d 份）。\n", keepBackups)
	}
	if dry {
		return 0
	}

	// 先把要改的已有文件都备份好，再动手写：备份失败就一个文件都不改。
	var backups []string
	for _, f := range []struct {
		changed  bool
		path     string
		existing map[string]any
	}{{cfgChanged, cfgPath, existingCfg}, {hooksChanged, hooksPath, existingHooks}} {
		if !f.changed || f.existing == nil {
			continue
		}
		bak, err := backupFile(f.path, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "备份 %s 失败：%v\n没有改动任何文件。\n", f.path, err)
			return 1
		}
		backups = append(backups, bak)
	}
	if cfgChanged {
		if err := writeJSONAtomic(cfgPath, newCfg); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
	}
	if hooksChanged {
		if err := writeJSONAtomic(hooksPath, newHooks); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
	}
	for _, b := range backups {
		fmt.Printf("已备份原文件：%s\n", b)
	}
	tokenEnv := opts.TokenEnv
	if tokenEnv == "" {
		tokenEnv = defaultTokenEnv
	}
	fmt.Printf("安装完成。令牌不会写进任何文件：运行时读取环境变量 %s，或 ~/.cursor/mcp.json 里 RelayMesh 那一项。\n", tokenEnv)
	if status {
		fmt.Println("状态事件只上报「发生了什么」（事件类型、工具名、耗时、时间、客户端类型与版本、项目目录）；不会上报提示词、思考、回复、工具参数与结果，也不读取邮箱。")
	}
	fmt.Printf("检查连通性：%s status\n", cmdPath)
	fmt.Println("Cursor 通常会在保存 hooks.json 后重新加载；已经打开的会话可能不会，没生效时重启 Cursor 或新开一个会话。")
	return 0
}

func RunUninstall(project string, userScope, purge bool) int {
	home, _ := os.UserHomeDir()
	if project == "" {
		project, _ = os.Getwd()
	}
	project, _ = filepath.Abs(project)
	base := filepath.Join(project, ".cursor")
	if userScope {
		base = filepath.Join(home, ".cursor")
	}
	hooksPath := filepath.Join(base, "hooks.json")
	doc, err := readJSONStrict(hooksPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	removed := 0
	if doc != nil {
		removed = stripHooks(doc)
		if removed > 0 {
			bak, err := backupFile(hooksPath, time.Now())
			if err != nil {
				fmt.Fprintf(os.Stderr, "备份 %s 失败：%v\n没有改动任何文件。\n", hooksPath, err)
				return 1
			}
			if err := writeJSONAtomic(hooksPath, doc); err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
				return 1
			}
			fmt.Printf("已备份原文件：%s\n", bak)
		}
	}
	fmt.Printf("已移除 %d 条 hook 登记。\n", removed)
	if purge && !userScope {
		p := filepath.Join(project, configRel)
		_ = os.Remove(p)
	}
	return 0
}
