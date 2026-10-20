package notify

import "time"

// Reason 说明一次决策为什么是这个结果，供测试断言与排查（不会展示给用户）。
type Reason string

const (
	ReasonPenetrate    Reason = "penetrate"     // 穿透类，直接通知
	ReasonDelivered    Reason = "delivered"     // 受约束类，偏好放行
	ReasonNever        Reason = "never"         // 永不通知的种类
	ReasonUnknownKind  Reason = "unknown_kind"  // 未登记的种类：不通知
	ReasonDND          Reason = "dnd"           // 被免打扰挡下
	ReasonMuted        Reason = "muted"         // 会话静音
	ReasonMentionsOnly Reason = "mentions_only" // 会话只在被 @ 时通知，而这条没有 @
)

// Decision 是闸对一条事件的裁决。
type Decision struct {
	Kind      Kind   `json:"kind"`      // 实际生效的种类（agent_text 且点名了你时升格为 mention）
	Browser   bool   `json:"browser"`   // 发 SSE notify 给浏览器
	Ntfy      bool   `json:"ntfy"`      // 交给 ntfy 推送器
	Sound     bool   `json:"sound"`     // 浏览器可以响提示音（还要看本机开关）
	Badge     bool   `json:"badge"`     // 计入标题角标的未读数
	Penetrate bool   `json:"penetrate"` // 穿透类：浏览器侧也不做前台抑制
	Preview   bool   `json:"preview"`   // ntfy 新消息类报文能带预览正文
	Reason    Reason `json:"reason"`
}

// Delivered 报告至少有一个通道要通知。
func (d Decision) Delivered() bool { return d.Browser || d.Ntfy }

// Decide 是闸里唯一的决策函数（纯函数）。顺序即设计稿 §5：
//
//	① 种类是否永不通知 / 是否穿透类 → 穿透类直接通知，不看任何偏好；
//	② 否则看免打扰；
//	③ 再看会话级策略（muted / mentions 抑制，all 放行）。
//
// 前台抑制与合并限速在两条通道各自的下游（浏览器 notifyBus、ntfy 聚合），不在这里。
// Mention=true 的 agent_text 视同 mention（点名你，穿透）；其余种类忽略 Mention。
func Decide(ev Event, p Prefs, now time.Time) Decision {
	kind := EffectiveKind(ev)
	spec, ok := kindSpecs[kind]
	d := Decision{Kind: kind}
	switch {
	case !ok:
		d.Reason = ReasonUnknownKind
		return d
	case spec.never:
		d.Reason = ReasonNever
		return d
	case spec.penetrate:
		return deliver(d, spec, ev, p, ReasonPenetrate)
	}
	if p.DND {
		d.Reason = ReasonDND
		return d
	}
	switch p.PolicyFor(ev.Conversation, now) {
	case PolicyMuted:
		d.Reason = ReasonMuted
		return d
	case PolicyMentions:
		d.Reason = ReasonMentionsOnly
		return d
	}
	return deliver(d, spec, ev, p, ReasonDelivered)
}

// EffectiveKind 返回事件实际生效的种类：点名了你的 agent_text 升格为 mention。
func EffectiveKind(ev Event) Kind {
	if ev.Kind == KindAgentText && ev.Mention {
		return KindMention
	}
	return ev.Kind
}

func deliver(d Decision, spec kindSpec, ev Event, p Prefs, reason Reason) Decision {
	d.Browser = spec.browser
	d.Ntfy = spec.ntfy
	d.Sound = spec.browser
	d.Badge = spec.browser
	d.Penetrate = spec.penetrate
	d.Preview = p.ShowPreview(ev)
	d.Reason = reason
	return d
}
