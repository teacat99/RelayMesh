package notify

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

const (
	conv1to1  = "wf:demo"
	convGroup = "grp:g1"
)

func prefsOf(conv string, pol Policy, dnd bool) Prefs {
	p := Prefs{DND: dnd, NtfyPreview: PreviewAuto}
	if pol != "" {
		p.Conversations = map[string]ConvPref{conv: {Policy: pol}}
	}
	return p
}

func evOf(kind Kind, conv string, mention bool) Event {
	return Event{ID: "e1", Kind: kind, Conversation: conv, Mention: mention, CreatedAt: t0}
}

// 受约束类（agent_text / ask_card / run_end）的期望，逐条手写：会话策略 × 免打扰 × 是否点名。
// 点名只对 agent_text 有意义（升格为 mention，穿透）；ask_card 与 run_end 忽略它。
var restrictedMatrix = []struct {
	policy  Policy
	dnd     bool
	mention bool
	reason  Reason
}{
	{PolicyAll, false, false, ReasonDelivered},
	{PolicyAll, false, true, ReasonPenetrate},
	{PolicyAll, true, false, ReasonDND},
	{PolicyAll, true, true, ReasonPenetrate},
	{PolicyMentions, false, false, ReasonMentionsOnly},
	{PolicyMentions, false, true, ReasonPenetrate},
	{PolicyMentions, true, false, ReasonDND}, // 免打扰先于会话策略
	{PolicyMentions, true, true, ReasonPenetrate},
	{PolicyMuted, false, false, ReasonMuted},
	{PolicyMuted, false, true, ReasonPenetrate},
	{PolicyMuted, true, false, ReasonDND},
	{PolicyMuted, true, true, ReasonPenetrate},
}

type decisionRow struct {
	name    string
	ev      Event
	prefs   Prefs
	browser bool
	ntfy    bool
	reason  Reason
}

func decisionTable() []decisionRow {
	var rows []decisionRow

	// agent_text：浏览器 + ntfy 两个通道，受约束。
	for _, m := range restrictedMatrix {
		pass := m.reason == ReasonDelivered || m.reason == ReasonPenetrate
		rows = append(rows, decisionRow{
			name:    fmt.Sprintf("agent_text/%s/dnd=%v/mention=%v", m.policy, m.dnd, m.mention),
			ev:      evOf(KindAgentText, conv1to1, m.mention),
			prefs:   prefsOf(conv1to1, m.policy, m.dnd),
			browser: pass, ntfy: pass, reason: m.reason,
		})
	}
	// ask_card：同 agent_text，但点名不起作用。
	for _, m := range restrictedMatrix {
		if m.mention {
			continue
		}
		pass := m.reason == ReasonDelivered
		rows = append(rows, decisionRow{
			name:    fmt.Sprintf("ask_card/%s/dnd=%v", m.policy, m.dnd),
			ev:      evOf(KindAskCard, conv1to1, false),
			prefs:   prefsOf(conv1to1, m.policy, m.dnd),
			browser: pass, ntfy: pass, reason: m.reason,
		})
	}
	// run_end：只有 ntfy 通道。
	for _, m := range restrictedMatrix {
		if m.mention {
			continue
		}
		pass := m.reason == ReasonDelivered
		rows = append(rows, decisionRow{
			name:    fmt.Sprintf("run_end/%s/dnd=%v", m.policy, m.dnd),
			ev:      evOf(KindRunEnd, conv1to1, false),
			prefs:   prefsOf(conv1to1, m.policy, m.dnd),
			browser: false, ntfy: pass, reason: m.reason,
		})
	}

	// 穿透类：任何会话策略与免打扰组合下都到达两个通道。
	for _, k := range []Kind{KindBlockingFeedback, KindMention, KindApprovalWaiting, KindGuardAlert} {
		for _, pol := range []Policy{PolicyAll, PolicyMentions, PolicyMuted} {
			for _, dnd := range []bool{false, true} {
				rows = append(rows, decisionRow{
					name:    fmt.Sprintf("%s/%s/dnd=%v", k, pol, dnd),
					ev:      evOf(k, conv1to1, false),
					prefs:   prefsOf(conv1to1, pol, dnd),
					browser: true, ntfy: true, reason: ReasonPenetrate,
				})
			}
		}
	}

	// 永不通知：任务更新与系统行。
	for _, k := range []Kind{KindTaskUpdate, KindSystemEvent} {
		for _, pol := range []Policy{PolicyAll, PolicyMentions, PolicyMuted} {
			for _, dnd := range []bool{false, true} {
				rows = append(rows, decisionRow{
					name:   fmt.Sprintf("%s/%s/dnd=%v", k, pol, dnd),
					ev:     evOf(k, conv1to1, false),
					prefs:  prefsOf(conv1to1, pol, dnd),
					reason: ReasonNever,
				})
			}
		}
	}

	// 未登记的种类：保守地不通知。
	rows = append(rows,
		decisionRow{name: "unknown/empty", ev: evOf("", conv1to1, false), prefs: prefsOf(conv1to1, "", false), reason: ReasonUnknownKind},
		decisionRow{name: "unknown/bogus", ev: evOf("bogus", conv1to1, true), prefs: prefsOf(conv1to1, PolicyAll, false), reason: ReasonUnknownKind},
	)

	// 默认策略与会话级设置的细节。
	soon := t0.Add(time.Hour)
	past := t0.Add(-time.Minute)
	rows = append(rows,
		decisionRow{name: "default/1to1-all", ev: evOf(KindAgentText, conv1to1, false), prefs: prefsOf(conv1to1, "", false), browser: true, ntfy: true, reason: ReasonDelivered},
		decisionRow{name: "default/group-mentions", ev: evOf(KindAgentText, convGroup, false), prefs: prefsOf(convGroup, "", false), reason: ReasonMentionsOnly},
		decisionRow{name: "group/explicit-all", ev: evOf(KindAgentText, convGroup, false), prefs: prefsOf(convGroup, PolicyAll, false), browser: true, ntfy: true, reason: ReasonDelivered},
		decisionRow{name: "group/explicit-muted", ev: evOf(KindAskCard, convGroup, false), prefs: prefsOf(convGroup, PolicyMuted, false), reason: ReasonMuted},
		decisionRow{name: "group/mention-penetrates-default", ev: evOf(KindAgentText, convGroup, true), prefs: prefsOf(convGroup, "", true), browser: true, ntfy: true, reason: ReasonPenetrate},
		decisionRow{name: "mute/active-until-future", ev: evOf(KindAgentText, conv1to1, false),
			prefs:  Prefs{Conversations: map[string]ConvPref{conv1to1: {Policy: PolicyMuted, MutedUntil: &soon}}},
			reason: ReasonMuted},
		decisionRow{name: "mute/expired-falls-back-to-default", ev: evOf(KindAgentText, conv1to1, false),
			prefs:   Prefs{Conversations: map[string]ConvPref{conv1to1: {Policy: PolicyMuted, MutedUntil: &past}}},
			browser: true, ntfy: true, reason: ReasonDelivered},
		decisionRow{name: "mute/boundary-now-is-expired", ev: evOf(KindAgentText, conv1to1, false),
			prefs:   Prefs{Conversations: map[string]ConvPref{conv1to1: {Policy: PolicyMuted, MutedUntil: &t0}}},
			browser: true, ntfy: true, reason: ReasonDelivered},
		decisionRow{name: "other-conversation-unaffected", ev: evOf(KindAgentText, "wf:other", false), prefs: prefsOf(conv1to1, PolicyMuted, false), browser: true, ntfy: true, reason: ReasonDelivered},
	)
	return rows
}

func TestDecideTable(t *testing.T) {
	rows := decisionTable()
	if len(rows) < 60 {
		t.Fatalf("决策表只有 %d 条，设计要求至少 60 条", len(rows))
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			d := Decide(r.ev, r.prefs, t0)
			if d.Browser != r.browser || d.Ntfy != r.ntfy || d.Reason != r.reason {
				t.Fatalf("得到 browser=%v ntfy=%v reason=%s，期望 browser=%v ntfy=%v reason=%s",
					d.Browser, d.Ntfy, d.Reason, r.browser, r.ntfy, r.reason)
			}
			if d.Delivered() != (r.browser || r.ntfy) {
				t.Fatalf("Delivered() 与通道不一致：%+v", d)
			}
			// 浏览器通道才有提示音与角标。
			if d.Sound != d.Browser || d.Badge != d.Browser {
				t.Fatalf("sound/badge 应与 browser 同步：%+v", d)
			}
			// 穿透标记只在穿透裁决里出现。
			if d.Penetrate != (r.reason == ReasonPenetrate) {
				t.Fatalf("penetrate 标记与原因不一致：%+v", d)
			}
		})
	}
}

func TestDecideMentionPromotesAgentText(t *testing.T) {
	d := Decide(evOf(KindAgentText, convGroup, true), prefsOf(convGroup, PolicyMuted, true), t0)
	if d.Kind != KindMention || !d.Penetrate || !d.Browser || !d.Ntfy {
		t.Fatalf("点名的 agent_text 应升格为 mention 并穿透：%+v", d)
	}
	// 没点名则保持原种类。
	d = Decide(evOf(KindAgentText, convGroup, false), prefsOf(convGroup, PolicyAll, false), t0)
	if d.Kind != KindAgentText {
		t.Fatalf("未点名不应升格：%+v", d)
	}
	// 点名对其他种类不起作用。
	d = Decide(evOf(KindAskCard, conv1to1, true), prefsOf(conv1to1, PolicyMuted, false), t0)
	if d.Kind != KindAskCard || d.Delivered() {
		t.Fatalf("ask_card 不受点名影响：%+v", d)
	}
}

// 决策性质：对所有组合成立，与逐条表格互为补充。
func TestDecideProperties(t *testing.T) {
	kinds := []Kind{
		KindBlockingFeedback, KindMention, KindApprovalWaiting, KindGuardAlert,
		KindAgentText, KindAskCard, KindRunEnd, KindTaskUpdate, KindSystemEvent, "bogus", "",
	}
	convs := []string{conv1to1, convGroup, "wf:x"}
	policies := []Policy{"", PolicyAll, PolicyMentions, PolicyMuted}
	rank := map[Policy]int{PolicyMuted: 0, PolicyMentions: 1, PolicyAll: 2}

	for _, k := range kinds {
		for _, conv := range convs {
			for _, mention := range []bool{false, true} {
				ev := evOf(k, conv, mention)
				for _, pol := range policies {
					off := Decide(ev, prefsOf(conv, pol, false), t0)
					on := Decide(ev, prefsOf(conv, pol, true), t0)

					// 1. 穿透类（含升格后的 mention）在任何偏好下都到达两个通道。
					if EffectiveKind(ev).Penetrates() {
						for _, d := range []Decision{off, on} {
							if !d.Browser || !d.Ntfy || !d.Penetrate {
								t.Fatalf("穿透类被吞：kind=%s conv=%s pol=%q d=%+v", k, conv, pol, d)
							}
						}
					}
					// 2. 永不通知的种类与未登记的种类，任何偏好下都不通知。
					if !EffectiveKind(ev).Known() || k == KindTaskUpdate || k == KindSystemEvent {
						if off.Delivered() || on.Delivered() {
							t.Fatalf("不该通知的种类被通知：kind=%s off=%+v on=%+v", k, off, on)
						}
					}
					// 3. 免打扰只会让结果更安静，不会更吵。
					if on.Delivered() && !off.Delivered() {
						t.Fatalf("免打扰让通知变多了：kind=%s conv=%s pol=%q", k, conv, pol)
					}
				}
				// 4. 会话策略的放行集合：muted ⊆ mentions ⊆ all。
				for _, dnd := range []bool{false, true} {
					prev := -1
					for _, pol := range []Policy{PolicyMuted, PolicyMentions, PolicyAll} {
						d := Decide(ev, prefsOf(conv, pol, dnd), t0)
						cur := 0
						if d.Delivered() {
							cur = 1
						}
						if cur < prev {
							t.Fatalf("策略放行集合不单调：kind=%s conv=%s dnd=%v pol=%s（rank %d）", k, conv, dnd, pol, rank[pol])
						}
						prev = cur
					}
				}
			}
		}
	}
}

func TestDecidePreviewFlag(t *testing.T) {
	cases := []struct {
		mode  PreviewMode
		cross bool
		want  bool
	}{
		{PreviewAuto, false, true},
		{PreviewAuto, true, false},
		{PreviewAlways, false, true},
		{PreviewAlways, true, true},
		{PreviewNever, false, false},
		{PreviewNever, true, false},
		{"", false, true}, // 零值按 auto 处理
		{"", true, false},
	}
	for _, c := range cases {
		ev := evOf(KindAgentText, conv1to1, false)
		ev.CrossProject = c.cross
		got := Decide(ev, Prefs{NtfyPreview: c.mode}, t0).Preview
		if got != c.want {
			t.Errorf("mode=%q cross=%v：preview=%v，期望 %v", c.mode, c.cross, got, c.want)
		}
	}
}

func TestKindSpecs(t *testing.T) {
	for _, k := range []Kind{KindBlockingFeedback, KindMention, KindApprovalWaiting, KindGuardAlert} {
		if !k.Penetrates() {
			t.Errorf("%s 应是穿透类", k)
		}
	}
	for _, k := range []Kind{KindAgentText, KindAskCard, KindRunEnd, KindTaskUpdate, KindSystemEvent, "bogus"} {
		if k.Penetrates() {
			t.Errorf("%s 不应是穿透类", k)
		}
	}
	if Kind("bogus").Known() || !KindRunEnd.Known() {
		t.Error("Known 判断有误")
	}
}
