package chat

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

func TestCursor_RoundTripAndRejects(t *testing.T) {
	for _, k := range []Key{
		{Ms: 1760000000123, Rank: 0, ID: "sess-ab12cd34"},
		{Ms: 0, Rank: 10, ID: "e:000000000007"},
		{Ms: 1, Rank: 5, ID: "a.b.c"}, // 主键里允许出现分隔符
	} {
		got, err := ParseCursor(k.Encode())
		if err != nil || !got.Equal(k) {
			t.Fatalf("往返失败: %+v -> %+v (%v)", k, got, err)
		}
	}
	for _, bad := range []string{"", "  ", "###", "YQ", "MS4yLg" /* "1.2." 主键为空 */, "LTEuMS54" /* 负毫秒 */} {
		if _, err := ParseCursor(bad); err == nil {
			t.Fatalf("坏游标 %q 应被拒绝", bad)
		}
	}
}

func TestKey_OrderIsMsThenRankThenID(t *testing.T) {
	keys := []Key{
		{5, 2, "b"}, {5, 2, "a"}, {5, 0, "z"}, {4, 9, "z"}, {6, 0, "a"},
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Less(keys[j]) })
	want := []Key{{4, 9, "z"}, {5, 0, "z"}, {5, 2, "a"}, {5, 2, "b"}, {6, 0, "a"}}
	for i := range want {
		if !keys[i].Equal(want[i]) {
			t.Fatalf("排序不对: %+v", keys)
		}
	}
	// 数字主键补零后字典序等于数值序。
	if !(Key{1, 1, numKey(9)}).Less(Key{1, 1, numKey(10)}) {
		t.Fatal("numKey 字典序应等于数值序")
	}
}

type tieFixture struct {
	Order        []string          `json:"order"`
	FrontendKind map[string]string `json:"frontend_kind"`
}

// 与前端共用的同毫秒次序：Go 一侧的来源序必须与 testdata/tie_order.json 完全一致。
func TestTieOrder_MatchesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/tie_order.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx tieFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	got := TieOrder()
	if len(got) != len(fx.Order) {
		t.Fatalf("来源数不一致: %v vs %v", got, fx.Order)
	}
	for i := range got {
		if got[i] != fx.Order[i] {
			t.Fatalf("第 %d 位不一致: %s vs %s", i, got[i], fx.Order[i])
		}
	}
	// 并且 TieOrder 的先后必须与真实使用的 rank 常量一致。
	ranks := map[string]int{
		"report": rankReport, "reply": rankReply, "inbox": rankInbox, "notice": rankNotice,
		"capsule": rankCapsule, "update": rankUpdate, "card": rankCard, "answer": rankAnswer,
		"decision": rankDecision, "guard": rankGuard, "system": rankSystem,
	}
	for i := 1; i < len(got); i++ {
		if ranks[got[i-1]] >= ranks[got[i]] {
			t.Fatalf("rank 常量与 TieOrder 不一致: %s(%d) !< %s(%d)", got[i-1], ranks[got[i-1]], got[i], ranks[got[i]])
		}
	}
}
