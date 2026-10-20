package docs

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ExportEntry 是导出清单里的一篇文档。
type ExportEntry struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Rev     int    `json:"rev"`
	Hash    string `json:"hash"`
	Updated string `json:"updated_at"`
}

// DecisionExportRow 供导出表使用的决策结果行。
type DecisionExportRow struct {
	ID     string
	Title  string
	Status string
	Ref    string
	Result string
}

// BuildIndexMarkdown 生成 INDEX.md 内容。
func BuildIndexMarkdown(workflowID string, entries []ExportEntry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	var b strings.Builder
	b.WriteString("# 项目文档索引\n\n")
	b.WriteString(fmt.Sprintf("工作流：`%s`  \n", workflowID))
	b.WriteString(fmt.Sprintf("生成时间：%s  \n\n", time.Now().Format("2006-01-02 15:04")))
	b.WriteString("| 路径 | 标题 | 类型 | 状态 | 修订 |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, e := range entries {
		b.WriteString(fmt.Sprintf("| [%s](%s) | %s | %s | %s | %d |\n",
			e.Path, e.Path, escapeTable(e.Title), e.Kind, e.Status, e.Rev))
	}
	b.WriteString("\n")
	return b.String()
}

func escapeTable(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

// AppendDecisionResultsTable 在 Markdown 末尾追加决策结果表。
func AppendDecisionResultsTable(content string, rows []DecisionExportRow) string {
	if len(rows) == 0 {
		return content
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(content, "\n"))
	b.WriteString("\n\n<!-- relaymesh:decisions -->\n\n")
	b.WriteString("## 决策结果\n\n")
	b.WriteString("| 决策 id | 标题 | 状态 | 确认项 | 结果 |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			escapeTable(r.ID), escapeTable(r.Title), escapeTable(r.Status), escapeTable(r.Ref), escapeTable(r.Result)))
	}
	b.WriteString("\n<!-- /relaymesh:decisions -->\n")
	return b.String()
}
