package terminal

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// FileService 实现 termexec.FileExecutor：模型经 terminal_files 查看（stat / hash）、拉取（pull_prepare）与写入（push_url）
// 已授权主机上令牌允许目录内的文件。
//
// 闸门流水线与 ExecService 同源，在其后追加文件专属的几道（任何一道不通过都不会向主机发出请求）：
//
//	RELAYMESH_TERMINAL_FILES=on ∧ G1（模型执行总开关 + 终端总闸）
//	→ 令牌：文件权限位 + 强度 + 非空 allowed_paths
//	→ G3 令牌主机白名单 → G2 主机允许模型 → G5 暂停 → 在线 → 主机具备文件能力 → G4 本机一票否决
//	→ 执行模式：文件传输（拉取 / 写入）要求主机与令牌的有效模式都是 auto
//	→ 并发
//
// 路径授权（允许目录、符号链接、特殊文件、大小）由 Agent 强制执行——Hub 只转发令牌的 allowed_paths，
// 并把 Agent 的回包当作不可信输入校验。文件内容和票据从不进入数据库、审计与日志。
type FileService struct {
	h       *Hub
	enabled bool // RELAYMESH_TERMINAL_FILES=on
	tickets *fileTickets

	mu        sync.Mutex
	perCred   map[uint]int
	downloads int
	perHost   map[string]int
}

var _ termexec.FileExecutor = (*ExecService)(nil)

func newFileService(h *Hub, enabled bool) *FileService {
	return &FileService{
		h:       h,
		enabled: enabled,
		tickets: newFileTickets(h.now),
		perCred: make(map[uint]int),
		perHost: make(map[string]int),
	}
}

// Files 返回文件服务（REST 与测试使用）。
func (h *Hub) Files() *FileService { return h.files }

// Files 让 Hub.Executor() 同时满足 termexec.FileExecutor：MCP 层只需要一个执行器。
func (s *ExecService) Files(ctx context.Context, c termexec.Caller, req termexec.FileRequest) (any, error) {
	return s.h.files.Files(ctx, c, req)
}

// 审计事件名（文件内容、票据与网址的查询串从不写入）。
const (
	EvFileOp       = "file_op"       // stat / hash
	EvFilePull     = "file_pull"     // 签发了一张拉取票据
	EvFileDownload = "file_download" // 票据被使用，文件被下载
	EvFilePush     = "file_push"     // push_url 写入了文件
)

const (
	fileMaxInflightPerCred  = 4
	fileMaxDownloads        = 4
	fileMaxDownloadsPerHost = 2
	fileAuditPath           = 200
)

var fileOps = map[string]string{
	"stat":         termproto.FileOpStat,
	"hash":         termproto.FileOpHash,
	"pull_prepare": termproto.FileOpOpen,
	"push_url":     termproto.FileOpPushURL,
}

func clipPath(p string, n int) string { return termproto.SanitizeText(p, n) }

func fileBlockMessage(code string) string {
	switch code {
	case termexec.CodeHostLegacy:
		return "这台主机上的 Agent 不支持文件能力（版本过旧、会话守护进程不可用，或是 SSH 主机）"
	case termexec.CodeHostDenied:
		return "这台主机在本机策略（--deny-model-exec）下拒绝了一切模型访问，包括文件"
	}
	return blockMessage(code)
}

// hostBlock 依次检查对这台主机的 G2 / G5 / 在线 / 文件能力 / G4，返回第一个不通过的原因码；全部通过返回空串。
func (s *FileService) hostBlock(rec model.TerminalHost, a *agentConn) string {
	switch {
	case !rec.AllowModel:
		return termexec.CodeHostOff
	case s.h.exec.isPaused() || rec.ModelPaused:
		return termexec.CodePaused
	case rec.IsSSH():
		return termexec.CodeHostLegacy // SSH 主机没有文件能力
	case a == nil || !a.isEnabled():
		return termexec.CodeHostOffline
	}
	supported, denied := a.filesSupport()
	switch {
	case !supported:
		return termexec.CodeHostLegacy
	case denied:
		return termexec.CodeHostDenied
	}
	return ""
}

// auditScopeWholeFS 是写进文件类审计明细的标记：这次访问来自开启了「允许整个文件系统」的令牌，没有目录边界。
const auditScopeWholeFS = "scope=whole_fs"

// wholeFSLegacyMessage 是令牌被授予「允许整个文件系统」、而目标主机的 Agent 还不认识它时给模型的说明。
const wholeFSLegacyMessage = "这台主机上的 Agent 版本较旧，还不支持「允许整个文件系统」，在升级 Agent 之前这把令牌无法访问这台主机上的文件"

// scopeBlock 检查令牌的文件范围对这台主机是否成立：「允许整个文件系统」需要 Agent 认识哨兵，
// 旧版 Agent 会拒绝一切路径，所以在发请求之前就给出明确的原因（而不是让模型看到莫名其妙的越界错误）。
// 返回 host_legacy 或空串。
func (s *FileService) scopeBlock(l model.TerminalLimits, a *agentConn) string {
	if l.AllowWholeFS && (a == nil || !a.filesWholeFS()) {
		return termexec.CodeHostLegacy
	}
	return ""
}

// fileRoots 是下发给 Agent 的访问范围：「允许整个文件系统」时只有哨兵，否则是令牌的允许目录。
// 开关优先于目录：开关打开时，保留着的目录设置不起作用。
func fileRoots(l model.TerminalLimits) []string {
	if l.AllowWholeFS {
		return []string{termproto.FileRootAll}
	}
	return append([]string(nil), l.AllowedPaths...)
}

// offeredTo 判断服务端是否向这个令牌提供文件能力（开关 + 权限位 + 访问范围），用于 terminal_targets 的提示。
func (s *FileService) offeredTo(c termexec.Caller) bool {
	return s.enabled && c.Credential != nil && c.Permissions.TerminalFiles && c.Credential.TerminalLimits.HasFileScope()
}

// prelude 是文件工具的前置检查：文件开关、G1、令牌权限与强度、允许目录。
func (s *FileService) prelude(ctx context.Context, c termexec.Caller, action string) *termexec.Error {
	if !s.enabled {
		return execErr(termexec.CodeFilesOff, "文件能力未在服务端启用（需要运维设置 RELAYMESH_TERMINAL_FILES=on）")
	}
	if e := s.h.exec.g1(ctx); e != nil {
		return e
	}
	tool := "files:" + action
	switch {
	case !c.Permissions.TerminalFiles || c.Credential == nil:
		return s.h.exec.deny(c, tool, "", termexec.CodeNotAllowed, "此令牌没有文件权限")
	case !c.Credential.TerminalLimits.HasFileScope():
		return s.h.exec.deny(c, tool, "", termexec.CodeNotAllowed, "此令牌没有配置允许访问的目录（allowed_paths），也没有开启「允许整个文件系统」")
	}
	if w := config.MCPTokenWeakness(c.TokenString); w != "" {
		return s.h.exec.deny(c, tool, "", termexec.CodeWeakToken, "令牌强度不足以代表远程访问（"+w+"），请在 Web 的权限页重新生成令牌")
	}
	return nil
}

func (s *FileService) acquire(credID uint) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perCred[credID] >= fileMaxInflightPerCred {
		return nil, false
	}
	s.perCred[credID]++
	return func() {
		s.mu.Lock()
		if s.perCred[credID]--; s.perCred[credID] <= 0 {
			delete(s.perCred, credID)
		}
		s.mu.Unlock()
	}, true
}

// transportError 把传输层问题（超时、断线、请求放不进帧）归入稳定错误码。
func transportError(err error) *termexec.Error {
	switch {
	case errors.Is(err, errFileTimeout), errors.Is(err, context.DeadlineExceeded):
		return execErr(termproto.FileErrTimeout, "主机没有在期限内应答")
	case errors.Is(err, errFileTooMany):
		return execErr(termexec.CodeBusy, "对这台主机的文件请求过多，请稍后重试")
	case errors.Is(err, errAgentClosed), errors.Is(err, errAgentNotActive):
		return execErr(termexec.CodeHostOffline, "主机当前不在线")
	case errors.Is(err, errFileTooBig):
		return execErr(termexec.CodeBadRequest, "请求过大（路径、网址或允许目录的总长度超过限制）")
	case errors.Is(err, context.Canceled):
		return execErr(termexec.CodeInternal, "请求已取消")
	}
	return execErr(termexec.CodeInternal, "与主机通信失败")
}

// agentFailure 把 Agent 报告的失败转成带稳定错误码的错误，并按性质写审计：
// 与授权边界相关的失败（越界、网址被拒、本机否决）走限流的「被拒绝」审计，其余是普通的文件操作失败。
func (s *FileService) agentFailure(c termexec.Caller, action, machine, path string, res termproto.FileRes) *termexec.Error {
	code := res.Code
	switch code {
	case termproto.FileErrDenied:
		code = termexec.CodeHostDenied
	case termproto.FileErrNotAllowed:
		code = termexec.CodePathDenied
	}
	msg := res.Msg
	if msg == "" {
		msg = code
	}
	switch res.Code {
	case termproto.FileErrNotAllowed, termproto.FileErrURLBlocked, termproto.FileErrDenied, termproto.FileErrBadPath:
		return s.deny(c, action, machine, code, msg, path)
	}
	s.auditOp(c, EvFileOp, ResultFail, machine, "action="+action+" path="+clipPath(path, fileAuditPath)+" code="+res.Code)
	return &termexec.Error{Code: code, Message: msg}
}

// auditOp 写入文件类审计。stat / hash 这类可被高频调用的事件走限流，其余照常写。
func (s *FileService) auditOp(c termexec.Caller, event, result, machine, detail string) {
	if c.Credential != nil && c.Credential.TerminalLimits.AllowWholeFS {
		detail += " " + auditScopeWholeFS // 取证时一眼看出这次访问没有目录边界
	}
	e := auditEvent{
		Event: event, Result: result, Actor: c.CredentialName, IP: c.RemoteIP,
		MachineCode: machine, ActorKind: ActorModel, Detail: detail + channelDetail(c),
	}
	if event == EvFileOp {
		s.h.audit.recordLimited(s.h.ctx, strconv.FormatUint(uint64(c.CredentialID), 10)+"|"+machine, e)
		return
	}
	s.h.audit.record(s.h.ctx, e)
}

// deny 写入「文件操作被拒绝」的（限流的）审计，明细里带上被请求的路径（取证用：模型到底想碰哪里）；不含网址、内容或票据。
func (s *FileService) deny(c termexec.Caller, action, machine, code, msg, path string) *termexec.Error {
	return s.h.exec.denyWith(c, "files:"+action, machine, code, msg, "path="+clipPath(path, fileAuditPath))
}

// Files 是 terminal_files 的入口。
func (s *FileService) Files(ctx context.Context, c termexec.Caller, req termexec.FileRequest) (any, error) {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	op, known := fileOps[action]
	if !known {
		return nil, execErr(termexec.CodeBadRequest, "action 只能是 stat / hash / pull_prepare / push_url")
	}
	if e := s.prelude(ctx, c, action); e != nil {
		return nil, e
	}

	// ---- 参数形状（权威的路径授权在 Agent 一侧；这里只拦下明显无效的请求）----
	fr := termproto.FileReq{Op: op, Roots: fileRoots(c.Credential.TerminalLimits), Path: req.Path}
	switch action {
	case "hash":
		fr.Max = termproto.MaxFileHash
	case "pull_prepare":
		fr.Max = termproto.MaxFilePull
	case "push_url":
		fr.Max = termproto.MaxFilePush
		fr.URL, fr.Overwrite, fr.SHA256 = strings.TrimSpace(req.URL), req.Overwrite, strings.ToLower(strings.TrimSpace(req.SHA256))
		u, err := url.Parse(fr.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, execErr(termexec.CodeBadRequest, "url 必须是 http 或 https 网址")
		}
	}
	if msg := fr.Problem(); msg != "" {
		return nil, execErr(termexec.CodeBadRequest, "%s", msg)
	}

	// ---- G3 / 目标 / G2 / G5 / 在线 / 文件能力 / G4 ----
	hosts, err := s.h.exec.callerHosts(ctx, c)
	if err != nil {
		return nil, execErr(termexec.CodeInternal, "读取主机列表失败")
	}
	host, terr := resolveTarget(hosts, req.Target)
	if terr != nil {
		return nil, s.deny(c, action, "", terr.Code, terr.Message, req.Path)
	}
	agent := s.h.onlineAgent(host.MachineCode)
	if code := s.hostBlock(*host, agent); code != "" {
		return nil, s.deny(c, action, host.MachineCode, code, fileBlockMessage(code), req.Path)
	}
	if code := s.scopeBlock(c.Credential.TerminalLimits, agent); code != "" {
		return nil, s.deny(c, action, host.MachineCode, code, wholeFSLegacyMessage, req.Path)
	}

	// ---- 模式：文件传输要求 auto ----
	if action == "pull_prepare" || action == "push_url" {
		if mode := effectiveMode(host.ExecMode, c.Credential.TerminalModeCap); mode != model.TerminalModeAuto {
			return nil, s.deny(c, action, host.MachineCode, termexec.CodeModeDenied,
				"文件传输要求这台主机与令牌的执行模式都是 auto（当前为 "+mode+"；文件传输暂不支持逐次批准）；stat / hash 不受影响", req.Path)
		}
	}

	// ---- 并发 ----
	release, ok := s.acquire(c.CredentialID)
	if !ok {
		return nil, s.deny(c, action, host.MachineCode, termexec.CodeBusy, "此令牌同时进行的文件操作过多，请等前面的完成", req.Path)
	}
	defer release()

	switch action {
	case "stat":
		return s.doStat(ctx, c, agent, host.MachineCode, fr)
	case "hash":
		return s.doHash(ctx, c, agent, host.MachineCode, fr)
	case "pull_prepare":
		return s.doPullPrepare(ctx, c, agent, host.MachineCode, fr)
	}
	return s.doPush(ctx, c, agent, host.MachineCode, fr)
}

func (s *FileService) doStat(ctx context.Context, c termexec.Caller, a *agentConn, machine string, fr termproto.FileReq) (any, error) {
	res, err := a.fileCall(ctx, fr, fileCallTimeout)
	if err != nil {
		return nil, transportError(err)
	}
	if !res.OK {
		return nil, s.agentFailure(c, "stat", machine, fr.Path, res)
	}
	if res.Stat == nil {
		return nil, execErr(termexec.CodeInternal, "主机返回了无效的应答")
	}
	out := termexec.FileStatResult{Target: machine, Path: clipPath(fr.Path, 512), Exists: res.Stat.Exists}
	if res.Stat.Exists {
		out.Type, out.Size, out.Mode = res.Stat.Type, res.Stat.Size, res.Stat.Mode
		if res.Stat.MTime > 0 {
			out.MTime = time.Unix(res.Stat.MTime, 0).UTC().Format(time.RFC3339)
		}
	}
	s.auditOp(c, EvFileOp, ResultOK, machine, "action=stat path="+clipPath(fr.Path, fileAuditPath))
	return out, nil
}

func (s *FileService) doHash(ctx context.Context, c termexec.Caller, a *agentConn, machine string, fr termproto.FileReq) (any, error) {
	res, err := a.fileCall(ctx, fr, fileHashTimeout)
	if err != nil {
		return nil, transportError(err)
	}
	if !res.OK {
		return nil, s.agentFailure(c, "hash", machine, fr.Path, res)
	}
	if !termproto.ValidSHA256Hex(res.SHA256) || res.Size < 0 || res.Size > termproto.MaxFileHash {
		return nil, execErr(termexec.CodeInternal, "主机返回了无效的应答")
	}
	s.auditOp(c, EvFileOp, ResultOK, machine, "action=hash path="+clipPath(fr.Path, fileAuditPath)+" size="+strconv.FormatInt(res.Size, 10))
	return termexec.FileHashResult{Target: machine, Path: clipPath(fr.Path, 512), SHA256: res.SHA256, Size: res.Size}, nil
}

func (s *FileService) doPullPrepare(ctx context.Context, c termexec.Caller, a *agentConn, machine string, fr termproto.FileReq) (any, error) {
	res, err := a.fileCall(ctx, fr, fileCallTimeout)
	if err != nil {
		return nil, transportError(err)
	}
	if !res.OK {
		return nil, s.agentFailure(c, "pull_prepare", machine, fr.Path, res)
	}
	// 这里只是验证路径、类型与大小：句柄立即放掉，真正的读取发生在票据被使用时（那时会重新授权）。
	a.fileClose(res.Handle)
	if res.Size < 0 || res.Size > termproto.MaxFilePull {
		return nil, execErr(termexec.CodeInternal, "主机返回了无效的应答")
	}
	token, ok := s.tickets.issue(fileTicket{
		CredentialID: c.CredentialID, Credential: c.CredentialName, Machine: machine,
		Path: fr.Path, Roots: fr.Roots, Size: res.Size, IP: c.RemoteIP,
	})
	if !ok {
		return nil, s.deny(c, "pull_prepare", machine, termexec.CodeBusy, "未使用的下载票据过多，请先使用或等待它们过期", fr.Path)
	}
	s.auditOp(c, EvFilePull, ResultOK, machine, "path="+clipPath(fr.Path, fileAuditPath)+" size="+strconv.FormatInt(res.Size, 10))
	return termexec.FilePullResult{
		Target: machine, Path: clipPath(fr.Path, 512), URL: "/files/dl/" + token, Method: "GET",
		ExpiresIn: int(fileTicketTTL.Seconds()), Size: res.Size,
		Hint: "用 GET 请求这个地址（补全为与 MCP 端点相同的服务端地址）即可下载；票据只能用一次，" +
			strconv.Itoa(int(fileTicketTTL.Seconds())) + " 秒内有效，且它就是凭证——不要写进日志或对话记录。" +
			"需要校验完整性时，对同一路径调用 action=hash 并与下载结果比对。",
	}, nil
}

func (s *FileService) doPush(ctx context.Context, c termexec.Caller, a *agentConn, machine string, fr termproto.FileReq) (any, error) {
	res, err := a.fileCall(ctx, fr, filePushTimeout)
	if err != nil {
		return nil, transportError(err)
	}
	if !res.OK {
		s.auditOp(c, EvFilePush, ResultFail, machine, "path="+clipPath(fr.Path, fileAuditPath)+" code="+res.Code)
		return nil, s.agentFailure(c, "push_url", machine, fr.Path, res)
	}
	if !termproto.ValidSHA256Hex(res.SHA256) || res.Size < 0 || res.Size > termproto.MaxFilePush {
		return nil, execErr(termexec.CodeInternal, "主机返回了无效的应答")
	}
	s.auditOp(c, EvFilePush, ResultOK, machine, "path="+clipPath(fr.Path, fileAuditPath)+" size="+strconv.FormatInt(res.Size, 10)+
		" sha256="+res.SHA256[:12]+" src="+clipPath(res.Source, 120))
	return termexec.FilePushResult{
		Target: machine, Path: clipPath(fr.Path, 512), Size: res.Size, SHA256: res.SHA256,
		Status: res.Status, Source: res.Source, Redirects: res.Redirects,
	}, nil
}

// MCPCredentialChanged 在令牌被修改 / 停用 / 删除 / 重新生成时作废它名下的全部拉取票据。
func (s *FileService) MCPCredentialChanged(id uint) int {
	if s == nil || id == 0 {
		return 0
	}
	return s.tickets.revokeCredential(id)
}
