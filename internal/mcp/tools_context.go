package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
)

type workflowContextArgs struct {
	Action               string                  `json:"action"`
	WorkflowID           string                  `json:"workflow_id,omitempty"`
	Checkpoint           *model.CheckpointContent `json:"checkpoint,omitempty"`
	Revision             int                     `json:"revision,omitempty"`
	SessionID            string                  `json:"session_id,omitempty"`
	NoteKey              string                  `json:"note_key,omitempty"`
	NoteID               uint                    `json:"note_id,omitempty"`
	Content              string                  `json:"content,omitempty"`
	Limit                int                     `json:"limit,omitempty"`
	ReplayAfterCheckpoint bool                   `json:"replay_after_checkpoint,omitempty"`
	MaxBytes             int                     `json:"max_bytes,omitempty"`
	PhaseID              string                  `json:"phase_id,omitempty"`
}

func (s *Server) handleWorkflowContext(ctx context.Context, raw json.RawMessage) (any, error) {
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
	case "list_workflows":
		return s.handleListWorkflows(ctx, args)
	case "set_phase":
		return s.handleSetPhase(ctx, args)
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

	if err := s.store.SupersedeOldCheckpoints(ctx, args.WorkflowID, cp.Revision); err != nil {
		// non-fatal
	}

	return map[string]any{
		"status":      "saved",
		"workflow_id": args.WorkflowID,
		"revision":    cp.Revision,
		"created_at":  cp.CreatedAt,
	}, nil
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
			"freshness":  map[string]any{"status": "missing", "reasons": []string{"no checkpoint exists"}},
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
	summaries, err := s.store.ListWorkflowSummaries(ctx, args.Limit)
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

	if err := s.store.SetWorkflowPhase(ctx, args.WorkflowID, args.PhaseID, false); err != nil {
		return nil, fmt.Errorf("failed to set phase: %w", err)
	}

	if s.onUpdate != nil {
		s.onUpdate("phase_changed", map[string]string{
			"workflow_id": args.WorkflowID,
			"phase":       args.PhaseID,
		})
	}

	currentPhase, phaseItems, _ := s.store.GetWorkflowPhaseWithDefaults(ctx, args.WorkflowID)
	var currentPrompt string
	for _, p := range phaseItems {
		if p.ID == currentPhase {
			currentPrompt = p.Prompt
			break
		}
	}

	return map[string]any{
		"status":        "updated",
		"workflow_id":   args.WorkflowID,
		"current_phase": currentPhase,
		"phase_prompt":  currentPrompt,
	}, nil
}

// collectDeltasAfterCheckpoint gathers reports/feedback that arrived after the checkpoint basis
func (s *Server) collectDeltasAfterCheckpoint(ctx context.Context, workflowID string, basis model.CheckpointBasis) ([]map[string]any, error) {
	var deltas []map[string]any

	sessions, err := s.store.GetSessionsByWorkflow(ctx, workflowID, 10)
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

	sessions, _ := s.store.GetSessionsByWorkflow(ctx, workflowID, 5)
	newSessions := 0
	for _, sess := range sessions {
		if sess.CreatedAt.After(cp.CreatedAt) {
			newSessions++
		}
	}
	if newSessions > 0 {
		reasons = append(reasons, fmt.Sprintf("%d new session(s) since checkpoint", newSessions))
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
