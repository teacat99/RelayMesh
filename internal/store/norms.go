package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

const (
	NormNameMaxLen    = 128
	NormSummaryMaxLen = 500
	NormContentMaxLen = 20000
	NormMaxCount      = 50
)

const DefaultAgentModesSummary = "会话场景模式 playbook（online 值守 / away 安全兜底 / autopilot 外部编排）+ Phase 0-7 开发工作流。"

const DefaultAgentModesContent = `---
name: agent-modes
description: 会话场景模式 playbook（online 值守 / away 安全兜底 / autopilot 外部编排）+ Phase 0-7 开发工作流。隶属 L0 路由契约，冲突以 L0 为准。RelayMesh flowPrompts 为行为指令主载体，本 Skill 为 RelayMesh 不可用时的最小兜底。触发词：场景模式 / 值守 / 兜底 / 自驾 / 静默推进 / autopilot / Phase / 工作流 / agent-modes。
---

# agent-modes · 会话场景模式 + 开发工作流

> **定位**：本 Skill 是 L0 路由契约的〈模式 + Phase〉兜底实现档。
> **RelayMesh 为主**：模式行为指令优先由 RelayMesh flowPrompts 在 MCP 回执中动态注入；本规范作为系统级常驻规范。
> **模式判定**：online 在线值守（默认）/ away 安全兜底（MCP 通信异常自动降级）/ autopilot 外部编排（Task API 编排）。
> **全模式底线**：生产部署、DB 迁移、删数据、破坏性命令、push main、发布 release/tag 等不可逆或高风险操作，必须二次确认。

## 1. 模式总览与行为约束

| 模式 | 代码值 | 场景 | 核心行为 |
|---|---|---|---|
| **在线值守** | online | 用户在场，标准人机协同 | 高频 feedback 沟通，歧义即问，不可逆操作二次确认 |
| **安全兜底** | away | autopilot 运行中遇到 MCP 报错/异常时自动降级 | 不主动触发沟通，记录待决事项，推进已授权范围，不可逆动作暂缓 |
| **外部编排** | autopilot | 外部系统（Codex 等）通过 RelayMesh Task API 编排 | 通过 configure_task 接收指令，通过 report_progress 汇报进度，按 task segments 执行 |

## 2. Phase 0-7 开发工作流

- Phase 0: 需求收集
- Phase 0b: 需求澄清（AI 复述 → 用户确认）
- Phase 1: 可选外部规划
- Phase 2: 方案评审（C-PLAN / C-RT）
- Phase 3: 实施准备（决策锁定）
- Phase 4: 开发执行（增量验证，分批汇报）
- Phase 5: 可选外部审计
- Phase 6: 部署验证（硬停点）
- Phase 7: 收尾归档（环境清理与总结）

## 3. 推理与工程纪律 (C-PLAN / C-RT)

- **C-PLAN (正向推演)**: 决策确认 → 问题拆解 → 数据流与状态 → 不变量 → 边界与异常 → 影响面 → 备选方案(≥2) → 验收标准。
- **C-RT (反向自驳)**: 哪最幼稚？什么输入会崩溃？漏了哪个边界？是否局部最优？命中立即修正。

## 4. 会话工作表 (Workflow Sheet) 自适应规范

- 场景 A：若项目根目录存在 .cursor/sessions/ 规范，按该规范维护本地会话文档；
- 场景 B：遵守当前项目文档规范（如项目自有 docs/ 体系）；
- 场景 C：若项目无本地文档体系，统一使用 workflow_context(action: "session_doc_save") 持久化至中枢。`

func (s *Store) SeedBuiltinNorms(ctx context.Context) error {
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.UserNorm{}).Where("name = ?", "agent-modes").Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		now := time.Now()
		norm := &model.UserNorm{
			Name:      "agent-modes",
			Summary:   DefaultAgentModesSummary,
			Content:   DefaultAgentModesContent,
			IsActive:  true,
			SortOrder: 0,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return s.db.WithContext(ctx).Create(norm).Error
	}
	return nil
}

func (s *Store) ListUserNorms(ctx context.Context) ([]model.UserNorm, error) {
	var norms []model.UserNorm
	err := s.db.WithContext(ctx).Order("sort_order ASC, created_at ASC").Find(&norms).Error
	return norms, err
}

func (s *Store) ListActiveUserNorms(ctx context.Context) ([]model.UserNorm, error) {
	var norms []model.UserNorm
	err := s.db.WithContext(ctx).Where("is_active = ?", true).Order("sort_order ASC, created_at ASC").Find(&norms).Error
	return norms, err
}

func (s *Store) GetUserNorm(ctx context.Context, name string) (*model.UserNorm, error) {
	var norm model.UserNorm
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&norm).Error
	if err == gorm.ErrRecordNotFound {
		return nil, NewNotFoundError(fmt.Sprintf("norm %q not found", name))
	}
	return &norm, err
}

func (s *Store) CreateUserNorm(ctx context.Context, norm *model.UserNorm) error {
	name := strings.TrimSpace(norm.Name)
	if name == "" {
		return NewInvalidInputError("name is required")
	}
	if len(name) > NormNameMaxLen {
		return NewInvalidInputError(fmt.Sprintf("name exceeds %d characters", NormNameMaxLen))
	}
	if len(norm.Summary) > NormSummaryMaxLen {
		return NewInvalidInputError(fmt.Sprintf("summary exceeds %d characters", NormSummaryMaxLen))
	}
	if len(norm.Content) > NormContentMaxLen {
		return NewInvalidInputError(fmt.Sprintf("content exceeds %d characters", NormContentMaxLen))
	}

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.UserNorm{}).Count(&count).Error; err != nil {
			return err
		}
		if count >= NormMaxCount {
			return NewInvalidInputError(fmt.Sprintf("maximum %d norms reached", NormMaxCount))
		}

		var existing model.UserNorm
		if err := tx.Where("name = ?", name).First(&existing).Error; err == nil {
			return NewConflictError(fmt.Sprintf("norm %q already exists", name), 0)
		}

		now := time.Now()
		norm.Name = name
		norm.CreatedAt = now
		norm.UpdatedAt = now
		return tx.Create(norm).Error
	})
}

func (s *Store) UpdateUserNorm(ctx context.Context, name string, updates map[string]any) (*model.UserNorm, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, NewInvalidInputError("name is required")
	}

	if v, ok := updates["summary"]; ok {
		if s, ok := v.(string); ok && len(s) > NormSummaryMaxLen {
			return nil, NewInvalidInputError(fmt.Sprintf("summary exceeds %d characters", NormSummaryMaxLen))
		}
	}
	if v, ok := updates["content"]; ok {
		if s, ok := v.(string); ok && len(s) > NormContentMaxLen {
			return nil, NewInvalidInputError(fmt.Sprintf("content exceeds %d characters", NormContentMaxLen))
		}
	}

	var norm model.UserNorm
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Where("name = ?", name).First(&norm).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("norm %q not found", name))
			}
			return err
		}
		updates["updated_at"] = time.Now()
		if err := tx.Model(&norm).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("name = ?", name).First(&norm).Error
	})
	return &norm, err
}

func (s *Store) DeleteUserNorm(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return NewInvalidInputError("name is required")
	}

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		result := tx.Where("name = ?", name).Delete(&model.UserNorm{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return NewNotFoundError(fmt.Sprintf("norm %q not found", name))
		}
		return nil
	})
}
