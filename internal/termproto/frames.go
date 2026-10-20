package termproto

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// 文本（JSON）控制帧的类型标识，存放在每条消息的 "t" 字段。
const (
	// Agent ↔ Hub
	TChallenge    = "challenge"
	THello        = "hello"
	TWelcome      = "welcome"
	TApproved     = "approved"
	TRejected     = "rejected"
	TPolicy       = "policy"
	TUninstall    = "uninstall" // Hub → Agent：卸载本机的 Agent 常驻服务（删除主机时可选；仅在 Meta.Uninstall 声明支持时发送）
	TOpen         = "open"
	TOpened       = "opened"
	TOpenFailed   = "open_failed"
	TAttach       = "attach"
	TAttached     = "attached"
	TAttachFailed = "attach_failed"
	TDetach       = "detach"
	TDetached     = "detached"
	TResize       = "resize"
	TKill         = "kill"
	TRename       = "rename"
	TLifecycle    = "lifecycle"
	TExit         = "exit"
	TSessions     = "sessions"
	TBye          = "bye"
	THolder       = "holder"

	// Hub ↔ 浏览器
	TReady  = "ready"
	TAck    = "ack"
	TPing   = "ping"
	TPong   = "pong"
	TClosed = "closed"
	// TLag 通知浏览器「你跟不上输出了 / 追上了」（见 BrowserLag）。旧浏览器忽略不认识的控制帧。
	TLag = "lag"
)

// 主机状态在 welcome.status 中的取值。
const (
	WelcomeEnabled = "enabled"
	WelcomePending = "pending"
)

// 会话状态。
const (
	SessionRunning = "running"
	SessionExited  = "exited"
)

// 浏览器 WS 二进制帧类型：[类型 1B][…]。
const (
	BrowserOutput byte = 0x01 // [0x01][偏移 u64][输出]（Hub→浏览器）
	BrowserInput  byte = 0x02 // [0x02][输入]（浏览器→Hub）
)

// Agent↔Hub 二进制帧类型：[类型 1B][通道 u32][…]。
const (
	FrameOutput byte = 0x01 // [0x01][通道 u32][偏移 u64][输出]（Agent→Hub）
	FrameInput  byte = 0x02 // [0x02][通道 u32][输入]（Hub→Agent）
	FrameAck    byte = 0x03 // [0x03][通道 u32][已确认偏移 u64]（Hub→Agent）
)

const (
	outputHeaderLen = 1 + 4 + 8
	inputHeaderLen  = 1 + 4
	ackFrameLen     = 1 + 4 + 8
	browserOutHdr   = 1 + 8
)

// 二进制帧解码错误。
var (
	ErrShortFrame    = errors.New("frame too short")
	ErrWrongFrame    = errors.New("unexpected frame type")
	ErrPayloadTooBig = errors.New("frame payload too large")
)

// Envelope 仅用于先嗅探消息类型。
type Envelope struct {
	T string `json:"t"`
}

// ParseType 读取 JSON 控制帧的类型字段。
func ParseType(data []byte) (string, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return "", err
	}
	if e.T == "" {
		return "", errors.New("missing message type")
	}
	return e.T, nil
}

// Challenge 是服务端发出的一次性挑战。
type Challenge struct {
	T     string `json:"t"`
	V     int    `json:"v"`
	Nonce string `json:"nonce"`
}

// Meta 是 Agent 自报的元数据。**全部不可信**：使用前必须经过 Sanitized。
type Meta struct {
	Host string `json:"host"`
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Ver  string `json:"ver"`
	// Rev 是这个 Agent 二进制的「Agent 侧代码指纹」（见 agentversion.go）。版本号跟着整个仓库的构建走，代码没变也会变；
	// 指纹只在 Agent 依赖的代码变了才变，服务端据此判断「可升级」。旧版本与非发布构建没有这一项，服务端回退到版本号比较。
	Rev  string `json:"rev,omitempty"`
	Priv string `json:"priv"`
	Kind string `json:"kind"`
	// Files 声明这个 Agent 能处理文件请求（file_req）。旧版本没有这一项，服务端据此判断「主机不支持」。
	Files bool `json:"files,omitempty"`
	// FilesAll 声明这个 Agent 认识 FileRootAll 哨兵（令牌被明确授予「整个文件系统」）。没有它的旧版 Agent 会拒绝一切路径，
	// 服务端据此在发请求之前就给出「请先升级 Agent」的明确说明，而不是让模型看到莫名其妙的越界错误。
	FilesAll bool `json:"files_all,omitempty"`
	// Uninstall 声明这个 Agent 是以常驻服务运行、并且知道如何卸载自己（收到 uninstall 帧时）。
	// 前台运行 / 临时身份 / 暂不支持的系统都不声明；旧版本没有这一项，服务端据此不提供「同时卸载设备端」。
	Uninstall bool `json:"uninstall,omitempty"`
	// Plat 是这个 Agent 的平台标识（运行时的 GOOS-GOARCH，如 linux-amd64），与分发目录里 Agent 二进制的平台后缀同一写法。
	// OS 字段是给人看的系统描述（如「Debian GNU/Linux 12」），无法据此挑出对应的二进制，所以单独声明：
	// 服务端在发出升级请求之前就能确认「分发目录里有适用于这台主机的二进制」，并取到它的校验值。
	Plat string `json:"plat,omitempty"`
	// WebUpgrade 声明这个 Agent 能响应 Hub 的 upgrade 帧自己升级：WebUpgradeOn 表示主机所有者已在本机允许，
	// WebUpgradeOff 表示支持但没有允许；空串是不支持（旧版本 / 前台运行 / 临时身份 / 暂不支持的系统）。见 upgrade.go。
	WebUpgrade string `json:"web_upgrade,omitempty"`
	// LastUpgrade 是 Agent 本地保存的上一次升级的结果（升级成功 / 已回滚 / 失败）；没有则为空。
	LastUpgrade *UpgradeNote `json:"last_upgrade,omitempty"`
	// AutoUpgrade 声明主机所有者在本机为「自动升级」选的模式：AutoUpgradeNotify / AutoUpgradeSilent 表示已开启，
	// AutoUpgradeOff 表示支持但没有开启；空串是不支持（与 WebUpgrade 同样的条件，另加「旧版本」）。见 autoupgrade.go。
	AutoUpgrade string `json:"auto_upgrade,omitempty"`
	// AutoWindow 是主机所有者给静默自动升级限定的时间段（主机本地时间，HH:MM-HH:MM）；空表示不限。
	// 只用于展示：是否在时间段内由 Agent 自己判断，Hub 不据此做任何决定。
	AutoWindow string `json:"auto_window,omitempty"`
}

// Sanitized 返回净化并限长后的副本：控制字符、双向控制符、零宽字符、非法 UTF-8 一律去除；
// 枚举字段回落到安全默认值。界面与日志只按纯文本渲染这些字段。
func (m Meta) Sanitized() Meta {
	out := Meta{
		Host:  SanitizeText(m.Host, MaxHostnameRunes),
		OS:    SanitizeText(m.OS, MaxOSRunes),
		Arch:  SanitizeText(m.Arch, MaxArchRunes),
		Ver:   SanitizeText(m.Ver, MaxVersionRunes),
		Rev:   SanitizeAgentRev(m.Rev),
		Priv:  PrivUser,
		Kind:  KindPersistent,
		Files: m.Files,

		FilesAll:  m.FilesAll,
		Uninstall: m.Uninstall,
		Plat:      SanitizePlatform(m.Plat),
	}
	switch m.WebUpgrade {
	case WebUpgradeOn, WebUpgradeOff:
		out.WebUpgrade = m.WebUpgrade
	}
	if m.LastUpgrade != nil {
		out.LastUpgrade = m.LastUpgrade.Sanitized()
	}
	switch m.AutoUpgrade {
	case AutoUpgradeOff, AutoUpgradeNotify, AutoUpgradeSilent:
		out.AutoUpgrade = m.AutoUpgrade
	}
	if out.AutoUpgrade == AutoUpgradeSilent && ValidAutoWindow(m.AutoWindow) {
		out.AutoWindow = m.AutoWindow
	}
	switch m.Priv {
	case PrivRoot, PrivAdmin:
		out.Priv = m.Priv
	}
	if m.Kind == KindTemporary {
		out.Kind = KindTemporary
	}
	return out
}

// SessionInfo 描述 Agent 上的一个终端会话。
type SessionInfo struct {
	SID      string `json:"sid"`
	Name     string `json:"name,omitempty"`
	Rows     uint16 `json:"rows"`
	Cols     uint16 `json:"cols"`
	Base     uint64 `json:"base"` // 环形缓冲中最旧字节的累计偏移
	End      uint64 `json:"end"`  // 已产生输出的累计字节数
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Created  int64  `json:"created,omitempty"` // Agent 时钟的 Unix 秒，仅供展示（服务端不据此做任何 TTL 判定）
	Attached int    `json:"attached"`
	Shell    string `json:"shell,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Alt      bool   `json:"alt,omitempty"` // 当前处于备用屏（全屏程序如 vim）
	// Target 非空表示这是经 SSH 网关的会话：值是 Hub 侧那台 SSH 主机的机器码（新建时由 Hub 指定，守护进程原样保存）。
	Target string `json:"target,omitempty"`

	// 生命周期（H1）：Idle 是生效的「无交互多久后回收」秒数，0 表示永久保持；IdleOwn 表示这个会话单独设置过，
	// 否则沿用主机默认。Active 是最后一次交互（有输入，或附着数降为 0）的 Unix 秒；有人附着时即为「现在」。
	Idle    int   `json:"idle,omitempty"`
	IdleOwn bool  `json:"idle_own,omitempty"`
	Active  int64 `json:"active,omitempty"`

	// 录制状态（终端上显示什么就记录什么，内容只存在主机本地，这里只有索引）。
	Rec      string `json:"rec,omitempty"`       // on | paused | error | off | veto | incognito
	RecBytes int64  `json:"rec_bytes,omitempty"` // 主机上当前保留的记录字节数
	RecFirst int64  `json:"rec_first,omitempty"` // 最早保留内容的时间（Unix 秒）
	RecLast  int64  `json:"rec_last,omitempty"`  // 最近写入的时间（Unix 秒）
	RecGap   bool   `json:"rec_gap,omitempty"`   // 记录不完整（开头被清理或中途有缺口）

	// Screen 是屏幕扫描器（#113 M3.1）对 cursor-agent TUI 的识别结果；认不出任何东西时为空。
	// 只有 Agent 守护进程 / Hub 本地会话在「识别出变化」或定时刷新时才会更新它；旧版 Agent 没有这个字段。
	Screen *Screen `json:"screen,omitempty"`
}

// Screen 是一次屏幕扫描的线上格式。不含任何屏幕文字，只有状态词（取自白名单字符）、计数与标志。
type Screen struct {
	Word      string  `json:"word,omitempty"`      // 状态行动词（Thinking / Working / Globbing ...），仅「现在还在动画」时有值
	Tokens    int     `json:"tokens"`              // 状态行里的实时 token 数；-1 = 没读到
	CtxPct    float64 `json:"ctx_pct"`             // 页脚的上下文占用百分比；-1 = 没读到
	Approval  bool    `json:"approval,omitempty"`  // 屏幕停在「Waiting for approval...」
	Cancelled bool    `json:"cancelled,omitempty"` // 最近一屏是「Cancelled • 15s」
	Animating bool    `json:"animating,omitempty"` // 最近 2 秒仍有输出字节
	BPS       float64 `json:"bps"`                 // 最近 5 秒的平均字节速率
	Quiet     int     `json:"quiet"`               // 距最后一次输出的秒数；-1 = 从未有输出
}

// sanitizedScreen 收敛对端上报的 Screen：状态词只留英文字母（最长 24 个），数值夹到合理范围。
func sanitizedScreen(in *Screen) *Screen {
	if in == nil {
		return nil
	}
	out := *in
	word := make([]rune, 0, len(in.Word))
	for _, r := range in.Word {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			word = append(word, r)
			if len(word) >= 24 {
				break
			}
		}
	}
	out.Word = string(word)
	if out.Tokens < -1 || out.Tokens > 1_000_000_000 {
		out.Tokens = -1
	}
	if !(out.CtxPct >= 0 && out.CtxPct <= 100) { // 同时挡住 NaN
		out.CtxPct = -1
	}
	if !(out.BPS >= 0) || out.BPS > 1e12 {
		out.BPS = 0
	}
	if out.Quiet < -1 {
		out.Quiet = -1
	}
	return &out
}

var sidPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidSessionID 判断会话 ID 是否为 128 位小写十六进制。
func ValidSessionID(s string) bool { return sidPattern.MatchString(s) }

const (
	maxTermDim = 1000 // 行 / 列的上限，防止异常尺寸
)

// ClampSize 把终端尺寸限制在 [1, 1000]；0 视为未指定并回落到 24×80。
func ClampSize(rows, cols uint16) (uint16, uint16) {
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	if rows > maxTermDim {
		rows = maxTermDim
	}
	if cols > maxTermDim {
		cols = maxTermDim
	}
	return rows, cols
}

// Sanitized 返回净化后的会话信息；SID 非法时 ok=false，调用方应丢弃该条目。
func (s SessionInfo) Sanitized() (SessionInfo, bool) {
	if !ValidSessionID(s.SID) {
		return SessionInfo{}, false
	}
	out := s
	out.Name = SanitizeText(s.Name, MaxSessionNameRunes)
	out.Shell = SanitizeText(s.Shell, 128)
	if s.Target != "" && !ValidMachineCode(s.Target) {
		out.Target = ""
	}
	out.Rows, out.Cols = ClampSize(s.Rows, s.Cols)
	if out.State != SessionRunning && out.State != SessionExited {
		out.State = SessionExited
	}
	if out.End < out.Base {
		out.Base = out.End
	}
	if out.Attached < 0 {
		out.Attached = 0
	}
	switch out.Rec {
	case "", RecOn, RecPaused, RecError, RecOff, RecVeto, RecIncognito:
	default:
		out.Rec = ""
	}
	out.Idle = ClampIdle(out.Idle)
	if out.Active < 0 {
		out.Active = 0
	}
	if out.RecBytes < 0 {
		out.RecBytes = 0
	}
	if out.RecFirst < 0 {
		out.RecFirst = 0
	}
	if out.RecLast < 0 {
		out.RecLast = 0
	}
	out.Screen = sanitizedScreen(s.Screen)
	return out, true
}

// Hello 是 Agent 对挑战的应答。
type Hello struct {
	T        string        `json:"t"`
	V        int           `json:"v"`
	Pub      string        `json:"pub"`
	Sig      string        `json:"sig"`
	Meta     Meta          `json:"meta"`
	Sessions []SessionInfo `json:"sessions,omitempty"`
}

// ErrUnsupportedVersion 表示协议版本不兼容。
var ErrUnsupportedVersion = errors.New("unsupported protocol version")

// Decode 解出并校验公钥与签名的编码、长度与版本；不做验签（验签需要本连接的 nonce）。
func (h Hello) Decode() (ed25519.PublicKey, []byte, error) {
	if h.V != ProtocolVersion {
		return nil, nil, ErrUnsupportedVersion
	}
	pub, err := DecodeKey(h.Pub, ed25519.PublicKeySize)
	if err != nil {
		return nil, nil, fmt.Errorf("bad pub: %w", err)
	}
	sig, err := DecodeKey(h.Sig, ed25519.SignatureSize)
	if err != nil {
		return nil, nil, fmt.Errorf("bad sig: %w", err)
	}
	return ed25519.PublicKey(pub), sig, nil
}

// SanitizedSessions 净化会话清单：丢弃非法项与超出上限的多余项。
func (h Hello) SanitizedSessions() []SessionInfo {
	out := make([]SessionInfo, 0, len(h.Sessions))
	for _, s := range h.Sessions {
		if len(out) >= MaxSessionsPerHost {
			break
		}
		if clean, ok := s.Sanitized(); ok {
			out = append(out, clean)
		}
	}
	return out
}

// Policy 是服务端下发的策略；Agent 取「本地默认」与「策略」中更严的一方。
type Policy struct {
	Terminal       bool   `json:"terminal"`
	MaxSessions    int    `json:"max_sessions,omitempty"`
	DetachedTTLSec int    `json:"detached_ttl_sec,omitempty"`
	Reason         string `json:"reason,omitempty"` // terminal=false 时的总闸原因码

	// IdleSec 是主机默认的生命周期：连续这么多秒没有交互（没人附着、也没有输入）才回收会话；0 表示永久保持。
	// 会话可以单独覆盖（Open.IdleSec / SetLifecycle）。强制执行者是守护进程，Hub 或 Agent 离线时照常计时。
	IdleSec int `json:"idle_sec,omitempty"`

	// 录制策略：只能比 Agent 本机配置更严格（关闭 / 缩短保留 / 降低配额），不能强制开启或放宽。
	RecOff           bool `json:"rec_off,omitempty"`
	RecRetentionDays int  `json:"rec_retention_days,omitempty"`
	RecSessionMB     int  `json:"rec_session_mb,omitempty"`
	RecHostMB        int  `json:"rec_host_mb,omitempty"`
	// RecMaxLines 是单个会话记录保留的输出行数上限（按换行符计，超出后滚动丢弃最旧的分段）；0 表示不限制。
	// 同样只能比 Agent 本机配置更严格。旧版 Agent 不认识这个字段，会照旧只按字节上限滚动。
	RecMaxLines int `json:"rec_max_lines,omitempty"`
}

// Welcome 是握手通过后的应答。
type Welcome struct {
	T           string `json:"t"`
	Status      string `json:"status"`
	MachineCode string `json:"machine_code"`
	Policy      Policy `json:"policy"`
}

// Approved 在待启用的主机被管理员启用后，于同一连接推送。
type Approved struct {
	T      string `json:"t"`
	Policy Policy `json:"policy"`
}

// PolicyMsg 是运行期策略变更（如总闸翻转）。
type PolicyMsg struct {
	T      string `json:"t"`
	Policy Policy `json:"policy"`
}

// Uninstall 请求 Agent 卸载自己的常驻服务，效果等同于管理员在主机上手动执行 `relaymesh-agent uninstall`。
// 只会在管理员（已登录且已解锁）删除主机并勾选「同时卸载设备端」时发送，并且先于随后的关闭帧。
// Agent 在本机另起一个与自身进程树无关的卸载进程来完成（卸载会停止 Agent 自己，所以不能在自己的进程树里做）。
type Uninstall struct {
	T string `json:"t"`
	// Purge 同时删除身份文件与已信任服务器记录（等同 --purge）；终端录像与命令输出始终保留在本机，由主机所有者自行处理。
	Purge bool `json:"purge,omitempty"`
}

// Rejected 在待启用的主机被忽略时推送，随后以 CloseDisabled 关闭。
type Rejected struct {
	T      string `json:"t"`
	Reason string `json:"reason,omitempty"`
}

// Open 请求在 Agent 上新建会话。
type Open struct {
	T         string `json:"t"`
	Req       string `json:"req"`
	Name      string `json:"name,omitempty"`
	Rows      uint16 `json:"rows"`
	Cols      uint16 `json:"cols"`
	Incognito bool   `json:"incognito,omitempty"` // 无痕会话：不录制，主机上不留任何记录
	// Compact 为 true 时是「精简会话」：回放缓冲只有 CompactRingSize、终端宽度不超过 CompactMaxCols（会话守护预设用）。旧版 Agent 会忽略它。
	Compact bool `json:"compact,omitempty"`

	// IdleSec 为本会话单独设置的空闲回收秒数（0 = 永久保持）；nil 表示沿用主机默认。
	IdleSec *int `json:"idle_sec,omitempty"`
	// Cwd 是希望新会话启动时所在的目录（「在原目录重建会话」）；必须是主机上存在的绝对路径，否则回落到默认目录。
	Cwd string `json:"cwd,omitempty"`
	// SSH 非空表示这是一个经 SSH 网关的会话：不启动本机 shell，而是由本机的会话守护进程去连接这个目标。
	// 只在本机开启了 SSH 网关时才会被接受；open 只接受引用网关上已保存凭据的目标（Stored=true），帧里不带任何凭据；
	// 目标本身仍不应被随意打印（SSHTarget 自带脱敏）。
	SSH *SSHTarget `json:"ssh,omitempty"`
}

// SetLifecycle 修改一个已有会话的生命周期：IdleSec 为 nil 表示改回沿用主机默认，0 表示永久保持。
type SetLifecycle struct {
	T       string `json:"t"`
	SID     string `json:"sid"`
	IdleSec *int   `json:"idle_sec,omitempty"`
}

// Opened 是 Open 的成功应答，sid 由 Agent 生成。
type Opened struct {
	T       string      `json:"t"`
	Req     string      `json:"req"`
	Session SessionInfo `json:"session"`
}

// OpenFailed 是 Open 的失败应答。
type OpenFailed struct {
	T      string `json:"t"`
	Req    string `json:"req"`
	Reason string `json:"reason"`
	// Code 是 SSH 会话失败时的稳定错误码（SSHCode*），普通会话为空。
	Code string `json:"code,omitempty"`
}

// Attach 请求把一条数据通道附着到会话；通道号由 Hub 分配（每条 Agent 连接内唯一）。
type Attach struct {
	T     string `json:"t"`
	Ch    uint32 `json:"ch"`
	SID   string `json:"sid"`
	Since uint64 `json:"since"`
	Rows  uint16 `json:"rows,omitempty"`
	Cols  uint16 `json:"cols,omitempty"`
	// TailLines 只对全新附着（since = 0）有意义：缓冲量足够大、会话记录又在正常录制时，只回放主屏末尾这么多行，
	// 更早的内容由浏览器按需读取记录。0 = 整体回放（旧行为）。
	// 不认识这个字段的旧版本 Agent / 守护进程会忽略它，照常整体回放，所以无需同步升级。
	TailLines uint16 `json:"tail_lines,omitempty"`
}

// Attached 是 Attach 的成功应答，随后开始按偏移输出数据帧。
type Attached struct {
	T         string `json:"t"`
	Ch        uint32 `json:"ch"`
	SID       string `json:"sid"`
	Base      uint64 `json:"base"`
	End       uint64 `json:"end"`
	From      uint64 `json:"from"`                // 实际回放起点（首个数据帧的偏移）
	Truncated bool   `json:"truncated,omitempty"` // 请求的起点已被缓冲覆盖
	Prefix    string `json:"prefix,omitempty"`    // base64：应先写入终端的模式恢复序列（不占用偏移）
	Rows      uint16 `json:"rows"`
	Cols      uint16 `json:"cols"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}

// AttachFailed 是 Attach 的失败应答。
type AttachFailed struct {
	T      string `json:"t"`
	Ch     uint32 `json:"ch"`
	SID    string `json:"sid"`
	Reason string `json:"reason"`
}

// Detach 请求释放数据通道（Hub→Agent）。
type Detach struct {
	T  string `json:"t"`
	Ch uint32 `json:"ch"`
}

// Detached 是 Agent 主动释放通道的通知（会话结束、停滞等）。
type Detached struct {
	T      string `json:"t"`
	Ch     uint32 `json:"ch"`
	Reason string `json:"reason"`
}

// Resize 调整会话 PTY 尺寸（以最近一次为准）。
type Resize struct {
	T    string `json:"t"`
	SID  string `json:"sid"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// Kill 终止会话。
type Kill struct {
	T   string `json:"t"`
	SID string `json:"sid"`
}

// Rename 修改会话名。
type Rename struct {
	T    string `json:"t"`
	SID  string `json:"sid"`
	Name string `json:"name"`
}

// Exit 通知会话中的进程已退出。
type Exit struct {
	T      string `json:"t"`
	SID    string `json:"sid"`
	Code   *int   `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
}

// Sessions 是 Agent 上会话清单的全量更新。
type Sessions struct {
	T        string        `json:"t"`
	Sessions []SessionInfo `json:"sessions"`
}

// 会话守护进程（holder）的状态，取自 holder 帧的 state 字段。
const (
	HolderReady       = "ready"       // 守护进程可用，Agent 已与它建立控制连接
	HolderUnavailable = "unavailable" // 守护进程暂不可达：新建 / 附着会话会失败，现有会话是否存活未知
)

// 守护进程不可用的原因码（holder.reason）。
const (
	HolderReasonConnecting  = "connecting"  // 正在连接
	HolderReasonSuperseded  = "superseded"  // 另一个 Agent 实例取代了本实例的控制连接
	HolderReasonVersion     = "version"     // 本地协议主版本不一致
	HolderReasonUnreachable = "unreachable" // 连不上（守护进程没启动、被停止或崩溃）
)

// LostSession 描述一个已经不存在的会话以及它消失的原因，由守护进程在重启后上报。
type LostSession struct {
	SID     string `json:"sid"`
	Name    string `json:"name,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Created int64  `json:"created,omitempty"`
	Reason  string `json:"reason"`
	At      int64  `json:"at,omitempty"`
}

// 会话丢失原因（LostSession.Reason）。
const (
	LostHostReboot    = "host_reboot"
	LostHolderExit    = "holder_exit"
	LostHolderStopped = "holder_stopped"
	LostUnknown       = "unknown"
)

// Holder 是 Agent 向 Hub 报告的会话守护进程状态（Agent→Hub）。
// 内嵌模式下 Persistent=false：会话随 Agent 进程结束，界面据此标注「重启会中断」。
type Holder struct {
	T          string `json:"t"`
	State      string `json:"state"`
	Persistent bool   `json:"persistent"`
	Version    string `json:"version,omitempty"`
	// Rev 是守护进程自己的「Agent 侧代码指纹」：与 Agent 的 Meta.Rev 一致说明两者跑的是同一份代码，不需要重启守护进程。
	// 旧版本的守护进程没有这一项（服务端回退到能力位判断）。
	Rev     string        `json:"rev,omitempty"`
	BootID  string        `json:"boot_id,omitempty"`
	Started int64         `json:"started,omitempty"`
	Caps    []string      `json:"caps,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Lost    []LostSession `json:"lost,omitempty"`

	// DenyExec 是本机策略（G4）的摘要：为 true 表示这台主机用 --deny-model-exec 拒绝了一切模型运行（人工终端不受影响）。
	DenyExec bool `json:"deny_exec,omitempty"`
	// Exec 描述模型运行的执行环境（默认 shell、家目录、路径分隔符）；不支持模型运行的守护进程不带这一项。
	Exec *ExecInfo `json:"exec,omitempty"`
}

// ExecInfo 描述主机上模型运行使用的执行环境，用于 terminal_targets 让模型知道该写什么语法的命令。
type ExecInfo struct {
	Shell string `json:"shell,omitempty"` // 默认 shell 的名称（bash / sh / pwsh / powershell / cmd …）
	Home  string `json:"home,omitempty"`  // 默认工作目录（账户家目录）
	Sep   string `json:"sep,omitempty"`   // 路径分隔符
}

// MaxLostSessions 是一次 holder 帧里接受的「已丢失会话」条数上限。
const MaxLostSessions = 64

// Sanitized 返回净化后的副本：全部字段都来自对端，界面与日志只按纯文本渲染。
func (h Holder) Sanitized() Holder {
	out := Holder{
		T:          THolder,
		State:      HolderUnavailable,
		Persistent: h.Persistent,
		Version:    SanitizeText(h.Version, MaxVersionRunes),
		Rev:        SanitizeAgentRev(h.Rev),
		BootID:     SanitizeText(h.BootID, 64),
		Started:    h.Started,
		Reason:     SanitizeText(h.Reason, 32),
	}
	if h.State == HolderReady {
		out.State = HolderReady
	}
	for _, c := range h.Caps {
		if c = SanitizeText(c, 24); c != "" && len(out.Caps) < 16 {
			out.Caps = append(out.Caps, c)
		}
	}
	out.DenyExec = h.DenyExec
	if h.Exec != nil {
		ex := ExecInfo{
			Shell: SanitizeText(h.Exec.Shell, 32),
			Home:  SanitizeText(h.Exec.Home, 256),
			Sep:   SanitizeText(h.Exec.Sep, 2),
		}
		if ex.Sep != "/" && ex.Sep != "\\" {
			ex.Sep = ""
		}
		out.Exec = &ex
	}
	for _, l := range h.Lost {
		if len(out.Lost) >= MaxLostSessions {
			break
		}
		if !ValidSessionID(l.SID) {
			continue
		}
		reason := LostUnknown
		switch l.Reason {
		case LostHostReboot, LostHolderExit, LostHolderStopped:
			reason = l.Reason
		}
		out.Lost = append(out.Lost, LostSession{
			SID:     l.SID,
			Name:    SanitizeText(l.Name, MaxSessionNameRunes),
			Cwd:     SanitizeText(l.Cwd, 256),
			Created: l.Created,
			Reason:  reason,
			At:      l.At,
		})
	}
	return out
}

// Bye 通知对端即将关闭（附带原因，便于日志与界面提示）。
type Bye struct {
	T      string `json:"t"`
	Reason string `json:"reason,omitempty"`
}

// ---- 浏览器控制帧 ----

// BrowserReady 在浏览器附着成功后下发。
type BrowserReady struct {
	T         string `json:"t"`
	SID       string `json:"sid"`
	Base      uint64 `json:"base"`
	End       uint64 `json:"end"`
	From      uint64 `json:"from"`
	Truncated bool   `json:"truncated,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Rows      uint16 `json:"rows"`
	Cols      uint16 `json:"cols"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}

// BrowserExit 通知浏览器会话已结束。
type BrowserExit struct {
	T      string `json:"t"`
	Code   *int   `json:"code,omitempty"`
	Signal string `json:"signal,omitempty"`
}

// BrowserAck 是浏览器确认已渲染到某偏移（驱动流控窗口）。
type BrowserAck struct {
	T      string `json:"t"`
	Offset uint64 `json:"offset"`
}

// BrowserResize 是浏览器上报的终端尺寸。
type BrowserResize struct {
	T    string `json:"t"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// BrowserLag 告诉浏览器它跟不上会话输出的状态变化：On 为真表示持续饱和（浏览器应盖上「正在追赶」遮罩，
// 若饱和持续下去服务端会以 CloseLagging 断开并要求重新附着），为假表示已经追上。
type BrowserLag struct {
	T  string `json:"t"`
	On bool   `json:"on"`
}

// BrowserClosed 在服务端主动断开附着前给出原因。
type BrowserClosed struct {
	T      string `json:"t"`
	Reason string `json:"reason"`
}

// Marshal 把控制帧编码为 JSON；编码失败属于程序错误，返回 error 供调用方处理。
func Marshal(v any) ([]byte, error) { return json.Marshal(v) }

// ---- 二进制帧编解码 ----

// EncodeOutput 编码 Agent→Hub 的输出帧。
func EncodeOutput(ch uint32, offset uint64, payload []byte) []byte {
	buf := make([]byte, outputHeaderLen+len(payload))
	buf[0] = FrameOutput
	binary.BigEndian.PutUint32(buf[1:5], ch)
	binary.BigEndian.PutUint64(buf[5:13], offset)
	copy(buf[outputHeaderLen:], payload)
	return buf
}

// DecodeOutput 解码输出帧；返回的 payload 与 frame 共享底层数组。
func DecodeOutput(frame []byte) (ch uint32, offset uint64, payload []byte, err error) {
	if len(frame) < outputHeaderLen {
		return 0, 0, nil, ErrShortFrame
	}
	if frame[0] != FrameOutput {
		return 0, 0, nil, ErrWrongFrame
	}
	payload = frame[outputHeaderLen:]
	if len(payload) > MaxDataPayload {
		return 0, 0, nil, ErrPayloadTooBig
	}
	return binary.BigEndian.Uint32(frame[1:5]), binary.BigEndian.Uint64(frame[5:13]), payload, nil
}

// EncodeInput 编码 Hub→Agent 的输入帧。
func EncodeInput(ch uint32, payload []byte) []byte {
	buf := make([]byte, inputHeaderLen+len(payload))
	buf[0] = FrameInput
	binary.BigEndian.PutUint32(buf[1:5], ch)
	copy(buf[inputHeaderLen:], payload)
	return buf
}

// DecodeInput 解码输入帧。
func DecodeInput(frame []byte) (ch uint32, payload []byte, err error) {
	if len(frame) < inputHeaderLen {
		return 0, nil, ErrShortFrame
	}
	if frame[0] != FrameInput {
		return 0, nil, ErrWrongFrame
	}
	payload = frame[inputHeaderLen:]
	if len(payload) > MaxInputFrame {
		return 0, nil, ErrPayloadTooBig
	}
	return binary.BigEndian.Uint32(frame[1:5]), payload, nil
}

// EncodeAck 编码 Hub→Agent 的确认帧。
func EncodeAck(ch uint32, offset uint64) []byte {
	buf := make([]byte, ackFrameLen)
	buf[0] = FrameAck
	binary.BigEndian.PutUint32(buf[1:5], ch)
	binary.BigEndian.PutUint64(buf[5:13], offset)
	return buf
}

// DecodeAck 解码确认帧；长度必须精确。
func DecodeAck(frame []byte) (ch uint32, offset uint64, err error) {
	if len(frame) != ackFrameLen {
		return 0, 0, ErrShortFrame
	}
	if frame[0] != FrameAck {
		return 0, 0, ErrWrongFrame
	}
	return binary.BigEndian.Uint32(frame[1:5]), binary.BigEndian.Uint64(frame[5:13]), nil
}

// FrameType 返回二进制帧的类型字节；空帧返回 ErrShortFrame。
func FrameType(frame []byte) (byte, error) {
	if len(frame) == 0 {
		return 0, ErrShortFrame
	}
	return frame[0], nil
}

// EncodeBrowserOutput 编码 Hub→浏览器的输出帧。
func EncodeBrowserOutput(offset uint64, payload []byte) []byte {
	buf := make([]byte, browserOutHdr+len(payload))
	buf[0] = BrowserOutput
	binary.BigEndian.PutUint64(buf[1:9], offset)
	copy(buf[browserOutHdr:], payload)
	return buf
}

// DecodeBrowserOutput 解码浏览器侧输出帧（供测试与 Go 客户端使用）。
func DecodeBrowserOutput(frame []byte) (offset uint64, payload []byte, err error) {
	if len(frame) < browserOutHdr {
		return 0, nil, ErrShortFrame
	}
	if frame[0] != BrowserOutput {
		return 0, nil, ErrWrongFrame
	}
	return binary.BigEndian.Uint64(frame[1:9]), frame[browserOutHdr:], nil
}

// DecodeBrowserInput 解码浏览器→Hub 的输入帧，净荷不得超过 MaxInputFrame。
func DecodeBrowserInput(frame []byte) (payload []byte, err error) {
	if len(frame) < 1 {
		return nil, ErrShortFrame
	}
	if frame[0] != BrowserInput {
		return nil, ErrWrongFrame
	}
	payload = frame[1:]
	if len(payload) > MaxInputFrame {
		return nil, ErrPayloadTooBig
	}
	return payload, nil
}

// EncodeBrowserInput 编码浏览器→Hub 的输入帧（供测试与 Go 客户端使用）。
func EncodeBrowserInput(payload []byte) []byte {
	buf := make([]byte, 1+len(payload))
	buf[0] = BrowserInput
	copy(buf[1:], payload)
	return buf
}
