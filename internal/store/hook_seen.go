package store

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// ---- hook 最近出现时间的落库与恢复（#103）----
//
// 背景：异步对话的「有效」要求近 60 分钟见过该工作流的编辑器 hook（asyncmode.HookFreshness）。这个时间原先只在内存里，
// Hub 每次重启（热更新也算）之后，要等 hook 再来一次才算新鲜，期间异步按 no_hook 降级成同步；
// 同样受影响的还有「能不能开启异步」的判断、会话守护的 hook 痕迹、回执里的「安装 hook」提示。
//
// 做法：把这个时间落库，启动时装回内存；热路径（hook 每十几秒来一次）仍然只读写内存，落库有节流。
//
// 安全性：恢复出来的就是重启之前内存里本来会有的那个时间（最多偏旧 HookSeenPersistEvery），
// 新鲜度仍按同一个 60 分钟窗口判断——没有给异步多送任何「宽限期」。Hub 停了很久再起来，恢复出来的时间自然是旧的，
// 照旧降级为 hook_stale。

// HookSeenPersistEvery 是同一个工作流的 hook 时间落库的最小间隔：落库值最多比真实值旧这么久。
// 重启后恢复出来的值只会偏旧、不会偏新，对 60 分钟的新鲜度窗口可以忽略，方向也是安全的。
const HookSeenPersistEvery = time.Minute

// hookSeenKeep 是落库记录保留多久：更久之前的 hook 痕迹没有恢复的意义，启动时顺手清掉，免得表无限增长。
const hookSeenKeep = 90 * 24 * time.Hour

// hookSeenRestoreMax 与内存表的上限（RecordHookSeen 里 2000 条后开始清陈旧项）对齐：只恢复最近的这些。
const hookSeenRestoreMax = 2000

// noteHook 记下内存里的 hook 时间，并回答两件事：要不要通知 Web 刷新、要不要落库（按 HookSeenPersistEvery 节流）。
// 落库的「已写入时间」在这里就先记上，所以并发的多次调用里只有一个会去写库。
func (a *agentActivity) noteHook(workflowID string, at time.Time) (broadcast, persist bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hook == nil {
		a.hook = make(map[string]time.Time)
	}
	if a.hookSaved == nil {
		a.hookSaved = make(map[string]time.Time)
	}
	if len(a.hook) > 2000 {
		for k, t := range a.hook {
			if at.Sub(t) > InboxExpireAfter {
				delete(a.hook, k)
				delete(a.hookSaved, k)
			}
		}
	}
	prev, seen := a.hook[workflowID]
	a.hook[workflowID] = at
	broadcast = !seen || at.Sub(prev) >= InboxHookSeenBroadcastEvery
	saved, savedOK := a.hookSaved[workflowID]
	if !savedOK || at.Sub(saved) >= HookSeenPersistEvery {
		a.hookSaved[workflowID] = at
		persist = true
	}
	return broadcast, persist
}

// persistHookSeen 把某个工作流的 hook 时间写进库（有则更新）。尽力而为：写不进去只意味着重启后恢复出来的值更旧，
// 不影响当前进程里的判断，所以不向调用方报错（hook 请求本身也不该因为它失败）。
func (s *Store) persistHookSeen(workflowID string, at time.Time) {
	if s.db == nil { // 零值 Store（测试里只用它的内存部分）没有库；落库本来就是尽力而为，跳过即可
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "workflow_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"seen_at"}),
	}).Create(&model.WorkflowHookSeen{WorkflowID: workflowID, SeenAt: at.In(time.Local)}).Error
}

// RestoreHookSeen 在启动时把库里的 hook 时间装回内存，并清掉太久以前的记录。不会覆盖内存里更新的值。
// 返回错误只用于测试与排查：启动流程把它当成尽力而为（装不回来就退回「从头记」的旧行为，那是保守的）。
func (s *Store) RestoreHookSeen(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	// 库里的时间是按本地时区写成文本的，SQLite 里逐字符比较，所以比较用的时间先换成本地时区。
	cutoff := time.Now().Add(-hookSeenKeep).In(time.Local)
	if err := s.db.WithContext(ctx).Where("seen_at < ?", cutoff).Delete(&model.WorkflowHookSeen{}).Error; err != nil {
		return err
	}
	var rows []model.WorkflowHookSeen
	if err := s.db.WithContext(ctx).Order("seen_at DESC").Limit(hookSeenRestoreMax).Find(&rows).Error; err != nil {
		return err
	}
	a := &s.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hook == nil {
		a.hook = make(map[string]time.Time)
	}
	if a.hookSaved == nil {
		a.hookSaved = make(map[string]time.Time)
	}
	for _, r := range rows {
		if cur, ok := a.hook[r.WorkflowID]; !ok || r.SeenAt.After(cur) {
			a.hook[r.WorkflowID] = r.SeenAt
		}
		a.hookSaved[r.WorkflowID] = r.SeenAt
	}
	return nil
}
