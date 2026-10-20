package agentstate

import (
	"fmt"
	"strings"
)

// 身份显示名（设计稿 04 §1，需求 I-2）：显示名 = 改名 ?? 目录名；
// 出现重名时，所有重名项降级显示「主机:完整路径」。全站只用这一处计算，避免状态卡、成员面板、群消息各算各的。

// LabelInput 是计算显示名需要的字段。
type LabelInput struct {
	ID               uint
	HostName         string
	ProjectDirectory string
	Name             string // 用户改的名字；空 = 用目录名
}

// Label 是一个身份的显示名。
type Label struct {
	Text     string
	Degraded bool // true = 因为重名降级成了「主机:完整路径」
}

const unknownProject = "未知项目"

// DirectoryName 取路径的最后一段，Unix 与 Windows 风格的分隔符都认；取不到返回空串。
func DirectoryName(dir string) string {
	d := strings.TrimRight(strings.TrimSpace(dir), `/\`)
	if d == "" {
		return ""
	}
	if i := strings.LastIndexAny(d, `/\`); i >= 0 {
		d = d[i+1:]
	}
	return d
}

// BaseLabel 是不考虑重名时的显示名：改名，否则目录名，再否则「未知项目」。
func BaseLabel(in LabelInput) string {
	if n := strings.TrimSpace(in.Name); n != "" {
		return n
	}
	if d := DirectoryName(in.ProjectDirectory); d != "" {
		return d
	}
	return unknownProject
}

func degradedLabel(in LabelInput) string {
	dir := strings.TrimRight(strings.TrimSpace(in.ProjectDirectory), `/\`)
	host := strings.TrimSpace(in.HostName)
	switch {
	case host != "" && dir != "":
		return host + ":" + dir
	case dir != "":
		return dir
	case host != "":
		return host + ":" + BaseLabel(in)
	}
	return BaseLabel(in)
}

// Labels 计算一批身份的显示名（同一批里才会比较重名，调用方传入同一个用户的全部身份）。
func Labels(items []LabelInput) map[uint]Label {
	out := make(map[uint]Label, len(items))
	groups := make(map[string][]LabelInput, len(items))
	for _, it := range items {
		k := strings.ToLower(BaseLabel(it))
		groups[k] = append(groups[k], it)
	}
	for _, g := range groups {
		if len(g) == 1 {
			out[g[0].ID] = Label{Text: BaseLabel(g[0])}
			continue
		}
		seen := make(map[string]int, len(g))
		for _, it := range g {
			seen[strings.ToLower(degradedLabel(it))]++
		}
		for _, it := range g {
			text := degradedLabel(it)
			if seen[strings.ToLower(text)] > 1 { // 同一主机同一路径下的两个凭据：再补上编号才分得开
				text = fmt.Sprintf("%s #%d", text, it.ID)
			}
			out[it.ID] = Label{Text: text, Degraded: true}
		}
	}
	return out
}
