package holder

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 守护进程与控制端（Agent）之间的本地协议。
//
// 数据面完全复用 Agent↔Hub 的帧（open / attach / detach / resize / kill / rename / policy / sessions、
// 以及二进制的输出 / 输入 / 确认），Agent 只做近乎透明的转发；这里只定义本地独有的几种帧：
// 握手（ipc_open / ipc_hello）、查询（ipc_list / ipc_status）、通道清场（ipc_detach_all）、
// 关停（ipc_shutdown）与告别（ipc_bye）。
const (
	// IPCVersion 是本地协议的主版本；不一致时双方都拒绝并给出明确原因。
	IPCVersion = 1

	TIPCOpen        = "ipc_open"
	TIPCHello       = "ipc_hello"
	TIPCList        = "ipc_list"
	TIPCDetachAll   = "ipc_detach_all"
	TIPCShutdown    = "ipc_shutdown"
	TIPCStatus      = "ipc_status"
	TIPCStatusReply = "ipc_status_reply"
	TIPCBye         = "ipc_bye"
)

// 连接角色。
const (
	// RoleController 是唯一的控制端（Agent）：持有数据通道、接收会话清单推送。同一时刻只有一个，新来的取代旧的。
	RoleController = "controller"
	// RoleAdmin 是只做查询与关停的管理连接（命令行用），不影响控制端。
	RoleAdmin = "admin"
)

// 能力位：双方各自声明，取交集使用；缺失的能力降级而不是报错（INV-H2）。
const (
	CapPTY    = "pty"    // 持久伪终端会话
	CapPolicy = "policy" // 策略下发与本地持久化
	CapDrain  = "drain"  // 排空式关停
	CapLost   = "lost"   // 上报「重启后丢失的会话」及原因
	// CapCharset：新建会话时把字符类别钉在 LC_CTYPE 上（见 locale_unix.go 的 ensureUTF8），登录脚本把 LANG 改回 C 也不影响中文输入。
	// 会话是守护进程创建的，而升级只替换 Agent、不会重启守护进程，所以 Hub 靠这个能力位告诉用户「这台主机的守护进程还没带上这项修复」。
	CapCharset = "charset"
	CapExec    = "exec"                 // 模型执行（非交互命令运行，输出保存在本机）
	CapSSH     = termproto.HolderCapSSH // SSH 网关：本机开启了 --ssh-gateway，可替 Hub 连接 SSH 目标
	// CapSSHStore：网关的凭据库可用，目标的登录凭据可以保存在这台网关本机（Hub 不保管）。没有它，Hub 不会把主机登记到这台网关上。
	CapSSHStore = termproto.HolderCapSSHStore
	// CapRecLines：记录引擎支持按行数限高（见 record.Policy.SessionLines）。
	CapRecLines = termproto.HolderCapRecLines
)

// 关停方式。
const (
	// ShutdownNow 立即终止全部会话并退出。
	ShutdownNow = "now"
	// ShutdownDrain 不再接受新会话与新命令，等运行中的会话和模型命令都自然结束后退出。
	ShutdownDrain = "drain"
)

// 会话丢失原因：界面据此给出人能看懂的说明，而不是一个无解释的「会话没了」。
const (
	LostHostReboot    = "host_reboot"    // 主机重启（启动标识变了）
	LostHolderExit    = "holder_exit"    // 守护进程在会话存续期间退出（崩溃或被杀）
	LostHolderStopped = "holder_stopped" // 守护进程被有意停止（停止单元 / 关停命令）
	LostUnknown       = "unknown"
)

// IPCOpen 是客户端连上之后的第一帧。
type IPCOpen struct {
	T      string   `json:"t"`
	V      int      `json:"v"`
	Role   string   `json:"role"`
	Client string   `json:"client,omitempty"` // 客户端（Agent / 命令行）的版本
	Caps   []string `json:"caps,omitempty"`
}

// Lost 描述一个已经不存在的会话以及它消失的原因。
type Lost struct {
	SID     string `json:"sid"`
	Name    string `json:"name,omitempty"`
	Shell   string `json:"shell,omitempty"`
	Cwd     string `json:"cwd,omitempty"` // 尽力而为：会话消失前最后一次记录到的工作目录
	Created int64  `json:"created,omitempty"`
	Reason  string `json:"reason"`
	At      int64  `json:"at,omitempty"` // 守护进程发现它已消失的时间（Unix 秒）
}

// IPCHello 是守护进程对 ipc_open 的应答，携带版本、启动标识与能力。
type IPCHello struct {
	T          string   `json:"t"`
	V          int      `json:"v"`
	Holder     string   `json:"holder"`            // 守护进程自己的版本
	Rev        string   `json:"rev,omitempty"`     // 守护进程二进制的 Agent 侧代码指纹（旧版本没有）
	BootID     string   `json:"boot_id,omitempty"` // 主机启动标识（主机重启后变化）
	PID        int      `json:"pid"`               // 守护进程的进程号
	Started    int64    `json:"started"`           // 守护进程启动时间（Unix 秒）
	Persistent bool     `json:"persistent"`        // 会话是否能挺过 Agent 的重启（外置守护进程为 true，内嵌模式为 false）
	Caps       []string `json:"caps,omitempty"`
	Lost       []Lost   `json:"lost,omitempty"`
	// DenyExec 为 true 表示本机用 --deny-model-exec 一票否决了模型执行；Exec 描述运行命令时使用的 shell 与目录约定。
	DenyExec bool                `json:"deny_exec,omitempty"`
	Exec     *termproto.ExecInfo `json:"exec,omitempty"`
}

// IPCShutdown 请求守护进程关停。
type IPCShutdown struct {
	T    string `json:"t"`
	Mode string `json:"mode"`
}

// IPCStatusReply 是 ipc_status 的应答。
type IPCStatusReply struct {
	T          string                  `json:"t"`
	Hello      IPCHello                `json:"hello"`
	Sessions   []termproto.SessionInfo `json:"sessions"`
	Controller bool                    `json:"controller"` // 当前是否有控制端在连着
	Draining   bool                    `json:"draining,omitempty"`
	// Runs 是正在运行的模型命令数（terminal_exec 发起，由 ExecManager 持有，不在 Sessions 里）。
	// 用指针区分「0 条」与「这个守护进程太旧、没有这个字段」：旧版本的排空也不会等模型命令，命令行要据此提醒。
	Runs *int `json:"runs,omitempty"`
}

// IPCBye 在守护进程主动结束一条连接前给出原因。
type IPCBye struct {
	T      string `json:"t"`
	Reason string `json:"reason"`
}

// 本地协议的 bye 原因。
const (
	ByeSuperseded = "superseded" // 另一个控制端取代了它
	ByeVersion    = "version"    // 主版本不兼容
	ByeShutdown   = "shutdown"   // 守护进程正在关停
	ByeBadRequest = "bad_request"
)

// DefaultCaps 是本实现支持的全部能力位。
func DefaultCaps() []string {
	return []string{CapPTY, CapPolicy, CapDrain, CapLost, CapCharset, CapRecLines}
}

// HasCap 判断能力列表里是否有 c。
func HasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// ---------- 客户端：Dial 之后的握手 ----------

// Client 是一条完成握手的本地连接（Agent 或命令行用）。
type Client struct {
	Conn  Conn
	Hello IPCHello
}

// Handshake 在一条刚建立的 Conn 上完成 ipc_open → ipc_hello。失败时关闭连接。
// 守护进程可能在握手阶段回复 ipc_bye（例如版本不兼容），其原因会体现在错误里。
func Handshake(ctx context.Context, conn Conn, role, clientVersion string) (*Client, error) {
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	raw, err := json.Marshal(IPCOpen{T: TIPCOpen, V: IPCVersion, Role: role, Client: clientVersion, Caps: DefaultCaps()})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Write(hctx, MsgText, raw); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("holder handshake: %w", err)
	}
	typ, data, err := conn.Read(hctx)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("holder handshake: %w", err)
	}
	if typ != MsgText {
		_ = conn.Close()
		return nil, fmt.Errorf("holder handshake: unexpected frame")
	}
	t, _ := termproto.ParseType(data)
	switch t {
	case TIPCHello:
		var h IPCHello
		if err := json.Unmarshal(data, &h); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("holder handshake: bad hello: %w", err)
		}
		if h.V != IPCVersion {
			_ = conn.Close()
			return nil, &ByeError{Reason: ByeVersion}
		}
		return &Client{Conn: conn, Hello: h}, nil
	case TIPCBye:
		var b IPCBye
		_ = json.Unmarshal(data, &b)
		_ = conn.Close()
		return nil, &ByeError{Reason: b.Reason}
	}
	_ = conn.Close()
	return nil, fmt.Errorf("holder handshake: unexpected frame %q", t)
}

// ByeError 表示守护进程在握手或服务期间明确拒绝 / 结束了连接。
type ByeError struct{ Reason string }

func (e *ByeError) Error() string { return "holder closed the connection: " + e.Reason }

// SendControl 向守护进程发送一条无参数的本地控制帧（ipc_list / ipc_detach_all / ipc_status）。
func SendControl(ctx context.Context, conn Conn, t string) error {
	raw, err := json.Marshal(termproto.Envelope{T: t})
	if err != nil {
		return err
	}
	return conn.Write(ctx, MsgText, raw)
}

// Status 向守护进程查询状态与会话清单（管理端 / 控制端都可以用）。
func Status(ctx context.Context, c *Client) (IPCStatusReply, error) {
	var reply IPCStatusReply
	if err := SendControl(ctx, c.Conn, TIPCStatus); err != nil {
		return reply, err
	}
	for {
		typ, data, err := c.Conn.Read(ctx)
		if err != nil {
			return reply, err
		}
		if typ != MsgText {
			continue
		}
		t, _ := termproto.ParseType(data)
		switch t {
		case TIPCStatusReply:
			if err := json.Unmarshal(data, &reply); err != nil {
				return reply, fmt.Errorf("bad status reply: %w", err)
			}
			return reply, nil
		case TIPCBye:
			var b IPCBye
			_ = json.Unmarshal(data, &b)
			return reply, &ByeError{Reason: b.Reason}
		}
		// 其余帧（控制端才会收到的会话清单推送等）与状态查询无关，忽略。
	}
}

// SendShutdown 请求守护进程关停。
func SendShutdown(ctx context.Context, conn Conn, mode string) error {
	raw, err := json.Marshal(IPCShutdown{T: TIPCShutdown, Mode: mode})
	if err != nil {
		return err
	}
	return conn.Write(ctx, MsgText, raw)
}

// DescribeLost 把丢失原因翻译成给人看的短句。
func DescribeLost(reason string) string {
	switch reason {
	case LostHostReboot:
		return "主机重启"
	case LostHolderExit:
		return "守护进程意外退出"
	case LostHolderStopped:
		return "守护进程被停止"
	}
	return "原因未知"
}
