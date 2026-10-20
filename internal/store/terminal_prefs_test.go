package store

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestTerminalPrefsDefaultsAndRoundTrip(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	got, err := st.GetTerminalPrefs(ctx)
	if err != nil || got != (TerminalPrefs{}) {
		t.Fatalf("从未设置时应全部关闭: %+v err=%v", got, err)
	}

	// 两个设置项分别保存，互不覆盖（读—改—写）。
	if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.FixedCols = 120 }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.RecordMaxLines = 20000 }); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetTerminalPrefs(ctx)
	if err != nil || got.FixedCols != 120 || got.RecordMaxLines != 20000 {
		t.Fatalf("两项设置应同时保留: %+v err=%v", got, err)
	}

	// 改回 0 即关闭，另一项不受影响。
	if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.FixedCols = 0 }); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetTerminalPrefs(ctx)
	if got.FixedCols != 0 || got.RecordMaxLines != 20000 {
		t.Fatalf("关闭一项不应影响另一项: %+v", got)
	}
}

func TestTerminalPrefsValidationRejectsOutOfRangeAndKeepsOldValue(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.FixedCols = 100 }); err != nil {
		t.Fatal(err)
	}
	bad := []func(*TerminalPrefs){
		func(p *TerminalPrefs) { p.FixedCols = MinFixedCols - 1 },
		func(p *TerminalPrefs) { p.FixedCols = MaxFixedCols + 1 },
		func(p *TerminalPrefs) { p.FixedCols = -1 },
		func(p *TerminalPrefs) { p.RecordMaxLines = MinRecordMaxLines - 1 },
		func(p *TerminalPrefs) { p.RecordMaxLines = MaxRecordMaxLines + 1 },
		func(p *TerminalPrefs) { p.RecordMaxLines = -1 },
	}
	for i, mut := range bad {
		if _, err := st.UpdateTerminalPrefs(ctx, mut); !errors.Is(err, ErrInvalidTerminalPrefs) {
			t.Fatalf("第 %d 个越界取值应被拒绝，得到 %v", i, err)
		}
	}
	got, _ := st.GetTerminalPrefs(ctx)
	if got.FixedCols != 100 || got.RecordMaxLines != 0 {
		t.Fatalf("被拒绝的修改不能改动已保存的值: %+v", got)
	}
	// 边界值本身合法。
	for _, v := range []int{MinFixedCols, MaxFixedCols, 0} {
		if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.FixedCols = v }); err != nil {
			t.Fatalf("固定列数 %d 应合法: %v", v, err)
		}
	}
	for _, v := range []int{MinRecordMaxLines, MaxRecordMaxLines, 0} {
		if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.RecordMaxLines = v }); err != nil {
			t.Fatalf("保留行数 %d 应合法: %v", v, err)
		}
	}
}

// 库里的值被手工改坏或越界时，读出来按「关闭」处理，而不是把坏值交给终端与策略下发。
func TestTerminalPrefsDecodeToleratesGarbage(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	put := func(v string) {
		t.Helper()
		if err := st.WithTx(ctx, func(tx *gorm.DB) error {
			return tx.Save(&model.SystemSetting{Key: TerminalPrefsKey, Value: v}).Error
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("not json at all")
	if got, err := st.GetTerminalPrefs(ctx); err != nil || got != (TerminalPrefs{}) {
		t.Fatalf("损坏的 JSON 应按默认处理: %+v err=%v", got, err)
	}
	put(`{"fixed_cols": 5, "record_max_lines": 99999999999}`)
	if got, err := st.GetTerminalPrefs(ctx); err != nil || got != (TerminalPrefs{}) {
		t.Fatalf("越界的字段应各自归零: %+v err=%v", got, err)
	}
	put(`{"fixed_cols": 100, "record_max_lines": 3}`)
	if got, _ := st.GetTerminalPrefs(ctx); got.FixedCols != 100 || got.RecordMaxLines != 0 {
		t.Fatalf("只有越界的字段归零: %+v", got)
	}
}

func TestTerminalPrefsAutoUpgradeSwitchDefaultsOffAndIsIndependent(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	// 从未设置、以及升级前保存的旧数据（没有这个键）：总闸一律是关的。
	if got, _ := st.GetTerminalPrefs(ctx); got.AgentAutoUpgrade {
		t.Fatalf("从未设置时总闸应关闭: %+v", got)
	}
	if err := st.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Save(&model.SystemSetting{Key: TerminalPrefsKey, Value: `{"fixed_cols": 100, "record_max_lines": 20000}`}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalPrefs(ctx); got.AgentAutoUpgrade || got.FixedCols != 100 || got.RecordMaxLines != 20000 {
		t.Fatalf("旧数据里没有总闸：应是关闭，其余设置不变: %+v", got)
	}

	// 打开总闸不影响别的设置，别的设置也不影响总闸。
	if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.AgentAutoUpgrade = true }); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalPrefs(ctx); !got.AgentAutoUpgrade || got.FixedCols != 100 || got.RecordMaxLines != 20000 {
		t.Fatalf("打开总闸后: %+v", got)
	}
	if _, err := st.UpdateTerminalPrefs(ctx, func(p *TerminalPrefs) { p.FixedCols = 0 }); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalPrefs(ctx); !got.AgentAutoUpgrade || got.FixedCols != 0 {
		t.Fatalf("改别的设置不应带走总闸: %+v", got)
	}

	// 一个越界的设置不会连累总闸：越界的字段单独归零，总闸保持。
	if err := st.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Save(&model.SystemSetting{Key: TerminalPrefsKey, Value: `{"fixed_cols": 5, "agent_auto_upgrade": true}`}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalPrefs(ctx); !got.AgentAutoUpgrade || got.FixedCols != 0 {
		t.Fatalf("越界字段归零，总闸保持: %+v", got)
	}
	// 损坏的 JSON 一律回到默认（关）：读不清楚时落在关闭的一侧。
	if err := st.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Save(&model.SystemSetting{Key: TerminalPrefsKey, Value: `{"agent_auto_upgrade": tru`}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTerminalPrefs(ctx); got.AgentAutoUpgrade {
		t.Fatalf("损坏的 JSON 应关闭总闸: %+v", got)
	}
}
