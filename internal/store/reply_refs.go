package store

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 回复哈希（#106）：每条智能体回复（一轮汇报或一条进度通知）都有一个 8 位的短哈希，
// 就是会话编号 sess- 之后的那一段。用户在输入框里写「【回复 9f02517c】」引用它，
// Hub 把引用解析成这条回复的轮次、标题与开头再交给智能体，免得智能体对着一串哈希猜。

// ReplyRef 是一个哈希解析出的会话。
type ReplyRef struct {
	Session model.FeedbackSession
	// Turn 是它在工作流里按创建时间数的轮次序号（从 1 开始，与界面上的「第 N 轮」一致）；
	// 进度通知不占轮次，为 0。
	Turn int
}

// IsNotice 报告这条是不是异步模式下的进度气泡（而不是一轮汇报）。
func (r ReplyRef) IsNotice() bool { return r.Session.Kind == model.FeedbackKindNotice }

var replyHashPattern = regexp.MustCompile(`^[0-9a-f]{6,12}$`)

// NormalizeReplyHash 把用户写的哈希整理成小写十六进制：容忍大小写、前后空白和多写的 sess- 前缀；
// 不合法（太短、太长、含非十六进制字符）返回空串。这也保证了拼进 LIKE 的内容里没有通配符。
func NormalizeReplyHash(raw string) string {
	h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "sess-")
	if !replyHashPattern.MatchString(h) {
		return ""
	}
	return h
}

// FindReplyRefs 在一个工作流里按哈希（6~12 位前缀）找回复，旧的在前，最多 limit 条。
// 只在给定的工作流里找：别的工作流里哈希相同的会话不可见，不能靠引用窥探别的工作流。
// 哈希不合法或工作流为空时返回空，不报错。
func (s *Store) FindReplyRefs(ctx context.Context, workflowID, hash string, limit int) ([]ReplyRef, error) {
	workflowID = strings.TrimSpace(workflowID)
	h := NormalizeReplyHash(hash)
	if workflowID == "" || h == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	var rows []model.FeedbackSession
	if err := s.db.WithContext(ctx).
		Where("workflow_id = ? AND id LIKE ?", workflowID, "sess-"+h+"%").
		Order("created_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ReplyRef, 0, len(rows))
	for _, row := range rows {
		ref := ReplyRef{Session: row}
		if row.Kind != model.FeedbackKindNotice {
			// 轮次序号 = 同一工作流里不晚于它的汇报（不含通知）有几条。SQLite 里存的是本地偏移的文本时间，
			// 比较前把参数换成本地时区，否则字典序会错。
			var n int64
			if err := s.db.WithContext(ctx).Model(&model.FeedbackSession{}).
				Where("workflow_id = ? AND "+notNoticeSQL+" AND created_at <= ?", workflowID, row.CreatedAt.In(time.Local)).
				Count(&n).Error; err != nil {
				return nil, err
			}
			ref.Turn = int(n)
		}
		out = append(out, ref)
	}
	return out, nil
}
