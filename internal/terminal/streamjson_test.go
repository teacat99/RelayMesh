package terminal

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agentstate"
)

// ---------- 夹具：与 Cursor CLI 的 stream-json 同形的几类行 ----------

const (
	lnInit      = `{"type":"system","subtype":"init","apiKeySource":"login","cwd":"/tmp/w","session_id":"s-1","model":"m","permissionMode":"default"}`
	lnUser      = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]},"session_id":"s-1"}`
	lnThink     = `{"type":"thinking","subtype":"delta","text":"hmm","session_id":"s-1","timestamp_ms":1760000000000}`
	lnThinkDone = `{"type":"thinking","subtype":"completed","session_id":"s-1","timestamp_ms":1760000000100}`
	lnAssistant = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"session_id":"s-1"}`
	lnResultOK  = `{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,"result":"全部完成","session_id":"s-1","request_id":"r-1"}`
	lnResultErr = `{"type":"result","subtype":"error","is_error":true,"duration_ms":10,"result":"boom","session_id":"s-1"}`
)

func toolStarted(id, kind string) string {
	return fmt.Sprintf(`{"type":"tool_call","subtype":"started","call_id":%q,"tool_call":{%q:{"args":{"command":"sleep 20","path":"a.txt"}}},"session_id":"s-1"}`, id, kind)
}

func toolDone(id, kind, resultKey string) string {
	return fmt.Sprintf(`{"type":"tool_call","subtype":"completed","call_id":%q,"tool_call":{%q:{"args":{"command":"sleep 20"},"result":{%q:{"exitCode":0,"stdout":"x"}}}},"session_id":"s-1"}`, id, kind, resultKey)
}

func join(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// brief 把信号压成「名字[:工具|:状态]」，便于整串比较。
func brief(sigs []runSignal) []string {
	out := make([]string, 0, len(sigs))
	for _, s := range sigs {
		b := s.Name
		switch {
		case s.Tool != "":
			b += ":" + s.Tool
		case s.Status != "":
			b += ":" + s.Status
		}
		out = append(out, b)
	}
	return out
}

func feedAll(p *streamParser, s string) []string { return brief(p.Feed([]byte(s))) }

func expect(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("signals:\n got  %v\n want %v", got, want)
	}
}

// ---------- 基本形状 ----------

func TestStreamParserTypicalRun(t *testing.T) {
	var p streamParser
	got := feedAll(&p, join(
		lnInit, lnUser, lnThink, lnThink, lnThinkDone,
		toolStarted("c1", "shellToolCall"),
		toolDone("c1", "shellToolCall", "success"),
		lnAssistant, lnResultOK,
	))
	expect(t, got, []string{
		"prompt", "thought", "thought", "thought",
		"tool_start:Shell", "tool_end:Shell",
		"response", "stop:completed",
	})
	if p.InFlight() != 0 {
		t.Fatalf("in flight after result = %d", p.InFlight())
	}
}

func TestStreamParserResultStatuses(t *testing.T) {
	for name, line := range map[string]string{
		"is_error":      lnResultErr,
		"subtype only":  `{"type":"result","subtype":"error_max_turns","result":"x"}`,
		"is_error only": `{"type":"result","is_error":true}`,
	} {
		var p streamParser
		expect(t, feedAll(&p, join(line)), []string{"stop:error"})
		_ = name
	}
	var p streamParser
	expect(t, feedAll(&p, join(`{"type":"result","result":"no subtype, not an error"}`)), []string{"stop:completed"})
}

func TestStreamParserToolNames(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{toolStarted("a", "shellToolCall"), "tool_start:Shell"},
		{toolStarted("a", "readToolCall"), "tool_start:Read"},
		{toolStarted("a", "editToolCall"), "tool_start:Edit"},
		{toolStarted("a", "grepToolCall"), "tool_start:Grep"},
		{toolStarted("a", "globToolCall"), "tool_start:Glob"},
		{`{"type":"tool_call","subtype":"started","call_id":"a","tool_call":{"mcpToolCall":{"args":{"name":"relaymesh-interactive_feedback","toolName":"interactive_feedback","providerIdentifier":"relaymesh","args":{}}}}}`, "tool_start:MCP:interactive_feedback"},
		{`{"type":"tool_call","subtype":"started","call_id":"a","tool_call":{"mcpToolCall":{"args":{"name":"get_system_info"}}}}`, "tool_start:MCP:get_system_info"},
		{`{"type":"tool_call","subtype":"started","call_id":"a","tool_call":{"mcpToolCall":{"args":{}}}}`, "tool_start:MCP"},
		{`{"type":"tool_call","subtype":"started","call_id":"a","tool_call":{"function":{"name":"web_search","arguments":"{}"}}}`, "tool_start:web_search"},
		{`{"type":"tool_call","subtype":"started","call_id":"a","tool_call":{"function":{"arguments":"{}"}}}`, "tool_start:Function"},
		{`{"type":"tool_call","subtype":"started","call_id":"a","tool_call":{"bad key!":{}}}`, "tool_start"},
		{`{"type":"tool_call","subtype":"started","call_id":"a","tool_call":{}}`, "tool_start"},
		{`{"type":"tool_call","subtype":"started","call_id":"a"}`, "tool_start"},
		{`{"type":"tool_call","subtype":"started","call_id":"a","tool_call":"oops"}`, "tool_start"},
	}
	for _, c := range cases {
		var p streamParser
		expect(t, feedAll(&p, join(c.line)), []string{c.want})
	}
}

func TestStreamParserToolFailureKinds(t *testing.T) {
	for result, want := range map[string]string{
		"success":  "tool_end:Shell",
		"error":    "tool_fail:Shell",
		"rejected": "tool_fail:Shell",
		"timeout":  "tool_fail:Shell",
		"someNew":  "tool_end:Shell", // 不认识的结果键：当作正常结束
	} {
		var p streamParser
		feedAll(&p, join(toolStarted("c", "shellToolCall")))
		expect(t, feedAll(&p, join(toolDone("c", "shellToolCall", result))), []string{want})
	}
	// subtype 直接是 failed。
	var p streamParser
	feedAll(&p, join(toolStarted("c", "readToolCall")))
	expect(t, feedAll(&p, join(`{"type":"tool_call","subtype":"failed","call_id":"c"}`)), []string{"tool_fail:Read"})
}

func TestStreamParserCompletedTakesTheNameFromTheStart(t *testing.T) {
	// completed 事件没带 tool_call 对象（或种类认不出）时，用开始时记下的名字。
	var p streamParser
	feedAll(&p, join(toolStarted("c1", "shellToolCall")))
	got := feedAll(&p, join(`{"type":"tool_call","subtype":"completed","call_id":"c1"}`))
	expect(t, got, []string{"tool_end:Shell"})
	// 没见过开始的 completed（比如缺口之后）：名字空，照样产生结束信号。
	expect(t, feedAll(&p, join(`{"type":"tool_call","subtype":"completed","call_id":"zzz"}`)), []string{"tool_end"})
}

func TestStreamParserParallelToolCalls(t *testing.T) {
	var p streamParser
	expect(t, feedAll(&p, join(toolStarted("a", "readToolCall"), toolStarted("b", "shellToolCall"))),
		[]string{"tool_start:Read", "tool_start:Shell"})
	// A 先结束，B 还在跑：状态要重新指回 B，不能掉回「思考」。
	expect(t, feedAll(&p, join(toolDone("a", "readToolCall", "success"))), []string{"tool_end:Read", "tool_start:Shell"})
	expect(t, feedAll(&p, join(toolDone("b", "shellToolCall", "success"))), []string{"tool_end:Shell"})
	if p.InFlight() != 0 {
		t.Fatalf("in flight = %d", p.InFlight())
	}
}

func TestStreamParserInFlightIsBounded(t *testing.T) {
	var p streamParser
	var b strings.Builder
	for i := 0; i < streamMaxFlight*3; i++ {
		b.WriteString(toolStarted(fmt.Sprintf("c%d", i), "shellToolCall"))
		b.WriteByte('\n')
	}
	p.Feed([]byte(b.String()))
	if p.InFlight() > streamMaxFlight || len(p.order) > streamMaxFlight {
		t.Fatalf("in flight %d / order %d exceed cap %d", p.InFlight(), len(p.order), streamMaxFlight)
	}
}

// ---------- 容忍现实 ----------

func TestStreamParserIgnoresNoise(t *testing.T) {
	var p streamParser
	got := feedAll(&p, "\n   \nplain text error: something broke\n"+
		`{"type":"mystery","subtype":"x"}`+"\n"+
		`{"no_type":true}`+"\n"+
		`[1,2,3]`+"\n"+
		`{"type":`+"\n"+ // 被截断的半行
		`{"type":"thinking","subtype":"delta"`+"\n"+ // 缺右花括号
		"\x00\x01\xff garbage \n"+
		join(lnInit))
	expect(t, got, []string{"prompt"})
}

func TestStreamParserAcceptsCRLFAndLeadingSpaces(t *testing.T) {
	var p streamParser
	expect(t, feedAll(&p, "  "+lnInit+"\r\n"+lnThink+"\r\n"), []string{"prompt", "thought"})
}

func TestStreamParserEveryChunkBoundaryGivesTheSameSignals(t *testing.T) {
	sample := join(lnInit, lnUser, lnThink, toolStarted("c1", "shellToolCall"), toolDone("c1", "shellToolCall", "error"), lnAssistant, lnResultOK)
	var whole streamParser
	want := feedAll(&whole, sample)
	if len(want) != 6 {
		t.Fatalf("setup: %v", want)
	}
	for i := 0; i <= len(sample); i++ {
		var p streamParser
		got := append(feedAll(&p, sample[:i]), feedAll(&p, sample[i:])...)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("split at %d:\n got  %v\n want %v", i, got, want)
		}
	}
	// 一个字节一个字节喂。
	var p streamParser
	var got []string
	for i := 0; i < len(sample); i++ {
		got = append(got, feedAll(&p, sample[i:i+1])...)
	}
	expect(t, got, want)
}

func TestStreamParserFlushHandlesTheLastLineWithoutNewline(t *testing.T) {
	var p streamParser
	if got := feedAll(&p, join(lnInit)+lnResultOK); !reflect.DeepEqual(got, []string{"prompt"}) {
		t.Fatalf("before flush: %v", got)
	}
	expect(t, brief(p.Flush()), []string{"stop:completed"})
	if got := brief(p.Flush()); len(got) != 0 {
		t.Fatalf("second flush must be empty: %v", got)
	}
	// 半截的最后一行：不产生信号，也不留下缓冲。
	var q streamParser
	q.Feed([]byte(`{"type":"resu`))
	if got := brief(q.Flush()); len(got) != 0 {
		t.Fatalf("truncated tail: %v", got)
	}
	if len(q.buf) != 0 {
		t.Fatalf("buffer must be empty after flush, has %d", len(q.buf))
	}
}

func TestStreamParserResyncDropsTheRestOfAPartialLine(t *testing.T) {
	var p streamParser
	feedAll(&p, join(toolStarted("c1", "shellToolCall")))
	// 缺口之前读到半行，缺口之后接上的是另一行的后半截。
	p.Feed([]byte(`{"type":"thinking","sub`))
	p.Resync()
	got := feedAll(&p, `type":"result","subtype":"success"}`+"\n"+join(lnThink))
	expect(t, got, []string{"thought"}) // 残行（里面看着像 result）被丢弃，下一行恢复
	// 在途工具的记录跨缺口保留：缺口后结束的调用仍能认出名字。
	expect(t, feedAll(&p, join(`{"type":"tool_call","subtype":"completed","call_id":"c1"}`)), []string{"tool_end:Shell"})

	// 缺口恰好落在行边界（缓冲为空）：下一行完整，但也被当作残行丢掉一行——宁可少一条信号，不能读错。
	var q streamParser
	q.Resync()
	got = feedAll(&q, join(lnThink, lnThink))
	expect(t, got, []string{"thought"})
}

// ---------- 超长行 ----------

func TestStreamParserHugeLinesAreSniffedNotBuffered(t *testing.T) {
	huge := strings.Repeat("x", 3<<20) // 3 MiB 的文件内容

	var p streamParser
	feedAll(&p, join(toolStarted("c1", "readToolCall")))
	completed := fmt.Sprintf(`{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"readToolCall":{"args":{"path":"big"},"result":{"success":{"content":%q}}}}}`, huge)
	// 分成 64 KiB 的块喂入，模拟读取页。
	var got []string
	data := []byte(completed + "\n")
	for len(data) > 0 {
		n := 64 << 10
		if n > len(data) {
			n = len(data)
		}
		got = append(got, feedAll(&p, string(data[:n]))...)
		if len(p.buf) > streamLineMax {
			t.Fatalf("line buffer grew to %d", len(p.buf))
		}
		data = data[n:]
	}
	expect(t, got, []string{"tool_end:Read"})
	if cap(p.buf) > streamKeepBuf {
		t.Fatalf("buffer capacity %d was kept after a huge line", cap(p.buf))
	}

	// 超长的 started（写大文件的参数）：靠开头认出种类；紧接着的正常行不受影响。
	started := fmt.Sprintf(`{"type":"tool_call","subtype":"started","call_id":"c2","tool_call":{"writeToolCall":{"args":{"path":"big","fileText":%q}}}}`, huge)
	got = feedAll(&p, started+"\n"+join(lnThink))
	expect(t, got, []string{"tool_start:Write", "thought"})

	// 超长的 result：is_error 在前面，嗅探得到。
	longResult := `{"type":"result","subtype":"error","is_error":true,"result":"` + huge + `"}`
	expect(t, feedAll(&p, longResult+"\n"), []string{"stop:error"})
	longOK := `{"type":"result","subtype":"success","is_error":false,"result":"` + huge + `"}`
	expect(t, feedAll(&p, longOK+"\n"), []string{"stop:completed"})
}

func TestStreamParserHugeLineWithoutRecognizableTypeIsDropped(t *testing.T) {
	var p streamParser
	line := `{"payload":"` + strings.Repeat("y", 1<<20) + `"}`
	expect(t, feedAll(&p, line+"\n"+join(lnInit)), []string{"prompt"})
}

func TestStreamParserHugeNonJSONLineKeepsMemoryBounded(t *testing.T) {
	var p streamParser
	p.Feed([]byte(strings.Repeat("z", 5<<20))) // 没有换行的 5 MiB 垃圾
	if len(p.buf) > streamLineMax {
		t.Fatalf("buffer = %d", len(p.buf))
	}
	expect(t, feedAll(&p, "\n"+join(lnInit)), []string{"prompt"})
}

// ---------- 乱输入不能崩 ----------

func TestStreamParserSurvivesRandomInput(t *testing.T) {
	rng := rand.New(rand.NewSource(129))
	pieces := []string{
		lnInit, lnThink, lnAssistant, lnResultOK, lnResultErr,
		toolStarted("a", "shellToolCall"), toolDone("a", "shellToolCall", "error"),
		`{"type":"tool_call","subtype":"started"`, `{"type":`, `}`, `{`, `"`, "\x00", "\r", "\xff\xfe",
		`{"type":"tool_call","subtype":"completed","call_id":"a","tool_call":{"x":`,
	}
	for iter := 0; iter < 3000; iter++ {
		var p streamParser
		var b strings.Builder
		for n := rng.Intn(12); n >= 0; n-- {
			b.WriteString(pieces[rng.Intn(len(pieces))])
			if rng.Intn(3) > 0 {
				b.WriteByte('\n')
			}
		}
		data := []byte(b.String())
		for len(data) > 0 {
			n := 1 + rng.Intn(40)
			if n > len(data) {
				n = len(data)
			}
			p.Feed(data[:n])
			if rng.Intn(25) == 0 {
				p.Resync()
			}
			data = data[n:]
		}
		p.Flush()
		if len(p.buf) > streamLineMax {
			t.Fatalf("iter %d: buffer %d", iter, len(p.buf))
		}
	}
}

// ---------- 折叠与名字 ----------

func TestCollapseSignals(t *testing.T) {
	th := runSignal{Name: agentstate.EvThought}
	rs := runSignal{Name: agentstate.EvResponse}
	ts := runSignal{Name: agentstate.EvToolStart, Tool: "Shell"}
	got := brief(collapseSignals([]runSignal{th, th, th, ts, ts, rs, rs, th}))
	// 连续的思考 / 回复折成一个；连续的 tool_start 不折（它们可能是两个调用）。
	expect(t, got, []string{"thought", "tool_start:Shell", "tool_start:Shell", "response", "thought"})
	if got := collapseSignals(nil); len(got) != 0 {
		t.Fatalf("nil: %v", got)
	}
	one := []runSignal{th}
	if got := collapseSignals(one); len(got) != 1 {
		t.Fatalf("single: %v", got)
	}
}

func TestKindDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"shellToolCall": "Shell", "readToolCall": "Read", "ToolCall": "", "": "",
		"todoWrite": "TodoWrite", "9bad": "", "has space": "", "a.b": "",
	} {
		if got := kindDisplayName(in); got != want {
			t.Errorf("kindDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}
