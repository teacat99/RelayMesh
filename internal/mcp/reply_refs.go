package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 回复引用（#106）。
//
// 用户在 Web 输入框里点某条智能体回复旁的「引用」，会插入「【回复 9f02517c】」（哈希是会话编号 sess- 之后的部分）；
// 点恢复胶囊旁的「引用」插入「【恢复 r8】」，点确认卡（Q5）上的「引用」插入「【确认 Q5】」（#132）。
// 智能体光看到一串哈希或编号认不出是哪条，所以 Hub 在把用户的话交给智能体时，把这些引用解析成一小段说明追加在正文后面：
// 引用的是哪一轮 / 哪个确认项、标题、时间与开头（确认项还带全部选项与用户的答复），
// 并告诉它要读全文用 get_session_history 带 session_id（确认项用 async_ask 的 get 带 ref）。
// 解析只在同一个工作流里进行，不会窥探别的工作流。

var (
	replyRefPattern   = regexp.MustCompile(`【回复\s*([0-9A-Za-z-]{1,24})\s*】`)
	capsuleRefPattern = regexp.MustCompile(`【恢复\s*[rR]?(\d{1,6})\s*】`)
	confirmRefPattern = regexp.MustCompile(`【确认\s*[qQ]?(\d{1,6})\s*】`)
)

const (
	maxQuotedRefs      = 5   // 一条消息里最多展开几处引用，多出来的只说明还有几处没展开
	maxHashMatches     = 3   // 一个哈希最多列出几条（6 位前缀可能撞上多条）
	quotedSnippetRunes = 120 // 引用开头的字数
	quotedTitleRunes   = 60
)

type quotedRef struct {
	Kind     string // "reply" | "capsule" | "confirm"
	Hash     string // reply：规整后的小写哈希
	Revision int    // capsule：胶囊的修订号
	Seq      int    // confirm：确认项在工作流里的序号（Q5 的 5）
}

// extractQuotedRefs 按出现的先后顺序取出文字里的引用并去重。
// 写法不合法的（比如「【回复 你好】」「【回复 abc】」）当作用户的普通文字，不算引用。
func extractQuotedRefs(text string) []quotedRef {
	if !strings.Contains(text, "【") {
		return nil
	}
	type hit struct {
		pos int
		ref quotedRef
	}
	var hits []hit
	for _, m := range replyRefPattern.FindAllStringSubmatchIndex(text, -1) {
		hash := store.NormalizeReplyHash(text[m[2]:m[3]])
		if hash == "" {
			continue
		}
		hits = append(hits, hit{m[0], quotedRef{Kind: "reply", Hash: hash}})
	}
	for _, m := range capsuleRefPattern.FindAllStringSubmatchIndex(text, -1) {
		rev, err := strconv.Atoi(text[m[2]:m[3]])
		if err != nil || rev <= 0 {
			continue
		}
		hits = append(hits, hit{m[0], quotedRef{Kind: "capsule", Revision: rev}})
	}
	for _, m := range confirmRefPattern.FindAllStringSubmatchIndex(text, -1) {
		seq, err := strconv.Atoi(text[m[2]:m[3]])
		if err != nil || seq <= 0 {
			continue
		}
		hits = append(hits, hit{m[0], quotedRef{Kind: "confirm", Seq: seq}})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	seen := make(map[quotedRef]bool, len(hits))
	var out []quotedRef
	for _, h := range hits {
		if seen[h.ref] {
			continue
		}
		seen[h.ref] = true
		out = append(out, h.ref)
	}
	return out
}

// quotedRefsBlock 解析 text 里的引用，返回追加给智能体的说明块；没有引用或没有工作流时返回空串。
func (s *Server) quotedRefsBlock(ctx context.Context, workflowID, text string) string {
	workflowID = strings.TrimSpace(workflowID)
	refs := extractQuotedRefs(text)
	if workflowID == "" || len(refs) == 0 {
		return ""
	}
	shown := len(refs)
	if shown > maxQuotedRefs {
		shown = maxQuotedRefs
	}
	var b strings.Builder
	b.WriteString("=== 引用 ===\n用户在上面的话里用「【回复 …】」「【恢复 …】」「【确认 …】」引用了此前的内容（只列开头；要读全文：回复用 get_session_history 带 session_id，胶囊用 workflow_context 的 checkpoint_get 带 revision，确认项用 async_ask 的 get 带 ref）：\n")
	for _, ref := range refs[:shown] {
		var lines []string
		switch ref.Kind {
		case "reply":
			lines = s.describeReplyRef(ctx, workflowID, ref.Hash)
		case "capsule":
			lines = s.describeCapsuleRef(ctx, workflowID, ref.Revision)
		case "confirm":
			lines = s.describeConfirmRef(ctx, workflowID, ref.Seq)
		}
		for _, line := range lines {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	if extra := len(refs) - shown; extra > 0 {
		fmt.Fprintf(&b, "（另有 %d 处引用未展开）\n", extra)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (s *Server) describeReplyRef(ctx context.Context, workflowID, hash string) []string {
	token := "【回复 " + hash + "】"
	found, err := s.store.FindReplyRefs(ctx, workflowID, hash, maxHashMatches+1)
	if err != nil {
		return []string{"- " + token + "暂时无法解析（读取数据库失败），请让用户直接说明引用的是哪一条。"}
	}
	if len(found) == 0 {
		return []string{"- " + token + "没有找到这条回复（可能已被删除，或不属于当前工作流）。"}
	}
	var lines []string
	if len(found) > 1 {
		lines = append(lines, fmt.Sprintf("- %s这个哈希对应多条回复，按时间由早到晚列出（用户多半指最近的那条，拿不准就向用户确认）：", token))
	}
	for i, f := range found {
		if i >= maxHashMatches {
			lines = append(lines, "  （同一个哈希还有更多条，没有全部列出）")
			break
		}
		prefix := "- " + token
		if len(found) > 1 {
			prefix = "  · "
		}
		lines = append(lines, prefix+describeSessionRef(f))
	}
	return lines
}

// describeSessionRef 把一条被引用的回复写成一行人话：第几轮 / 通知、标题、时间、编号、开头，以及用户当时的回复。
func describeSessionRef(r store.ReplyRef) string {
	sess := r.Session
	what := fmt.Sprintf("第 %d 轮汇报", r.Turn)
	if r.IsNotice() {
		what = "进度通知"
	}
	var b strings.Builder
	b.WriteString(what)
	if title := quoteSnippet(sess.Title, quotedTitleRunes); title != "" {
		fmt.Fprintf(&b, "「%s」", title)
	}
	fmt.Fprintf(&b, "（%s，session_id=%s）", sess.CreatedAt.In(time.Local).Format("01-02 15:04"), sess.ID)
	if body := quoteSnippet(sess.Summary, quotedSnippetRunes); body != "" {
		fmt.Fprintf(&b, "：「%s」", body)
	}
	if reply := quoteSnippet(sess.ResponseText, quotedSnippetRunes); reply != "" && !r.IsNotice() {
		fmt.Fprintf(&b, "；用户当时的回复：「%s」", reply)
	}
	return b.String()
}

func (s *Server) describeCapsuleRef(ctx context.Context, workflowID string, revision int) []string {
	token := fmt.Sprintf("【恢复 r%d】", revision)
	cp, err := s.store.GetCheckpointByRevision(ctx, workflowID, revision)
	if err != nil {
		return []string{"- " + token + "暂时无法解析（读取数据库失败），请让用户直接说明引用的是哪一份。"}
	}
	if cp == nil {
		return []string{"- " + token + "没有找到这份恢复胶囊（修订号不存在，或不属于当前工作流）。"}
	}
	var content model.CheckpointContent
	_ = json.Unmarshal([]byte(cp.ContentJSON), &content)
	line := fmt.Sprintf("- %s恢复胶囊 r%d（%s，状态 %s）", token, cp.Revision, cp.CreatedAt.In(time.Local).Format("01-02 15:04"), cp.Status)
	if obj := quoteSnippet(content.Objective, quotedSnippetRunes); obj != "" {
		line += fmt.Sprintf("：目标「%s」", obj)
	}
	return []string{line}
}

// confirmKindText 是确认项类型的人话（与界面的 KIND_LABEL 一致）。
func confirmKindText(kind string) string {
	switch kind {
	case model.ConfirmKindPlan:
		return "计划确认"
	case model.ConfirmKindDecision:
		return "文档决策"
	default:
		return "提问"
	}
}

// confirmStatusText 是确认项状态的人话：智能体据此判断这件事还在等用户，还是已经有了结论。
func confirmStatusText(status string) string {
	switch status {
	case model.ConfirmStatusPending:
		return "还在等用户答复"
	case model.ConfirmStatusAnswered:
		return "用户已答复"
	case model.ConfirmStatusDismissed:
		return "用户已忽略"
	case model.ConfirmStatusWithdrawn:
		return "已撤回"
	case model.ConfirmStatusExpired:
		return "已过期（选项变了，旧答复作废）"
	default:
		return status
	}
}

// describeConfirmRef 把被引用的确认项写成一行人话：类型、标题、时间、状态，全部选项（带推荐标记）、用户的答复与备注、正文开头。
// 用户事后引用一张已处理的确认卡，多半是要对着当时的方案与答复继续说下去，所以选项与答复比「开头」更要紧。
func (s *Server) describeConfirmRef(ctx context.Context, workflowID string, seq int) []string {
	ref := model.ConfirmRef(seq)
	token := "【确认 " + ref + "】"
	item, err := s.store.GetConfirmItemByRef(ctx, workflowID, ref)
	if err != nil {
		var appErr *store.AppError
		if errors.As(err, &appErr) && appErr.Kind == store.ErrNotFound {
			return []string{"- " + token + "没有找到这个确认项（编号不存在，或不属于当前工作流）。"}
		}
		return []string{"- " + token + "暂时无法解析（读取数据库失败），请让用户直接说明引用的是哪一项。"}
	}
	view := model.NewConfirmItemView(item)
	labelOf := func(id string) string {
		for _, o := range view.Options {
			if o.ID == id && o.Label != "" && o.Label != id {
				return fmt.Sprintf("%s「%s」", id, quoteSnippet(o.Label, quotedTitleRunes))
			}
		}
		return id
	}

	var b strings.Builder
	fmt.Fprintf(&b, "- %s%s", token, confirmKindText(view.Kind))
	if title := quoteSnippet(view.Title, quotedTitleRunes); title != "" {
		fmt.Fprintf(&b, "「%s」", title)
	}
	fmt.Fprintf(&b, "（%s，风险 %s，%s）", view.CreatedAt.In(time.Local).Format("01-02 15:04"), view.Risk, confirmStatusText(view.Status))
	if len(view.Options) > 0 {
		parts := make([]string, 0, len(view.Options))
		for _, o := range view.Options {
			p := labelOf(o.ID)
			if o.Recommended {
				p += "（推荐）"
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(&b, "；选项：%s", strings.Join(parts, "、"))
	}
	if view.Status == model.ConfirmStatusAnswered && view.Answer != nil {
		// 自定义答复（#183）：预设选项都不合适时用户自己写的话；只写了它时没有「选 …」
		custom := quoteSnippet(view.Answer.Custom, quotedSnippetRunes)
		if len(view.Answer.OptionIDs) > 0 || custom != "" {
			if len(view.Answer.OptionIDs) > 0 {
				chosen := make([]string, 0, len(view.Answer.OptionIDs))
				for _, id := range view.Answer.OptionIDs {
					chosen = append(chosen, labelOf(id))
				}
				fmt.Fprintf(&b, "；用户的答复：选 %s", strings.Join(chosen, "、"))
			} else {
				b.WriteString("；用户的答复：没有选预设选项")
			}
			if note := quoteSnippet(view.Answer.Note, quotedSnippetRunes); note != "" {
				fmt.Fprintf(&b, "，备注「%s」", note)
			}
			if custom != "" {
				fmt.Fprintf(&b, "，自定义答复「%s」", custom)
			}
		}
	}
	if body := quoteSnippet(view.Body, quotedSnippetRunes); body != "" {
		fmt.Fprintf(&b, "；正文开头：「%s」", body)
	}
	return []string{b.String()}
}

// quoteSnippet 折叠空白并截到 n 个字符，用于引用说明里的开头预览。
func quoteSnippet(s string, n int) string {
	return truncateString(strings.Join(strings.Fields(s), " "), n)
}
