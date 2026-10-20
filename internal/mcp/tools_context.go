package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/asyncmode"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

type workflowContextArgs struct {
	Action                string                   `json:"action"`
	WorkflowID            string                   `json:"workflow_id,omitempty"`
	Checkpoint            *model.CheckpointContent `json:"checkpoint,omitempty"`
	Revision              int                      `json:"revision,omitempty"`
	SessionID             string                   `json:"session_id,omitempty"`
	NoteKey               string                   `json:"note_key,omitempty"`
	NoteID                uint                     `json:"note_id,omitempty"`
	Content               string                   `json:"content,omitempty"`
	Limit                 int                      `json:"limit,omitempty"`
	ReplayAfterCheckpoint bool                     `json:"replay_after_checkpoint,omitempty"`
	MaxBytes              int                      `json:"max_bytes,omitempty"`
	PhaseID               string                   `json:"phase_id,omitempty"`
	ProjectDirectory      string                   `json:"project_directory,omitempty"`
	HostName              string                   `json:"host_name,omitempty"`

	// task_* 动作（独立于会话的任务清单）。指针字段区分「没传」与「传了空值」：progress 传空串表示清空，labels 传 [] 表示清空。
	TaskID        int       `json:"task_id,omitempty"` // 工作流内的任务序号（#12 → 12）
	Title         string    `json:"title,omitempty"`
	Kind          string    `json:"kind,omitempty"`
	State         string    `json:"state,omitempty"`
	States        []string  `json:"states,omitempty"`
	Labels        *[]string `json:"labels,omitempty"`
	Refs          *[]string `json:"refs,omitempty"`
	Description   *string   `json:"description,omitempty"` // 任务内容说明（这件事是什么、做到什么算完成）；task_update 传 "" 清除
	Progress      *string   `json:"progress,omitempty"`
	Conclusion    *string   `json:"conclusion,omitempty"`
	Note          string    `json:"note,omitempty"`
	ParentTaskID  *int      `json:"parent_task_id,omitempty"` // 0 = 解除父任务
	Query         string    `json:"query,omitempty"`
	IncludeClosed bool      `json:"include_closed,omitempty"`
	Module        *string   `json:"module,omitempty"`  // 模块路径（须是已存在的，如 终端/历史日志）；task_update 传 "" 取消归类
	Percent       *looseInt `json:"percent,omitempty"` // 完成度 0–100；-1 清除（回到由状态推导）
	Stage         *string   `json:"stage,omitempty"`   // 阶段 assess / plan / dev / improve；task_update 传 "" 清除（回到未标注）
	// 共享任务板的协作（#175 B2）：assignee 在 task_create / task_update 里是指派（"" 撤销），在 task_list 里是过滤（me / none / 工作流编号）；
	// claim 是 task_update 的显式协作动作（accept / decline / deliver / accept_delivery / reject_delivery）。
	Assignee *string `json:"assignee,omitempty"`
	Claim    string  `json:"claim,omitempty"`

	// inbox / inbox_peek / inbox_ack 动作（追加指令收件箱，见 inbox.go）。
	IDs      looseIDList `json:"ids,omitempty"`      // inbox_ack：只确认这几条；不传就确认全部已读取的
	Reminder bool        `json:"reminder,omitempty"` // inbox_peek：这是 hook 在问「该不该提醒」，要按节奏记提醒次数
	Recent   bool        `json:"recent,omitempty"`   // inbox：同时列出最近已确认的几条
	Source   string      `json:"source,omitempty"`   // inbox_peek：调用方自报来源——hook（编辑器 hook 的轮询）/ check（安装检查）；智能体自己调用时不用填
	// inbox_peek（source=hook）：hook 所在的 Cursor 对话编号。对话被 Hub 发起的运行认领时，提醒要查认领它的工作流的收件箱（#182）。
	ConversationID string `json:"conversation_id,omitempty"`

	// wake_permit：唤醒许可（0 = 标记任务全部完成，1 = 取消标记）。
	Permit flexInt `json:"permit,omitempty"`
}

func (s *Server) handleWorkflowContext(ctx context.Context, raw json.RawMessage) (any, error) {
	return s.handleWorkflowContextWith(ctx, nil, raw)
}

// handleWorkflowContextWith 与 handleWorkflowContext 相同，另带凭据上下文（收件箱里的图片要用它拼下载链接）。
func (s *Server) handleWorkflowContextWith(ctx context.Context, credCtx *CredentialContext, raw json.RawMessage) (any, error) {
	var args workflowContextArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	switch args.Action {
	case "checkpoint_save":
		return s.handleCheckpointSave(ctx, args)
	case "checkpoint_get":
		return s.handleCheckpointGet(ctx, args)
	case "checkpoint_list":
		return s.handleCheckpointList(ctx, args)
	case "checkpoint_verify":
		return s.handleCheckpointVerify(ctx, args)
	case "note_save":
		return s.handleNoteSave(ctx, args)
	case "note_get":
		return s.handleNoteGet(ctx, args)
	case "note_list":
		return s.handleNoteList(ctx, args)
	case "note_update":
		return s.handleNoteUpdate(ctx, args)
	case "note_delete":
		return s.handleNoteDelete(ctx, args)
	case "session_doc_save":
		if args.WorkflowID == "" {
			return nil, fmt.Errorf("workflow_id is required for session_doc_save")
		}
		if args.Content == "" {
			return nil, fmt.Errorf("content is required for session_doc_save")
		}
		args.NoteKey = "session_doc"
		res, err := s.handleNoteSave(ctx, args)
		if err == nil {
			_ = s.store.MarkWorkflowHistorySessionsConsumed(ctx, args.WorkflowID)
			if s.onUpdate != nil {
				s.onUpdate("workflow_sheet_updated", map[string]any{"workflow_id": args.WorkflowID})
			}
		}
		return res, err
	case "session_doc_get":
		if args.WorkflowID == "" {
			return nil, fmt.Errorf("workflow_id is required for session_doc_get")
		}
		args.NoteKey = "session_doc"
		return s.handleNoteGet(ctx, args)
	case "list_workflows":
		return s.handleListWorkflows(ctx, args)
	case "set_phase":
		return s.handleSetPhase(ctx, args)
	case "task_create":
		return s.handleTaskCreate(ctx, args)
	case "task_update":
		return s.handleTaskUpdate(ctx, args)
	case "task_get":
		return s.handleTaskGet(ctx, args)
	case "task_list":
		return s.handleTaskList(ctx, args)
	case "inbox":
		return s.handleInbox(ctx, credCtx, args)
	case "inbox_peek":
		return s.handleInboxPeek(ctx, args)
	case "inbox_ack":
		return s.handleInboxAck(ctx, args)
	case "wake_permit":
		return s.handleWakePermit(ctx, args)
	default:
		return nil, fmt.Errorf("unknown action: %s", args.Action)
	}
}

func (s *Server) handleCheckpointSave(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required for checkpoint_save")
	}
	if args.Checkpoint == nil {
		return nil, fmt.Errorf("checkpoint content is required for checkpoint_save")
	}

	// 恢复胶囊与交付锚点联动：缺省基准取本工作流最近一次带锚点的交付，并记下它所属的会话。
	basisDefaulted := s.linkCheckpointToAnchor(ctx, args.WorkflowID, args.Checkpoint)

	contentJSON, err := json.Marshal(args.Checkpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal checkpoint content: %w", err)
	}

	cp := &model.WorkflowCheckpoint{
		WorkflowID:  args.WorkflowID,
		Status:      "submitted",
		ContentJSON: string(contentJSON),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.store.SaveCheckpoint(ctx, cp); err != nil {
		return nil, fmt.Errorf("failed to save checkpoint: %w", err)
	}

	_ = s.store.MarkWorkflowHistorySessionsConsumed(ctx, args.WorkflowID)

	if err := s.store.SupersedeOldCheckpoints(ctx, args.WorkflowID, cp.Revision); err != nil {
		// non-fatal
	}

	if s.onUpdate != nil {
		s.onUpdate("checkpoint_saved", map[string]any{"workflow_id": args.WorkflowID, "revision": cp.Revision})
	}

	result := map[string]any{
		"status":      "saved",
		"workflow_id": args.WorkflowID,
		"revision":    cp.Revision,
		"created_at":  cp.CreatedAt,
		"basis":       args.Checkpoint.Basis,
	}
	if basisDefaulted {
		result["basis_defaulted"] = true
	}
	return result, nil
}

// linkCheckpointToAnchor 把胶囊的基准与交付锚点关联起来，返回是否使用了缺省基准。
//
//   - 没有显式给 basis.git_commit：取本工作流最近一次交付锚点的哈希作缺省基准，
//     并把那次交付的会话 ID 记进 basis.anchor_session_id；工作流里没有任何锚点则保持原样。
//   - 显式给了哈希：尽力在本工作流的交付里找到同一提交，找到就记下 anchor_session_id。
//
// 任何查询失败都静默跳过——胶囊保存本身永远不能因为锚点联动而失败。
func (s *Server) linkCheckpointToAnchor(ctx context.Context, workflowID string, content *model.CheckpointContent) bool {
	if content == nil {
		return false
	}
	basis := &content.Basis
	if strings.TrimSpace(basis.GitCommit) == "" {
		anchored, err := s.store.GetLatestAnchoredSession(ctx, workflowID, "")
		if err != nil || anchored == nil {
			return false
		}
		basis.GitCommit = anchored.GitCommit
		basis.AnchorSessionID = anchored.ID
		return true
	}
	if basis.AnchorSessionID == "" {
		if anchored, err := s.store.FindAnchoredSessionByCommit(ctx, workflowID, basis.GitCommit); err == nil && anchored != nil {
			basis.AnchorSessionID = anchored.ID
		}
	}
	return false
}

func (s *Server) handleCheckpointGet(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required for checkpoint_get")
	}

	var cp *model.WorkflowCheckpoint
	var err error

	if args.Revision > 0 {
		cp, err = s.store.GetCheckpointByRevision(ctx, args.WorkflowID, args.Revision)
	} else {
		cp, err = s.store.GetLatestCheckpoint(ctx, args.WorkflowID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint: %w", err)
	}
	if cp == nil {
		return map[string]any{
			"workflow_id": args.WorkflowID,
			"checkpoint":  nil,
			"message":     "no checkpoint found for this workflow",
		}, nil
	}

	var content model.CheckpointContent
	if err := json.Unmarshal([]byte(cp.ContentJSON), &content); err != nil {
		return nil, fmt.Errorf("failed to parse checkpoint content: %w", err)
	}

	result := map[string]any{
		"workflow_id": args.WorkflowID,
		"revision":    cp.Revision,
		"status":      cp.Status,
		"checkpoint":  content,
		"created_at":  cp.CreatedAt,
	}

	if args.ReplayAfterCheckpoint {
		deltas, _ := s.collectDeltasAfterCheckpoint(ctx, args.WorkflowID, content.Basis)
		if deltas != nil {
			result["deltas_after_checkpoint"] = deltas
		}
	}

	return result, nil
}

func (s *Server) handleCheckpointList(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required for checkpoint_list")
	}

	cps, err := s.store.ListCheckpoints(ctx, args.WorkflowID, args.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list checkpoints: %w", err)
	}

	items := make([]map[string]any, 0, len(cps))
	for _, cp := range cps {
		var content model.CheckpointContent
		json.Unmarshal([]byte(cp.ContentJSON), &content)

		items = append(items, map[string]any{
			"revision":   cp.Revision,
			"status":     cp.Status,
			"objective":  content.Objective,
			"stage":      content.CurrentStage,
			"created_at": cp.CreatedAt,
		})
	}
	return map[string]any{
		"workflow_id": args.WorkflowID,
		"checkpoints": items,
		"count":       len(items),
	}, nil
}

func (s *Server) handleCheckpointVerify(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required for checkpoint_verify")
	}

	cp, err := s.store.GetLatestCheckpoint(ctx, args.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint: %w", err)
	}
	if cp == nil {
		return map[string]any{
			"workflow_id": args.WorkflowID,
			"freshness":   map[string]any{"status": "missing", "reasons": []string{"no checkpoint exists"}},
		}, nil
	}

	var content model.CheckpointContent
	if err := json.Unmarshal([]byte(cp.ContentJSON), &content); err != nil {
		return nil, fmt.Errorf("failed to parse checkpoint: %w", err)
	}

	reasons := s.checkFreshness(ctx, args.WorkflowID, content.Basis, cp)
	status := "fresh"
	if len(reasons) > 0 {
		status = "stale"
	}

	result := map[string]any{
		"workflow_id": args.WorkflowID,
		"revision":    cp.Revision,
		"freshness": map[string]any{
			"status":  status,
			"reasons": reasons,
		},
		"checkpoint_age": time.Since(cp.CreatedAt).Round(time.Second).String(),
	}

	if status == "stale" {
		result["required_next_action"] = "Save a new checkpoint with updated basis anchors"
	}

	return result, nil
}

func (s *Server) handleNoteSave(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required for note_save")
	}
	if args.Content == "" {
		return nil, fmt.Errorf("content is required for note_save")
	}

	note := &model.WorkflowNote{
		WorkflowID: args.WorkflowID,
		SessionID:  args.SessionID,
		NoteKey:    args.NoteKey,
		Content:    args.Content,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := s.store.SaveNote(ctx, note); err != nil {
		return nil, fmt.Errorf("failed to save note: %w", err)
	}

	if args.NoteKey != "" {
		existing, _ := s.store.GetNoteByKey(ctx, args.WorkflowID, args.NoteKey)
		if existing != nil {
			note = existing
		}
	}

	return map[string]any{
		"status":      "saved",
		"note_id":     note.ID,
		"workflow_id": args.WorkflowID,
		"note_key":    note.NoteKey,
		"updated_at":  note.UpdatedAt,
	}, nil
}

func (s *Server) handleNoteGet(ctx context.Context, args workflowContextArgs) (any, error) {
	var note *model.WorkflowNote
	var err error

	if args.NoteID > 0 {
		note, err = s.store.GetNote(ctx, args.NoteID)
	} else if args.WorkflowID != "" && args.NoteKey != "" {
		note, err = s.store.GetNoteByKey(ctx, args.WorkflowID, args.NoteKey)
	} else {
		return nil, fmt.Errorf("provide note_id, or workflow_id + note_key")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get note: %w", err)
	}
	if note == nil {
		return map[string]any{"note": nil, "message": "note not found"}, nil
	}
	return map[string]any{"note": note}, nil
}

func (s *Server) handleNoteList(ctx context.Context, args workflowContextArgs) (any, error) {
	notes, err := s.store.ListNotes(ctx, args.WorkflowID, args.SessionID, args.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list notes: %w", err)
	}
	return map[string]any{
		"notes": notes,
		"count": len(notes),
	}, nil
}

func (s *Server) handleNoteUpdate(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.NoteID == 0 {
		return nil, fmt.Errorf("note_id is required for note_update")
	}
	if args.Content == "" {
		return nil, fmt.Errorf("content is required for note_update")
	}
	if err := s.store.UpdateNote(ctx, args.NoteID, args.Content); err != nil {
		return nil, fmt.Errorf("failed to update note: %w", err)
	}
	return map[string]any{"status": "updated", "note_id": args.NoteID}, nil
}

func (s *Server) handleNoteDelete(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.NoteID == 0 {
		return nil, fmt.Errorf("note_id is required for note_delete")
	}
	if err := s.store.DeleteNote(ctx, args.NoteID); err != nil {
		return nil, fmt.Errorf("failed to delete note: %w", err)
	}
	return map[string]any{"status": "deleted", "note_id": args.NoteID}, nil
}

func (s *Server) handleListWorkflows(ctx context.Context, args workflowContextArgs) (any, error) {
	projDir := strings.TrimSpace(args.ProjectDirectory)
	hostName := strings.TrimSpace(args.HostName)

	credCtx := CredentialFromContext(ctx)
	if hostName == "" && credCtx != nil && credCtx.HostName != "" {
		hostName = credCtx.HostName
	}

	summaries, err := s.store.ListWorkflowSummaries(ctx, projDir, hostName, args.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflows: %w", err)
	}
	return map[string]any{
		"workflows": summaries,
		"count":     len(summaries),
	}, nil
}

func (s *Server) handleSetPhase(ctx context.Context, args workflowContextArgs) (any, error) {
	if args.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required for set_phase")
	}
	if args.PhaseID == "" {
		return nil, fmt.Errorf("phase_id is required for set_phase")
	}

	// 旧五段式的阶段 id（assess / verify / done）仍被接受，落库时换成对应的新 id（讨论 / 开发 / 开发）。
	requested := strings.TrimSpace(args.PhaseID)
	canonical := model.CanonicalPhaseID(requested)

	if err := s.store.SetWorkflowPhase(ctx, args.WorkflowID, canonical, false); err != nil {
		return nil, fmt.Errorf("failed to set phase: %w", err)
	}

	_ = s.store.MarkWorkflowHistorySessionsConsumed(ctx, args.WorkflowID)

	if s.onUpdate != nil {
		s.onUpdate("phase_changed", map[string]string{
			"workflow_id": args.WorkflowID,
			"phase":       canonical,
		})
	}

	currentPhase, phaseItems, _ := s.store.GetWorkflowPhaseWithDefaults(ctx, args.WorkflowID)
	store.BackfillPhasePrompts(phaseItems)
	var currentPrompt string
	available := make([]string, 0, len(phaseItems))
	known := false
	for _, p := range phaseItems {
		available = append(available, p.ID)
		if p.ID == currentPhase {
			currentPrompt = p.Prompt
			known = true
		}
	}

	result := map[string]any{
		"status":           "updated",
		"workflow_id":      args.WorkflowID,
		"current_phase":    currentPhase,
		"phase_prompt":     currentPrompt,
		"available_phases": available,
	}
	if canonical != requested {
		result["phase_alias"] = fmt.Sprintf("旧阶段「%s」已并入「%s」，实际生效的阶段是 %s；之后请直接使用新的阶段 id。", requested, canonical, canonical)
	}
	if !known {
		result["warning"] = fmt.Sprintf("阶段「%s」不在当前工作流的阶段列表里（可用：%s），已按原样记录，但没有对应的阶段提示词。", currentPhase, strings.Join(available, " / "))
	}
	// 异步对话生效时，授权档位取代阶段提示词（与回执头一致）；同步工作流的返回不变（INV-2）。
	if info, mErr := s.store.WorkflowModeInfo(ctx, args.WorkflowID, time.Now()); mErr == nil && info.State.Effective {
		delete(result, "phase_prompt")
		result["mode"] = "async"
		result["auth"] = asyncmode.AuthLine(authProfileOf(info.Profile))
	}
	return result, nil
}

// collectDeltasAfterCheckpoint gathers reports/feedback that arrived after the checkpoint basis
func (s *Server) collectDeltasAfterCheckpoint(ctx context.Context, workflowID string, basis model.CheckpointBasis) ([]map[string]any, error) {
	var deltas []map[string]any

	sessions, err := s.store.GetFeedbackRoundsByWorkflow(ctx, workflowID, 10)
	if err != nil {
		return nil, err
	}
	for _, sess := range sessions {
		if sess.Status == "completed" && sess.ResponseText != "" {
			deltas = append(deltas, map[string]any{
				"type":       "feedback",
				"session_id": sess.ID,
				"response":   truncateString(sess.ResponseText, 500),
				"at":         sess.UpdatedAt,
			})
		}
	}

	return deltas, nil
}

func (s *Server) checkFreshness(ctx context.Context, workflowID string, basis model.CheckpointBasis, cp *model.WorkflowCheckpoint) []string {
	var reasons []string

	age := time.Since(cp.CreatedAt)
	if age > 2*time.Hour {
		reasons = append(reasons, fmt.Sprintf("checkpoint is %s old", age.Round(time.Minute)))
	}

	sessions, _ := s.store.GetFeedbackRoundsByWorkflow(ctx, workflowID, 5)
	newSessions := 0
	for _, sess := range sessions {
		if sess.CreatedAt.After(cp.CreatedAt) {
			newSessions++
		}
	}
	if newSessions > 0 {
		reasons = append(reasons, fmt.Sprintf("%d new session(s) since checkpoint", newSessions))
	}

	// 交付锚点对比：胶囊基准的提交与本工作流最近一次交付锚点不一致，说明胶囊保存之后仓库又有了新提交。
	if strings.TrimSpace(basis.GitCommit) != "" {
		if latest, err := s.store.GetLatestAnchoredSession(ctx, workflowID, ""); err == nil && latest != nil &&
			!sameCommit(latest.GitCommit, basis.GitCommit) {
			reasons = append(reasons, fmt.Sprintf("delivery anchor moved: %s -> %s (new commits since the checkpoint basis)", basis.GitCommit, latest.GitCommit))
		}
	}

	return reasons
}

func truncateString(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
