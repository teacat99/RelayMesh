package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/notify"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 共享任务板上「指派久无人接」的一次性提醒（#180 B3d）：给用户看的，走统一通知闸。
//
// 后台每分钟扫一遍：指派派出后停在「待接取」超过阈值（通知偏好里的 board_stale_minutes，默认 2 小时）、
// 而这一轮派出还没提醒过的，按（任务板、承接者）合成一条，经闸发给浏览器（SSE notify）与 ntfy。
// 「每个指派只提醒一次」靠任务上的 offer_reminded_at 保证（见 store/work_board_remind.go），重启不会重复。
//
// 两种情况下这一遍什么都不做、也不打标记（等条件解除后的下一遍再补）：提醒开关关着；免打扰开着。
// 这样关掉又打开、或免打扰结束之后，还没人接的指派会被补一次提醒，而不是被悄悄吞掉。

const (
	// boardRemindMaxSeqs 是一条提醒里最多列出的编号个数。
	boardRemindMaxSeqs = 6
	// boardRemindTitleRunes 是单个指派的标题在提醒里最多留的字数（含省略号）。
	boardRemindTitleRunes = 28
	// boardRemindMinTick 是扫描间隔的下限：实验里把「一分钟」缩得很短时，也不让它每毫秒查一次库。
	boardRemindMinTick = 20 * time.Millisecond
)

// boardReminder 是提醒的发现端。
type boardReminder struct {
	store    *store.Store
	notifier *gateNotifier
	// unit 是「一分钟」有多长：生产是 time.Minute；实验与端到端测试经 RELAYMESH_BOARD_STALE_UNIT_MS 缩短，
	// 阈值（分钟数）、扫描间隔与文案里「已等多久」都按它换算，所以不必真等几个小时，写出来的数字也和设置里的分钟数对得上。
	// 只影响这里的提醒，回执头与网页的阈值仍按真实分钟。
	unit time.Duration
	now  func() time.Time

	lifeMu sync.Mutex
	cancel context.CancelFunc
}

// newBoardReminder 接线；unitMs <= 0 表示用真实的一分钟。
func newBoardReminder(st *store.Store, n *gateNotifier, unitMs int) *boardReminder {
	unit := time.Minute
	if unitMs > 0 {
		unit = time.Duration(unitMs) * time.Millisecond
	}
	return &boardReminder{store: st, notifier: n, unit: unit, now: time.Now}
}

// start 启动后台扫描；重复调用无副作用。启动后先等一个「分钟」再扫第一遍，不和启动期的迁移、重连抢资源。
func (r *boardReminder) start() {
	if r == nil || r.store == nil || r.notifier == nil {
		return
	}
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()
	if r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	tick := max(r.unit, boardRemindMinTick)
	go func() {
		timer := time.NewTimer(tick)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			r.safeScan(ctx)
			timer.Reset(tick)
		}
	}()
}

func (r *boardReminder) stop() {
	if r == nil {
		return
	}
	r.lifeMu.Lock()
	defer r.lifeMu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

// safeScan 保证扫描里任何意外都不会带走进程或这个协程。
func (r *boardReminder) safeScan(parent context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[board] 久无人接提醒的扫描发生内部错误：%v", rec)
		}
	}()
	// stop() 只让循环不再开始新的一遍，不打断已经开始的这一遍：扫描里有「认领」这样的写事务，被取消的事务会连带丢掉它的数据库连接——
	// 文件库无所谓，内存库（测试、单元夹具）会因此整个丢失，表现为停下之后别的操作报 no such table。所以这一遍沿用「不随停止取消」的
	// 上下文，只受 20 秒超时约束；一遍扫描通常是几毫秒。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 20*time.Second)
	defer cancel()
	r.scan(ctx)
}

// scan 扫一遍，返回这一遍发出的提醒条数。
func (r *boardReminder) scan(ctx context.Context) int {
	prefs, err := r.store.GetNotifyPrefs(ctx)
	if err != nil {
		return 0
	}
	if prefs.BoardStaleOff || prefs.DND {
		return 0
	}
	minutes := prefs.BoardStaleAfterMinutes()
	staleAfter := time.Duration(minutes) * r.unit
	now := r.now()
	offers, err := r.store.ClaimStaleOffers(ctx, now, staleAfter, store.BoardStaleMaxGroupsPerScan)
	if err != nil {
		log.Printf("[board] 查找久无人接的指派失败：%v", err)
		return 0
	}
	for _, o := range offers {
		r.publish(ctx, o, minutes, now)
	}
	return len(offers)
}

// inMinutes 把一段真实经过的时间换算成「按 unit 计的分钟」：生产里 unit 就是一分钟，原样返回；实验里把一分钟缩短后，
// 实际等了 6 秒、unit 是 100 毫秒，就当作等了 60 分钟，文案才和设置里的阈值（分钟数）在同一个尺度上。
func (r *boardReminder) inMinutes(d time.Duration) time.Duration {
	if r.unit <= 0 || r.unit == time.Minute {
		return d
	}
	return time.Duration(float64(d) * float64(time.Minute) / float64(r.unit))
}

// publish 把一条提醒交给通知闸。
func (r *boardReminder) publish(ctx context.Context, o store.BoardStaleOffer, minutes int, now time.Time) {
	lookupCtx, cancel := context.WithTimeout(ctx, notifyLookupTimeout)
	defer cancel()
	title, sender := r.notifier.names(lookupCtx, o.AssigneeWorkflowID, "")
	who := strings.TrimSpace(sender)
	if who == "" {
		who = strings.TrimSpace(title)
	}
	if who == "" {
		who = o.AssigneeWorkflowID
	}
	ev := notify.Event{
		ID:       boardStaleEventID(o, now),
		Kind:     notify.KindBoardStale,
		Title:    "指派久无人接",
		Preview:  boardStaleText(who, o, minutes, r.inMinutes(now.Sub(o.OldestAt)), time.Duration(minutes)*time.Minute),
		DeepLink: boardStaleLink(o),
	}
	r.notifier.gate.Publish(ev, r.notifier.pushEvent)
	log.Printf("[board] 久无人接的提醒：承接者 %s 那里有 %d 个指派超过 %s 没人接", o.AssigneeWorkflowID, len(o.Seqs), boardThresholdText(minutes))
}

// boardStaleEventID 是闸里的幂等键：同一块板、同一个承接者、同一批编号、同一个时刻，只会有一条。
func boardStaleEventID(o store.BoardStaleOffer, now time.Time) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s|%s|%v|%d", o.BoardKey, o.AssigneeWorkflowID, o.Seqs, now.UnixNano())
	return "bst:" + hex.EncodeToString(h.Sum(nil))[:20]
}

// boardStaleLink 是点开提醒后去的地方：只有一个指派时直达它的详情，否则打开承接者的任务页并套上「待接取」快捷筛选。
func boardStaleLink(o store.BoardStaleOffer) string {
	link := "/work/" + url.PathEscape(o.AssigneeWorkflowID)
	// ?task= 是任务的数据库编号，不是文案里的 #N（板内序号）：用错了会打开别的任务（或别的项目的任务）。
	if len(o.Seqs) == 1 && o.ID != 0 {
		return link + "?task=" + strconv.FormatUint(uint64(o.ID), 10)
	}
	return link + "?quick=offered"
}

// boardStaleText 是提醒的正文：谁那里有哪些指派、超过多久没人接；等得比阈值久得多（例如刚升级、或关掉提醒很久后又打开）时补一句最久已等多久。
func boardStaleText(who string, o store.BoardStaleOffer, minutes int, waited, staleAfter time.Duration) string {
	var subject string
	if len(o.Seqs) == 1 {
		subject = fmt.Sprintf("#%d", o.Seqs[0])
		if t := boardShortTitle(o.Title); t != "" {
			subject += "「" + t + "」"
		}
	} else {
		subject = fmt.Sprintf("%d 个指派（%s）", len(o.Seqs), boardSeqText(o.Seqs))
	}
	// 编号与「派给」之间留一个空格；主语以「」或「）」收尾时不留（全角标点后面再加空格很别扭）。
	gap := " "
	if strings.HasSuffix(subject, "」") || strings.HasSuffix(subject, "）") {
		gap = ""
	}
	text := fmt.Sprintf("%s%s派给 %s，已超过 %s没人接", subject, gap, who, boardThresholdText(minutes))
	if staleAfter > 0 && waited >= 2*staleAfter {
		text += "，最久已等 " + boardWaitText(waited)
	}
	return text
}

// boardShortTitle 把任务标题折成一行并截到 28 个字：通知正文总共只有 120 字，标题太长会把后面「派给谁、等了多久」挤掉。
func boardShortTitle(t string) string {
	t = strings.Join(strings.Fields(t), " ")
	r := []rune(t)
	if len(r) <= boardRemindTitleRunes {
		return t
	}
	return strings.TrimSpace(string(r[:boardRemindTitleRunes-1])) + "…"
}

// boardSeqText 列出编号，超过上限时末尾写「等」。
func boardSeqText(seqs []int) string {
	n := len(seqs)
	if n > boardRemindMaxSeqs {
		n = boardRemindMaxSeqs
	}
	parts := make([]string, 0, n)
	for _, s := range seqs[:n] {
		parts = append(parts, "#"+strconv.Itoa(s))
	}
	out := strings.Join(parts, " ")
	if len(seqs) > n {
		out += " 等"
	}
	return out
}

// boardThresholdText 把阈值写成整小时或分钟（与回执头 board: 行同一种写法）。
func boardThresholdText(minutes int) string {
	if minutes >= 60 && minutes%60 == 0 {
		return fmt.Sprintf("%d 小时", minutes/60)
	}
	return fmt.Sprintf("%d 分钟", minutes)
}

// boardWaitText 是「已等多久」的粗略写法（与回执头 board: 行同一种写法）。
func boardWaitText(d time.Duration) string {
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
