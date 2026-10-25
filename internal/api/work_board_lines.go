package api

import (
	"context"
	"log"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// postBoardLines 把网页上这次任务写入产生的协作事件（验收、退回等）写成群里的系统行（#180 B3c），并通知网页刷新对应的群。
// 尽力而为：写不进去只影响群里的可见性，记一条日志即可，不影响任务本身的回执。
func (h *APIHandler) postBoardLines(ctx context.Context, res *store.WorkItemResult) {
	if res == nil || res.BoardLine == nil {
		return
	}
	groups, err := h.store.PostBoardGroupLines(ctx, []store.BoardLineEvent{*res.BoardLine})
	if err != nil {
		log.Printf("[board] 写群里的协作系统行失败：%v", err)
		return
	}
	for _, g := range groups {
		h.touchGroup(g)
	}
}
