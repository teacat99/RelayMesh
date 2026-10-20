package terminal

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 审计事件名（稳定的机器可读值）。
const (
	EvGateDenied        = "gate_denied"         // 总闸关闭时有人访问终端接口
	EvGateChanged       = "gate_changed"        // 总闸状态翻转
	EvUnlockOK          = "unlock_ok"           // 解锁成功
	EvUnlockFail        = "unlock_fail"         // 解锁失败（密码错误）
	EvUnlockLocked      = "unlock_locked"       // 解锁尝试过多被临时锁定
	EvLock              = "lock"                // 主动上锁
	EvDiscoveryOpen     = "discovery_open"      // 打开发现窗口
	EvDiscoveryClose    = "discovery_close"     // 关闭发现窗口（手动或到期）
	EvUpgradeWindow     = "upgrade_window"      // 某台主机的 Agent 升级窗口：开启 / 延时 / 结束 / 到期 / 检测到升级后自动关闭（详情里说明）
	EvWebUpgrade        = "agent_web_upgrade"   // 管理员在 Web 上请某台主机的 Agent 自己升级：请求 / 交接 / 完成 / 失败（详情里说明，含目标版本与确认过的会话数）
	EvHostPending       = "host_pending"        // 出现新的待启用主机
	EvHostEnabled       = "host_enabled"        // 启用主机
	EvHostIgnored       = "host_ignored"        // 忽略主机
	EvHostUnignored     = "host_unignored"      // 取消忽略
	EvHostDisabled      = "host_disabled"       // 停用主机
	EvHostDeleted       = "host_deleted"        // 删除主机
	EvHostRemark        = "host_remark"         // 修改备注
	EvAgentConnected    = "agent_connected"     // 已启用主机的 Agent 上线
	EvAgentHandshakeBad = "agent_handshake_bad" // Agent 握手失败（签名 / 版本 / 格式）
	EvSessionOpen       = "session_open"        // 新建会话
	EvSessionKill       = "session_kill"        // 终止会话
	EvSessionAttach     = "session_attach"      // 浏览器附着会话
	EvSessionDetach     = "session_detach"      // 浏览器断开会话
	EvHolderState       = "holder_state"        // 主机上的会话守护进程可用性变化
	EvSessionsLost      = "sessions_lost"       // 守护进程重启后报告：有会话已不存在（含原因）
	EvRecordRead        = "record_read"         // 人类查看了某个会话在主机上的记录（只记录谁看了哪个会话，不含内容）
	EvRecordDownload    = "record_download"     // 人类下载了某个会话的记录
	EvRecordPolicy      = "record_policy"       // 修改主机的录制策略
	EvLifecycle         = "lifecycle_changed"   // 修改主机默认或某个会话的生命周期（永久保持 / 空闲回收）
	EvLauncherSave      = "launcher_save"       // 新建或修改了终端启动预设（只记预设编号与主机，不含命令内容——命令里可能带令牌）
	EvLauncherDelete    = "launcher_delete"     // 删除了终端启动预设
	EvGuardWake         = "guard_wake"          // 人类点了「唤醒」：让浏览器把一句提示词输入到预设的会话里（或重新拉起它）；只记预设编号、方式与第几次，不含提示词与命令
	EvGuardPermit       = "guard_permit"        // 唤醒许可切换：只记预设编号、permit 值与 by，不含备注
	EvLauncherRun       = "launcher_run"        // 人类点了「非交互续聊」预设的「运行」：只记预设编号与权限姿态（运行编号在 run_id 里），不含提示词与命令
	EvLauncherAutoRun   = "launcher_auto_run"   // 系统按预设里打开的「异常退出时自动续跑」发起了一次运行（#151）：只记预设编号与第几次（运行编号在 run_id 里），不含提示词与命令
	EvLauncherAutoStop  = "launcher_auto_stop"  // 人类取消了一次排期中的自动续跑（#151）：只记预设编号（出事的运行编号在 run_id 里）
	EvLauncherAgentRun  = "launcher_agent_run"  // 项目管理 Agent 经 MCP 运行了一条预设（#179）：只记谁（身份编号）、预设编号、目标工作流与权限姿态（运行编号在 run_id 里），不含提示词与命令；被拒绝的尝试记成 deny / fail
	EvSessionEnable     = "session_enable"      // 人类在「待接入」会话页点了「启用 Agent」（#176）：只记工作流、预设编号、权限姿态与是否新开对话（运行编号在 run_id 里），不含提示词与命令
	EvSessionRun        = "session_run"         // 人类经 session-run 接口发起了会话页运行（#128；Web 从 #140 起改走预设运行 launcher_run，接口保留）：只记工作流与权限姿态（运行编号在 run_id 里），不含提示词与命令
	EvCredentialsChange = "credentials_changed" // 登录凭据变更，全部终端凭据与连接被撤销
	EvCredentialReveal  = "credential_reveal"   // 人类在设置页重新输入 Web 密码后查看 MCP 令牌（成功 / 密码错误 / 被锁定）；只记哪个令牌，绝不含令牌内容
	EvFilesScope        = "files_scope"         // 人类开启 / 关闭了某把令牌的「允许整个文件系统」（或因没有刚刚重新验证密码而被拒）；只记哪个令牌与动作

	// Agent 自动升级（#142）。只记版本号与原因，不含任何下载地址或票据。
	EvAutoUpgradeSwitch = "agent_auto_upgrade_switch" // 管理员打开 / 关闭 Hub 的「Agent 自动升级」总闸（详情里是新状态）
	// 静默自动升级的生命周期（主机自己发起，所以没有操作人，Actor 是 system）：
	EvAutoUpgradeStart    = "agent_auto_upgrade_start"    // Hub 批准了一台主机的静默升级请求（版本、升级时有多少运行中的会话 / 模型运行在详情里）
	EvAutoUpgradeOK       = "agent_auto_upgrade_ok"       // 升级完成：主机带着新版本重新上线了
	EvAutoUpgradeFail     = "agent_auto_upgrade_fail"     // 升级失败（原因代码与说明在详情里，含发布签名验证失败）
	EvAutoUpgradeRollback = "agent_auto_upgrade_rollback" // 新版本没有连上服务端，已自动还原成旧版本

	// 模型执行（M6）。命令文本与输出从不写入审计，只记录谁在哪台主机上做了什么类型的事。
	EvExecStart   = "exec_start"       // 模型发起一次运行（含需批准模式下提交的请求）
	EvExecDenied  = "exec_denied"      // 模型的请求被闸门拒绝
	EvExecCancel  = "exec_cancel"      // 运行被取消（模型 / 人 / 暂停 / 系统）
	EvExecApprove = "exec_approve"     // 人批准了一次待批准的运行
	EvExecReject  = "exec_reject"      // 人拒绝了一次待批准的运行，或批准超时
	EvExecPause   = "exec_pause"       // 暂停模型执行（全局 / 某台主机）
	EvExecResume  = "exec_resume"      // 恢复模型执行
	EvHostModel   = "host_model"       // 修改主机的模型访问设置（允许 / 执行模式 / 新模型默认态）
	EvModelGrant  = "model_host_grant" // 把一台主机写入模型令牌的主机名单（新增主机时按默认态授予，或管理员手动「授予现有模型」）；只记数量与令牌名
	EvRunLost     = "run_lost"         // 运行结果无法确认（主机重启 / 守护进程消失）
	EvExecRead    = "exec_output_read" // 人类查看了某次运行的完整输出

	// SSH 主机（M10）。凭据与私钥从不写入审计，只记录地址、用户、认证方式与主机密钥指纹。
	EvSSHProbe = "ssh_probe" // 探测了某个地址的主机密钥指纹（尚未登记）
	EvSSHAdd   = "ssh_host_add"
	EvSSHTest  = "ssh_host_test"
	EvSSHConn  = "ssh_host_conn"  // 修改了地址 / 用户 / 凭据
	EvSSHRekey = "ssh_host_rekey" // 重新固定了主机密钥（管理员核对了新指纹）
	// EvSSHCredSync：网关（重新）就绪时 Hub 与它对账了登录凭据；只记主机数、缺失数与被回收数，从不记任何凭据内容。
	EvSSHCredSync = "ssh_cred_sync"

	// OAuth 授权服务器（不含授权码、令牌与验证码）。
	EvOAuthConsentGranted = "oauth_consent_granted"
	EvOAuthConsentDenied  = "oauth_consent_denied"
	EvOAuthTokenIssued    = "oauth_token_issued"
	EvOAuthTokenRefresh   = "oauth_token_refresh"
	EvOAuthRefreshReplay  = "oauth_refresh_replay"
	EvOAuthCodeReplay     = "oauth_code_replay"
	EvOAuthRevoke         = "oauth_revoke"
	EvOAuthClientRegister = "oauth_client_register"
	EvOAuthPolicyChanged  = "oauth_policy_changed"
)

// 审计操作者类型。
const (
	ActorHuman  = "human"
	ActorModel  = "model"
	ActorSystem = "system"
)

// 审计结果。
const (
	ResultOK   = "ok"
	ResultFail = "fail"
	ResultDeny = "deny"
)

// auditEvent 是待写入的审计事件；Detail 写入前会被净化并限长。
type auditEvent struct {
	Event       string
	Result      string
	Actor       string
	IP          string
	MachineCode string
	SessionID   string
	Detail      string
	ActorKind   string // human / model / system；留空表示沿用旧事件的默认
	RunID       string // 与某次模型运行相关时给出
}

// auditor 写审计日志：写失败只记日志、不影响业务；高频来源的事件按 (事件, 键) 限流并合并计数，防止被灌爆数据库。
type auditor struct {
	store *store.Store
	now   func() time.Time

	mu      sync.Mutex
	limits  map[string]*auditBucket
	lastGC  time.Time
	pruneAt time.Time
}

type auditBucket struct {
	windowStart time.Time
	written     int
	dropped     int
}

const (
	auditRateWindow = time.Minute
	auditRateBurst  = 6
	auditKeepRows   = 20000
)

func newAuditor(st *store.Store, now func() time.Time) *auditor {
	return &auditor{store: st, now: now, limits: make(map[string]*auditBucket)}
}

// record 写入一条审计记录。
func (a *auditor) record(ctx context.Context, e auditEvent) {
	if a == nil || a.store == nil {
		return
	}
	if e.Result == "" {
		e.Result = ResultOK
	}
	entry := &model.TerminalAuditLog{
		CreatedAt:   a.now(),
		Event:       e.Event,
		Result:      e.Result,
		Actor:       termproto.SanitizeText(e.Actor, 64),
		IP:          termproto.SanitizeText(e.IP, 64),
		MachineCode: termproto.SanitizeText(e.MachineCode, 32),
		SessionID:   termproto.SanitizeText(e.SessionID, 40),
		Detail:      termproto.SanitizeText(e.Detail, 512),
		ActorKind:   termproto.SanitizeText(e.ActorKind, 8),
		RunID:       termproto.SanitizeText(e.RunID, 32),
	}
	if err := a.store.AppendTerminalAudit(ctx, entry); err != nil && ctx.Err() == nil {
		log.Printf("[terminal] 审计日志写入失败 (%s): %v", e.Event, err)
	}
}

// recordLimited 用于攻击者可触发的高频事件：同一 (event, key) 在一分钟内最多落盘 auditRateBurst 条，
// 超出的只计数；窗口滚动时补写一条「被合并 N 条」的汇总记录。
func (a *auditor) recordLimited(ctx context.Context, key string, e auditEvent) {
	if a == nil || a.store == nil {
		return
	}
	now := a.now()
	bucketKey := e.Event + "|" + key

	a.mu.Lock()
	a.gcLocked(now)
	b := a.limits[bucketKey]
	if b == nil {
		b = &auditBucket{windowStart: now}
		a.limits[bucketKey] = b
	}
	var summary *auditEvent
	if now.Sub(b.windowStart) >= auditRateWindow {
		if b.dropped > 0 {
			s := e
			s.Detail = "（一分钟内另有 " + strconv.Itoa(b.dropped) + " 条同类事件已合并）"
			summary = &s
		}
		b.windowStart, b.written, b.dropped = now, 0, 0
	}
	allow := b.written < auditRateBurst
	if allow {
		b.written++
	} else {
		b.dropped++
	}
	a.mu.Unlock()

	if summary != nil {
		a.record(ctx, *summary)
	}
	if allow {
		a.record(ctx, e)
	}
}

// gcLocked 定期清理长时间没有事件的限流桶，避免 map 无界增长（攻击者可变换 key）。
func (a *auditor) gcLocked(now time.Time) {
	if now.Sub(a.lastGC) < auditRateWindow {
		return
	}
	a.lastGC = now
	for k, b := range a.limits {
		if now.Sub(b.windowStart) >= 2*auditRateWindow {
			delete(a.limits, k)
		}
	}
}

// maybePrune 每天至多修剪一次，只保留最近 auditKeepRows 条。
func (a *auditor) maybePrune(ctx context.Context) {
	if a == nil || a.store == nil {
		return
	}
	now := a.now()
	a.mu.Lock()
	if now.Before(a.pruneAt) {
		a.mu.Unlock()
		return
	}
	a.pruneAt = now.Add(24 * time.Hour)
	a.mu.Unlock()
	if n, err := a.store.PruneTerminalAudit(ctx, auditKeepRows); err != nil {
		if ctx.Err() == nil {
			log.Printf("[terminal] 审计日志修剪失败: %v", err)
		}
	} else if n > 0 {
		log.Printf("[terminal] 审计日志已修剪 %d 条旧记录", n)
	}
}
