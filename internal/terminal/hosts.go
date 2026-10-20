package terminal

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

var (
	// ErrHostNotFound 表示机器码不存在（或不处于该操作适用的状态）。
	ErrHostNotFound = errors.New("host not found")
)

// HostView 是返回给前端的主机视图。自报字段均已净化，界面只应按纯文本渲染。
type HostView struct {
	MachineCode  string `json:"machine_code"`
	Remark       string `json:"remark"`
	Status       string `json:"status"`    // enabled | disabled | blocked
	Temporary    bool   `json:"temporary"` // 仅内存的临时启用
	Online       bool   `json:"online"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
	// AgentRev 是主机上 Agent 的「Agent 侧代码指纹」（空 = 旧版本 / 非发布构建）。版本号跟着整个仓库的构建走，指纹只在 Agent 代码真的变了才变。
	AgentRev   string                  `json:"agent_rev,omitempty"`
	Privilege  string                  `json:"privilege"`
	LastIP     string                  `json:"last_ip"`
	LastSeenAt *time.Time              `json:"last_seen_at,omitempty"`
	EnabledAt  *time.Time              `json:"enabled_at,omitempty"`
	Sessions   []termproto.SessionInfo `json:"sessions"`
	// RecOff 是管理员在这台主机上关闭终端记录的开关；RecServerOff 表示服务端全局关闭（此时单机开关不起作用）。
	RecOff       bool `json:"rec_off"`
	RecServerOff bool `json:"rec_server_off,omitempty"`
	// IdleSec 是这台主机上会话的默认生命周期（秒）：连续没有交互这么久才回收；0 表示永久保持。
	IdleSec int `json:"idle_sec"`
	// 模型访问（M6）：AllowModel 是 G2（管理员允许模型使用这台主机，默认关闭）；ExecMode 为空表示 auto；
	// ModelPaused 是这台主机的暂停开关；ModelRunning 是此刻进行中的模型运行数（含待批准）。
	AllowModel   bool   `json:"allow_model"`
	ExecMode     string `json:"exec_mode"`
	ModelPaused  bool   `json:"model_paused"`
	ModelRunning int    `json:"model_running"`
	// DefaultForNewModels 是这台主机对「新建模型授权」的默认态：allow（新建令牌时预先授予，新增主机时授予现有令牌）/ deny（默认）。
	// 只是预填规则：令牌能否使用主机始终只看令牌自己的主机名单。
	DefaultForNewModels string `json:"default_for_new_models"`
	// Holder 是会话守护进程的状态；nil 表示 Agent 没有报告（旧版本，会话随 Agent 进程结束）。
	Holder        *HolderView `json:"holder,omitempty"`
	DuplicateName bool        `json:"duplicate_name,omitempty"` // 与其他主机同名，请以机器码为准
	// Kind 是主机类型：agent（装了 Agent）或 ssh（只经 SSH 供模型执行，没有人工终端与文件）。
	Kind string   `json:"kind"`
	SSH  *SSHView `json:"ssh,omitempty"`
	// CanUninstall：在线的 Agent 声明了「删除主机时可以同时卸载设备端」的能力（装成常驻服务运行、版本足够新、系统受支持）。
	// 离线、旧版本、前台运行 / 临时 Agent 一律为 false，界面据此决定是否提供「同时卸载设备端」。
	CanUninstall bool `json:"can_uninstall,omitempty"`
	// 升级提醒（只对装了 Agent 的持久主机）：LatestAgentVersion 是本服务端自带的 Agent 版本；
	// LatestAgentRev 是自带这批 Agent 的代码指纹（分发目录的 REV 文件，没有则为空）。
	// 两边都有指纹时只比指纹：指纹不同才算 UpgradeAvailable（能从版本号确定更旧的 UpgradeKind = older，否则 = changed），
	// 指纹一致则 UpToDateByRev = true（版本号不同只是构建号不同，Agent 代码一致）；任一边没有指纹时回退到比版本号，
	// UpgradeKind 区分「能确定更旧」（older）与「不同但无法比较新旧」（different）。
	// UpgradeWindow 非空表示这台主机的升级窗口正开着（含供界面拼出升级命令的票据）。
	LatestAgentVersion string             `json:"latest_agent_version,omitempty"`
	LatestAgentRev     string             `json:"latest_agent_rev,omitempty"`
	UpgradeAvailable   bool               `json:"upgrade_available,omitempty"`
	UpgradeKind        string             `json:"upgrade_kind,omitempty"`
	UpToDateByRev      bool               `json:"up_to_date_by_rev,omitempty"`
	UpgradeWindow      *UpgradeWindowView `json:"upgrade_window,omitempty"`
	// Web 一键升级：WebUpgrade 是在线 Agent 声明的能力（空 = 不支持；off = 支持但主机所有者没有在本机允许；on = 已允许）；
	// UpgradeJob 是最近一次升级请求的进展（含结果）；LastUpgrade 是主机本地记录的上一次升级结果（回滚后旧版本上线时会带回）。
	WebUpgrade  string                 `json:"web_upgrade,omitempty"`
	UpgradeJob  *UpgradeJobView        `json:"upgrade_job,omitempty"`
	LastUpgrade *termproto.UpgradeNote `json:"last_upgrade,omitempty"`
	// 自动升级：AutoUpgrade 是在线 Agent 声明的模式（空 = 不提供：旧版本 / 前台运行 / 暂不支持的系统；off = 提供但主机所有者没有开启；
	// notify / silent = 主机所有者已在本机开启）；AutoCheck 是这台主机最近一次检查的结果（还没检查过则没有，Agent 重连后重新检查）。
	AutoUpgrade string         `json:"auto_upgrade,omitempty"`
	AutoCheck   *AutoCheckView `json:"auto_check,omitempty"`
	// AutoWindow 是静默自动升级允许开始的时间段（主机本地时间，HH:MM-HH:MM，可跨午夜），只在 AutoUpgrade == "silent" 且设了时间段时有。
	// 仅供界面展示：是否允许升级由 Agent 在本机判断，Hub 的裁决不看它。
	AutoWindow string `json:"auto_window,omitempty"`
	// AutoGrant 是这台主机最近一次静默升级请求得到的裁决（批准 / 没有签名 / 主机忙 …）；还没请求过则没有，Agent 重连后重新来过。
	AutoGrant *AutoGrantView `json:"auto_grant,omitempty"`
}

// PendingView 是发现列表中的待启用主机。
type PendingView struct {
	MachineCode   string    `json:"machine_code"`
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	AgentVersion  string    `json:"agent_version"`
	Privilege     string    `json:"privilege"`
	Kind          string    `json:"kind"` // persistent | temporary
	IP            string    `json:"ip"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	Online        bool      `json:"online"`
	DuplicateName bool      `json:"duplicate_name,omitempty"`
}

// DiscoveryView 是发现窗口的展示状态。
type DiscoveryView struct {
	Open             bool       `json:"open"`
	Always           bool       `json:"always"`
	Until            *time.Time `json:"until,omitempty"`
	RemainingSeconds int        `json:"remaining_seconds"`
}

func (h *Hub) discoveryView() DiscoveryView {
	now := h.now()
	active, always, until := h.disc.state(now)
	v := DiscoveryView{Open: active, Always: always}
	if active && !always {
		u := until
		v.Until = &u
		if rem := int(until.Sub(now).Seconds()); rem > 0 {
			v.RemainingSeconds = rem
		}
	}
	return v
}

// listHosts 汇总已处理的主机（持久 + 临时）与待启用主机。
func (h *Hub) listHosts(ctx context.Context) ([]HostView, []PendingView, error) {
	recs, err := h.store.ListTerminalHosts(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := h.now()
	latest, latestRev := h.dist.Version(), h.dist.Rev()
	// 「会话已不存在」的忽略记录只影响提示是否显示；读不出来时宁可多提醒，也不让整个主机列表失败。
	dismissed, derr := h.store.ListTerminalLostDismissals(ctx)
	if derr != nil {
		log.Printf("[terminal] 读取「会话已不存在」忽略记录失败: %v", derr)
	}

	hosts := make([]HostView, 0, len(recs))
	for _, r := range recs {
		v := HostView{
			MachineCode:  r.MachineCode,
			Remark:       r.Remark,
			Status:       r.Status,
			Hostname:     r.Hostname,
			OS:           r.OS,
			Arch:         r.Arch,
			AgentVersion: r.AgentVersion,
			AgentRev:     r.AgentRev,
			Privilege:    r.Privilege,
			LastIP:       r.LastIP,
			LastSeenAt:   r.LastSeenAt,
			EnabledAt:    r.EnabledAt,
			Sessions:     []termproto.SessionInfo{},
			RecOff:       r.RecOff,
			RecServerOff: h.opts.RecordOff,
			IdleSec:      r.IdleSec,
			AllowModel:   r.AllowModel,
			ExecMode:     r.ExecMode,
			ModelPaused:  r.ModelPaused,
			ModelRunning: h.exec.ActiveOnHost(r.MachineCode),
			Kind:         model.HostKindAgent,

			DefaultForNewModels: store.HostDefaultState(r.DefaultForNewModels),
		}
		if r.IsSSH() {
			v.Kind = model.HostKindSSH
			v.SSH = h.ssh.view(r)
			v.Online = h.ssh.reachable(r)
			if r.ViaGateway() {
				// 经网关接入的主机：网关不在线（或没开启网关能力）时，终端就是打不开的。
				v.Online = v.Online && v.SSH.GatewayReady
			} else {
				// Hub 直连的主机：人工终端的会话持有在 Hub 里（见 localterm.go），列在这台主机名下。
				v.Sessions = h.local.list(r.MachineCode)
			}
		} else {
			h.fillOnline(&v)
			v.Holder.dropDismissedLost(dismissed[r.MachineCode])
			h.fillUpgrade(&v, latest, latestRev, now)
		}
		hosts = append(hosts, v)
	}
	for _, t := range h.temps.snapshot(now) {
		seen, enabled := t.LastSeen, t.EnabledAt
		v := HostView{
			MachineCode:  t.Code,
			Remark:       t.Remark,
			Status:       termproto.HostStatusEnabled,
			Temporary:    true,
			Hostname:     t.Meta.Host,
			OS:           t.Meta.OS,
			Arch:         t.Meta.Arch,
			AgentVersion: t.Meta.Ver,
			AgentRev:     t.Meta.Rev,
			Privilege:    t.Meta.Priv,
			LastIP:       t.IP,
			LastSeenAt:   &seen,
			EnabledAt:    &enabled,
			Sessions:     []termproto.SessionInfo{},
			RecServerOff: h.opts.RecordOff,
			Kind:         model.HostKindAgent,

			DefaultForNewModels: model.HostDefaultDeny,
		}
		h.fillOnline(&v)
		hosts = append(hosts, v)
	}

	entries := h.pending.snapshot()
	pend := make([]PendingView, 0, len(entries))
	for _, e := range entries {
		pend = append(pend, PendingView{
			MachineCode:  e.Code,
			Hostname:     e.Meta.Host,
			OS:           e.Meta.OS,
			Arch:         e.Meta.Arch,
			AgentVersion: e.Meta.Ver,
			Privilege:    e.Meta.Priv,
			Kind:         e.Meta.Kind,
			IP:           e.IP,
			FirstSeen:    e.FirstSeen,
			LastSeen:     e.LastSeen,
			Online:       e.OfflineAt.IsZero(),
		})
	}

	markDuplicateNames(hosts, pend)
	return hosts, pend, nil
}

func (h *Hub) fillOnline(v *HostView) {
	if a := h.onlineAgent(v.MachineCode); a != nil && a.isEnabled() {
		v.Online = true
		v.CanUninstall = a.meta.Uninstall
		v.WebUpgrade = a.meta.WebUpgrade
		v.LastUpgrade = a.meta.LastUpgrade
		v.AutoUpgrade = a.meta.AutoUpgrade
		v.AutoWindow = a.meta.AutoWindow
		v.AutoCheck = a.autoCheckView()
		v.AutoGrant = a.autoGrantView()
		v.Sessions = a.sessionList()
		v.Holder = a.holderView()
		// 在线时以本次握手的自报信息为准（主机可能改了名）。
		if v.Hostname == "" {
			v.Hostname = a.meta.Host
		}
	}
}

// markDuplicateNames 标注显示名重复的条目。显示名 = 备注，没有备注才是主机名（见 displayNameOf）：
// 它是界面标题，也是模型在 terminal_targets 里看到的 name，所以「同名」按它判断，两边口径一致；
// 主机名是 Agent 自报的，备注是管理员写的，都不能作为身份依据，界面必须提示以机器码为准。
// 参与比较的是已启用的主机与待启用的发现项（停用 / 屏蔽的主机不会被模型用到，也不会与新主机冲突）。
func markDuplicateNames(hosts []HostView, pend []PendingView) {
	count := map[string]int{}
	for _, v := range hosts {
		if n := displayNameOf(v.Remark, v.Hostname); v.Status == termproto.HostStatusEnabled && n != "" {
			count[strings.ToLower(n)]++
		}
	}
	for _, v := range pend {
		if v.Hostname != "" {
			count[strings.ToLower(v.Hostname)]++
		}
	}
	for i := range hosts {
		n := displayNameOf(hosts[i].Remark, hosts[i].Hostname)
		hosts[i].DuplicateName = hosts[i].Status == termproto.HostStatusEnabled && n != "" && count[strings.ToLower(n)] > 1
	}
	for i := range pend {
		pend[i].DuplicateName = pend[i].Hostname != "" && count[strings.ToLower(pend[i].Hostname)] > 1
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		if hosts[i].Online != hosts[j].Online {
			return hosts[i].Online
		}
		return hosts[i].MachineCode < hosts[j].MachineCode
	})
}

// hostView 返回单个主机的视图（持久或临时）。
func (h *Hub) hostView(ctx context.Context, code string) (*HostView, error) {
	hosts, _, err := h.listHosts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if hosts[i].MachineCode == code {
			return &hosts[i], nil
		}
	}
	return nil, ErrHostNotFound
}

// ---------- 状态变更 ----------

// enableHost 启用主机：待启用条目转正，或把已停用的持久主机重新启用。
// 被忽略（blocked）的主机不能直接启用：那是管理员明确的拒绝，需要先「取消忽略」让它重新出现在发现列表。
//
// defaultModels 是这台主机对新建模型授权的默认态（allow / deny / 空串，空串等价于 deny），只在「新增主机」
// （待启用条目首次转正）时生效：选 allow 会把它写入现有模型令牌的主机名单，并返回授予结果供界面提示。
// 重新启用一台已存在的主机不会改动任何令牌名单——名单是管理员已经确认过的事实。
func (h *Hub) enableHost(ctx context.Context, code, remark, defaultModels, actor, ip string) (*HostView, *store.HostGrantResult, error) {
	remark = termproto.SanitizeText(remark, termproto.MaxRemarkRunes)
	if err := store.ValidateHostDefaultForNewModels(defaultModels); err != nil {
		return nil, nil, err
	}
	now := h.now()
	var grant *store.HostGrantResult

	if e, ok := h.pending.take(code); ok {
		if e.Meta.Kind == termproto.KindTemporary {
			h.temps.add(&tempHost{
				Code: code, PubKey: e.PubKey, Meta: e.Meta, Remark: remark, IP: e.IP,
				EnabledAt: now, LastSeen: now, Online: e.agent != nil,
			})
		} else {
			rec := &model.TerminalHost{
				MachineCode:  code,
				PublicKey:    termproto.EncodeKey(e.PubKey),
				Remark:       remark,
				Status:       termproto.HostStatusEnabled,
				Hostname:     e.Meta.Host,
				OS:           e.Meta.OS,
				Arch:         e.Meta.Arch,
				AgentVersion: e.Meta.Ver,
				AgentRev:     e.Meta.Rev,
				Privilege:    e.Meta.Priv,
				LastIP:       e.IP,
				LastSeenAt:   &now,
				EnabledAt:    &now,

				DefaultForNewModels: store.HostDefaultState(defaultModels),
			}
			if err := h.store.SaveTerminalHost(ctx, rec); err != nil {
				h.pending.restore(e) // 持久化失败：放回待启用列表，管理员可重试
				return nil, nil, err
			}
			grant = h.grantNewHostByDefault(ctx, rec, actor, ip)
		}
		if e.agent != nil {
			h.activate(e.agent)
		}
		h.audit.record(ctx, auditEvent{Event: EvHostEnabled, Actor: actor, IP: ip, MachineCode: code, Detail: hostDetail(e.Meta.Host, remark, e.Meta.Kind)})
		view, err := h.hostView(ctx, code)
		return view, grant, err
	}

	rec, err := h.store.GetTerminalHost(ctx, code)
	if err != nil {
		return nil, nil, err
	}
	if rec == nil || rec.Status == termproto.HostStatusBlocked {
		return nil, nil, ErrHostNotFound
	}
	if rec.Status != termproto.HostStatusEnabled {
		if _, err := h.store.SetTerminalHostStatus(ctx, code, termproto.HostStatusEnabled); err != nil {
			return nil, nil, err
		}
	}
	if remark != "" && remark != rec.Remark {
		if err := h.store.SetTerminalHostRemark(ctx, code, remark); err != nil {
			return nil, nil, err
		}
	}
	h.audit.record(ctx, auditEvent{Event: EvHostEnabled, Actor: actor, IP: ip, MachineCode: code, Detail: hostDetail(rec.Hostname, remark, "persistent")})
	view, err := h.hostView(ctx, code)
	return view, nil, err
}

// grantNewHostByDefault 按主机的默认态处理「新增主机」：默认态为 allow 时，把它写入现有模型令牌的主机名单。
// 授予失败只记日志、不让启用失败：主机已经启用，管理员可以在「模型执行与权限」里手动补授予。
func (h *Hub) grantNewHostByDefault(ctx context.Context, rec *model.TerminalHost, actor, ip string) *store.HostGrantResult {
	if !rec.GrantsNewModelsByDefault() {
		return nil
	}
	res, err := h.grantHostToModels(ctx, rec.MachineCode, "new_host", actor, ip)
	if err != nil {
		log.Printf("[terminal] 新增主机 %s 后按默认态授予模型令牌失败: %v", rec.MachineCode, err)
		return nil
	}
	return &res
}

// grantHostToModels 把一台主机写入现有模型令牌的主机名单并写审计（只记数量与令牌名，令牌内容从不出现）。
func (h *Hub) grantHostToModels(ctx context.Context, code, reason, actor, ip string) (store.HostGrantResult, error) {
	res, err := h.store.GrantHostToTerminalCredentials(ctx, code)
	if err != nil {
		return res, err
	}
	names := res.Granted
	suffix := ""
	if len(names) > 8 {
		names, suffix = names[:8], ",…"
	}
	h.audit.record(ctx, auditEvent{
		Event: EvModelGrant, Actor: actor, IP: ip, MachineCode: code, ActorKind: ActorHuman,
		Detail: "reason=" + reason + " granted=" + strconv.Itoa(len(res.Granted)) + " full=" + strconv.Itoa(res.Full) + " tokens=" + strings.Join(names, ",") + suffix,
	})
	return res, nil
}

// activate 把待启用的连接提升为在线 Agent：通知 Agent 并登记。
func (h *Hub) activate(a *agentConn) {
	if !a.promote() {
		return // 连接已经断开；Agent 重连时会按已启用主机直接上线
	}
	if a.temp {
		h.temps.setOnline(a.code, true, &a.meta, a.ip, h.now())
	}
	if err := a.writeJSON(termproto.Approved{T: termproto.TApproved, Policy: h.policyFor(h.ctx, a.code)}); err != nil {
		return
	}
	if old := h.registerAgent(a); old != nil && old != a {
		old.closeWith(termproto.CloseSuperseded, "superseded by a newer connection")
	}
}

// ignoreHost 忽略待启用主机：持久身份记为 blocked（之后不再出现在发现列表），临时身份直接丢弃。
func (h *Hub) ignoreHost(ctx context.Context, code, actor, ip string) error {
	e, ok := h.pending.take(code)
	if !ok {
		return ErrHostNotFound
	}
	if e.Meta.Kind != termproto.KindTemporary {
		now := h.now()
		rec := &model.TerminalHost{
			MachineCode:  code,
			PublicKey:    termproto.EncodeKey(e.PubKey),
			Status:       termproto.HostStatusBlocked,
			Hostname:     e.Meta.Host,
			OS:           e.Meta.OS,
			Arch:         e.Meta.Arch,
			AgentVersion: e.Meta.Ver,
			AgentRev:     e.Meta.Rev,
			Privilege:    e.Meta.Priv,
			LastIP:       e.IP,
			LastSeenAt:   &now,
		}
		if err := h.store.SaveTerminalHost(ctx, rec); err != nil {
			h.pending.restore(e)
			return err
		}
	}
	if e.agent != nil {
		_ = e.agent.writeJSON(termproto.Rejected{T: termproto.TRejected, Reason: "ignored"})
		e.agent.closeWith(termproto.CloseDisabled, "ignored")
	}
	h.audit.record(ctx, auditEvent{Event: EvHostIgnored, Actor: actor, IP: ip, MachineCode: code, Detail: hostDetail(e.Meta.Host, "", e.Meta.Kind)})
	return nil
}

// unignoreHost 取消忽略：删除 blocked 记录，该主机之后可再次出现在发现列表。
func (h *Hub) unignoreHost(ctx context.Context, code, actor, ip string) error {
	rec, err := h.store.GetTerminalHost(ctx, code)
	if err != nil {
		return err
	}
	if rec == nil || rec.Status != termproto.HostStatusBlocked {
		return ErrHostNotFound
	}
	if _, err := h.store.DeleteTerminalHost(ctx, code); err != nil {
		return err
	}
	h.audit.record(ctx, auditEvent{Event: EvHostUnignored, Actor: actor, IP: ip, MachineCode: code, Detail: hostDetail(rec.Hostname, "", "")})
	return nil
}

// disableHost 停用已启用的主机：立即断开连接，并令 Agent 终止其全部会话。临时主机等同于移除。
func (h *Hub) disableHost(ctx context.Context, code, actor, ip string) error {
	detail := ""
	if h.temps.remove(code) {
		detail = "temporary"
	} else {
		rec, err := h.store.GetTerminalHost(ctx, code)
		if err != nil {
			return err
		}
		if rec == nil || rec.Status == termproto.HostStatusBlocked {
			return ErrHostNotFound
		}
		if _, err := h.store.SetTerminalHostStatus(ctx, code, termproto.HostStatusDisabled); err != nil {
			return err
		}
		detail = hostDetail(rec.Hostname, rec.Remark, "")
		// 经网关接入的 SSH 主机：它的终端会话运行在网关上，停用时一并终止，别让已切断的连接继续开着。
		if rec.ViaGateway() {
			h.endGatewaySessions(ctx, rec.SSHGateway, code, "disabled", actor, ip)
		} else if rec.IsSSH() {
			// Hub 直连的 SSH 主机：终端会话持有在 Hub 里，同样一并终止。
			h.endLocalSessions(ctx, code, "disabled", actor, ip)
		}
	}
	h.kickAgent(code, "host_disabled")
	h.closeUpgradeWindow(ctx, code, actor, ip, "disabled")
	h.webUpgrades.drop(code)
	h.audit.record(ctx, auditEvent{Event: EvHostDisabled, Actor: actor, IP: ip, MachineCode: code, Detail: detail})
	return nil
}

// UninstallOutcome 是删除主机时「同时卸载设备端」的结果，原样返回给界面，让管理员知道设备上到底发生了什么。
type UninstallOutcome string

const (
	UninstallNone        UninstallOutcome = ""            // 没有请求卸载
	UninstallRequested   UninstallOutcome = "requested"   // 已向在线的 Agent 下发卸载请求（卸载在设备上异步完成）
	UninstallOffline     UninstallOutcome = "offline"     // 主机不在线，没法下发——需要在设备上手动卸载
	UninstallUnsupported UninstallOutcome = "unsupported" // 在线，但这个 Agent 不支持（旧版本 / 不是常驻服务 / 暂不支持的系统）
	UninstallFailed      UninstallOutcome = "failed"      // 下发时出错
)

// requestUninstall 向在线的 Agent 下发卸载请求。必须在 kickAgent 之前调用：帧是有序的，
// Agent 先收到 uninstall，再收到随后的策略与关闭。请求前提（登录 + 解锁）由路由的 guard 保证，这里只做能力判断。
func (h *Hub) requestUninstall(code string) UninstallOutcome {
	a := h.onlineAgent(code)
	if a == nil || !a.isEnabled() {
		return UninstallOffline
	}
	if !a.meta.Uninstall {
		return UninstallUnsupported
	}
	// 主机已经被删除，留着身份文件没有意义：一并清除（终端录像与命令输出仍留在设备上，由主机所有者自行处理）。
	if err := a.writeJSON(termproto.Uninstall{T: termproto.TUninstall, Purge: true}); err != nil {
		log.Printf("[terminal] 向 %s 下发卸载请求失败: %v", code, err)
		return UninstallFailed
	}
	return UninstallRequested
}

// deleteHost 删除主机记录（任意状态的持久主机或临时主机）；在线连接被断开。
// uninstall 为 true 时，先请求在线的 Agent 卸载自己（见 requestUninstall），结果通过返回值告知调用方。
func (h *Hub) deleteHost(ctx context.Context, code, actor, ip string, uninstall bool) (UninstallOutcome, error) {
	detail := ""
	if h.temps.remove(code) {
		detail = "temporary"
	} else {
		rec, err := h.store.GetTerminalHost(ctx, code)
		if err != nil {
			return UninstallNone, err
		}
		if rec == nil {
			return UninstallNone, ErrHostNotFound
		}
		if !rec.IsSSH() {
			// 仍被 SSH 主机当作网关：先删除或改接它们，免得它们变成无人可达的孤儿。
			n, err := h.store.CountSSHHostsViaGateway(ctx, code)
			if err != nil {
				return UninstallNone, err
			}
			if n > 0 {
				return UninstallNone, &ErrGatewayInUse{Count: n}
			}
		}
		if _, err := h.store.DeleteTerminalHost(ctx, code); err != nil {
			return UninstallNone, err
		}
		if err := h.store.DeleteTerminalSessionsOfHost(ctx, code); err != nil {
			log.Printf("[terminal] 删除主机的会话索引失败 (%s): %v", code, err)
		}
		if err := h.store.DeleteTerminalRunsOfHost(ctx, code); err != nil {
			log.Printf("[terminal] 删除主机的模型运行记录失败 (%s): %v", code, err)
		}
		if err := h.store.DeleteTerminalArchivesOfHost(ctx, code); err != nil {
			log.Printf("[terminal] 删除主机的侧栏归档记录失败 (%s): %v", code, err)
		}
		if err := h.store.DeleteTerminalLostDismissalsOfHost(ctx, code); err != nil {
			log.Printf("[terminal] 删除主机的「会话已不存在」忽略记录失败 (%s): %v", code, err)
		}
		detail = hostDetail(rec.Hostname, rec.Remark, "")
		// 经网关接入的 SSH 主机：终端会话在网关上，删除时一并终止。
		if rec.ViaGateway() {
			h.endGatewaySessions(ctx, rec.SSHGateway, code, "deleted", actor, ip)
			// 凭据只存在网关上：主机删除后一并清掉（尽力而为，网关离线时留待下一次对账回收）。
			// 要等这台主机名下的运行取消完毕再删——取消远端作业还要用这份凭据。
			h.dropGatewayCredWhenIdle(rec.SSHGateway, code)
		} else if rec.IsSSH() {
			h.endLocalSessions(ctx, code, "deleted", actor, ip)
		}
	}
	outcome := UninstallNone
	if uninstall {
		outcome = h.requestUninstall(code)
		detail = strings.TrimSpace(detail + " uninstall=" + string(outcome))
	}
	h.kickAgent(code, "host_deleted")
	h.closeUpgradeWindow(ctx, code, actor, ip, "deleted")
	h.webUpgrades.drop(code)
	h.audit.record(ctx, auditEvent{Event: EvHostDeleted, Actor: actor, IP: ip, MachineCode: code, Detail: detail})
	return outcome, nil
}

// kickAgent 断开在线 Agent：先下发「终端关闭」策略令其终止全部会话，再以 CloseDisabled 关闭。
func (h *Hub) kickAgent(code, reason string) {
	h.exec.hostGone(code, cancelHostRemoved) // 先取消这台主机上的模型运行，再断开连接
	h.sshForgetHost(code)
	a := h.onlineAgent(code)
	if a == nil {
		return
	}
	_ = a.writeJSON(termproto.PolicyMsg{T: termproto.TPolicy, Policy: termproto.Policy{Terminal: false, Reason: reason}})
	a.closeAttachments(termproto.CloseNotFound, reason)
	a.closeWith(termproto.CloseDisabled, reason)
}

// setHostRemark 修改备注（不需要解锁令牌，仅登录）。
func (h *Hub) setHostRemark(ctx context.Context, code, remark, actor, ip string) error {
	remark = termproto.SanitizeText(remark, termproto.MaxRemarkRunes)
	if h.temps.setRemark(code, remark) {
		h.audit.record(ctx, auditEvent{Event: EvHostRemark, Actor: actor, IP: ip, MachineCode: code, Detail: remark})
		return nil
	}
	if err := h.store.SetTerminalHostRemark(ctx, code, remark); err != nil {
		if errors.Is(err, store.ErrTerminalHostNotFound) {
			return ErrHostNotFound
		}
		return err
	}
	h.audit.record(ctx, auditEvent{Event: EvHostRemark, Actor: actor, IP: ip, MachineCode: code, Detail: remark})
	return nil
}

// openDiscovery 打开发现窗口（常开模式下无操作），返回展示状态。
func (h *Hub) openDiscovery(ctx context.Context, actor, ip string, dur time.Duration) DiscoveryView {
	h.disc.open(h.now(), dur)
	h.audit.record(ctx, auditEvent{Event: EvDiscoveryOpen, Actor: actor, IP: ip})
	return h.discoveryView()
}

// extendDiscovery 顺延发现窗口（续期）；窗口没开着时等同于开启，常开模式下无操作。
// 续期不换发安装票据：正在进行的下载、已经复制出去的命令都不会因此中断。
func (h *Hub) extendDiscovery(ctx context.Context, actor, ip string, add time.Duration) DiscoveryView {
	if until, ok := h.disc.extend(h.now(), add); ok {
		h.audit.record(ctx, auditEvent{Event: EvDiscoveryOpen, Actor: actor, IP: ip, Detail: "extend until=" + until.UTC().Format(time.RFC3339)})
		return h.discoveryView()
	}
	return h.openDiscovery(ctx, actor, ip, add)
}

// closeDiscovery 关闭发现窗口，并清空待启用列表、断开这些连接。
func (h *Hub) closeDiscovery(ctx context.Context, actor, ip string) DiscoveryView {
	h.disc.close(h.now())
	h.dropPending("discovery closed")
	h.audit.record(ctx, auditEvent{Event: EvDiscoveryClose, Actor: actor, IP: ip})
	return h.discoveryView()
}

func hostDetail(hostname, remark, kind string) string {
	parts := make([]string, 0, 3)
	if hostname != "" {
		parts = append(parts, "host="+hostname)
	}
	if remark != "" {
		parts = append(parts, "remark="+remark)
	}
	if kind != "" && kind != termproto.KindPersistent {
		parts = append(parts, "kind="+kind)
	}
	return strings.Join(parts, " ")
}
