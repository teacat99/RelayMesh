package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidConversation(t *testing.T) {
	ok := []string{"wf:demo", "wf:relaymesh-continuation", "grp:g1", "wf:中文项目", "wf:a b"}
	bad := []string{"", "wf:", "wf:   ", "grp:", "demo", "xx:demo", "wf:a\nb", "wf:a\x00b", "wf:" + strings.Repeat("x", 201)}
	for _, id := range ok {
		if !ValidConversation(id) {
			t.Errorf("%q 应合法", id)
		}
	}
	for _, id := range bad {
		if ValidConversation(id) {
			t.Errorf("%q 应非法", id)
		}
	}
	// 边界：标识恰好 200 字节合法。
	if !ValidConversation("wf:" + strings.Repeat("x", 200)) {
		t.Error("200 字节的标识应合法")
	}
}

func TestDefaultPolicy(t *testing.T) {
	if DefaultPolicy("wf:a") != PolicyAll || DefaultPolicy("grp:a") != PolicyMentions || DefaultPolicy("") != PolicyAll {
		t.Fatal("默认策略应是：1:1 全部、群只看 @")
	}
}

func TestPolicyForExpiry(t *testing.T) {
	future, past := t0.Add(time.Hour), t0.Add(-time.Hour)
	p := Prefs{Conversations: map[string]ConvPref{
		"wf:future":  {Policy: PolicyMuted, MutedUntil: &future},
		"wf:past":    {Policy: PolicyMuted, MutedUntil: &past},
		"wf:now":     {Policy: PolicyMuted, MutedUntil: &t0},
		"wf:forever": {Policy: PolicyMuted},
		"grp:g":      {Policy: PolicyAll},
		"wf:bad":     {Policy: "weird"},
	}}
	cases := map[string]Policy{
		"wf:future":  PolicyMuted,
		"wf:past":    PolicyAll,
		"wf:now":     PolicyAll,
		"wf:forever": PolicyMuted,
		"grp:g":      PolicyAll,
		"wf:bad":     PolicyAll, // 未知策略忽略，回到默认
		"wf:none":    PolicyAll,
		"grp:none":   PolicyMentions,
	}
	for conv, want := range cases {
		if got := p.PolicyFor(conv, t0); got != want {
			t.Errorf("%s：得到 %s，期望 %s", conv, got, want)
		}
	}
}

func TestPrefsNormalize(t *testing.T) {
	future, past := t0.Add(time.Hour), t0.Add(-time.Hour)
	in := Prefs{
		DND:         true,
		NtfyPreview: "weird",
		Conversations: map[string]ConvPref{
			"wf:keep-muted":   {Policy: PolicyMuted},
			"wf:keep-timed":   {Policy: PolicyMuted, MutedUntil: &future},
			"wf:expired":      {Policy: PolicyMuted, MutedUntil: &past},
			"wf:redundant":    {Policy: PolicyAll},                           // 与默认相同
			"grp:redundant":   {Policy: PolicyMentions},                      // 与默认相同
			"grp:keep-all":    {Policy: PolicyAll},                           // 与群默认不同
			"wf:until-on-all": {Policy: PolicyMentions, MutedUntil: &future}, // 非 muted 不带期限
			"bad-id":          {Policy: PolicyMuted},
			"wf:bad-policy":   {Policy: "weird"},
		},
	}
	got := in.Normalize(t0)
	if !got.DND || got.NtfyPreview != PreviewAuto {
		t.Fatalf("DND 应保留、非法预览模式应回到 auto：%+v", got)
	}
	want := map[string]Policy{
		"wf:keep-muted":   PolicyMuted,
		"wf:keep-timed":   PolicyMuted,
		"grp:keep-all":    PolicyAll,
		"wf:until-on-all": PolicyMentions,
	}
	if len(got.Conversations) != len(want) {
		t.Fatalf("规整后应保留 %d 条，得到 %d：%+v", len(want), len(got.Conversations), got.Conversations)
	}
	for id, pol := range want {
		if got.Conversations[id].Policy != pol {
			t.Errorf("%s：得到 %q，期望 %q", id, got.Conversations[id].Policy, pol)
		}
	}
	if got.Conversations["wf:until-on-all"].MutedUntil != nil {
		t.Error("非 muted 策略不应带期限")
	}
	if got.Conversations["wf:keep-timed"].MutedUntil == nil || !got.Conversations["wf:keep-timed"].MutedUntil.Equal(future) {
		t.Error("有效的静音期限应保留")
	}
	// 不改动输入：期限指针与映射都不能被共享写入。
	if len(in.Conversations) != 9 {
		t.Error("Normalize 不应改动接收者的映射")
	}
	*got.Conversations["wf:keep-timed"].MutedUntil = past
	if !in.Conversations["wf:keep-timed"].MutedUntil.Equal(future) {
		t.Error("期限应是副本，不能与输入共享指针")
	}
}

func TestPrefsNormalizeZeroValue(t *testing.T) {
	got := Prefs{}.Normalize(t0)
	if got.DND || got.NtfyPreview != PreviewAuto || got.Conversations != nil {
		t.Fatalf("零值应规整为出厂默认：%+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPrefsValidate(t *testing.T) {
	if err := (Prefs{NtfyPreview: "weird"}).Validate(); err == nil {
		t.Error("非法预览模式应被拒绝")
	}
	p := Prefs{NtfyPreview: PreviewAuto, Conversations: map[string]ConvPref{}}
	for i := 0; i < MaxConversations; i++ {
		p.Conversations["wf:"+strings.Repeat("a", 3)+itoa(i)] = ConvPref{Policy: PolicyMuted}
	}
	if err := p.Validate(); err != nil {
		t.Errorf("恰好 %d 条应通过：%v", MaxConversations, err)
	}
	p.Conversations["wf:one-more"] = ConvPref{Policy: PolicyMuted}
	if err := p.Validate(); err == nil {
		t.Error("超过上限应被拒绝")
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestPrefsJSONRoundTrip(t *testing.T) {
	future := t0.Add(time.Hour)
	in := Prefs{DND: true, NtfyPreview: PreviewNever, Conversations: map[string]ConvPref{
		"wf:a":  {Policy: PolicyMuted, MutedUntil: &future},
		"grp:b": {Policy: PolicyAll},
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Prefs
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.DND || out.NtfyPreview != PreviewNever || len(out.Conversations) != 2 ||
		out.Conversations["wf:a"].MutedUntil == nil || !out.Conversations["wf:a"].MutedUntil.Equal(future) ||
		out.Conversations["grp:b"].Policy != PolicyAll {
		t.Fatalf("往返后内容不一致：%+v", out)
	}
	// 没有会话设置时不输出 conversations 字段，保持 JSON 很小。
	raw, _ = json.Marshal(Prefs{NtfyPreview: PreviewAuto})
	if strings.Contains(string(raw), "conversations") {
		t.Errorf("空的会话设置不应输出：%s", raw)
	}
}
