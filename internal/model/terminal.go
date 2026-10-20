package model

import "time"

// TerminalHost 是已被管理员处理过（启用 / 停用 / 忽略）的终端主机。
//
// 库里只保存 Agent 的**公钥**：机器码由公钥派生，冒充主机需要私钥；
// 服务端数据库泄露不会泄露任何可冒充主机的秘密。
// 待启用（发现列表）的主机只存在于内存，不入库，避免被匿名灌库。
type TerminalHost struct {
	ID           uint   `gorm:"primaryKey" json:"-"`
	MachineCode  string `gorm:"uniqueIndex;size:32;not null" json:"machine_code"`
	PublicKey    string `gorm:"size:64;not null" json:"-"` // base64url(Ed25519 公钥 32 字节)
	Remark       string `gorm:"size:256" json:"remark"`
	Status       string `gorm:"size:16;index;not null" json:"status"` // enabled | disabled | blocked
	Hostname     string `gorm:"size:128" json:"hostname"`
	OS           string `gorm:"size:64" json:"os"`
	Arch         string `gorm:"size:32" json:"arch"`
	AgentVersion string `gorm:"size:32" json:"agent_version"`
	// AgentRev 是 Agent 自报的「Agent 侧代码指纹」（见 termproto.ValidAgentRev），每次握手刷新；空表示旧版本 / 非发布构建。
	// 离线主机的「可升级」也靠它判断，所以要落库。
	AgentRev   string     `gorm:"size:40" json:"agent_rev,omitempty"`
	Privilege  string     `gorm:"size:16" json:"privilege"`
	LastIP     string     `gorm:"size:64" json:"last_ip"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	EnabledAt  *time.Time `json:"enabled_at,omitempty"`
	// RecOff 为 true 表示管理员在这台主机上关闭了终端记录（作为服务端策略下发，只能更严格）。
	RecOff bool `gorm:"not null;default:false" json:"rec_off"`
	// IdleSec 是这台主机上会话的默认生命周期：连续这么多秒没有交互才回收；0 表示永久保持（默认）。
	IdleSec int `gorm:"not null;default:0" json:"idle_sec"`
	// AllowModel 是 G2：管理员在 Web 上（解锁后）允许模型通过 MCP 在这台主机上执行命令。默认关闭。
	AllowModel bool `gorm:"not null;default:false" json:"allow_model"`
	// DefaultForNewModels 决定这台主机对「之后新建的模型授权」的默认态：空串或 deny（默认，不预先授予）/ allow（预先授予）。
	// 它只在两个时刻生效，并且生效方式是「写成令牌主机名单里的明确条目」：这台主机被新增时授予现有的模型令牌，
	// 新建模型令牌时预填入名单。运行期间不做任何继承——判定一台主机能否被某个令牌使用，仍然只看令牌自己的名单。
	DefaultForNewModels string `gorm:"size:8;not null;default:''" json:"default_for_new_models"`
	// ExecMode 是这台主机的模型执行模式：空串或 auto（令牌授权 + 主机授权即放行）/ approve（每次需人工批准）/ readonly（只读）。
	// 令牌自己的模式上限只能比它更严。
	ExecMode string `gorm:"size:16;not null;default:''" json:"exec_mode"`
	// ModelPaused 为 true 时拒绝这台主机上的一切模型运行（并已取消进行中的）；全局暂停是另一个独立的开关。
	ModelPaused bool `gorm:"not null;default:false" json:"model_paused"`

	// Kind 是主机的接入方式：空串或 agent（装了 RelayMesh Agent，由 Agent 与会话守护进程执行）/ ssh（没有 Agent，经 SSH 执行，仅支持模型运行）。
	Kind string `gorm:"size:8;not null;default:''" json:"kind"`
	// 以下 SSH_* 字段只对 Kind=ssh 有意义。连接地址与凭据只来自这里，模型永远只能给出 target。
	SSHAddr        string     `gorm:"column:ssh_addr;size:260;not null;default:''" json:"-"`         // host:port
	SSHUser        string     `gorm:"column:ssh_user;size:64;not null;default:''" json:"-"`          // 登录用户名
	SSHAuth        string     `gorm:"column:ssh_auth;size:8;not null;default:''" json:"-"`           // password | key
	SSHSecret      string     `gorm:"column:ssh_secret;type:text;not null;default:''" json:"-"`      // 密封后的凭据（AES-256-GCM），永不进 JSON、永不明文落库
	SSHHostKeyType string     `gorm:"column:ssh_host_key_type;size:32;not null;default:''" json:"-"` // 固定的主机密钥类型
	SSHHostKey     string     `gorm:"column:ssh_host_key;type:text;not null;default:''" json:"-"`    // 固定的主机公钥（base64）
	SSHFingerprint string     `gorm:"column:ssh_fingerprint;size:64;not null;default:''" json:"-"`   // SHA256:…
	SSHKeyState    string     `gorm:"column:ssh_key_state;size:12;not null;default:''" json:"-"`     // 空 | mismatch（出现过不一致，需管理员重新确认）
	SSHError       string     `gorm:"column:ssh_error;size:200;not null;default:''" json:"-"`        // 最近一次失败的原因（已净化，不含凭据）
	SSHCheckedAt   *time.Time `gorm:"column:ssh_checked_at" json:"-"`                                // 最近一次连接尝试
	SSHOKAt        *time.Time `gorm:"column:ssh_ok_at" json:"-"`                                     // 最近一次成功
	// SSHGateway 非空表示这台 SSH 主机经「网关主机」接入：值是网关（一台开启了 --ssh-gateway 的 Agent 主机）的机器码。
	// 经网关接入的主机：连接由网关上的会话守护进程出站建立（人工终端由它持有会话；模型执行由 Hub 驱动、经网关代理 SSH exec），
	// Hub 不会直连它，也不保存它的登录凭据（SSHSecret 恒为空，凭据只在网关自己的密封凭据库里）。
	// 空串表示由 Hub 直连（仅供模型执行，没有人工终端）。
	SSHGateway string `gorm:"column:ssh_gateway;size:32;not null;default:''" json:"-"`
	// SSHTermOnly 为 true 表示这台经网关接入的主机只能当人工终端用（登记时无法探测出 POSIX 环境：Windows OpenSSH、网络设备、受限 shell），
	// 模型不能在它上面执行。取反的语义让存量行默认可执行。
	SSHTermOnly bool `gorm:"column:ssh_term_only;not null;default:false" json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// 主机的接入方式。
const (
	HostKindAgent = "agent"
	HostKindSSH   = "ssh"
)

// 主机对新建模型授权的默认态（TerminalHost.DefaultForNewModels）。
const (
	HostDefaultDeny  = "deny"
	HostDefaultAllow = "allow"
)

// SSH 主机密钥状态。
const SSHKeyMismatch = "mismatch"

// GrantsNewModelsByDefault 判断这台主机是否预先授予新建的模型令牌。
func (h TerminalHost) GrantsNewModelsByDefault() bool {
	return h.DefaultForNewModels == HostDefaultAllow
}

// IsSSH 判断这是一台经 SSH 接入的主机。
func (h TerminalHost) IsSSH() bool { return h.Kind == HostKindSSH }

// ViaGateway 判断这是一台经网关主机接入的 SSH 主机（Hub 不直连、不保存凭据，由网关出站连接）。
func (h TerminalHost) ViaGateway() bool { return h.Kind == HostKindSSH && h.SSHGateway != "" }

// TerminalSession 是终端会话的**索引**：用来在侧栏里保留「已结束」的会话并指向主机上的记录。
//
// 这里永远不保存终端内容、按键、输入输出（INV-M4）：内容只存在主机本地的记录文件里，
// 由人类按需经 Agent 读取，Hub 不落盘。名称是人类给会话起的标签，经净化后保存。
type TerminalSession struct {
	ID          uint   `gorm:"primaryKey" json:"-"`
	SID         string `gorm:"column:sid;uniqueIndex:ux_terminal_session,priority:2;size:32;not null" json:"sid"`
	MachineCode string `gorm:"uniqueIndex:ux_terminal_session,priority:1;size:32;not null" json:"machine_code"`
	Name        string `gorm:"size:128" json:"name"`
	Shell       string `gorm:"size:128" json:"shell"`
	StartedAt   int64  `json:"started_at"` // 会话创建时间（Agent 时钟的 Unix 秒，仅供展示）

	// EndedAt 是 Hub 观察到会话结束的时间；nil 表示仍在运行。
	EndedAt     *time.Time `gorm:"index" json:"ended_at,omitempty"`
	EndedReason string     `gorm:"size:32" json:"ended_reason"` // exit | killed | gone | host_reboot | holder_stopped | holder_exit | …
	ExitCode    *int       `json:"exit_code,omitempty"`

	// 录制状态与主机上记录的索引（不含内容）。
	RecState  string `gorm:"size:12" json:"rec"`                       // on | paused | error | off | veto | incognito | ""（旧版本 Agent 未报告）
	RecBytes  int64  `json:"rec_bytes"`                                // 主机上保留的记录字节数（最近一次报告）
	RecFirst  int64  `json:"rec_first"`                                // 最早保留内容的时间（Unix 秒）
	RecLast   int64  `json:"rec_last"`                                 // 最近写入的时间（Unix 秒）
	RecGap    bool   `json:"rec_gap"`                                  // 记录不完整
	RecPurged bool   `gorm:"not null;default:false" json:"rec_purged"` // 主机报告记录已不存在（过期或被清理）

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TerminalArchive 记录 ExecMesh 侧栏里哪些条目被归档（D-200）。
//
// 归档只决定侧栏默认显示不显示：不结束会话、不影响记录与审计，也不改变模型线程里的任何内容。
// ItemKey 与前端侧栏条目的 key 一致：人工会话是「机器码:会话 ID」，模型线程是「r:线程键」。
// 单独存一张表而不是挂在 TerminalSession 上，是因为模型线程没有自己的行，两类条目这样可以走同一条路径。
type TerminalArchive struct {
	ItemKey    string    `gorm:"column:item_key;primaryKey;size:160" json:"key"`
	ArchivedAt time.Time `gorm:"index" json:"archived_at"`
}

// TerminalLostDismissal 记录管理员在主机页「忽略」了哪些「会话已不存在」提示（第六批 #28）。
//
// 提示的来源是主机上会话守护进程的上报（terminal.HolderView.Lost）：守护进程每次与 Hub 建立连接都会把
// 它记着的丢失会话再报一遍，所以「删掉这条提示」必须由 Hub 记住，否则下一次重连又会冒出来。
// 忽略只是「不再提醒」：不删除会话记录、审计与历史入口，也不要求主机在线、不要求 Agent 升级（协议不变）。
// 过期的忽略记录随时间清理（见 store.DismissTerminalLost），主机被删除时一并清除。
type TerminalLostDismissal struct {
	MachineCode string    `gorm:"column:machine_code;primaryKey;size:32" json:"machine_code"`
	SID         string    `gorm:"column:sid;primaryKey;size:64" json:"sid"`
	DismissedAt time.Time `gorm:"index" json:"dismissed_at"`
}

// TerminalAuditLog 是终端相关的安全审计记录。
//
// 永不记录按键、终端输出、票据、解锁令牌与 Agent 私钥；
// Agent 自报字段必须先净化再写入 Detail。
type TerminalAuditLog struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CreatedAt   time.Time `gorm:"index" json:"created_at"`
	Event       string    `gorm:"size:48;index;not null" json:"event"`
	Result      string    `gorm:"size:8" json:"result"` // ok | fail | deny
	Actor       string    `gorm:"size:64" json:"actor"`
	IP          string    `gorm:"size:64" json:"ip"`
	MachineCode string    `gorm:"size:32;index" json:"machine_code"`
	SessionID   string    `gorm:"size:40" json:"session_id"`
	Detail      string    `gorm:"size:512" json:"detail"`
	// ActorKind 标明操作者类型：human（Web 登录主体）/ model（MCP 令牌）/ system；空表示旧记录（均为 human 或 system）。
	ActorKind string `gorm:"size:8" json:"actor_kind"`
	// RunID 在审计事件与某次模型运行有关时给出，便于从运行跳到审计。
	RunID string `gorm:"size:32;index" json:"run_id"`
}

// 模型运行的状态。
const (
	RunAwaitingApproval = "awaiting_approval"
	RunRunning          = "running"
	RunSucceeded        = "succeeded"
	RunFailed           = "failed" // 命令自行结束且退出码非 0，或被信号终止
	RunTimedOut         = "timeout"
	RunCancelled        = "cancelled"
	RunRejected         = "rejected" // 需批准模式下被人工拒绝或批准超时
	RunDenied           = "denied"   // 被闸门拒绝（含本机一票否决）
	RunLost             = "lost"     // 主机重启 / 守护进程消失后无法确认结果
)

// IsFinalRunState 判断运行状态是否已终结。
func IsFinalRunState(state string) bool {
	switch state {
	case RunSucceeded, RunFailed, RunTimedOut, RunCancelled, RunRejected, RunDenied, RunLost:
		return true
	}
	return false
}

// TerminalRun 是一次模型运行的**元数据**：谁、在哪台主机、做了什么、结果如何，外加有界的预览。
//
// 这里永远不保存完整输出（INV-M4）：完整输出只存在主机本机，由人类或持有令牌的模型按需经 Agent 读取。
// 命令文本保存的是脱敏副本（常见密钥模式已被替换）；预览同样脱敏并去除控制序列。
type TerminalRun struct {
	ID    uint   `gorm:"primaryKey" json:"-"`
	RunID string `gorm:"column:run_id;uniqueIndex;size:32;not null" json:"run_id"`

	// ThreadKey 是线程键的短哈希：ExecMesh 档为 (凭据, 目标主机, thread)，混合令牌且带 workflow_id 时为 (workflow_id, 目标主机)。
	ThreadKey string `gorm:"size:32;index;not null" json:"thread_key"`
	Backend   string `gorm:"size:8" json:"backend"`    // agent
	ActorKind string `gorm:"size:8" json:"actor_kind"` // model | human | system

	CredentialID   uint   `gorm:"index" json:"credential_id"`
	CredentialName string `gorm:"size:128" json:"credential_name"`
	ClientName     string `gorm:"size:96" json:"client_name"` // 客户端自报（User-Agent 摘要），只作展示
	RemoteIP       string `gorm:"size:64" json:"remote_ip"`
	OriginHost     string `gorm:"size:128" json:"origin_host"`

	WorkflowID       string `gorm:"size:128;index" json:"workflow_id"`
	Thread           string `gorm:"size:64" json:"thread"`
	ProjectDirectory string `gorm:"size:256" json:"project_directory"`
	Description      string `gorm:"size:256" json:"description"`

	Target        string `gorm:"size:32;index;not null" json:"target"` // 机器码
	Cwd           string `gorm:"size:512" json:"cwd"`
	Shell         string `gorm:"size:16" json:"shell"`
	Command       string `gorm:"size:4096" json:"command"` // 脱敏副本
	CommandSHA256 string `gorm:"size:64" json:"command_sha256"`
	Tools         string `gorm:"size:256" json:"tools"` // 命令名清单（逗号分隔），仅展示

	State      string     `gorm:"size:20;index;not null" json:"state"`
	DenyReason string     `gorm:"size:32" json:"deny_reason"` // denied / rejected 时的原因码
	ExitCode   *int       `json:"exit_code,omitempty"`
	Signal     string     `gorm:"size:16" json:"signal"`
	PolicyMode string     `gorm:"size:8" json:"policy_mode"` // 生效的执行模式：auto | approve | readonly
	ApprovedBy string     `gorm:"size:64" json:"approved_by"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `gorm:"index" json:"ended_at,omitempty"`
	DurationMS int64      `json:"duration_ms"`
	TimeoutSec int        `json:"timeout_sec"`

	StdoutBytes   int64 `json:"stdout_bytes"`
	StderrBytes   int64 `json:"stderr_bytes"`
	Truncated     bool  `json:"truncated"`
	MiddleDropped int64 `json:"middle_dropped_bytes"`

	PreviewHead string `gorm:"type:text" json:"preview_head"`
	PreviewTail string `gorm:"type:text" json:"preview_tail"`

	IdempotencyKey string `gorm:"size:96;index" json:"-"`

	CreatedAt time.Time `gorm:"index" json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
