package terminal

import (
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 对话 f5a86e3f 的运行 41（#169）的真实输出尾部：最后一个 stream-json 事件之后，CLI 自己在标准错误上打了一行，退出码 1。
const run41Tail = `{"type":"thinking","subtype":"delta","text":"act workflow module path \"前","session_id":"bd6f639c-5bcc-4c76-9b19-5bfbaec9d2c4","timestamp_ms":1792143182039}
{"type":"thinking","subtype":"completed","session_id":"bd6f639c-5bcc-4c76-9b19-5bfbaec9d2c4","timestamp_ms":1792143182039}
{"type":"tool_call","subtype":"completed","call_id":"toolu_01","tool_call":{"mcpToolCall":{"result":{"error":{"error":"Tool execution error","readToolDefReminder":"Tool call arguments were not valid JSON (Expected ',' or '}' after property value in JSON at position 51 (line 1 column 52))."}}}},"session_id":"bd6f639c-5bcc-4c76-9b19-5bfbaec9d2c4","timestamp_ms":1792143182087}
RetriableError: [resource_exhausted] Error
`

func failedRun(tail string, code int) model.TerminalRun {
	c := code
	return model.TerminalRun{State: model.RunFailed, ExitCode: &c, PreviewTail: tail}
}

func TestClassifyRunExit_RealRun41(t *testing.T) {
	e := classifyRunExit(failedRun(run41Tail, 1))
	if e.Class != ExitRetriable || e.Code != "resource_exhausted" || !e.Retry || !e.IsFailure() {
		t.Fatalf("运行 41 应是可重试的 resource_exhausted：%+v", e)
	}
	if e.Raw != "RetriableError: [resource_exhausted] Error" {
		t.Fatalf("原行：%q", e.Raw)
	}
	for _, want := range []string{"可重试", "限流", "resource_exhausted"} {
		if !strings.Contains(e.Text, want) {
			t.Fatalf("说明里缺 %q：%s", want, e.Text)
		}
	}
	if strings.Contains(e.Text, "：Error") {
		t.Fatalf("消息只有「Error」时不该重复：%s", e.Text)
	}
	v := failureViewOf(e)
	if v == nil || v.Class != "retriable" || !v.Retry || v.Code != "resource_exhausted" {
		t.Fatalf("界面用的失败说明不对：%+v", v)
	}
}

func TestClassifyRunExit_States(t *testing.T) {
	cases := []struct {
		state string
		want  RunExitClass
		fail  bool
	}{
		{model.RunSucceeded, ExitOK, false},
		{model.RunCancelled, ExitCancelled, false},
		{model.RunTimedOut, ExitTimedOut, false},
		{model.RunRejected, ExitNotRun, false},
		{model.RunDenied, ExitNotRun, false},
		{model.RunLost, ExitLost, false},
		{model.RunRunning, "", false},
		{model.RunAwaitingApproval, "", false},
	}
	for _, c := range cases {
		e := classifyRunExit(model.TerminalRun{State: c.state, PreviewTail: run41Tail})
		if e.Class != c.want || e.IsFailure() != c.fail || e.Retry {
			t.Errorf("状态 %s：%+v，应是 %q（失败=%v，不续跑）", c.state, e, c.want, c.fail)
		}
		if failureViewOf(e) != nil {
			t.Errorf("状态 %s 不该有失败说明", c.state)
		}
	}
}

func TestClassifyRunExit_CLIErrorKinds(t *testing.T) {
	cases := []struct {
		name  string
		tail  string
		class RunExitClass
		code  string
		retry bool
		text  string
	}{
		{"要你处理", "...\nActionRequiredError: [unauthenticated] Please log in again\n", ExitActionRequired, "unauthenticated", false, "登录已失效"},
		{"不可重试", "NonRetriableError: [invalid_argument] bad model\n", ExitNonRetriable, "invalid_argument", false, "bad model"},
		{"连接失败带消息", "RetriableError: [unavailable] Connection failed 10 times\n", ExitRetriable, "unavailable", true, "Connection failed 10 times"},
		{"错误码大写也认", "RetriableError: [RESOURCE_EXHAUSTED] Error\n", ExitRetriable, "resource_exhausted", true, "限流"},
		{"没有错误码", "NonRetriableError: Step limit not supported\n", ExitNonRetriable, "", false, "Step limit not supported"},
		{"错误行后面还有栈", "RetriableError: [internal] Error\n    at foo (index.js:1:1)\n    at bar (index.js:2:2)\n\n", ExitRetriable, "internal", true, "服务端内部错误"},
		{"彩色输出", "\x1b[31mRetriableError: [unavailable] down\x1b[0m\n", ExitRetriable, "unavailable", true, "服务暂时不可用"},
		{"不认识的错误码", "RetriableError: [brand_new_code] x\n", ExitRetriable, "brand_new_code", true, "错误码 brand_new_code"},
	}
	for _, c := range cases {
		e := classifyRunExit(failedRun(c.tail, 1))
		if e.Class != c.class || e.Code != c.code || e.Retry != c.retry || !strings.Contains(e.Text, c.text) {
			t.Errorf("%s：%+v，应是 %s / %q / retry=%v / 含 %q", c.name, e, c.class, c.code, c.retry, c.text)
		}
	}
}

func TestClassifyRunExit_HeadOnlyAndNoise(t *testing.T) {
	// 输出很短时整份都在 PreviewHead、PreviewTail 为空。
	rec := model.TerminalRun{State: model.RunFailed, PreviewHead: "RetriableError: [unavailable] x\n"}
	if e := classifyRunExit(rec); e.Class != ExitRetriable {
		t.Fatalf("开头预览里的错误行也要认：%+v", e)
	}
	// 智能体的回复里引用这句话：它在 JSON 字符串里，不是顶格的错误行，不能误判成可重试。
	quoted := `{"type":"result","subtype":"success","is_error":false,"result":"上次报了 RetriableError: [resource_exhausted] Error，已重试","session_id":"s"}` + "\n"
	e := classifyRunExit(failedRun(quoted, 1))
	if e.Class != ExitUnknown || e.Retry {
		t.Fatalf("引用在 JSON 里的文字不该当成 CLI 的错误行：%+v", e)
	}
	// 太靠前的错误行（后面还有一大堆输出）不认：CLI 的错误行紧挨着末尾。
	far := "RetriableError: [unavailable] x\n" + strings.Repeat("{\"type\":\"thinking\"}\n", exitScanLines+5)
	if e := classifyRunExit(failedRun(far, 1)); e.Class != ExitUnknown {
		t.Fatalf("离末尾太远的行不认：%+v", e)
	}
}

func TestClassifyRunExit_Unknown(t *testing.T) {
	e := classifyRunExit(failedRun("some stack trace\nError: boom\n", 7))
	if e.Class != ExitUnknown || e.Retry || !e.IsFailure() || !strings.Contains(e.Text, "退出码 7") {
		t.Fatalf("认不出的失败：%+v", e)
	}
	e = classifyRunExit(model.TerminalRun{State: model.RunFailed})
	if e.Class != ExitUnknown || !strings.Contains(e.Text, "退出码 未知") {
		t.Fatalf("没有退出码也不能崩：%+v", e)
	}
}

func TestClassifyRunExit_Signals(t *testing.T) {
	for sig, retry := range map[string]bool{"SIGKILL": true, "SIGSEGV": true, "SIGABRT": true, "SIGTERM": false, "SIGINT": false, "SIGHUP": false, "SIGPIPE": false, "SIG31": false} {
		rec := model.TerminalRun{State: model.RunFailed, Signal: sig, PreviewTail: run41Tail}
		e := classifyRunExit(rec)
		if e.Class != ExitCrashed || e.Retry != retry || !strings.Contains(e.Text, sig) {
			t.Errorf("信号 %s：%+v，应是崩溃 / retry=%v", sig, e, retry)
		}
	}
	// 信号是硬事实：它排在输出里的错误行前面。
	rec := model.TerminalRun{State: model.RunFailed, Signal: "sigkill", PreviewTail: "ActionRequiredError: [unauthenticated] x\n"}
	if e := classifyRunExit(rec); e.Class != ExitCrashed || !e.Retry {
		t.Fatalf("小写信号名也要认，且信号优先：%+v", e)
	}
}
