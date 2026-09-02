package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/teacat99/RelayMesh/internal/model"
	"github.com/teacat99/RelayMesh/internal/store"
)

type manageSkillsArgs struct {
	Action   string `json:"action"`
	Name     string `json:"name,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Content  string `json:"content,omitempty"`
	IsActive *bool  `json:"is_active,omitempty"`
}

func (s *Server) handleManageSkills(ctx context.Context, raw json.RawMessage) (any, error) {
	var args manageSkillsArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}

	switch args.Action {
	case "list":
		return s.handleSkillsList(ctx)
	case "get":
		return s.handleSkillsGet(ctx, args)
	case "create":
		return s.handleSkillsCreate(ctx, args)
	case "update":
		return s.handleSkillsUpdate(ctx, args)
	case "delete":
		return s.handleSkillsDelete(ctx, args)
	default:
		return nil, store.NewInvalidInputError(fmt.Sprintf("unknown action: %q, expected one of: list, get, create, update, delete", args.Action))
	}
}

func (s *Server) handleSkillsList(ctx context.Context) (any, error) {
	norms, err := s.store.ListUserNorms(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]map[string]any, 0, len(norms))
	for _, n := range norms {
		items = append(items, map[string]any{
			"name":      n.Name,
			"summary":   n.Summary,
			"is_active": n.IsActive,
		})
	}

	return map[string]any{
		"total":  len(items),
		"skills": items,
	}, nil
}

func (s *Server) handleSkillsGet(ctx context.Context, args manageSkillsArgs) (any, error) {
	if args.Name == "" {
		return nil, store.NewInvalidInputError("name is required for get action")
	}

	norm, err := s.store.GetUserNorm(ctx, args.Name)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"name":      norm.Name,
		"summary":   norm.Summary,
		"content":   norm.Content,
		"is_active": norm.IsActive,
	}, nil
}

func (s *Server) handleSkillsCreate(ctx context.Context, args manageSkillsArgs) (any, error) {
	if args.Name == "" {
		return nil, store.NewInvalidInputError("name is required for create action")
	}
	if args.Summary == "" {
		return nil, store.NewInvalidInputError("summary is required for create action")
	}
	if args.Content == "" {
		return nil, store.NewInvalidInputError("content is required for create action")
	}

	isActive := true
	if args.IsActive != nil {
		isActive = *args.IsActive
	}

	norm := &model.UserNorm{
		Name:     args.Name,
		Summary:  args.Summary,
		Content:  args.Content,
		IsActive: isActive,
	}

	if err := s.store.CreateUserNorm(ctx, norm); err != nil {
		return nil, err
	}

	if s.onUpdate != nil {
		s.onUpdate("norms_updated", map[string]string{"name": norm.Name, "action": "create"})
	}

	return map[string]any{
		"success": true,
		"name":    norm.Name,
	}, nil
}

func (s *Server) handleSkillsUpdate(ctx context.Context, args manageSkillsArgs) (any, error) {
	if args.Name == "" {
		return nil, store.NewInvalidInputError("name is required for update action")
	}

	updates := make(map[string]any)
	if args.Summary != "" {
		updates["summary"] = args.Summary
	}
	if args.Content != "" {
		updates["content"] = args.Content
	}
	if args.IsActive != nil {
		updates["is_active"] = *args.IsActive
	}

	if len(updates) == 0 {
		return nil, store.NewInvalidInputError("at least one of summary, content, or is_active is required for update")
	}

	norm, err := s.store.UpdateUserNorm(ctx, args.Name, updates)
	if err != nil {
		return nil, err
	}

	if s.onUpdate != nil {
		s.onUpdate("norms_updated", map[string]string{"name": norm.Name, "action": "update"})
	}

	return map[string]any{
		"success": true,
		"name":    norm.Name,
	}, nil
}

func (s *Server) handleSkillsDelete(ctx context.Context, args manageSkillsArgs) (any, error) {
	if args.Name == "" {
		return nil, store.NewInvalidInputError("name is required for delete action")
	}

	if err := s.store.DeleteUserNorm(ctx, args.Name); err != nil {
		return nil, err
	}

	if s.onUpdate != nil {
		s.onUpdate("norms_updated", map[string]string{"name": args.Name, "action": "delete"})
	}

	return map[string]any{
		"success": true,
		"name":    args.Name,
	}, nil
}
