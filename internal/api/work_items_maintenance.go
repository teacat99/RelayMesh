package api

import (
	"context"
	"log"
	"time"
)

const (
	// workCompactFirstDelay：启动后先等一会儿再做第一次整理，不和启动期的迁移、重连抢资源。
	workCompactFirstDelay = 2 * time.Minute
	workCompactInterval   = 24 * time.Hour
)

// startWorkItemMaintenance 启动任务清单的后台整理：关闭超过 90 天的任务压缩掉时间线与进度（保留结论与引用）。
// 重复调用无副作用；Shutdown 时随 stopWorkItemMaintenance 退出。
func (s *Server) startWorkItemMaintenance() {
	s.workMaintMu.Lock()
	defer s.workMaintMu.Unlock()
	if s.workMaintCancel != nil || s.store == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.workMaintCancel = cancel
	go func() {
		timer := time.NewTimer(workCompactFirstDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if n, err := s.store.CompactWorkItems(ctx, time.Now()); err != nil {
				log.Printf("[work] 整理关闭已久的任务失败: %v", err)
			} else if n > 0 {
				log.Printf("[work] 已压缩 %d 个关闭超过 90 天的任务（保留标题 / 结论 / 引用，清理进展时间线）", n)
			}
			timer.Reset(workCompactInterval)
		}
	}()
}

func (s *Server) stopWorkItemMaintenance() {
	s.workMaintMu.Lock()
	defer s.workMaintMu.Unlock()
	if s.workMaintCancel != nil {
		s.workMaintCancel()
		s.workMaintCancel = nil
	}
}
