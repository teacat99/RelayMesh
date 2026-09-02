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
	Phase            string `json:"phase,omitempty"`
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

	credCtx := CredentialFromContext(ctx)
	credHostName := ""
	if credCtx != nil {
		credHostName = credCtx.HostName
	}

	session, err := s.store.CreateFeedbackSession(ctx, store.CreateSessionInput{
		WorkflowID:         args.WorkflowID,
		EnvHostName:        s.cfg.HostName,
		CredentialHostName: credHostName,
		ProjectDirectory:   args.ProjectDirectory,
		Title:              args.Title,
		Summary:            args.Summary,
		TimeoutSeconds:     timeoutSec,
	})
	if err != nil {
		return nil, err
	}

	if args.Phase != "" && session.WorkflowID != "" {
		if e := s.store.SetWorkflowPhase(ctx, session.WorkflowID, args.Phase, false); e == nil {
			if s.onUpdate != nil {
				s.onUpdate("phase_changed", map[string]string{
					"workflow_id": session.WorkflowID,
					"phase":       args.Phase,
				})
			}
		}
	}

	// 只要 session 状态已经为 completed（例如命中了提前暂存的 QueuedFeedback 秒回），直接格式化返回，无需进入挂起等待
	if session.Status == "completed" {
		s.markConsumedAndBroadcast(ctx, session)
		return s.formatFeedbackResultWithCtx(ctx, session), nil
	}

	// 获取业务层动态配置的真实挂起等待时长
	waitSec := s.cfg.FeedbackTimeoutSeconds
	if session.WaitCountdownMinutes > 0 {
		waitSec = session.WaitCountdownMinutes * 60
	} else if session.TimeoutSeconds > 0 {
		waitSec = session.TimeoutSeconds
	}
	if waitSec <= 0 {
		waitSec = 120
	}

	s.notifySessionUpdate(session.ID)

	// Wait for user feedback or timeout
	return s.waitForFeedback(ctx, session.ID, waitSec)
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
		if sess.ConsumedByAI {
			// 若该会话已经由 AI 提取消费过，检查该工作流是否存在新的 pending 活跃轮次
			wID := sess.WorkflowID
			if wID == "" {
				wID = strings.TrimSpace(args.WorkflowID)
			}
			if wID != "" {
				latestSess, err := s.store.GetLatestWorkflowFeedbackSession(ctx, wID)
				if err == nil && latestSess != nil && latestSess.ID != sess.ID && latestSess.Status == "pending" {
					// 存在更新的活跃 pending 会话，切换至最新会话继续挂起等待
					sess = latestSess
					targetSessionID = latestSess.ID
				} else {
					return nil, store.NewConflictError(fmt.Sprintf("session %q has already been completed and consumed by AI. No pending feedback in workflow %q. To start a new feedback turn, please use interactive_feedback.", sess.ID, wID), 0)
				}
			} else {
				return nil, store.NewConflictError(fmt.Sprintf("session %q has already been completed and consumed by AI. To start a new feedback turn, please use interactive_feedback.", sess.ID), 0)
			}
		} else {
			s.markConsumedAndBroadcast(ctx, sess)
			return s.formatFeedbackResultWithCtx(ctx, sess), nil
		}
	}
	if sess.Status == "cancelled" {
		return s.formatCancelledResultWithCtx(ctx, sess), nil
	}

	// 获取业务层动态配置的真实挂起等待时长
	waitSec := s.cfg.FeedbackTimeoutSeconds
	if sess.WaitCountdownMinutes > 0 {
		waitSec = sess.WaitCountdownMinutes * 60
	} else if sess.TimeoutSeconds > 0 {
		waitSec = sess.TimeoutSeconds
	}
	if waitSec <= 0 {
		waitSec = 120
	}

	session, err := s.store.KeepaliveFeedbackSession(ctx, targetSessionID, waitSec)
	if err != nil {
		return nil, err
	}

	s.notifySessionUpdate(session.ID)
	return s.waitForFeedback(ctx, session.ID, waitSec)
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
	// 服务端挂起等待时长由业务层（WaitCountdownMinutes / TimeoutSeconds）动态配置严格驱动
	effectiveWaitSec := timeoutSec
	if effectiveWaitSec <= 0 {
		effectiveWaitSec = 120
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
				return s.formatFeedbackResultWithCtx(ctx, sess), nil
			}
			s.RecordKeepaliveResponse(sessionID)
			s.notifySessionUpdate(sessionID)
			return s.formatKeepaliveResult(ctx, sess), nil

		case updatedSess := <-waitCh:
			if updatedSess != nil && updatedSess.Status == "completed" {
				s.markConsumedAndBroadcast(ctx, updatedSess)
				return s.formatFeedbackResultWithCtx(ctx, updatedSess), nil
			}
			if updatedSess != nil && updatedSess.Status == "cancelled" {
				return s.formatCancelledResultWithCtx(ctx, updatedSess), nil
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

func (s *Server) formatSessionHeaderWithContext(ctx context.Context, sess *model.FeedbackSession) string {
	header := formatSessionHeader(sess)
	globalSettings, _ := s.store.GetGlobalAppSettings(ctx)
	if globalSettings != nil && strings.TrimSpace(globalSettings.UserMemory) != "" {
		header += "\ncontext: " + strings.TrimSpace(globalSettings.UserMemory)
	}

	if sess.WorkflowID != "" {
		currentPhase, phaseItems, _ := s.store.GetWorkflowPhaseWithDefaults(ctx, sess.WorkflowID)
		store.BackfillPhasePrompts(phaseItems)
		if currentPhase != "" {
			header += "\ncurrent_phase: " + currentPhase
			for _, p := range phaseItems {
				if p.ID == currentPhase && strings.TrimSpace(p.Prompt) != "" {
					header += "\nphase_prompt: " + strings.TrimSpace(p.Prompt)
					break
				}
			}
		}

		if cp, _ := s.store.GetLatestCheckpoint(ctx, sess.WorkflowID); cp != nil {
			var content model.CheckpointContent
			if json.Unmarshal([]byte(cp.ContentJSON), &content) == nil {
				cpHint := fmt.Sprintf("rev=%d", cp.Revision)
				if content.Objective != "" {
					obj := content.Objective
					if len([]rune(obj)) > 80 {
						obj = string([]rune(obj)[:80]) + "..."
					}
					cpHint += " obj=" + obj
				}
				if content.CurrentStage != nil {
					cpHint += " stage=" + content.CurrentStage.ID + "/" + content.CurrentStage.Status
				}
				if len(content.NextActions) > 0 {
					next := content.NextActions[0]
					if len([]rune(next)) > 60 {
						next = string([]rune(next)[:60]) + "..."
					}
					cpHint += " next=" + next
				}
				header += "\ncheckpoint: " + cpHint
			}
		}
	}

	activeNorms, _ := s.store.ListActiveUserNorms(ctx)
	if len(activeNorms) > 0 {
		const maxInjectedLen = 2000
		var summaries []string
		totalLen := 0
		for _, n := range activeNorms {
			entry := n.Name + ": " + n.Summary
			if totalLen+len(entry) > maxInjectedLen {
				summaries = append(summaries, "... (truncated, use manage_skills to view all)")
				break
			}
			summaries = append(summaries, entry)
			totalLen += len(entry)
		}
		header += "\nactive_skills: " + strings.Join(summaries, " | ")
	}

	recentWfs := s.buildRecentWorkflowsHint(ctx, sess)
	if recentWfs != "" {
		header += "\nrecent_workflows: " + recentWfs
	}

	return header
}

func (s *Server) buildRecentWorkflowsHint(ctx context.Context, currentSess *model.FeedbackSession) string {
	allSessions, err := s.store.ListFeedbackSessions(ctx, "", "", 200)
	if err != nil || len(allSessions) == 0 {
		return ""
	}

	type wfBrief struct {
		ID           string
		Title        string
		LastActive   time.Time
		SessionCount int
		HasPending   bool
	}

	wfMap := make(map[string]*wfBrief)
	var wfOrder []string

	projDir := ""
	if currentSess != nil {
		projDir = currentSess.ProjectDirectory
	}

	for _, sess := range allSessions {
		if sess.Status == "archived" {
			continue
		}
		if projDir != "" && projDir != "." && sess.ProjectDirectory != projDir {
			continue
		}
		key := sess.WorkflowID
		if key == "" {
			continue
		}
		wf, exists := wfMap[key]
		if !exists {
			wf = &wfBrief{ID: key, Title: sess.Title, LastActive: sess.UpdatedAt}
			wfMap[key] = wf
			wfOrder = append(wfOrder, key)
		}
		wf.SessionCount++
		if sess.UpdatedAt.After(wf.LastActive) {
			wf.LastActive = sess.UpdatedAt
		}
		if sess.Status == "pending" {
			wf.HasPending = true
		}
		if wf.Title == "" && sess.Title != "" {
			wf.Title = sess.Title
		}
	}

	if len(wfOrder) == 0 {
		return ""
	}

	const maxWorkflows = 5
	var parts []string
	for i, key := range wfOrder {
		if i >= maxWorkflows {
			break
		}
		wf := wfMap[key]
		status := "idle"
		if wf.HasPending {
			status = "active"
		}
		part := fmt.Sprintf("%s(%s, %d轮, %s)",
			wf.ID, status, wf.SessionCount,
			wf.LastActive.Format("15:04"))
		parts = append(parts, part)
	}
	return strings.Join(parts, " | ")
}

func (s *Server) formatFeedbackResultWithCtx(ctx context.Context, sess *model.FeedbackSession) string {
	header := s.formatSessionHeaderWithContext(ctx, sess)
	text := sess.ResponseText
	if text == "" {
		text = "(用户已确认，未附加文字)"
	}
	return fmt.Sprintf("%s\n=== 用户反馈 ===\n%s", header, text)
}

func (s *Server) formatCancelledResultWithCtx(ctx context.Context, sess *model.FeedbackSession) string {
	header := s.formatSessionHeaderWithContext(ctx, sess)
	return fmt.Sprintf("%s\n=== 取消反馈 ===\n用户已取消当前信息反馈，请重新询问用户的新目标。", header)
}

func (s *Server) formatKeepaliveResult(ctx context.Context, sess *model.FeedbackSession) string {
	header := s.formatSessionHeaderWithContext(ctx, sess)
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

	expandCommonVars := func(tpl string) string {
		r := strings.NewReplacer(
			"{wait_minutes}", fmt.Sprintf("%d", waitMin),
			"{wait_ms}", fmt.Sprintf("%d", waitMin*60000),
			"{session_id}", sess.ID,
			"{workflow_id}", sess.WorkflowID,
		)
		return r.Replace(tpl)
	}

	if sess.UserPresence == "away" {
		awayTpl := ""
		if globalSettings != nil {
			awayTpl = globalSettings.FlowPrompts.Away.ImmediatePrompt
		}
		if awayTpl == "" {
			awayTpl = "【系统回执·人工暂离】用户已确认当前推进目标并主动暂离，请继续执行已授权范围内的工作。\n行为约束：\n- 按会话文档「当前任务」和「关键决策」已锁定的方向继续推进\n- 遇到非阻塞性问题记入会话文档「待用户拍板」，不阻塞进度\n- 不可逆动作：已授权的按计划执行，未授权的记录待确认并暂缓\n- 每完成一个逻辑单元执行增量验证（lint/type-check→build）\n- 阶段完成或遇到阻塞时，通过 interactive_feedback 提交阶段简报\n- 用户回来后按会话文档记录对齐进度"
		}
		return fmt.Sprintf("%s\n=== 用户暂离 ===\n%s", header, expandCommonVars(awayTpl))
	}

	if sess.UserPresence == "autopilot" {
		apTpl := ""
		if globalSettings != nil {
			apTpl = globalSettings.FlowPrompts.Autopilot.ImmediatePrompt
		}
		if apTpl == "" {
			apTpl = "【系统回执·外部编排】当前处于 autopilot 外部编排模式，由外部系统通过 Task API 驱动。\n行为约束：\n- 通过 report_progress 汇报进度和检查反馈\n- 按 task segments 定义的范围执行，不越界\n- 不通过 interactive_feedback 向用户直接提问\n- 遇不可逆动作以 question 类型上报并等待\n- 遇 MCP 通信错误降级为 away 模式"
		}
		return fmt.Sprintf("%s\n=== 托管自驾 ===\n%s", header, expandCommonVars(apTpl))
	}

	exhaustedTpl := ""
	waitPollTpl := ""
	if globalSettings != nil {
		exhaustedTpl = globalSettings.FlowPrompts.Online.ExhaustedPrompt
		waitPollTpl = globalSettings.FlowPrompts.Online.WaitPollPrompt
	}
	if exhaustedTpl == "" {
		exhaustedTpl = "用户反馈已超时。立即挂起当前任务并停止后续执行，进入会话结束与环境收尾规程：\n1. 终端与后台任务盘点清理（强制）：\n   - 检查所有活跃终端与后台任务（通过 terminals 目录或 AwaitShell）\n   - 显式终止临时开发服务、构建进程和测试任务，确认进程退出\n   - 确保后台异步任务挂起数归零，防止延迟事件注入\n2. 临时产物与会话状态归档：\n   - 清理 .cursor/tmp/ 临时文件，仅保留必要证据\n   - 完成 git 阶段性提交，确保无文件滞留暂存区\n   - 更新会话文档状态并记录恢复点\n3. 最终汇报：\n   - 总结执行进度、已完成/未完成事项与后续恢复建议\n   - 通过普通 chat 提交最终状态报告，结束本轮执行"
	}
	if waitPollTpl == "" {
		waitPollTpl = "下一步操作：\n1. 调用 AwaitShell({ block_until_ms: {wait_ms} }) 等待 {wait_minutes} 分钟\n2. 等待结束后立即调用 continue_feedback_session({ workflow_id: \"{workflow_id}\" })\n3. 根据返回的 === 标记 === 判断下一步\n\n等待期间禁止：chat 正文输出、其他工具调用、阶段整理。\n禁止将本回执或 Agent 生成的文字当作用户反馈。\n所有沟通均通过 RelayMesh MCP 工具。"
	}

	expandVars := func(tpl string) string {
		r := strings.NewReplacer(
			"{wait_minutes}", fmt.Sprintf("%d", waitMin),
			"{wait_ms}", fmt.Sprintf("%d", waitMin*60000),
			"{session_id}", sess.ID,
			"{workflow_id}", sess.WorkflowID,
			"{max_checks}", fmt.Sprintf("%d", maxChecks),
			"{total_hours}", fmt.Sprintf("%.1f", float64(maxChecks*waitMin)/60.0),
		)
		return r.Replace(tpl)
	}

	if maxChecks > 0 && checks >= maxChecks {
		return fmt.Sprintf("%s\n=== 反馈超时 ===\n%s", header, expandVars(exhaustedTpl))
	}

	return fmt.Sprintf("%s\n=== 等待回执 ===\n%s", header, expandVars(waitPollTpl))
}

type listSessionsArgs struct {
	Status           string `json:"status"`
	WorkflowID       string `json:"workflow_id"`
	ProjectDirectory string `json:"project_directory"`
	HostName         string `json:"host_name"`
	GroupBy          string `json:"group_by"`
	Limit            int    `json:"limit"`
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

	status := args.Status
	if status == "all" {
		status = ""
	}

	sessions, err := s.store.ListFeedbackSessions(ctx, "", status, 0)
	if err != nil {
		return nil, err
	}

	projDir := strings.TrimSpace(args.ProjectDirectory)
	hostName := strings.TrimSpace(args.HostName)
	wfID := strings.TrimSpace(args.WorkflowID)

	var filtered []model.FeedbackSession
	for _, sess := range sessions {
		if wfID != "" && sess.WorkflowID != wfID {
			continue
		}
		if projDir != "" && sess.ProjectDirectory != projDir {
			continue
		}
		if hostName != "" && sess.HostName != hostName {
			continue
		}
		filtered = append(filtered, sess)
	}

	if strings.TrimSpace(args.GroupBy) == "workflow" {
		return s.groupSessionsByWorkflow(filtered, limit), nil
	}

	var result []map[string]any
	for i, sess := range filtered {
		if i >= limit {
			break
		}
		result = append(result, map[string]any{
			"session_id":        sess.ID,
			"workflow_id":       sess.WorkflowID,
			"title":             sess.Title,
			"status":            sess.Status,
			"user_presence":     sess.UserPresence,
			"consumed_by_ai":    sess.ConsumedByAI,
			"created_at":        sess.CreatedAt,
			"updated_at":        sess.UpdatedAt,
			"host_name":         sess.HostName,
			"project_directory": sess.ProjectDirectory,
			"response_text":     sess.ResponseText,
			"user_messages":     sess.UserMessages,
		})
	}

	return map[string]any{
		"total":    len(result),
		"sessions": result,
	}, nil
}

func (s *Server) groupSessionsByWorkflow(sessions []model.FeedbackSession, limit int) map[string]any {
	type wfInfo struct {
		WorkflowID       string    `json:"workflow_id"`
		Title            string    `json:"title"`
		HostName         string    `json:"host_name"`
		ProjectDirectory string    `json:"project_directory"`
		SessionCount     int       `json:"session_count"`
		LastActiveAt     time.Time `json:"last_active_at"`
		LatestStatus     string    `json:"latest_status"`
		HasPending       bool      `json:"has_pending"`
	}

	wfMap := make(map[string]*wfInfo)
	var wfOrder []string

	for _, sess := range sessions {
		key := sess.WorkflowID
		if key == "" {
			key = sess.ID
		}
		wf, exists := wfMap[key]
		if !exists {
			wf = &wfInfo{
				WorkflowID:       key,
				Title:            sess.Title,
				HostName:         sess.HostName,
				ProjectDirectory: sess.ProjectDirectory,
				LatestStatus:     sess.Status,
				LastActiveAt:     sess.UpdatedAt,
			}
			wfMap[key] = wf
			wfOrder = append(wfOrder, key)
		}
		wf.SessionCount++
		if sess.UpdatedAt.After(wf.LastActiveAt) {
			wf.LastActiveAt = sess.UpdatedAt
			wf.LatestStatus = sess.Status
		}
		if sess.Status == "pending" {
			wf.HasPending = true
		}
		if wf.Title == "" && sess.Title != "" {
			wf.Title = sess.Title
		}
	}

	var workflows []map[string]any
	for _, key := range wfOrder {
		if len(workflows) >= limit {
			break
		}
		wf := wfMap[key]
		workflows = append(workflows, map[string]any{
			"workflow_id":       wf.WorkflowID,
			"title":             wf.Title,
			"host_name":         wf.HostName,
			"project_directory": wf.ProjectDirectory,
			"session_count":     wf.SessionCount,
			"last_active_at":    wf.LastActiveAt,
			"latest_status":     wf.LatestStatus,
			"has_pending":       wf.HasPending,
		})
	}

	return map[string]any{
		"total":     len(workflows),
		"workflows": workflows,
	}
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
