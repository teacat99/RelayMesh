package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 自动升级的「检查」一侧（方案见文档库 design/agent-auto-upgrade.md）。
//
// 主机所有者在本机开启自动升级（auto-upgrade enable）之后，Agent 连上 Hub 并被启用时问一次、之后每隔几个小时问一次：
// 「有没有比我新的版本」。这一层只做「知道有新版本」——提示模式下到此为止：记在日志与本机的检查结果文件里，
// 并由 Hub 在 Web 主机卡片上显示；不下载、不安装。
//
// 几条不变量：
//   - 问不问由本机的同意决定：每次检查前现读模式，所有者 `auto-upgrade disable` 之后下一次检查就不再发出；
//   - 一律是 Agent 主动问，Hub 从不主动推送；回答里没有任何可下载的东西；
//   - Agent 不盲信回答：Hub 说「有新版本」但本机自己比较后版本并不更旧，一律当作已是最新（版本只升不降）；
//   - 问不到答案（Hub 较旧、网络问题）不是错误：记一次日志，下一个周期再问。

// autoCheckTiming 是自动升级检查的节奏。
type autoCheckTiming struct {
	// First 是连上并被启用之后第一次检查的最长等待（实际在 [First/4, First] 内随机，避免一批主机同时检查）。
	First time.Duration
	// Every 是拿到确定答案（有新版本 / 已是最新）之后，每隔多久再检查一次（上下浮动 10%）。
	Every time.Duration
	// Retry 是没有拿到确定答案（总闸关着、Hub 暂时没有适用的程序、没有回答……）之后多久再问一次（上下浮动 10%）。
	// 比 Every 短得多：这样 Hub 管理员事后打开总闸、或 Hub 刚升级好，主机很快就能知道，而不是等上整整一个周期。
	// 0 表示和 Every 一样。
	Retry time.Duration
	// Timeout 是等 Hub 回答的最长时间。
	Timeout time.Duration
}

// autoCheckDefaults 是默认节奏。是变量只为了让测试不必真等；调用方在启动检查协程之前取一份副本
// （不要在协程里读它，否则与测试结束时的还原构成数据竞争）。
var autoCheckDefaults = autoCheckTiming{First: 40 * time.Second, Every: 6 * time.Hour, Retry: 15 * time.Minute, Timeout: 30 * time.Second}

// 本机记录的检查结果（AutoCheckNote.Status）里，除了 Hub 的回答（termproto.AutoOffer*），还有一个本机自己的取值。
const autoCheckNoReply = "no_reply" // Hub 没有在时限内回答（可能是较旧的版本，不认识自动升级）

// AutoCheckNote 是最近一次自动升级检查的结果，写在数据目录里，供 `auto-upgrade status` 在本机展示。
type AutoCheckNote struct {
	// At 是检查完成的 Unix 秒（本机时钟）。
	At int64 `json:"at"`
	// Status 是 Hub 的回答（termproto.AutoOffer*）或 autoCheckNoReply。
	Status string `json:"status"`
	// Latest 是 Hub 上最新的 Agent 版本（回答里有时才有）；Current 是检查时本机的版本。
	Latest  string `json:"latest,omitempty"`
	Current string `json:"current,omitempty"`
	// Install 与 Detail 只有静默模式才有：这一轮检查之后，静默升级做了什么决定（autoInstall*）以及原因的一句话说明。
	Install string `json:"install,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

const (
	autoCheckNoteFile     = "auto-check.json"
	maxAutoCheckNoteBytes = 4 << 10
)

// AutoCheckNotePath 是检查结果文件的路径（与升级相关的其他文件放在一起）。
func AutoCheckNotePath(dataDir string) string {
	return filepath.Join(UpgradeDir(dataDir), autoCheckNoteFile)
}

// WriteAutoCheckNote 保存最近一次检查的结果。
func WriteAutoCheckNote(dataDir string, n AutoCheckNote) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(UpgradeDir(dataDir), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(AutoCheckNotePath(dataDir), append(data, '\n'), 0o600)
}

// ReadAutoCheckNote 读取最近一次检查的结果；不存在、过大或损坏时返回 false。
func ReadAutoCheckNote(dataDir string) (AutoCheckNote, bool) {
	if dataDir == "" {
		return AutoCheckNote{}, false
	}
	path := AutoCheckNotePath(dataDir)
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxAutoCheckNoteBytes {
		return AutoCheckNote{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return AutoCheckNote{}, false
	}
	var n AutoCheckNote
	if json.Unmarshal(data, &n) != nil || n.Status == "" || n.At <= 0 {
		return AutoCheckNote{}, false
	}
	// 文件是本机写的，但展示前仍按「来自磁盘的不可信文本」净化：版本号必须合规，状态必须是已知的取值。
	if !knownAutoCheckStatus(n.Status) {
		return AutoCheckNote{}, false
	}
	if !termproto.ValidAgentVersion(n.Latest) {
		n.Latest = ""
	}
	if !termproto.ValidAgentVersion(n.Current) {
		n.Current = ""
	}
	if !knownAutoInstall(n.Install) {
		n.Install, n.Detail = "", ""
	}
	n.Detail = termproto.SanitizeText(n.Detail, termproto.MaxUpgradeDetailRunes)
	return n, true
}

// RemoveAutoCheckNote 删除检查结果（收回同意时一并清掉，免得 status 以后还展示一份过期的结果）。文件不存在不算错。
func RemoveAutoCheckNote(dataDir string) error {
	if dataDir == "" {
		return errors.New("数据目录为空")
	}
	if err := os.Remove(AutoCheckNotePath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// knownAutoInstall 判断 s 是不是静默升级记录里认识的取值（空串也认识：表示没有静默升级的记录）。
func knownAutoInstall(s string) bool {
	switch s {
	case "", autoInstallWaitingWindow, autoInstallPaused, autoInstallNoKeys, autoInstallBadSettings,
		autoInstallDeferred, autoInstallUnsigned, autoInstallStarted, autoInstallFailed:
		return true
	}
	return false
}

func knownAutoCheckStatus(s string) bool {
	switch s {
	case termproto.AutoOfferDisabled, termproto.AutoOfferNotAllowed, termproto.AutoOfferUpToDate,
		termproto.AutoOfferAvailable, termproto.AutoOfferUnavailable, autoCheckNoReply:
		return true
	}
	return false
}

// autoChecker 是这个 Agent 进程的自动升级检查器：跨连接共享「上次说过什么」，每条 Hub 连接上各自跑一个检查循环。
type autoChecker struct {
	cfg     *SelfUpgrade
	version string
	logf    func(string, ...any)
	// upg 是静默升级复用的下载 / 校验 / 交接链路（与 Web 一键升级是同一个，共用「进行中」名额）；nil 表示这个检查器只提示。
	upg *upgrader

	mu       sync.Mutex
	waiting  map[string]chan termproto.AutoUpgradeOffer // 在途的检查，按标识
	granting map[string]chan termproto.AutoUpgradeGrant // 在途的静默升级请求，按标识
	last     AutoCheckNote                              // 最近一次检查的结论：静默升级的记录在它的基础上补充
	lastLog  string                                     // 上一次写进日志的结论；只有结论变了才再写，避免每个周期刷一遍
}

func newAutoChecker(cfg *SelfUpgrade, version string, logf func(string, ...any)) *autoChecker {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &autoChecker{
		cfg: cfg, version: version, logf: logf,
		waiting:  make(map[string]chan termproto.AutoUpgradeOffer),
		granting: make(map[string]chan termproto.AutoUpgradeGrant),
	}
}

// deliver 把 Hub 的回答交给等待它的那次检查。标识对不上的（别的连接留下的、已经超时的、凭空来的）一律丢弃。
func (c *autoChecker) deliver(o termproto.AutoUpgradeOffer) {
	c.mu.Lock()
	ch, ok := c.waiting[o.ID]
	if ok {
		delete(c.waiting, o.ID)
	}
	c.mu.Unlock()
	if ok {
		ch <- o // 通道有一个缓冲位，且每个标识只会取走一次，不会阻塞
	}
}

// deliverGrant 把 Hub 的裁决交给等待它的那次请求。标识对不上的（本机没有发出过的、已经超时的、别的连接留下的）一律丢弃——
// 带下载凭据的裁决只有在本机自己发出请求之后才会被采纳。
func (c *autoChecker) deliverGrant(g termproto.AutoUpgradeGrant) {
	c.mu.Lock()
	ch, ok := c.granting[g.ID]
	if ok {
		delete(c.granting, g.ID)
	}
	c.mu.Unlock()
	if ok {
		ch <- g // 通道有一个缓冲位，且每个标识只会取走一次，不会阻塞
	}
}

// run 是一条 Hub 连接上的检查循环：随机等一会儿后检查一次，之后每个周期一次，直到连接结束。
func (c *autoChecker) run(ctx context.Context, t autoCheckTiming, send func(any) error) {
	timer := time.NewTimer(jitterBetween(t.First/4, t.First))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		wait, exact := c.cycle(ctx, t, send)
		if exact {
			timer.Reset(windowWake(wait))
		} else {
			timer.Reset(jitterAround(wait, 0.1))
		}
	}
}

// windowWake 是「等到时间段开始」的实际等待：不能像普通周期那样上下浮动 10%（提前醒只是多睡一次，推迟醒却可能错过
// 短的时间段），只在后面加一点点随机（1~90 秒），让一批主机不会在同一秒涌向 Hub。
func windowWake(d time.Duration) time.Duration {
	return d + jitterBetween(time.Second, 90*time.Second)
}

// cycle 是一个完整的周期：检查；检查得出「有新版本」且本机选的是静默模式，就继续走静默升级。
// 返回距离下一个周期的间隔；exact 为 true 表示这个间隔是「等到时间段开始」，不应再做上下浮动。
func (c *autoChecker) cycle(ctx context.Context, t autoCheckTiming, send func(any) error) (wait time.Duration, exact bool) {
	settled, available := c.check(ctx, t.Timeout, send)
	wait = t.Every
	if !settled && t.Retry > 0 {
		wait = t.Retry
	}
	if available != "" && c.cfg.autoState() == termproto.AutoUpgradeSilent {
		return c.install(ctx, t, available, send)
	}
	return wait, false
}

// checkOnce 做一次检查：现读本机的模式（不是提示 / 静默就什么都不发），发出检查，等回答并处理。
// 返回值表示这次是否拿到了确定的答案（有新版本 / 已是最新）；没拿到（总闸关着、没有适用的程序、没有回答、发不出去……）时调用方应该早一点再问。
func (c *autoChecker) checkOnce(ctx context.Context, timeout time.Duration, send func(any) error) (settled bool) {
	settled, _ = c.check(ctx, timeout, send)
	return settled
}

// check 是 checkOnce 的完整版：available 非空表示本机确认 Hub 上有比自己更新的版本（就是那个版本号）。
func (c *autoChecker) check(ctx context.Context, timeout time.Duration, send func(any) error) (settled bool, available string) {
	mode := c.cfg.autoState()
	if !termproto.ValidAutoUpgradeMode(mode) {
		return true, "" // 本机没开启：没有什么要问的，不必为此提前再来
	}
	id, err := randomHex(16)
	if err != nil {
		return false, ""
	}
	ch := make(chan termproto.AutoUpgradeOffer, 1)
	c.mu.Lock()
	c.waiting[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiting, id)
		c.mu.Unlock()
	}()

	if err := send(termproto.AutoUpgradeCheck{T: termproto.TAutoUpgradeCheck, ID: id}); err != nil {
		return false, "" // 连接已经断了：循环随连接一起结束，下一条连接上重新开始
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case o := <-ch:
		return c.handle(mode, o)
	case <-timer.C:
		c.conclude(mode, autoCheckNoReply, "")
		return false, ""
	case <-ctx.Done():
		return true, "" // 连接结束了，循环马上也会结束
	}
}

// handle 处理 Hub 的回答，返回它是不是确定的答案，以及（有新版本时）那个版本号。
// too_soon 不是结论（Hub 在限流，稍后再问），其余都会被记录。
func (c *autoChecker) handle(mode string, o termproto.AutoUpgradeOffer) (settled bool, available string) {
	status := o.Status
	if status == termproto.AutoOfferTooSoon {
		return false, ""
	}
	// 不盲信：Hub 说有新版本，本机自己比较后版本并不更旧（相同、更新、或无法比较），一律当作已是最新。
	if status == termproto.AutoOfferAvailable && termproto.RelateAgentVersion(c.version, o.Version) != termproto.VersionOlder {
		status = termproto.AutoOfferUpToDate
	}
	c.conclude(mode, status, o.Version)
	if status == termproto.AutoOfferAvailable {
		return true, o.Version
	}
	return status == termproto.AutoOfferUpToDate, ""
}

// conclude 记下一次检查的结论：写本机的检查结果文件，并在结论变化时写一行日志。
func (c *autoChecker) conclude(mode, status, latest string) {
	note := AutoCheckNote{At: c.cfg.now().Unix(), Status: status, Latest: latest, Current: c.version}
	if c.cfg.AutoChecked != nil {
		c.cfg.AutoChecked(note)
	}
	key := status + "|" + latest
	c.mu.Lock()
	c.last = note
	changed := key != c.lastLog
	c.lastLog = key
	c.mu.Unlock()
	if changed {
		c.logf("%s", describeAutoCheck(mode, c.version, status, latest))
	}
}

// describeAutoCheck 把一次检查的结论整理成一行给主机所有者看的日志。
func describeAutoCheck(mode, current, status, latest string) string {
	label := "自动升级（提示）"
	if mode == termproto.AutoUpgradeSilent {
		label = "自动升级（静默）"
	}
	switch status {
	case termproto.AutoOfferAvailable:
		if mode == termproto.AutoUpgradeSilent {
			return fmt.Sprintf("%s：Hub 上有新版本 %s（本机 %s）", label, latest, orUnknownVersion(current))
		}
		return fmt.Sprintf("%s：Hub 上有新版本 %s（本机 %s）。提示模式下 Agent 不会自己下载或安装；要升级，请在 Web 主机页点「升级」，或在本机按升级窗口里的命令操作", label, latest, orUnknownVersion(current))
	case termproto.AutoOfferUpToDate:
		return fmt.Sprintf("%s：已是最新（Hub 上最新版本 %s）", label, orUnknownVersion(latest))
	case termproto.AutoOfferDisabled:
		return label + "：Hub 的自动升级总闸是关的，不会收到新版本的提示（需要 Hub 管理员在设置里打开）"
	case termproto.AutoOfferUnavailable:
		return label + "：Hub 这边没有适用于本机的 Agent 程序，或没有记录版本，暂时无法提示"
	case termproto.AutoOfferNotAllowed:
		return label + "：Hub 认为这台主机没有开启自动升级（刚改过本机设置的话，下一次握手后会同步）"
	case autoCheckNoReply:
		return label + "：Hub 没有回答版本检查（可能是较旧的版本，不认识自动升级）；稍后会再问一次"
	}
	return label + "：收到 Hub 的回答 " + status
}

func orUnknownVersion(v string) string {
	if v == "" {
		return "（未知）"
	}
	return v
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// jitterBetween 在 [lo, hi] 里随机取一个时长（hi <= lo 时返回 lo，且至少 1 毫秒）。
func jitterBetween(lo, hi time.Duration) time.Duration {
	if lo < time.Millisecond {
		lo = time.Millisecond
	}
	if hi <= lo {
		return lo
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(hi-lo)+1))
	if err != nil {
		return lo
	}
	return lo + time.Duration(n.Int64())
}

// jitterAround 在 d 的上下 frac 比例内随机浮动（至少 1 毫秒）。
func jitterAround(d time.Duration, frac float64) time.Duration {
	delta := time.Duration(float64(d) * frac)
	return jitterBetween(d-delta, d+delta)
}
