package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func phaseIDsOf(items []model.PhaseItem) string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return strings.Join(ids, ",")
}

func TestSetWorkflowPhaseAcceptsLegacyIDs(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	for legacy, want := range map[string]string{"assess": "discuss", "verify": "dev", "done": "dev", "plan": "plan"} {
		wf := "wf-legacy-" + legacy
		if err := st.SetWorkflowPhase(ctx, wf, legacy, false); err != nil {
			t.Fatalf("SetWorkflowPhase(%q): %v", legacy, err)
		}
		state, err := st.GetWorkflowPhase(ctx, wf)
		if err != nil || state == nil {
			t.Fatalf("GetWorkflowPhase(%q): %v %v", wf, state, err)
		}
		if state.CurrentPhaseID != want {
			t.Errorf("%s: 落库的阶段应是 %s，got %s", legacy, want, state.CurrentPhaseID)
		}
		if state.HumanPreferredPhaseID != model.DefaultPhaseID {
			t.Errorf("%s: 新建行的人工偏好阶段应是默认阶段，got %s", legacy, state.HumanPreferredPhaseID)
		}
	}

	// 人工点击会同时记下偏好阶段，旧 id 同样先换成新 id
	if err := st.SetWorkflowPhase(ctx, "wf-human", "assess", true); err != nil {
		t.Fatal(err)
	}
	state, _ := st.GetWorkflowPhase(ctx, "wf-human")
	if state.HumanPreferredPhaseID != "discuss" {
		t.Errorf("人工偏好阶段应是 discuss，got %s", state.HumanPreferredPhaseID)
	}
}

func TestGetWorkflowPhaseWithDefaultsStartsAtDiscuss(t *testing.T) {
	st := setupTestStore(t)
	cur, phases, err := st.GetWorkflowPhaseWithDefaults(context.Background(), "wf-brand-new")
	if err != nil {
		t.Fatal(err)
	}
	if cur != "discuss" {
		t.Errorf("新工作流应从讨论开始，got %q", cur)
	}
	if got := phaseIDsOf(phases); got != "discuss,plan,dev" {
		t.Errorf("默认阶段列表 = %s", got)
	}
}

// 读取路径本身就会把存量的旧值收敛掉，不依赖启动迁移是否已经跑过。
func TestGetWorkflowPhaseWithDefaultsNormalizesStoredLegacy(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	legacyJSON := `[{"id":"assess","label":"评估"},{"id":"plan","label":"方案"},{"id":"dev","label":"开发"},{"id":"verify","label":"验证"},{"id":"done","label":"完成"}]`
	if err := st.db.Create(&model.WorkflowPhaseState{
		WorkflowID: "wf-stored-legacy", CurrentPhaseID: "done", HumanPreferredPhaseID: "assess",
		PhasesJSON: legacyJSON, UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	cur, phases, err := st.GetWorkflowPhaseWithDefaults(ctx, "wf-stored-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if cur != "dev" {
		t.Errorf("存量的 done 应读成 dev，got %q", cur)
	}
	if got := phaseIDsOf(phases); got != "discuss,plan,dev" {
		t.Errorf("存量五段式应读成三段式，got %s", got)
	}
}

func TestMigratePhaseRows(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	stamp := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	legacyJSON := `[{"id":"assess","label":"评估"},{"id":"dev","label":"开发"},{"id":"verify","label":"验证"},{"id":"done","label":"完成"}]`
	rows := []model.WorkflowPhaseState{
		{WorkflowID: "wf-a", CurrentPhaseID: "assess", HumanPreferredPhaseID: "assess", UpdatedAt: stamp},
		{WorkflowID: "wf-b", CurrentPhaseID: "done", HumanPreferredPhaseID: "plan", UpdatedAt: stamp},
		{WorkflowID: "wf-c", CurrentPhaseID: "verify", HumanPreferredPhaseID: "dev", PhasesJSON: legacyJSON, UpdatedAt: stamp},
		{WorkflowID: "wf-clean", CurrentPhaseID: "plan", HumanPreferredPhaseID: "plan", UpdatedAt: stamp},
	}
	for i := range rows {
		if err := st.db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	if n := st.migratePhaseRows(ctx); n != 3 {
		t.Fatalf("应改动 3 行（wf-clean 不动），got %d", n)
	}
	check := func(id, cur, pref string) {
		t.Helper()
		got, _ := st.GetWorkflowPhase(ctx, id)
		if got.CurrentPhaseID != cur || got.HumanPreferredPhaseID != pref {
			t.Errorf("%s: got %s/%s, want %s/%s", id, got.CurrentPhaseID, got.HumanPreferredPhaseID, cur, pref)
		}
		if !got.UpdatedAt.Equal(stamp) {
			t.Errorf("%s: 迁移不应改动 updated_at，got %v", id, got.UpdatedAt)
		}
	}
	check("wf-a", "discuss", "discuss")
	check("wf-b", "dev", "plan")
	check("wf-c", "dev", "dev")
	check("wf-clean", "plan", "plan")

	c, _ := st.GetWorkflowPhase(ctx, "wf-c")
	var items []model.PhaseItem
	if err := json.Unmarshal([]byte(c.PhasesJSON), &items); err != nil {
		t.Fatal(err)
	}
	if got := phaseIDsOf(items); got != "discuss,dev" {
		t.Errorf("工作流级阶段列表应收敛为 discuss,dev，got %s", got)
	}

	if n := st.migratePhaseRows(ctx); n != 0 {
		t.Errorf("迁移应当幂等，第二次不应再有改动，got %d", n)
	}
}

// putRawSettings 绕过 SaveSettings（它自己会迁移），直接写入旧版形态的设置行。
func putRawSettings(t *testing.T, st *Store, m map[string]any) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.db.Save(&model.SystemSetting{Key: GlobalSettingsKey, Value: string(b), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestMigratePhaseSettings(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	legacyInstructions := "前言\n\n" + phaseSectionLegacy + "\n\n5. 后面的内容"
	putRawSettings(t, st, map[string]any{
		"hostName":           "keep-me",
		"serverInstructions": legacyInstructions,
		"phaseTemplate": []map[string]any{
			{"id": "assess", "label": "评估", "description": "需求接入与理解确认"},
			{"id": "plan", "label": "方案"},
			{"id": "dev", "label": "开发"},
			{"id": "verify", "label": "验证"},
			{"id": "done", "label": "完成"},
		},
	})

	if !st.migratePhaseSettings(ctx) {
		t.Fatal("旧版设置应当被迁移")
	}
	got, err := st.GetGlobalAppSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ids := phaseIDsOf(got.PhaseTemplate); ids != "discuss,plan,dev" {
		t.Errorf("阶段模板应收敛为三段，got %s", ids)
	}
	if got.HostName != "keep-me" {
		t.Errorf("无关的设置项不应被改动: %q", got.HostName)
	}
	if strings.Contains(got.ServerInstructions, phaseSectionLegacy) || !strings.Contains(got.ServerInstructions, phaseSection) {
		t.Errorf("服务指引的阶段一节应换成新写法")
	}
	if !strings.HasPrefix(got.ServerInstructions, "前言\n\n") || !strings.HasSuffix(got.ServerInstructions, "\n\n5. 后面的内容") {
		t.Errorf("只应替换阶段一节，其余内容保持不变:\n%s", got.ServerInstructions)
	}
	if st.migratePhaseSettings(ctx) {
		t.Error("迁移应当幂等")
	}
}

func TestMigratePhaseSettingsKeepsCustomizedInstructions(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	custom := "我自己写的指引，评估 (assess) 阶段不许改代码。"
	putRawSettings(t, st, map[string]any{"serverInstructions": custom})
	if st.migratePhaseSettings(ctx) {
		t.Error("没有逐字相同的旧阶段一节时不应改动")
	}
	got, _ := st.GetSettings(ctx)
	if got["serverInstructions"] != custom {
		t.Errorf("自己改过的指引应保持原样，got %v", got["serverInstructions"])
	}
}

func TestSaveSettingsNormalizesLegacyPayload(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	payload := map[string]any{
		"serverInstructions": phaseSectionLegacy,
		"phaseTemplate": []any{
			map[string]any{"id": "assess", "label": "评估"},
			map[string]any{"id": "done", "label": "完成"},
		},
	}
	if err := st.SaveSettings(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if payload["serverInstructions"] != phaseSectionLegacy {
		t.Error("SaveSettings 不应修改调用方传入的 map")
	}
	got, _ := st.GetGlobalAppSettings(ctx)
	if ids := phaseIDsOf(got.PhaseTemplate); ids != "discuss,dev" {
		t.Errorf("保存时应收敛旧阶段，got %s", ids)
	}
	if got.ServerInstructions != phaseSection {
		t.Errorf("保存时应换掉旧的阶段一节")
	}
}

func TestDefaultServerInstructionsUsesNewPhaseSection(t *testing.T) {
	if !strings.Contains(DefaultServerInstructions, phaseSection) {
		t.Fatal("默认指引应包含新的阶段一节")
	}
	if strings.Contains(DefaultServerInstructions, phaseSectionLegacy) || strings.Contains(DefaultServerInstructions, "评估 (assess)") {
		t.Error("默认指引不应再出现旧的五段式写法")
	}
	if i, j := strings.Index(DefaultServerInstructions, "4. 阶段流转"), strings.Index(DefaultServerInstructions, "5. 全模式不可逆操作底线"); i < 0 || j < i {
		t.Errorf("第 4、5 条应按顺序出现: %d %d", i, j)
	}
	if got, changed := migrateServerInstructions(DefaultServerInstructions); changed || got != DefaultServerInstructions {
		t.Error("新默认指引不应被迁移改动")
	}
	old := strings.Replace(DefaultServerInstructions, phaseSection, phaseSectionLegacy, 1)
	if got, changed := migrateServerInstructions(old); !changed || got != DefaultServerInstructions {
		t.Error("把旧阶段一节放回去再迁移，应得到当前的默认指引")
	}
}
