package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 会话守护（D-214）的持久状态：每个预设「上一次推送了哪次事件」「唤醒过几次」。
// 它只记次数与时间，不记任何内容。单独一个键，不与预设本身混在一起：预设是人配的，这份是机器记的。

// TerminalGuardStateKey 是守护状态在 system_settings 表里的键，值是 JSON（见 GuardStateDoc）。
const TerminalGuardStateKey = "terminal_guard_state"

const (
	// 唤醒记录最多保留这么久、这么多条：它用来算「今天唤醒了几次」和「上次唤醒有没有进展」，不是审计日志。
	GuardWakeKeep    = 7 * 24 * time.Hour
	GuardWakeKeepMax = 20
)

// 一次唤醒之后智能体有没有动静（GuardWakeRecord.Progress）。空 = 还没结论（观察窗口没过完）。
const (
	GuardProgressYes  = "progress" // 唤醒之后智能体有了新的活动
	GuardProgressNone = "none"     // 观察窗口过完了，仍然没有任何新的活动
)

// GuardWakeRecord 是一次唤醒的登记。
type GuardWakeRecord struct {
	At      time.Time `json:"at"`
	Mode    string    `json:"mode"`              // type（输入到存活的会话）| relaunch（会话已结束，重新拉起）
	Reason  string    `json:"reason"`            // 出错中断 / 长时间无活动 / 会话意外结束 / 手动唤醒……
	Episode string    `json:"episode,omitempty"` // 唤醒时判断的事件标识
	// Progress 由守护的周期判断补上：这次唤醒之后智能体有没有新的动静。连续几次「没有」说明唤醒在白费力气。
	Progress string `json:"progress,omitempty"`
}

// GuardWakePermitState 是唤醒许可的持久化（默认 1；标记为 0 表示任务全部完成、结束时不唤醒）。
type GuardWakePermitState struct {
	Permit            int       `json:"permit,omitempty"` // 0 = 已标记；缺省或 1 = 正常守护
	SetAt             time.Time `json:"set_at,omitempty"`
	SetBy             string    `json:"set_by,omitempty"` // agent | human
	Note              string    `json:"note,omitempty"`   // 可选备注，审计不含
	SuppressedEpisode string    `json:"suppressed_episode,omitempty"`
}

// 非交互运行异常退出后自动续跑（#151，D-260）的登记状态（AutoResumeRecord.State）。
const (
	// AutoResumePending：已排期，到点（Due）开跑；到点前可以取消。
	AutoResumePending = "pending"
	// AutoResumeStarted：已经续跑，RunID 是续跑出来的新运行。
	AutoResumeStarted = "started"
	// AutoResumeStopped：你在到点前取消了这次自动续跑。
	AutoResumeStopped = "stopped"
	// AutoResumeSkipped：这次出事不自动续跑，或到点时条件不满足（你已经重新运行了、预设改了……）；Why 写明原因。
	AutoResumeSkipped = "skipped"
	// AutoResumeGaveUp：本该续跑但预算用完、连续没有进展或没法提交，放弃并通知；Why 写明原因。
	AutoResumeGaveUp = "gaveup"
)

const (
	// AutoResumeKeep、AutoResumeKeepMax：登记最多保留这么久、这么多条。它用来算「24 小时内续跑了几次」「这一串续跑有没有进展」，
	// 也让界面能说明上一次出事之后发生了什么；不是审计日志。待开跑的登记不受时间限制（到点没跑成会被标成跳过）。
	AutoResumeKeep    = 7 * 24 * time.Hour
	AutoResumeKeepMax = 30
)

// AutoResumeRecord 是一次「异常退出之后自动续跑」的登记：从排期，到开跑 / 跳过 / 放弃 / 被你取消。
// 它只记运行编号、次数、时间与一句话原因，不含任何输出内容。
type AutoResumeRecord struct {
	// FromRun 是出事的那次运行（它异常退出了）；同一次运行最多一条登记。
	FromRun string `json:"from_run"`
	// Attempt 是这次续跑在这一串里排第几次（1 起）；出事的运行本身是手动运行时从 1 开始。
	Attempt int `json:"attempt"`
	// Class、Code、Reason 是出事的原因：类别（retriable……）、服务端错误码、一句中文。
	Class  string `json:"class,omitempty"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// PlannedAt 是登记（出事被发现）的时间，Due 是排定的开跑时间。
	PlannedAt time.Time `json:"planned_at"`
	Due       time.Time `json:"due,omitempty"`
	State     string    `json:"state"`
	// Why 是跳过 / 放弃 / 取消的原因（一句中文）。
	Why string `json:"why,omitempty"`
	// RunID 是续跑出来的新运行；StartedAt 是它提交的时间。
	RunID     string    `json:"run_id,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	// Quick 在续跑出来的运行又失败时由后一次登记补上：它跑得太短（没有进展，短于「续跑成功」的判定时长）。连续几次 Quick 就熔断。
	Quick bool `json:"quick,omitempty"`
	// Settled 表示这次续跑出来的运行连续跑满了「续跑成功」的判定时长（#172）：这次续跑算成功，
	// 24 小时内累计的续跑次数（预算与退避阶梯）从它之后重新算，它自己和它之前的登记都不再占名额。
	Settled bool `json:"settled,omitempty"`
}

// GuardPresetState 是一个预设的守护状态。
type GuardPresetState struct {
	// NotifiedEpisode 是最近一次推送过的事件标识：同一次事件不重复推送。
	NotifiedEpisode string                `json:"notified_episode,omitempty"`
	NotifiedAt      time.Time             `json:"notified_at,omitempty"`
	Reminders       int                   `json:"reminders,omitempty"` // 同一次事件已经补发过几次提醒
	Wakes           []GuardWakeRecord     `json:"wakes,omitempty"`
	Permit          *GuardWakePermitState `json:"permit,omitempty"`
	// AutoResumes 是「非交互运行异常退出后自动续跑」的登记（按登记先后，新的在后）。
	AutoResumes []AutoResumeRecord `json:"auto_resumes,omitempty"`
}

func (p *GuardPresetState) empty() bool {
	if p == nil {
		return true
	}
	permitEmpty := p.Permit == nil || (p.Permit.Permit == 0 && p.Permit.SetAt.IsZero() && p.Permit.SetBy == "" && p.Permit.Note == "" && p.Permit.SuppressedEpisode == "")
	return p.NotifiedEpisode == "" && p.NotifiedAt.IsZero() && p.Reminders == 0 && len(p.Wakes) == 0 && len(p.AutoResumes) == 0 && permitEmpty
}

// AutoResumeStartedSince 返回 since 之后已经开跑的自动续跑次数（滚动窗口里的预算用量）。
// 成功的续跑（Settled，见 SettleAutoResume）会把计数清零：从最近一次成功的续跑之后才开始数，它自己和它之前的都不算。
func (p *GuardPresetState) AutoResumeStartedSince(since time.Time) int {
	if p == nil {
		return 0
	}
	floor := since
	for _, r := range p.AutoResumes {
		if r.State == AutoResumeStarted && r.Settled && r.StartedAt.After(floor) {
			floor = r.StartedAt
		}
	}
	n := 0
	for _, r := range p.AutoResumes {
		if r.State == AutoResumeStarted && r.StartedAt.After(floor) {
			n++
		}
	}
	return n
}

// SettleAutoResume 认定「开跑出了 runID 这次运行」的那次续跑成功（调用方已确认它连续跑满了判定时长）：
// 标上 Settled、去掉 Quick，并让预算用量从它之后重新算。返回是否有改动：runID 不是自动续跑出来的、
// 那条登记不是「已开跑」状态、或者早就认定过，都返回 false（幂等，调用方据此只写一次聊天行）。
func (p *GuardPresetState) SettleAutoResume(runID string) bool {
	if p == nil || runID == "" {
		return false
	}
	for i := len(p.AutoResumes) - 1; i >= 0; i-- {
		r := &p.AutoResumes[i]
		if r.RunID != runID {
			continue
		}
		if r.State != AutoResumeStarted || r.Settled {
			return false
		}
		r.Settled, r.Quick = true, false
		return true
	}
	return false
}

// AutoResumeFrom 返回为 fromRun 这次出事登记的自动续跑；没有时 ok 为 false。
func (p *GuardPresetState) AutoResumeFrom(fromRun string) (AutoResumeRecord, bool) {
	if p == nil || fromRun == "" {
		return AutoResumeRecord{}, false
	}
	for i := len(p.AutoResumes) - 1; i >= 0; i-- {
		if p.AutoResumes[i].FromRun == fromRun {
			return p.AutoResumes[i], true
		}
	}
	return AutoResumeRecord{}, false
}

// AutoResumeStarting 返回那条「开跑出了 runID 这次运行」的登记；runID 不是自动续跑出来的就 ok 为 false。
func (p *GuardPresetState) AutoResumeStarting(runID string) (AutoResumeRecord, bool) {
	if p == nil || runID == "" {
		return AutoResumeRecord{}, false
	}
	for i := len(p.AutoResumes) - 1; i >= 0; i-- {
		if p.AutoResumes[i].RunID == runID {
			return p.AutoResumes[i], true
		}
	}
	return AutoResumeRecord{}, false
}

// AutoResumeTrailingQuick 返回沿着「续跑出来的运行又出事」这条链，最近连续有几次续跑「没有进展」（Quick）。
// 链断了（中间被你手动运行过、或某次续跑被跳过）就停：那之后重新算。
func (p *GuardPresetState) AutoResumeTrailingQuick() int {
	if p == nil {
		return 0
	}
	n, next := 0, ""
	for i := len(p.AutoResumes) - 1; i >= 0; i-- {
		r := p.AutoResumes[i]
		if r.State != AutoResumeStarted || !r.Quick {
			break
		}
		if next != "" && r.RunID != next {
			break // 链断了：这条登记续跑出来的运行不是后一条登记的出事运行
		}
		n, next = n+1, r.FromRun
	}
	return n
}

// WakesSince 返回 since 之后的唤醒次数。
func (p *GuardPresetState) WakesSince(since time.Time) int {
	if p == nil {
		return 0
	}
	n := 0
	for _, w := range p.Wakes {
		if w.At.After(since) {
			n++
		}
	}
	return n
}

// LastWake 返回最近一次唤醒；没有时 ok 为 false。
func (p *GuardPresetState) LastWake() (GuardWakeRecord, bool) {
	if p == nil || len(p.Wakes) == 0 {
		return GuardWakeRecord{}, false
	}
	return p.Wakes[len(p.Wakes)-1], true
}

// TrailingNoProgress 返回最近连续有几次唤醒「过了观察窗口仍没有动静」（遇到有动静的、或还没结论的就停）。
func (p *GuardPresetState) TrailingNoProgress() int {
	if p == nil {
		return 0
	}
	n := 0
	for i := len(p.Wakes) - 1; i >= 0; i-- {
		if p.Wakes[i].Progress != GuardProgressNone {
			break
		}
		n++
	}
	return n
}

// GuardStateDoc 是整份守护状态：预设编号 → 状态。
type GuardStateDoc struct {
	Presets map[string]*GuardPresetState `json:"presets"`
}

// Preset 返回某个预设的状态；没有时返回新建的空状态（并登记进文档，方便调用方直接改）。
func (d *GuardStateDoc) Preset(id string) *GuardPresetState {
	if d.Presets == nil {
		d.Presets = map[string]*GuardPresetState{}
	}
	p := d.Presets[id]
	if p == nil {
		p = &GuardPresetState{}
		d.Presets[id] = p
	}
	return p
}

// normalize 清理过期与超量的唤醒记录，丢掉空条目：文档始终只有「正在用的预设」那么大。
func (d *GuardStateDoc) normalize(now time.Time) {
	if d.Presets == nil {
		d.Presets = map[string]*GuardPresetState{}
		return
	}
	for id, p := range d.Presets {
		if p == nil {
			delete(d.Presets, id)
			continue
		}
		kept := p.Wakes[:0]
		for _, w := range p.Wakes {
			if now.Sub(w.At) <= GuardWakeKeep {
				kept = append(kept, w)
			}
		}
		if len(kept) > GuardWakeKeepMax {
			kept = kept[len(kept)-GuardWakeKeepMax:]
		}
		p.Wakes = kept
		p.AutoResumes = pruneAutoResumes(p.AutoResumes, now)
		if p.empty() {
			delete(d.Presets, id)
		}
	}
}

// pruneAutoResumes 丢掉过期与超量的自动续跑登记：待开跑的一直留着（到点没跑成会被标成跳过），其余的保留 AutoResumeKeep 内最近的 AutoResumeKeepMax 条。
func pruneAutoResumes(in []AutoResumeRecord, now time.Time) []AutoResumeRecord {
	if len(in) == 0 {
		return in
	}
	kept := in[:0]
	for _, r := range in {
		if r.State == AutoResumePending || now.Sub(r.PlannedAt) <= AutoResumeKeep {
			kept = append(kept, r)
		}
	}
	if len(kept) > AutoResumeKeepMax {
		kept = kept[len(kept)-AutoResumeKeepMax:]
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func decodeGuardState(raw string) GuardStateDoc {
	var doc GuardStateDoc
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return GuardStateDoc{Presets: map[string]*GuardPresetState{}}
	}
	if doc.Presets == nil {
		doc.Presets = map[string]*GuardPresetState{}
	}
	return doc
}

// GetTerminalGuardState 读取守护状态；从未保存过或存档损坏时返回空文档（不是 nil）。
func (s *Store) GetTerminalGuardState(ctx context.Context) (GuardStateDoc, error) {
	var rec model.SystemSetting
	err := s.db.WithContext(ctx).Where("key = ?", TerminalGuardStateKey).First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return GuardStateDoc{Presets: map[string]*GuardPresetState{}}, nil
		}
		return GuardStateDoc{}, err
	}
	return decodeGuardState(rec.Value), nil
}

// UpdateTerminalGuardState 在一个事务里「读—改—写」守护状态，并清理过期记录；返回写入后的文档。
func (s *Store) UpdateTerminalGuardState(ctx context.Context, now time.Time, mutate func(*GuardStateDoc)) (GuardStateDoc, error) {
	var out GuardStateDoc
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		doc := GuardStateDoc{Presets: map[string]*GuardPresetState{}}
		var rec model.SystemSetting
		if err := tx.Where("key = ?", TerminalGuardStateKey).First(&rec).Error; err == nil {
			doc = decodeGuardState(rec.Value)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		mutate(&doc)
		doc.normalize(now)
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if err := tx.Save(&model.SystemSetting{Key: TerminalGuardStateKey, Value: string(raw), UpdatedAt: now}).Error; err != nil {
			return err
		}
		out = doc
		return nil
	})
	return out, err
}
