package store

import (
	"context"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 「指派久无人接」的一次性提醒（#180 B3d）。
//
// 一个协作指派派出后停在「待接取」超过阈值（通知偏好里的 board_stale_minutes，默认 2 小时），就算久无人接，
// 给用户提醒一次：任务上有一个可空的时间戳 offer_reminded_at，记「这一轮派出已经提醒过」。
// 判断「还没提醒过」用 offer_reminded_at 为空、或早于 assigned_at——改派 / 拒绝后重新派出，assigned_at 变新，
// 这一轮又有资格被提醒一次，不必在任何别的路径里去清这个标记。
// 提醒的口径与 B3a 回执头、B3b 网页徽标的「待接取」一致：协作指派（派发者与承接者不是同一个，人派的也算）、
// claim=offered、任务未结束。

// BoardStaleMaxGroupsPerScan 是一次扫描最多发出的提醒条数（一条 = 一块任务板上的一个承接者）。
// 一口气有很多承接者同时到点（例如刚升级、或关掉提醒很久后又打开）时，每一遍只处理前几条，其余留给下一遍，
// 不会一下子弹出一屏通知。
const BoardStaleMaxGroupsPerScan = 5

// BoardStaleOffer 是一条提醒要说的事：同一块任务板上、同一个承接者那里「久无人接」的指派。
type BoardStaleOffer struct {
	BoardKey           string
	AssigneeWorkflowID string
	// Seqs 是这些指派的编号（#N，板内序号），从小到大。
	Seqs []int
	// ID 只在恰好一个指派时有值：它的数据库编号。任务页地址里的 ?task= 按它打开详情；它和 #N（Seqs）不是同一个数，
	// 多个板、多个工作流并存时两者必然错开，深链写成 #N 会打开别的任务。
	ID uint
	// Title 只在恰好一个指派时有值：它的标题。
	Title string
	// OldestAt 是其中最早的派出时间（提醒里写「已等多久」用）。
	OldestAt time.Time
}

// ClaimStaleOffers 找出「派出已满 staleAfter、这一轮还没提醒过」的待接取指派，打上已提醒标记，
// 按（任务板、承接者）成组返回，最早派出的组在前；最多返回 maxGroups 组（<= 0 用 BoardStaleMaxGroupsPerScan），
// 没被选中的留着，下一遍再处理。
//
// 「找出」与「打标记」在同一个事务里，所以同一个指派不会被两次扫描各提醒一次；代价是标记打上之后、
// 通知真正发出之前进程退出的话，那一条就丢了——对「提醒一次」来说，宁可偶尔少一条，也不重复。
// 打标记只写 offer_reminded_at 这一列：不动 updated_at、不进时间线、不让任务清单的各种缓存失效。
func (s *Store) ClaimStaleOffers(ctx context.Context, now time.Time, staleAfter time.Duration, maxGroups int) ([]BoardStaleOffer, error) {
	if maxGroups <= 0 {
		maxGroups = BoardStaleMaxGroupsPerScan
	}
	cutoff := now.Add(-staleAfter)
	var out []BoardStaleOffer
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		out = nil
		// 待接取的指派在任何时刻都很少，整批取出后在 Go 里按时间判断：SQLite 里时间列是文本，不同时区写法的字符串不能直接比较大小。
		var rows []model.WorkItem
		if err := tx.Select("id", "board_key", "seq", "title", "assignee_workflow_id", "assigned_at", "offer_reminded_at").
			Where("claim_state = ? AND assignee_workflow_id <> '' AND assigned_by_workflow_id <> assignee_workflow_id AND state IN ?",
				model.WorkClaimOffered, digestOpenStates).
			Find(&rows).Error; err != nil {
			return err
		}
		due := rows[:0]
		for _, r := range rows {
			if r.AssignedAt == nil || r.AssignedAt.IsZero() || r.AssignedAt.After(cutoff) {
				continue // 还没到点（或没有派出时间，无从算起）
			}
			if r.OfferRemindedAt != nil && !r.OfferRemindedAt.Before(*r.AssignedAt) {
				continue // 这一轮派出已经提醒过
			}
			due = append(due, r)
		}
		if len(due) == 0 {
			return nil
		}
		// 最早派出的在前；同一时刻按编号，让结果稳定。
		sort.SliceStable(due, func(i, j int) bool {
			ai, aj := *due[i].AssignedAt, *due[j].AssignedAt
			if !ai.Equal(aj) {
				return ai.Before(aj)
			}
			return due[i].ID < due[j].ID
		})

		type groupKey struct{ board, assignee string }
		idx := map[groupKey]int{}
		var ids []uint
		for _, r := range due {
			k := groupKey{r.BoardKey, r.AssigneeWorkflowID}
			gi, ok := idx[k]
			if !ok {
				if len(out) >= maxGroups {
					continue // 超出这一遍的名额：整组留给下一遍
				}
				gi = len(out)
				idx[k] = gi
				out = append(out, BoardStaleOffer{BoardKey: r.BoardKey, AssigneeWorkflowID: r.AssigneeWorkflowID, OldestAt: *r.AssignedAt})
			}
			g := &out[gi]
			g.Seqs = append(g.Seqs, r.Seq)
			if g.Title == "" {
				g.Title = r.Title
				g.ID = r.ID
			}
			ids = append(ids, r.ID)
		}
		for i := range out {
			sort.Ints(out[i].Seqs)
			if len(out[i].Seqs) != 1 {
				out[i].Title, out[i].ID = "", 0 // 标题与数据库编号只在恰好一个指派时有意义
			}
		}
		// UpdateColumn 不触发 updated_at 的自动更新：提醒不是任务的变化。
		return tx.Model(&model.WorkItem{}).Where("id IN ?", ids).UpdateColumn("offer_reminded_at", now).Error
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
