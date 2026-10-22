package termproto

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const testRunID = "0123456789abcdef0123456789abcdef"

func TestValidRunID(t *testing.T) {
	for _, ok := range []string{testRunID, strings.Repeat("a", 32), strings.Repeat("0", 32)} {
		if !ValidRunID(ok) {
			t.Errorf("%q 应当合法", ok)
		}
	}
	for _, bad := range []string{"", "xyz", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), "../" + strings.Repeat("a", 29)} {
		if ValidRunID(bad) {
			t.Errorf("%q 应当非法", bad)
		}
	}
}

func TestStreamNames(t *testing.T) {
	if StreamName(StreamStdout) != "stdout" || StreamName(StreamStderr) != "stderr" || StreamName(0) != "" || StreamName(9) != "" {
		t.Fatal("流名称不对")
	}
	if s, ok := ParseStream("stderr"); !ok || s != StreamStderr {
		t.Fatal("解析 stderr 失败")
	}
	if _, ok := ParseStream("stdin"); ok {
		t.Fatal("stdin 不是合法的输出流")
	}
}

func TestRunOutRoundTrip(t *testing.T) {
	payload := []byte("hello\x00\xff world")
	frame := EncodeRunOut(testRunID, StreamStderr, 1<<40+7, payload)
	if frame == nil {
		t.Fatal("编码失败")
	}
	if typ, err := FrameType(frame); err != nil || typ != FrameRunOut {
		t.Fatalf("帧类型 = %v %v", typ, err)
	}
	id, stream, off, got, err := DecodeRunOut(frame)
	if err != nil {
		t.Fatal(err)
	}
	if id != testRunID || stream != StreamStderr || off != 1<<40+7 || !bytes.Equal(got, payload) {
		t.Fatalf("往返不一致: %q %d %d %q", id, stream, off, got)
	}
}

func TestRunOutRejectsBadInput(t *testing.T) {
	if EncodeRunOut("not-an-id", StreamStdout, 0, []byte("x")) != nil {
		t.Fatal("非法 ID 不应编码成功")
	}
	good := EncodeRunOut(testRunID, StreamStdout, 0, []byte("x"))
	if _, _, _, _, err := DecodeRunOut(good[:10]); err != ErrShortFrame {
		t.Fatalf("短帧应报 ErrShortFrame，得到 %v", err)
	}
	wrong := append([]byte(nil), good...)
	wrong[0] = FrameOutput
	if _, _, _, _, err := DecodeRunOut(wrong); err != ErrWrongFrame {
		t.Fatalf("错误类型应报 ErrWrongFrame，得到 %v", err)
	}
	badStream := append([]byte(nil), good...)
	badStream[1+runIDLen] = 7
	if _, _, _, _, err := DecodeRunOut(badStream); err != ErrWrongFrame {
		t.Fatalf("未知流应报 ErrWrongFrame，得到 %v", err)
	}
	big := EncodeRunOut(testRunID, StreamStdout, 0, make([]byte, MaxDataPayload+1))
	if _, _, _, _, err := DecodeRunOut(big); err != ErrPayloadTooBig {
		t.Fatalf("超大净荷应报 ErrPayloadTooBig，得到 %v", err)
	}
}

func TestRunDataRoundTrip(t *testing.T) {
	in := RunChunk{Req: 77, Flags: RunFlagEOF | RunFlagLast, Stream: StreamStdout, Off: 100, Next: 4096, Total: 5000, Data: []byte("abc")}
	got, err := DecodeRunData(EncodeRunData(in))
	if err != nil {
		t.Fatal(err)
	}
	if got.Req != in.Req || got.Flags != in.Flags || got.Stream != in.Stream || got.Off != in.Off ||
		got.Next != in.Next || got.Total != in.Total || string(got.Data) != "abc" {
		t.Fatalf("往返不一致: %+v", got)
	}
	if _, err := DecodeRunData(make([]byte, 5)); err != ErrShortFrame {
		t.Fatalf("短帧: %v", err)
	}
	over := EncodeRunData(RunChunk{Stream: StreamStdout, Data: make([]byte, MaxRunDataFrame+1)})
	if _, err := DecodeRunData(over); err != ErrPayloadTooBig {
		t.Fatalf("超大帧: %v", err)
	}
	bad := EncodeRunData(RunChunk{Stream: 9})
	if _, err := DecodeRunData(bad); err != ErrWrongFrame {
		t.Fatalf("未知流: %v", err)
	}
}

func TestRunEndSanitized(t *testing.T) {
	code := 3
	in := RunEnd{
		ID: testRunID, State: "weird", Code: &code, Signal: "SIG\x1bKILL\n", Reason: strings.Repeat("原", 500),
		DurMS: -5, OutBytes: -1, ErrBytes: 9, OutDropped: -3,
		Out:      make([]byte, RunInlineMax+100),
		PrevHead: make([]byte, RunPreviewHead+1),
		PrevTail: make([]byte, RunPreviewTail+1),
	}
	got, ok := in.Sanitized()
	if !ok {
		t.Fatal("合法 ID 不应被丢弃")
	}
	if got.State != RunLost {
		t.Errorf("未知状态应回落为 lost，得到 %q", got.State)
	}
	if got.T != TRunEnd {
		t.Errorf("T = %q", got.T)
	}
	if strings.ContainsAny(got.Signal, "\x1b\n") || got.Signal != "SIGKILL" {
		t.Errorf("Signal 未净化: %q", got.Signal)
	}
	if len([]rune(got.Reason)) > 96 {
		t.Errorf("Reason 未限长: %d", len([]rune(got.Reason)))
	}
	if got.DurMS != 0 || got.OutBytes != 0 || got.OutDropped != 0 || got.ErrBytes != 9 {
		t.Errorf("负数应归零: %+v", got)
	}
	if len(got.Out) != RunInlineMax || len(got.PrevHead) != RunPreviewHead || len(got.PrevTail) != RunPreviewTail {
		t.Errorf("字节字段未裁剪: %d %d %d", len(got.Out), len(got.PrevHead), len(got.PrevTail))
	}
	if _, ok := (RunEnd{ID: "bad"}).Sanitized(); ok {
		t.Error("非法 ID 应被丢弃")
	}
	for _, s := range []string{RunExited, RunSignaled, RunTimeout, RunCancelled, RunLost} {
		if g, _ := (RunEnd{ID: testRunID, State: s}).Sanitized(); g.State != s {
			t.Errorf("已知状态 %q 被改写为 %q", s, g.State)
		}
	}
}

func TestRunEndJSONUsesBase64ForBytes(t *testing.T) {
	// 输出里可能有任意字节：用 base64 承载，既不会膨胀（JSON 转义最坏 6 倍），也不会因非法 UTF-8 被改写。
	raw := []byte{0x00, 0x1b, 0xff, 0xfe, 'a', '\n'}
	data, err := json.Marshal(RunEnd{T: TRunEnd, ID: testRunID, State: RunExited, Out: raw})
	if err != nil {
		t.Fatal(err)
	}
	var back RunEnd
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Out, raw) {
		t.Fatalf("字节往返不一致: %v", back.Out)
	}
	if strings.Contains(string(data), `\u0000`) {
		t.Fatalf("不应出现逐字节转义: %s", data)
	}
}

func TestRunStartedAndFailedSanitized(t *testing.T) {
	s, ok := (RunStarted{ID: testRunID, PID: -4, Shell: "ba\x07sh", Cwd: "/tmp\n/evil"}).Sanitized()
	if !ok || s.PID != 0 || s.Shell != "bash" || strings.Contains(s.Cwd, "\n") {
		t.Fatalf("RunStarted 未净化: %+v ok=%v", s, ok)
	}
	if _, ok := (RunStarted{ID: "x"}).Sanitized(); ok {
		t.Error("非法 ID 应被丢弃")
	}
	f, ok := (RunFailed{ID: testRunID, Reason: "bad\x00reason", Detail: strings.Repeat("x", 500)}).Sanitized()
	if !ok || strings.ContainsRune(f.Reason, 0) || len(f.Detail) > 160 {
		t.Fatalf("RunFailed 未净化: %+v ok=%v", f, ok)
	}
}

func TestRunsSanitizedDropsInvalidAndCaps(t *testing.T) {
	in := Runs{Running: []RunBrief{{ID: "bad"}, {ID: testRunID, PID: -1, Started: 5}}}
	for i := 0; i < MaxRunsPerFrame+10; i++ {
		in.Running = append(in.Running, RunBrief{ID: testRunID})
	}
	got := in.Sanitized()
	if len(got.Running) != MaxRunsPerFrame {
		t.Fatalf("应封顶 %d 条，得到 %d", MaxRunsPerFrame, len(got.Running))
	}
	if got.Running[0].PID != 0 || got.Running[0].Started != 5 {
		t.Fatalf("第一项被改写: %+v", got.Running[0])
	}
	if got.T != TRuns {
		t.Errorf("T = %q", got.T)
	}
}

func TestHolderSanitizedKeepsExecInfoAndDenyFlag(t *testing.T) {
	h := Holder{State: HolderReady, DenyExec: true, Exec: &ExecInfo{Shell: "ba\x1bsh", Home: "/home/u\nx", Sep: "x"}}.Sanitized()
	if !h.DenyExec {
		t.Error("DenyExec 丢失")
	}
	if h.Exec == nil || h.Exec.Shell != "bash" || strings.Contains(h.Exec.Home, "\n") || h.Exec.Sep != "" {
		t.Errorf("Exec 未净化: %+v", h.Exec)
	}
	win := Holder{State: HolderReady, Exec: &ExecInfo{Sep: "\\"}}.Sanitized()
	if win.Exec == nil || win.Exec.Sep != "\\" {
		t.Errorf("反斜杠分隔符应保留: %+v", win.Exec)
	}
	if (Holder{State: HolderReady}).Sanitized().Exec != nil {
		t.Error("没有 Exec 时不应凭空出现")
	}
}
