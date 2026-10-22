package ntfy

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// RequestInfo 是「新反馈请求」推送要用到的字段。
// 取快照而不是持有会话指针：推送在别的协程里异步完成，而会话对象随后还会被修改。
type RequestInfo struct {
	SessionID        string
	WorkflowID       string
	Title            string
	Summary          string
	ProjectDirectory string
	HostName         string
}

// InfoFromSession 复制出推送需要的字段。
func InfoFromSession(s *model.FeedbackSession) RequestInfo {
	if s == nil {
		return RequestInfo{}
	}
	return RequestInfo{
		SessionID:        s.ID,
		WorkflowID:       s.WorkflowID,
		Title:            s.Title,
		Summary:          s.Summary,
		ProjectDirectory: s.ProjectDirectory,
		HostName:         s.HostName,
	}
}

const (
	pushTitle         = "智能体在等你回复"
	defaultBodyLine   = "收到新的 AI 反馈请求，请及时查看。"
	summaryExcerptLen = 160
	titleLineMaxLen   = 120
)

// 摘要是 Markdown：只保留正文文字，去掉标题井号、引用与列表符号、粗体与行内代码的标记，代码围栏整段丢弃。
var (
	mdLinePrefix = regexp.MustCompile(`^(#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+)`)
	mdInline     = strings.NewReplacer("**", "", "__", "", "`", "")
)

// BuildRequestMessage 生成「新反馈请求」的推送内容。
// merged 是此前因限流而没发出去的请求条数，大于 0 时在末尾说明一下，免得你以为只有这一条。
func BuildRequestMessage(cfg Config, in RequestInfo, merged int) Message {
	lines := make([]string, 0, 4)

	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = defaultBodyLine
	}
	lines = append(lines, truncateRunes(oneLine(title), titleLineMaxLen))

	if cfg.IncludeSummary {
		if excerpt := summaryExcerpt(in.Summary); excerpt != "" {
			lines = append(lines, excerpt)
		}
	}

	var where []string
	if p := projectName(in); p != "" {
		where = append(where, "项目："+p)
	}
	if h := strings.TrimSpace(in.HostName); h != "" {
		where = append(where, "主机："+truncateRunes(oneLine(h), 40))
	}
	if len(where) > 0 {
		lines = append(lines, strings.Join(where, "　"))
	}
	if merged > 0 {
		lines = append(lines, mergedNote(merged))
	}

	return Message{
		Title:    pushTitle,
		Body:     strings.Join(lines, "\n"),
		Priority: cfg.Priority,
		Tags:     []string{"speech_balloon"},
		Click:    cfg.ClickURL,
	}
}

// BuildTestMessage 生成「测试推送」的内容。
func BuildTestMessage(cfg Config, now time.Time) Message {
	return Message{
		Title:    "RelayMesh 测试推送",
		Body:     fmt.Sprintf("收到这条消息，说明服务地址、主题与认证都配置正确。（%s）", now.Format("15:04:05")),
		Priority: cfg.Priority,
		Tags:     []string{"white_check_mark"},
		Click:    cfg.ClickURL,
	}
}

// mergedNote 说明有几条通知因限流被合并了。被合并的既可能是请求、也可能是运行结束，所以统一说「通知」。
func mergedNote(merged int) string {
	return fmt.Sprintf("（另有 %d 条通知因推送过于频繁被合并，请到控制台查看）", merged)
}

// projectName 取请求所属项目的名字，见 projectLabel。
func projectName(in RequestInfo) string {
	return projectLabel(in.ProjectDirectory, in.WorkflowID)
}

// projectLabel 取项目目录的最后一段（兼容 / 与 \）；目录为空时退回工作流标识。
func projectLabel(projectDirectory, workflowID string) string {
	dir := strings.TrimRight(strings.TrimSpace(projectDirectory), `/\`)
	if i := strings.LastIndexAny(dir, `/\`); i >= 0 {
		dir = dir[i+1:]
	}
	if dir == "" {
		dir = strings.TrimSpace(workflowID)
	}
	return truncateRunes(oneLine(dir), 60)
}

// summaryExcerpt 取摘要开头的一小段纯文本。
func summaryExcerpt(summary string) string {
	parts := make([]string, 0, 8)
	inFence := false
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" {
			continue
		}
		line = strings.TrimSpace(mdInline.Replace(mdLinePrefix.ReplaceAllString(line, "")))
		if line != "" {
			parts = append(parts, line)
		}
		if len(parts) >= 6 {
			break
		}
	}
	return truncateRunes(oneLine(strings.Join(parts, " ")), summaryExcerptLen)
}

// oneLine 把任意空白（含换行）折叠成单个空格。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
