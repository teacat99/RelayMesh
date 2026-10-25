package mcp

import (
	"context"
	"log"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// postBoardLines 把一批任务写入产生的协作事件写成群里的系统行（#180 B3c），再通知网页刷新对应的群。
//
// 在任务写入成功之后调用，批量（task_board batch）把整批结果一起交进来：同一种事件、同一对派发者与承接者合成一条。
// 尽力而为：写不进去只影响群里的可见性，记一条日志即可，不影响工具回执。
func (s *Server) postBoardLines(ctx context.Context, results ...*store.WorkItemResult) {
	var events []store.BoardLineEvent
	for _, res := range results {
		if res != nil && res.BoardLine != nil {
			events = append(events, *res.BoardLine)
		}
	}
	if len(events) == 0 {
		return
	}
	groups, err := s.store.PostBoardGroupLines(ctx, events)
	if err != nil {
		log.Printf("[board] 写群里的协作系统行失败：%v", err)
		return
	}
	for _, g := range groups {
		s.notifyGroupChanged(g)
	}
}
