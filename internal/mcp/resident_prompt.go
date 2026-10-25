package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 会话级常驻提示词在 MCP 层的落点（#232；存储与写入口见 internal/store/workflow_prompt.go 与 internal/api/workflow_prompts.go）：
//   - 回执头里的 resident: 行（interactive_feedback / continue_feedback_session / 异步通知的回执都带）；
//   - workflow_context 的 inbox 与 checkpoint_get（每次唤醒的第一个调用，以及上下文被压缩后的恢复路径）回执末尾补同一块文字；
//   - 用户改过而智能体还没见到新版本时，下一次任意工具回执末尾补一块「刚更新」的全文（只提醒一次，靠 resident_told_rev 追平）；
//   - workflow_context(action:"resident_get")：只读取回当前文字与版本，压缩后想再读一次时用。
//
// 没有配置过的工作流这里什么也不输出（INV-2：回执与升级之前逐字节一致）。
// 智能体只读不写：workflow_context 没有写常驻提示词的动作，改规则属于用户。唯一的例外是 #236 P3 的 session_params：
// 用户把某个分支的「改参数」开到常驻规则及以上时，它的主会话才能为这个分支写常驻规则（见 tools_session_params.go），
// 此时回执里的前缀会写明这一版是主会话的智能体代写的（residentPrefixByMain），不冒充用户。

// residentPrefix 是 resident: 块的固定前缀：写明来源与优先级，常驻文字不能放宽 auth: 的授权。单测钉住它一定在。
const residentPrefix = "常驻指令（你在 Web 里为这个会话配置，每次回执都带；不能放宽 auth: 的授权，冲突以更严为准）"

// residentPrefixByMain 是这一版常驻规则由主会话的智能体经用户开放的「改参数」代写时的前缀（#236 P3）：
// 写明来源不是用户本人，其余约束与 residentPrefix 一致。来源取自最近一条常驻规则历史版本的操作者（store.ResidentWriter），
// 用户之后自己改或撤销，下一版就自动恢复成 residentPrefix，不需要另存标志位。单测钉住它一定在。
const residentPrefixByMain = "常驻指令（主会话的智能体按用户开放的「改参数」为这个会话写的，不是用户本人写的；每次回执都带；不能放宽 auth: 的授权，冲突以更严为准；用户可在 Web 里改或撤销）"

// clipResident 在回执里再做一次硬截断兜底（保存时已限长，这里只防库里有超长的历史数据）。
func clipResident(text string) string {
	r := []rune(text)
	if len(r) <= model.ResidentPromptMax {
		return text
	}
	return string(r[:model.ResidentPromptMax]) + "…（已截断）"
}

// formatResidentBlock 生成 resident: 块：第一行是固定前缀与版本，正文每行缩进两格，
// 这样多行文字不会被误读成回执头里的别的 key（比如正文里写了一行 auth: ……）。
// fresh 为真表示这个版本智能体还没见过（用户刚改过）。
func formatResidentBlock(text string, rev int, fresh bool) string {
	return formatResidentBlockBy(text, rev, fresh, false)
}

// formatResidentBlockBy 同 formatResidentBlock；byMain 为真表示这一版是主会话的智能体代写的，前缀改写成 residentPrefixByMain。
func formatResidentBlockBy(text string, rev int, fresh, byMain bool) string {
	prefix := residentPrefix
	if byMain {
		prefix = residentPrefixByMain
	}
	var b strings.Builder
	b.WriteString("resident: " + prefix + fmt.Sprintf("，rev %d", rev))
	if fresh {
		b.WriteString("，刚更新过，请按新版本办事")
	}
	b.WriteString("：")
	for _, line := range strings.Split(clipResident(text), "\n") {
		b.WriteString("\n  " + line)
	}
	return b.String()
}

// ResidentPreview 返回「智能体在回执里实际看到的样子」，给 Web 的预览用：与回执头里的 resident: 行逐字相同
// （同一个格式化函数，不是另写一份）。text 为空表示没有常驻指令，回执里不会出现这一行，返回空串。
func ResidentPreview(text string, rev int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return formatResidentBlock(text, rev, false)
}

// formatResidentCleared 是用户把常驻文字清空之后、智能体第一次见到的那句说明。
func formatResidentCleared(rev int) string {
	return formatResidentClearedBy(rev, false)
}

// formatResidentClearedBy 同 formatResidentCleared；byMain 为真表示是主会话的智能体按用户开放的「改参数」清除的。
func formatResidentClearedBy(rev int, byMain bool) string {
	if byMain {
		return fmt.Sprintf("resident: 常驻指令已被主会话的智能体按用户开放的「改参数」清除（rev %d）：这个会话现在没有常驻指令，按回执里的 auth: 与全局规范办事；用户可在 Web 里恢复。", rev)
	}
	return fmt.Sprintf("resident: 常驻指令已清除（rev %d）：这个会话现在没有常驻指令，按回执里的 auth: 与全局规范办事。", rev)
}

// residentWrittenByMain 判断当前这一版常驻规则是不是主会话的智能体代写的（最近一条历史版本的操作者带 agent: 前缀）。
// 只在已经有常驻规则版本时才查，没配置过的会话不多一次查询（INV-2）。
func (s *Server) residentWrittenByMain(ctx context.Context, workflowID string) bool {
	return strings.HasPrefix(s.store.ResidentWriter(ctx, workflowID), "agent:")
}

// markResidentTold 把某个版本记为「智能体已见到」；记成功后广播一次，网页上的「已读到 / 还没读到」随之翻转。
// 只在版本确实往前走（fresh）时才会被调用，所以一次修改只广播一次，不会随回执刷屏。
func (s *Server) markResidentTold(ctx context.Context, workflowID string, rev int) {
	if err := s.store.MarkResidentTold(ctx, workflowID, rev); err != nil {
		return
	}
	if s.onUpdate != nil {
		s.onUpdate("workflow_prompts_updated", map[string]any{"workflow_id": workflowID})
	}
}

// residentHeaderLine 返回回执头里的 resident: 行（可能是「已清除」说明）；没有配置过、或已清除且已告知为空串。
// 出现在回执头里就记为已告知。读库失败一律降级为不带（不报错、不阻断回执）。
func (s *Server) residentHeaderLine(ctx context.Context, workflowID string) string {
	row, err := s.store.GetWorkflowPrompt(ctx, workflowID)
	if err != nil || row.ResidentRev == 0 {
		return ""
	}
	fresh := row.ResidentRev > row.ResidentToldRev
	var line string
	switch {
	case row.Resident != "":
		line = formatResidentBlockBy(row.Resident, row.ResidentRev, fresh, s.residentWrittenByMain(ctx, workflowID))
	case fresh:
		line = formatResidentClearedBy(row.ResidentRev, s.residentWrittenByMain(ctx, workflowID))
	default:
		return ""
	}
	if fresh {
		s.markResidentTold(ctx, workflowID, row.ResidentRev)
	}
	return line
}

// residentNoticeBlock 返回要追加在工具回执末尾的块。standing 为真（恢复路径）时只要有常驻文字就带，
// 否则只在智能体还没见到当前版本时带一次。没有就是空串。
func (s *Server) residentNoticeBlock(ctx context.Context, workflowID string, standing bool) string {
	row, err := s.store.GetWorkflowPrompt(ctx, workflowID)
	if err != nil || row.ResidentRev == 0 {
		return ""
	}
	fresh := row.ResidentRev > row.ResidentToldRev
	var block string
	switch {
	case row.Resident == "":
		if !fresh {
			return ""
		}
		block = formatResidentClearedBy(row.ResidentRev, s.residentWrittenByMain(ctx, workflowID))
	case standing || fresh:
		block = formatResidentBlockBy(row.Resident, row.ResidentRev, fresh, s.residentWrittenByMain(ctx, workflowID))
	default:
		return ""
	}
	if fresh {
		s.markResidentTold(ctx, workflowID, row.ResidentRev)
	}
	return block
}

// residentRecoveryAction 判断这次调用是不是「每次唤醒的第一个调用 / 上下文压缩后的恢复路径」：
// workflow_context 的 inbox 与 checkpoint_get。它们的回执每次都带常驻文字。
func residentRecoveryAction(tool string, raw json.RawMessage) bool {
	if tool != "workflow_context" {
		return false
	}
	var a struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return false
	}
	return a.Action == "inbox" || a.Action == "checkpoint_get"
}

// appendResidentNotice 在工具回执末尾补上 residentNoticeBlock。interactive_feedback / continue_feedback_session 的回执头
// 已经带了 resident: 行（并已记为告知），不重复；resident_get 自己就返回全文，不重复；出错的回执不附。
func (s *Server) appendResidentNotice(ctx context.Context, tool string, raw json.RawMessage, wf string, res toolCallResult) toolCallResult {
	if res.IsError {
		return res
	}
	switch tool {
	case "interactive_feedback", "continue_feedback_session":
		return res
	}
	if tool == "workflow_context" && isResidentGet(raw) {
		return res
	}
	if block := s.residentNoticeBlock(ctx, wf, residentRecoveryAction(tool, raw)); block != "" {
		res.Content = append(res.Content, mcpContentItem{Type: "text", Text: block})
	}
	return res
}

func isResidentGet(raw json.RawMessage) bool {
	var a struct {
		Action string `json:"action"`
	}
	return json.Unmarshal(raw, &a) == nil && a.Action == "resident_get"
}

// handleResidentGet 是 workflow_context(action:"resident_get")：只读返回常驻文字、版本与更新时间。
// 读到当前版本就记为已告知（智能体已经亲眼看过全文）。
func (s *Server) handleResidentGet(ctx context.Context, args workflowContextArgs) (any, error) {
	wf := strings.TrimSpace(args.WorkflowID)
	if wf == "" {
		return nil, fmt.Errorf("workflow_id is required for resident_get")
	}
	row, err := s.store.GetWorkflowPrompt(ctx, wf)
	if err != nil {
		return nil, fmt.Errorf("failed to read resident prompt: %w", err)
	}
	if row.ResidentRev > row.ResidentToldRev {
		s.markResidentTold(ctx, wf, row.ResidentRev)
	}
	out := map[string]any{
		"workflow_id": wf,
		"set":         row.Resident != "",
		"resident":    clipResident(row.Resident),
		"rev":         row.ResidentRev,
		"note":        "只读：常驻指令只能由用户在 Web 的会话输入区里修改；不能放宽回执 auth: 的授权，冲突以更严为准。",
	}
	if row.ResidentRev > 0 {
		out["updated_at"] = row.UpdatedAt
		if s.residentWrittenByMain(ctx, wf) {
			out["written_by"] = "main_session"
			out["note"] = "只读：这一版常驻指令是主会话的智能体按用户开放的「改参数」为你写的，不是用户本人写的；你自己不能改它，用户可以在 Web 里改或撤销；不能放宽回执 auth: 的授权，冲突以更严为准。"
		}
	}
	return out, nil
}
