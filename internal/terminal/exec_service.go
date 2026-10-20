package terminal

import (
	"context"
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 模型执行服务（ExecService）：MCP 的 terminal_targets / terminal_exec / terminal_run 在 Hub 一侧的实现。
//
// 授权是一条严格的流水线，每一道闸只能让请求更难通过：
//
//	G1 服务端总开关（RELAYMESH_TERMINAL_EXEC=on 且终端总闸开启，另加令牌强度）
//	G3 令牌授权（终端权限位 + 主机白名单）→ 解析目标 → G2 管理员对这台主机的「允许模型」
//	G5 暂停开关（全局 / 单机）→ 主机在线 → G4 本机一票否决（--deny-model-exec）
//	→ 执行模式（取主机设置与令牌上限中更严的）→ 令牌级限额。
//
// 不变量：
//   - 完整输出永不进入 Hub 的数据库（只存脱敏后的命令、有界预览与元数据）；
//   - 一次运行至多有一个终态（数据库侧用条件更新保证）；
//   - 模型只能操作自己令牌创建的运行；
//   - 任何拒绝都不泄露「令牌无权访问的主机是否存在」。

const (
	execDefaultWait    = 30
	execMaxWait        = 120
	execDefaultOutput  = 64 << 10
	execMaxOutput      = termproto.MaxRunRead
	execMinOutput      = 1 << 10
	execTailBytes      = 4 << 10
	execApprovalTTL    = 10 * time.Minute
	execStartTimeout   = 30 * time.Second
	execCancelWait     = 8 * time.Second
	execHubMaxActive   = 128
	execHubMaxAwaiting = 32
	execRetention      = 30 * 24 * time.Hour
	execLostGrace      = 15 * time.Second
	execMaxLabel       = 256
)

// liveRun 是一次尚未终结的运行在内存里的状态。
type liveRun struct {
	id   string
	done chan struct{} // 终态时关闭

	mu       sync.Mutex
	rec      model.TerminalRun // 记录的权威副本（落库前先改这里）
	frame    termproto.Run     // 待发送 / 已发送的请求（发送后清空命令文本）
	peer     execPeer          // 发送运行请求所用的执行层（Agent 连接或 SSH）
	sentAt   time.Time
	started  bool
	expires  time.Time // 待批准的截止时间
	final    bool
	end      *termproto.RunEnd
	cancelBy string // 已请求取消的来源（user / pause / system …）
}

func (lr *liveRun) snapshot() model.TerminalRun {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.rec
}

func (lr *liveRun) endFrame() *termproto.RunEnd {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.end
}

func (lr *liveRun) agentDone() <-chan struct{} {
	lr.mu.Lock()
	p := lr.peer
	lr.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.gone()
}

// ExecService 实现 termexec.Executor，并为 REST / 事件流提供人类侧的操作。
type ExecService struct {
	h       *Hub
	enabled bool // G1：RELAYMESH_TERMINAL_EXEC=on

	mu     sync.Mutex
	live   map[string]*liveRun
	paused bool

	idemMu  sync.Mutex
	bus     *execBus
	pruneAt time.Time

	// endHook 是运行终结通知的接收方（见 SetRunEndHook）；nil 表示没有接线。
	endHook atomic.Pointer[func(RunEndEvent)]

	// headlessTok 是非交互唤醒的内部身份用的随机串（见 headlessToken）。
	headlessTokOnce sync.Once
	headlessTok     string

	// lostWatch 记录「对账时发现主机上已不存在」的运行，宽限期过后仍未收到结束事件就标记为 lost。
	lostWatch map[string]time.Time

	// watch 读系统发起的非交互运行的输出，把它翻成 Agent 会话的状态（见 run_watch.go）。
	watch *runWatcher
}

var _ termexec.Executor = (*ExecService)(nil)

func newExecService(h *Hub, enabled bool) *ExecService {
	s := &ExecService{
		h:         h,
		enabled:   enabled,
		live:      make(map[string]*liveRun),
		bus:       newExecBus(),
		lostWatch: make(map[string]time.Time),
	}
	s.watch = newRunWatcher(h)
	return s
}

// Executor 返回给 MCP 层使用的执行接口。
func (h *Hub) Executor() termexec.Executor { return h.exec }

// Exec 返回执行服务（REST 与测试使用）。
func (h *Hub) Exec() *ExecService { return h.exec }

// MCPCredentialChanged 通知 Hub：某个 MCP 令牌被修改 / 停用 / 删除 / 重新生成。见 ExecService.MCPCredentialChanged。
func (h *Hub) MCPCredentialChanged(id uint, reason string) {
	if h == nil {
		return
	}
	h.exec.MCPCredentialChanged(h.ctx, id, reason)
	h.files.MCPCredentialChanged(id)
}

func (s *ExecService) isPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

func (s *ExecService) liveRun(id string) *liveRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

// ---------- 闸门 ----------

func execErr(code, format string, args ...any) *termexec.Error {
	return &termexec.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// g1 检查服务端总开关：未开启时静默（不写审计）——一个关着的功能不应该被调用方灌满日志。
func (s *ExecService) g1(ctx context.Context) *termexec.Error {
	if !s.enabled {
		return execErr(termexec.CodeDisabled, "模型执行未在服务端启用（需要运维设置 RELAYMESH_TERMINAL_EXEC=on）")
	}
	if d := s.h.gate.Evaluate(ctx); !d.Open {
		return execErr(termexec.CodeDisabled, "终端功能当前不可用（%s）", d.Reason)
	}
	return nil
}

// deny 构造拒绝错误并写入（限流的）审计。
func (s *ExecService) deny(c termexec.Caller, tool, machine, code, msg string) *termexec.Error {
	return s.denyWith(c, tool, machine, code, msg, "")
}

// denyWith 同 deny，并在审计明细末尾追加 extra（例如被拒绝访问的路径）。限流的键仍是「令牌 + 原因码」。
func (s *ExecService) denyWith(c termexec.Caller, tool, machine, code, msg, extra string) *termexec.Error {
	detail := "tool=" + tool + " reason=" + code
	if extra != "" {
		detail += " " + extra
	}
	s.h.audit.recordLimited(s.h.ctx, strconv.FormatUint(uint64(c.CredentialID), 10)+"|"+code, auditEvent{
		Event: EvExecDenied, Result: ResultDeny, Actor: c.CredentialName, IP: c.RemoteIP,
		MachineCode: machine, ActorKind: ActorModel, Detail: detail,
	})
	return &termexec.Error{Code: code, Message: msg}
}

// prelude 是三个工具共同的前置检查：G1、令牌身份与强度。
func (s *ExecService) prelude(ctx context.Context, c termexec.Caller, tool string) *termexec.Error {
	if e := s.g1(ctx); e != nil {
		return e
	}
	// 只带文件权限的令牌只能看主机清单（terminal_targets）：它需要知道有哪些主机和允许目录，但不能执行命令。
	granted := c.Permissions.Terminal || (tool == "targets" && c.Permissions.TerminalFiles)
	if !granted || c.Credential == nil {
		return s.deny(c, tool, "", termexec.CodeNotAllowed, "此令牌没有终端执行权限")
	}
	if w := config.MCPTokenWeakness(c.TokenString); w != "" {
		return s.deny(c, tool, "", termexec.CodeWeakToken, "令牌强度不足以代表远程执行（"+w+"），请在 Web 的权限页重新生成令牌")
	}
	return nil
}

func blockMessage(code string) string {
	switch code {
	case termexec.CodeHostOff:
		return "管理员尚未允许模型访问这台主机（Web → ExecMesh → 主机 → 模型访问）"
	case termexec.CodePaused:
		return "模型执行已被暂停（全局或这台主机），需要管理员在 Web 上恢复"
	case termexec.CodeHostOffline:
		return "主机当前不在线"
	case termexec.CodeHostLegacy:
		return "这台主机上的 Agent 版本过旧，或会话守护进程不可用，不支持模型执行"
	case termexec.CodeHostDenied:
		return "这台主机在本机策略（--deny-model-exec）下拒绝了一切模型执行"
	case termexec.CodeReadonly:
		return "这台主机（或此令牌）的执行模式是只读，不允许执行命令"
	case termexec.CodeSSHOff:
		return "SSH 主机功能未启用（服务端没有配置有效的 RELAYMESH_SSH_SECRET_KEY）"
	case termexec.CodeSSHUntrusted:
		return "这台 SSH 主机的主机密钥尚未固定，或与已固定的不一致：管理员需要在 Web 主机页核对指纹后重新确认"
	case termexec.CodeSSHTerminalOnly:
		return "这台 SSH 主机经网关主机接入，但登记时没能探测出 POSIX 环境（需要 sh 与 base64），只能当人工终端用，不支持模型执行"
	case termexec.CodeSSHCredMissing:
		return "这台经网关接入的 SSH 主机的登录凭据已不在网关上（凭据只保存在网关自己的存储里）：管理员需要在 Web 主机页重新填写凭据"
	}
	return code
}

// hostBlock 依次检查对这台主机的 G2 / G5 / 在线 / G4，返回第一个不通过的原因码；全部通过返回空串。
func (s *ExecService) hostBlock(rec model.TerminalHost, a execPeer) string {
	switch {
	case rec.ViaGateway() && rec.SSHTermOnly:
		// 探测不出 POSIX 环境的网关主机只能当人工终端用：无论其余开关如何，模型都得到同一个明确的原因（而不是含糊的 host_model_off）。
		return termexec.CodeSSHTerminalOnly
	case !rec.AllowModel:
		return termexec.CodeHostOff
	case s.isPaused() || rec.ModelPaused:
		return termexec.CodePaused
	case rec.IsSSH():
		// SSH 主机没有 G4（本机一票否决）：先过后端自己的闸门，再确认执行层存在。
		if code := s.h.ssh.block(rec); code != "" {
			return code
		}
		if a == nil || !a.usable() {
			return termexec.CodeHostOffline
		}
		return ""
	case a == nil || !a.usable():
		return termexec.CodeHostOffline
	}
	supported, denied, _ := a.execSupport()
	switch {
	case !supported:
		return termexec.CodeHostLegacy
	case denied:
		return termexec.CodeHostDenied
	}
	return ""
}

func modeRank(m string) int {
	switch m {
	case model.TerminalModeReadonly:
		return 3
	case model.TerminalModeApprove:
		return 2
	}
	return 1
}

// effectiveMode 返回主机设置与令牌上限中更严格的执行模式。令牌只能把主机的模式收紧，不能放宽。
func effectiveMode(hostMode, tokenCap string) string {
	if hostMode == "" {
		hostMode = model.TerminalModeAuto
	}
	if tokenCap == "" {
		return hostMode
	}
	if modeRank(tokenCap) > modeRank(hostMode) {
		return tokenCap
	}
	return hostMode
}

// callerHosts 返回此令牌「看得见」的主机：已启用且在令牌白名单内。无权访问的主机对它等同不存在。
func (s *ExecService) callerHosts(ctx context.Context, c termexec.Caller) ([]model.TerminalHost, error) {
	recs, err := s.h.store.ListTerminalHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := recs[:0]
	for _, r := range recs {
		if r.Status == termproto.HostStatusEnabled && c.Credential.AllowsTerminalHost(r.MachineCode) {
			out = append(out, r)
		}
	}
	return out, nil
}

// hostDisplayName 是一台主机对外的名字：管理员写的备注优先，没有备注才用主机名。
// 界面标题、「同名」提示、模型在 terminal_targets 里看到的 name 与 target 的解析，用的都是这同一个口径。
func hostDisplayName(r model.TerminalHost) string { return displayNameOf(r.Remark, r.Hostname) }

// displayNameOf 是显示名的唯一定义：备注优先，没有备注才用主机名。Web 列表（HostView / PendingView）与模型侧共用它。
func displayNameOf(remark, hostname string) string {
	if remark != "" {
		return remark
	}
	return hostname
}

// matchHosts 按「机器码 → 显示名 → 主机名」的顺序找出与 want 匹配的主机下标（大小写不敏感）。
// 主机名只是别名：只有没有任何主机的显示名与 want 相同时才参与匹配，免得一台主机自报的主机名「抢走」另一台主机的备注。
func matchHosts(hosts []model.TerminalHost, want string) []int {
	for i := range hosts {
		if strings.EqualFold(hosts[i].MachineCode, want) {
			return []int{i}
		}
	}
	var byName, byHost []int
	for i := range hosts {
		if n := hostDisplayName(hosts[i]); n != "" && strings.EqualFold(n, want) {
			byName = append(byName, i)
		}
		if hosts[i].Hostname != "" && strings.EqualFold(hosts[i].Hostname, want) {
			byHost = append(byHost, i)
		}
	}
	if len(byName) > 0 {
		return byName
	}
	return byHost
}

// duplicateNames 找出在这批主机里显示名相同的主机（机器码集合）：它们不能靠名字区分，必须用机器码。
func duplicateNames(hosts []model.TerminalHost) map[string]bool {
	count := map[string]int{}
	for _, r := range hosts {
		if n := hostDisplayName(r); n != "" {
			count[strings.ToLower(n)]++
		}
	}
	dup := map[string]bool{}
	for _, r := range hosts {
		if n := hostDisplayName(r); n != "" && count[strings.ToLower(n)] > 1 {
			dup[r.MachineCode] = true
		}
	}
	return dup
}

// resolveTarget 把调用方给的目标解析成唯一的主机：机器码，或唯一的显示名（备注，没有备注时是主机名）；只在令牌可见的主机里找。
func resolveTarget(hosts []model.TerminalHost, want string) (*model.TerminalHost, *termexec.Error) {
	want = strings.TrimSpace(want)
	if want == "" {
		if len(hosts) == 1 {
			return &hosts[0], nil
		}
		return nil, execErr(termexec.CodeBadRequest, "必须指定 target（用 terminal_targets 查看可用主机）")
	}
	matches := matchHosts(hosts, want)
	switch len(matches) {
	case 0:
		return nil, execErr(termexec.CodeHostNotFound, "找不到主机 %q（用 terminal_targets 查看可用主机）", termproto.SanitizeText(want, 64))
	case 1:
		return &hosts[matches[0]], nil
	}
	codes := make([]string, 0, len(matches))
	for _, i := range matches {
		codes = append(codes, hosts[i].MachineCode)
	}
	return nil, execErr(termexec.CodeHostNotFound, "目标 %q 有歧义，请改用机器码：%s", termproto.SanitizeText(want, 64), strings.Join(codes, ", "))
}

// dirAllowed 判断工作目录是否落在白名单目录之内（按路径段前缀匹配；兼容 Windows 风格的盘符与反斜杠）。
// 这是防误操作的便利限制，不是沙箱：命令本身仍可以 cd 到任何地方。
func dirAllowed(cwd string, allowed []string) bool {
	norm := func(p string) string {
		p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
		win := len(p) >= 2 && p[1] == ':'
		p = path.Clean(p)
		if win {
			p = strings.ToLower(p)
		}
		return strings.TrimRight(p, "/")
	}
	c := norm(cwd)
	for _, a := range allowed {
		n := norm(a)
		if n == "" { // 根目录
			return true
		}
		if c == n || strings.HasPrefix(c, n+"/") {
			return true
		}
	}
	return false
}

// ---------- terminal_targets ----------

func (s *ExecService) Targets(ctx context.Context, c termexec.Caller, target string) (any, error) {
	if e := s.prelude(ctx, c, "targets"); e != nil {
		return nil, e
	}
	hosts, err := s.callerHosts(ctx, c)
	if err != nil {
		return nil, execErr(termexec.CodeInternal, "读取主机列表失败")
	}
	want := strings.TrimSpace(target)
	// 「同名」按显示名（备注优先）在这把令牌看得到的主机里判断，与界面、与 target 的解析是同一个口径。
	dup := duplicateNames(hosts)
	var picked map[int]bool
	if want != "" {
		picked = map[int]bool{}
		for _, i := range matchHosts(hosts, want) {
			picked[i] = true
		}
	}
	res := &termexec.TargetsResult{Targets: []termexec.Target{}}
	for i, r := range hosts {
		if picked != nil && !picked[i] {
			continue
		}
		a := s.peerFor(r.MachineCode)
		t := termexec.Target{
			MachineCode:   r.MachineCode,
			Name:          hostDisplayName(r),
			DuplicateName: dup[r.MachineCode],
			Hostname:      r.Hostname,
			OS:            r.OS,
			Arch:          r.Arch,
			Mode:          effectiveMode(r.ExecMode, c.Credential.TerminalModeCap),
			Running:       s.activeFor(c.CredentialID, r.MachineCode),
		}
		if r.IsSSH() {
			t.Backend = model.HostKindSSH
		}
		if a != nil && a.usable() {
			t.Online = !r.IsSSH() || s.h.ssh.reachable(r)
			if _, _, info := a.execSupport(); info != nil {
				t.Shell, t.Home, t.PathSep = info.Shell, info.Home, info.Sep
			}
		}
		if code := s.hostBlock(r, a); code != "" {
			t.Blocked = code
		} else if t.Mode == model.TerminalModeReadonly {
			t.Blocked = termexec.CodeReadonly
		} else {
			t.Usable = true
		}
		if !c.Permissions.Terminal {
			// 只带文件权限：这把令牌不能执行命令，执行侧的可用状态对它没有意义，免得模型误以为能跑命令。
			t.Usable, t.Blocked = false, termexec.CodeNotAllowed
		}
		if s.h.files.offeredTo(c) {
			fa := s.h.onlineAgent(r.MachineCode)
			if code := s.h.files.hostBlock(r, fa); code != "" {
				t.FilesBlocked = code
			} else if code := s.h.files.scopeBlock(c.Credential.TerminalLimits, fa); code != "" {
				t.FilesBlocked = code // 令牌是「整个文件系统」，而这台主机的 Agent 还不支持
			} else {
				t.Files = true
			}
		}
		res.Targets = append(res.Targets, t)
	}
	if l := c.Credential.TerminalLimits; !l.IsZero() {
		res.Limits = &termexec.Limits{
			MaxConcurrent: l.MaxConcurrent, MaxWaitSeconds: l.MaxWaitSeconds, MaxTimeoutSeconds: l.MaxTimeoutSeconds,
			MaxOutputBytes: l.MaxOutputBytes, AllowedDirs: append([]string(nil), l.AllowedDirs...),
			AllowedPaths: append([]string(nil), l.AllowedPaths...), WholeFS: l.AllowWholeFS,
		}
		if l.AllowWholeFS {
			res.Limits.AllowedPaths = nil // 开关优先：保留着的目录设置不起作用，不要让模型误以为只能访问它们
		}
	}
	switch {
	case len(res.Targets) == 0:
		res.Hint = "此令牌没有可用的主机：请管理员在 Web 上为令牌授权主机，并在主机页允许模型访问。"
	case !c.Permissions.Terminal:
		res.Hint = "此令牌只有文件权限，不能执行命令（usable=false 只说明这一点）；files=true 的主机可以使用 terminal_files，files_blocked 给出不可用的原因，limits.allowed_paths 是允许访问的目录（limits.whole_fs=true 表示管理员允许整个文件系统，不限目录）；mode 不是 auto 时 pull_prepare / push_url 不可用。"
	default:
		res.Hint = "usable=false 的主机暂时不能执行，blocked 给出原因；mode=approve 表示每条命令需要用户在 Web 上批准。"
		if s.h.files.offeredTo(c) {
			res.Hint += " files=true 表示 terminal_files 可用于这台主机，limits.allowed_paths 是允许访问的目录（limits.whole_fs=true 表示管理员允许整个文件系统，不限目录）。"
		}
	}
	return res, nil
}

// activeFor 统计某个令牌在某台主机上进行中的运行数（machine 为空表示全部主机）。
func (s *ExecService) activeFor(credID uint, machine string) int {
	s.mu.Lock()
	runs := make([]*liveRun, 0, len(s.live))
	for _, lr := range s.live {
		runs = append(runs, lr)
	}
	s.mu.Unlock()
	n := 0
	for _, lr := range runs {
		rec := lr.snapshot()
		if rec.CredentialID == credID && (machine == "" || rec.Target == machine) && !model.IsFinalRunState(rec.State) {
			n++
		}
	}
	return n
}

// ---------- terminal_exec ----------

var allowedShells = map[string]struct{}{"": {}, "bash": {}, "sh": {}, "zsh": {}, "pwsh": {}, "powershell": {}, "cmd": {}}

// backendOf 返回主机的执行后端名（写入运行记录）。
func backendOf(h model.TerminalHost) string {
	if h.IsSSH() {
		return model.HostKindSSH
	}
	return "agent"
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *ExecService) Exec(ctx context.Context, c termexec.Caller, req termexec.ExecRequest) (any, error) {
	if e := s.prelude(ctx, c, "exec"); e != nil {
		return nil, e
	}

	// ---- 参数 ----
	cmd := req.Command
	switch {
	case strings.TrimSpace(cmd) == "":
		return nil, execErr(termexec.CodeBadRequest, "command 不能为空")
	case len(cmd) > termproto.MaxRunCommandBytes:
		return nil, execErr(termexec.CodeBadRequest, "command 过长（上限 %d 字节）：请拆成多条命令，或先把脚本写入主机上的文件再执行", termproto.MaxRunCommandBytes)
	case !utf8.ValidString(cmd) || strings.ContainsRune(cmd, 0):
		return nil, execErr(termexec.CodeBadRequest, "command 必须是有效的 UTF-8 文本且不能含 NUL 字符")
	}
	shell := strings.ToLower(strings.TrimSpace(req.Shell))
	if _, ok := allowedShells[shell]; !ok {
		return nil, execErr(termexec.CodeBadRequest, "shell 只能是 bash / sh / zsh / pwsh / powershell / cmd，或留空自动选择")
	}
	cwd := strings.TrimSpace(req.Cwd)
	if utf8.RuneCountInString(cwd) > termproto.MaxRunCwdRunes || strings.ContainsAny(cwd, "\x00\r\n") {
		return nil, execErr(termexec.CodeBadRequest, "cwd 无效（过长或含控制字符）")
	}
	if req.TimeoutSeconds < 0 || req.WaitSeconds < 0 || req.MaxOutputBytes < 0 {
		return nil, execErr(termexec.CodeBadRequest, "timeout_seconds / wait_seconds / max_output_bytes 不能为负数")
	}

	// ---- G3 / 目标 / G2 / G5 / 在线 / G4 ----
	hosts, err := s.callerHosts(ctx, c)
	if err != nil {
		return nil, execErr(termexec.CodeInternal, "读取主机列表失败")
	}
	host, terr := resolveTarget(hosts, req.Target)
	if terr != nil {
		return nil, s.deny(c, "exec", "", terr.Code, terr.Message)
	}
	agent := s.peerFor(host.MachineCode)
	if code := s.hostBlock(*host, agent); code != "" {
		return nil, s.deny(c, "exec", host.MachineCode, code, blockMessage(code))
	}
	if agent == nil { // 防御：闸门全部通过时执行层一定存在
		return nil, s.deny(c, "exec", host.MachineCode, termexec.CodeHostOffline, blockMessage(termexec.CodeHostOffline))
	}
	if host.IsSSH() {
		if !sshexec.ValidRemoteShell(shell) {
			return nil, execErr(termexec.CodeBadRequest, "SSH 主机的 shell 只能是 bash / sh / zsh，或留空自动选择")
		}
		if s.h.ssh.activeOn(host.MachineCode) >= sshMaxRunsPerHost {
			return nil, s.deny(c, "exec", host.MachineCode, termexec.CodeBusy, "这台 SSH 主机上同时运行的命令过多，请稍后重试")
		}
	}
	_, _, info := agent.execSupport()

	// ---- 模式 ----
	mode := effectiveMode(host.ExecMode, c.Credential.TerminalModeCap)
	if mode == model.TerminalModeReadonly {
		return nil, s.deny(c, "exec", host.MachineCode, termexec.CodeReadonly, blockMessage(termexec.CodeReadonly))
	}

	// ---- 令牌限额（默认不设限）----
	lim := c.Credential.TerminalLimits
	var adjusted []string
	if len(lim.AllowedDirs) > 0 {
		if cwd == "" && info != nil {
			cwd = info.Home
		}
		if cwd == "" || !dirAllowed(cwd, lim.AllowedDirs) {
			return nil, s.deny(c, "exec", host.MachineCode, termexec.CodeLimit, "工作目录不在此令牌允许的目录范围内："+strings.Join(lim.AllowedDirs, ", "))
		}
	}
	// 超出范围的参数会被收进范围，但不能悄悄收：模型传了「等 7 天」、以为这次调用会一直等，结果 120 秒就返回了；
	// 回执里的 adjusted 让它知道发生了什么、接下来该怎么做（令牌限额造成的调整同样记在这里）。
	wait := execDefaultWait
	if req.WaitSet {
		wait = req.WaitSeconds
	}
	if wait > execMaxWait {
		adjusted = append(adjusted, "wait_seconds="+strconv.Itoa(wait)+" 超过上限 "+strconv.Itoa(execMaxWait)+"，已调整为 "+strconv.Itoa(execMaxWait)+
			"：单次调用最多等这么久；命令没跑完时会返回运行编号，凭它继续查询即可")
	}
	wait = clampInt(wait, 0, execMaxWait)
	if lim.MaxWaitSeconds > 0 && wait > lim.MaxWaitSeconds {
		wait = lim.MaxWaitSeconds
		adjusted = append(adjusted, "wait_seconds 被令牌限额调整为 "+strconv.Itoa(wait))
	}
	timeout := req.TimeoutSeconds
	if timeout == 0 {
		timeout = termproto.DefaultRunTimeoutSec
	}
	if timeout > termproto.MaxRunTimeoutSec {
		adjusted = append(adjusted, "timeout_seconds="+strconv.Itoa(timeout)+" 超过上限 "+strconv.Itoa(termproto.MaxRunTimeoutSec)+"（7 天），已调整为 "+
			strconv.Itoa(termproto.MaxRunTimeoutSec))
	}
	timeout = clampInt(timeout, 1, termproto.MaxRunTimeoutSec)
	if lim.MaxTimeoutSeconds > 0 && timeout > lim.MaxTimeoutSeconds {
		timeout = lim.MaxTimeoutSeconds
		adjusted = append(adjusted, "timeout_seconds 被令牌限额调整为 "+strconv.Itoa(timeout))
	}
	maxOut := req.MaxOutputBytes
	if maxOut == 0 {
		maxOut = execDefaultOutput
	}
	if maxOut < execMinOutput || maxOut > execMaxOutput {
		adjusted = append(adjusted, "max_output_bytes="+strconv.Itoa(maxOut)+" 超出范围 "+strconv.Itoa(execMinOutput)+"-"+strconv.Itoa(execMaxOutput)+"，已调整为 "+
			strconv.Itoa(clampInt(maxOut, execMinOutput, execMaxOutput))+"：其余输出用分页读取")
	}
	maxOut = clampInt(maxOut, execMinOutput, execMaxOutput)
	if lim.MaxOutputBytes > 0 && maxOut > lim.MaxOutputBytes {
		maxOut = max(lim.MaxOutputBytes, execMinOutput)
		adjusted = append(adjusted, "max_output_bytes 被令牌限额调整为 "+strconv.Itoa(maxOut))
	}

	// ---- 归属（自报，仅用于分组与展示）----
	workflow := label(req.WorkflowID, 128)
	desc := label(req.Description, execMaxLabel)
	thread := label(req.Thread, 64)
	var warnings []string
	if c.Hybrid() {
		if workflow == "" {
			warnings = append(warnings, "缺少 workflow_id：这次运行被归入「未归属」，请在后续调用中带上当前会话的 workflow_id")
		}
		if desc == "" {
			warnings = append(warnings, "缺少 description：请用一句话说明这条命令的意图")
		}
	}

	// ---- 幂等 + 容量 + 落库（同一把锁内完成，避免并发重复创建）----
	idemKey := label(req.IdempotencyKey, 96)
	runID := randHex(16)
	now := s.h.now()
	rec := model.TerminalRun{
		RunID:            runID,
		ThreadKey:        threadKeyFor(c, host.MachineCode, thread, workflow),
		Backend:          backendOf(*host),
		ActorKind:        ActorModel,
		CredentialID:     c.CredentialID,
		CredentialName:   termproto.SanitizeText(c.CredentialName, 128),
		ClientName:       termproto.SanitizeText(c.UserAgent, 96),
		RemoteIP:         termproto.SanitizeText(c.RemoteIP, 64),
		OriginHost:       termproto.SanitizeText(c.OriginHost, 128),
		WorkflowID:       workflow,
		Thread:           thread,
		ProjectDirectory: label(req.ProjectDirectory, 256),
		Description:      desc,
		Target:           host.MachineCode,
		Cwd:              termproto.SanitizeText(cwd, 512),
		Shell:            shell,
		Command:          clipBytes(redactSecrets(cmd), 4096),
		CommandSHA256:    commandDigest(cmd),
		Tools:            strings.Join(commandNames(cmd), ","),
		State:            model.RunRunning,
		PolicyMode:       mode,
		TimeoutSec:       timeout,
		IdempotencyKey:   idemKey,
		CreatedAt:        now,
	}
	if mode == model.TerminalModeApprove {
		rec.State = model.RunAwaitingApproval
	}
	lr := &liveRun{
		id:   runID,
		done: make(chan struct{}),
		rec:  rec,
		frame: termproto.Run{
			T: termproto.TRun, ID: runID, Cmd: cmd, Shell: shell, Cwd: cwd, TimeoutSec: timeout,
		},
	}
	if mode == model.TerminalModeApprove {
		lr.expires = now.Add(execApprovalTTL)
	}

	s.idemMu.Lock()
	if idemKey != "" {
		prev, perr := s.h.store.FindTerminalRunByIdempotency(ctx, c.CredentialID, idemKey)
		if perr == nil && prev != nil {
			s.idemMu.Unlock()
			return s.replay(ctx, *prev, wait, maxOut), nil
		}
	}
	if e := s.capacityLocked(c, lim, mode); e != nil {
		s.idemMu.Unlock()
		return nil, s.deny(c, "exec", host.MachineCode, e.Code, e.Message)
	}
	if err := s.h.store.CreateTerminalRun(ctx, &lr.rec); err != nil {
		s.idemMu.Unlock()
		log.Printf("[terminal] 创建运行记录失败: %v", err)
		return nil, execErr(termexec.CodeInternal, "无法记录这次运行")
	}
	s.mu.Lock()
	s.live[runID] = lr
	s.mu.Unlock()
	s.idemMu.Unlock()

	s.h.audit.record(s.h.ctx, auditEvent{
		Event: EvExecStart, Actor: c.CredentialName, IP: c.RemoteIP, MachineCode: host.MachineCode,
		ActorKind: ActorModel, RunID: runID, Detail: "mode=" + mode + " tool=exec" + channelDetail(c),
	})
	s.bus.publishRun(lr.snapshot())

	if mode == model.TerminalModeAuto {
		s.dispatch(lr, agent)
	}

	s.waitDone(ctx, lr, time.Duration(wait)*time.Second)
	res := s.resultFor(ctx, lr.snapshot(), lr.endFrame(), maxOut)
	res.Adjusted, res.Warnings = adjusted, warnings
	return res, nil
}

// capacityLocked 检查 Hub 的保护性上限与令牌的并发限额。调用方持有 idemMu。
func (s *ExecService) capacityLocked(c termexec.Caller, lim model.TerminalLimits, mode string) *termexec.Error {
	s.mu.Lock()
	active, awaiting, mine := 0, 0, 0
	for _, lr := range s.live {
		rec := lr.snapshot()
		active++
		if rec.State == model.RunAwaitingApproval {
			awaiting++
		}
		if rec.CredentialID == c.CredentialID {
			mine++
		}
	}
	s.mu.Unlock()
	switch {
	case lim.MaxConcurrent > 0 && mine >= lim.MaxConcurrent:
		return execErr(termexec.CodeLimit, "此令牌同时进行的运行已达上限（%d）", lim.MaxConcurrent)
	case active >= execHubMaxActive:
		return execErr(termexec.CodeBusy, "服务端同时进行的模型运行过多（%d），请稍后再试", execHubMaxActive)
	case mode == model.TerminalModeApprove && awaiting >= execHubMaxAwaiting:
		return execErr(termexec.CodeBusy, "待批准的运行过多（%d），请先处理已有的待批准项", execHubMaxAwaiting)
	}
	return nil
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// waitDone 等待运行终结，最多 d；调用方断开、Agent 掉线或到期都会提前返回。
func (s *ExecService) waitDone(ctx context.Context, lr *liveRun, d time.Duration) bool {
	select {
	case <-lr.done:
		return true
	default:
	}
	if d <= 0 {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-lr.done:
		return true
	case <-t.C:
	case <-ctx.Done():
	case <-lr.agentDone():
	}
	return false
}

// replay 返回幂等键命中的已有运行的当前状态。
func (s *ExecService) replay(ctx context.Context, prev model.TerminalRun, wait, maxOut int) *termexec.RunResult {
	if lr := s.liveRun(prev.RunID); lr != nil {
		s.waitDone(ctx, lr, time.Duration(wait)*time.Second)
		res := s.resultFor(ctx, lr.snapshot(), lr.endFrame(), maxOut)
		res.Warnings = append(res.Warnings, "幂等键命中：返回的是此前同一次请求创建的运行，没有重复执行")
		return res
	}
	res := s.resultFor(ctx, prev, nil, maxOut)
	res.Warnings = append(res.Warnings, "幂等键命中：返回的是此前同一次请求创建的运行，没有重复执行")
	return res
}

// channelDetail 在经能力 URL 进来的调用的审计详情里留下线索；普通通道不加任何内容。
func channelDetail(c termexec.Caller) string {
	if c.Channel != "" {
		return " channel=" + c.Channel
	}
	return ""
}
