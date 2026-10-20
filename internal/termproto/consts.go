// Package termproto 定义终端功能中「Hub（服务端）」与「Agent（主机端）」共用的线协议、机器码派生与字段净化。
//
// 它只依赖标准库，刻意不引用项目内任何其他包：协议是两端的契约，改动必须同时被两端的测试看见。
// 金标准向量（identity_test.go）固化了机器码与签名的派生方式，任何不兼容的改动都会让它们失败。
package termproto

import "time"

// 协议标识与版本。
const (
	// SubprotocolAgent 是 Agent↔Hub WebSocket 的子协议名（版本化，便于日后其他实现接入）。
	SubprotocolAgent = "relaymesh.agent.v1"
	// SubprotocolBrowser 是浏览器↔Hub 终端 WebSocket 的子协议名。
	SubprotocolBrowser = "relaymesh.term.v1"
	// TicketSubprotocolPrefix 用于在 Sec-WebSocket-Protocol 中携带一次性票据：
	// 浏览器 WebSocket 无法自定义请求头，放进 URL 会进入访问日志，因此放进子协议头（服务端在升级前读取、不记录）。
	TicketSubprotocolPrefix = "tk."
	// ProtocolVersion 是 challenge / hello 中的协议版本号。
	ProtocolVersion = 1
)

// WebSocket 应用层关闭码（4000~4999 为应用自定义范围）。
const (
	CloseInvalidHandshake = 4401 // 握手或签名无效（多为版本不兼容），Agent 60 秒后重试
	CloseGateClosed       = 4403 // 终端总闸关闭（含免密私有直连模式）
	CloseNotFound         = 4404 // 目标主机不在线或会话不存在（浏览器侧）
	CloseUnknownMachine   = 4405 // 未知机器码且发现模式关闭
	CloseDisabled         = 4409 // 主机被停用 / 忽略
	CloseSessionEnded     = 4410 // 会话已结束（浏览器侧）
	CloseStalled          = 4412 // 附着方长时间不确认，被服务端断开（浏览器侧）
	CloseSuperseded       = 4413 // 同一机器码的新连接取代了旧连接
	CloseLagging          = 4414 // 附着方持续跟不上输出（会话有记录时）：断开并要求重新附着，跳到最新输出（浏览器侧）
	CloseRateLimited      = 4429 // 限流或名额已满
)

// 预认证与握手限额。
const (
	// MaxPreAuthFrame 是 online 之前（未启用或未完成握手）单帧的字节上限。
	MaxPreAuthFrame = 8 << 10
	// HandshakeTimeout 是 Hello 必须完成的期限。
	HandshakeTimeout = 10 * time.Second
	// MaxPreAuthConns / MaxPreAuthPerIP 是预认证阶段并发连接的全局 / 单 IP 上限。
	MaxPreAuthConns = 16
	MaxPreAuthPerIP = 4
	// MaxPending 是待启用列表的条数上限；PendingOfflineTTL 是离线条目保留时长。
	MaxPending        = 32
	PendingOfflineTTL = 10 * time.Minute
)

// 已认证阶段的帧与会话限额。
const (
	// MaxControlFrame 是 online 之后 JSON 控制帧的字节上限（hello 携带会话清单，需要比预认证更宽）。
	MaxControlFrame = 64 << 10
	// MaxDataPayload 是单个输出 / 输入二进制帧的净荷上限。
	MaxDataPayload = 32 << 10
	// MaxInputFrame 是浏览器单次输入帧上限，粘贴大文本由前端分块。
	MaxInputFrame = 16 << 10
	// AttachWindow 是附着后每通道在途（已发送未确认）字节窗口。
	AttachWindow = 256 << 10
	// StallTimeout 是窗口耗尽后附着方仍不确认则断开该附着的期限。
	StallTimeout = 10 * time.Second
	// LagUnacked / LagClear 是「附着方跟不上输出」的判据（迟滞）：在途未确认量达到 LagUnacked 算饱和，降到 LagClear 以下才算恢复。
	// 链路带宽小于程序的输出速度时，窗口会一直停在接近满的位置；普通输出、快链路下窗口很快排空，不会触发。
	LagUnacked = AttachWindow * 3 / 4
	LagClear   = AttachWindow / 2
	// LagWarnAfter 是持续饱和多久后告诉浏览器「正在追赶」（浏览器据此盖上遮罩）；LagEjectAfter 是持续多久后断开并要求重新附着。
	// 只对会话记录正常的附着生效：被跳过的中间输出得能从记录里读到，没有记录的会话保持无损背压。
	// 提醒在输出到来时第一时间发；撤回与断开每 250 毫秒检查一次（见 attach.go 的 attachTick），所以断开最多晚 250 毫秒。
	LagWarnAfter  = 500 * time.Millisecond
	LagEjectAfter = 1200 * time.Millisecond
	// LagInputGrace：浏览器刚送来键盘输入的这段时间里不断开——用户多半正在处理眼前的输出（最常见的是用 Ctrl+C 打断刷屏），
	// 断开重连的几秒里这些按键赶不上。提醒（遮罩）照常，只推迟断开；输入一停、过了这个时间还跟不上，才断开。
	LagInputGrace = 2 * time.Second
	// RingSize 是每会话回放缓冲的默认大小。
	RingSize = 1 << 20
	// CompactRingSize 是「精简会话」（会话守护预设启动的 Cursor CLI 会话）的回放缓冲大小：32 KiB 约为 100 行左右的可见输出。
	// CLI 自己的对话日志在 Cursor 里保存，终端里留一大截历史没有意义，还会让缓冲与回放持续膨胀（追加 #80）。
	CompactRingSize = 32 << 10
	// CompactMaxCols 是精简会话的终端宽度上限：再宽只会让每行更长、同样的缓冲装得更少。
	CompactMaxCols = 120
	// MaxTailLines 是浏览器请求「只回放末尾若干行」时接受的行数上限（超过按此截断）。
	MaxTailLines = 5000
	// TailMinBytes 是启用「只回放末尾若干行」的缓冲量门槛：缓冲里不足这么多字节的会话本来就回放得很快，
	// 整体回放还能保住完整的终端状态，没必要裁剪。
	TailMinBytes = 256 << 10
	// MaxSessionsPerHost 是每主机会话数上限。
	MaxSessionsPerHost = 16
	// DetachedTTL 是旧版本固定的「断开 7 天回收」。自 H1 起生命周期由 Policy.IdleSec 与会话设置决定（默认永久保持），
	// 这个常量只作为兼容旧 Hub 的参考值保留，不再是默认行为。
	DetachedTTL = 7 * 24 * time.Hour
	// MaxSessionNameRunes 是会话名上限；MaxRemarkRunes 是主机备注上限。
	MaxSessionNameRunes = 64
	MaxRemarkRunes      = 64
	// MaxCwdRunes 是「在原目录重建会话」所带目录路径的上限。
	MaxCwdRunes = 1024
)

// 空闲回收（生命周期）的取值范围，单位秒。0 表示永久保持；其余取值必须落在 [MinIdleSec, MaxIdleSec] 内。
const (
	MinIdleSec = 60
	MaxIdleSec = 400 * 24 * 3600
)

// ClampIdle 把空闲回收秒数规整到合法范围：不大于 0 的一律视为永久保持（0），过小抬到 MinIdleSec，过大压到 MaxIdleSec。
// 守护进程用它兜住任何来源（策略、Open、SetLifecycle）的取值，避免一个异常数字让会话被立刻回收。
func ClampIdle(sec int) int {
	switch {
	case sec <= 0:
		return 0
	case sec < MinIdleSec:
		return MinIdleSec
	case sec > MaxIdleSec:
		return MaxIdleSec
	}
	return sec
}

// 心跳：WS ping 间隔与「无 pong 判定断链」的期限（两端对称）。
const (
	PingInterval = 15 * time.Second
	PongTimeout  = 45 * time.Second
)

// 自报字段长度上限（rune 数）。
const (
	MaxHostnameRunes = 64
	MaxOSRunes       = 64
	MaxArchRunes     = 32
	MaxVersionRunes  = 32
)

// 主机状态（持久化在 terminal_hosts.status）。
const (
	HostStatusEnabled  = "enabled"
	HostStatusDisabled = "disabled"
	HostStatusBlocked  = "blocked"
)

// Agent 特权等级（hello.meta.priv，自报，仅用于界面徽标与提示）。
const (
	PrivRoot  = "root"
	PrivAdmin = "admin"
	PrivUser  = "user"
)

// Agent 形态（hello.meta.kind，自报）：temporary 表示仅内存的临时身份，服务端不持久化其启用状态。
const (
	KindPersistent = "persistent"
	KindTemporary  = "temporary"
)
