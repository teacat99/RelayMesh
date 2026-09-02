package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

func (s *Store) GetWorkflowPhase(ctx context.Context, workflowID string) (*model.WorkflowPhaseState, error) {
	var state model.WorkflowPhaseState
	err := s.db.WithContext(ctx).Where("workflow_id = ?", workflowID).First(&state).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

func (s *Store) SetWorkflowPhase(ctx context.Context, workflowID, phaseID string, isHumanClick bool) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var existing model.WorkflowPhaseState
		err := tx.Where("workflow_id = ?", workflowID).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			state := model.WorkflowPhaseState{
				WorkflowID:     workflowID,
				CurrentPhaseID: phaseID,
				UpdatedAt:      time.Now(),
			}
			if isHumanClick {
				state.HumanPreferredPhaseID = phaseID
			}
			return tx.Create(&state).Error
		}
		if err != nil {
			return err
		}
		updates := map[string]any{
			"current_phase_id": phaseID,
			"updated_at":       time.Now(),
		}
		if isHumanClick {
			updates["human_preferred_phase_id"] = phaseID
		}
		return tx.Model(&existing).Updates(updates).Error
	})
}

func (s *Store) GetHumanPreferredPhase(ctx context.Context, workflowID string) (string, error) {
	state, err := s.GetWorkflowPhase(ctx, workflowID)
	if err != nil {
		return "", err
	}
	if state != nil && state.HumanPreferredPhaseID != "" {
		return state.HumanPreferredPhaseID, nil
	}
	return "assess", nil
}

func (s *Store) SetWorkflowPhaseConfig(ctx context.Context, workflowID string, phases []model.PhaseItem) error {
	phasesJSON, err := json.Marshal(phases)
	if err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var existing model.WorkflowPhaseState
		e := tx.Where("workflow_id = ?", workflowID).First(&existing).Error
		if e == gorm.ErrRecordNotFound {
			return tx.Create(&model.WorkflowPhaseState{
				WorkflowID:     workflowID,
				CurrentPhaseID: "assess",
				PhasesJSON:     string(phasesJSON),
				UpdatedAt:      time.Now(),
			}).Error
		}
		if e != nil {
			return e
		}
		return tx.Model(&existing).Updates(map[string]any{
			"phases_json": string(phasesJSON),
			"updated_at":  time.Now(),
		}).Error
	})
}

func (s *Store) GetWorkflowPhaseWithDefaults(ctx context.Context, workflowID string) (currentPhaseID string, phases []model.PhaseItem, err error) {
	state, err := s.GetWorkflowPhase(ctx, workflowID)
	if err != nil {
		return "", nil, err
	}

	if state != nil && state.PhasesJSON != "" {
		if e := json.Unmarshal([]byte(state.PhasesJSON), &phases); e != nil {
			phases = nil
		}
	}

	if len(phases) == 0 {
		globalSettings, e := s.GetGlobalAppSettings(ctx)
		if e == nil && globalSettings != nil && len(globalSettings.PhaseTemplate) > 0 {
			phases = globalSettings.PhaseTemplate
		} else {
			phases = model.DefaultPhaseTemplate()
		}
	}

	if state != nil {
		currentPhaseID = state.CurrentPhaseID
	}
	if currentPhaseID == "" && len(phases) > 0 {
		currentPhaseID = phases[0].ID
	}

	return currentPhaseID, phases, nil
}

// BackfillPhasePrompts fills in missing Prompt fields from DefaultPhaseTemplate.
func BackfillPhasePrompts(phases []model.PhaseItem) {
	defaults := model.DefaultPhaseTemplate()
	defaultMap := make(map[string]string, len(defaults))
	for _, d := range defaults {
		if d.Prompt != "" {
			defaultMap[d.ID] = d.Prompt
		}
	}
	for i := range phases {
		if phases[i].Prompt == "" {
			if p, ok := defaultMap[phases[i].ID]; ok {
				phases[i].Prompt = p
			}
		}
	}
}
