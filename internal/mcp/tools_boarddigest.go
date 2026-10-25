package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 回执头里的 board 行（#180 B3a）：共享任务板上「该你处理的协作待办」。
//
// 背景：承接者只有主动调 task_board overview 才知道有新指派，派发者也只有调 overview 才知道对方交付了，
// 生产里「指派 → 接取」的等待中位数约 102 分钟。回执头每次都会被读到，所以把这几类待办放在这里，处理掉就自然消失。
// 没有任何待办时不输出这一行，回执与升级前逐字节一致。数据与口径见 store/work_board_digest.go。

// boardStaleAfter 是「指派派出后多久没人接算久无人接」：读通知偏好里的阈值（设置里可调，30 分钟 – 24 小时，默认 2 小时）。
// 读不出来就用默认——这是软提示的口径，不能因为读偏好失败就让回执少一行或拖垮回执。
func (s *Server) boardStaleAfter(ctx context.Context) time.Duration {
	prefs, err := s.store.GetNotifyPrefs(ctx)
	if err != nil {
		return store.BoardDigestStaleDefault
	}
	return prefs.BoardStaleAfter()
}

// boardDigestHeader 返回 board 行；没有待办（或查询失败——这是软提示，不能拖垮回执）时返回空串。
func (s *Server) boardDigestHeader(ctx context.Context, workflowID string) string {
	staleAfter := s.boardStaleAfter(ctx)
	now := time.Now()
	d, err := s.store.BoardDigestFor(ctx, workflowID, now, staleAfter)
	if err != nil || d.Empty() {
		return ""
	}
	return formatBoardDigest(d, now, staleAfter)
}

// formatBoardDigest 把待办汇总排成单行。承接者侧在前（它们最容易被晾着），派发者侧在后；每类最多列 5 个编号。
func formatBoardDigest(d *store.BoardDigest, now time.Time, staleAfter time.Duration) string {
	if d.Empty() {
		return ""
	}
	var parts []string
	add := func(g store.BoardDigestGroup, head string, wait bool, action string) {
		if g.Total == 0 {
			return
		}
		detail := boardSeqList(g)
		if wait && !g.Oldest.IsZero() {
			detail += "，最久已等 " + humanWait(now.Sub(g.Oldest))
		}
		parts = append(parts, fmt.Sprintf("%d 个%s（%s）→ %s", g.Total, head, detail, action))
	}
	add(d.ToAccept, "指派待你接取", true,
		`task_update {task_id, claim:"accept"} 接取，做不了就 claim:"decline" + note 写理由`)
	add(d.Returned, "交付被退回", false,
		`看任务时间线上的退回原因，改完再 claim:"deliver" + note 重新交付`)
	add(d.AwaitingReview, "你派出的任务已交付待验收", false,
		`claim:"accept_delivery" 验收，或 claim:"reject_delivery" + note 写原因退回`)
	add(d.Declined, "你派出的指派被拒绝", false,
		`看拒绝理由，改派给别人或自己做`)
	add(d.Stale, "你派出的指派超过 "+humanThreshold(staleAfter)+"没人接", true,
		`可以催一下对方，或改派`)
	add(d.StaleGone, "你派出的指派承接者已不存在", true,
		`改派给别人或自己做`)
	return "board: " + strings.Join(parts, " ｜ ")
}

// boardSeqList 列出编号；一共不止列出的这些时末尾加「等」。
func boardSeqList(g store.BoardDigestGroup) string {
	ids := make([]string, 0, len(g.Seqs))
	for _, n := range g.Seqs {
		ids = append(ids, fmt.Sprintf("#%d", n))
	}
	out := strings.Join(ids, " ")
	if g.Total > len(g.Seqs) {
		out += " 等"
	}
	return out
}

// humanWait 把等待时长写成「不到 1 分钟 / N 分钟 / N 小时 / N 天」，只取最大的一个单位。
func humanWait(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "不到 1 分钟"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
	}
}

// humanThreshold 把阈值写成整小时或分钟（设置里的阈值通常是整数小时 / 整数分钟）。
func humanThreshold(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	}
	return fmt.Sprintf("%d 分钟", int(d/time.Minute))
}
