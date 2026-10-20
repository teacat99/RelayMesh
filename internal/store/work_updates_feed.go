package store

import (
	"context"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// WorkflowUpdate 是会话时间线上的一条任务进展：一条更新记录，外加它所属任务的序号 / 标题 / 当前状态。
// 会话页的进展卡「每次更新一张」，靠它按提交时间排进对话（#105）。
type WorkflowUpdate struct {
	ID uint `json:"id"`
	// Seq / Title / State 是任务本身的序号、标题与当前状态（State 不是这次更新当时的状态，当时的在 ToState）。
	Seq   int    `json:"seq"`
	Title string `json:"title"`
	State string `json:"state"`
	// FromState / ToState 只有这次更新改了状态时才有；创建任务那一条只有 ToState。
	FromState string    `json:"from_state,omitempty"`
	ToState   string    `json:"to_state,omitempty"`
	Text      string    `json:"text,omitempty"`
	ActorKind string    `json:"actor_kind"`
	CreatedAt time.Time `json:"created_at"`
}

// WorkflowUpdatesMax 是一次最多返回的进展条数：会话里只看最近的，更早的去任务页看。
const WorkflowUpdatesMax = 100

// ListWorkflowUpdates 返回一个工作流里「智能体」写下的任务进展，按提交时间升序，最多 limit 条最新的。
// 人在网页上点状态 / 改进度是自己做的事，不进对话；since 非零时只要这个时间之后的。
func (s *Store) ListWorkflowUpdates(ctx context.Context, workflowID string, since time.Time, limit int) ([]WorkflowUpdate, error) {
	if limit <= 0 || limit > WorkflowUpdatesMax {
		limit = WorkflowUpdatesMax
	}
	q := s.db.WithContext(ctx).
		Table("work_item_updates AS u").
		Select("u.id AS id, w.seq AS seq, w.title AS title, w.state AS state, "+
			"u.from_state AS from_state, u.to_state AS to_state, u.text AS text, "+
			"u.actor_kind AS actor_kind, u.created_at AS created_at").
		Joins("JOIN work_items AS w ON w.id = u.work_item_id").
		Where("w.workflow_id = ? AND u.actor_kind = ?", workflowID, model.WorkActorAgent)
	if !since.IsZero() {
		// 库里的时间是按进程本地时区写成文本的，SQLite 里是逐字符比较：外面传进来的 UTC 时间（浏览器的 ISO 串）
		// 必须先换成同一个时区，否则 +08:00 与 Z 之间会整段比错。
		q = q.Where("u.created_at > ?", since.In(time.Local))
	}
	var rows []WorkflowUpdate
	if err := q.Order("u.created_at DESC, u.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	// 取的是最新的 limit 条（倒序），交付时翻回时间升序
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}
