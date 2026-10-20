package termproto

import (
	"errors"
	"regexp"
)

// Agent 的「自动升级」协议（方案见文档库 design/agent-auto-upgrade.md）。
//
// 信任边界与「Web 一键升级」一致：是否自动升级由主机所有者在主机本地决定
// （relaymesh-agent auto-upgrade enable --mode notify|silent），Hub 无法替他选，也无法远程打开。
// Agent 在握手里声明本机选的模式（Meta.AutoUpgrade）；Hub 从不主动推送——它只回答 Agent 主动发来的检查。
// 这样「什么时候去问、问完怎么办」始终是主机自己的决定，Hub 一侧只有「总闸」可以让它一律回答「不提供」。

// 文本控制帧类型。
const (
	TAutoUpgradeCheck   = "auto_upgrade_check"   // Agent → Hub：问「有没有比我新的版本」
	TAutoUpgradeOffer   = "auto_upgrade_offer"   // Hub → Agent：对一次检查的回答
	TAutoUpgradeRequest = "auto_upgrade_request" // Agent → Hub：静默模式下，「我现在就要升级到这个版本，请给我下载凭据」
	TAutoUpgradeGrant   = "auto_upgrade_grant"   // Hub → Agent：对一次请求的裁决；只有 granted 带下载凭据与发布签名
)

// Meta.AutoUpgrade 的取值。空串表示这个 Agent 不提供自动升级（旧版本、前台运行、临时身份、暂不支持的系统）。
const (
	AutoUpgradeOff    = "off"    // 支持，但主机所有者没有开启
	AutoUpgradeNotify = "notify" // 只提示：发现新版本只记录与显示，不下载、不安装
	AutoUpgradeSilent = "silent" // 静默：发现新版本后自己下载、验签、升级（要求发布产物带独立签名）
)

// ValidAutoUpgradeMode 判断 s 是否是「主机所有者可以选的开启模式」（不含 off：关闭是删除同意，不是一种模式）。
func ValidAutoUpgradeMode(s string) bool { return s == AutoUpgradeNotify || s == AutoUpgradeSilent }

// AutoUpgradeOffer.Status 的取值。
const (
	AutoOfferDisabled    = "disabled"    // Hub 的总闸是关的：不提供任何自动升级
	AutoOfferNotAllowed  = "not_allowed" // 这台主机没有声明开启（模式 off / 没有声明）
	AutoOfferUpToDate    = "up_to_date"  // 没有比当前更新的版本
	AutoOfferAvailable   = "available"   // 有更新的版本，Version 是它
	AutoOfferUnavailable = "unavailable" // Hub 这边没有适用于这台主机的构建，或没有版本清单
	AutoOfferTooSoon     = "too_soon"    // 检查得太频繁，这次不回答实质内容
)

// AutoUpgradeCheck 是 Agent 的一次检查。帧里只有标识：版本与平台以握手时声明的为准（Hub 本来就有），
// 这样 Agent 无法借检查帧让 Hub 去查任意版本或任意平台。
type AutoUpgradeCheck struct {
	T  string `json:"t"`
	ID string `json:"id"`
}

// Validate 检查字段格式。
func (c AutoUpgradeCheck) Validate() error {
	if !upgradeIDRe.MatchString(c.ID) {
		return errors.New("auto_upgrade_check id 格式不对")
	}
	return nil
}

// AutoUpgradeOffer 是 Hub 对检查的回答。Agent 收到后必须先过 Sanitized：它来自对端，不可信。
type AutoUpgradeOffer struct {
	T      string `json:"t"`
	ID     string `json:"id"`
	Status string `json:"status"`
	// Version 是 Hub 上最新的 Agent 版本；Status 为 available / up_to_date 时给出。
	Version string `json:"version,omitempty"`
}

// Sanitized 返回净化后的副本；标识或状态不认识时返回 false。
func (o AutoUpgradeOffer) Sanitized() (AutoUpgradeOffer, bool) {
	if !upgradeIDRe.MatchString(o.ID) {
		return AutoUpgradeOffer{}, false
	}
	switch o.Status {
	case AutoOfferDisabled, AutoOfferNotAllowed, AutoOfferUpToDate, AutoOfferAvailable, AutoOfferUnavailable, AutoOfferTooSoon:
	default:
		return AutoUpgradeOffer{}, false
	}
	out := AutoUpgradeOffer{T: TAutoUpgradeOffer, ID: o.ID, Status: o.Status}
	if ValidAgentVersion(o.Version) {
		out.Version = o.Version
	}
	return out, true
}

// AutoUpgradeRequest 是静默模式的 Agent 在「决定现在就升级」时发出的请求：只有标识与它想升级到的版本。
// 版本、平台、模式仍以握手时声明的为准——Agent 无法借请求让 Hub 去签发别的平台、别的主机的东西。
// ID 同时是之后 upgrade_status 进展报告里的升级标识（Hub 把这次请求登记为一次升级）。
type AutoUpgradeRequest struct {
	T       string `json:"t"`
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Validate 检查字段格式。
func (r AutoUpgradeRequest) Validate() error {
	switch {
	case !upgradeIDRe.MatchString(r.ID):
		return errors.New("auto_upgrade_request id 格式不对")
	case !ValidAgentVersion(r.Version):
		return errors.New("auto_upgrade_request 的版本号格式不对")
	}
	return nil
}

// AutoUpgradeGrant.Status 的取值。
const (
	AutoGrantGranted     = "granted"     // 批准：帧里有下载凭据、SHA-256 与发布签名
	AutoGrantDisabled    = "disabled"    // Hub 的总闸是关的
	AutoGrantNotAllowed  = "not_allowed" // 这台主机没有声明静默模式（或是临时身份）
	AutoGrantUpToDate    = "up_to_date"  // 没有比当前更新的版本（或无法证明更新）
	AutoGrantStale       = "stale"       // Hub 上最新的版本已经不是请求的那个：请重新检查
	AutoGrantUnavailable = "unavailable" // Hub 没有适用于这台主机的构建，或没有版本清单
	AutoGrantUnsigned    = "unsigned"    // 这个发布版本没有发布签名：静默模式不会升级到它
	AutoGrantBusy        = "busy"        // 已经有一次升级在进行，或这台主机上有会损失的运行中会话：稍后再来
	AutoGrantTooSoon     = "too_soon"    // 请求得太频繁
)

// MaxSignatureTextLen 是帧里发布签名文本的长度上限（与 relsign.MaxSignatureLen 相同；termproto 不依赖 relsign，
// 所以只在这里限字符集与长度，真正的验证在 Agent 里）。
const MaxSignatureTextLen = 128

var grantSigRe = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,128}$`)

// AutoUpgradeGrant 是 Hub 对静默升级请求的裁决。Agent 收到后必须先过 Sanitized：它来自对端，不可信。
// 与 AutoUpgradeOffer 不同，granted 的裁决带有下载凭据——所以它只在 Agent 自己发出请求之后才会被采纳。
type AutoUpgradeGrant struct {
	T      string `json:"t"`
	ID     string `json:"id"`
	Status string `json:"status"`
	// 以下只在 Status 为 granted 时有：目标版本（与请求一致）、这台主机的升级窗口票据、
	// 目标平台程序的 SHA-256，以及发布流程给这个（版本、平台、SHA-256）签的名（rmsig1:…）。
	Version string `json:"version,omitempty"`
	Ticket  string `json:"ticket,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Sig     string `json:"sig,omitempty"`
}

// Sanitized 返回净化后的副本；标识或状态不认识、或 granted 缺少合规的凭据时返回 false。
// 不是 granted 的裁决里夹带的凭据一律丢弃。
func (g AutoUpgradeGrant) Sanitized() (AutoUpgradeGrant, bool) {
	if !upgradeIDRe.MatchString(g.ID) {
		return AutoUpgradeGrant{}, false
	}
	out := AutoUpgradeGrant{T: TAutoUpgradeGrant, ID: g.ID, Status: g.Status}
	switch g.Status {
	case AutoGrantGranted:
		if !ValidAgentVersion(g.Version) || !upgradeTicketRe.MatchString(g.Ticket) ||
			!upgradeSHA256Re.MatchString(g.SHA256) || !grantSigRe.MatchString(g.Sig) {
			return AutoUpgradeGrant{}, false
		}
		out.Version, out.Ticket, out.SHA256, out.Sig = g.Version, g.Ticket, g.SHA256, g.Sig
	case AutoGrantDisabled, AutoGrantNotAllowed, AutoGrantUpToDate, AutoGrantStale,
		AutoGrantUnavailable, AutoGrantUnsigned, AutoGrantBusy, AutoGrantTooSoon:
		if ValidAgentVersion(g.Version) {
			out.Version = g.Version
		}
	default:
		return AutoUpgradeGrant{}, false
	}
	return out, true
}

// ToUpgrade 把已净化的 granted 裁决转成 upgrade 帧的形状：Agent 复用 Web 一键升级的下载 / 校验 / 交接链路，
// 以及它对各字段的 Validate。
func (g AutoUpgradeGrant) ToUpgrade() Upgrade {
	return Upgrade{T: TUpgrade, ID: g.ID, Version: g.Version, Ticket: g.Ticket, SHA256: g.SHA256}
}

// 静默升级的时间段写法：HH:MM-HH:MM，主机本地时间的 24 小时制，可以跨午夜（如 23:00-02:00）。空串表示不限。
var autoWindowRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]-([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidAutoWindow 判断 s 是否是合规的时间段写法（不含空串）。起止相同的写法（如 03:00-03:00）含义含糊，不合规。
func ValidAutoWindow(s string) bool {
	return autoWindowRe.MatchString(s) && s[:5] != s[6:]
}
