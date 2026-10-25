package api

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 分支请示的超时升级（#236 P2）：路由给主会话的请示超过时限（全局偏好 escalate_minutes，默认 30 分钟）仍没人答复，
// 自动转交给用户，免得主会话不在线 / 在睡觉时分支一直卡着。
//
// 后台每分钟扫一遍（范式同 board_remind.go：启动后先等一个「分钟」、停止不打断进行中的一遍、panic 兜住）。
// 幂等靠库里的条件更新（只选 addressee 非空且仍待处理的），重启不丢、不重复；升级后按新请示通知用户（走通知闸，免打扰与会话静音照常生效）。

const (
	// branchEscalateMinTick 是扫描间隔的下限：实验里把「一分钟」缩得很短时，也不让它每毫秒查一次库。
	branchEscalateMinTick = 20 * time.Millisecond
	// branchEscalatePerScan 是一遍扫描最多升级的条数（超出的留给下一遍，防止一次性刷屏）。
	branchEscalatePerScan = 50
)

// branchEscalator 是超时升级的发现端。
type branchEscalator struct {
	store *store.Store
	// unit 是「一分钟」有多长：生产是 time.Minute；实验与端到端测试经 RELAYMESH_BRANCH_ESCALATE_UNIT_MS 缩短，
	// 时限（分钟数）与扫描间隔都按它换算，所以不必真等几十分钟。
	unit time.Duration
	now  func() time.Time
	// onEscalated 拿到一遍扫描里被升级的请示，负责刷新界面与通知用户。
	onEscalated func(items []model.ConfirmItem)

	lifeMu sync.Mutex
	cancel context.CancelFunc
}

// newBranchEscalator 接线；unitMs <= 0 表示用真实的一分钟。
func newBranchEscalator(st *store.Store, unitMs int, onEscalated func(items []model.ConfirmItem)) *branchEscalator {
	unit := time.Minute
	if unitMs > 0 {
		unit = time.Duration(unitMs) * time.Millisecond
	}
	return &branchEscalator{store: st, unit: unit, now: time.Now, onEscalated: onEscalated}
}

// start 启动后台扫描；重复调用无副作用。启动后先等一个「分钟」再扫第一遍，不和启动期的迁移、重连抢资源。
func (r *branchEscalator) start() {
	if r == nil || r.store == nil {
		return
	}
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()
	if r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	tick := max(r.unit, branchEscalateMinTick)
	go func() {
		timer := time.NewTimer(tick)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			r.safeScan(ctx)
			timer.Reset(tick)
		}
	}()
}

func (r *branchEscalator) stop() {
	if r == nil {
		return
	}
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

// safeScan 保证扫描里任何意外都不会带走进程或这个协程；这一遍沿用「不随停止取消」的上下文（理由同 boardReminder.safeScan：
// 被取消的写事务会连带丢掉数据库连接），只受 20 秒超时约束。
func (r *branchEscalator) safeScan(parent context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[branch] 请示超时升级的扫描发生内部错误：%v", rec)
		}
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 20*time.Second)
	defer cancel()
	r.scan(ctx)
}

// scan 扫一遍，返回这一遍升级的条数。
func (r *branchEscalator) scan(ctx context.Context) int {
	prefs, err := r.store.GetBranchPrefs(ctx)
	if err != nil {
		return 0
	}
	minutes := prefs.EscalateAfterMinutes()
	note := fmt.Sprintf("主会话 %d 分钟没有答复，自动转交给你", minutes)
	items, err := r.store.EscalateOverdueBranchAsks(ctx, r.now(), time.Duration(minutes)*r.unit, note, branchEscalatePerScan)
	if err != nil {
		log.Printf("[branch] 请示超时升级失败：%v", err)
		return 0
	}
	if len(items) > 0 {
		log.Printf("[branch] 有 %d 项请示超过 %d 分钟没有主会话答复，已转交给用户", len(items), minutes)
		if r.onEscalated != nil {
			r.onEscalated(items)
		}
	}
	return len(items)
}
