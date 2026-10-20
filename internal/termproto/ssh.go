package termproto

import (
	"fmt"
	"strings"
)

// SSH 网关（人工 SSH 终端）的线协议。
//
// 网关就是一个本机用 --ssh-gateway 显式开启了该能力的普通 Agent：Hub 在它上面新建会话时，
// 在 open 帧里附带 SSH 目标（地址、用户与已固定的主机公钥），由网关上的会话守护进程去连接目标并申请伪终端。
// 登记 / 测试 / 重新确认指纹 / 代为执行脚本这类一次性动作走 ssh_op → ssh_result。
//
// 凭据保存在网关自己的存储里（见 internal/agent/sshcred），Hub 不保存：只有登记 / 修改凭据时，凭据才会在
// 「管理员 → Hub → 网关」的 check 帧里经过一次；网关**真实登录成功之后**才把它写进自己的密封库（键是 SSH 主机的机器码）。
// 之后的 open / exec 帧只带 Stored=true，网关按 ID 取用，凭据不再离开网关，也不进会话信息、记录、日志与审计。
// SSHTarget / SSHOp 的 String / GoString 都做了脱敏，防止它们被 %v 误打进日志。

// 帧类型。
const (
	TSSHOp     = "ssh_op"     // Hub → 网关：一次性的 SSH 操作（探测 / 检查 / 代为执行 / 凭据维护）
	TSSHResult = "ssh_result" // 网关 → Hub：ssh_op 的结果
)

// HolderCapSSH 是会话守护进程在 holder 帧 caps 里声明的能力：本机开启了 SSH 网关。
const HolderCapSSH = "ssh"

// HolderCapSSHStore 声明网关的凭据库可用，并支持 stored 目标与 exec / cred_del / cred_sync：
// 缺少它的网关（旧版本，或凭据库打不开）Hub 一律视为未就绪，不会向它发送任何 SSH 目标。
const HolderCapSSHStore = "ssh_store"

// HolderCapRecLines 声明记录引擎支持「按行数限高」（Policy.RecMaxLines / --record-session-lines）。
// 升级只替换 Agent、不会重启会话守护进程，而记录引擎在守护进程里，所以 Hub 靠这个能力位判断一台主机上的设置是否已经生效。
const HolderCapRecLines = "rec_lines"

// ssh_op 的操作类型。
const (
	SSHOpProbe    = "probe"     // 只做密钥交换取回主机公钥，不发送任何认证信息
	SSHOpCheck    = "check"     // 用固定的主机公钥与凭据真实登录一次，并读取远端环境信息；persist 时成功后保存凭据
	SSHOpExec     = "exec"      // 在目标上运行一段脚本并回传输出（Hub 的 SSH 执行层经网关使用，目标须 stored）
	SSHOpCredDel  = "cred_del"  // 删除网关上保存的某台 SSH 主机的凭据
	SSHOpCredSync = "cred_sync" // 只保留名单内的凭据（回收其余），并回报仍持有哪些
)

// ssh_op / ssh_result 的尺寸约束。Hub ↔ Agent 的控制帧不超过 MaxControlFrame（64 KiB），超限的帧会让对端关闭连接；
// 输出字节经 JSON 字节数组编码（base64，膨胀 4/3），所以一次 exec 应答里**两路输出合计**只放 40 KiB（编码后约 54 KiB）。
const (
	MaxSSHExecOut = 40 << 10 // 一次 exec 应答里 stdout 与 stderr 合计的上限
	MaxSSHKeep    = 512      // cred_sync 名单 / 回报的条数上限（远多于 SSH 主机数量上限）
)

// 稳定的错误码（ssh_result.code 与 open_failed.code）。
const (
	SSHCodeDisabled    = "gateway_disabled"    // 这台主机没有开启 SSH 网关能力
	SSHCodeDenied      = "target_denied"       // 目标不在网关本机的允许名单内
	SSHCodeBadRequest  = "bad_request"         // 目标信息无效
	SSHCodeUnreachable = "unreachable"         // 连不上目标 / 连接中断 / 超时
	SSHCodeAuth        = "auth_failed"         // 认证失败
	SSHCodeHostKey     = "host_key_mismatch"   // 主机密钥与已固定的不一致
	SSHCodeNoHostKey   = "no_host_key"         // 没有固定的主机密钥，拒绝连接
	SSHCodePassphrase  = "passphrase_required" // 私钥已加密，需要口令
	SSHCodeUnsupported = "unsupported"         // 目标不是受支持的 POSIX 环境
	SSHCodeBusy        = "busy"                // 网关正在处理太多 SSH 操作
	SSHCodeNoCred      = "no_credential"       // 网关上没有这台 SSH 主机的凭据（换了数据卷 / 重建了容器 / 还没登记）
	SSHCodeExecDenied  = "exec_denied"         // 网关本机用 --deny-model-exec 禁止了模型执行
	SSHCodeStore       = "cred_store"          // 网关无法读写自己的凭据库
	SSHCodeInternal    = "internal"
)

// SSHTarget 描述网关要连接的目标。只有 check 帧会携带凭据（Hub → 网关方向），且永远不应被打印。
type SSHTarget struct {
	// ID 是 Hub 侧这台 SSH 主机的机器码：标注会话归属，同时是网关凭据库里这份凭据的键（不是秘密）。
	ID   string `json:"id,omitempty"`
	Addr string `json:"addr"`
	User string `json:"user"`
	// Auth 为 password 或 key；probe 不需要认证信息。
	Auth string `json:"auth,omitempty"`
	// Stored 为 true 表示凭据已保存在网关本机：帧里不带任何凭据，网关按 ID 取用（open / exec / check 复测都这样用）。
	Stored     bool   `json:"stored,omitempty"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	// HostKey 是已固定的主机公钥（标准 base64 的 SSH 公钥线格式）；probe 不需要，其余操作必须有。
	HostKey string `json:"host_key,omitempty"`
}

// String 只描述连到哪里，不含任何凭据（%v / %s / %+v 都会走到这里）。
func (t SSHTarget) String() string { return "ssh://" + t.User + "@" + t.Addr }

// GoString 同样脱敏（%#v）。
func (t SSHTarget) GoString() string { return t.String() }

// SSHOp 是 Hub 向网关发起的一次性 SSH 操作。
type SSHOp struct {
	T      string    `json:"t"`
	Req    string    `json:"req"`
	Op     string    `json:"op"`
	Target SSHTarget `json:"target"`

	// Persist（check）：真实登录成功之后，把这份凭据保存到网关本机（键是 Target.ID）；失败不留任何痕迹。
	Persist bool `json:"persist,omitempty"`

	// exec：交给远端的命令行与标准输入（Hub 生成的任务脚本，命令文本已在脚本里 base64，不会拼进命令行），
	// 以及两路输出合计的上限（<=0 取 MaxSSHExecOut，且不会超过它）。
	Cmd    string `json:"cmd,omitempty"`
	Stdin  []byte `json:"stdin,omitempty"`
	MaxOut int    `json:"max_out,omitempty"`

	// cred_sync：应当保留的 SSH 主机机器码；网关删除名单之外的凭据。
	Keep []string `json:"keep,omitempty"`
}

// String 脱敏描述（不含命令、标准输入与凭据）。
func (o SSHOp) String() string { return "ssh_op{" + o.Op + " " + o.Target.String() + "}" }

// GoString 脱敏描述（%#v）。
func (o SSHOp) GoString() string { return o.String() }

// SSHResult 是 ssh_op 的结果。全部字段来自对端：Hub 使用前必须经过 Sanitized。
type SSHResult struct {
	T    string `json:"t"`
	Req  string `json:"req"`
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`

	// probe：规整后的地址与探测到的主机公钥。
	Addr        string `json:"addr,omitempty"`
	KeyType     string `json:"key_type,omitempty"`
	HostKey     string `json:"host_key,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`

	// check：远端环境信息与耗时。Posix 表示环境信息探测成功——目标是受支持的 POSIX 环境，可以被模型执行；
	// 探测失败（Windows OpenSSH、网络设备、受限 shell）仍然可以开人工终端。
	Host      string `json:"host,omitempty"`
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
	User      string `json:"user,omitempty"`
	Home      string `json:"home,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Posix     bool   `json:"posix,omitempty"`

	// exec：脚本的标准输出 / 标准错误与退出码（两路合计不超过 MaxSSHExecOut）。
	Stdout []byte `json:"stdout,omitempty"`
	Stderr []byte `json:"stderr,omitempty"`
	Exit   int    `json:"exit,omitempty"`

	// cred_sync：网关此刻仍持有凭据的主机码（名单内的部分）与被回收的凭据数。
	Have    []string `json:"have,omitempty"`
	Dropped int      `json:"dropped,omitempty"`
}

// 结果字段的长度上限。
const (
	maxSSHMsgRunes   = 200
	maxSSHKeyRunes   = 4096
	maxSSHFieldRunes = 128
)

// Sanitized 返回净化并限长后的副本；未知的错误码一律归为 internal。
func (r SSHResult) Sanitized() SSHResult {
	out := SSHResult{
		T:   TSSHResult,
		Req: SanitizeText(r.Req, 32),
		OK:  r.OK,
	}
	if !r.OK {
		out.Code = SanitizeSSHCode(r.Code)
		out.Msg = SanitizeText(r.Msg, maxSSHMsgRunes)
		return out
	}
	out.Addr = SanitizeText(r.Addr, 260)
	out.KeyType = SanitizeText(r.KeyType, 40)
	out.Fingerprint = SanitizeText(r.Fingerprint, 80)
	if k := strings.TrimSpace(r.HostKey); len(k) <= maxSSHKeyRunes && isBase64Text(k) {
		out.HostKey = k
	}
	out.Host = SanitizeText(r.Host, maxSSHFieldRunes)
	out.OS = SanitizeText(r.OS, 64)
	out.Arch = SanitizeText(r.Arch, 32)
	out.User = SanitizeText(r.User, 64)
	out.Home = SanitizeText(r.Home, 256)
	if r.LatencyMS > 0 {
		out.LatencyMS = r.LatencyMS
	}
	out.Posix = r.Posix

	// exec 的输出是不透明的字节（Hub 的任务脚本自己约定格式并解析），这里只约束大小：
	// 超限说明对端违反了协议，整个结果按失败处理，绝不截断后继续使用。
	if len(r.Stdout)+len(r.Stderr) > MaxSSHExecOut {
		return SSHResult{T: TSSHResult, Req: out.Req, Code: SSHCodeInternal, Msg: "网关返回的输出超过了协议上限"}
	}
	out.Stdout, out.Stderr, out.Exit = r.Stdout, r.Stderr, r.Exit

	// cred_sync 的回报：只收合法的机器码，去重，限条数——对端不能借它往 Hub 里塞别的东西。
	if len(r.Have) > 0 {
		seen := make(map[string]struct{}, len(r.Have))
		for _, c := range r.Have {
			if len(out.Have) >= MaxSSHKeep {
				break
			}
			if !ValidMachineCode(c) {
				continue
			}
			if _, dup := seen[c]; dup {
				continue
			}
			seen[c] = struct{}{}
			out.Have = append(out.Have, c)
		}
	}
	if r.Dropped > 0 {
		out.Dropped = r.Dropped
	}
	return out
}

// SanitizeSSHCode 把来自对端的错误码规整成已知的稳定取值。
func SanitizeSSHCode(code string) string {
	switch code {
	case SSHCodeDisabled, SSHCodeDenied, SSHCodeBadRequest, SSHCodeUnreachable, SSHCodeAuth, SSHCodeHostKey,
		SSHCodeNoHostKey, SSHCodePassphrase, SSHCodeUnsupported, SSHCodeBusy, SSHCodeNoCred, SSHCodeExecDenied,
		SSHCodeStore, SSHCodeInternal:
		return code
	}
	return SSHCodeInternal
}

func isBase64Text(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
		default:
			return false
		}
	}
	return true
}

// SSHLabel 生成会话标签里展示的目标描述（不含凭据）。
func SSHLabel(user, addr string) string {
	return fmt.Sprintf("%s@%s", SanitizeText(user, 64), SanitizeText(addr, 200))
}
