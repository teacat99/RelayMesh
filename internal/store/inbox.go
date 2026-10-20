package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 追加指令收件箱（D-210）。
//
// 用户在智能体没有在等待的时候从 Web 追加的消息，过去会被合并成一条、只有智能体下次发起交互时才被取走，
// 中间隔多久、它读到了没有，谁都不知道。现在每条追加独立一行，各自带状态：
//
//	queued（已排队）→ delivered（Agent 读到了全文）→ acked（Agent 已确认）
//	旁支：revoked（读到之前撤回）、retracted（读到之后撤回，会通知 Agent 忽略）、expired（超过保留期）
//
// 智能体发起交互（interactive_feedback）时，所有 queued 的消息按时间顺序合并成一次「秒回」，行为与过去一致。

const (
	// InboxExpireAfter 是未被取走的追加的保留期；超过后标为 expired，不再送达。
	InboxExpireAfter = 7 * 24 * time.Hour
	// InboxMaxReminders 是每条消息最多让 hook 提醒几次。
	InboxMaxReminders = 3
	// InboxRecentWindow 是智能体用 inbox recent=true 找回「最近已确认的追加」时回看多久。
	InboxRecentWindow = 24 * time.Hour
	// InboxListMax 是一次最多返回给 Web 的条数。追加信息在会话页里是对话中的永久气泡（不再是几天后消失的底部提示条），
	// 要能回看整个工作流的历史，所以放宽到 200；超出时保留最近的。
	InboxListMax = 200
	// InboxMaxPending 是同一个工作流里未确认的追加条数上限（防止误操作或脚本无限堆积）。
	InboxMaxPending = 100
	// InboxPreviewRunes 是提示行 / hook 里给出的预览长度。
	InboxPreviewRunes = 160

	// inboxViaWindow：智能体读取收件箱前这段时间内收到过提醒，就把读取归因到那条提醒路径。
	inboxViaWindow = 30 * time.Minute
	// receiptHintThrottle：回执提示行的「最近一次提醒」时间戳至多每分钟写一次，避免每次工具调用都写库。
	receiptHintThrottle = time.Minute
)

// 一条追加是经哪条路径被智能体发现的。
const (
	InboxViaFeedback = "feedback" // 智能体发起交互时随「秒回」带走
	InboxViaHook     = "hook"     // Cursor hook 提醒后读取
	InboxViaReceipt  = "receipt"  // 工具回执里的 inbox: 提示行提醒后读取
	InboxViaTool     = "tool"     // 智能体自己调用 inbox 读取
)

// inboxReminderGap[i] 是第 i+1 次 hook 提醒与上一次之间至少要隔多久（第 1 次立即）。
var inboxReminderGap = [InboxMaxReminders]time.Duration{0, 5 * time.Minute, 15 * time.Minute}

// QueueFeedbackInput 是 Web 上追加一条消息的入参。
type QueueFeedbackInput struct {
	WorkflowID       string               `json:"workflow_id"`
	HostName         string               `json:"host_name,omitempty"`
	ProjectDirectory string               `json:"project_directory,omitempty"`
	ResponseText     string               `json:"response_text"`
	UserMessages     []string             `json:"user_messages,omitempty"`
	Images           []model.SessionImage `json:"images,omitempty"`
	Kind             string               `json:"kind,omitempty"` // append（默认）| answer（确认项答复，不并入 interactive_feedback 秒回）
}

// QueueWorkflowFeedback 把一条追加存进收件箱。每条独立一行，不再合并。
func (s *Store) QueueWorkflowFeedback(ctx context.Context, input QueueFeedbackInput) (*model.QueuedFeedback, error) {
	if input.WorkflowID == "" {
		return nil, NewInvalidInputError("workflow_id is required")
	}
	if strings.TrimSpace(input.ResponseText) == "" && len(input.UserMessages) == 0 && len(input.Images) == 0 {
		return nil, NewInvalidInputError("追加内容为空：请写点文字、选个快捷标签或附一张图")
	}

	var q model.QueuedFeedback
	now := time.Now()
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := expireInboxTx(tx, input.WorkflowID, now); err != nil {
			return err
		}
		// 上限只约束用户的追加；确认项答复由系统投递、数量受确认项本身约束，不占用户的额度。
		var pending int64
		if err := tx.Model(&model.QueuedFeedback{}).
			Where("workflow_id = ? AND status = 'pending' AND (kind IS NULL OR kind NOT IN ?)", input.WorkflowID, systemInboxKinds).
			Count(&pending).Error; err != nil {
			return err
		}
		if pending >= InboxMaxPending {
			return NewConflictError(fmt.Sprintf("这个工作流的收件箱里已有 %d 条未确认的追加，请先撤回不需要的，或等智能体处理后再追加", pending), 0)
		}
		kind := strings.TrimSpace(input.Kind)
		if kind == "" {
			kind = "append"
		}
		q = model.QueuedFeedback{
			WorkflowID:       input.WorkflowID,
			HostName:         input.HostName,
			ProjectDirectory: input.ProjectDirectory,
			ResponseText:     input.ResponseText,
			UserMessages:     model.StringArray(input.UserMessages),
			Images:           model.SessionImages(input.Images),
			Status:           "pending",
			Kind:             kind,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		return tx.Create(&q).Error
	})
	if err != nil {
		return nil, err
	}
	// 用户回复了：异步对话的回复窗口清零（#123）；清零失败不影响追加本身。
	_ = s.ClearReplyWindow(ctx, input.WorkflowID)
	q.State = q.DeriveState()
	return &q, nil
}

// RevokeQueuedFeedback 撤回一条追加。尚未被读取的直接作废；已被读取但没确认的标为撤回，
// 并在下一次读取收件箱时告诉智能体忽略它。已确认的不能撤回。
func (s *Store) RevokeQueuedFeedback(ctx context.Context, queuedID uint) (*RevokedFeedbackResult, error) {
	var res RevokedFeedbackResult
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		var q model.QueuedFeedback
		if err := tx.Where("id = ?", queuedID).First(&q).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return NewNotFoundError(fmt.Sprintf("queued feedback %d not found", queuedID))
			}
			return err
		}
		if q.Status != "pending" {
			return NewConflictError(fmt.Sprintf("queued feedback %d cannot be revoked (status: %s)", queuedID, q.Status), 0)
		}
		if inboxMessageKind(q.Kind) == model.InboxKindAnswer {
			return NewConflictError("确认项答复提交后不能撤回；如果答错了，请在交流页给智能体发一条追加说明来更正", 0)
		}
		if inboxMessageKind(q.Kind) == model.InboxKindChatMention {
			return NewConflictError("群聊里的提醒不能在这里撤回", 0)
		}

		now := time.Now()
		res = RevokedFeedbackResult{
			WorkflowID:   q.WorkflowID,
			QueuedID:     q.ID,
			ResponseText: q.ResponseText,
			UserMessages: []string(q.UserMessages),
			Images:       []model.SessionImage(q.Images),
			Delivered:    q.DeliveredAt != nil,
		}
		q.Status = "revoked"
		q.UpdatedAt = now
		if q.DeliveredAt != nil {
			q.RetractedAt = &now
		}
		return tx.Save(&q).Error
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// ListPendingQueuedFeedbacks 列出还没有被智能体确认的追加（含已读取未确认的）。保留给旧调用方。
func (s *Store) ListPendingQueuedFeedbacks(ctx context.Context, workflowID string) ([]model.QueuedFeedback, error) {
	var list []model.QueuedFeedback
	query := s.db.WithContext(ctx).Where("status = 'pending'")
	if workflowID != "" {
		query = query.Where("workflow_id = ?", workflowID)
	}
	if err := query.Order("created_at ASC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	fillInboxStates(list)
	return list, nil
}

// ListInboxItems 返回 Web 上要展示的追加信息（按时间从早到晚）。它们在会话页里是对话中的永久气泡，所以：
//   - 未确认的（排队中 / 已读取未确认）全部在内；
//   - 已确认的只保留经提示行 / hook / 主动读取送达的，不再设时间窗口——随「秒回」带走的那些
//     已经原样出现在对话记录里，不重复展示，而经这三条路径送达的从来不会出现在对话记录里，不留下就查不到了；
//   - 过期的（保留期内一直没被读到）留一条记录，说明智能体始终没有看到它；
//   - 已撤回的不在其中。
//
// 最多 InboxListMax 条，超出时保留最近的。
func (s *Store) ListInboxItems(ctx context.Context, workflowID string) ([]model.QueuedFeedback, error) {
	if workflowID == "" {
		return nil, NewInvalidInputError("workflow_id is required")
	}
	now := time.Now()
	s.expireInbox(ctx, now)

	var list []model.QueuedFeedback
	err := s.db.WithContext(ctx).
		Where("workflow_id = ? AND (kind IS NULL OR kind NOT IN ?) AND ((status = 'pending' AND created_at >= ?) OR (status = 'consumed' AND delivered_via IN ('hook','receipt','tool')) OR status = 'expired')",
			workflowID, systemInboxKinds, inboxCutoff(now)).
		Order("created_at DESC, id DESC").
		Limit(InboxListMax).
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	fillInboxStates(list)
	// 已确认 / 已过期的条目不再内联图片数据：把 base64 去掉，省得每次刷新都往浏览器灌几 MB。
	// 但图片本身要继续能看（用户回看历史追加时需要核对当时附的截图），所以换成按需取图的直链：
	// 走 inbox-<编号> 的图片接口，带内容哈希，浏览器可以长期缓存，不会随列表刷新反复下载。
	for i := range list {
		if st := list[i].State; st == model.InboxStateAcked || st == model.InboxStateExpired {
			for j := range list[i].Images {
				img := &list[i].Images[j]
				if img.Hash == "" && img.Data != "" {
					img.Hash = imageContentHash(img.Data)
				}
				if img.URL == "" {
					img.URL = model.BuildSessionImageRelativeURL(InboxSessionID(list[i].ID), j, img.Hash)
				}
				img.Data = ""
			}
		}
	}
	return list, nil
}

// imageContentHash 取图片 base64 内容的短哈希，口径与会话图片一致（见 api.projectSessionImages）：
// 先去掉 data URL 前缀再哈希，同一张图在两处得到同一个值。
func imageContentHash(data string) string {
	raw := strings.TrimSpace(data)
	if comma := strings.Index(raw, ","); comma != -1 {
		raw = raw[comma+1:]
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// InboxSessionIDPrefix：收件箱里的图片对外用 inbox-<编号> 当作「会话编号」寻址，
// 这样 get_session_image 与 /sessions/:id/images/:index 不用新增一套接口就能取到追加里的图片。
const InboxSessionIDPrefix = "inbox-"

// InboxSessionID 把追加编号变成对外的图片寻址编号。
func InboxSessionID(queuedID uint) string { return fmt.Sprintf("%s%d", InboxSessionIDPrefix, queuedID) }

// ParseInboxSessionID 反过来解析；不是 inbox-<数字> 时 ok 为 false。
func ParseInboxSessionID(id string) (queuedID uint, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(id), InboxSessionIDPrefix)
	if !found || rest == "" {
		return 0, false
	}
	var n uint64
	for _, r := range rest {
		if r < '0' || r > '9' || n > 1<<40 {
			return 0, false
		}
		n = n*10 + uint64(r-'0')
	}
	return uint(n), n > 0
}

// GetQueuedFeedback 按编号取一条追加（含图片全文）。不存在时返回 (nil, nil)。
func (s *Store) GetQueuedFeedback(ctx context.Context, id uint) (*model.QueuedFeedback, error) {
	var q model.QueuedFeedback
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&q).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q.State = q.DeriveState()
	return &q, nil
}

// inboxBrief 是只为生成概况而读的几列。
type inboxBrief struct {
	ID           uint
	CreatedAt    time.Time
	Kind         string
	ResponseText string
	UserMessages model.StringArray
	ImageCount   int
}

// InboxSummary 是一个工作流收件箱的概况：提示行与 hook 都靠它。
type InboxSummary struct {
	Unread         int        `json:"unread"`                     // 排队中、智能体还没读到的条数
	UnreadMentions int        `json:"unread_mentions,omitempty"`  // 其中是群聊 @ 提醒（kind=chat_mention）的条数
	UnreadAnswers  int        `json:"unread_answers,omitempty"`   // 其中是确认项答复（kind=answer）的条数；提示行据此换措辞
	Delivered      int        `json:"delivered"`                  // 已读取、尚未确认的条数
	RetractNotices int        `json:"retract_notices"`            // 读取后被撤回、还没告诉智能体的条数
	OldestUnreadAt *time.Time `json:"oldest_unread_at,omitempty"` // 最早一条未读的时间
	LatestUnreadAt *time.Time `json:"latest_unread_at,omitempty"` // 最新一条未读的时间
	LatestPreview  string     `json:"latest_preview,omitempty"`   // 最新一条未读的预览（单行、截断）
}

// InboxSummary 统计一个工作流收件箱的现状。只读。
func (s *Store) InboxSummary(ctx context.Context, workflowID string, now time.Time) (InboxSummary, error) {
	var sum InboxSummary
	if workflowID == "" {
		return sum, nil
	}
	s.expireInbox(ctx, now)

	// 回执提示行在每次工具调用时都会走到这里，所以只取预览需要的列、图片只数个数，不把 base64 图片读进内存。
	var unread []inboxBrief
	if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Select("id, created_at, kind, response_text, user_messages, CASE WHEN json_valid(images) THEN json_array_length(images) ELSE 0 END AS image_count").
		Where("workflow_id = ? AND status = 'pending' AND delivered_at IS NULL AND created_at >= ?", workflowID, inboxCutoff(now)).
		Order("created_at ASC, id ASC").
		Scan(&unread).Error; err != nil {
		return sum, err
	}
	sum.Unread = len(unread)
	for i := range unread {
		switch inboxMessageKind(unread[i].Kind) {
		case model.InboxKindAnswer:
			sum.UnreadAnswers++
		case model.InboxKindChatMention:
			sum.UnreadMentions++
		}
	}
	if len(unread) > 0 {
		oldest, latest := unread[0], unread[len(unread)-1]
		sum.OldestUnreadAt = &oldest.CreatedAt
		sum.LatestUnreadAt = &latest.CreatedAt
		sum.LatestPreview = previewOf(latest.ResponseText, latest.UserMessages, latest.ImageCount)
	}

	var delivered, retracted int64
	if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Where("workflow_id = ? AND status = 'pending' AND delivered_at IS NOT NULL AND created_at >= ?", workflowID, inboxCutoff(now)).
		Count(&delivered).Error; err != nil {
		return sum, err
	}
	if err := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Where("workflow_id = ? AND status = 'revoked' AND delivered_at IS NOT NULL AND retract_noticed_at IS NULL", workflowID).
		Count(&retracted).Error; err != nil {
		return sum, err
	}
	sum.Delivered = int(delivered)
	sum.RetractNotices = int(retracted)
	return sum, nil
}

// InboxPeek 是 hook 的查询结果：概况，外加「这次该不该提醒」。
type InboxPeek struct {
	InboxSummary
	Remind bool `json:"remind"`
}

// InboxPeek 看一眼收件箱。reminder=false 纯只读；reminder=true 是 hook 在用：
// 按「立即、5 分钟后、15 分钟后」的节奏判断每条未读该不该再提醒一次，提醒过就记一笔（每条最多 InboxMaxReminders 次），
// 这样 hook 即使每隔十几秒查一次，也不会反复打扰。
func (s *Store) InboxPeek(ctx context.Context, workflowID string, reminder bool, now time.Time) (InboxPeek, error) {
	sum, err := s.InboxSummary(ctx, workflowID, now)
	out := InboxPeek{InboxSummary: sum}
	if err != nil || !reminder || sum.Unread == 0 {
		return out, err
	}
	err = s.WithTx(ctx, func(tx *gorm.DB) error {
		var rows []model.QueuedFeedback
		if err := tx.Select("id", "notified_count", "last_notified_at").
			Where("workflow_id = ? AND status = 'pending' AND delivered_at IS NULL AND created_at >= ?", workflowID, inboxCutoff(now)).
			Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			if !reminderDue(r, now) {
				continue
			}
			if err := tx.Model(&model.QueuedFeedback{}).Where("id = ?", r.ID).
				Updates(map[string]any{"notified_count": r.NotifiedCount + 1, "last_notified_at": now}).Error; err != nil {
				return err
			}
			out.Remind = true
		}
		return nil
	})
	return out, err
}

func reminderDue(r model.QueuedFeedback, now time.Time) bool {
	switch {
	case r.NotifiedCount >= InboxMaxReminders:
		return false
	case r.NotifiedCount == 0:
		return true
	case r.LastNotifiedAt == nil:
		return false
	default:
		return now.Sub(*r.LastNotifiedAt) >= inboxReminderGap[r.NotifiedCount]
	}
}

// MarkInboxReceiptHint 记下「回执提示行刚刚提醒过」，用于在读取时把送达归因到这条路径。
// 时间戳至多每分钟写一次。
// 返回 true 表示这次真的写了库（界面据此决定要不要刷新）。
func (s *Store) MarkInboxReceiptHint(ctx context.Context, workflowID string, now time.Time) (bool, error) {
	if workflowID == "" {
		return false, nil
	}
	res := s.db.WithContext(ctx).Model(&model.QueuedFeedback{}).
		Where("workflow_id = ? AND status = 'pending' AND delivered_at IS NULL AND (receipt_hint_at IS NULL OR receipt_hint_at < ?)",
			workflowID, now.Add(-receiptHintThrottle)).
		Update("receipt_hint_at", now)
	return res.RowsAffected > 0, res.Error
}

// InboxReadOptions 控制 InboxRead 返回什么。
type InboxReadOptions struct {
	// Recent 同时返回最近已确认的几条（上下文被压缩后想找回之前读过的追加时用）。
	Recent bool
}

// InboxReadResult 是一次读取收件箱的结果。
type InboxReadResult struct {
	New       []model.QueuedFeedback // 这次第一次送达
	Pending   []model.QueuedFeedback // 此前已读取、仍未确认（再列一遍，防止遗漏）
	Retracted []model.QueuedFeedback // 读取后被撤回，这是第一次告诉智能体：请忽略
	Recent    []model.QueuedFeedback // 最近已确认的（仅 Recent=true 时）
}

// Empty 表示没有任何内容可给。
func (r InboxReadResult) Empty() bool {
	return len(r.New) == 0 && len(r.Pending) == 0 && len(r.Retracted) == 0 && len(r.Recent) == 0
}

// InboxRead 是智能体主动读取收件箱：把排队中的追加标为已送达并返回全文。
// 送达归因：读取前 30 分钟内收到过 hook 提醒 / 回执提示行的，记为那条路径，否则记为主动读取。
func (s *Store) InboxRead(ctx context.Context, workflowID string, opts InboxReadOptions, now time.Time) (InboxReadResult, error) {
	var res InboxReadResult
	if workflowID == "" {
		return res, NewInvalidInputError("workflow_id is required")
	}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		if err := expireInboxTx(tx, workflowID, now); err != nil {
			return err
		}
		var pending []model.QueuedFeedback
		if err := tx.Where("workflow_id = ? AND status = 'pending'", workflowID).
			Order("created_at ASC, id ASC").Find(&pending).Error; err != nil {
			return err
		}
		for i := range pending {
			q := &pending[i]
			if q.DeliveredAt != nil {
				res.Pending = append(res.Pending, *q)
				continue
			}
			via := resolveDeliveredVia(*q, now)
			if err := tx.Model(&model.QueuedFeedback{}).Where("id = ?", q.ID).
				Updates(map[string]any{"delivered_at": now, "delivered_via": via, "updated_at": now}).Error; err != nil {
				return err
			}
			q.DeliveredAt, q.DeliveredVia = &now, via
			res.New = append(res.New, *q)
		}

		if err := tx.Where("workflow_id = ? AND status = 'revoked' AND delivered_at IS NOT NULL AND retract_noticed_at IS NULL", workflowID).
			Order("created_at ASC, id ASC").Find(&res.Retracted).Error; err != nil {
			return err
		}
		for i := range res.Retracted {
			if err := tx.Model(&model.QueuedFeedback{}).Where("id = ?", res.Retracted[i].ID).
				Update("retract_noticed_at", now).Error; err != nil {
				return err
			}
		}

		if opts.Recent {
			if err := tx.Where("workflow_id = ? AND status = 'consumed' AND acked_at >= ?", workflowID, now.Add(-InboxRecentWindow)).
				Order("created_at DESC, id DESC").Limit(10).Find(&res.Recent).Error; err != nil {
				return err
			}
			for i, j := 0, len(res.Recent)-1; i < j; i, j = i+1, j-1 {
				res.Recent[i], res.Recent[j] = res.Recent[j], res.Recent[i]
			}
		}
		return nil
	})
	if err != nil {
		return InboxReadResult{}, err
	}
	fillInboxStates(res.New)
	fillInboxStates(res.Pending)
	fillInboxStates(res.Retracted)
	fillInboxStates(res.Recent)
	return res, nil
}

// InboxAck 确认已读取的追加。ids 为空表示确认这个工作流里全部「已读取未确认」的。
// 还没被读取的（排队中）不能确认——没读过就谈不上确认。返回实际确认的编号。
func (s *Store) InboxAck(ctx context.Context, workflowID string, ids []uint, now time.Time) ([]uint, error) {
	if workflowID == "" {
		return nil, NewInvalidInputError("workflow_id is required")
	}
	var acked []uint
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		query := tx.Select("id").Where("workflow_id = ? AND status = 'pending' AND delivered_at IS NOT NULL", workflowID)
		if len(ids) > 0 {
			query = query.Where("id IN ?", ids)
		}
		var rows []model.QueuedFeedback
		if err := query.Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			acked = append(acked, r.ID)
		}
		if len(acked) == 0 {
			return nil
		}
		return tx.Model(&model.QueuedFeedback{}).Where("id IN ?", acked).
			Updates(map[string]any{"status": "consumed", "acked_at": now, "updated_at": now}).Error
	})
	return acked, err
}

// inboxMerge 是智能体发起交互时被一并取走的追加。
type inboxMerge struct {
	Text     string
	Messages model.StringArray
	Images   model.SessionImages
	Count    int // 随「秒回」带走的条数（只算此前没读到的）
	// First 是随「秒回」带走的追加里最早一条的创建时间（用户真正敲下它的时刻）；没有带走任何内容时是零值。
	First time.Time
}

// consumeInboxTx 在创建新会话的事务里取走收件箱：
// 排队中的全部按时间顺序合并成一次「秒回」；已读取未确认的视为随这次交互一并确认（智能体既然回来汇报了，就当它处理过）。
// sessionID 是正在创建的那一轮：被并进它的追加会记下 origin_ref = s:<会话编号>。
func consumeInboxTx(tx *gorm.DB, workflowID, sessionID string, now time.Time) (inboxMerge, error) {
	var merged inboxMerge
	if workflowID == "" {
		return merged, nil
	}
	if err := expireInboxTx(tx, workflowID, now); err != nil {
		return merged, err
	}
	var rows []model.QueuedFeedback
	if err := tx.Where("workflow_id = ? AND status = 'pending'", workflowID).
		Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return merged, err
	}
	var texts []string
	for i := range rows {
		q := &rows[i]
		// 确认项答复只经 inbox / hook / 回执提示行送达，不并进 interactive_feedback 的秒回
		// （它不是「对当前这次汇报的回复」）；已经读过的那些，照旧随这次交互一并确认。
		if k := inboxMessageKind(q.Kind); (k == model.InboxKindAnswer || k == model.InboxKindChatMention) && q.DeliveredAt == nil {
			continue
		}
		updates := map[string]any{"status": "consumed", "acked_at": now, "updated_at": now}
		if q.DeliveredAt == nil {
			if t := strings.TrimSpace(q.ResponseText); t != "" {
				texts = append(texts, q.ResponseText)
			}
			merged.Messages = append(merged.Messages, q.UserMessages...)
			merged.Images = append(merged.Images, q.Images...)
			merged.Count++
			if merged.First.IsZero() || q.CreatedAt.Before(merged.First) {
				merged.First = q.CreatedAt
			}
			updates["delivered_at"] = now
			updates["delivered_via"] = InboxViaFeedback
			if sessionID != "" {
				updates["origin_ref"] = "s:" + sessionID
			}
		}
		if err := tx.Model(&model.QueuedFeedback{}).Where("id = ?", q.ID).Updates(updates).Error; err != nil {
			return merged, err
		}
	}
	merged.Text = strings.Join(texts, "\n\n")
	return merged, nil
}

// expireInboxTx 把超过保留期仍未确认的追加标为 expired。workflowID 为空表示全部工作流。
func expireInboxTx(tx *gorm.DB, workflowID string, now time.Time) error {
	q := tx.Model(&model.QueuedFeedback{}).
		Where("status = 'pending' AND created_at < ?", now.Add(-InboxExpireAfter))
	if workflowID != "" {
		q = q.Where("workflow_id = ?", workflowID)
	}
	return q.Updates(map[string]any{"status": "expired", "updated_at": now}).Error
}

// inboxSweepEvery：读路径上的过期清理（一次写库）至多每隔这么久做一次——
// 回执提示行在每次工具调用时都要看收件箱，不能让每次调用都去抢一次写锁。
// 过期的行在查询里另有时间过滤，所以清理晚一点只影响「状态是否已经标成 expired」，不影响读到的内容。
const inboxSweepEvery = 5 * time.Minute

// expireInbox 是读路径上的顺手清理，失败了也不影响读取。
func (s *Store) expireInbox(ctx context.Context, now time.Time) {
	s.sweep.mu.Lock()
	if now.Sub(s.sweep.last) < inboxSweepEvery {
		s.sweep.mu.Unlock()
		return
	}
	s.sweep.last = now
	s.sweep.mu.Unlock()
	_ = expireInboxTx(s.db.WithContext(ctx), "", now)
}

type inboxSweep struct {
	mu   sync.Mutex
	last time.Time
}

// inboxCutoff 是保留期的起点：更早创建且仍未确认的追加视为过期。
func inboxCutoff(now time.Time) time.Time { return now.Add(-InboxExpireAfter) }

func resolveDeliveredVia(q model.QueuedFeedback, now time.Time) string {
	via, at := InboxViaTool, time.Time{}
	if t := q.LastNotifiedAt; t != nil && now.Sub(*t) <= inboxViaWindow && t.After(at) {
		via, at = InboxViaHook, *t
	}
	if t := q.ReceiptHintAt; t != nil && now.Sub(*t) <= inboxViaWindow && t.After(at) {
		via = InboxViaReceipt
	}
	return via
}

func fillInboxStates(list []model.QueuedFeedback) {
	for i := range list {
		list[i].State = list[i].DeriveState()
	}
}

// inboxMessageKind 把历史行里为空的 kind 视为 append（收件箱最初只有这一种）。
func inboxMessageKind(kind string) string {
	k := strings.TrimSpace(kind)
	if k == "" {
		return "append"
	}
	return k
}

// InboxPreview 是一条追加的单行预览：折叠空白、截断到 InboxPreviewRunes 个字符；只有图片时注明张数。
// 智能体在回执里看到的正文就是 ResponseText（快捷标签只用于界面展示），所以预览也以它为准，没有正文时才退回到标签。
func InboxPreview(q model.QueuedFeedback) string {
	return previewOf(q.ResponseText, q.UserMessages, len(q.Images))
}

func previewOf(responseText string, userMessages []string, imageCount int) string {
	text := strings.Join(strings.Fields(responseText), " ")
	if text == "" && len(userMessages) > 0 {
		text = strings.Join(strings.Fields(strings.Join(userMessages, " ")), " ")
	}
	if text == "" && imageCount > 0 {
		return fmt.Sprintf("（%d 张图片）", imageCount)
	}
	runes := []rune(text)
	if len(runes) > InboxPreviewRunes {
		text = string(runes[:InboxPreviewRunes]) + "…"
	}
	if imageCount > 0 {
		text += fmt.Sprintf("（附 %d 张图片）", imageCount)
	}
	return text
}

// ---- 智能体最近一次与 RelayMesh 交互的时间（只在内存里，仅用于 Web 展示）----

type agentActivity struct {
	mu        sync.Mutex
	last      map[string]time.Time // 智能体最近一次调用 RelayMesh
	hook      map[string]time.Time // 编辑器 hook 最近一次来问收件箱（说明 hook 装好了、在跑）；落库节流后重启可恢复（hook_seen.go）
	hookSaved map[string]time.Time // hook 时间最近一次落库的时间（只用于节流，见 noteHook）
	hookHint  map[string]time.Time // 回执里 hook: 安装提示最近一次出现的时间（节流）
	link      map[string]AgentLink // hook 汇报的运行情况：工具调用时间、一轮对话的结束状态、对话编号（见 agent_link.go）
}

const hookInstallHintEvery = 6 * time.Hour
const hookTraceStale = 24 * time.Hour

// InboxHookSeenBroadcastEvery 是 hook 查询触发 Web 刷新的最小间隔：hook 每隔十几秒就会问一次，
// 没必要每次都让所有打开的页面重新拉取。
const InboxHookSeenBroadcastEvery = time.Minute

// RecordAgentActivity 记下某个工作流的智能体刚刚调用过 RelayMesh。
func (s *Store) RecordAgentActivity(workflowID string, at time.Time) {
	if workflowID == "" {
		return
	}
	a := &s.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		a.last = make(map[string]time.Time)
	}
	if len(a.last) > 2000 { // 工作流数量不会这么多；真到了就清掉陈旧的，免得无限增长
		for k, t := range a.last {
			if at.Sub(t) > InboxExpireAfter {
				delete(a.last, k)
			}
		}
	}
	a.last[workflowID] = at
}

// AgentLastActivity 返回某个工作流的智能体最近一次调用 RelayMesh 的时间（服务重启后从头记）。
func (s *Store) AgentLastActivity(workflowID string) (time.Time, bool) {
	a := &s.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.last[workflowID]
	return t, ok
}

// RecordHookSeen 记下某个工作流的编辑器 hook 刚刚来问过一次。
// 返回值表示要不要通知 Web 刷新：之前从没记过，或距上一次记录已超过 InboxHookSeenBroadcastEvery。
// 时间同时按 HookSeenPersistEvery 节流落库，Hub 重启后由 RestoreHookSeen 装回来（#103，见 hook_seen.go）。
func (s *Store) RecordHookSeen(workflowID string, at time.Time) bool {
	if workflowID == "" {
		return false
	}
	broadcast, persist := s.activity.noteHook(workflowID, at)
	if persist {
		s.persistHookSeen(workflowID, at)
	}
	return broadcast
}

// HookLastSeen 返回某个工作流的编辑器 hook 最近一次来问收件箱的时间（Hub 重启后从库里恢复，见 RestoreHookSeen）。
func (s *Store) HookLastSeen(workflowID string) (time.Time, bool) {
	a := &s.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.hook[workflowID]
	return t, ok
}

// NeedsHookInstallHint 判断是否在回执末尾提示安装 hook（无 hook 痕迹或超过 24h，且 6h 内未提示过）。
func (s *Store) NeedsHookInstallHint(workflowID string, now time.Time) bool {
	if workflowID == "" {
		return false
	}
	a := &s.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.hook[workflowID]; ok && now.Sub(t) <= hookTraceStale {
		return false
	}
	if t, ok := a.hookHint[workflowID]; ok && now.Sub(t) < hookInstallHintEvery {
		return false
	}
	return true
}

// MarkHookInstallHint 记下刚刚在回执里提示过安装 hook。
func (s *Store) MarkHookInstallHint(workflowID string, now time.Time) {
	if workflowID == "" {
		return
	}
	a := &s.activity
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hookHint == nil {
		a.hookHint = make(map[string]time.Time)
	}
	a.hookHint[workflowID] = now
}
