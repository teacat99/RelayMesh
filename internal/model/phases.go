package model

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

// 工作流阶段。
//
// v1.7 起阶段收敛为三段：讨论 / 方案 / 开发。
//   - 讨论：快速沟通信息（原「评估」）；
//   - 方案：基于已核实的事实做方案评估；
//   - 开发：按已确认的方案实施，原「验证」「完成」的检查清单并入本阶段的提示词。
//
// 阶段描述的是「智能体现在该怎么表现」，与任务状态（工作项走到哪一步）是两条正交的轴。
const (
	PhaseDiscuss = "discuss"
	PhasePlan    = "plan"
	PhaseDev     = "dev"

	// DefaultPhaseID 是新工作流的起始阶段。
	DefaultPhaseID = PhaseDiscuss
)

// legacyPhaseAlias 是旧五段式阶段 id 到新阶段 id 的映射。
// 旧 id 仍然被所有入口接受（MCP 的 phase 参数、set_phase、Web API），落库前统一换成新 id。
var legacyPhaseAlias = map[string]string{
	"assess": PhaseDiscuss,
	"verify": PhaseDev,
	"done":   PhaseDev,
}

// CanonicalPhaseID 把旧阶段 id 换成对应的新 id，其他 id（含用户自建的阶段）原样返回。
func CanonicalPhaseID(id string) string {
	id = strings.TrimSpace(id)
	if canon, ok := legacyPhaseAlias[id]; ok {
		return canon
	}
	return id
}

// IsLegacyPhaseID 判断 id 是不是已经并入别的阶段的旧 id。
func IsLegacyPhaseID(id string) bool {
	_, ok := legacyPhaseAlias[strings.TrimSpace(id)]
	return ok
}

// DefaultPhaseTemplate 是出厂的三段式阶段模板。
func DefaultPhaseTemplate() []PhaseItem {
	return []PhaseItem{
		{
			ID:          PhaseDiscuss,
			Label:       "讨论",
			Description: "快速沟通信息，先给结论再给依据",
			Prompt: "当前处于讨论阶段：目标是和用户快速对齐信息——回答问题、澄清需求、交换想法，不急着动手。\n" +
				"- 先给结论，再给依据；回答控制在用户能一眼读完的长度，细节按需展开。\n" +
				"- 信息不足时，一次最多问 3 个问题，并尽量给出备选项和你的推荐，不要让用户从零作答。\n" +
				"- 用户的原话逐字记入会话文档，已确认的结论另行写明，二者不要混在一起。\n" +
				"- 可以只读地查看代码、日志与文档来核实事实，但不得创建、修改或删除任何源代码文件，也不要执行有副作用的命令。\n" +
				"- 话题需要在多个方案之间权衡、改动面超过一个模块，或带有不可逆操作时，建议切换到方案阶段（interactive_feedback 的 phase 传 plan）；用户明确要求直接动手的小改动，得到确认后可以进入开发阶段（phase 传 dev）。",
		},
		{
			ID:          PhasePlan,
			Label:       "方案",
			Description: "基于已核实的事实做方案评估",
			Prompt: "当前处于方案阶段：目标是基于核实过的事实，产出一份可以让用户拍板的方案。\n" +
				"- 先核实现状再下结论：阅读相关代码、配置、文档与既有决策，引用具体的文件或函数作为依据；没有核实过的内容明确标为假设。\n" +
				"- 方案固定包含：目标与非目标 / 事实依据 / 备选方案（至少 2 个，写清取舍）/ 数据流、状态、不变量与边界异常 / 影响面（所有引用方）/ 风险与回滚 / 验收标准 / 需要用户确认的不可逆事项。\n" +
				"- 决策逐项通过 feedback 与用户确认，锁定后写入会话文档「关键决策」；用户没有拍板的事项记入「待用户拍板」，不要自行假定。\n" +
				"- 本阶段只读：不得创建、修改或删除任何源代码文件，也不要执行有副作用的命令；确实需要改动才能验证时，先向用户说明并获得二次确认。\n" +
				"- 用户确认方案后，切换到开发阶段（phase 传 dev）再动手。",
		},
		{
			ID:          PhaseDev,
			Label:       "开发",
			Description: "按已确认方案实施，验证并汇报",
			Prompt: "当前处于开发阶段：按已确认的方案实施，并在本阶段内完成验证、收尾与汇报。\n" +
				"- 改前先读相关代码与文档，沿用项目惯用模式；改动接口、类型或 import 时检查所有引用方。\n" +
				"- 小步增量：每完成一个逻辑单元立即做增量验证（lint / type-check → build → 相关测试），不要全部写完再统一修；每 200-500 行改动或一个独立模块完成就提交，汇报时附带 commit 参数（git log -1 --format='%h %s' 的单行输出）。\n" +
				"- 完成标准 = 功能 + 类型 + 编译 + 测试 + 文档与配置同步 + 开发记录，每一条都要有可验证的证据（命令输出、截图或日志），没有证据不算完成。\n" +
				"- 部署、数据库迁移、删除业务数据、git push 远端、发布 release / tag 等不可逆操作，必须先通过 feedback 获得用户的二次确认。\n" +
				"- 发现方案与现实不符（接口不存在、假设不成立、影响面超出方案）时先停下记录：能在方案范围内解决就解决，否则向用户汇报并回到方案阶段，不要悄悄改变方案。\n" +
				"- 收尾：盘点后台进程与未提交变更，清理临时文件，更新会话文档，再通过 feedback 提交最终汇报（修改内容、原因、影响范围、验证证据、后续建议）。用户的下一条反馈如果是新话题，先把阶段切回讨论。",
		},
	}
}

// legacyShipped 记录旧五段式的出厂文案：标签、描述，以及历史上出厂过的各版提示词的 SHA-1。
//
// 迁移时用它判断某个阶段是「仍是出厂文案」（直接换成新模板）还是「用户改过」（原样保留）。
// 只存哈希而不存全文：全文已随版本历史保留，这里只需要做相等判断。
// 哈希来自 git 历史里 DefaultPhaseTemplate / 前端默认模板出现过的所有版本。
var legacyShipped = map[string]struct {
	label, description string
	promptSHA1         []string
}{
	"assess": {"评估", "需求接入与理解确认", []string{
		"f9dddf9e8b62c795d43dcdf8341c64752c25ce14",
		"a31f7f622b449051a2075c0dd48a23c9258c996b",
	}},
	"plan": {"方案", "方案设计与评审拍板", []string{
		"89cfd38af5cba55b6773edde9abbbeb9b7bf1402",
		"aec06a0ad83c6cf92d600ec67dbb5c9607593d90",
	}},
	"dev": {"开发", "编码实施与增量验证", []string{
		"d00a95d74d5ca2c54397b0ea5967b55dc2cea5ca",
		"1f71daab70fadea8fdc82fba8fe18bb9ea02ccc0",
	}},
	"verify": {"验证", "三件套通过与功能验证", []string{
		"3e1f05415f2e93a641811ad63e82f3e9f30cefb8",
	}},
	"done": {"完成", "汇报完成与等待下一步", []string{
		"3c646286b24807169894401342be4cce26c8b0ac",
		"ca953cb9bdc6dc1ecb0f21c9c87cb0c0e404c311",
	}},
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// isShippedLegacyPrompt 判断 prompt 是否是 id 对应阶段在旧版里出厂过的某一版提示词。
func isShippedLegacyPrompt(id, prompt string) bool {
	legacy, ok := legacyShipped[id]
	if !ok || prompt == "" {
		return false
	}
	h := sha1Hex(prompt)
	for _, want := range legacy.promptSHA1 {
		if h == want {
			return true
		}
	}
	return false
}

// MigratePhaseItems 把旧五段式的阶段列表收敛为三段式，返回新列表以及是否发生了变化。
//
// 规则（逐字段，保证「用户改过的文案不丢」）：
//   - 旧 id 收敛：assess → discuss；verify、done → dev。列表里已经有目标阶段时，旧条目并入它（直接丢弃）；
//     没有时，第一条旧条目改名充当目标阶段，后面的重复条目丢弃。
//   - 标签、描述、提示词三个字段各自判断：仍然等于旧版出厂文案的，换成新模板对应阶段的文案；
//     用户改过的原样保留。plan、dev 两个 id 没变，但出厂提示词变了，同样按此处理。
//   - 用户自建的阶段（id 不属于旧五段）不动。
//
// 输入不含旧 id、也不含旧版出厂提示词时原样返回（changed=false），可安全重复调用。
func MigratePhaseItems(items []PhaseItem) ([]PhaseItem, bool) {
	if len(items) == 0 {
		return items, false
	}
	needs := false
	for _, it := range items {
		if IsLegacyPhaseID(it.ID) || isShippedLegacyPrompt(it.ID, it.Prompt) {
			needs = true
			break
		}
	}
	if !needs {
		return items, false
	}

	defaults := make(map[string]PhaseItem, 3)
	for _, d := range DefaultPhaseTemplate() {
		defaults[d.ID] = d
	}
	native := make(map[string]bool, len(items)) // 列表里本来就有的、不是旧 id 的阶段
	for _, it := range items {
		if !IsLegacyPhaseID(it.ID) {
			native[it.ID] = true
		}
	}

	out := make([]PhaseItem, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		origID := it.ID
		canon := CanonicalPhaseID(origID)
		if IsLegacyPhaseID(origID) && native[canon] {
			continue // 并入已有的目标阶段
		}
		if seen[canon] {
			continue
		}
		seen[canon] = true
		it.ID = canon
		if legacy, ok := legacyShipped[origID]; ok {
			if def, ok := defaults[canon]; ok {
				if isShippedLegacyPrompt(origID, it.Prompt) {
					it.Prompt = def.Prompt
				}
				if it.Label == legacy.label {
					it.Label = def.Label
				}
				if it.Description == legacy.description {
					it.Description = def.Description
				}
			}
		}
		out = append(out, it)
	}
	return out, !samePhaseItems(out, items)
}

func samePhaseItems(a, b []PhaseItem) bool {
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
