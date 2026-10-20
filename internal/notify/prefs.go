package notify

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Policy 是某个会话的通知策略。
type Policy string

const (
	PolicyAll      Policy = "all"      // 全部通知（1:1 默认）
	PolicyMentions Policy = "mentions" // 只在被 @ 时通知（群默认）
	PolicyMuted    Policy = "muted"    // 静音：只有穿透类能到达
)

// Valid 报告 p 是不是已登记的策略。
func (p Policy) Valid() bool {
	return p == PolicyAll || p == PolicyMentions || p == PolicyMuted
}

// PreviewMode 决定 ntfy 新消息类的推送里要不要带预览正文。
type PreviewMode string

const (
	PreviewAuto   PreviewMode = "auto"   // 默认：1:1 显示，跨项目群隐藏
	PreviewAlways PreviewMode = "always" // 一律显示
	PreviewNever  PreviewMode = "never"  // 一律隐藏，只写「有新消息」
)

// Valid 报告 m 是不是已登记的模式。
func (m PreviewMode) Valid() bool {
	return m == PreviewAuto || m == PreviewAlways || m == PreviewNever
}

const (
	// MaxConversations 是会话级偏好条目的上限：偏好存在一个 JSON 值里，不能无限长大。
	MaxConversations = 500

	conversationPrefixWorkflow = "wf:"
	conversationPrefixGroup    = "grp:"
	maxConversationKeyBytes    = 200 // 前缀之后的标识部分
)

// ErrInvalidPrefs 表示偏好内容不合法（数值越界、策略未知等）。
var ErrInvalidPrefs = errors.New("通知偏好无效")

// ConvPref 是一个会话的偏好。MutedUntil 只对 muted 有意义：到点之后静音自动失效，回到默认策略。
type ConvPref struct {
	Policy     Policy     `json:"policy"`
	MutedUntil *time.Time `json:"muted_until,omitempty"`
}

// Prefs 是全部通知偏好。零值就是「出厂默认」：免打扰关、预览 auto、没有任何会话级设置。
type Prefs struct {
	DND           bool                `json:"dnd"`
	NtfyPreview   PreviewMode         `json:"ntfy_preview"`
	Conversations map[string]ConvPref `json:"conversations,omitempty"`
}

// DefaultPolicy 是会话没有显式设置时的策略：群只在被 @ 时通知，其余（1:1）全部通知。
func DefaultPolicy(conversation string) Policy {
	if strings.HasPrefix(conversation, conversationPrefixGroup) {
		return PolicyMentions
	}
	return PolicyAll
}

// ValidConversation 检查会话 id 的形状：wf:<工作流> 或 grp:<群>，标识非空、不含控制字符、长度有限。
func ValidConversation(id string) bool {
	var rest string
	switch {
	case strings.HasPrefix(id, conversationPrefixWorkflow):
		rest = id[len(conversationPrefixWorkflow):]
	case strings.HasPrefix(id, conversationPrefixGroup):
		rest = id[len(conversationPrefixGroup):]
	default:
		return false
	}
	if strings.TrimSpace(rest) == "" || len(rest) > maxConversationKeyBytes {
		return false
	}
	for _, r := range rest {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// PolicyFor 返回会话当前生效的策略：显式设置优先；过期的静音视为没有设置。
func (p Prefs) PolicyFor(conversation string, now time.Time) Policy {
	if cp, ok := p.Conversations[conversation]; ok && cp.Policy.Valid() {
		if cp.Policy == PolicyMuted && cp.MutedUntil != nil && !now.Before(*cp.MutedUntil) {
			return DefaultPolicy(conversation)
		}
		return cp.Policy
	}
	return DefaultPolicy(conversation)
}

// Normalize 返回规整后的副本（不改动接收者）：补默认预览模式；丢掉非法条目、已过期的静音、
// 以及与默认策略相同且没有期限的冗余条目；muted 以外的策略不带期限。
func (p Prefs) Normalize(now time.Time) Prefs {
	out := Prefs{DND: p.DND, NtfyPreview: p.NtfyPreview}
	if !out.NtfyPreview.Valid() {
		out.NtfyPreview = PreviewAuto
	}
	for id, cp := range p.Conversations {
		if !ValidConversation(id) || !cp.Policy.Valid() {
			continue
		}
		if cp.Policy != PolicyMuted {
			cp.MutedUntil = nil
		} else if cp.MutedUntil != nil && !now.Before(*cp.MutedUntil) {
			continue // 到期的静音：回到默认
		}
		if cp.Policy == DefaultPolicy(id) && cp.MutedUntil == nil {
			continue // 与默认相同：不必占位置
		}
		if cp.MutedUntil != nil {
			t := *cp.MutedUntil
			cp.MutedUntil = &t
		}
		if out.Conversations == nil {
			out.Conversations = make(map[string]ConvPref)
		}
		out.Conversations[id] = cp
	}
	return out
}

// Validate 检查（已规整的）偏好是否在允许的范围内。
func (p Prefs) Validate() error {
	if !p.NtfyPreview.Valid() {
		return fmt.Errorf("%w：预览模式只能是 auto / always / never", ErrInvalidPrefs)
	}
	if len(p.Conversations) > MaxConversations {
		return fmt.Errorf("%w：会话级设置最多 %d 个，请先清理不用的", ErrInvalidPrefs, MaxConversations)
	}
	return nil
}

// ShowPreview 报告 ntfy 的新消息类推送里能不能带预览正文。
// 老三类（阻塞待答 / 守护告警 / 运行结束）不看这里，仍由 ntfy 配置里的「附带摘要」决定。
func (p Prefs) ShowPreview(ev Event) bool {
	switch p.NtfyPreview {
	case PreviewAlways:
		return true
	case PreviewNever:
		return false
	default:
		return !ev.CrossProject
	}
}
