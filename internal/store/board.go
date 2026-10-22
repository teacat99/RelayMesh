package store

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 任务板（board）：任务的归属单位（#175，设计稿 design/shared-task-board.md）。
//
// 板用字符串 board_key 标识，不新建「板」表：
//   - 项目板 "p<项目编号>"：目录归入同一个项目的所有工作流共用一张板；
//   - 独立板 "w:<工作流编号>"：工作流不属于任何项目时自己一张板。
//
// 请求面（REST / MCP / 前端）传的仍是 workflow_id，含义是「调用方所在的工作流」；store 先把它解析成板，
// 再按 board_key 取数。单工作流的板里一切与按工作流取数完全一致。
//
// 这个文件放「板」的规则：key 的拼写与解析、工作流 → 板的解析（动态 + 粘性）。
// 启动时给历史任务补板见 board_reconcile.go；并入预演见 board_merge.go。

const (
	boardProjectPrefix  = "p"
	boardWorkflowPrefix = "w:"
)

// ProjectBoardKey 返回项目板的 key。
func ProjectBoardKey(projectID uint) string {
	return boardProjectPrefix + strconv.FormatUint(uint64(projectID), 10)
}

// WorkflowBoardKey 返回工作流独立板的 key。
func WorkflowBoardKey(workflowID string) string {
	return boardWorkflowPrefix + workflowID
}

// ParseBoardKey 拆开 board_key：项目板返回 projectID，独立板返回 workflowID；不合法返回 ok=false。
func ParseBoardKey(key string) (projectID uint, workflowID string, ok bool) {
	switch {
	case strings.HasPrefix(key, boardWorkflowPrefix):
		wf := strings.TrimPrefix(key, boardWorkflowPrefix)
		return 0, wf, wf != ""
	case strings.HasPrefix(key, boardProjectPrefix):
		n, err := strconv.ParseUint(strings.TrimPrefix(key, boardProjectPrefix), 10, 64)
		return uint(n), "", err == nil && n > 0
	}
	return 0, "", false
}

// isProjectBoard 判断 key 是不是项目板。
func isProjectBoard(key string) bool {
	p, _, ok := ParseBoardKey(key)
	return ok && p != 0
}

// BoardInfo 是一个工作流按目录归属解析出的板。
type BoardInfo struct {
	// Key 是板的 key。
	Key string
	// Project 在项目板时非空。
	Project *model.Project
	// Host / Dir 是解析所依据的最近一条带目录的汇报会话；没有时为空。
	Host, Dir string
	// Reason 是一句话说明为什么落在这张板（迁移预演与排错用）。
	Reason string
}

// resolveBoard 按「最近一条带目录的会话所在（主机，目录）」解析工作流所属的板：
// 命中某个未归档项目的目录 = 项目板，否则 = 独立板。规则与 ProjectOfWorkflow 一致，是它的超集。
//
// 这是「按当前目录归属」的动态解析；已经有任务的工作流不会因为目录改了就换板，
// 那一层稳定性由 boardKeyOn 的粘性规则保证（换板只能经显式合并，见设计稿第 8 节第 5 点）。
func (s *Store) resolveBoard(ctx context.Context, workflowID string) (BoardInfo, error) {
	return resolveBoardOn(ctx, s.db, workflowID)
}

// resolveBoardOn 是 resolveBoard 的核心，可以在事务句柄上调用（单连接下事务里不能再用 s.db）。
func resolveBoardOn(ctx context.Context, db *gorm.DB, workflowID string) (BoardInfo, error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return BoardInfo{}, errors.New("workflow_id 不能为空")
	}
	host, dir := workflowDirOn(ctx, db, wf)
	info := BoardInfo{Key: WorkflowBoardKey(wf), Host: host, Dir: dir}
	if dir == "" {
		info.Reason = "没有带目录的汇报会话，落独立板"
		return info, nil
	}
	p, err := projectOfDirOn(ctx, db, host, dir)
	if err != nil {
		return BoardInfo{}, err
	}
	if p != nil {
		info.Key, info.Project = ProjectBoardKey(p.ID), p
		info.Reason = "目录属于项目 " + p.Slug
		return info, nil
	}
	if ap, err := archivedProjectOfDirOn(ctx, db, host, dir); err == nil && ap != nil {
		info.Reason = "目录属于已归档的项目 " + ap.Slug + "，落独立板"
	} else {
		info.Reason = "目录不属于任何项目，落独立板"
	}
	return info, nil
}

// archivedProjectOfDirOn 找目录所属的「已归档」项目，只用于给解析结果补一句说明；没有返回 nil, nil。
func archivedProjectOfDirOn(ctx context.Context, db *gorm.DB, host, dir string) (*model.Project, error) {
	q := db.WithContext(ctx).Where("project_directory = ?", dir)
	if h := strings.TrimSpace(host); h != "" {
		q = q.Where("LOWER(host_name) = LOWER(?)", h)
	}
	var pd model.ProjectDir
	if err := q.Order("id ASC").Take(&pd).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var p model.Project
	if err := db.WithContext(ctx).Where("archived_at IS NOT NULL").First(&p, pd.ProjectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// BoardKeyOf 返回工作流现在使用的板（见 boardKeyOn）。
func (s *Store) BoardKeyOf(ctx context.Context, workflowID string) (string, error) {
	return boardKeyOn(ctx, s.db, workflowID)
}

// boardKeyOn 返回工作流现在使用的板，粘性优先：
//  1. 工作流已有归板的任务 → 最近一条任务所在的板；
//  2. 否则已有归板的规划节点 → 最近一个节点所在的板（先规划后建任务时，任务要和规划树在同一张板）；
//  3. 否则按目录动态解析（resolveBoardOn）。
//
// 粘性保证目录归属的变化不会让已有任务「搬家」：换板只能经显式合并，不会静默改号（设计稿 §8、§11.1）。
// db 可以是事务句柄：单连接下事务回调里必须用 tx，不能回头用 s.db（会等自己持有的那条连接）。
func boardKeyOn(ctx context.Context, db *gorm.DB, workflowID string) (string, error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return "", NewInvalidInputError("workflow_id 不能为空")
	}
	for _, table := range []string{"work_items", "work_modules"} {
		var keys []string
		if err := db.WithContext(ctx).Table(table).Where("workflow_id = ? AND board_key <> ''", wf).
			Order("id DESC").Limit(1).Pluck("board_key", &keys).Error; err != nil {
			return "", err
		}
		if len(keys) > 0 {
			return keys[0], nil
		}
	}
	info, err := resolveBoardOn(ctx, db, wf)
	if err != nil {
		return "", err
	}
	return info.Key, nil
}

// BoardWorkflowIDs 列出一张板上能被指派的工作流（#175 B2b，详情里「指派」下拉用）：
// 项目板 = 项目里的全部工作流（最近一次汇报会话的目录落在项目目录里的）加上已经在这张板上建过任务的工作流
// （它们的会话目录后来即使被移出项目，任务仍在板上，按「粘性归板」仍然属于这张板，checkAssignee 也认）；
// 独立板没有可指派的对象，返回空列表。
func (s *Store) BoardWorkflowIDs(ctx context.Context, boardKey string) ([]string, error) {
	pid, _, ok := ParseBoardKey(boardKey)
	if !ok || pid == 0 {
		return []string{}, nil
	}
	ids, err := s.ProjectWorkflowIDs(ctx, pid)
	if err != nil {
		return nil, err
	}
	var withTasks []string
	if err := s.db.WithContext(ctx).Model(&model.WorkItem{}).Where("board_key = ?", boardKey).Distinct().Pluck("workflow_id", &withTasks).Error; err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(ids)+len(withTasks))
	for _, id := range ids {
		set[id] = true
	}
	for _, id := range withTasks {
		if id != "" {
			set[id] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
