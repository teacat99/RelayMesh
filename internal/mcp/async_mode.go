package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/asyncmode"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 异步对话模式在 MCP 层的落点（判定与文案在 internal/asyncmode，存储在 internal/store/workflow_mode.go）：
//   - 回执头里的 mode: / async: / auth: 行（只对开启过异步的工作流出现，INV-2）；
//   - 模式变化后下一次任意工具回执里补一段说明（只提醒一次），让智能体在工作中途就知道升级 / 降级了；
//   - interactive_feedback 的 wait:false：异步生效时发布不阻塞的 notice，否则忽略 wait:false、照常阻塞并说明原因。

// waitFlowsInstructionsSection 是 MCP 指引里「等待用户回复的两种流程」一节（#121 / #123）。
// 真正的时长与步骤一直由回执给出（等待回执、【收尾宽限】、async: 契约、window: 行），这一节只在 initialize 时
// 把两种流程的词汇与分界讲清楚：回执出现什么行、该怎么等、什么时候才能收尾。
// 它不放进用户可编辑的全局指引（与任务清单、收件箱、引用链接几节一样追加在后面，且固定在最末尾），只有能调用 interactive_feedback 的令牌才收到。
const waitFlowsInstructionsSection = `

13. 等待用户回复的两种流程（时长与步骤一律以回执为准，由服务端按用户设置算好，不要自行改）：
   - 同步 1:1（回执头没有 mode: async）：interactive_feedback 发出后收到 === 等待回执 === 时，用计时器 AwaitShell({ block_until_ms: 回执给的毫秒数 }) 等待，结束后立即调用 continue_feedback_session；回执末尾的【收尾宽限】写明这是第几轮空等、共几轮，只有出现 === 反馈超时 === 才进入收尾，期间用户来了消息就清零继续干活；用户在 Web 点了「+1 分钟」或正在输入时，服务端会自动继续挂着、不发空回执，你只管继续等；
   - 异步对话（回执头 mode: async，并带 async: 契约行）：不要为问话停下来等，进度用 interactive_feedback 且 wait:false 发布，用户的答复由 hook 送达并放进收件箱（workflow_context action:"inbox"）。回执里的 window: 行是「回复窗口」：你每发一条新消息服务端自动给它加时间（累计有上限），用户一回复就清零，有进行中 / 验证中的任务时它暂停（必须等用户拍板的任务置「暂缓」并写明在等什么，不挡窗口也不挡收尾）；手头没有别的事时，按 window: 行的剩余时间用计时器等待，到点后读收件箱，仍无消息且窗口未到期就按新的剩余时间再等，窗口到期且收件箱为空才收尾；用户输入或点「+1 分钟」时窗口会被顺延，以最新回执的时间为准；
   - 无论哪种流程：等待只能用 AwaitShell 计时器，禁止 shell sleep，也不要用 chat 正文代替等待；不要把回执或自己生成的文字当成用户反馈。`

// authProfileOf 把存储里的档位换成回执文案需要的信息；没有档位（不应出现）时给一个保守的空档位。
func authProfileOf(p *model.AuthProfile) asyncmode.Profile {
	if p == nil {
		return asyncmode.Profile{Name: "默认", Level: model.AuthLevelL1}
	}
	return asyncmode.Profile{Name: p.Name, Level: p.Level, Rules: p.Rules}
}

// modeHeaderLines 返回某个工作流回执头里 mode 相关的行与 auth 行。
// 同时记下「这个状态已经告诉过智能体了」，免得紧接着的工具回执再重复一遍。
func (s *Server) modeHeaderLines(ctx context.Context, workflowID string) (mode []string, auth string) {
	info, err := s.store.WorkflowModeInfo(ctx, workflowID, time.Now())
	if err != nil || !info.State.Ever {
		return nil, ""
	}
	s.markModeTold(ctx, info)
	mode, auth = asyncmode.ReceiptLines(info.State, authProfileOf(info.Profile))
	// 异步生效且回复窗口还在走：把「剩余多久、怎么等」写进每条回执（#123），智能体不用自己算。
	if line := s.windowLineFor(ctx, info); line != "" {
		mode = append(mode, line)
	}
	return mode, auth
}

// windowLineFor 返回异步生效的工作流此刻的 window: 行；同步、没有窗口都为空串。
// 先按任务情况对齐窗口（有任务就暂停、任务清零再接着走），对齐有变化时通知网页刷新。
func (s *Server) windowLineFor(ctx context.Context, info store.ModeInfo) string {
	if !info.State.Effective {
		return ""
	}
	now := time.Now()
	w, changed, err := s.store.ReconcileReplyWindow(ctx, info.WorkflowID, now)
	if err != nil {
		return ""
	}
	if changed && s.onUpdate != nil {
		s.onUpdate("workflow_mode_updated", map[string]string{"workflow_id": info.WorkflowID})
	}
	_, slice := s.store.AsyncWindowTimes(ctx)
	return asyncmode.WindowLine(w, now, slice)
}

// extendReplyWindow 智能体发了一条新消息：异步生效时把回复窗口累加一段，并返回新的 window: 行；其余情况返回空串。
func (s *Server) extendReplyWindow(ctx context.Context, workflowID string) string {
	now := time.Now()
	info, err := s.store.WorkflowModeInfo(ctx, workflowID, now)
	if err != nil || !info.State.Effective {
		return ""
	}
	add, slice := s.store.AsyncWindowTimes(ctx)
	w, err := s.store.ExtendReplyWindow(ctx, workflowID, add, now)
	if err != nil || w.Empty() {
		return ""
	}
	if s.onUpdate != nil {
		s.onUpdate("workflow_mode_updated", map[string]string{"workflow_id": workflowID})
	}
	return asyncmode.WindowLine(w, now, slice)
}

// afterTaskChange 任务清单变了（智能体经 task_board / workflow_context 改了任务）：对齐回复窗口，
// 让「最后一个任务做完 → 窗口开始走」在这一刻发生，而不是等到下次有人读取。
func (s *Server) afterTaskChange(ctx context.Context, raw json.RawMessage) {
	var a struct {
		WorkflowID string `json:"workflow_id"`
	}
	if json.Unmarshal(raw, &a) != nil || strings.TrimSpace(a.WorkflowID) == "" {
		return
	}
	if _, changed, err := s.store.ReconcileReplyWindow(ctx, a.WorkflowID, time.Now()); err == nil && changed && s.onUpdate != nil {
		s.onUpdate("workflow_mode_updated", map[string]string{"workflow_id": a.WorkflowID})
	}
}

func (s *Server) markModeTold(ctx context.Context, info store.ModeInfo) {
	sig := info.Signature()
	if sig == "" || sig == info.Row.ToldSig {
		return
	}
	_ = s.store.MarkModeTold(ctx, info.WorkflowID, sig)
}

// modeNoticeBlock 在模式状态变化后、智能体还没被告知时，给出一段与回执头里一致的说明；其余时候返回空串。
// 它作为独立的文本块追加在工具回执后面（和 inbox: / hook: 提示一样），不会碰坏 JSON 回执。
func (s *Server) modeNoticeBlock(ctx context.Context, workflowID string) string {
	info, err := s.store.WorkflowModeInfo(ctx, workflowID, time.Now())
	if err != nil || !info.State.Ever {
		return ""
	}
	sig := info.Signature()
	if sig == info.Row.ToldSig {
		return ""
	}
	mode, auth := asyncmode.ReceiptLines(info.State, authProfileOf(info.Profile))
	lines := append([]string{}, mode...)
	if auth != "" {
		lines = append(lines, auth)
	}
	if len(lines) == 0 {
		return ""
	}
	s.markModeTold(ctx, info)
	return strings.Join(lines, "\n")
}

// appendModeNotice 在工具回执末尾补上 modeNoticeBlock。interactive_feedback / continue_feedback_session 的回执头
// 已经带了完整的 mode 行（并已记为告知），不重复；出错的回执不附。
func (s *Server) appendModeNotice(ctx context.Context, tool, wf string, res toolCallResult) toolCallResult {
	if res.IsError {
		return res
	}
	switch tool {
	case "interactive_feedback", "continue_feedback_session":
		return res
	}
	if block := s.modeNoticeBlock(ctx, wf); block != "" {
		res.Content = append(res.Content, mcpContentItem{Type: "text", Text: block})
	}
	return res
}

// waitIgnoredNote 是 wait:false 没有生效（工作流不在有效的异步模式）时，写进回执头的一行说明。
func waitIgnoredNote(st asyncmode.State) string {
	reason := "这个工作流没有开启异步对话"
	if st.Ever {
		reason = asyncmode.ReasonText(st.Reason)
	}
	return "wait_ignored: wait:false 只在异步对话生效时才不阻塞；" + reason + "，本次已按普通汇报处理（会等待用户回复）。"
}

// noticeRatePerHour 是每个工作流每小时最多发布的进度通知条数：一个失控的循环不该把时间线刷成一面墙。
const noticeRatePerHour = 60

// explicitFalse 只在 wait 明确写成 false（false / "false" / 0 / "0" / "no"）时为真；
// 缺省、null、true、空串、乱写都按「照常等待」处理——宁可多等，也不误放行成不阻塞。
func explicitFalse(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	switch strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`)) {
	case "false", "0", "no":
		return true
	}
	return false
}

// joinHeaderNotes 把回执头里要追加的几行（锚点、wait_ignored……）拼起来，跳过空行。
func joinHeaderNotes(notes ...string) string {
	var out []string
	for _, n := range notes {
		if n != "" {
			out = append(out, n)
		}
	}
	return strings.Join(out, "\n")
}

// anchorNoteFor 生成回执里的 anchor_saved / anchor_warning 行；没有传 commit 时为空串。
func (s *Server) anchorNoteFor(ctx context.Context, sess *model.FeedbackSession, hash, subject string, ok, warn bool) string {
	switch {
	case ok:
		previous, _ := s.store.GetLatestAnchoredSession(ctx, sess.WorkflowID, sess.ID)
		return formatAnchorSaved(hash, previous)
	case warn:
		return anchorWarning
	}
	return ""
}

// applyReportedPhase 落库智能体随汇报报告的阶段并广播。旧五段式的阶段 id 仍被接受，
// 落库与广播都用换算后的新 id（界面按新 id 高亮阶段）。
func (s *Server) applyReportedPhase(ctx context.Context, workflowID, rawPhase string) {
	if rawPhase == "" || workflowID == "" {
		return
	}
	phase := model.CanonicalPhaseID(rawPhase)
	if err := s.store.SetWorkflowPhase(ctx, workflowID, phase, false); err == nil && s.onUpdate != nil {
		s.onUpdate("phase_changed", map[string]string{
			"workflow_id": workflowID,
			"phase":       phase,
		})
	}
}

// publishNotice 发布一条不阻塞的进度 / 回答气泡并立即返回。调用方已确认这个工作流处于有效的异步模式。
func (s *Server) publishNotice(ctx context.Context, args interactiveFeedbackArgs, anchorHash, anchorSubject string, anchorOK, anchorWarn bool) (any, error) {
	session, err := s.createNotice(ctx, args, anchorHash, anchorSubject)
	if err != nil {
		return nil, err
	}
	anchorNote := s.anchorNoteFor(ctx, session, anchorHash, anchorSubject, anchorOK, anchorWarn)
	return injectHeaderLine(s.formatNoticeResult(ctx, session), anchorNote), nil
}

// createNotice 建一条 notice 会话并做好所有副作用（限流、阶段、网页刷新、通知闸），返回这条会话。
// interactive_feedback(wait:false) 与 chat(action:"send") 共用这一份，两条路径的行为不会分叉。
func (s *Server) createNotice(ctx context.Context, args interactiveFeedbackArgs, anchorHash, anchorSubject string) (*model.FeedbackSession, error) {
	wf := strings.TrimSpace(args.WorkflowID)
	n, err := s.store.CountRecentNotices(ctx, wf, time.Now().Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	if n >= noticeRatePerHour {
		return nil, store.NewConflictError(fmt.Sprintf("这个工作流一小时内已发布 %d 条进度通知，达到上限；请把多条进度合并成一条再发布，或改用不带 wait:false 的 interactive_feedback 汇报。", n), 0)
	}

	credCtx := CredentialFromContext(ctx)
	credHostName := ""
	var credID uint
	if credCtx != nil {
		credHostName = credCtx.HostName
		credID = credCtx.CredentialID
	}
	session, err := s.store.CreateNoticeSession(ctx, store.CreateSessionInput{
		WorkflowID:         wf,
		CredentialID:       credID,
		EnvHostName:        s.cfg.HostName,
		CredentialHostName: credHostName,
		ProjectDirectory:   args.ProjectDirectory,
		Title:              args.Title,
		Summary:            args.Summary,
		GitCommit:          anchorHash,
		GitSubject:         anchorSubject,
	})
	if err != nil {
		return nil, err
	}
	s.applyReportedPhase(ctx, session.WorkflowID, args.Phase)
	// 异步对话：新消息让回复窗口累加一段（#123）；之后组装回执头时会带上新的 window: 行。
	s.extendReplyWindow(ctx, wf)

	// 网页刷新照旧；另外把它作为 agent_text 交给通知闸（设计稿 07 §4）。没有人「在等」，所以不走 notifyNewRequest——
	// 它受免打扰与会话静音约束，不像「有人在等你」那样穿透。
	s.notifySessionUpdate(session.ID)
	s.notifyNotice(session)
	return session, nil
}

// formatNoticeResult 是 wait:false 发布成功的回执：与普通回执同样的头（含 mode / auth 行），随后一句说明。
func (s *Server) formatNoticeResult(ctx context.Context, sess *model.FeedbackSession) string {
	header := s.formatSessionHeaderWithContext(ctx, sess)
	return header + "\n=== 已发布（不阻塞）===\n" +
		"这条内容已作为进度通知发布到 Web 会话页（session_id: " + sess.ID + "），没有等待回复，请继续你的工作。\n" +
		"用户的追加与答复由 hook 在工具调用之间送达；回执末尾出现 inbox: 行，或想主动确认时，调用 workflow_context(action: \"inbox\", workflow_id: \"" + sess.WorkflowID + "\") 读取。"
}

// addKindField 只给 notice 带出 kind：常规汇报的条目与以前逐字节一致（INV-2）。
func addKindField(item map[string]any, sess *model.FeedbackSession) {
	if sess.Kind == model.FeedbackKindNotice {
		item["kind"] = model.FeedbackKindNotice
	}
}
