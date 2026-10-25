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

// 「指派久无人接」的阈值范围（#180 B3d）：指派派出后超过这么久还没人接取，就算「久无人接」——
// 回执头 board: 行、任务页上「已等多久」转红、以及给用户的一次性提醒都按它算。
const (
	BoardStaleDefaultMinutes = 120
	BoardStaleMinMinutes     = 30
	BoardStaleMaxMinutes     = 24 * 60
)

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

// Prefs 是全部通知偏好。零值就是「出厂默认」：免打扰关、预览 auto、没有任何会话级设置、
// 「指派久无人接」提醒开且阈值 2 小时。
type Prefs struct {
	DND         bool        `json:"dnd"`
	NtfyPreview PreviewMode `json:"ntfy_preview"`
	// BoardStaleOff 为真表示不再发「指派久无人接」的提醒（网页与 ntfy 都不发；阈值本身仍然生效）。
	// 取反存放，是为了让「没有这个字段的旧偏好」自然落在出厂默认（提醒开）上。
	BoardStaleOff bool `json:"board_stale_off,omitempty"`
	// BoardStaleMinutes 是「久无人接」的阈值（分钟，30~1440）；0 = 没设置过，按默认 120。
	// 存「用户选的值」而不是「生效的值」，这样以后调整默认值时，没改过的人跟着走，不会冻结在旧值上。
	BoardStaleMinutes int                 `json:"board_stale_minutes,omitempty"`
	Conversations     map[string]ConvPref `json:"conversations,omitempty"`
}

// ValidBoardStaleMinutes 报告用户提交的阈值是否可以接受：0（恢复默认）或 30~1440 分钟。
func ValidBoardStaleMinutes(m int) bool {
	return m == 0 || (m >= BoardStaleMinMinutes && m <= BoardStaleMaxMinutes)
}

// clampBoardStale 把存下来的阈值收进允许的范围：没设置过（0）或是负数的当作没设置，其余越界的收到边界上
// （手写进库里的 5 分钟按 30 算，99999 按 1440 算），免得一个坏值让回执头与提醒的口径跑偏。
func clampBoardStale(m int) int {
	switch {
	case m <= 0:
		return 0
	case m < BoardStaleMinMinutes:
		return BoardStaleMinMinutes
	case m > BoardStaleMaxMinutes:
		return BoardStaleMaxMinutes
	}
	return m
}

// BoardStaleAfterMinutes 返回生效的「久无人接」阈值（分钟）：没设置过的按默认。
func (p Prefs) BoardStaleAfterMinutes() int {
	if m := clampBoardStale(p.BoardStaleMinutes); m > 0 {
		return m
	}
	return BoardStaleDefaultMinutes
}

// BoardStaleAfter 返回生效的「久无人接」阈值。
func (p Prefs) BoardStaleAfter() time.Duration {
	return time.Duration(p.BoardStaleAfterMinutes()) * time.Minute
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
	out := Prefs{DND: p.DND, NtfyPreview: p.NtfyPreview, BoardStaleOff: p.BoardStaleOff, BoardStaleMinutes: clampBoardStale(p.BoardStaleMinutes)}
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
