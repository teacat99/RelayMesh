package docs

import (
	"strings"
)

// BlueprintItem 是推荐文档结构里的一项。
type BlueprintItem struct {
	Path          string `json:"path"`
	Title         string `json:"title"`
	Hint          string `json:"hint"`
	Present       bool   `json:"present"`
	SuggestedKind string `json:"suggested_kind,omitempty"`
	Template      string `json:"template,omitempty"`
}

var defaultBlueprint = []struct {
	Path, Title, Hint, Kind string
}{
	{"README.md", "项目概览", "一句话定位、主要能力、如何启动与验证", "guide"},
	{"glossary.md", "术语表", "团队内专有名词与缩写", "reference"},
	{"architecture/context.md", "C4 上下文", "系统与外部角色/系统的边界", "design"},
	{"architecture/containers.md", "C4 容器", "主要进程、数据库、队列与协议", "design"},
	{"data-model.md", "数据模型", "核心实体、关系与迁移策略", "spec"},
	{"security.md", "安全", "鉴权、凭据、审计与威胁模型摘要", "spec"},
	{"decisions/README.md", "ADR 索引", "决策记录目录与编号规则", "decision"},
	{"decisions/0001-template.md", "ADR 模板", "单条架构决策的结构示例", "decision"},
	{"features/README.md", "功能设计索引", "各子系统设计文档入口", "design"},
	{"dev-environment.md", "开发环境", "依赖、工作树、常用命令", "guide"},
	{"runbook/deploy.md", "部署与回滚", "生产变更步骤与回滚点", "runbook"},
	{"runbook/rollback.md", "故障回滚", "常见故障与恢复", "runbook"},
	{"testing.md", "测试", "回归口径、端到端与实验 Hub", "reference"},
	{"api-reference.md", "API 与配置", "REST/MCP 与关键环境变量", "reference"},
	{"roadmap.md", "路线图", "版本规划与变更记录入口", "changelog"},
	{"changelog.md", "变更记录", "对用户可见的版本变更", "changelog"},
}

// Blueprint 对照已有路径指出缺口。
func Blueprint(existingPaths map[string]bool, withTemplates bool) []BlueprintItem {
	var out []BlueprintItem
	for _, d := range defaultBlueprint {
		p := d.Path
		if !strings.HasSuffix(p, ".md") {
			p += ".md"
		}
		item := BlueprintItem{
			Path:          p,
			Title:         d.Title,
			Hint:          d.Hint,
			Present:       existingPaths[p],
			SuggestedKind: d.Kind,
		}
		if withTemplates && !item.Present {
			item.Template = blueprintTemplate(p, d.Title, d.Hint)
		}
		out = append(out, item)
	}
	return out
}

func blueprintTemplate(path, title, hint string) string {
	return "# " + title + "\n\n> " + hint + "\n\n（在此撰写内容。路径：`" + path + "`）\n"
}

// CountBlueprintGaps 返回缺口数量。
func CountBlueprintGaps(items []BlueprintItem) int {
	n := 0
	for _, it := range items {
		if !it.Present {
			n++
		}
	}
	return n
}

var projectBlueprint = []struct {
	Path, Title, Hint, Kind string
}{
	{"overview.md", "项目总览", "一句话定位、整体设计、主要模块与当前状态（整体设计由你来写并维护）", "design"},
	{"architecture/context.md", "系统上下文", "系统与外部角色 / 系统的边界", "design"},
	{"architecture/data-flow.md", "数据流", "关键数据怎样流过各个模块", "design"},
	{"architecture/deployment.md", "部署形态", "进程、存储、网络与回滚点", "design"},
	{"decisions/README.md", "跨模块决策索引", "ADR 的编号规则与目录；单个模块自己的决策放在模块目录下", "decision"},
	{"runbooks/deploy.md", "部署与回滚", "生产变更步骤与回滚点", "runbook"},
}

// ProjectBlueprint 给出项目知识库的推荐结构与缺口（设计稿 06 §4）：固定的几篇总览类文档，加上每个已建模块的总述与设计。
func ProjectBlueprint(existingPaths map[string]bool, modulePaths []string, withTemplates bool) []BlueprintItem {
	var out []BlueprintItem
	add := func(path, title, hint, kind string) {
		item := BlueprintItem{Path: path, Title: title, Hint: hint, Present: existingPaths[path], SuggestedKind: kind}
		if withTemplates && !item.Present {
			item.Template = blueprintTemplate(path, title, hint)
		}
		out = append(out, item)
	}
	for _, d := range projectBlueprint {
		add(d.Path, d.Title, d.Hint, d.Kind)
	}
	for _, m := range modulePaths {
		add("modules/"+m+"/index.md", m+" · 总述", "这个模块做什么、对外接口、负责人与当前状态", "design")
		add("modules/"+m+"/design.md", m+" · 设计", "关键设计、数据与不变量、边界与失败处理", "design")
	}
	return out
}
