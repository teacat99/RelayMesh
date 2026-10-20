package terminal

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
)

// stream-json 解析（#129）：把 `agent -p --output-format stream-json` 的输出翻成状态机的事件。
//
// 输出每行一个 JSON 事件。这里只关心「智能体在干什么」，所以只认下面几类，其余一律忽略，字段缺了也不报错：
//
//	system / init          一次运行开始                          → prompt
//	thinking               思考的增量 / 完成                     → thought
//	assistant              一条回复                              → response
//	tool_call / started    工具开始（带 call_id）                → tool_start
//	tool_call / completed  工具结束；结果是 error / rejected 的算失败 → tool_end / tool_fail
//	result                 这次运行的终局                        → stop（成功 completed，is_error 为真 error）
//
// 解析器必须容忍现实里的一切：半行（读取边界落在一行中间）、超长行（读文件的结果可以有几百 KB）、非 JSON 的纯文本行
// （报错信息）、不认识的事件类型、重复与乱序的事件、输出中间被丢掉一段（Gap）。任何一种都不能让它 panic、
// 无限占内存，或把状态带歪——认不出的就当没看见。

// runSignal 是从运行输出里认出的一个事实（已按状态机的事件名归一）。
type runSignal struct {
	Name   string // agentstate.Ev* 之一
	Tool   string // tool_start / tool_end / tool_fail 的工具名（展示用，首字母大写，与 hook 的写法一致）
	Status string // stop 的结束状态
	// Conv 是 init 事件里的 session_id（Cursor 的对话编号）：新开对话的运行靠它学到对话编号（#176）。其他信号为空。
	Conv string
}

const (
	streamLineMax   = 256 << 10 // 整行解析的上限；更长的行只看开头
	streamSniffLen  = 4 << 10   // 超长行保留并「嗅探」的开头长度
	streamMaxFlight = 64        // 同时跟踪的在途工具调用数上限
	streamKeepBuf   = 64 << 10  // 行缓冲涨过这个就在用完后释放，不长期占着
)

// streamParser 是按字节流喂入的增量解析器。零值可用；不是并发安全的（一个运行一个解析器，由监视协程独占）。
type streamParser struct {
	buf     []byte
	over    bool              // 当前行超过 streamLineMax：只保留开头，行尾到了按嗅探处理
	skip    bool              // 丢弃到下一个换行（输出有缺口之后，当前位置多半在一行中间）
	flights map[string]string // 在途的工具调用：call_id → 工具名
	order   []string          // 在途调用的开始顺序（最后一个是最近开始的）
}

// Feed 喂入一段输出，返回其中完整行产生的信号（按出现顺序）。半行留在缓冲里等下一次。
func (p *streamParser) Feed(data []byte) []runSignal {
	var out []runSignal
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			p.add(data)
			break
		}
		p.add(data[:i])
		out = p.endLine(out)
		data = data[i+1:]
	}
	return out
}

// Flush 处理末尾没有换行的最后一行（运行结束时调用）。
func (p *streamParser) Flush() []runSignal {
	var out []runSignal
	if len(p.buf) > 0 && !p.skip {
		out = p.endLine(out)
	}
	p.buf, p.over, p.skip = nil, false, false
	return out
}

// Resync 在输出出现缺口（中间一段被丢掉）之后调用：缓冲里的半行作废，并丢弃到下一个换行为止。在途工具的记录保留——
// 缺口前开始的调用可能在缺口后结束。
func (p *streamParser) Resync() {
	p.buf, p.over, p.skip = nil, false, true
}

// add 把一段（不含换行的）字节并进当前行；行太长就只留开头。
func (p *streamParser) add(b []byte) {
	if p.skip || p.over || len(b) == 0 {
		return
	}
	if len(p.buf)+len(b) > streamLineMax {
		p.over = true
		if keep := streamSniffLen - len(p.buf); keep > 0 {
			if keep > len(b) {
				keep = len(b)
			}
			p.buf = append(p.buf, b[:keep]...)
		} else if len(p.buf) > streamSniffLen {
			p.buf = p.buf[:streamSniffLen]
		}
		return
	}
	p.buf = append(p.buf, b...)
}

// endLine 处理刚凑齐的一行，把产生的信号追加到 out。
func (p *streamParser) endLine(out []runSignal) []runSignal {
	if p.skip { // 缺口之后的残行：丢掉，从下一行开始恢复
		p.buf, p.over, p.skip = nil, false, false
		return out
	}
	raw := bytes.TrimSpace(p.buf)
	over := p.over
	var ev rawStreamEvent
	ok := len(raw) > 0 && raw[0] == '{'
	if ok {
		if over {
			ev, ok = sniffStreamEvent(raw)
		} else if json.Unmarshal(raw, &ev) != nil {
			ok = false // 非法 JSON（含被截断的半行）：当没看见
		}
	}
	if ok {
		out = p.classify(out, ev)
	}
	p.over = false
	if cap(p.buf) > streamKeepBuf {
		p.buf = nil
	} else {
		p.buf = p.buf[:0]
	}
	return out
}

// rawStreamEvent 只取需要的字段；大对象（tool_call）按原样留着，用到时才解。
type rawStreamEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	CallID    string          `json:"call_id"`
	IsError   bool            `json:"is_error"`
	SessionID string          `json:"session_id"`
	ToolCall  json.RawMessage `json:"tool_call"`
}

func (p *streamParser) classify(out []runSignal, ev rawStreamEvent) []runSignal {
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			out = append(out, runSignal{Name: agentstate.EvPrompt, Conv: ev.SessionID})
		}
	case "thinking":
		out = append(out, runSignal{Name: agentstate.EvThought})
	case "assistant":
		out = append(out, runSignal{Name: agentstate.EvResponse})
	case "tool_call":
		switch ev.Subtype {
		case "started":
			name := toolNameOf(ev.ToolCall)
			p.track(ev.CallID, name)
			out = append(out, runSignal{Name: agentstate.EvToolStart, Tool: name})
		case "completed", "failed":
			name := toolNameOf(ev.ToolCall)
			if known, ok := p.release(ev.CallID); ok && known != "" {
				name = known
			}
			kind := agentstate.EvToolEnd
			if ev.Subtype == "failed" || toolFailed(ev.ToolCall) {
				kind = agentstate.EvToolFail
			}
			out = append(out, runSignal{Name: kind, Tool: name})
			// 并行的工具调用：还有别的在途，就把状态重新指向它，别让「工具 X」一结束就掉回「思考」。
			if rest, ok := p.latestFlight(); ok {
				out = append(out, runSignal{Name: agentstate.EvToolStart, Tool: rest})
			}
		}
	case "result":
		p.flights, p.order = nil, nil
		status := agentstate.StopCompleted
		if ev.IsError || (ev.Subtype != "" && ev.Subtype != "success") {
			status = agentstate.StopError
		}
		out = append(out, runSignal{Name: agentstate.EvStop, Status: status})
	}
	return out
}

func (p *streamParser) track(id, name string) {
	if id == "" || len(p.flights) >= streamMaxFlight {
		return
	}
	if p.flights == nil {
		p.flights = make(map[string]string)
	}
	if _, ok := p.flights[id]; !ok {
		p.order = append(p.order, id)
	}
	p.flights[id] = name
}

func (p *streamParser) release(id string) (string, bool) {
	name, ok := p.flights[id]
	if !ok {
		return "", false
	}
	delete(p.flights, id)
	for i, v := range p.order {
		if v == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	return name, true
}

// latestFlight 返回最近开始、仍在途的工具调用的名字。
func (p *streamParser) latestFlight() (string, bool) {
	for i := len(p.order) - 1; i >= 0; i-- {
		if name, ok := p.flights[p.order[i]]; ok {
			return name, true
		}
	}
	return "", false
}

// InFlight 返回此刻在途的工具调用数（测试与排查用）。
func (p *streamParser) InFlight() int { return len(p.flights) }

// collapseSignals 把一批信号里连续重复的思考心跳 / 回复折成一个：思考增量一秒几十条，状态机只需要知道「还在想」。
func collapseSignals(in []runSignal) []runSignal {
	if len(in) < 2 {
		return in
	}
	out := in[:0:0]
	for _, s := range in {
		if n := len(out); n > 0 && out[n-1] == s && (s.Name == agentstate.EvThought || s.Name == agentstate.EvResponse) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ---------- 工具名与结果 ----------

var streamKindRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,47}$`)

// toolNameOf 从 tool_call 对象取工具名。对象里只有一个键，写成 <种类>ToolCall（shellToolCall、readToolCall……）；
// 通用的是 function 对象（带 name）。mcp 带上被调用的工具名，写成 MCP:<工具>。认不出就返回空串。
func toolNameOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var kinds map[string]json.RawMessage
	if json.Unmarshal(raw, &kinds) != nil || len(kinds) == 0 {
		return ""
	}
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kind := keys[0]
	switch {
	case kind == "function":
		var f struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(kinds[kind], &f) == nil && strings.TrimSpace(f.Name) != "" {
			return agentstate.SanitizeTool(f.Name)
		}
		return "Function"
	case kind == "mcpToolCall":
		var m struct {
			Args struct {
				ToolName string `json:"toolName"`
				Name     string `json:"name"`
			} `json:"args"`
		}
		if json.Unmarshal(kinds[kind], &m) == nil {
			if n := firstTrimmed(m.Args.ToolName, m.Args.Name); n != "" {
				return agentstate.SanitizeTool("MCP:" + n)
			}
		}
		return "MCP"
	}
	return kindDisplayName(kind)
}

// kindDisplayName 把 shellToolCall 写成 Shell：去掉 ToolCall 后缀，首字母大写（与 hook 里的工具名写法一致）。
func kindDisplayName(kind string) string {
	base := strings.TrimSuffix(kind, "ToolCall")
	if base == "" || !streamKindRe.MatchString(base) {
		return ""
	}
	return strings.ToUpper(base[:1]) + base[1:]
}

// firstTrimmed 返回第一个去掉首尾空白后非空的字符串。
func firstTrimmed(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// toolFailed 判断一个已完成的工具调用是不是失败：结果对象里是 error / rejected 这类键，而不是 success。
// 注意：shell 命令退出码非零仍然是 success（工具本身执行了）——与 hook 的 postToolUse / postToolUseFailure 口径一致。
func toolFailed(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var kinds map[string]struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &kinds) != nil {
		return false
	}
	for _, k := range kinds {
		for rk := range k.Result {
			switch rk {
			case "error", "rejected", "failure", "failed", "timeout", "cancelled", "aborted":
				return true
			}
		}
	}
	return false
}

// ---------- 超长行的嗅探 ----------

// sniffStreamEvent 只看一行的开头，认出事件类型、子类型、call_id 与工具种类。
// 超长的行（读大文件的结果、写大文件的参数）整体解析既占内存又没必要：Cursor 的事件总是把 type / subtype / call_id /
// tool_call 种类写在最前面。看不出类型就返回 false。
func sniffStreamEvent(b []byte) (rawStreamEvent, bool) {
	if len(b) > streamSniffLen {
		b = b[:streamSniffLen]
	}
	var ev rawStreamEvent
	if ev.Type = sniffString(b, `"type":"`); ev.Type == "" {
		return ev, false
	}
	ev.Subtype = sniffString(b, `"subtype":"`)
	ev.CallID = sniffString(b, `"call_id":"`)
	ev.SessionID = sniffString(b, `"session_id":"`)
	ev.IsError = bytes.Contains(b, []byte(`"is_error":true`))
	if i := bytes.Index(b, []byte(`"tool_call":{"`)); i >= 0 {
		if kind := sniffString(b[i+len(`"tool_call":{`):], `"`); streamKindRe.MatchString(kind) {
			ev.ToolCall = json.RawMessage(`{"` + kind + `":{}}`)
		}
	}
	return ev, true
}

// sniffString 取 marker 之后、下一个引号之前的文本（不处理转义；最长 64 个字符，过长或没找到返回空串）。
func sniffString(b []byte, marker string) string {
	i := bytes.Index(b, []byte(marker))
	if i < 0 {
		return ""
	}
	rest := b[i+len(marker):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 || j > 64 {
		return ""
	}
	return string(rest[:j])
}
