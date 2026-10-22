package mcp

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 交付锚点（Delivery Anchor）
//
// 模型在 interactive_feedback 里可选地带一个 commit 字符串（通常是
// `git log -1 --format='%h %s'` 的输出）。服务端只做三件事：
//  1. 解析出哈希与摘要，规整成单行文本后随会话入库（时间取服务端的会话创建时间）；
//  2. 在每次回执的响应头里回显「最近一次交付锚点」，让上下文被重置后的模型自己判断要不要
//     `git log <哈希>..HEAD`；
//  3. 格式不合法时交付照常成功，只给一条软告警，不阻断、不入库。

const (
	// anchorMinHashLen / anchorMaxHashLen 对应 git 的短哈希到完整 SHA-1 的长度范围。
	anchorMinHashLen = 7
	anchorMaxHashLen = 40
	// anchorMaxSubjectRunes 是摘要保留的最大字符数（按字符而非字节截断，避免切坏中文）。
	anchorMaxSubjectRunes = 200
)

// anchorTokenTrimSet 是匹配哈希时允许被剥掉的两侧包裹字符，例如模型把整行用引号括起来的情形。
const anchorTokenTrimSet = "\"'`[](){}<>,;:"

// ParseAnchor 解析 commit 参数，返回小写哈希与单行摘要。
//
// 规则：把输入按空白切词，第一个（去掉两侧包裹字符后）匹配 `^[0-9a-f]{7,40}$` 的词是哈希，
// 其余词按原顺序拼成摘要并截断到 200 个字符。没有任何词匹配时 ok 为 false。
//
// 输出一定是单行且不含控制字符 / 不可见格式字符——摘要会被回显到模型可见的响应头里，
// 绝不能让一个提交标题伪造出新的「头字段」或「=== 标记 ===」行。
func ParseAnchor(raw string) (hash, subject string, ok bool) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, raw)
	tokens := strings.Fields(cleaned)
	if len(tokens) == 0 {
		return "", "", false
	}

	hashIdx := -1
	for i, tok := range tokens {
		candidate := strings.ToLower(strings.Trim(tok, anchorTokenTrimSet))
		if isHexHash(candidate) {
			hash = candidate
			hashIdx = i
			break
		}
	}
	if hashIdx < 0 {
		return "", "", false
	}

	rest := make([]string, 0, len(tokens)-1)
	rest = append(rest, tokens[:hashIdx]...)
	rest = append(rest, tokens[hashIdx+1:]...)
	subject = strings.Join(rest, " ")
	if r := []rune(subject); len(r) > anchorMaxSubjectRunes {
		subject = strings.TrimSpace(string(r[:anchorMaxSubjectRunes]))
	}
	return hash, subject, true
}

func isHexHash(s string) bool {
	if len(s) < anchorMinHashLen || len(s) > anchorMaxHashLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// humanAge 把时间差渲染成「刚刚 / N 分钟前 / N 小时前 / N 天前」。
func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d 天前", int(d/(24*time.Hour)))
	}
}

// formatAnchorLine 生成响应头里的 `anchor:` 行。
// 摘要用 strconv.Quote 包起来：即便将来摘要里混进引号或反斜杠，也不会破坏这一行的结构。
func formatAnchorLine(a *model.FeedbackSession, now time.Time) string {
	line := "anchor: " + a.GitCommit
	if a.GitSubject != "" {
		line += " " + strconv.Quote(a.GitSubject)
	}
	line += " @ " + a.CreatedAt.Local().Format("01-02 15:04") + "（" + humanAge(now.Sub(a.CreatedAt)) + "）"
	return line
}

// formatAnchorSaved 生成回执里的 `anchor_saved:` 行：本次入库的哈希，以及与上一次锚点的关系。
func formatAnchorSaved(hash string, previous *model.FeedbackSession) string {
	line := "anchor_saved: " + hash
	if previous == nil || previous.GitCommit == "" {
		return line
	}
	if sameCommit(previous.GitCommit, hash) {
		return line + "（与上一次相同）"
	}
	return line + "（上一次为 " + previous.GitCommit + "）"
}

// anchorWarning 是 commit 参数无法解析时的软告警：交付照常成功，只是没有记录锚点。
const anchorWarning = "anchor_warning: commit 参数无法解析（应为 `git log -1 --format='%h %s'` 的输出，首个词须是 7~40 位十六进制哈希），本次未记录交付锚点；交付本身不受影响。"

// sameCommit 判断两个哈希是否指向同一提交（长短不一时按前缀比较）。
func sameCommit(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// injectHeaderLine 把一行文本插到回执响应头的末尾（第一个 `=== 标记 ===` 之前）。
// 回执不是字符串或没有标记行时，原样返回，绝不改变调用方看到的结构。
func injectHeaderLine(result any, line string) any {
	if line == "" {
		return result
	}
	text, ok := result.(string)
	if !ok {
		return result
	}
	if idx := strings.Index(text, "\n=== "); idx >= 0 {
		return text[:idx] + "\n" + line + text[idx:]
	}
	return text + "\n" + line
}

// addAnchorFields 在会话条目里带出交付锚点；没有锚点的会话不添加任何键，输出与以前逐字节一致。
func addAnchorFields(item map[string]any, sess *model.FeedbackSession) {
	if sess.GitCommit == "" {
		return
	}
	item["git_commit"] = sess.GitCommit
	if sess.GitSubject != "" {
		item["git_subject"] = sess.GitSubject
	}
}
