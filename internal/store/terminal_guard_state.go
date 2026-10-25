package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 预设的持久运行状态：非交互运行异常退出后自动续跑的登记（#151）。
// 它只记运行编号、次数与时间，不记任何内容。单独一个键，不与预设本身混在一起：预设是人配的，这份是机器记的。
//
// 它原先也记会话守护（D-214）的「上一次推送了哪次事件」「唤醒过几次」「唤醒许可」；守护在 #245 第二期下线，这些字段不再读写，
// 旧文档里残留的字段在下一次写入时就被丢掉（JSON 解码忽略不认识的字段，所以回滚到旧版本也读得出）。
// 类型与存储键里的「Guard」是历史命名：存储键 terminal_guard_state 不能改（数据在库里）。

// TerminalGuardStateKey 是守护状态在 system_settings 表里的键，值是 JSON（见 GuardStateDoc）。
const TerminalGuardStateKey = "terminal_guard_state"

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

// GuardPresetState 是一个预设的持久运行状态。
type GuardPresetState struct {
	// AutoResumes 是「非交互运行异常退出后自动续跑」的登记（按登记先后，新的在后）。
	AutoResumes []AutoResumeRecord `json:"auto_resumes,omitempty"`
}

func (p *GuardPresetState) empty() bool {
	return p == nil || len(p.AutoResumes) == 0
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

// ClaimAutoResumeRun 把「已开跑、却还没写上运行编号」的那条登记认领给 runID（#187）：占名额、提交运行、写聊天行、写运行编号是分开的几步，
// 运行很快就结束（或这几步被负载拖慢）时，结束的处理会先到，按编号找不到登记。结束的处理拿运行说明末尾「自动续跑 n/m」里的次数 attempt 来认领：
// 找最近一条已开跑、编号为空、次数相同的登记。提交方随后写入的是同一个编号，所以认领与写入的先后不影响结果。
// 返回是否有改动：已经有登记带着这个编号、找不到合适的登记（不是续跑出来的、编号已经写上、次数对不上），都返回 false 且什么也不改。
func (p *GuardPresetState) ClaimAutoResumeRun(runID string, attempt int) bool {
	if p == nil || runID == "" || attempt <= 0 {
		return false
	}
	for _, r := range p.AutoResumes {
		if r.RunID == runID {
			return false
		}
	}
	for i := len(p.AutoResumes) - 1; i >= 0; i-- {
		r := &p.AutoResumes[i]
		if r.State != AutoResumeStarted || r.RunID != "" || r.Attempt != attempt {
			continue
		}
		r.RunID = runID
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

// GuardStateDoc 是整份预设运行状态：预设编号 → 状态。
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

// normalize 清理过期与超量的自动续跑登记，丢掉空条目：文档始终只有「正在用的预设」那么大。
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

// GetTerminalGuardState 读取预设运行状态；从未保存过或存档损坏时返回空文档（不是 nil）。
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

// UpdateTerminalGuardState 在一个事务里「读—改—写」预设运行状态，并清理过期记录；返回写入后的文档。
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
