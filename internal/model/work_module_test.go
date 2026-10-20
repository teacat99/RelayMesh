package model

import (
	"slices"
	"testing"
)

func TestSplitModulePath(t *testing.T) {
	cases := map[string][]string{
		"终端":          {"终端"},
		" 终端 / 历史日志 ": {"终端", "历史日志"},
		"终端>历史日志":     {"终端", "历史日志"},
		"终端＞历史日志":     {"终端", "历史日志"},
		"终端／历史日志":     {"终端", "历史日志"},
		"终端›历史日志":     {"终端", "历史日志"},
		"终端→历史日志":     {"终端", "历史日志"},
		"/终端//历史日志/":  {"终端", "历史日志"},
		"":            {},
		" / ":         {},
	}
	for in, want := range cases {
		if got := SplitModulePath(in); !slices.Equal(got, want) {
			t.Errorf("SplitModulePath(%q) = %v，期望 %v", in, got, want)
		}
	}
}

func TestEffectivePercent(t *testing.T) {
	p := func(v int) *int { return &v }
	cases := []struct {
		name        string
		state       string
		percent     *int
		total, done int
		want        int
		from        string
	}{
		{"待办默认 0", WorkStateTodo, nil, 0, 0, 0, "state"},
		{"进行中默认 50", WorkStateDoing, nil, 0, 0, 50, "state"},
		{"验证中默认 90", WorkStateReview, nil, 0, 0, 90, "state"},
		{"搁置默认 0", WorkStateShelved, nil, 0, 0, 0, "state"},
		{"显式值优先于状态", WorkStateDoing, p(20), 0, 0, 20, "explicit"},
		{"显式 0 也算显式", WorkStateDoing, p(0), 0, 0, 0, "explicit"},
		{"显式值越界被夹到范围内", WorkStateDoing, p(250), 0, 0, 100, "explicit"},
		{"显式负值被夹到 0", WorkStateDoing, p(-5), 0, 0, 0, "explicit"},
		{"已完成恒为 100", WorkStateDone, p(10), 0, 0, 100, "state"},
		{"子任务比例", WorkStateDoing, nil, 4, 1, 25, "children"},
		{"子任务比例四舍五入", WorkStateTodo, nil, 3, 2, 67, "children"},
		{"显式值优先于子任务", WorkStateDoing, p(80), 4, 1, 80, "explicit"},
		{"已完成优先于子任务", WorkStateDone, nil, 4, 1, 100, "state"},
	}
	for _, c := range cases {
		got, from := EffectivePercent(c.state, c.percent, c.total, c.done)
		if got != c.want || from != c.from {
			t.Errorf("%s: 得到 %d/%s，期望 %d/%s", c.name, got, from, c.want, c.from)
		}
	}
}

func TestWorkModuleStatsOpen(t *testing.T) {
	s := WorkModuleStats{Todo: 2, Doing: 1, Review: 3, Done: 5, Shelved: 4}
	if s.Open() != 6 {
		t.Fatalf("Open() = %d，期望 6（待办 + 进行中 + 验证中）", s.Open())
	}
}
