package model

import "testing"

func TestCanonicalPhaseID(t *testing.T) {
	cases := map[string]string{
		"assess":      PhaseDiscuss,
		"verify":      PhaseDev,
		"done":        PhaseDev,
		" assess ":    PhaseDiscuss, // 容忍首尾空白
		"discuss":     PhaseDiscuss,
		"plan":        PhasePlan,
		"dev":         PhaseDev,
		"phase-17301": "phase-17301", // 用户自建的阶段原样返回
		"":            "",
	}
	for in, want := range cases {
		if got := CanonicalPhaseID(in); got != want {
			t.Errorf("CanonicalPhaseID(%q) = %q, want %q", in, got, want)
		}
	}
	if !IsLegacyPhaseID("assess") || !IsLegacyPhaseID("verify") || !IsLegacyPhaseID("done") {
		t.Error("assess / verify / done 应当是旧 id")
	}
	if IsLegacyPhaseID("plan") || IsLegacyPhaseID("dev") || IsLegacyPhaseID("discuss") {
		t.Error("plan / dev / discuss 不是旧 id")
	}
}

func TestDefaultPhaseTemplateShape(t *testing.T) {
	tpl := DefaultPhaseTemplate()
	ids := make([]string, len(tpl))
	for i, p := range tpl {
		ids[i] = p.ID
		if p.Label == "" || p.Description == "" || p.Prompt == "" {
			t.Errorf("阶段 %q 的标签 / 描述 / 提示词不能为空: %+v", p.ID, p)
		}
		if IsLegacyPhaseID(p.ID) {
			t.Errorf("出厂模板里不应再出现旧 id %q", p.ID)
		}
	}
	want := []string{PhaseDiscuss, PhasePlan, PhaseDev}
	if len(ids) != len(want) {
		t.Fatalf("出厂模板应是三段: %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("出厂模板顺序应为 %v, got %v", want, ids)
		}
	}
	if DefaultPhaseID != tpl[0].ID {
		t.Errorf("起始阶段应是第一段（讨论），got %q", DefaultPhaseID)
	}
}

// legacyShipped 里的哈希必须与 git 历史里出厂过的提示词一一对应，否则「是否改过」的判断会失灵。
func TestLegacyShippedHashesMatchHistory(t *testing.T) {
	for id, prompts := range legacyShippedPrompts {
		want := len(legacyShipped[id].promptSHA1)
		if len(prompts) != want {
			t.Errorf("%s: 夹具有 %d 版提示词，legacyShipped 记录了 %d 个哈希", id, len(prompts), want)
		}
		for _, p := range prompts {
			if !isShippedLegacyPrompt(id, p) {
				t.Errorf("%s: 历史出厂提示词没有被识别: %.30s…", id, p)
			}
		}
	}
	if isShippedLegacyPrompt("plan", "") {
		t.Error("空提示词不应被当成出厂文案")
	}
	if isShippedLegacyPrompt("discuss", legacyShippedPrompts["assess"][0]) {
		t.Error("新阶段 id 不应命中旧阶段的哈希表")
	}
}

// legacyTemplate 用最新一版旧出厂文案拼出一份完整的旧五段式模板。
func legacyTemplate() []PhaseItem {
	var items []PhaseItem
	for _, id := range []string{"assess", "plan", "dev", "verify", "done"} {
		prompts := legacyShippedPrompts[id]
		items = append(items, PhaseItem{
			ID:          id,
			Label:       legacyShipped[id].label,
			Description: legacyShipped[id].description,
			Prompt:      prompts[len(prompts)-1],
		})
	}
	return items
}

func phaseIDs(items []PhaseItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMigratePhaseItemsUntouchedLegacyBecomesDefaults(t *testing.T) {
	got, changed := MigratePhaseItems(legacyTemplate())
	if !changed {
		t.Fatal("旧五段式模板应当被迁移")
	}
	want := DefaultPhaseTemplate()
	if len(got) != len(want) {
		t.Fatalf("迁移后应是 %d 段: %v", len(want), phaseIDs(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 段应与出厂模板一致:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}

// 线上真实情况：开发阶段的提示词是更早一版的出厂文案，其余是最新一版。
func TestMigratePhaseItemsMixedShippedVersions(t *testing.T) {
	items := legacyTemplate()
	items[2].Prompt = legacyShippedPrompts["dev"][0]
	got, changed := MigratePhaseItems(items)
	if !changed {
		t.Fatal("应当被迁移")
	}
	if !equalStrings(phaseIDs(got), []string{PhaseDiscuss, PhasePlan, PhaseDev}) {
		t.Fatalf("got %v", phaseIDs(got))
	}
	if got[2].Prompt != DefaultPhaseTemplate()[2].Prompt {
		t.Error("更早一版的出厂开发提示词也应换成新模板")
	}
}

func TestMigratePhaseItemsKeepsCustomizedText(t *testing.T) {
	items := legacyTemplate()
	items[0].Label = "需求"            // 改过标签的评估
	items[0].Prompt = "只聊需求，别碰代码。"   // 改过提示词
	items[1].Prompt = "我自己的方案阶段提示词。" // plan 的提示词是自己写的
	items[2].Description = "写代码"     // 只改了描述
	got, _ := MigratePhaseItems(items)
	if !equalStrings(phaseIDs(got), []string{PhaseDiscuss, PhasePlan, PhaseDev}) {
		t.Fatalf("got %v", phaseIDs(got))
	}
	def := DefaultPhaseTemplate()
	if got[0].Label != "需求" || got[0].Prompt != "只聊需求，别碰代码。" {
		t.Errorf("自定义的标签和提示词应保留: %+v", got[0])
	}
	if got[0].Description != def[0].Description {
		t.Errorf("没改过的描述应换成新文案: %q", got[0].Description)
	}
	if got[1].Prompt != "我自己的方案阶段提示词。" {
		t.Errorf("自己写的 plan 提示词应保留: %q", got[1].Prompt)
	}
	if got[2].Description != "写代码" {
		t.Errorf("自己改的描述应保留: %q", got[2].Description)
	}
	if got[2].Prompt != def[2].Prompt {
		t.Error("没改过的开发提示词应换成新模板")
	}
}

func TestMigratePhaseItemsIdempotentAndNoOp(t *testing.T) {
	once, _ := MigratePhaseItems(legacyTemplate())
	twice, changed := MigratePhaseItems(once)
	if changed {
		t.Error("迁移后再迁移不应再有变化")
	}
	if !samePhaseItems(once, twice) {
		t.Error("迁移应是幂等的")
	}

	def := DefaultPhaseTemplate()
	if got, changed := MigratePhaseItems(def); changed || !samePhaseItems(got, def) {
		t.Error("新出厂模板不应被改动")
	}
	if got, changed := MigratePhaseItems(nil); changed || got != nil {
		t.Error("空列表原样返回")
	}
}

func TestMigratePhaseItemsLeavesCustomPhasesAlone(t *testing.T) {
	items := append(legacyTemplate(), PhaseItem{ID: "phase-1730", Label: "上线", Prompt: "上线前核对清单"})
	got, _ := MigratePhaseItems(items)
	if !equalStrings(phaseIDs(got), []string{PhaseDiscuss, PhasePlan, PhaseDev, "phase-1730"}) {
		t.Fatalf("用户自建阶段应保留在原来的相对位置: %v", phaseIDs(got))
	}
	if got[3].Label != "上线" || got[3].Prompt != "上线前核对清单" {
		t.Errorf("自建阶段内容不应被改: %+v", got[3])
	}
}

func TestMigratePhaseItemsMergesIntoExistingTarget(t *testing.T) {
	// 列表里已经同时有 dev 和 verify / done：旧条目并入 dev，不产生重复。
	items := []PhaseItem{
		{ID: "assess", Label: "评估"},
		{ID: "dev", Label: "开发"},
		{ID: "verify", Label: "验证"},
		{ID: "done", Label: "完成"},
	}
	got, _ := MigratePhaseItems(items)
	if !equalStrings(phaseIDs(got), []string{PhaseDiscuss, PhaseDev}) {
		t.Fatalf("got %v", phaseIDs(got))
	}
}

func TestMigratePhaseItemsNoDevInList(t *testing.T) {
	// 用户删掉了开发阶段、只留下验证和完成：第一条旧条目充当 dev，后面的重复条目丢弃。
	items := []PhaseItem{
		{ID: "plan", Label: "方案"},
		{ID: "verify", Label: "验证", Description: "三件套通过与功能验证", Prompt: legacyShippedPrompts["verify"][0]},
		{ID: "done", Label: "完成"},
	}
	got, _ := MigratePhaseItems(items)
	if !equalStrings(phaseIDs(got), []string{PhasePlan, PhaseDev}) {
		t.Fatalf("got %v", phaseIDs(got))
	}
	def := DefaultPhaseTemplate()[2]
	if got[1].Label != def.Label || got[1].Prompt != def.Prompt || got[1].Description != def.Description {
		t.Errorf("未改过的验证条目转为开发时应使用新模板文案: %+v", got[1])
	}
}

func TestMigratePhaseItemsUpgradesShippedPromptsWithoutLegacyIDs(t *testing.T) {
	// 只有 plan、dev 两个 id（没有旧 id），但提示词还是旧出厂版本：也要换成新文案。
	items := []PhaseItem{
		{ID: "plan", Label: "方案", Description: "方案设计与评审拍板", Prompt: legacyShippedPrompts["plan"][1]},
		{ID: "dev", Label: "开发", Description: "编码实施与增量验证", Prompt: legacyShippedPrompts["dev"][0]},
	}
	got, changed := MigratePhaseItems(items)
	if !changed {
		t.Fatal("旧出厂提示词应被升级")
	}
	def := DefaultPhaseTemplate()
	if got[0].Prompt != def[1].Prompt || got[1].Prompt != def[2].Prompt {
		t.Error("提示词应换成新模板")
	}
}
