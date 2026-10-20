package hookcli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// RunHook 执行一个 hook 事件（子命令名见 eventSpecs）；任何异常都静默放行，输出该事件的默认输出（多数是空对象）。
//
//   - post-tool-use / stop：同步，要把 Hub 的回复（提醒 / 拦一次）交还给 Cursor，同时顺带上报运行情况；
//   - 其余事件（会话开始 / 结束、提交提示词、思考、工具开始、回复、工具失败、压缩）：不需要回包，
//     只做白名单上报并交给分离出去的子进程发送，父进程立刻返回（见 async.go）。
func RunHook(event string, stdin []byte) (out map[string]any) {
	spec := specForSub(event)
	if spec == nil {
		return map[string]any{}
	}
	defer func() {
		_ = recover()
		if out == nil {
			out = specOutput(spec)
		}
	}()
	return runHook(spec, stdin)
}

// runHook 是 RunHook 的主体；返回 nil 表示「没什么要说的」，由 RunHook 补上事件的默认输出。
func runHook(spec *eventSpec, stdin []byte) map[string]any {
	payload := readPayload(stdin)
	cfgPath := findConfig(payload, mustWd())
	if cfgPath == "" {
		return nil
	}
	cfg := loadJSON(cfgPath)
	if cfg["enabled"] == false {
		return nil
	}
	workflow := strings.TrimSpace(str(cfg["workflow_id"]))
	if workflow == "" {
		return nil
	}

	ep := resolveEndpoint(cfg)
	if ep.URL == "" || ep.Source == "" {
		return nil
	}

	report := buildEventReport(*spec, payload, cfgPath, time.Now())
	ctx := context.Background()

	switch spec.Sub {
	case "post-tool-use":
		interval := defaultInterval
		if v, ok := cfg["interval_seconds"].(float64); ok {
			interval = v
		}
		if !throttleOK(workflow, ep.URL, interval) {
			// 提醒检查被节流了，但这次工具结束仍要报给状态核心：状态机靠它把「工具运行中」翻回「思考中」，漏报会误判卡住。
			asyncReport(cfgPath, workflow, ep.URL, report)
			return nil
		}
		data, err := peek(ctx, ep, workflow, true, "hook", report)
		if err != nil {
			markReportFailure(workflow, ep.URL)
			return nil
		}
		clearReportFailure(workflow, ep.URL)
		if data["remind"] == true {
			if msg, ok := data["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return map[string]any{"additional_context": msg}
			}
		}
		return nil

	case "stop":
		mayBlock := true
		st := payload["status"]
		if st != nil && st != "completed" {
			mayBlock = false
		}
		if lc, ok := toInt(payload["loop_count"]); ok && lc >= 1 {
			mayBlock = false
		}
		data, err := peek(ctx, ep, workflow, false, "hook", report)
		if err != nil {
			markReportFailure(workflow, ep.URL)
			return nil
		}
		clearReportFailure(workflow, ep.URL)
		if !mayBlock {
			return nil
		}
		msg, _ := data["stop_message"].(string)
		unread := 0
		if u, ok := toInt(data["unread"]); ok {
			unread = u
		}
		if unread > 0 && strings.TrimSpace(msg) != "" {
			return map[string]any{"followup_message": msg}
		}
		return nil
	}

	// 不需要回包的状态事件。
	if spec.Min > 0 && !eventThrottleOK(workflow, ep.URL, conversationID(payload), spec.Wire, spec.Min) {
		return nil
	}
	asyncReport(cfgPath, workflow, ep.URL, report)
	return nil
}

// asyncReport 交给分离子进程发送；熔断期间、起不来子进程都静默放弃（上报丢一次不影响任何功能）。
func asyncReport(cfgPath, workflow, url string, report map[string]any) {
	if breakerOpen(workflow, url) {
		return
	}
	_ = spawnReport(reportJob{ConfigPath: cfgPath, Report: report})
}

// specOutput 返回事件的默认输出；每次给新的 map，调用方可以随便改。
func specOutput(spec *eventSpec) map[string]any {
	out := map[string]any{}
	for k, v := range spec.Output {
		out[k] = v
	}
	return out
}

func mustWd() string {
	wd, _ := os.Getwd()
	return wd
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case json.Number:
		i, err := x.Int64()
		return int(i), err == nil
	case int:
		return x, true
	default:
		return 0, false
	}
}

// EmitHookOutput 写 stdout 单行 JSON 并返回退出码 0。
func EmitHookOutput(out map[string]any) {
	if out == nil {
		out = map[string]any{}
	}
	b, _ := json.Marshal(out)
	os.Stdout.Write(append(b, '\n'))
}
