package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 按工作流的文档 → 项目（设计稿 06 §8）。
//
// 流程：用户把目录归入项目 → 先 dry-run 出报告（每篇文档的去向、冲突落点、哈希）→ 确认时带上报告里的哈希，
// 哈希对不上（期间文档变了）就拒绝，要求重新 dry-run。确认的就是看过的那一份。
//
// 规则：
//   - doc id / seq / 修订历史都不变，书签不会失效；`workflow_id` 列保留（旧二进制照常按工作流读）；
//   - 项目内路径唯一：先创建的保留原路径，同路径的后来者落到 `legacy/<工作流>/<原路径>`；
//   - 工作流的会话文档 `session/…`（导入的 Workflow Sheet）落到 `sessions/<工作流>/…`，不同工作流各有一份、不会冲突；
//   - 已经在项目里的文档（project_id 已等于该项目）不动，但它们占着自己的路径。

// MigrationMove 是一篇文档的去向。
type MigrationMove struct {
	DocID      uint   `json:"doc_id"`
	WorkflowID string `json:"workflow_id"`
	FromPath   string `json:"from_path"`
	ToPath     string `json:"to_path"`
	Reason     string `json:"reason"` // sheet | conflict
}

// MigrationItem 是一篇待并入项目的文档的去向（路径不变的也在内）。
//
// Moves 只列「要改路径」的，看不出其余文档是谁；Items 把每一篇都列出来——用户确认迁移前要看清「每篇文档去哪」，
// 知识库页也靠它在迁移完成前继续列出这些还没并入的文档，不让它们从入口上消失（#131）。
type MigrationItem struct {
	DocID      uint   `json:"doc_id"`
	WorkflowID string `json:"workflow_id"`
	FromPath   string `json:"from_path"`
	ToPath     string `json:"to_path"`
	Title      string `json:"title"`
	Reason     string `json:"reason,omitempty"` // 空 = 路径不变；sheet | conflict（与 MigrationMove 同义）
}

// MigrationReport 是迁移报告（dry-run 与确认后都用这个结构）。
type MigrationReport struct {
	ProjectID   uint            `json:"project_id"`
	Workflows   []string        `json:"workflows"`
	Docs        int             `json:"docs"`                  // 涉及的、尚未归入项目的文档数
	Assigned    int             `json:"assigned"`              // 其中原路径不变直接归入的
	Moves       []MigrationMove `json:"moves"`                 // 需要改路径的
	Items       []MigrationItem `json:"items"`                 // 全部待迁移的文档（= Docs 篇，含路径不变的）；不参与哈希
	Already     int             `json:"already"`               // 已经在项目里的文档数
	Unplaceable []string        `json:"unplaceable,omitempty"` // 改路径后仍不合法（太长等）而跳过的文档说明
	Hash        string          `json:"hash"`
	Applied     bool            `json:"applied"`
}

type migrationDoc struct {
	ID         uint
	WorkflowID string
	Path       string
	Hash       string
}

// planMigration 算出迁移计划（只读）。
func (s *Store) planMigration(ctx context.Context, projectID uint) (*MigrationReport, []migrationDoc, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, nil, err
	}
	wfs, err := s.ProjectWorkflowIDs(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	rep := &MigrationReport{ProjectID: projectID, Workflows: wfs, Moves: []MigrationMove{}, Items: []MigrationItem{}}
	if len(wfs) == 0 {
		rep.Hash = migrationHash(rep, nil)
		return rep, nil, nil
	}
	var all []model.ProjectDoc
	if err := s.db.WithContext(ctx).Select("id", "workflow_id", "path", "title", "hash", "project_id", "created_at").
		Where("workflow_id IN ? AND deleted_at IS NULL AND (project_id = 0 OR project_id = ?)", wfs, projectID).
		Order("created_at ASC, id ASC").Find(&all).Error; err != nil {
		return nil, nil, err
	}
	taken := map[string]bool{}
	for _, d := range all {
		if d.ProjectID == projectID {
			taken[d.Path] = true
			rep.Already++
		}
	}
	// 项目里已有、但属于别的工作流（不在上面列表里）的文档也占路径。
	var others []model.ProjectDoc
	if err := s.db.WithContext(ctx).Select("path").Where("project_id = ? AND deleted_at IS NULL", projectID).Find(&others).Error; err != nil {
		return nil, nil, err
	}
	for _, d := range others {
		taken[d.Path] = true
	}
	// 同一工作流里本来就有的路径：改过去的目标不能撞上（老唯一键是 工作流 + 路径）。
	wfPaths := map[string]bool{}
	for _, d := range all {
		wfPaths[d.WorkflowID+"\x00"+d.Path] = true
	}
	var todo []migrationDoc
	for _, d := range all {
		if d.ProjectID == projectID {
			continue
		}
		rep.Docs++
		to, reason := d.Path, ""
		if rest, ok := strings.CutPrefix(d.Path, "session/"); ok {
			to, reason = "sessions/"+d.WorkflowID+"/"+rest, "sheet"
		}
		if taken[to] {
			to, reason = "legacy/"+d.WorkflowID+"/"+d.Path, "conflict"
			if taken[to] {
				to = fmt.Sprintf("legacy/%s/%d-%s", d.WorkflowID, d.ID, d.Path)
			}
		}
		norm, err := docs.NormalizePath(to)
		if err != nil || taken[norm] || (norm != d.Path && wfPaths[d.WorkflowID+"\x00"+norm]) {
			rep.Unplaceable = append(rep.Unplaceable, fmt.Sprintf("#%d %s（%s）：改路径后不合法或仍冲突", d.ID, d.Path, d.WorkflowID))
			continue
		}
		taken[norm] = true
		todo = append(todo, migrationDoc{ID: d.ID, WorkflowID: d.WorkflowID, Path: norm, Hash: d.Hash})
		item := MigrationItem{DocID: d.ID, WorkflowID: d.WorkflowID, FromPath: d.Path, ToPath: norm, Title: d.Title}
		if norm == d.Path {
			rep.Assigned++
		} else {
			item.Reason = reason
			rep.Moves = append(rep.Moves, MigrationMove{DocID: d.ID, WorkflowID: d.WorkflowID, FromPath: d.Path, ToPath: norm, Reason: reason})
		}
		rep.Items = append(rep.Items, item)
	}
	rep.Hash = migrationHash(rep, todo)
	return rep, todo, nil
}

func migrationHash(rep *MigrationReport, todo []migrationDoc) string {
	h := sha256.New()
	fmt.Fprintf(h, "p=%d\n", rep.ProjectID)
	ids := make([]migrationDoc, len(todo))
	copy(ids, todo)
	sort.Slice(ids, func(i, j int) bool { return ids[i].ID < ids[j].ID })
	for _, d := range ids {
		fmt.Fprintf(h, "%d|%s|%s|%s\n", d.ID, d.WorkflowID, d.Path, d.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// MigrationDryRun 只读：报告「如果现在确认，每篇文档会去哪里」。
func (s *Store) MigrationDryRun(ctx context.Context, projectID uint) (*MigrationReport, error) {
	rep, _, err := s.planMigration(ctx, projectID)
	return rep, err
}

// ApplyMigration 确认迁移：重新计算计划，哈希与 dry-run 的不一致就拒绝；一致才在一个事务里落库。
func (s *Store) ApplyMigration(ctx context.Context, projectID uint, expectHash string) (*MigrationReport, error) {
	rep, todo, err := s.planMigration(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(expectHash) == "" || expectHash != rep.Hash {
		return nil, NewConflictError("文档在 dry-run 之后发生了变化（或没有带上 dry-run 的哈希）：请重新 dry-run，看过新的报告再确认", 0)
	}
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		for _, d := range todo {
			// UpdateColumns 不碰 updated_at：迁移不是内容修改，「最近修订」要保持真实。
			if err := tx.Model(&model.ProjectDoc{}).Where("id = ?", d.ID).
				UpdateColumns(map[string]any{"project_id": projectID, "path": d.Path}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rep.Applied = true
	return rep, nil
}
