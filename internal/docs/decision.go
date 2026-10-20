package docs

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DecisionOption 是决策卡里的一个选项。
type DecisionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Detail      string `json:"detail,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// DecisionCard 是 ```decision 围栏内的结构化决策（本批只解析与展示，作答在第二波）。
type DecisionCard struct {
	ID      string           `json:"id"`
	Title   string           `json:"title"`
	Body    string           `json:"body,omitempty"`
	Options []DecisionOption `json:"options"`
	Default string           `json:"default"`
	Multi   bool             `json:"multi,omitempty"`
	Note    bool             `json:"note,omitempty"`
	Risk    string           `json:"risk,omitempty"`
}

// ParsedDecision 带围栏位置与校验结果。
type ParsedDecision struct {
	Card       DecisionCard `json:"card"`
	StartLine  int          `json:"start_line"`
	EndLine    int          `json:"end_line"`
	RawJSON    string       `json:"-"`
	Errors     []string     `json:"errors,omitempty"`
	Status     string       `json:"status"` // 本批恒为 pending
	StatusNote string       `json:"status_note,omitempty"`
}

const decisionFence = "```decision"

// ParseDecisions 扫描正文中的 decision 围栏并校验 JSON。
func ParseDecisions(body string) []ParsedDecision {
	lines := strings.Split(body, "\n")
	var out []ParsedDecision
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != decisionFence {
			continue
		}
		start := i
		var jsonLines []string
		i++
		for i < len(lines) && strings.TrimSpace(lines[i]) != "```" {
			jsonLines = append(jsonLines, lines[i])
			i++
		}
		if i >= len(lines) {
			out = append(out, ParsedDecision{
				StartLine:  start + 1,
				EndLine:    len(lines),
				Errors:     []string{"decision 围栏未闭合"},
				Status:     "pending",
				StatusNote: "待接入审批",
			})
			break
		}
		raw := strings.TrimSpace(strings.Join(jsonLines, "\n"))
		pd := ParsedDecision{
			StartLine:  start + 1,
			EndLine:    i + 1,
			RawJSON:    raw,
			Status:     "pending",
			StatusNote: "待接入审批",
		}
		if raw == "" {
			pd.Errors = append(pd.Errors, "decision 围栏内容为空")
			out = append(out, pd)
			continue
		}
		var card DecisionCard
		if err := json.Unmarshal([]byte(raw), &card); err != nil {
			pd.Errors = append(pd.Errors, fmt.Sprintf("JSON 解析失败：%v", err))
			out = append(out, pd)
			continue
		}
		pd.Errors = append(pd.Errors, validateDecision(card)...)
		pd.Card = card
		out = append(out, pd)
	}
	return out
}

func validateDecision(c DecisionCard) []string {
	var errs []string
	if strings.TrimSpace(c.ID) == "" {
		errs = append(errs, "id 不能为空")
	}
	if strings.TrimSpace(c.Title) == "" {
		errs = append(errs, "title 不能为空")
	}
	nOpt := len(c.Options)
	if nOpt < 2 || nOpt > 8 {
		errs = append(errs, fmt.Sprintf("options 需要 2～8 个，当前 %d 个", nOpt))
	}
	optIDs := map[string]bool{}
	hasDefault := false
	for _, o := range c.Options {
		if strings.TrimSpace(o.ID) == "" {
			errs = append(errs, "每个 option 必须有 id")
			continue
		}
		if optIDs[o.ID] {
			errs = append(errs, fmt.Sprintf("option id 重复：%s", o.ID))
		}
		optIDs[o.ID] = true
		if strings.TrimSpace(o.Label) == "" {
			errs = append(errs, fmt.Sprintf("option %s 缺少 label", o.ID))
		}
		if c.Default == o.ID {
			hasDefault = true
		}
	}
	if c.Default != "" && !hasDefault {
		errs = append(errs, "default 必须是某个 option 的 id")
	}
	if c.Risk != "" && c.Risk != "L0" && c.Risk != "L1" && c.Risk != "L2" {
		errs = append(errs, "risk 必须是 L0、L1 或 L2")
	}
	return errs
}

// UniqueDecisionIDs 检查文档内 decision id 是否唯一；返回重复的 id。
func UniqueDecisionIDs(decisions []ParsedDecision) []string {
	seen := map[string]int{}
	var dup []string
	for _, d := range decisions {
		id := strings.TrimSpace(d.Card.ID)
		if id == "" {
			continue
		}
		seen[id]++
		if seen[id] == 2 {
			dup = append(dup, id)
		}
	}
	return dup
}
