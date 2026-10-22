package hookcli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// hook v2：把 Cursor 的各类 hook 事件翻译成上报给 Hub 的状态事件（设计稿 04 §2.5，#113）。
//
// 铁律：只报「发生了什么」，从不报内容。提示词、思考、回复、工具参数与结果、邮箱一律不读不转——
// 上报只由下面的白名单字段构成（buildEventReport），子进程收到后还会再清洗一遍（sanitizeReport）。
// 邮箱（载荷里的邮箱字段、Cursor 导出的邮箱环境变量）永不读取，有静态测试守着。

// eventSpec 描述一个 hook 事件：登记在 hooks.json 里的名字、子命令、以及上报的事件名。
type eventSpec struct {
	Cursor string         // hooks.json 里的事件名
	Sub    string         // relaymesh-hook 的子命令
	Wire   string         // 上报给 Hub 的 agent_event
	Core   bool           // 收件箱本身就要用的（postToolUse / stop）；其余只为状态上报
	Async  bool           // 不需要回包：交给分离出去的子进程，父进程立刻返回
	Output map[string]any // 父进程的标准输出（Cursor 不看内容的事件就是空对象）
	Min    time.Duration  // 同一对话内最小上报间隔（0 = 不限）
	Extra  map[string]any // 登记到 hooks.json 时的附加字段
}

// eventSpecs 的顺序就是登记与展示的顺序（不要用 map 遍历）。
//
// postToolUse 与 stop 的上报沿用旧事件名 tool / stop：旧版 Hub 只认这两个，新 hook 连旧 Hub 时守护的活动信号不能丢；
// 新版 Hub 把 tool 当作工具结束（tool_end）。
var eventSpecs = []eventSpec{
	{Cursor: "postToolUse", Sub: "post-tool-use", Wire: "tool", Core: true,
		Extra: map[string]any{"timeout": float64(5)}},
	{Cursor: "stop", Sub: "stop", Wire: "stop", Core: true,
		Extra: map[string]any{"timeout": float64(5), "loop_limit": float64(1)}},
	{Cursor: "sessionStart", Sub: "session-start", Wire: "session_start", Async: true,
		Extra: map[string]any{"timeout": float64(5)}},
	{Cursor: "sessionEnd", Sub: "session-end", Wire: "session_end", Async: true,
		Extra: map[string]any{"timeout": float64(5)}},
	{Cursor: "beforeSubmitPrompt", Sub: "before-submit-prompt", Wire: "prompt", Async: true,
		Output: map[string]any{"continue": true}, Extra: map[string]any{"timeout": float64(5)}},
	{Cursor: "afterAgentThought", Sub: "after-agent-thought", Wire: "thought", Async: true, Min: 5 * time.Second,
		Extra: map[string]any{"timeout": float64(5)}},
	{Cursor: "preToolUse", Sub: "pre-tool-use", Wire: "tool_start", Async: true,
		Extra: map[string]any{"timeout": float64(5)}},
	{Cursor: "afterAgentResponse", Sub: "after-agent-response", Wire: "response", Async: true,
		Extra: map[string]any{"timeout": float64(5)}},
	{Cursor: "postToolUseFailure", Sub: "post-tool-use-failure", Wire: "tool_fail", Async: true,
		Extra: map[string]any{"timeout": float64(5)}},
	{Cursor: "preCompact", Sub: "pre-compact", Wire: "compact", Async: true,
		Extra: map[string]any{"timeout": float64(5)}},
}

func specForSub(sub string) *eventSpec {
	for i := range eventSpecs {
		if eventSpecs[i].Sub == sub {
			return &eventSpecs[i]
		}
	}
	return nil
}

func specForCursor(name string) *eventSpec {
	for i := range eventSpecs {
		if eventSpecs[i].Cursor == name {
			return &eventSpecs[i]
		}
	}
	return nil
}

// reportWireNames 是子进程接受的 agent_event 取值（上面各事件的上报名）。
var reportWireNames = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range eventSpecs {
		m[s.Wire] = true
	}
	return m
}()

var (
	toolUnsafeRe = regexp.MustCompile(`[^A-Za-z0-9_:.\-]`)
	versionRe    = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,32}$`)
	reasonRe     = regexp.MustCompile(`[^a-z_]`)
)

const (
	maxConversationID = 64
	maxToolName       = 64
	maxProjectDir     = 512
	maxStatusLen      = 16
	maxDurationMS     = 24 * 3600 * 1000
)

func conversationID(payload map[string]any) string {
	cid, ok := payload["conversation_id"].(string)
	if !ok {
		return ""
	}
	cid = strings.TrimSpace(cid)
	if len(cid) > maxConversationID {
		cid = cid[:maxConversationID]
	}
	return cid
}

// cleanTool 清洗工具名：只留字母数字与 _ : . -，其余换成 _，最长 64 个字符。
func cleanTool(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = toolUnsafeRe.ReplaceAllString(s, "_")
	if r := []rune(s); len(r) > maxToolName {
		s = string(r[:maxToolName])
	}
	return s
}

// durationMS 取工具耗时（毫秒）；不是数字、非正、超过一天都当没有。
func durationMS(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f <= 0 || f >= maxDurationMS {
		return 0, false
	}
	return int64(f), true
}

func cleanVersion(s string) string {
	s = strings.TrimSpace(s)
	if versionRe.MatchString(s) {
		return s
	}
	return ""
}

// cursorVersion 取 Cursor 的版本号：先看 hook 载荷，再看环境变量 CURSOR_VERSION；不合格当没有。
func cursorVersion(payload map[string]any, getenv func(string) string) string {
	if s, ok := payload["cursor_version"].(string); ok {
		if v := cleanVersion(s); v != "" {
			return v
		}
	}
	return cleanVersion(getenv("CURSOR_VERSION"))
}

// detectClientKind 判断 hook 跑在哪种 Cursor 客户端里：
//   - CLI：`agent` 启动脚本会导出 CURSOR_INVOKED_AS，CLI 自身带 CURSOR_CLI / CURSOR_AGENT_CLI_LOCAL_MODE；
//   - IDE：没有上面的标记，但环境里有 VS Code 系的变量（推断，待 IDE 里的观测 hook 验证）。
//
// 拿不准就返回空串（不上报），界面显示「未知客户端」，不乱贴标签。
func detectClientKind(getenv func(string) string) string {
	if getenv("CURSOR_INVOKED_AS") != "" || getenv("CURSOR_CLI") != "" || getenv("CURSOR_AGENT_CLI_LOCAL_MODE") == "true" {
		return "cli"
	}
	if getenv("VSCODE_IPC_HOOK_CLI") != "" || getenv("VSCODE_PID") != "" || getenv("TERM_PROGRAM") == "vscode" {
		return "ide"
	}
	return ""
}

func cleanProjectDir(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxProjectDir || !filepath.IsAbs(s) {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}

// projectDirectory 决定上报哪个目录当「项目目录」（Agent 身份的一部分）：
// 优先是包含 relaymesh.json 的那个工作区根目录，其次第一个工作区根目录，最后是 CURSOR_PROJECT_DIR；都没有就不报。
func projectDirectory(payload map[string]any, cfgPath string, getenv func(string) string) string {
	var roots []string
	if rs, ok := payload["workspace_roots"].([]any); ok {
		for _, r := range rs {
			if s, ok := r.(string); ok {
				if c := cleanProjectDir(s); c != "" {
					roots = append(roots, c)
				}
			}
		}
	}
	if cfgPath != "" {
		cfgRoot := filepath.Dir(filepath.Dir(cfgPath))
		for _, r := range roots {
			if samePath(r, cfgRoot) {
				return r
			}
		}
		for _, r := range roots {
			if underPath(cfgRoot, r) {
				return r
			}
		}
		// 配置在工作区根的上层（monorepo 里工作区只开了一个子目录）：这个工作区根仍然属于该配置。
		for _, r := range roots {
			if underPath(r, cfgRoot) {
				return r
			}
		}
	}
	if len(roots) > 0 {
		return roots[0]
	}
	return cleanProjectDir(getenv("CURSOR_PROJECT_DIR"))
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// underPath 判断 child 是不是 parent 目录本身或它下面的目录。
func underPath(child, parent string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// cleanStatus 清洗状态 / 结束原因：小写字母与下划线，最长 16 个字符。
func cleanStatus(s string) string {
	s = strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(s)))
	s = reasonRe.ReplaceAllString(s, "")
	if len(s) > maxStatusLen {
		s = s[:maxStatusLen]
	}
	return s
}

// buildEventReport 生成一次上报：只有白名单里的字段，没有任何内容正文。
func buildEventReport(spec eventSpec, payload map[string]any, cfgPath string, now time.Time) map[string]any {
	report := map[string]any{"agent_event": spec.Wire, "agent_ts": now.UnixMilli()}
	if cid := conversationID(payload); cid != "" {
		report["conversation_id"] = cid
	}
	switch spec.Wire {
	case "tool", "tool_start", "tool_fail":
		if tool := cleanTool(payload["tool_name"]); tool != "" {
			report["agent_tool"] = tool
		}
	}
	switch spec.Wire {
	case "tool", "tool_fail":
		for _, key := range []string{"duration_ms", "duration"} {
			if d, ok := durationMS(payload[key]); ok {
				report["agent_duration_ms"] = d
				break
			}
		}
	case "stop":
		status, ok := payload["status"]
		if !ok {
			status = "completed"
		}
		if s, ok := status.(string); ok {
			report["agent_status"] = cleanStatusLoose(s)
		}
	case "session_end":
		reason, _ := payload["reason"].(string)
		if strings.TrimSpace(reason) == "" {
			reason, _ = payload["final_status"].(string)
		}
		if s := cleanStatus(reason); s != "" {
			report["agent_status"] = s
		}
	}
	if k := detectClientKind(os.Getenv); k != "" {
		report["client_kind"] = k
	}
	if v := cursorVersion(payload, os.Getenv); v != "" {
		report["cursor_version"] = v
	}
	if d := projectDirectory(payload, cfgPath, os.Getenv); d != "" {
		report["project_directory"] = d
	}
	return report
}

// cleanStatusLoose 沿用 v1 对 stop 状态的处理：转小写、去空白、最长 16 个字符，不丢字符（Hub 会再做归一）。
func cleanStatusLoose(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > maxStatusLen {
		s = s[:maxStatusLen]
	}
	return s
}

// sanitizeReport 在子进程里再清洗一遍：只放行白名单字段和合法取值，其余一律丢弃。
// 子进程的输入来自标准输入，理论上只有父进程会写，但「只发白名单」不能靠约定，要靠这里。
func sanitizeReport(in map[string]any) map[string]any {
	ev, _ := in["agent_event"].(string)
	if !reportWireNames[ev] {
		return nil
	}
	out := map[string]any{"agent_event": ev}
	if s, ok := in["conversation_id"].(string); ok {
		if s = strings.TrimSpace(s); s != "" {
			if len(s) > maxConversationID {
				s = s[:maxConversationID]
			}
			out["conversation_id"] = s
		}
	}
	if s, ok := in["agent_status"].(string); ok {
		if s = cleanStatusLoose(s); s != "" {
			out["agent_status"] = s
		}
	}
	if t := cleanTool(in["agent_tool"]); t != "" {
		out["agent_tool"] = t
	}
	if f, ok := in["agent_ts"].(float64); ok && f > 0 && f < 1e15 {
		out["agent_ts"] = int64(f)
	}
	if d, ok := durationMS(in["agent_duration_ms"]); ok {
		out["agent_duration_ms"] = d
	}
	if s, ok := in["client_kind"].(string); ok && (s == "ide" || s == "cli") {
		out["client_kind"] = s
	}
	if s, ok := in["cursor_version"].(string); ok {
		if v := cleanVersion(s); v != "" {
			out["cursor_version"] = v
		}
	}
	if s, ok := in["project_directory"].(string); ok {
		if d := cleanProjectDir(s); d != "" {
			out["project_directory"] = d
		}
	}
	return out
}
