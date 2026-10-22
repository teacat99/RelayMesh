package mcp

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 任务清单的「结构体检」。
//
// 工具说明与初始化指引已经告诉智能体「模块是项目稳定的功能域」，但模型在长对话里仍然会把清单搭成几种不期望的形状：
//   - 按「第 N 轮 / 第 N 批」「日期」「会话」建模块或标题——这些划分几周后就失效，清单变成一本流水账；
//   - 按阶段（评估 / 开发 / 测试）或状态（进行中 / 验证中）建模块——阶段与状态本来就有各自的字段；
//   - 「其他 / 杂项」之类的杂物箱，什么都往里放；
//   - 一直不规划模块，所有任务都挂在「未归类」；
//   - 顶层模块越建越多，一次需求一个模块。
//
// 这里的检查只提醒、不拒绝（真实项目里偶尔确有这样的名字），提醒会随 overview / plan / batch 的回执一起带给智能体，
// 回执头里的 tasks 行也会在有「未归类」任务时提一句。判断都是纯函数，方便单测。

const (
	// boardModulesAdvised 是建议的顶层模块数上限（硬上限是 model.WorkModulesMax）。
	boardModulesAdvised = 8
	// boardFlatTasks 是「模块下还没有功能分层」时，任务多到该分功能的门槛。
	boardFlatTasks = 10
	// boardWarningsMax 是一次回执最多带几条提醒，免得提醒本身成为噪声。
	boardWarningsMax = 5
	// boardWarnNamesMax 是一条提醒里最多点名几个节点。
	boardWarnNamesMax = 3
)

// boardRoundNameRe 匹配带着「轮次 / 批次 / 日期 / 会话」痕迹的名字。
// 刻意没有收「会话」二字：它在很多项目里是正经的功能域（比如本项目的「会话与反馈」）。
var boardRoundNameRe = regexp.MustCompile(`(?i)(第\s*[0-9一二三四五六七八九十百零两]+\s*(轮|批|期|次|阶段|迭代|周)|\b(round|sprint|batch|iteration|phase|wave)\s*[-_#]?\s*[0-9]+|\b20[0-9]{2}[-/.][0-9]{1,2}([-/.][0-9]{1,2})?\b|[0-9]{1,2}\s*月\s*[0-9]{0,2}\s*[日号]?|\bsess-|\bwf-|本轮|本批|本次|这一轮|这一批)`)

// boardRoundTitleRe 匹配以轮次 / 批次开头的任务标题（「第八批：……」）。
var boardRoundTitleRe = regexp.MustCompile(`(?i)^\s*(第\s*[0-9一二三四五六七八九十百零两]+\s*(轮|批|期|次)|(round|sprint|batch)\s*[-_#]?\s*[0-9]+)`)

// boardCatchAllNames 是「杂物箱」名字（小写后整词匹配）。
var boardCatchAllNames = nameSet(
	"其他", "其它", "杂项", "杂务", "零散", "零碎", "临时", "默认", "通用", "未分类", "待分类", "未归类", "新任务", "新需求",
	"misc", "miscellaneous", "other", "others", "general", "default", "tmp", "temp", "wip",
)

// boardPhaseNames 是工作阶段 / 任务状态的名字（小写后整词匹配）；它们只在顶层模块上算问题：
// 功能一级偶尔有「测试」「设计」这样的叫法是正常的。
var boardPhaseNames = nameSet(
	"评估", "方案", "开发", "验证", "测试", "设计", "调研", "实现", "上线", "收尾", "改进",
	"待办", "进行中", "验证中", "待确认", "已完成", "已搁置",
	"assess", "plan", "dev", "development", "test", "testing", "design", "verify", "research", "improve",
	"todo", "doing", "review", "done", "shelved", "backlog",
)

func nameSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[strings.ToLower(n)] = true
	}
	return m
}

// nodeNameIssue 判断一个规划节点（模块或功能）的名字是否不太合适；合适时返回空串。
// topLevel 表示它是顶层模块（阶段 / 状态类名字只在顶层算问题）。
func nodeNameIssue(name string, topLevel bool) string {
	n := strings.TrimSpace(name)
	key := strings.ToLower(n)
	switch {
	case n == "":
		return ""
	case boardCatchAllNames[key]:
		return "是个杂物箱名字，什么都能往里放，清单很快就没法看了"
	case topLevel && boardPhaseNames[key]:
		return "是工作阶段 / 任务状态，不是项目的功能域（阶段用 stage、状态用 state 表达）"
	case boardRoundNameRe.MatchString(n):
		return "带着轮次 / 批次 / 日期 / 会话的痕迹，这类划分过几周就失效"
	}
	return ""
}

// nodeLabels 把「路径 + 原因」格式化成一句话里的清单，最多点名 boardWarnNamesMax 个。
func nodeLabels(items []string) string {
	if len(items) > boardWarnNamesMax {
		return strings.Join(items[:boardWarnNamesMax], "；") + fmt.Sprintf("；等共 %d 处", len(items))
	}
	return strings.Join(items, "；")
}

const boardRenameAdvice = "模块应按项目稳定的功能域（子系统 / 产品区域 / 长期工作线）划分：用 plan 的 rename 把它改成功能域名称（挂在上面的任务会跟着走），或把任务改归到合适的功能后再 remove 空节点。"

// newNodeWarnings 检查一次 plan 新建出来的节点（路径形如「终端」「终端/历史日志」）。
func newNodeWarnings(created []string) []string {
	var bad []string
	for _, p := range created {
		segs := model.SplitModulePath(p)
		if len(segs) == 0 {
			continue
		}
		if why := nodeNameIssue(segs[len(segs)-1], len(segs) == 1); why != "" {
			bad = append(bad, fmt.Sprintf("「%s」%s", p, why))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("刚新建的节点里有名字不太合适的：%s。%s", nodeLabels(bad), boardRenameAdvice)}
}

// boardStructureWarnings 检查整份清单的结构，返回要告诉智能体的提醒（已按 boardWarningsMax 截断）。
func boardStructureWarnings(list *store.WorkModuleList) []string {
	if list == nil {
		return nil
	}
	var out []string
	if n := list.Unassigned.Open(); n > 0 {
		out = append(out, fmt.Sprintf(`还有 %d 个未结束的任务没有归类：用 action=list module="未归类" 看是哪些，再用 batch 归入已有路径，例如 ops=[{"task_ids":[1,2],"module":"终端/历史日志"}]；之后新建的任务一开始就写上 module。`, n))
	}

	var bad []string
	tops := 0
	features := map[uint]int{}
	for _, m := range list.Modules {
		top := m.ParentID == nil
		if top {
			tops++
		} else {
			features[*m.ParentID]++
		}
		if why := nodeNameIssue(m.Name, top); why != "" {
			bad = append(bad, fmt.Sprintf("「%s」%s", m.Path, why))
		}
	}
	if len(bad) > 0 {
		out = append(out, fmt.Sprintf("规划树里有名字不太合适的节点：%s。%s", nodeLabels(bad), boardRenameAdvice))
	}
	if tops > boardModulesAdvised {
		out = append(out, fmt.Sprintf("顶层模块已有 %d 个，超过建议的 3–%d 个：新工作先归入现有模块的功能，不要为单次需求再新增模块；重复或相近的模块可以用 plan 合并（rename + 把任务改归后 remove）。", tops, boardModulesAdvised))
	}
	for _, m := range list.Modules {
		if m.ParentID == nil && features[m.ID] == 0 && m.Stats.Total >= boardFlatTasks {
			out = append(out, fmt.Sprintf("模块「%s」下已有 %d 个任务，却没有功能分层：用 plan 给它加几个功能（features），再用 batch 把任务按功能重新归类。", m.Name, m.Stats.Total))
			break
		}
	}
	if len(out) > boardWarningsMax {
		out = out[:boardWarningsMax]
	}
	return out
}

// batchWarnings 检查一次 batch 里新建的任务：没写 module 的数量，以及以轮次 / 批次开头的标题。
func batchWarnings(createdNoModule int, roundTitles []string) []string {
	var out []string
	if createdNoModule > 0 {
		out = append(out, fmt.Sprintf(`本次新建的 %d 个任务没有填 module，已落在「未归类」：用 batch 的 task_ids + module 补上（先 action=overview 看已有路径）；之后新建任务时直接写 module。`, createdNoModule))
	}
	if len(roundTitles) > 0 {
		shown := roundTitles
		if len(shown) > boardWarnNamesMax {
			shown = shown[:boardWarnNamesMax]
		}
		out = append(out, fmt.Sprintf("任务标题以轮次 / 批次开头（%s）：轮次属于对话，不是任务的一部分。把标题写成具体交付物（做完能验证什么），并归入对应的功能域；用户一次提的多件事，各归各的模块，不要按「这一轮」打包成一个父任务。", "「"+strings.Join(shown, "」「")+"」"))
	}
	return out
}

// isRoundTitle 判断任务标题是否以轮次 / 批次开头。
func isRoundTitle(title string) bool { return boardRoundTitleRe.MatchString(title) }
