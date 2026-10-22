package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/relsign"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 静默自动升级的「安装」一侧（方案见文档库 design/agent-auto-upgrade.md B 层）。
//
// 检查（autocheck.go）得出「Hub 上有比本机更新的版本」之后，本机选的是静默模式才会走到这里。一个周期里依次是：
// 现读设置与可信公钥 → 这个版本是否已经试满 → 是否在允许的时间段 → 向 Hub 发请求 → 收到批准后复用 Web 一键升级的
// 下载、校验、交接链路（upgrader.runGuarded），区别是：先记一次尝试、下载后先验发布签名再运行新程序。
//
// 几条不变量：
//   - 没有可信公钥、设置读不清、同意被收回、签名对不上：一律不运行下载到的程序，也不交接；
//   - 一律是 Agent 先发请求，Hub 才会给出下载凭据；没有对应请求的批准会被丢弃；
//   - 尝试记录先于下载写好，同一个版本最多试 maxAutoAttempts 次；记录写不下去就放弃；
//   - 与 Web 一键升级共用「进行中」名额，两者不会同时进行。

// 静默升级记录（AutoCheckNote.Install）里的取值。
const (
	autoInstallWaitingWindow = "waiting_window" // 现在不在允许的时间段内，等它开始
	autoInstallPaused        = "paused"         // 这个版本已经试满次数，暂停
	autoInstallNoKeys        = "no_keys"        // 没有可信的发布公钥
	autoInstallBadSettings   = "bad_settings"   // 设置里有读不懂的内容
	autoInstallDeferred      = "deferred"       // Hub 暂时不批准（忙、总闸关……）或没有回答，稍后再来
	autoInstallUnsigned      = "unsigned"       // Hub 上的这个版本没有发布签名
	autoInstallStarted       = "started"        // 已经拿到批准，开始升级
	autoInstallFailed        = "failed"         // 升级没有完成（原因在 Detail）
)

// 静默升级在这些情况下的重试间隔由 autoCheckTiming 决定：拿不到批准（忙、总闸关……）用 Retry；
// 已经确定「这一轮没有可做的事」（没有公钥、已暂停、没有签名）用 Every。

// install 是一个周期里「检查得出有新版本」之后的静默升级部分，返回距离下一个周期的间隔；
// exact 为 true 表示这个间隔是「等到时间段开始」，调用方不应再做上下浮动。
func (c *autoChecker) install(ctx context.Context, t autoCheckTiming, version string, send func(any) error) (wait time.Duration, exact bool) {
	set, ok := c.cfg.autoSettings()
	if !ok || set.Mode != termproto.AutoUpgradeSilent {
		return t.Every, false // 检查之后所有者改了主意：下一个周期再按新的选择来
	}
	if set.Problem != "" {
		c.concludeInstall(autoInstallBadSettings, set.Problem)
		return t.Every, false
	}
	if len(set.Keys()) == 0 {
		c.concludeInstall(autoInstallNoKeys, "没有可信的发布公钥（编进程序的和 --trust-key 钉住的都没有），无法验证新版本")
		return t.Every, false
	}
	if c.upg == nil || c.cfg.AutoAttempts == nil || c.cfg.AutoAttempted == nil {
		c.concludeInstall(autoInstallFailed, "这个 Agent 无法记录升级尝试，静默升级不会进行")
		return t.Every, false
	}
	if att := c.cfg.AutoAttempts(); att.Paused(version) {
		detail := fmt.Sprintf("%s 已经尝试升级 %d 次都没有成功", version, att.Count)
		if att.Unreadable {
			detail = "升级尝试的记录读不清，为稳妥起见暂停"
		}
		c.concludeInstall(autoInstallPaused, detail+"，已暂停；出现更新的版本，或在本机重新执行 auto-upgrade enable，会再试")
		return t.Every, false
	}

	if d := set.Window.UntilOpen(c.cfg.now()); d > 0 {
		c.concludeInstall(autoInstallWaitingWindow, fmt.Sprintf("新版本 %s 要等到允许的时间段（%s）才升级，约 %s 后开始", version, set.WindowText, roundWait(d)))
		if d > t.Every {
			return t.Every, false // 隔得太久：先回来刷新一次检查结果，再重新算
		}
		return d, true
	}

	g, answered := c.requestGrant(ctx, t.Timeout, version, send)
	if !answered {
		c.concludeInstall(autoInstallDeferred, "Hub 没有回答升级请求（可能是较旧的版本，不认识静默升级）；稍后再试")
		return retryOr(t), false
	}
	switch g.Status {
	case termproto.AutoGrantGranted:
		// 批准里的版本必须就是本机请求的那一个：Hub 无权在本机不知情的情况下换成别的版本。
		if g.Version != version {
			c.concludeInstall(autoInstallDeferred, fmt.Sprintf("Hub 批准的版本 %s 与请求的 %s 不一致，已忽略", g.Version, version))
			return retryOr(t), false
		}
		return c.runGranted(ctx, t, g, send)
	case termproto.AutoGrantUnsigned:
		c.concludeInstall(autoInstallUnsigned, fmt.Sprintf("Hub 上的新版本 %s 没有发布签名，静默升级不会升级到它（需要发布流程给它签名）", version))
		return t.Every, false
	case termproto.AutoGrantUpToDate:
		c.concludeInstall(autoInstallDeferred, "Hub 认为这台主机已是最新，不需要升级")
		return t.Every, false
	default: // busy / disabled / not_allowed / stale / unavailable / too_soon
		c.concludeInstall(autoInstallDeferred, describeGrantStatus(g.Status))
		return retryOr(t), false
	}
}

func retryOr(t autoCheckTiming) time.Duration {
	if t.Retry > 0 {
		return t.Retry
	}
	return t.Every
}

// roundWait 把等待时长整理成给人看的粒度（分钟）。
func roundWait(d time.Duration) time.Duration {
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}

// describeGrantStatus 把 Hub 不批准的原因整理成一句话。
func describeGrantStatus(status string) string {
	switch status {
	case termproto.AutoGrantBusy:
		return "Hub 暂时不批准升级（已经有一次升级在进行，或这台主机上有运行中的会话会因此中断）；稍后再试"
	case termproto.AutoGrantDisabled:
		return "Hub 的自动升级总闸是关的，暂不升级；稍后再试"
	case termproto.AutoGrantNotAllowed:
		return "Hub 认为这台主机没有开启静默自动升级（刚改过本机设置的话，下一次握手后会同步）；稍后再试"
	case termproto.AutoGrantStale:
		return "Hub 上的最新版本在请求期间变了，稍后重新检查"
	case termproto.AutoGrantUnavailable:
		return "Hub 这边没有适用于本机的 Agent 程序，或没有记录版本；稍后再试"
	case termproto.AutoGrantTooSoon:
		return "升级请求发得太频繁，Hub 要求稍等；稍后再试"
	}
	return "Hub 没有批准升级（" + status + "）；稍后再试"
}

// requestGrant 向 Hub 发出升级请求并等裁决。answered 为 false 表示没有拿到裁决（发不出去、超时、连接结束）。
func (c *autoChecker) requestGrant(ctx context.Context, timeout time.Duration, version string, send func(any) error) (g termproto.AutoUpgradeGrant, answered bool) {
	id, err := randomHex(16)
	if err != nil {
		return termproto.AutoUpgradeGrant{}, false
	}
	ch := make(chan termproto.AutoUpgradeGrant, 1)
	c.mu.Lock()
	c.granting[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.granting, id)
		c.mu.Unlock()
	}()

	if err := send(termproto.AutoUpgradeRequest{T: termproto.TAutoUpgradeRequest, ID: id, Version: version}); err != nil {
		return termproto.AutoUpgradeGrant{}, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case g := <-ch:
		return g, true
	case <-timer.C:
		return termproto.AutoUpgradeGrant{}, false
	case <-ctx.Done():
		return termproto.AutoUpgradeGrant{}, false
	}
}

// runGranted 在拿到批准之后走升级链路。链路会阻塞到交接完成（或失败）；交接成功时升级进程随后会重启 Agent。
func (c *autoChecker) runGranted(ctx context.Context, t autoCheckTiming, g termproto.AutoUpgradeGrant, send func(any) error) (time.Duration, bool) {
	c.concludeInstall(autoInstallStarted, fmt.Sprintf("Hub 已批准，开始升级到 %s（本机 %s）", g.Version, orUnknownVersion(c.version)))
	report := func(st termproto.UpgradeStatus) { _ = send(st) }
	result := c.upg.runGuarded(ctx, g.ToUpgrade(), report, c.autoGuard(g))
	if ctx.Err() != nil {
		// 连接结束了（升级成功时 Agent 被重启也是这个样子）：不在这里下结论，新的进程会重新检查。
		return retryOr(t), false
	}
	switch result {
	case upgradeNotDone:
		c.concludeInstall(autoInstallFailed, "升级没有完成（原因已报告给 Hub，也写在日志里）；已记一次尝试，稍后再试")
	case upgradeApplyFailed:
		c.concludeInstall(autoInstallFailed, "升级进程已经启动，但没有完成替换或重启；已记一次尝试，稍后再试")
	}
	// upgradeHandedOff：已交接而且没有看到失败。升级成功时 Agent 马上会被重启，这里不下任何结论。
	return retryOr(t), false
}

// autoGuard 是静默升级在 upgrader 里的放行条件：同意仍是静默、设置没有问题、信任名单非空；只升不降；
// 先记一次尝试再下载；下载后对着磁盘上那份文件的内容验发布签名。
func (c *autoChecker) autoGuard(g termproto.AutoUpgradeGrant) upgradeGuard {
	silentAllowed := func() bool {
		set, ok := c.cfg.autoSettings()
		return ok && set.Mode == termproto.AutoUpgradeSilent && set.Problem == "" && len(set.Keys()) > 0
	}
	return upgradeGuard{
		label:         "静默自动升级",
		allowed:       silentAllowed,
		notAllowedMsg: "这台主机没有开启静默自动升级，或没有可信的发布公钥：请由主机所有者在本机执行 relaymesh-agent auto-upgrade status 查看",
		revokedMsg:    "升级过程中，主机所有者关闭了静默自动升级，已放弃",
		requireOlder:  true,
		started:       func() error { return c.cfg.AutoAttempted(g.Version) },
		authenticate: func(file string) error {
			set, ok := c.cfg.autoSettings()
			if !ok || set.Problem != "" {
				return fmt.Errorf("读不到可用的自动升级设置")
			}
			// 对磁盘上那份文件重新算一遍 SHA-256 再验签：被验证的正是接下来要运行、要安装的那些字节，
			// 不依赖下载环节已经做过的比较。
			sum, err := sha256File(file)
			if err != nil {
				return fmt.Errorf("无法读取下载的程序: %w", err)
			}
			return relsign.Verify(set.Keys(), g.Sig, g.Version, c.cfg.platform(), sum)
		},
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// concludeInstall 记下静默升级在这一轮的决定：在最近一次检查结论的基础上补上 Install 与 Detail 写进本机的检查结果文件，
// 并在决定变化时写一行日志。
func (c *autoChecker) concludeInstall(install, detail string) {
	c.mu.Lock()
	note := c.last
	if note.Status == "" { // 理论上检查先于安装；保险起见补一个最小的记录
		note = AutoCheckNote{Status: termproto.AutoOfferAvailable, Current: c.version}
	}
	note.At = c.cfg.now().Unix()
	note.Install, note.Detail = install, detail
	key := "install|" + install + "|" + detail
	changed := key != c.lastLog
	c.lastLog = key
	c.mu.Unlock()
	if c.cfg.AutoChecked != nil {
		c.cfg.AutoChecked(note)
	}
	if changed {
		c.logf("自动升级（静默）：%s", detail)
	}
}
