package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
	"github.com/teacat99/RelayMesh/internal/store"
)

type interactiveFeedbackArgs struct {
	ProjectDirectory string `json:"project_directory"`
	Summary          string `json:"summary"`
	Title            string `json:"title,omitempty"`
	WorkflowID       string `json:"workflow_id,omitempty"`
	HostName         string `json:"host_name,omitempty"`
}

type continueFeedbackArgs struct {
	SessionID  string `json:"session_id,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty"`
}

func (s *Server) handleInteractiveFeedback(ctx context.Context, raw json.RawMessage) (any, error) {
	var args interactiveFeedbackArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	if strings.TrimSpace(args.Summary) == "" {
		return nil, store.NewInvalidInputError("summary is required and cannot be empty")
	}

	timeoutSec := s.cfg.FeedbackTimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 120
	}

	hostName := strings.TrimSpace(args.HostName)
	if hostName == "" {
		hostName = s.cfg.HostName
	}

	session, err := s.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID:       args.WorkflowID,
		HostName:         hostName,
		ProjectDirectory: args.ProjectDirectory,
		Title:            args.Title,
		Summary:          args.Summary,
		TimeoutSeconds:   timeoutSec,
	})
	if err != nil {
		return nil, err
	}

	// 只要 session 状态已经为 completed（例如命中了提前暂存的 QueuedFeedback 秒回），直接格式化返回，无需进入挂起等待
	if session.Status == "completed" {
		s.markConsumedAndBroadcast(ctx, session)
		return formatFeedbackResult(session), nil
	}

	s.notifySessionUpdate(session.ID)

	// Wait for user feedback or timeout
	return s.waitForFeedback(ctx, session.ID, timeoutSec)
}

func (s *Server) handleContinueFeedbackSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var args continueFeedbackArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	targetSessionID := strings.TrimSpace(args.SessionID)
	// 若未显式传入 session_id 但指定了 workflow_id，自动查找该工作流最新会话（优先 pending 轮次）
	if targetSessionID == "" && strings.TrimSpace(args.WorkflowID) != "" {
		latestSess, err := s.store.GetLatestWorkflowFeedbackSession(ctx, strings.TrimSpace(args.WorkflowID))
		if err != nil {
			return nil, err
		}
		targetSessionID = latestSess.ID
	}

	if targetSessionID == "" {
		return nil, store.NewInvalidInputError("session_id or workflow_id is required")
	}

	// 1. 先检查该会话是否已经被用户提前提交了反馈 (状态为 completed)
	sess, err := s.store.GetFeedbackSession(ctx, targetSessionID)
	if err != nil {
		return nil, err
	}
	if sess.Status == "completed" {
		s.markConsumedAndBroadcast(ctx, sess)
		return formatFeedbackResult(sess), nil
	}
	if sess.Status == "cancelled" {
		return formatCancelledResult(sess), nil
	}

	timeoutSec := s.cfg.FeedbackTimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 120
	}

	session, err := s.store.KeepaliveFeedbackSession(ctx, targetSessionID, timeoutSec)
	if err != nil {
		return nil, err
	}

	s.notifySessionUpdate(session.ID)
	return s.waitForFeedback(ctx, session.ID, timeoutSec)
}

func (s *Server) handleGetSystemInfo(ctx context.Context, raw json.RawMessage) (any, error) {
	info := map[string]any{
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"num_cpu":      runtime.NumCPU(),
		"go_version":   runtime.Version(),
		"server_name":  "RelayMesh Feedback & Task Hub",
		"version":      "1.0.0",
		"project_id":   s.cfg.ProjectID,
		"current_time": time.Now().Format(time.RFC3339),
	}
	bytes, _ := json.MarshalIndent(info, "", "  ")
	return string(bytes), nil
}

func (s *Server) waitForFeedback(ctx context.Context, sessionID string, timeoutSec int) (any, error) {
	// Cursor/IDE MCP 客户端传输层单次请求硬超时通常在 50~60 秒。
	// 为了彻底杜绝客户端出现 -32001 Request timed out 报错，服务端单次 Long-Polling 挂起等待上限严格限制为 40 秒。
	// 若 40 秒内用户未在 Web 端提交，服务端主动返回 HTTP 200「=== 等待回执 ===」，由 AI 读取提示词并根据设定的 PromptWaitMinutes 执行真实 sleep。
	effectiveWaitSec := timeoutSec
	if effectiveWaitSec <= 0 || effectiveWaitSec > 40 {
		effectiveWaitSec = 40
	}

	// Register waiter channel
	waitCh := s.registerSessionWaiter(sessionID, effectiveWaitSec)
	defer func() {
		s.unregisterSessionWaiter(sessionID, waitCh)
		s.RecordKeepaliveResponse(sessionID)
		s.notifySessionUpdate(sessionID)
	}()

	s.notifySessionUpdate(sessionID)

	waitDuration := time.Duration(effectiveWaitSec) * time.Second
	if waitDuration < 1*time.Second {
		waitDuration = 1 * time.Second
	}

	timer := time.NewTimer(waitDuration)
	defer timer.Stop()

	// Poll or wait on channel
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-timer.C:
			// Timeout reached, return keepalive prompt
			sess, err := s.store.GetFeedbackSession(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			if sess.Status == "completed" {
				s.markConsumedAndBroadcast(ctx, sess)
				return formatFeedbackResult(sess), nil
			}
			s.RecordKeepaliveResponse(sessionID)
			s.notifySessionUpdate(sessionID)
			return s.formatKeepaliveResult(ctx, sess), nil

		case updatedSess := <-waitCh:
			if updatedSess != nil && updatedSess.Status == "completed" {
				s.markConsumedAndBroadcast(ctx, updatedSess)
				return formatFeedbackResult(updatedSess), nil
			}
			if updatedSess != nil && updatedSess.Status == "cancelled" {
				return formatCancelledResult(updatedSess), nil
			}
		}
	}
}

func (s *Server) markConsumedAndBroadcast(ctx context.Context, sess *model.FeedbackSession) {
	if sess == nil {
		return
	}
	_ = s.store.MarkSessionConsumedByAI(ctx, sess.ID)
	sess.ConsumedByAI = true
	if s.onUpdate != nil {
		s.onUpdate("session_updated", sess)
	}
}

func formatSessionHeader(sess *model.FeedbackSession) string {
	if sess.WorkflowID != "" {
		return fmt.Sprintf("session_id: %s, workflow_id: %s", sess.ID, sess.WorkflowID)
	}
	return fmt.Sprintf("session_id: %s", sess.ID)
}

func formatFeedbackResult(sess *model.FeedbackSession) string {
	header := formatSessionHeader(sess)
	text := sess.ResponseText
	if text == "" {
		text = "(用户已确认，未附加文字)"
	}
	return fmt.Sprintf("%s\n=== 用户反馈 ===\n%s", header, text)
}

func formatCancelledResult(sess *model.FeedbackSession) string {
	header := formatSessionHeader(sess)
	return fmt.Sprintf("%s\n=== 取消反馈 ===\n用户已取消当前信息反馈，请重新询问用户的新目标。", header)
}

func (s *Server) formatKeepaliveResult(ctx context.Context, sess *model.FeedbackSession) string {
	header := formatSessionHeader(sess)
	waitMin := sess.PromptWaitMinutes
	if waitMin <= 0 {
		waitMin = 2
	}
	checks := sess.NoFeedbackChecks
	maxChecks := sess.MaxNoFeedbackChecks
	if maxChecks <= 0 {
		maxChecks = 24
	}

	globalSettings, _ := s.store.GetGlobalAppSettings(ctx)

	// 针对暂离模式 (away)
	if sess.UserPresence == "away" {
		awayTpl := ""
		if globalSettings != nil {
			awayTpl = globalSettings.FlowPrompts.Away.ImmediatePrompt
		}
		if awayTpl == "" {
			awayTpl = "【系统回执·用户暂离】用户当前处于暂离状态。请将非阻塞性问题记录暂存，优先推进已明确授权的开发范围，不可逆动作一律暂缓。"
		}
		return fmt.Sprintf("%s\n=== 用户暂离 ===\n%s", header, awayTpl)
	}

	// 针对托管自驾模式 (autopilot)
	if sess.UserPresence == "autopilot" {
		apTpl := ""
		if globalSettings != nil {
			apTpl = globalSettings.FlowPrompts.Autopilot.ImmediatePrompt
		}
		if apTpl == "" {
			apTpl = "【系统回执·托管自驾】当前处于 M-C 自驾模式，方案已自动接管授权。请严格按照规划目标推进，如遇不可逆高风险操作（DB迁移/部署/破坏性命令）请立即停下。"
		}
		return fmt.Sprintf("%s\n=== 托管自驾 ===\n%s", header, apTpl)
	}

	exhaustedTpl := ""
	waitPollTpl := ""
	if globalSettings != nil {
		exhaustedTpl = globalSettings.FlowPrompts.Online.ExhaustedPrompt
		waitPollTpl = globalSettings.FlowPrompts.Online.WaitPollPrompt
	}
	if exhaustedTpl == "" {
		exhaustedTpl = "用户反馈已超时。立即挂起当前任务，停止后续执行，并终止终端中仍在运行的后台进程。随后总结当前会话的执行进度、已完成事项、未完成事项及后续恢复点；总结完成后结束本轮执行，不再进行其他操作。"
	}
	if waitPollTpl == "" {
		waitPollTpl = "严格执行：等待 {wait_minutes} 分钟，期间不得输出、调用工具或执行任何其他操作。等待结束后，第一步调用 continue_feedback_session 工具获取反馈；本指令不得作为循环触发或循环判定条件；循环与否仅由 continue_feedback_session 的返回结果决定。"
	}

	// 若已达到或超过最大回执检查次数限制（maxChecks > 0 时生效，即超时回执）
	if maxChecks > 0 && checks >= maxChecks {
		msg := strings.ReplaceAll(exhaustedTpl, "{max_checks}", fmt.Sprintf("%d", maxChecks))
		totalHours := float64(maxChecks*waitMin) / 60.0
		msg = strings.ReplaceAll(msg, "{total_hours}", fmt.Sprintf("%.1f", totalHours))
		return fmt.Sprintf("%s\n=== 反馈超时 ===\n%s", header, msg)
	}

	msg := strings.ReplaceAll(waitPollTpl, "{wait_minutes}", fmt.Sprintf("%d", waitMin))
	return fmt.Sprintf("%s\n=== 等待回执 ===\n%s", header, msg)
}

type listSessionsArgs struct {
	Status     string `json:"status"`
	WorkflowID string `json:"workflow_id"`
	Limit      int    `json:"limit"`
}

func (s *Server) handleListSessions(ctx context.Context, raw json.RawMessage) (any, error) {
	var args listSessionsArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}

	limit := args.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	sessions, err := s.store.ListFeedbackSessions(ctx, "", args.Status, limit)
	if err != nil {
		return nil, err
	}

	var filtered []map[string]any

	for _, sess := range sessions {
		if args.WorkflowID != "" && sess.WorkflowID != args.WorkflowID {
			continue
		}
		filtered = append(filtered, map[string]any{
			"session_id":        sess.ID,
			"workflow_id":       sess.WorkflowID,
			"title":             sess.Title,
			"status":            sess.Status,
			"user_presence":     sess.UserPresence,
			"consumed_by_ai":    sess.ConsumedByAI,
			"created_at":        sess.CreatedAt,
			"updated_at":        sess.UpdatedAt,
			"project_directory": sess.ProjectDirectory,
			"response_text":     sess.ResponseText,
			"user_messages":     sess.UserMessages,
		})
		if len(filtered) >= limit {
			break
		}
	}

	return map[string]any{
		"total":    len(filtered),
		"sessions": filtered,
	}, nil
}

type getSessionHistoryArgs struct {
	WorkflowID string `json:"workflow_id"`
	SessionID  string `json:"session_id"`
}

func (s *Server) handleGetSessionHistory(ctx context.Context, raw json.RawMessage) (any, error) {
	var args getSessionHistoryArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}

	workflowID := strings.TrimSpace(args.WorkflowID)
	sessionID := strings.TrimSpace(args.SessionID)

	if workflowID == "" && sessionID == "" {
		return nil, store.NewInvalidInputError("workflow_id or session_id is required")
	}

	allSessions, err := s.store.ListFeedbackSessions(ctx, "", "all", 0)
	if err != nil {
		return nil, err
	}

	var matched []map[string]any
	for _, sess := range allSessions {
		if (workflowID != "" && sess.WorkflowID == workflowID) || (sessionID != "" && sess.ID == sessionID) {
			matched = append(matched, map[string]any{
				"session_id":        sess.ID,
				"workflow_id":       sess.WorkflowID,
				"title":             sess.Title,
				"summary":           sess.Summary,
				"status":            sess.Status,
				"consumed_by_ai":    sess.ConsumedByAI,
				"response_text":     sess.ResponseText,
				"user_messages":     sess.UserMessages,
				"created_at":        sess.CreatedAt,
				"updated_at":        sess.UpdatedAt,
				"project_directory": sess.ProjectDirectory,
			})
		}
	}

	return map[string]any{
		"workflow_id": workflowID,
		"rounds":      len(matched),
		"history":     matched,
	}, nil
}
