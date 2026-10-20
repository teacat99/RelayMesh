package terminal

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	// indexFlushEvery 是会话索引落库的合并周期。
	indexFlushEvery = 2 * time.Second
	// indexTelemetryEvery 是「只有记录字节数 / 最近写入时间变化」时的最小落库间隔：
	// 输出越多这些数字变得越快，没必要为它们频繁写库。
	indexTelemetryEvery = time.Minute
	indexQueue          = 256
	sessionIndexKeep    = 30 * 24 * time.Hour
	sessionPruneEvery   = 10 * time.Minute
)

// sessionIndexer 把 Agent 报告的会话清单异步同步到数据库索引（TerminalSession）。
//
// Agent 的读循环绝不能等数据库：这里只做非阻塞入队，由独立协程合并后批量写入。
// 索引里只有标签与录制统计，没有任何终端内容（INV-M4）。
type sessionIndexer struct {
	h  *Hub
	in chan indexJob
}

type indexJob struct {
	code string
	sync bool
	seen []store.TerminalSessionSeen
	ends []store.TerminalSessionEnd
}

func newSessionIndexer(h *Hub) *sessionIndexer {
	return &sessionIndexer{h: h, in: make(chan indexJob, indexQueue)}
}

// syncList 提交一台主机当前的会话清单。
func (x *sessionIndexer) syncList(code string, list []termproto.SessionInfo) {
	seen := make([]store.TerminalSessionSeen, 0, len(list))
	for _, s := range list {
		seen = append(seen, store.TerminalSessionSeen{
			SID: s.SID, Name: s.Name, Shell: s.Shell, StartedAt: s.Created,
			Exited: s.State == termproto.SessionExited, ExitCode: s.ExitCode,
			Rec: s.Rec, RecBytes: s.RecBytes, RecFirst: s.RecFirst, RecLast: s.RecLast, RecGap: s.RecGap,
		})
	}
	x.offer(indexJob{code: code, sync: true, seen: seen})
}

// ended 提交若干会话已结束及其原因。
func (x *sessionIndexer) ended(code string, ends []store.TerminalSessionEnd) {
	if len(ends) == 0 {
		return
	}
	x.offer(indexJob{code: code, ends: ends})
}

func (x *sessionIndexer) offer(j indexJob) {
	select {
	case x.in <- j:
	default: // 队列满：丢弃。下一次完整清单会把索引补齐。
	}
}

func signature(seen []store.TerminalSessionSeen) string {
	var b strings.Builder
	for _, s := range seen {
		b.WriteString(s.SID)
		b.WriteByte('|')
		b.WriteString(s.Name)
		b.WriteByte('|')
		b.WriteString(s.Shell)
		b.WriteByte('|')
		b.WriteString(strconv.FormatBool(s.Exited))
		b.WriteByte('|')
		b.WriteString(s.Rec)
		b.WriteByte('|')
		b.WriteString(strconv.FormatBool(s.RecGap))
		b.WriteByte(';')
	}
	return b.String()
}

// run 是索引协程：直到 ctx 取消。取消时尽力把待写入的内容落库。
func (x *sessionIndexer) run(ctx context.Context) {
	pending := map[string][]store.TerminalSessionSeen{}
	sigs := map[string]string{}
	wrote := map[string]time.Time{}
	var lastPrune time.Time

	flush := func(force bool) {
		now := x.h.now()
		for code, seen := range pending {
			sig := signature(seen)
			if !force && sig == sigs[code] && now.Sub(wrote[code]) < indexTelemetryEvery {
				continue
			}
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := x.h.store.SyncTerminalSessions(c, code, seen, now)
			cancel()
			if err != nil {
				log.Printf("[terminal] 同步会话索引失败: %v", err)
				continue
			}
			sigs[code], wrote[code] = sig, now
			delete(pending, code)
		}
	}
	for {
		select {
		case <-ctx.Done():
			flush(true)
			return
		case j := <-x.in:
			if j.sync {
				pending[j.code] = j.seen
				// 会话数量或状态变化要尽快落库（用户马上会在侧栏看到），不等下一个周期。
				if signature(j.seen) != sigs[j.code] {
					flush(false)
				}
				continue
			}
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := x.h.store.RecordTerminalSessionEnds(c, j.code, j.ends, x.h.now()); err != nil {
				log.Printf("[terminal] 更新会话结束状态失败: %v", err)
			}
			cancel()
		case <-time.After(indexFlushEvery):
			flush(false)
			if now := x.h.now(); now.Sub(lastPrune) >= sessionPruneEvery {
				lastPrune = now
				c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if _, err := x.h.store.PruneTerminalSessions(c, now.Add(-x.h.sessionIndexKeep())); err != nil {
					log.Printf("[terminal] 清理过期会话索引失败: %v", err)
				}
				cancel()
			}
		}
	}
}

// sessionIndexKeep 是已结束会话的索引保留期：与主机上记录的保留期一致（取服务端策略与默认值中更短者）。
func (h *Hub) sessionIndexKeep() time.Duration {
	keep := sessionIndexKeep
	if d := time.Duration(h.opts.RecordRetentionDays) * 24 * time.Hour; d > 0 && d < keep {
		keep = d
	}
	return keep
}
