package agent

import (
	"testing"
	"time"
)

func mustWindow(t *testing.T, s string) AutoWindow {
	t.Helper()
	w, err := ParseAutoWindow(s)
	if err != nil {
		t.Fatalf("ParseAutoWindow(%q): %v", s, err)
	}
	return w
}

func clock(h, m, s int) time.Time { return time.Date(2026, 10, 18, h, m, s, 0, time.UTC) }

func TestParseAutoWindow(t *testing.T) {
	for in, want := range map[string]string{
		"":            "",
		"03:00-05:00": "03:00-05:00",
		"00:00-23:59": "00:00-23:59",
		"23:00-02:00": "23:00-02:00",
	} {
		w := mustWindow(t, in)
		if got := w.String(); got != want {
			t.Errorf("%q 还原成 %q，期望 %q", in, got, want)
		}
		if w.Always() != (in == "") {
			t.Errorf("%q 的 Always() = %v", in, w.Always())
		}
	}
	for _, bad := range []string{"3:00-5:00", "03:00", "03:00-03:00", "24:00-01:00", "03:61-05:00", "03:00-05:00 ", "nonsense", "03:00–05:00"} {
		if _, err := ParseAutoWindow(bad); err == nil {
			t.Errorf("%q 应当被拒绝", bad)
		}
	}
}

func TestAutoWindowContainsIncludesStartExcludesEnd(t *testing.T) {
	w := mustWindow(t, "03:00-05:00")
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{clock(2, 59, 59), false},
		{clock(3, 0, 0), true},
		{clock(4, 59, 59), true},
		{clock(5, 0, 0), false},
		{clock(12, 0, 0), false},
		{clock(0, 0, 0), false},
	} {
		if got := w.Contains(c.at); got != c.want {
			t.Errorf("%s: Contains = %v，期望 %v", c.at.Format("15:04:05"), got, c.want)
		}
	}
}

func TestAutoWindowCrossingMidnight(t *testing.T) {
	w := mustWindow(t, "23:00-02:00")
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{clock(22, 59, 59), false},
		{clock(23, 0, 0), true},
		{clock(23, 59, 59), true},
		{clock(0, 0, 0), true},
		{clock(1, 59, 59), true},
		{clock(2, 0, 0), false},
		{clock(12, 0, 0), false},
	} {
		if got := w.Contains(c.at); got != c.want {
			t.Errorf("%s: Contains = %v，期望 %v", c.at.Format("15:04:05"), got, c.want)
		}
	}
}

func TestAutoWindowUnlimitedIsAlwaysOpen(t *testing.T) {
	w := mustWindow(t, "")
	for _, at := range []time.Time{clock(0, 0, 0), clock(12, 34, 56), clock(23, 59, 59)} {
		if !w.Contains(at) || w.UntilOpen(at) != 0 {
			t.Errorf("%s: 不限的时间段应当一直开着", at.Format("15:04:05"))
		}
	}
}

func TestAutoWindowUntilOpen(t *testing.T) {
	w := mustWindow(t, "03:00-05:00")
	for _, c := range []struct {
		name string
		at   time.Time
		want time.Duration
	}{
		{"已经在里面", clock(3, 30, 0), 0},
		{"正好是起点", clock(3, 0, 0), 0},
		{"差一个小时", clock(2, 0, 0), time.Hour},
		{"精确到秒", clock(2, 59, 30), 30 * time.Second},
		{"刚过终点：等到明天", clock(5, 0, 0), 22 * time.Hour},
		{"下午：等到明天凌晨", clock(15, 30, 0), 11*time.Hour + 30*time.Minute},
		{"午夜之后", clock(0, 0, 0), 3 * time.Hour},
	} {
		if got := w.UntilOpen(c.at); got != c.want {
			t.Errorf("%s（%s）: UntilOpen = %v，期望 %v", c.name, c.at.Format("15:04:05"), got, c.want)
		}
	}

	cross := mustWindow(t, "23:00-02:00")
	for _, c := range []struct {
		at   time.Time
		want time.Duration
	}{
		{clock(12, 0, 0), 11 * time.Hour},
		{clock(2, 0, 0), 21 * time.Hour},
		{clock(23, 30, 0), 0},
		{clock(1, 0, 0), 0},
	} {
		if got := cross.UntilOpen(c.at); got != c.want {
			t.Errorf("跨午夜 %s: UntilOpen = %v，期望 %v", c.at.Format("15:04:05"), got, c.want)
		}
	}
}

// UntilOpen 睡醒之后再判断：算出来的间隔一过，时间段必须真的开着（否则会反复睡一小段）。
func TestAutoWindowIsOpenAfterSleepingUntilOpen(t *testing.T) {
	for _, s := range []string{"03:00-05:00", "23:00-02:00", "00:00-00:01", "12:30-12:31"} {
		w := mustWindow(t, s)
		for h := 0; h < 24; h++ {
			for _, m := range []int{0, 1, 29, 30, 59} {
				at := clock(h, m, 17)
				d := w.UntilOpen(at)
				if d == 0 {
					if !w.Contains(at) {
						t.Errorf("%s %s: UntilOpen 为 0 但不在时间段内", s, at.Format("15:04:05"))
					}
					continue
				}
				if !w.Contains(at.Add(d)) {
					t.Errorf("%s %s: 睡 %v 之后（%s）时间段仍未开始", s, at.Format("15:04:05"), d, at.Add(d).Format("15:04:05"))
				}
				if w.Contains(at.Add(d - time.Second)) {
					t.Errorf("%s %s: 睡 %v 之前一秒时间段就已经开了，间隔算长了", s, at.Format("15:04:05"), d)
				}
			}
		}
	}
}

// 时钟读数取自传入时间自己的时区：本机时区不是 UTC 时，时间段按本地墙上的时间算。
func TestAutoWindowUsesTheClockOfTheGivenZone(t *testing.T) {
	w := mustWindow(t, "03:00-05:00")
	shanghai := time.FixedZone("CST", 8*3600)
	at := time.Date(2026, 10, 18, 4, 0, 0, 0, shanghai) // UTC 20:00，本地 04:00
	if !w.Contains(at) {
		t.Error("本地 04:00 应在 03:00-05:00 内")
	}
	if w.Contains(at.UTC()) {
		t.Error("同一时刻换成 UTC 读数（20:00）就不在时间段内：时间段只看传入时间自己的时钟")
	}
}
