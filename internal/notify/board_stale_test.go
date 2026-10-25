package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 「指派久无人接」提醒（#180 B3d）：种类、免打扰、偏好里的两个设置项与阈值的收敛。

func boardStaleEvent() Event {
	return Event{ID: "bst:1", Kind: KindBoardStale, Title: "指派久无人接", Preview: "#12 派给 wf-2，已超过 2 小时没人接", CreatedAt: t0}
}

// 这是「给用户的」受约束类：登记过、不穿透、两个通道都有；免打扰挡下，其余放行。
func TestBoardStaleKindAndDecision(t *testing.T) {
	if !KindBoardStale.Known() || KindBoardStale.Penetrates() {
		t.Fatal("board_stale 应是已登记的、不穿透的种类")
	}
	ev := boardStaleEvent()

	d := Decide(ev, Prefs{NtfyPreview: PreviewAuto}, t0)
	if !d.Browser || !d.Ntfy || !d.Sound || !d.Badge || d.Penetrate || d.Reason != ReasonDelivered || d.Kind != KindBoardStale {
		t.Fatalf("没开免打扰时应两个通道都送达: %+v", d)
	}
	d = Decide(ev, Prefs{DND: true, NtfyPreview: PreviewAuto}, t0)
	if d.Delivered() || d.Reason != ReasonDND {
		t.Fatalf("免打扰应挡下: %+v", d)
	}
	// 没有会话归属：别的会话的静音 / 只看 @ 设置与它无关；点名标记也不起作用。
	p := Prefs{Conversations: map[string]ConvPref{conv1to1: {Policy: PolicyMuted}, convGroup: {Policy: PolicyMuted}}}
	ev.Mention = true
	if d := Decide(ev, p, t0); !d.Browser || !d.Ntfy || d.Reason != ReasonDelivered || d.Kind != KindBoardStale {
		t.Fatalf("会话级静音不该影响它: %+v", d)
	}
}

func TestBoardStaleMinutesBounds(t *testing.T) {
	if BoardStaleMinMinutes != 30 || BoardStaleMaxMinutes != 1440 || BoardStaleDefaultMinutes != 120 {
		t.Fatalf("口径应是 30~1440、默认 120: %d %d %d", BoardStaleMinMinutes, BoardStaleMaxMinutes, BoardStaleDefaultMinutes)
	}
	for _, c := range []struct {
		m    int
		want bool
	}{
		{0, true}, {30, true}, {120, true}, {1440, true},
		{-1, false}, {1, false}, {29, false}, {1441, false}, {100000, false},
	} {
		if got := ValidBoardStaleMinutes(c.m); got != c.want {
			t.Errorf("ValidBoardStaleMinutes(%d) = %v，期望 %v", c.m, got, c.want)
		}
	}
}

// 零值就是出厂默认：提醒开、阈值 2 小时；存下来的是「用户选的值」，生效值另算；越界的存值收进范围。
func TestBoardStalePrefsEffectiveAndNormalize(t *testing.T) {
	var zero Prefs
	if zero.BoardStaleOff || zero.BoardStaleAfterMinutes() != 120 || zero.BoardStaleAfter() != 2*time.Hour {
		t.Fatalf("零值应是默认（开、120 分钟）: %+v %d", zero, zero.BoardStaleAfterMinutes())
	}
	for _, c := range []struct {
		stored, kept, effective int
	}{
		{0, 0, 120},        // 没设置过：跟着默认走
		{-5, 0, 120},       // 负数当没设置
		{5, 30, 30},        // 太小收到下限
		{29, 30, 30},       // 下限之差 1
		{30, 30, 30},       // 下限
		{90, 90, 90},       // 范围内原样
		{1440, 1440, 1440}, // 上限
		{1441, 1440, 1440}, // 上限之差 1
		{99999, 1440, 1440},
	} {
		raw := Prefs{BoardStaleMinutes: c.stored}
		if got := raw.BoardStaleAfterMinutes(); got != c.effective {
			t.Errorf("未规整的存值 %d：生效 %d，期望 %d（读的人不能被坏值带偏）", c.stored, got, c.effective)
		}
		norm := raw.Normalize(t0)
		if norm.BoardStaleMinutes != c.kept || norm.BoardStaleAfterMinutes() != c.effective {
			t.Errorf("规整 %d：存 %d 生效 %d，期望存 %d 生效 %d", c.stored, norm.BoardStaleMinutes, norm.BoardStaleAfterMinutes(), c.kept, c.effective)
		}
		if got := norm.BoardStaleAfter(); got != time.Duration(c.effective)*time.Minute {
			t.Errorf("BoardStaleAfter(%d) = %v", c.stored, got)
		}
	}
	// 关闭开关随规整保留；且不影响阈值。
	off := Prefs{BoardStaleOff: true, BoardStaleMinutes: 60}.Normalize(t0)
	if !off.BoardStaleOff || off.BoardStaleAfterMinutes() != 60 {
		t.Fatalf("规整应保留开关与阈值: %+v", off)
	}
}

// 出厂默认不写进 JSON（老库里没有这两个字段，读出来就是默认）；设置过的能原样往返。
func TestBoardStalePrefsJSON(t *testing.T) {
	raw, err := json.Marshal(Prefs{NtfyPreview: PreviewAuto})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "board_stale") {
		t.Fatalf("默认值不该出现在 JSON 里: %s", raw)
	}
	var old Prefs
	if err := json.Unmarshal([]byte(`{"dnd":true,"ntfy_preview":"auto"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.BoardStaleOff || old.BoardStaleAfterMinutes() != 120 || !old.DND {
		t.Fatalf("没有这两个字段的旧偏好应落在默认上: %+v", old)
	}
	set := Prefs{NtfyPreview: PreviewAuto, BoardStaleOff: true, BoardStaleMinutes: 240}
	raw, _ = json.Marshal(set)
	var back Prefs
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.BoardStaleOff || back.BoardStaleMinutes != 240 || back.BoardStaleAfterMinutes() != 240 {
		t.Fatalf("往返丢了字段: %s → %+v", raw, back)
	}
}
