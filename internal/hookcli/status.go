package hookcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var version = "dev"

// Version 返回编译时写入的版本号。
func Version() string { return version }

func RunStatus(project string, jsonOut bool) int {
	if project == "" {
		project, _ = os.Getwd()
	}
	project, _ = filepath.Abs(project)
	cfgPath := findConfig(map[string]any{"workspace_roots": []any{project}}, project)
	if cfgPath == "" {
		msg := fmt.Sprintf("未找到 .cursor/relaymesh.json（从 %s 往上找）。先运行 install。", project)
		if jsonOut {
			fmt.Printf(`{"ok":false,"error":%q}`+"\n", msg)
		} else {
			fmt.Println(msg)
		}
		return 1
	}
	cfg := loadJSON(cfgPath)
	workflow := strings.TrimSpace(str(cfg["workflow_id"]))
	ep := resolveEndpoint(cfg)

	type view struct {
		OK       bool              `json:"ok"`
		Config   string            `json:"config_path,omitempty"`
		Workflow string            `json:"workflow_id,omitempty"`
		McpURL   string            `json:"mcp_url,omitempty"`
		Source   string            `json:"token_source,omitempty"`
		Hooks    map[string]string `json:"hooks_json,omitempty"`
		Error    string            `json:"error,omitempty"`
		Detail   string            `json:"detail,omitempty"`
	}
	v := view{OK: true, Config: cfgPath, Workflow: workflow, McpURL: maskURL(ep.URL), Source: ep.Source}

	projectURLWarn := ""
	if urlTokenForm(str(cfg["mcp_url"])) != "" {
		projectURLWarn = "提示：relaymesh.json 的 mcp_url 里带着令牌——令牌不该写进项目文件（会被提交或同步），它也不算令牌来源；请去掉令牌，改由 mcp.json 或环境变量提供。"
	}
	if cfg["enabled"] == false {
		v.Detail = "配置里 enabled 为 false，hook 不会做任何事。"
	}
	if workflow == "" || ep.URL == "" || ep.Source == "" {
		v.OK = false
		if ep.Source == "" {
			v.Error = "没有找到令牌来源"
		}
	}

	home, _ := os.UserHomeDir()
	v.Hooks = map[string]string{}
	// 顺序固定（项目、用户；事件按 eventSpecs），输出才稳定。
	for _, hf := range []struct{ label, path string }{
		{"项目", filepath.Join(filepath.Dir(filepath.Dir(cfgPath)), ".cursor", "hooks.json")},
		{"用户", filepath.Join(home, ".cursor", "hooks.json")},
	} {
		label := hf.label
		doc := loadJSON(hf.path)
		hooks, _ := doc["hooks"].(map[string]any)
		var core, extra []string
		for _, spec := range eventSpecs {
			lst, _ := hooks[spec.Cursor].([]any)
			for _, e := range lst {
				em, ok := e.(map[string]any)
				if ok && isOurHookCommand(str(em["command"])) {
					if spec.Core {
						core = append(core, spec.Cursor)
					} else {
						extra = append(extra, spec.Cursor)
					}
					break
				}
			}
		}
		v.Hooks[label] = describeRegistered(core, extra)
	}

	if v.OK {
		data, err := peek(context.Background(), ep, workflow, false, "check", nil)
		if err != nil {
			v.OK = false
			if strings.Contains(err.Error(), "401") {
				v.Error = "连通性：服务端返回 HTTP 401（令牌不对或没有权限）"
			} else if strings.Contains(err.Error(), "HTTP") {
				v.Error = "连通性：服务端返回 " + err.Error()
			} else {
				v.Error = "连通性：失败（" + err.Error() + "）"
			}
		} else {
			v.Detail = fmt.Sprintf("连通性：正常。收件箱总开关 %s，hook 层 %s；当前未读 %v 条，已读未确认 %v 条。",
				onOff(data["enabled"]), onOff(data["hooks_enabled"]), data["unread"], data["delivered"])
		}
	}

	if jsonOut {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
		return boolToExit(v.OK)
	}
	fmt.Printf("配置文件：%s\n", cfgPath)
	fmt.Printf("工作流：%s\n", orDash(workflow))
	if v.Detail != "" && cfg["enabled"] == false {
		fmt.Println("提示：" + v.Detail)
	}
	fmt.Printf("MCP 地址：%s\n", orDash(v.McpURL))
	fmt.Printf("令牌来源：%s\n", orDash(ep.Source))
	if projectURLWarn != "" {
		fmt.Println(projectURLWarn)
	}
	for _, label := range []string{"项目", "用户"} {
		fmt.Printf("%s级 hooks.json：%s\n", label, v.Hooks[label])
	}
	if v.Error != "" {
		fmt.Println(v.Error)
	} else if strings.HasPrefix(v.Detail, "连通性") {
		fmt.Println(v.Detail)
	}
	return boolToExit(v.OK)
}

// describeRegistered 描述 hooks.json 里登记了哪些事件：核心事件（postToolUse / stop）逐个列出，状态事件只报个数，缺的补一句怎么办。
func describeRegistered(core, extra []string) string {
	if len(core) == 0 && len(extra) == 0 {
		return "未登记"
	}
	total := 0
	for _, spec := range eventSpecs {
		if !spec.Core {
			total++
		}
	}
	text := "已登记 " + strings.Join(core, "、")
	if len(core) == 0 {
		text = "只登记了状态事件"
	}
	switch {
	case len(extra) == total:
		text += fmt.Sprintf("；状态事件 %d/%d", len(extra), total)
	case len(extra) == 0:
		text += fmt.Sprintf("；状态事件 0/%d（运行 install 可补上）", total)
	default:
		text += fmt.Sprintf("；状态事件 %d/%d（%s；运行 install 可补齐）", len(extra), total, strings.Join(extra, "、"))
	}
	return text
}

func RunDoctor(project string) int {
	code := RunStatus(project, false)
	ex, err := selfBinary()
	if err == nil {
		if st, err2 := os.Stat(ex); err2 != nil || st.IsDir() {
			fmt.Println("doctor：本二进制路径不存在或不可访问。")
			code = 1
		} else {
			fmt.Printf("doctor：本二进制 %s 存在。\n", ex)
		}
	}
	return code
}

func onOff(v any) string {
	if v == true {
		return "开"
	}
	return "关（服务端已静音）"
}

func orDash(s string) string {
	if s == "" {
		return "（未设置）"
	}
	return s
}

func boolToExit(ok bool) int {
	if ok {
		return 0
	}
	return 1
}
