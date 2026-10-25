package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/asyncmode"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// ModeInfo 是某个工作流此刻的异步对话状态：用户的开关、有效模式的判定、选用的档位。
type ModeInfo struct {
	WorkflowID string
	Row        model.WorkflowMode // 没有行时只有 WorkflowID 有值
	State      asyncmode.State
	HookSeen   time.Time // 最近一次见到该工作流的 hook；零值表示没见过（落库节流，重启后恢复，见 RestoreHookSeen）
	// Profile 只在开启过异步的工作流上解析（回执里的 auth: 行要用）；
	// 没开启过的工作流不付这次查询的代价，也不需要它——界面要展示默认档位时自己取。
	Profile *model.AuthProfile
}

// Signature 是「智能体该知道的模式状态」的签名（模式、降级原因、实际生效的档位及其内容）；从未开启过为空串。
func (m ModeInfo) Signature() string {
	if !m.State.Ever || m.Profile == nil {
		return ""
	}
	return asyncmode.Signature(m.State, m.Profile.ID, asyncmode.Profile{Name: m.Profile.Name, Level: m.Profile.Level, Rules: m.Profile.Rules})
}

// WorkflowModeInfo 读取并判定某个工作流的异步状态。热路径（每条回执都会问一次）：
// 没开启过异步的工作流只多一次主键查询。
func (s *Store) WorkflowModeInfo(ctx context.Context, workflowID string, now time.Time) (ModeInfo, error) {
	wf := strings.TrimSpace(workflowID)
	info := ModeInfo{WorkflowID: wf, Row: model.WorkflowMode{WorkflowID: wf}}
	if wf == "" {
		return info, nil
	}
	var row model.WorkflowMode
	err := s.db.WithContext(ctx).Where("workflow_id = ?", wf).Take(&row).Error
	switch {
	case err == nil:
		info.Row = row
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return info, err
	}
	if at, ok := s.HookLastSeen(wf); ok {
		info.HookSeen = at
	}
	info.State = asyncmode.Evaluate(asyncmode.Input{
		Enabled: info.Row.AsyncEnabled, Ever: info.Row.EverEnabled, HookSeen: info.HookSeen, Now: now,
	})
	if info.State.Ever {
		p, err := s.ResolveAuthProfile(ctx, info.Row.ProfileID)
		if err != nil {
			return info, err
		}
		info.Profile = p
	}
	return info, nil
}

// IsAsyncEffective 回答「这个工作流此刻是不是有效的异步模式」。出错一律按同步处理（同步是安全的默认）。
func (s *Store) IsAsyncEffective(ctx context.Context, workflowID string, now time.Time) bool {
	info, err := s.WorkflowModeInfo(ctx, workflowID, now)
	return err == nil && info.State.Effective
}

// ModePatch 是修改工作流异步状态的入参：只改非 nil 的字段。
type ModePatch struct {
	Enabled   *bool
	ProfileID *uint // 0 表示跟随默认档位
	// StrictWrapup 开关「严格收尾门」（#232 D2）：true = 开（默认），false = 关、回到老口径。
	StrictWrapup *bool
}

// UpdateWorkflowMode 开关异步对话 / 选档位。
//
// 开启需要「环境允许」：这个工作流至少被服务端见过一次 hook（即使现在已经过期——过期只会让它降级，
// 用户可以先开着，等 hook 回来自动生效）。从没见过 hook 说明编辑器里根本没装，此时开启没有意义，
// 返回冲突并让界面引导安装。关闭永远允许。
func (s *Store) UpdateWorkflowMode(ctx context.Context, workflowID string, patch ModePatch, now time.Time) (ModeInfo, error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return ModeInfo{}, NewInvalidInputError("workflow_id 不能为空")
	}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var row model.WorkflowMode
		err := tx.Where("workflow_id = ?", wf).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = model.WorkflowMode{WorkflowID: wf}
		} else if err != nil {
			return err
		}
		if patch.ProfileID != nil {
			if *patch.ProfileID != 0 {
				var n int64
				if err := tx.Model(&model.AuthProfile{}).Where("id = ?", *patch.ProfileID).Count(&n).Error; err != nil {
					return err
				}
				if n == 0 {
					return NewNotFoundError("授权档位不存在")
				}
			}
			row.ProfileID = *patch.ProfileID
		}
		if patch.StrictWrapup != nil {
			if off := !*patch.StrictWrapup; row.StrictWrapupOff != off {
				row.StrictWrapupOff = off
				row.GateReleased = false // 口径一变，上一轮的「本次放行」作废
			}
		}
		if patch.Enabled != nil {
			switch {
			case *patch.Enabled && !row.AsyncEnabled:
				if _, seen := s.HookLastSeen(wf); !seen {
					return NewConflictError("还没有收到这个工作流的编辑器 hook 事件，不能开启异步对话：先安装 hook（芯片弹层里有安装命令），让智能体调用一次工具后再试", 0)
				}
				row.AsyncEnabled = true
				row.EverEnabled = true
				t := now
				row.EnabledAt = &t
			case !*patch.Enabled:
				row.AsyncEnabled = false
			}
		}
		row.UpdatedAt = now
		return tx.Save(&row).Error
	})
	if err != nil {
		return ModeInfo{}, err
	}
	return s.WorkflowModeInfo(ctx, wf, now)
}

// MarkModeTold 记下「已经把当前模式状态告诉了智能体」（签名见 asyncmode.Signature）。
func (s *Store) MarkModeTold(ctx context.Context, workflowID, sig string) error {
	return s.db.WithContext(ctx).Model(&model.WorkflowMode{}).
		Where("workflow_id = ?", strings.TrimSpace(workflowID)).
		Update("told_sig", sig).Error
}

// ---- 回复窗口（#123）----

// AsyncWindowTimes 返回全局设置里的「每条新消息加多久」与「单次计时器最长等多久」（没设过用默认值）。
func (s *Store) AsyncWindowTimes(ctx context.Context) (add, slice time.Duration) {
	add, slice = asyncmode.DefaultWindowAdd, asyncmode.DefaultWaitSlice
	if gs, err := s.GetGlobalAppSettings(ctx); err == nil && gs != nil {
		if gs.AsyncWindowAddMinutes > 0 {
			add = time.Duration(gs.AsyncWindowAddMinutes) * time.Minute
		}
		if gs.AsyncWaitSliceMinutes > 0 {
			slice = time.Duration(gs.AsyncWaitSliceMinutes) * time.Minute
		}
	}
	return add, slice
}

// ReplyWindow 是回复窗口此刻的状态（已按任务情况对齐过）。
type ReplyWindow = asyncmode.WindowState

// activeTaskWindow 一个任务多久没动静就不再算「手头有事」：长期挂在「验证中」等观察、或在等用户回复的任务，
// 不该把回复窗口永远钉住。
const activeTaskWindow = 3 * time.Hour

// activeTaskCount 进行中 + 验证中、且最近 3 小时内有过更新的任务数：智能体手头「有事」的度量。
func (s *Store) activeTaskCount(ctx context.Context, workflowID string) int {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.WorkItem{}).
		Where("workflow_id = ? AND state IN ? AND updated_at >= ?", workflowID,
			[]string{model.WorkStateDoing, model.WorkStateReview}, time.Now().Add(-activeTaskWindow)).
		Count(&n).Error
	if err != nil {
		return 0
	}
	return int(n)
}

// ReconcileReplyWindow 读出窗口并按任务情况对齐：有任务在做就把走动中的窗口暂停（剩余攒进 BankSec），
// 任务清零后再从攒着的时间接着走。changed 表示这次对齐改动了数据（调用方据此广播）。
// 「有任务在做」收尾门开着时按门口径数（本会话名下未完成、不按时间截断），门关着时按老口径（见 windowLoad）。
func (s *Store) ReconcileReplyWindow(ctx context.Context, workflowID string, now time.Time) (w ReplyWindow, changed bool, err error) {
	w, _, changed, err = s.reconcileReplyWindow(ctx, workflowID, now)
	return w, changed, err
}

// reconcileReplyWindow 同 ReconcileReplyWindow，另外把读到的行也交出去（累加窗口时要用其中的收尾门设置）。
func (s *Store) reconcileReplyWindow(ctx context.Context, workflowID string, now time.Time) (w ReplyWindow, row model.WorkflowMode, changed bool, err error) {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return w, row, false, nil
	}
	if err := s.db.WithContext(ctx).Where("workflow_id = ?", wf).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return w, model.WorkflowMode{}, false, nil
		}
		return w, model.WorkflowMode{}, false, err
	}
	w = ReplyWindow{Until: row.ReplyWindowUntil, BankSec: row.ReplyWindowBankSec}
	if w.Until == nil && w.BankSec <= 0 {
		return w, row, false, nil
	}
	s.windowLoad(ctx, wf, row).apply(&w)
	switch {
	case w.ActiveTasks > 0 && w.Until != nil:
		left := int(w.Until.Sub(now) / time.Second)
		if left < 0 {
			left = 0
		}
		w.BankSec, w.Until, changed = left, nil, true
	case w.ActiveTasks == 0 && w.Until == nil && w.BankSec > 0:
		next := now.Add(time.Duration(w.BankSec) * time.Second)
		w.Until, w.BankSec, changed = &next, 0, true
	}
	if changed {
		err = s.db.WithContext(ctx).Model(&model.WorkflowMode{}).Where("workflow_id = ?", wf).
			Updates(map[string]any{"reply_window_until": w.Until, "reply_window_bank_sec": w.BankSec}).Error
	}
	return w, row, changed, err
}

// ExtendReplyWindow 把工作流的回复窗口累加 add（封顶见 asyncmode.WindowMax）。
// 智能体手头有任务时只往「攒着的时间」里加（窗口不走）；没有任务时从当前截止时间累加。
// 只更新已有的工作流行：没有行说明从未开启过异步，没有窗口可言。
func (s *Store) ExtendReplyWindow(ctx context.Context, workflowID string, add time.Duration, now time.Time) (ReplyWindow, error) {
	wf := strings.TrimSpace(workflowID)
	var out ReplyWindow
	if wf == "" {
		return out, nil
	}
	// 先对齐，再在对齐后的状态上累加
	cur, row0, _, err := s.reconcileReplyWindow(ctx, wf, now)
	if err != nil {
		return out, err
	}
	if row0.WorkflowID == "" {
		return out, nil
	}
	// 窗口从空（或已用完）重新起算：上一轮的「本次放行」到此为止，这一轮收尾门照常生效。
	// 任务负载要在事务外算（事务里再走 s.db 查询会和事务抢同一条连接而死锁），所以先在这里定好。
	fresh := (cur.Until == nil && cur.BankSec <= 0) || (cur.Until != nil && !cur.Until.After(now))
	resetRelease := fresh && row0.GateReleased
	if resetRelease {
		row0.GateReleased = false
	}
	load := s.windowLoad(ctx, wf, row0)
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var row model.WorkflowMode
		if err := tx.Where("workflow_id = ?", wf).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		upd := map[string]any{}
		if resetRelease {
			upd["gate_released"] = false
		}
		if load.Active > 0 {
			bank := time.Duration(cur.BankSec)*time.Second + add
			if bank > asyncmode.WindowMax {
				bank = asyncmode.WindowMax
			}
			upd["reply_window_bank_sec"] = int(bank / time.Second)
			upd["reply_window_until"] = nil
			out = ReplyWindow{BankSec: int(bank / time.Second)}
		} else {
			next := asyncmode.ExtendWindow(row.ReplyWindowUntil, now, add)
			upd["reply_window_until"] = next
			upd["reply_window_bank_sec"] = 0
			out = ReplyWindow{Until: &next}
		}
		load.apply(&out)
		return tx.Model(&model.WorkflowMode{}).Where("workflow_id = ?", wf).Updates(upd).Error
	})
	return out, err
}

// ClearReplyWindow 用户回复了：窗口清零（走动中的和攒着的都清），上一轮的「本次放行」也一并复位。没有窗口时什么都不做。
func (s *Store) ClearReplyWindow(ctx context.Context, workflowID string) error {
	wf := strings.TrimSpace(workflowID)
	if wf == "" {
		return nil
	}
	return s.db.WithContext(ctx).Model(&model.WorkflowMode{}).
		Where("workflow_id = ? AND (reply_window_until IS NOT NULL OR reply_window_bank_sec > 0 OR gate_released = ?)", wf, true).
		Updates(map[string]any{"reply_window_until": nil, "reply_window_bank_sec": 0, "gate_released": false}).Error
}

// typingBeat 记每个工作流上一次「正在输入」心跳的时间，用来把心跳间隔补进窗口（等于暂停倒计时）。
var typingBeat = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// TypingFreshness 超过这个间隔没有新的心跳就算停手了。
const TypingFreshness = 10 * time.Second

// BeatReplyWindow 记一次输入心跳：窗口走动中时，把距上一次心跳的间隔（不超过新鲜期）加进窗口，等于暂停倒计时。
// 暂停中（有任务）的窗口本来就不走，心跳不改动它。
func (s *Store) BeatReplyWindow(ctx context.Context, workflowID string, now time.Time) (ReplyWindow, error) {
	wf := strings.TrimSpace(workflowID)
	typingBeat.Lock()
	last, had := typingBeat.at[wf]
	typingBeat.at[wf] = now
	typingBeat.Unlock()
	cur, _, err := s.ReconcileReplyWindow(ctx, wf, now)
	if err != nil || cur.Until == nil {
		return cur, err
	}
	if had && now.Sub(last) > 0 && now.Sub(last) <= TypingFreshness {
		return s.ExtendReplyWindow(ctx, wf, now.Sub(last), now)
	}
	return cur, nil
}
