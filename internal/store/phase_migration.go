package store

import (
	"context"
	"encoding/json"
	"log"
	"maps"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// 阶段三段式迁移（v1.7）：评估 / 方案 / 开发 / 验证 / 完成 → 讨论 / 方案 / 开发。
//
// 需要收敛的存量数据有三处：
//  1. workflow_phase_states：每个工作流当前的阶段、人工偏好的阶段，以及可选的工作流级阶段列表；
//  2. 全局设置里的 phaseTemplate（阶段模板）；
//  3. 全局设置里的 serverInstructions（MCP initialize 指引）中「阶段流转」一节的旧写法。
//
// 迁移是幂等的，并且读取路径（GetWorkflowPhaseWithDefaults）本身也会就地收敛旧值，
// 所以即使这里因为任何原因没跑成，也不会影响阶段的读取和切换。
// 回滚时 deploy-direct 自带的数据库备份会一并恢复，这里不另存备份。

// serverInstructionSectionMigrations 是存量服务指引里「逐字相同才替换」的整节迁移表：
// 阶段流转（v1.7，五段式 → 三段式）与全模式不可逆操作底线（#217，L2 在异步对话里改发确认卡）。
var serverInstructionSectionMigrations = []struct{ legacy, current string }{
	{phaseSectionLegacy, phaseSection},
	{irreversibleSectionLegacy, irreversibleSection},
}

// migrateServerInstructions 把存量服务指引里逐字相同的旧章节换成新写法（见 serverInstructionSectionMigrations）。
// 用户改过某一节（或整段指引）时，那一节原样保留。
func migrateServerInstructions(text string) (string, bool) {
	changed := false
	for _, m := range serverInstructionSectionMigrations {
		if strings.Contains(text, m.legacy) {
			text = strings.Replace(text, m.legacy, m.current, 1)
			changed = true
		}
	}
	return text, changed
}

// migrateSettingsPhases 收敛设置里的阶段模板与服务指引。没有改动时原样返回 m；
// 有改动时返回浅拷贝，不修改调用方传入的 map。
func migrateSettingsPhases(m map[string]any) (map[string]any, bool) {
	out := m
	changed := false
	set := func(key string, value any) {
		if !changed {
			out = maps.Clone(m)
			changed = true
		}
		out[key] = value
	}

	if raw, ok := m["phaseTemplate"]; ok && raw != nil {
		if b, err := json.Marshal(raw); err == nil {
			var items []model.PhaseItem
			if json.Unmarshal(b, &items) == nil {
				if migrated, ch := model.MigratePhaseItems(items); ch {
					set("phaseTemplate", migrated)
				}
			}
		}
	}
	if text, ok := m["serverInstructions"].(string); ok {
		if migrated, ch := migrateServerInstructions(text); ch {
			set("serverInstructions", migrated)
		}
	}
	return out, changed
}

// migratePhasesV3 在启动时把存量数据收敛为三段式，失败只记日志、不阻止启动。
func (s *Store) migratePhasesV3(ctx context.Context) {
	rows := s.migratePhaseRows(ctx)
	settingsChanged := s.migratePhaseSettings(ctx)
	if rows > 0 || settingsChanged {
		log.Printf("[phase-migration] 阶段已收敛为讨论 / 方案 / 开发：工作流 %d 条，全局设置 %v", rows, settingsChanged)
	}
}

// migratePhaseRows 收敛 workflow_phase_states，返回被改动的行数。
func (s *Store) migratePhaseRows(ctx context.Context) int {
	var states []model.WorkflowPhaseState
	if err := s.db.WithContext(ctx).Find(&states).Error; err != nil {
		log.Printf("[phase-migration] 读取工作流阶段失败: %v", err)
		return 0
	}
	changedRows := 0
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		for _, st := range states {
			updates := map[string]any{}
			if c := model.CanonicalPhaseID(st.CurrentPhaseID); c != st.CurrentPhaseID {
				updates["current_phase_id"] = c
			}
			if p := model.CanonicalPhaseID(st.HumanPreferredPhaseID); p != st.HumanPreferredPhaseID {
				updates["human_preferred_phase_id"] = p
			}
			if st.PhasesJSON != "" {
				var items []model.PhaseItem
				if json.Unmarshal([]byte(st.PhasesJSON), &items) == nil {
					if migrated, ch := model.MigratePhaseItems(items); ch {
						if b, err := json.Marshal(migrated); err == nil {
							updates["phases_json"] = string(b)
						}
					}
				}
			}
			if len(updates) == 0 {
				continue
			}
			// 不改 updated_at：这只是 id 换名，不是用户或智能体的一次阶段变更。
			if err := tx.Model(&model.WorkflowPhaseState{}).
				Where("workflow_id = ?", st.WorkflowID).
				UpdateColumns(updates).Error; err != nil {
				return err
			}
			changedRows++
		}
		return nil
	})
	if err != nil {
		log.Printf("[phase-migration] 更新工作流阶段失败: %v", err)
		return 0
	}
	return changedRows
}

// migratePhaseSettings 收敛全局设置里的阶段模板与服务指引，返回是否有改动。
func (s *Store) migratePhaseSettings(ctx context.Context) bool {
	var record model.SystemSetting
	if err := s.db.WithContext(ctx).Where("key = ?", GlobalSettingsKey).First(&record).Error; err != nil {
		return false // 没有保存过设置：读取时用的就是新默认值
	}
	var current map[string]any
	if err := json.Unmarshal([]byte(record.Value), &current); err != nil {
		return false
	}
	migrated, changed := migrateSettingsPhases(current)
	if !changed {
		return false
	}
	if err := s.SaveSettings(ctx, migrated); err != nil {
		log.Printf("[phase-migration] 保存设置失败: %v", err)
		return false
	}
	return true
}
