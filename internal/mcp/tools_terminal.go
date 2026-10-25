package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/termexec"
)

// 终端类工具（terminal_targets / terminal_exec / terminal_run）：参数解析与结果格式化。
// 授权、执行与审计全部在 termexec.Executor（Hub 一侧）里完成，这里只做 MCP 协议适配。

// SetTerminalExecutor 注入终端执行能力。未注入（终端功能整体不可用）时三个工具返回明确的错误。
func (s *Server) SetTerminalExecutor(e termexec.Executor) {
	s.termMu.Lock()
	s.termExec = e
	s.termMu.Unlock()
}

func (s *Server) terminalExecutor() termexec.Executor {
	s.termMu.RLock()
	defer s.termMu.RUnlock()
	return s.termExec
}

// flexInt 宽容地解析整数：接受 30、30.0 以及 "30"，因为不同的模型客户端对数字的序列化并不一致。
type flexInt struct {
	V   int
	Set bool
}

func (f *flexInt) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	s := strings.Trim(string(b), `"`)
	if s == "" {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		f.V, f.Set = clampToInt(n), true
		return nil
	}
	fl, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("expected an integer, got %s", string(b))
	}
	f.V, f.Set = clampToInt(int64(fl)), true
	return nil
}

func clampToInt(n int64) int {
	const lim = 1 << 40
	if n > lim {
		return lim
	}
	if n < -lim {
		return -lim
	}
	return int(n)
}

func (s *Server) terminalCaller(credCtx *CredentialContext) termexec.Caller {
	return termexec.Caller{
		CredentialID:   credCtx.CredentialID,
		CredentialName: credCtx.CredentialName,
		Credential:     credCtx.Credential,
		Permissions:    credCtx.Permissions,
		TokenString:    credCtx.TokenString,
		Source:         credCtx.Source,
		OriginHost:     credCtx.HostName,
		RemoteIP:       credCtx.RemoteIP,
		UserAgent:      credCtx.UserAgent,
		Channel:        channelOf(credCtx),
	}
}

// terminalOutcome 把执行结果整理成 dispatchTool 的返回：带稳定错误码的业务错误以 JSON 呈现并标记 isError。
func terminalOutcome(res any, err error) (any, bool, error) {
	if err == nil {
		return res, false, nil
	}
	var te *termexec.Error
	if errors.As(err, &te) {
		payload := map[string]any{"error": map[string]string{"code": te.Code, "message": te.Message}}
		b, _ := json.MarshalIndent(payload, "", "  ")
		return toolCallResult{Content: []mcpContentItem{{Type: "text", Text: string(b)}}, IsError: true}, true, nil
	}
	return nil, true, err
}

func (s *Server) terminalUnavailable() (any, bool, error) {
	return terminalOutcome(nil, &termexec.Error{Code: termexec.CodeDisabled, Message: "终端功能在此服务上不可用"})
}

func decodeToolArgs(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return &termexec.Error{Code: termexec.CodeBadRequest, Message: "参数无法解析：" + err.Error()}
	}
	return nil
}

func (s *Server) handleTerminalTargets(ctx context.Context, credCtx *CredentialContext, raw json.RawMessage) (any, bool, error) {
	ex := s.terminalExecutor()
	if ex == nil {
		return s.terminalUnavailable()
	}
	var args struct {
		Target string `json:"target"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return terminalOutcome(nil, err)
	}
	return terminalOutcome(ex.Targets(ctx, s.terminalCaller(credCtx), args.Target))
}

func (s *Server) handleTerminalExec(ctx context.Context, credCtx *CredentialContext, raw json.RawMessage) (any, bool, error) {
	ex := s.terminalExecutor()
	if ex == nil {
		return s.terminalUnavailable()
	}
	var args struct {
		Target           string  `json:"target"`
		Command          string  `json:"command"`
		Description      string  `json:"description"`
		Thread           string  `json:"thread"`
		WorkflowID       string  `json:"workflow_id"`
		ProjectDirectory string  `json:"project_directory"`
		Cwd              string  `json:"cwd"`
		Shell            string  `json:"shell"`
		TimeoutSeconds   flexInt `json:"timeout_seconds"`
		WaitSeconds      flexInt `json:"wait_seconds"`
		MaxOutputBytes   flexInt `json:"max_output_bytes"`
		IdempotencyKey   string  `json:"idempotency_key"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return terminalOutcome(nil, err)
	}
	return terminalOutcome(ex.Exec(ctx, s.terminalCaller(credCtx), termexec.ExecRequest{
		Target:           args.Target,
		Command:          args.Command,
		Description:      args.Description,
		Thread:           args.Thread,
		WorkflowID:       args.WorkflowID,
		ProjectDirectory: args.ProjectDirectory,
		Cwd:              args.Cwd,
		Shell:            args.Shell,
		TimeoutSeconds:   args.TimeoutSeconds.V,
		WaitSeconds:      args.WaitSeconds.V,
		WaitSet:          args.WaitSeconds.Set,
		MaxOutputBytes:   args.MaxOutputBytes.V,
		IdempotencyKey:   args.IdempotencyKey,
	}))
}

func (s *Server) handleTerminalRun(ctx context.Context, credCtx *CredentialContext, raw json.RawMessage) (any, bool, error) {
	ex := s.terminalExecutor()
	if ex == nil {
		return s.terminalUnavailable()
	}
	var args struct {
		Action      string  `json:"action"`
		RunID       string  `json:"run_id"`
		Stream      string  `json:"stream"`
		Offset      flexInt `json:"offset"`
		Limit       flexInt `json:"limit"`
		WaitSeconds flexInt `json:"wait_seconds"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return terminalOutcome(nil, err)
	}
	return terminalOutcome(ex.Run(ctx, s.terminalCaller(credCtx), termexec.RunRequest{
		Action:      args.Action,
		RunID:       args.RunID,
		Stream:      args.Stream,
		Offset:      int64(args.Offset.V),
		Limit:       args.Limit.V,
		WaitSeconds: args.WaitSeconds.V,
	}))
}

// channelOf 返回调用通道标签（见 termexec.Caller.Channel）。
func channelOf(credCtx *CredentialContext) string {
	if credCtx != nil && credCtx.ViaCapability {
		return "capability"
	}
	return ""
}
