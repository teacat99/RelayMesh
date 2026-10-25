package store

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 群里的协作系统行（#180 B3c；方案见 .cursor/planning/2026-10-22-180-shared-board-b3-plan.md 的 B3c 一节）。
//
// 派发者与承接者（都是工作流）同在一个群里时，下面六种协作事件各在群里留一条居中的灰色系统行：
// 派活 / 改派、交付、拒绝、退回、验收。接取不说——它不需要对方做任何事。
//
// 规则：
//   - 只是群里的可见性：不带 @、不进任何人的收件箱、不算未读，也不进回复链与冷却的计数
//     （系统行的发送者种类是 system，这些统计都只数人和工作流发的消息；见 groups_test 与本文件的测试）。
//   - 同一次批量里「同一种事件、同一个操作者、同一对派发者与承接者」合成一条，列出编号（单个任务时带标题）。
//   - 同一个事件只发一次：只有任务的协作状态真的变化才算事件（重复交付、对同一个人重复指派不算），
//     另外用幂等键保证同一组时间线记录不会因为重复调用写出两行。
//   - 人在网页上派的任务没有派发者工作流，不发；自己派给自己的也不发。
//
// 文字里的成员名用 {{m:<编号>}} 占位，读出来时由 chat 包换成这个群里当前的显示名（含重名消歧，
// 与 @ 和「谁发的」用同一套名字），所以改名之后旧行也跟着变；旧版本（回滚之后）读到这样的行会原样显示占位符，
// 但整句话仍然看得懂。没有新列、没有新表。

const (
	boardLineTitleRunes = 24 // 单个任务的行里，标题最多写多少个字
	boardLineMaxSeqs    = 8  // 合并行里最多列出几个编号
)

// BoardLineEvent 是一次值得告诉群里的协作事件。
type BoardLineEvent struct {
	Event       string // assign / reassign / deliver / decline / reject_delivery / accept_delivery
	ActorRef    string // 谁做的：工作流编号；人在网页上做的是 GroupHumanRef
	AssignerRef string // 派发者（工作流）
	AssigneeRef string // 承接者（工作流）
	Seq         int    // 任务在板上的编号（#N）
	Title       string
	UpdateID    uint // 时间线记录编号：幂等键用，0 = 没有
}

// boardLineFor 从一次写入（改动前 / 改动后的任务、产生的协作事件）算出要不要告诉群里；不要就返回 nil。
// before 为空表示新建任务（创建时顺带指派）。
func boardLineFor(before, after *model.WorkItem, ev *workEvent, actor WorkActor, updateID uint) *BoardLineEvent {
	if ev == nil || after == nil {
		return nil
	}
	// 只有「协作状态真的变了」才算事件：重复交付、对同一个人重复指派、已完成的再验收都不说第二遍。
	switch ev.name {
	case model.WorkEventAssign, model.WorkEventReassign:
		if before != nil && before.AssigneeWorkflowID == after.AssigneeWorkflowID && before.ClaimState == after.ClaimState {
			return nil
		}
	case model.WorkEventDeliver:
		if before != nil && before.State == model.WorkStateReview {
			return nil
		}
	case model.WorkEventDecline:
		if before != nil && before.ClaimState == model.WorkClaimDeclined {
			return nil
		}
	case model.WorkEventAcceptDelivery:
		if before != nil && before.State == model.WorkStateDone {
			return nil
		}
	case model.WorkEventRejectDelivery:
		if before != nil && before.State != model.WorkStateReview {
			return nil
		}
	default: // 接取、撤销指派：不说
		return nil
	}
	by, to := strings.TrimSpace(after.AssignedByWorkflowID), strings.TrimSpace(after.AssigneeWorkflowID)
	if by == "" || to == "" || by == to { // 人派的没有派发者工作流；自己派给自己的没有第二个人要知道
		return nil
	}
	actorRef := actor.workflowRef()
	if actorRef == "" {
		actorRef = GroupHumanRef
	}
	return &BoardLineEvent{
		Event: ev.name, ActorRef: actorRef, AssignerRef: by, AssigneeRef: to,
		Seq: after.Seq, Title: after.Title, UpdateID: updateID,
	}
}

// mergedBoardLine 是同一批里合并之后的一条：同一种事件、同一个操作者、同一对派发者与承接者。
type mergedBoardLine struct {
	Event, ActorRef, AssignerRef, AssigneeRef string
	Seqs                                      []int  // 升序、去重
	Title                                     string // 只有一个任务时才用
	UpdateIDs                                 []uint
}

func mergeBoardEvents(events []BoardLineEvent) []*mergedBoardLine {
	var order []string
	byKey := map[string]*mergedBoardLine{}
	for _, e := range events {
		if e.Seq <= 0 || e.AssignerRef == "" || e.AssigneeRef == "" || e.AssignerRef == e.AssigneeRef {
			continue
		}
		switch e.Event {
		case model.WorkEventAssign, model.WorkEventReassign, model.WorkEventDeliver, model.WorkEventDecline,
			model.WorkEventRejectDelivery, model.WorkEventAcceptDelivery:
		default:
			continue
		}
		key := strings.Join([]string{e.Event, e.ActorRef, e.AssignerRef, e.AssigneeRef}, "\x00")
		m := byKey[key]
		if m == nil {
			m = &mergedBoardLine{Event: e.Event, ActorRef: e.ActorRef, AssignerRef: e.AssignerRef, AssigneeRef: e.AssigneeRef}
			byKey[key] = m
			order = append(order, key)
		}
		dup := false
		for _, s := range m.Seqs {
			if s == e.Seq {
				dup = true
				break
			}
		}
		if !dup {
			m.Seqs = append(m.Seqs, e.Seq)
			m.Title = e.Title
		}
		if e.UpdateID != 0 {
			m.UpdateIDs = append(m.UpdateIDs, e.UpdateID)
		}
	}
	out := make([]*mergedBoardLine, 0, len(order))
	for _, k := range order {
		m := byKey[k]
		sort.Ints(m.Seqs)
		sort.Slice(m.UpdateIDs, func(i, j int) bool { return m.UpdateIDs[i] < m.UpdateIDs[j] })
		if len(m.Seqs) != 1 {
			m.Title = ""
		}
		out = append(out, m)
	}
	return out
}

// memberToken 是文字里的成员占位：读出来时换成显示名。花括号会破坏占位的解析，编号里去掉。
func memberToken(ref string) string {
	return "{{m:" + strings.NewReplacer("{", "", "}", "").Replace(ref) + "}}"
}

// cleanBoardTitle 让标题可以安全放进一句话：一行、不含占位记号、不过长；
// 标题是任务创建者（可能是任何一个智能体）写的，所以引号也换掉，免得它在系统行里提前「闭合」标题、冒充后面的话。
func cleanBoardTitle(t string) string {
	t = strings.Join(strings.Fields(t), " ")
	t = strings.NewReplacer("{{", "{ {", "}}", "} }", "「", "『", "」", "』").Replace(t)
	r := []rune(t)
	if len(r) > boardLineTitleRunes {
		t = string(r[:boardLineTitleRunes]) + "…"
	}
	return t
}

func formatBoardSeqs(seqs []int) string {
	shown := seqs
	if len(shown) > boardLineMaxSeqs {
		shown = shown[:boardLineMaxSeqs]
	}
	parts := make([]string, 0, len(shown)+1)
	for _, s := range shown {
		parts = append(parts, fmt.Sprintf("#%d", s))
	}
	if len(seqs) > len(shown) {
		parts = append(parts, "…")
	}
	return strings.Join(parts, " ")
}

// boardLineText 是系统行的正文（带成员占位）。单个任务写「#12「标题」」，多个任务写个数与编号。
func boardLineText(m *mergedBoardLine) string {
	by, to, actor := memberToken(m.AssignerRef), memberToken(m.AssigneeRef), memberToken(m.ActorRef)
	n := len(m.Seqs)
	nums := formatBoardSeqs(m.Seqs)
	// one 是单个任务的指称：有标题写「#12「标题」」；gap 是它后面紧跟汉字动词时要不要空一格（有标题的右引号后不用）。
	one, gap := nums, " "
	if n == 1 && strings.TrimSpace(m.Title) != "" {
		one, gap = fmt.Sprintf("%s「%s」", nums, cleanBoardTitle(m.Title)), ""
	}
	switch m.Event {
	case model.WorkEventAssign:
		if n == 1 {
			return fmt.Sprintf("%s 把 %s%s派给了 %s", by, one, gap, to)
		}
		return fmt.Sprintf("%s 一次派给 %s %d 个任务：%s", by, to, n, nums)
	case model.WorkEventReassign:
		if n == 1 {
			return fmt.Sprintf("%s 把 %s%s改派给了 %s", by, one, gap, to)
		}
		return fmt.Sprintf("%s 一次改派给 %s %d 个任务：%s", by, to, n, nums)
	case model.WorkEventDeliver:
		if n == 1 {
			return fmt.Sprintf("%s 交付了 %s，等 %s 验收", to, one, by)
		}
		return fmt.Sprintf("%s 交付了 %d 个任务（%s），等 %s 验收", to, n, nums, by)
	case model.WorkEventDecline:
		if n == 1 {
			return fmt.Sprintf("%s 拒绝接取 %s，等 %s 改派", to, one, by)
		}
		return fmt.Sprintf("%s 拒绝接取 %d 个任务（%s），等 %s 改派", to, n, nums, by)
	case model.WorkEventRejectDelivery:
		if n == 1 {
			return fmt.Sprintf("%s 把 %s%s退回给 %s 返工", actor, one, gap, to)
		}
		return fmt.Sprintf("%s 把 %d 个任务（%s）退回给 %s 返工", actor, n, nums, to)
	case model.WorkEventAcceptDelivery:
		if n == 1 {
			return fmt.Sprintf("%s 验收通过了 %s 交付的 %s", actor, to, one)
		}
		return fmt.Sprintf("%s 验收通过了 %s 交付的 %d 个任务（%s）", actor, to, n, nums)
	}
	return ""
}

// boardLineIdem 是这一行的幂等键后缀：同一组时间线记录只会写出一行。没有记录编号时为空（不去重）。
func boardLineIdem(m *mergedBoardLine) string {
	if len(m.UpdateIDs) == 0 {
		return ""
	}
	parts := make([]string, len(m.UpdateIDs))
	for i, id := range m.UpdateIDs {
		parts[i] = fmt.Sprint(id)
	}
	sum := sha1.Sum([]byte(m.Event + "|" + strings.Join(parts, ",")))
	return "board|" + hex.EncodeToString(sum[:])[:20]
}

// sharedGroupsTx 返回两个工作流都还在、且没归档的群（按群编号排序）。
func sharedGroupsTx(tx *gorm.DB, a, b string) ([]string, error) {
	var ids []string
	err := tx.Model(&model.ConversationMember{}).
		Select("conversation_members.conversation_id").
		Joins("JOIN conversations ON conversations.id = conversation_members.conversation_id AND conversations.archived_at IS NULL").
		Where("conversation_members.member_kind = ? AND conversation_members.member_ref IN ? AND conversation_members.left_at IS NULL",
			model.MemberKindWorkflow, []string{a, b}).
		Group("conversation_members.conversation_id").
		Having("COUNT(DISTINCT conversation_members.member_ref) = 2").
		Order("conversation_members.conversation_id").
		Scan(&ids).Error
	return ids, err
}

// PostBoardGroupLines 把一批协作事件写成群里的系统行，返回写入了新行的群（调用方据此通知网页刷新）。
//
// 它在任务写入成功「之后」调用，是尽力而为：失败只影响群里的可见性，不影响任务本身，所以调用方只记日志。
// 不在 WithTx 里调用它（WithTx 持有存储的全局锁，不可重入）。
func (s *Store) PostBoardGroupLines(ctx context.Context, events []BoardLineEvent) ([]string, error) {
	merged := mergeBoardEvents(events)
	if len(merged) == 0 {
		return nil, nil
	}
	touched := map[string]bool{}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		now := time.Now() // 在事务里取时间：序号与时间同序，翻页才不会错位
		for _, m := range merged {
			groups, err := sharedGroupsTx(tx, m.AssignerRef, m.AssigneeRef)
			if err != nil {
				return err
			}
			if len(groups) == 0 {
				continue
			}
			text := boardLineText(m)
			if text == "" {
				continue
			}
			suffix := boardLineIdem(m)
			for _, gid := range groups {
				var idem *string
				if suffix != "" {
					full := gid + "|system|" + suffix
					var n int64
					if err := tx.Model(&model.ChatMessage{}).Where("idem_key = ?", full).Count(&n).Error; err != nil {
						return err
					}
					if n > 0 {
						continue
					}
					idem = &full
				}
				seq, err := nextSeqTx(tx, gid)
				if err != nil {
					return err
				}
				row := model.ChatMessage{
					ConversationID: gid, Seq: seq, SenderKind: model.ChatSenderSystem, SenderRef: m.ActorRef,
					Kind: model.ChatKindSystem, Body: text, IdemKey: idem, CreatedAt: now,
				}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				touched[gid] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(touched))
	for g := range touched {
		out = append(out, g)
	}
	sort.Strings(out)
	return out, nil
}
