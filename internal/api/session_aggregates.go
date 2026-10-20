package api

import (
	"context"
	"log"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// attachWorkflowAggregates 给会话行补上「整个工作流」的只读聚合（#159、#173）：
//   - PendingConfirmCount：这个工作流里还没答复的确认项（提问 / 计划 / 决策）条数；
//   - AgentLastActivityAt：智能体最近一次与 RelayMesh 交互的时间（服务端内存里记，重启后清空，没有记录时不带）；
//   - WorkflowRounds：这个工作流在库里一共有多少行会话（含已归档）——列表只带最新一批行，侧栏「N 轮」徽标不能拿行数凑。
//
// 侧栏会话卡要显示「会话级状态」（与页头同一套口径），可侧栏拿不到别的工作流的确认项与智能体动静，所以由列表接口顺带给出。
// 同一个工作流的多行共用同一份数据，所以按工作流去重后只查一次（两条分组查询 + 内存里取最近动作），不随行数线性增长。
// 没有工作流编号的行不带这几项；聚合查询失败时只丢对应的那一项（前端按「没拿到」处理），不拖垮会话列表本身。
func (h *APIHandler) attachWorkflowAggregates(ctx context.Context, rows []*model.FeedbackSession) {
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if r == nil || r.WorkflowID == "" {
			continue
		}
		if _, ok := seen[r.WorkflowID]; ok {
			continue
		}
		seen[r.WorkflowID] = struct{}{}
		ids = append(ids, r.WorkflowID)
	}
	if len(ids) == 0 {
		return
	}

	counts, err := h.store.CountPendingConfirmsByWorkflow(ctx, ids)
	if err != nil {
		log.Printf("会话列表聚合待处理确认项失败（不影响列表本身）: %v", err)
		counts = nil
	}
	rounds, err := h.store.CountFeedbackSessionsByWorkflow(ctx, ids)
	if err != nil {
		log.Printf("会话列表聚合工作流轮数失败（不影响列表本身）: %v", err)
		rounds = nil
	}

	for _, r := range rows {
		if r == nil || r.WorkflowID == "" {
			continue
		}
		if counts != nil {
			n := counts[r.WorkflowID]
			r.PendingConfirmCount = &n
		}
		if rounds != nil {
			n := rounds[r.WorkflowID]
			r.WorkflowRounds = &n
		}
		if t, ok := h.store.AgentLastActivity(r.WorkflowID); ok {
			at := t
			r.AgentLastActivityAt = &at
		}
	}
}
