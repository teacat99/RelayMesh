package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// async_ask：不阻塞的确认项（Q12）。智能体提一个问题就继续干活，用户在 Web 的「待确认」区作答，
// 答复经收件箱（kind=answer）回到智能体。
//
// 风险档（#217，用户在 Q68 批准方案 A）：
//   - L0 / L1 是可回滚的决定，可以带 default_option，智能体先按它推进可回滚的部分；
//   - L2 是不可逆 / 高风险的决定（部署、推送远端、删数据、改凭据、不可逆迁移、发布）。异步对话里它的默认做法也是
//     发确认卡、不阻塞其余工作，但「批准前绝不执行这一步，没有答复就是不批准」。服务端对 L2 守住这几条（INV-3）：
//     不能带 default_option（默认就是不做）、不进批量、不能一键采用推荐、没有自动过期；批准只可能来自用户
//     ——智能体没有任何作答确认项的工具。「批准前不执行」本身仍靠智能体守约（动手的是它），这点与阻塞式
//     interactive_feedback 相同。同步模式的阻塞确认不变。

type asyncAskArgs struct {
	Action        string                `json:"action,omitempty"`
	WorkflowID    string                `json:"workflow_id"`
	Title         string                `json:"title,omitempty"`
	Body          string                `json:"body,omitempty"`
	Options       []model.ConfirmOption `json:"options,omitempty"`
	Multi         bool                  `json:"multi,omitempty"`
	AllowNote     bool                  `json:"allow_note,omitempty"`
	DefaultOption string                `json:"default_option,omitempty"`
	Risk          string                `json:"risk,omitempty"`
	Kind          string                `json:"kind,omitempty"`
	TaskID        looseInt              `json:"task_id,omitempty"`
	DocPath       string                `json:"doc_path,omitempty"`
	Anchor        string                `json:"anchor,omitempty"`
	Ref           string                `json:"ref,omitempty"`
	Status        string                `json:"status,omitempty"`
}

// asyncAskL2NoDefault：L2 确认项不能带 default_option。default_option 的意思是「我等待期间先按它推进」，
// 对不可逆的决定这句话本身就是错的——默认只能是「不做」。
const asyncAskL2NoDefault = "risk=L2 的确认项不能带 default_option：不可逆或高风险的决定没有「先按默认值做」，" +
	"用户明确批准之前一律不执行；去掉 default_option 再提交，并先做不依赖它的事。"

// asyncAskL2Notice 是 L2 确认项创建成功后随结果返回的提醒（l2_notice）。ref 是刚建好的 Q 编号。
func asyncAskL2Notice(ref string) string {
	return "这是 L2（不可逆 / 高风险）确认项：用户明确批准之前绝不执行这一步，没有答复就是不批准；它没有默认值、不进批量、不会自动过期。" +
		"先做不依赖它的事，不要为它停下来等；到收尾还没批准，就把依赖它的任务置「暂缓」并写明在等 " + ref + "。"
}

func (s *Server) handleAsyncAsk(ctx context.Context, args json.RawMessage) (any, error) {
	var a asyncAskArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("参数无效: %w", err)
	}
	a.WorkflowID = strings.TrimSpace(a.WorkflowID)
	if a.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id 必填（用回执头里的 workflow_id）")
	}
	switch action := strings.TrimSpace(a.Action); action {
	case "", "create":
		return s.asyncAskCreate(ctx, a)
	case "get":
		return s.asyncAskGet(ctx, a)
	case "list":
		return s.asyncAskList(ctx, a)
	case "withdraw":
		return s.asyncAskWithdraw(ctx, a)
	default:
		return nil, fmt.Errorf("未知 action：%s（可用 create / get / list / withdraw）", action)
	}
}

// assignOptionIDs 给没写 id 的选项按位置补 a、b、c……（智能体常常只写文案）。
func assignOptionIDs(in []model.ConfirmOption) []model.ConfirmOption {
	out := make([]model.ConfirmOption, len(in))
	copy(out, in)
	for i := range out {
		if strings.TrimSpace(out[i].ID) == "" && i < 26 {
			out[i].ID = string(rune('a' + i))
		}
	}
	return out
}

func asyncAskUsageHint() string {
	return "答复会作为收件箱消息（确认项答复）送达：回执末尾出现 inbox: 提示行或 hook 提醒时，调用 workflow_context(action:\"inbox\") 读取；" +
		"也可以随时 async_ask get / list 查询。default_option 只是你等待期间的工作假设，不是用户的答复。"
}

// asyncAskUsageHintL2 是 L2 确认项的用法提示：同样讲清答复怎么读，但没有 default_option 那一句，
// 换成「没有明确批准就不执行」——选项文案由智能体自己写，批不批准以用户读到的那一项为准。
func asyncAskUsageHintL2() string {
	return "答复会作为收件箱消息（确认项答复）送达：回执末尾出现 inbox: 提示行或 hook 提醒时，调用 workflow_context(action:\"inbox\") 读取；" +
		"也可以随时 async_ask get / list 查询。只有答复里明确选了「批准」那一类的选项才算批准；没有答复、答复含糊或选了别的，都按未批准处理。"
}

func (s *Server) asyncAskCreate(ctx context.Context, a asyncAskArgs) (any, error) {
	// 没写 risk 默认按 L1（中间档）处理；写了的话只接受 L0 / L1 / L2。
	risk := model.ConfirmRiskL1
	if r := strings.ToUpper(strings.TrimSpace(a.Risk)); r != "" {
		if r != model.ConfirmRiskL0 && r != model.ConfirmRiskL1 && r != model.ConfirmRiskL2 {
			return nil, fmt.Errorf("risk 只能是 L0、L1 或 L2")
		}
		risk = r
	}
	// INV-3：L2 没有默认值。这一条先于其余校验，让智能体一次看到「不是补全参数就行，而是不能带默认值」。
	if risk == model.ConfirmRiskL2 && strings.TrimSpace(a.DefaultOption) != "" {
		return nil, errors.New(asyncAskL2NoDefault)
	}
	kind := strings.TrimSpace(a.Kind)
	if kind == "" {
		kind = model.ConfirmKindAsk
	}
	if kind != model.ConfirmKindAsk && kind != model.ConfirmKindPlan {
		return nil, fmt.Errorf("kind 只能是 ask 或 plan")
	}
	var docID uint
	if p := strings.TrimSpace(a.DocPath); p != "" {
		doc, err := s.store.GetProjectDocByRef(ctx, a.WorkflowID, p)
		if err != nil {
			return nil, fmt.Errorf("doc_path 无效：%w", err)
		}
		docID = doc.ID
	}
	item, created, err := s.store.CreateConfirmItem(ctx, store.ConfirmCreateInput{
		WorkflowID: a.WorkflowID, Kind: kind, Title: a.Title, Body: a.Body,
		Options: assignOptionIDs(a.Options), Multi: a.Multi, AllowNote: a.AllowNote,
		DefaultOption: a.DefaultOption, Risk: risk, TaskSeq: int(a.TaskID),
		DocID: docID, Anchor: strings.TrimSpace(a.Anchor), CreatedBy: model.ConfirmCreatedByAgent,
	})
	if err != nil {
		return nil, err
	}
	windowLine := ""
	if created {
		s.notifyConfirmUpdated(item)
		s.notifyAsk(item) // 通知闸：ask_card，受免打扰与会话静音约束
		windowLine = s.extendReplyWindow(ctx, a.WorkflowID)
	}
	pending, _ := s.store.CountPendingConfirms(ctx, a.WorkflowID)
	out := map[string]any{
		"ref": model.ConfirmRef(item.Seq), "request_id": model.ConfirmRef(item.Seq),
		"status": item.Status, "created": created, "pending_count": pending,
		"hint": asyncAskUsageHint(),
	}
	if windowLine != "" {
		out["window"] = windowLine
	}
	if !created {
		out["note"] = "同一问题（标题与选项相同）仍在待处理，没有重复创建，返回的是已有的那一条。"
	}
	if item.DefaultOption != "" {
		out["default_option"] = item.DefaultOption
	}
	// L2：回执里把「批准前绝不执行」说死，并把默认值那一句换掉（#217）。去重命中已有 L2 项时同样带上。
	if item.Risk == model.ConfirmRiskL2 {
		out["hint"] = asyncAskUsageHintL2()
		out["approval_required"] = true
		out["l2_notice"] = asyncAskL2Notice(model.ConfirmRef(item.Seq))
	}
	if pending > model.ConfirmPendingWarn {
		out["merge_hint"] = fmt.Sprintf("这个工作流现在有 %d 条待处理的确认项，尽量把相关问题合并成一条，别让用户逐条点。", pending)
	}
	return out, nil
}

func (s *Server) asyncAskGet(ctx context.Context, a asyncAskArgs) (any, error) {
	ref := strings.TrimSpace(a.Ref)
	if ref == "" {
		return nil, fmt.Errorf("get 需要 ref（如 Q12）")
	}
	item, err := s.store.GetConfirmItemByRef(ctx, a.WorkflowID, ref)
	if err != nil {
		return nil, err
	}
	view, err := s.store.ConfirmView(ctx, item)
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": view}, nil
}

func (s *Server) asyncAskList(ctx context.Context, a asyncAskArgs) (any, error) {
	var statuses []string
	switch st := strings.TrimSpace(a.Status); st {
	case "":
		statuses = []string{model.ConfirmStatusPending}
	case "all":
	default:
		statuses = []string{st}
	}
	list, err := s.store.ListConfirmItems(ctx, store.ConfirmFilter{
		WorkflowID: a.WorkflowID, Statuses: statuses, Ascending: true, Limit: 100,
	})
	if err != nil {
		return nil, err
	}
	views, err := s.store.ConfirmViews(ctx, list)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": views, "count": len(views)}, nil
}

func (s *Server) asyncAskWithdraw(ctx context.Context, a asyncAskArgs) (any, error) {
	ref := strings.TrimSpace(a.Ref)
	if ref == "" {
		return nil, fmt.Errorf("withdraw 需要 ref（如 Q12）")
	}
	item, err := s.store.WithdrawConfirmItem(ctx, a.WorkflowID, ref, true)
	if err != nil {
		return nil, err
	}
	s.notifyConfirmUpdated(item)
	view, err := s.store.ConfirmView(ctx, item)
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": view}, nil
}

func (s *Server) notifyConfirmUpdated(item *model.ConfirmItem) {
	if s.onUpdate == nil || item == nil {
		return
	}
	payload := map[string]any{
		"workflow_id": item.WorkflowID, "seq": item.Seq, "ref": model.ConfirmRef(item.Seq), "status": item.Status,
	}
	// 与 api.broadcastConfirmUpdated 同口径：带上这个工作流此刻待处理的条数，侧栏会话卡直接校准（#159）
	if s.store != nil {
		if n, err := s.store.CountPendingConfirms(context.Background(), item.WorkflowID); err == nil {
			payload["pending_count"] = n
		}
	}
	s.onUpdate("confirm_updated", payload)
}
