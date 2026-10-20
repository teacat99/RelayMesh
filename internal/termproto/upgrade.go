package termproto

import (
	"errors"
	"regexp"
)

// Agent 的「Web 一键升级」协议（方案见 .cursor/planning/2026-10-08-agent-web-upgrade-plan.md）。
//
// 信任边界：是否允许从 Web 升级，由主机所有者在主机本地决定（relaymesh-agent web-upgrade on），Hub 无法替他选。
// Agent 在握手里声明这个开关的状态（Meta.WebUpgrade）；只有声明 "on" 的 Agent 才会响应 upgrade 帧，其余一律拒绝。

// 文本控制帧类型。
const (
	TUpgrade       = "upgrade"        // Hub → Agent：请 Agent 自己下载新版本并升级（仅在 Meta.WebUpgrade == "on" 时发送）
	TUpgradeStatus = "upgrade_status" // Agent → Hub：升级进展 / 拒绝 / 失败
)

// Meta.WebUpgrade 的取值。空串表示这个 Agent 不支持（旧版本、前台运行、临时身份、暂不支持的系统）。
const (
	WebUpgradeOff = "off" // 支持，但主机所有者没有允许
	WebUpgradeOn  = "on"  // 支持，且主机所有者已允许
)

// UpgradeStatus.Stage 的取值。
const (
	UpgradeStageDownloading = "downloading" // 已接受请求，正在下载并校验
	UpgradeStageHandoff     = "handoff"     // 已校验通过，交给与 Agent 进程树无关的升级进程；Agent 随后会被重启
	UpgradeStageRefused     = "refused"     // 没有开始（本地开关、版本关系、正在进行等）
	UpgradeStageFailed      = "failed"      // 开始了但没有完成（下载、校验、交接失败）；已安装的程序没有被改动
)

// UpgradeStatus.Code 的取值（Stage 为 refused / failed 时给出）。
const (
	UpgradeCodeNotSupported = "not_supported" // 这个 Agent 不是以可升级的常驻服务运行
	UpgradeCodeNotAllowed   = "not_allowed"   // 主机所有者没有允许从 Web 升级
	UpgradeCodeUpToDate     = "up_to_date"    // 目标版本不比当前新
	UpgradeCodeBusy         = "busy"          // 已经有一次升级在进行
	UpgradeCodeBadRequest   = "bad_request"   // 请求的字段不合规
	UpgradeCodeDownload     = "download"      // 下载失败
	UpgradeCodeChecksum     = "checksum"      // 下载内容与 Hub 给出的 SHA-256 不一致
	UpgradeCodeSignature    = "signature"     // 下载内容没有通过发布签名验证（静默自动升级才要求）
	UpgradeCodeRecord       = "record"        // 无法在本机记录这次升级尝试（静默自动升级先记尝试再下载）
	UpgradeCodeVerify       = "verify"        // 新程序无法运行，或报出的版本不是期望的版本
	UpgradeCodeHandoff      = "handoff"       // 无法启动升级进程
	UpgradeCodeApply        = "apply"         // 升级进程已启动，但没能完成替换或重启（Agent 因此一直活着）
)

// UpgradeNote.Outcome 的取值。
const (
	UpgradeOutcomeOK         = "ok"          // 升级完成，新版本已连上服务端
	UpgradeOutcomeRolledBack = "rolled_back" // 新版本没有连上服务端，已还原为旧版本
	UpgradeOutcomeFailed     = "failed"      // 升级进程没能完成（例如替换或重启失败）
)

const (
	// MaxUpgradeDetailRunes 限制自报的升级说明长度。
	MaxUpgradeDetailRunes = 240
	// MaxUpgradeVersionRunes 与分发目录 VERSION 文件允许的版本号长度一致。
	MaxUpgradeVersionRunes = 64
)

var (
	upgradeIDRe      = regexp.MustCompile(`^[0-9a-f]{16,32}$`)
	upgradeTicketRe  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	upgradeSHA256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	upgradeVersionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	platformRe       = regexp.MustCompile(`^[a-z0-9]{2,16}-[a-z0-9]{2,16}$`)
)

// ValidAgentVersion 判断 s 是否是可以出现在升级请求里的版本号（与分发目录 VERSION 文件的规则一致）。
func ValidAgentVersion(s string) bool { return upgradeVersionRe.MatchString(s) }

// SanitizePlatform 校验 Agent 自报的平台标识（GOOS-GOARCH，如 linux-amd64）：写法不合规一律当作没有声明。
// 它只用来在分发目录里找对应的二进制，所以只接受小写字母与数字组成的两段，绝不放行路径或空白。
func SanitizePlatform(s string) string {
	if platformRe.MatchString(s) {
		return s
	}
	return ""
}

// Upgrade 请求 Agent 自己下载新版本并升级。只会在管理员（已登录且已解锁）点了「一键升级」、
// 并且这台主机的 Agent 声明了 Meta.WebUpgrade == "on" 时发送。
//
// 帧里**没有 URL 也没有文件名**：Agent 用自己配置的服务端地址与自己的 GOOS/GOARCH 推出下载地址，
// 所以 Hub（或冒充 Hub 的人）无法借它让 Agent 去访问任意地址。
type Upgrade struct {
	T string `json:"t"`
	// ID 是这次升级的标识，Agent 在 upgrade_status 里原样带回。
	ID string `json:"id"`
	// Version 是目标版本（Hub 自带的 Agent 版本）。
	Version string `json:"version"`
	// Ticket 是这台主机的升级窗口票据（/i/<票据>/<文件名>）。
	Ticket string `json:"ticket"`
	// SHA256 是目标平台的 Agent 二进制的 SHA-256（小写十六进制）。
	SHA256 string `json:"sha256"`
}

// Validate 检查字段格式。Agent 收到后必须先过它，不合规的一律拒绝。
func (u Upgrade) Validate() error {
	switch {
	case !upgradeIDRe.MatchString(u.ID):
		return errors.New("upgrade id 格式不对")
	case !ValidAgentVersion(u.Version):
		return errors.New("目标版本号格式不对")
	case !upgradeTicketRe.MatchString(u.Ticket):
		return errors.New("升级票据格式不对")
	case !upgradeSHA256Re.MatchString(u.SHA256):
		return errors.New("SHA-256 格式不对")
	}
	return nil
}

// ValidUpgradeID 判断 id 是否是合规的升级标识（Hub 生成时按它自检）。
func ValidUpgradeID(id string) bool { return upgradeIDRe.MatchString(id) }

// UpgradeStatus 是 Agent 对升级请求的进展报告。
type UpgradeStatus struct {
	T      string `json:"t"`
	ID     string `json:"id"`
	Stage  string `json:"stage"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Sanitized 返回净化后的副本（全部字段来自对端）；阶段不认识时返回 false。
func (s UpgradeStatus) Sanitized() (UpgradeStatus, bool) {
	if !upgradeIDRe.MatchString(s.ID) {
		return UpgradeStatus{}, false
	}
	switch s.Stage {
	case UpgradeStageDownloading, UpgradeStageHandoff, UpgradeStageRefused, UpgradeStageFailed:
	default:
		return UpgradeStatus{}, false
	}
	return UpgradeStatus{
		T:      TUpgradeStatus,
		ID:     s.ID,
		Stage:  s.Stage,
		Code:   SanitizeText(s.Code, 24),
		Detail: SanitizeText(s.Detail, MaxUpgradeDetailRunes),
	}, true
}

// UpgradeNote 是 Agent 本地保存的「上一次升级的结果」，握手时随 Meta 带给 Hub。
// 升级进程把它写进数据目录；新 Agent 上线（升级成功）或旧 Agent 被还原后重新上线（回滚）都会把它报出来，
// 这样即使 Agent 在升级中途被重启，管理员也能在 Web 上看到结果与原因，而不必去翻主机的日志。
type UpgradeNote struct {
	Outcome string `json:"outcome"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	// At 是结果产生的 Unix 秒（主机时钟）。
	At     int64  `json:"at,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Sanitized 返回净化后的副本；结果类型不认识时返回 nil。
func (n UpgradeNote) Sanitized() *UpgradeNote {
	switch n.Outcome {
	case UpgradeOutcomeOK, UpgradeOutcomeRolledBack, UpgradeOutcomeFailed:
	default:
		return nil
	}
	out := UpgradeNote{
		Outcome: n.Outcome,
		From:    SanitizeText(n.From, MaxUpgradeVersionRunes),
		To:      SanitizeText(n.To, MaxUpgradeVersionRunes),
		Detail:  SanitizeText(n.Detail, MaxUpgradeDetailRunes),
	}
	if n.At > 0 {
		out.At = n.At
	}
	return &out
}
