package oauth

import (
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestDCRModeOf(t *testing.T) {
	for name, tc := range map[string]struct {
		p    model.OAuthPolicy
		want string
	}{
		"关闭自动注册":         {model.OAuthPolicy{AllowDCR: false}, DCRModeOff},
		"关闭时 always 无意义": {model.OAuthPolicy{AllowDCR: false, DCRAlways: true}, DCRModeOff},
		"默认策略是限时窗口":      {model.DefaultOAuthPolicy(), DCRModeWindow},
		"常开":             {model.OAuthPolicy{AllowDCR: true, DCRAlways: true}, DCRModeAlways},
	} {
		if got := DCRModeOf(tc.p); got != tc.want {
			t.Errorf("%s：DCRModeOf = %q，期望 %q", name, got, tc.want)
		}
	}
	// 升级兼容：2.0.0 存下的策略没有 dcr_always 字段，读出来是 false，且 allow_dcr 为 true，
	// 于是自然落到「限时窗口」——从常开收紧到限时窗口是这次改动有意为之（见 CHANGELOG 升级须知）。
	if got := DCRModeOf(model.OAuthPolicy{AllowDCR: true}); got != DCRModeWindow {
		t.Errorf("旧策略（只有 allow_dcr）应当落到限时窗口，实际 %q", got)
	}
}

func TestDCRWindowOpenExtendClose(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var w dcrWindow

	if open, until := w.state(t0); open || !until.IsZero() {
		t.Fatalf("零值应当是关闭，得到 open=%v until=%v", open, until)
	}
	if w.close(t0) {
		t.Fatalf("没开着的窗口 close 应当返回 false")
	}

	// 打开：没给时长（≤0）用默认 10 分钟。
	if until := w.open(t0, 0); !until.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("默认时长应是 10 分钟，到期 %v", until)
	}
	if open, _ := w.state(t0.Add(9*time.Minute + 59*time.Second)); !open {
		t.Fatalf("到期之前应当开着")
	}
	if open, until := w.state(t0.Add(10 * time.Minute)); open || !until.IsZero() {
		t.Fatalf("到期那一刻应当已经关闭，得到 open=%v until=%v", open, until)
	}

	// 再次「开启」是重置，不是累加：剩余 9 分钟时开 10 分钟，只会变成从现在起 10 分钟。
	w.open(t0, 10*time.Minute)
	now := t0.Add(1 * time.Minute)
	if until := w.open(now, 10*time.Minute); !until.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("开启应当重置为从现在起 10 分钟，得到 %v", until)
	}

	// 续期：在剩余时间上累加。
	w.open(t0, 10*time.Minute)
	until, ok := w.extend(t0.Add(2*time.Minute), 10*time.Minute)
	if !ok || !until.Equal(t0.Add(20*time.Minute)) {
		t.Fatalf("续期应当在剩余时间上累加到 20 分钟，得到 ok=%v until=%v", ok, until)
	}
	// 续期不超过「从现在起 60 分钟」。
	for i := 0; i < 10; i++ {
		until, _ = w.extend(t0.Add(2*time.Minute), 30*time.Minute)
	}
	if want := t0.Add(2*time.Minute + MaxDCRWindow); !until.Equal(want) {
		t.Fatalf("总剩余应当封顶在 60 分钟，期望 %v 得到 %v", want, until)
	}
	// 窗口没开着时续期失败（调用方改走 open）。
	var closed dcrWindow
	if _, ok := closed.extend(t0, 10*time.Minute); ok {
		t.Fatalf("没开着的窗口不能续期")
	}

	// 过长的时长在打开时就截到上限。
	if until := w.open(t0, 5*time.Hour); !until.Equal(t0.Add(MaxDCRWindow)) {
		t.Fatalf("过长的时长应当截到 60 分钟，得到 %v", until)
	}

	// 关闭：返回此前是否开着，之后是关闭。
	if !w.close(t0.Add(time.Minute)) {
		t.Fatalf("开着的窗口 close 应当返回 true")
	}
	if open, _ := w.state(t0.Add(time.Minute)); open {
		t.Fatalf("close 之后应当关闭")
	}
}

func TestBuildDCRStatus(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	until := now.Add(7*time.Minute + 30*time.Second)
	window := model.DefaultOAuthPolicy()
	always := model.OAuthPolicy{AllowDCR: true, DCRAlways: true}
	off := model.OAuthPolicy{AllowDCR: false}

	t.Run("授权服务器没在服务：任何方式都不受理", func(t *testing.T) {
		for _, p := range []model.OAuthPolicy{window, always, off} {
			st := BuildDCRStatus(p, now, until, false)
			if st.Open || st.Until != nil || st.RemainingSeconds != 0 {
				t.Fatalf("没在服务时不应受理：%+v", st)
			}
		}
	})
	t.Run("限时窗口开着", func(t *testing.T) {
		st := BuildDCRStatus(window, now, until, true)
		if st.Mode != DCRModeWindow || !st.Open || st.Until == nil || !st.Until.Equal(until) || st.RemainingSeconds != 450 {
			t.Fatalf("窗口开着的状态不对：%+v", st)
		}
	})
	t.Run("限时窗口没开 / 已过期", func(t *testing.T) {
		for _, u := range []time.Time{{}, now, now.Add(-time.Second)} {
			st := BuildDCRStatus(window, now, u, true)
			if st.Mode != DCRModeWindow || st.Open || st.Until != nil || st.RemainingSeconds != 0 {
				t.Fatalf("窗口没开着时的状态不对（until=%v）：%+v", u, st)
			}
		}
	})
	t.Run("常开：随时受理，不带窗口时间", func(t *testing.T) {
		st := BuildDCRStatus(always, now, time.Time{}, true)
		if st.Mode != DCRModeAlways || !st.Open || st.Until != nil {
			t.Fatalf("常开状态不对：%+v", st)
		}
	})
	t.Run("关闭：即使窗口还开着也不受理", func(t *testing.T) {
		st := BuildDCRStatus(off, now, until, true)
		if st.Mode != DCRModeOff || st.Open || st.Until != nil {
			t.Fatalf("关闭状态不对：%+v", st)
		}
	})
	t.Run("默认时长与上限随状态返回，页面不再写死", func(t *testing.T) {
		st := BuildDCRStatus(window, now, time.Time{}, true)
		if st.DefaultMinutes != 10 || st.MaxMinutes != 60 {
			t.Fatalf("默认 / 上限分钟数不对：%+v", st)
		}
	})
}
