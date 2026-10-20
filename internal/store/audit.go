package store

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 审计事件（#114，设计稿 06 §7.6）。只记「谁、在哪个范围、做了什么、结果如何」，从不记录文档正文与聊天正文。

const (
	// AuditRetention 是审计保留时长（180 天）。
	AuditRetention = 180 * 24 * time.Hour
	// AuditCrossReadDedupe 是跨界读取的去重窗口：同一主体 + 范围 30 分钟内只记一条。
	AuditCrossReadDedupe = 30 * time.Minute

	auditPruneEvery = time.Hour
	auditPageMax    = 500
)

var auditLastPrune atomic.Int64

// AuditInput 是要记的一条审计。
type AuditInput struct {
	ProjectID  uint
	ActorKind  string
	ActorRef   string
	ActorLabel string
	Action     string
	ScopeKind  string
	ScopeRef   string
	Target     string
	Result     string // 空 = allow
	Detail     any    // 序列化成 JSON；不得含正文
	// Dedupe > 0 时：窗口内已有同主体、同动作、同范围、同结果的记录就不再写。
	Dedupe time.Duration
	// DedupeExact 为 true 时，去重还要求目标与详情也完全相同：只有「同一个目标、同一个原因」的重复才合并，
	// 不同的原因 / 不同的目标各记各的（项目管理 Agent 运行预设被拒绝时，用户要看清每一种原因，而不是只看到第一条）。
	DedupeExact bool
}

// AddAuditEvent 写一条审计；返回是否真的写入（被去重则为 false）。
func (s *Store) AddAuditEvent(ctx context.Context, in AuditInput) (bool, error) {
	now := time.Now()
	result := in.Result
	if result == "" {
		result = model.AuditAllow
	}
	ev := model.AuditEvent{
		At: now, ProjectID: in.ProjectID,
		ActorKind: strings.TrimSpace(in.ActorKind), ActorRef: strings.TrimSpace(in.ActorRef), ActorLabel: truncRunes(in.ActorLabel, 200),
		Action: in.Action, ScopeKind: in.ScopeKind, ScopeRef: truncRunes(in.ScopeRef, 256), Target: truncRunes(in.Target, 512), Result: result,
	}
	if ev.ActorKind == "" {
		ev.ActorKind = "system"
	}
	if ev.ActorRef == "" {
		ev.ActorRef = ev.ActorKind
	}
	if in.Detail != nil {
		if b, err := json.Marshal(in.Detail); err == nil {
			ev.Detail = string(b)
		}
	}
	if in.Dedupe > 0 {
		var n int64
		q := s.db.WithContext(ctx).Model(&model.AuditEvent{}).
			Where("at > ? AND actor_ref = ? AND action = ? AND scope_kind = ? AND scope_ref = ? AND result = ?",
				now.Add(-in.Dedupe), ev.ActorRef, ev.Action, ev.ScopeKind, ev.ScopeRef, ev.Result)
		if in.DedupeExact {
			q = q.Where("target = ? AND detail = ?", ev.Target, ev.Detail)
		}
		if err := q.Count(&n).Error; err != nil {
			return false, err
		}
		if n > 0 {
			return false, nil
		}
	}
	if err := s.db.WithContext(ctx).Create(&ev).Error; err != nil {
		return false, err
	}
	s.maybePruneAudit(ctx, now)
	return true, nil
}

// maybePruneAudit 每小时最多清理一次过期的审计。
func (s *Store) maybePruneAudit(ctx context.Context, now time.Time) {
	last := auditLastPrune.Load()
	if now.UnixNano()-last < int64(auditPruneEvery) || !auditLastPrune.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	_ = s.db.WithContext(ctx).Where("at < ?", now.Add(-AuditRetention)).Delete(&model.AuditEvent{}).Error
}

// AuditFilter 审计筛选。
type AuditFilter struct {
	ProjectID uint
	ActorRef  string
	Action    string // 精确，或以 . 结尾的前缀（如 doc.）
	Result    string
	ScopeRef  string // 前缀
	Since     time.Time
	Until     time.Time
	Limit     int
	BeforeID  uint // 翻页：只取 id 小于它的
}

// ListAuditEvents 按时间倒序返回（新的在前）。
func (s *Store) ListAuditEvents(ctx context.Context, f AuditFilter) ([]model.AuditEvent, error) {
	q := s.db.WithContext(ctx).Model(&model.AuditEvent{})
	if f.ProjectID > 0 {
		q = q.Where("project_id = ?", f.ProjectID)
	}
	if v := strings.TrimSpace(f.ActorRef); v != "" {
		q = q.Where("actor_ref = ?", v)
	}
	if v := strings.TrimSpace(f.Action); v != "" {
		if strings.HasSuffix(v, ".") {
			q = q.Where("action LIKE ?", v+"%")
		} else {
			q = q.Where("action = ?", v)
		}
	}
	if v := strings.TrimSpace(f.Result); v != "" {
		q = q.Where("result = ?", v)
	}
	if v := strings.TrimSpace(f.ScopeRef); v != "" {
		q = q.Where(`scope_ref LIKE ? ESCAPE '\'`, escapeLike(v)+"%")
	}
	if !f.Since.IsZero() {
		q = q.Where("at >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		q = q.Where("at <= ?", f.Until)
	}
	if f.BeforeID > 0 {
		q = q.Where("id < ?", f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 || limit > auditPageMax {
		limit = 100
	}
	var out []model.AuditEvent
	return out, q.Order("id DESC").Limit(limit).Find(&out).Error
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
