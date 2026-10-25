package mcp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
)

// MaxRequestBytes 是一个 MCP 请求体的大小上限。最大的合法请求是带长摘要的 interactive_feedback，远小于此。
const MaxRequestBytes = 8 << 20

// tokenEqual 用常量时间比较令牌，避免通过响应时间一个字节一个字节地猜出环境变量里的令牌。
func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type sessionWaiterItem struct {
	ch         chan *model.FeedbackSession
	startedAt  time.Time
	timeoutSec int
	// extra / lastTick：用户在场计时（#121）。extra 是用户「+1 分钟」与「正在输入」累计加给这次挂起的时间；
	// lastTick 是最近一次「正在输入」心跳，10 秒内算还在输入。
	extra    time.Duration
	lastTick time.Time
}

type Server struct {
	cfg                   *config.Config
	store                 *store.Store
	waitersMu             sync.Mutex
	waiters               map[string][]sessionWaiterItem
	lastKeepaliveMu       sync.Mutex
	lastKeepalives        map[string]time.Time
	lastKeepalivePrunedAt time.Time // 上次清理过期空回执记录的时刻
	onUpdate              func(eventType string, data any)

	usageMu       sync.Mutex
	usageLastSeen map[uint]time.Time // 凭据 ID -> 上次写入「最近使用」的时间

	// stdio 主进程的本机转发入口（#190，见 local_proxy.go）：钥匙为空 = 关闭（常驻服务 / Docker / 生产 Hub 永远为空）。
	localProxyMu    sync.RWMutex
	localProxyKey   string
	localProxyGuard localProxyGuard

	termMu   sync.RWMutex
	termExec termexec.Executor // 终端执行能力；终端功能不可用时为 nil

	presetRunner PresetRunner // 项目管理 Agent 运行预设（preset_run，#179）；终端功能不可用时为 nil（同样由 termMu 保护）

	requestNotifierMu sync.RWMutex
	requestNotifier   RequestNotifier // 通知闸的出口之一：阻塞待答（有人在等你）；未接线时为 nil
	groupNotifier     GroupNotifier   // 群聊里 Agent 发言的通知出口；未接线时为 nil
	eventNotifier     EventNotifier   // 通知闸的出口之二：没有人在等的来源（notice 文本、async_ask 卡片）；未接线时为 nil
}

// RequestNotifier 在智能体发起一条新的反馈请求（需要人来回复）时被调用，把提醒交给统一通知闸（浏览器 SSE、手机上的 ntfy 等）。
// 实现必须立即返回（不能在里面做网络请求），并且要自行复制需要的字段——会话对象随后还会被修改。
type RequestNotifier interface {
	NotifyRequest(s *model.FeedbackSession)
}

// EventNotifier 接收「没有人在等」的通知来源：非阻塞的 notice 文本（interactive_feedback 的 wait:false）与 async_ask 卡片。
// 它们是否真的打扰你由通知闸按偏好（免打扰 / 会话静音）决定。约束同 RequestNotifier：立即返回、自行复制字段。
type EventNotifier interface {
	NotifyNotice(s *model.FeedbackSession)
	NotifyAsk(item *model.ConfirmItem)
}

// GroupNotice 是群里 Agent 发言的通知素材：交给通知闸决定打不打扰你。
type GroupNotice struct {
	GroupID        string
	GroupTitle     string
	MessageID      string
	SenderLabel    string
	Text           string
	MentionsHuman  bool // 点名了你（或 @all）：穿透免打扰
	CoolingStarted bool // 这条触发了群冷却：穿透通知你
}

// GroupNotifier 接收群聊里 Agent 发言的通知出口。实现必须立即返回、自行复制字段。
type GroupNotifier interface {
	NotifyGroup(n GroupNotice)
}

// SetGroupNotifier 接入群聊通知出口；启动时调用一次，传 nil 表示关闭。
func (s *Server) SetGroupNotifier(n GroupNotifier) {
	s.requestNotifierMu.Lock()
	s.groupNotifier = n
	s.requestNotifierMu.Unlock()
}

func (s *Server) notifyGroupMessage(n GroupNotice) {
	s.requestNotifierMu.RLock()
	g := s.groupNotifier
	s.requestNotifierMu.RUnlock()
	if g != nil {
		g.NotifyGroup(n)
	}
}

// SetRequestNotifier 接入新请求提醒；启动时调用一次，传 nil 表示关闭。
func (s *Server) SetRequestNotifier(n RequestNotifier) {
	s.requestNotifierMu.Lock()
	s.requestNotifier = n
	s.requestNotifierMu.Unlock()
}

// SetEventNotifier 接入 notice / async_ask 的通知出口；启动时调用一次，传 nil 表示关闭。
func (s *Server) SetEventNotifier(n EventNotifier) {
	s.requestNotifierMu.Lock()
	s.eventNotifier = n
	s.requestNotifierMu.Unlock()
}

func (s *Server) notifyNotice(sess *model.FeedbackSession) {
	s.requestNotifierMu.RLock()
	n := s.eventNotifier
	s.requestNotifierMu.RUnlock()
	if n != nil && sess != nil {
		n.NotifyNotice(sess)
	}
}

func (s *Server) notifyAsk(item *model.ConfirmItem) {
	s.requestNotifierMu.RLock()
	n := s.eventNotifier
	s.requestNotifierMu.RUnlock()
	if n != nil && item != nil {
		n.NotifyAsk(item)
	}
}

func (s *Server) notifyNewRequest(sess *model.FeedbackSession) {
	s.requestNotifierMu.RLock()
	n := s.requestNotifier
	s.requestNotifierMu.RUnlock()
	if n != nil && sess != nil {
		n.NotifyRequest(sess)
	}
}

func NewServer(cfg *config.Config, st *store.Store, onUpdate func(eventType string, data any)) *Server {
	return &Server{
		cfg:            cfg,
		store:          st,
		waiters:        make(map[string][]sessionWaiterItem),
		lastKeepalives: make(map[string]time.Time),
		onUpdate:       onUpdate,
		usageLastSeen:  make(map[uint]time.Time),
	}
}

// touchCredentialUsage 记录凭据的最近使用时间与来源，同一凭据每分钟最多写库一次。
func (s *Server) touchCredentialUsage(ctx context.Context, credID uint, ip string) {
	if credID == 0 || s.store == nil {
		return
	}
	now := time.Now()
	s.usageMu.Lock()
	if last, ok := s.usageLastSeen[credID]; ok && now.Sub(last) < credentialUsageInterval {
		s.usageMu.Unlock()
		return
	}
	s.usageLastSeen[credID] = now
	s.usageMu.Unlock()
	_ = s.store.TouchCredentialUsage(ctx, credID, ip, now)
}

func (s *Server) registerSessionWaiter(sessionID string, timeoutSec int) chan *model.FeedbackSession {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()

	s.lastKeepaliveMu.Lock()
	delete(s.lastKeepalives, sessionID)
	s.lastKeepaliveMu.Unlock()

	ch := make(chan *model.FeedbackSession, 1)
	item := sessionWaiterItem{
		ch:         ch,
		startedAt:  time.Now(),
		timeoutSec: timeoutSec,
	}
	s.waiters[sessionID] = append(s.waiters[sessionID], item)
	return ch
}

func (s *Server) unregisterSessionWaiter(sessionID string, ch chan *model.FeedbackSession) {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()

	list := s.waiters[sessionID]
	for i, item := range list {
		if item.ch == ch {
			s.waiters[sessionID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(s.waiters[sessionID]) == 0 {
		delete(s.waiters, sessionID)
	}
}

const (
	// keepaliveRetention 空回执时间在内存里最多保留多久；超过的条目在清理时丢弃。
	keepaliveRetention = 7 * 24 * time.Hour
	// keepalivePruneEvery 两次清理之间的最短间隔，避免每次记录都扫一遍。
	keepalivePruneEvery = time.Hour
)

// RecordKeepaliveResponse 记下「智能体刚发出一次空回执（或放弃了这次长轮询）」的时刻。
// 前端据此显示「等待」计时：包括用户在智能体休眠期间提交回复之后——回复提交并不会清掉这个时刻，
// 要等智能体真正取走回复（markConsumedAndBroadcast）才忘掉它。
func (s *Server) RecordKeepaliveResponse(sessionID string) {
	s.lastKeepaliveMu.Lock()
	defer s.lastKeepaliveMu.Unlock()
	now := time.Now()
	s.lastKeepalives[sessionID] = now
	s.pruneKeepalivesLocked(now)
}

// pruneKeepalivesLocked 顺手清理很久以前的条目，让这张表不会随会话数无限增长（调用方须持有 lastKeepaliveMu）。
func (s *Server) pruneKeepalivesLocked(now time.Time) {
	if now.Sub(s.lastKeepalivePrunedAt) < keepalivePruneEvery {
		return
	}
	s.lastKeepalivePrunedAt = now
	for id, t := range s.lastKeepalives {
		if now.Sub(t) > keepaliveRetention {
			delete(s.lastKeepalives, id)
		}
	}
}

// forgetKeepalive 忘掉某个会话的空回执时刻（智能体已取走回复后调用）。
func (s *Server) forgetKeepalive(sessionID string) {
	s.lastKeepaliveMu.Lock()
	delete(s.lastKeepalives, sessionID)
	s.lastKeepaliveMu.Unlock()
}

func (s *Server) GetLastKeepaliveTime(sessionID string) *time.Time {
	s.lastKeepaliveMu.Lock()
	defer s.lastKeepaliveMu.Unlock()
	if t, ok := s.lastKeepalives[sessionID]; ok {
		return &t
	}
	return nil
}

func (s *Server) HasActiveWaiter(sessionID string) bool {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()
	list, ok := s.waiters[sessionID]
	return ok && len(list) > 0
}

func (s *Server) GetActiveWaiterInfo(sessionID string) (bool, *time.Time, int) {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()
	list, ok := s.waiters[sessionID]
	if !ok || len(list) == 0 {
		return false, nil, 0
	}
	latest := list[len(list)-1]
	startedAt := latest.startedAt
	return true, &startedAt, latest.timeoutSec + int(latest.extra/time.Second)
}

func (s *Server) NotifySessionCompleted(sess *model.FeedbackSession) {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()

	if sess == nil {
		return
	}

	// 注意：这里不清空「最近一次空回执」的时刻。用户在智能体休眠期间提交回复后，界面仍要显示「等待」，
	// 并且接着从那次空回执算起，才看得出智能体离开了多久；时刻在智能体取走回复时才忘掉（markConsumedAndBroadcast）。

	if list, ok := s.waiters[sess.ID]; ok {
		for _, item := range list {
			select {
			case item.ch <- sess:
			default:
			}
		}
	}
	s.notifySessionUpdate(sess.ID)
}

func (s *Server) notifySessionUpdate(sessionID string) {
	if s.onUpdate != nil {
		s.onUpdate("session_update", map[string]string{"session_id": sessionID})
	}
}

func (s *Server) notifyTaskUpdate(taskID string) {
	if s.onUpdate != nil {
		s.onUpdate("task_update", map[string]string{"task_id": taskID})
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Server Info / Health
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":    "RelayMesh Streamable MCP Server",
			"version": s.cfg.Version,
			"status":  "running",
		})
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 能力 URL（/mcp/c/<令牌>）自成一条通道：只认路径里的令牌，请求头 / 查询串里的凭据一律忽略。
	tokenString := ""
	capSecret, viaCapability := capabilitySecretFromPath(r.URL.Path)
	if viaCapability {
		tokenString = capSecret
	} else {
		// 提取 Token（优先级：1. Authorization Header -> 2. URL Query ?token=xxx -> 3. URL Path /mcp/token/xxx 或 /mcp/xxx）
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		} else if authHeader != "" {
			tokenString = authHeader
		}
		if tokenString == "" {
			tokenString = r.URL.Query().Get("token")
		}
		if tokenString == "" {
			// 支持 Path 路径嵌入 Token，如 /mcp/token/xxx 或 /mcp/xxx
			path := strings.TrimPrefix(r.URL.Path, "/mcp")
			path = strings.Trim(path, "/")
			if path != "" {
				if strings.HasPrefix(path, "token/") {
					tokenString = strings.TrimPrefix(path, "token/")
				} else if !strings.Contains(path, "/") {
					tokenString = path
				}
			}
		}
	}

	remoteIP := requestClientIP(r)
	var credCtx *CredentialContext
	var err error
	if viaCapability {
		credCtx, err = s.resolveCapability(WithClientIP(r.Context(), remoteIP), tokenString)
	} else {
		credCtx, err = s.resolveCredential(WithClientIP(r.Context(), remoteIP), tokenString, r)
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if !s.cfg.OAuthOff && tokenString == "" && !viaCapability {
			if v := oauth.WWWAuthenticateValue(s.cfg, s.store, r); v != "" {
				w.Header().Set("WWW-Authenticate", v)
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   &rpcError{Code: -32000, Message: err.Error()},
		})
		return
	}

	// 提取 URL Query 或 Header 中的主机名（用于客户端自报主机身份，如 ?hostname=wsl 或 X-Host-Name: wsl）
	queryHostName := strings.TrimSpace(r.URL.Query().Get("hostname"))
	if queryHostName == "" {
		queryHostName = strings.TrimSpace(r.URL.Query().Get("host_name"))
	}
	if queryHostName == "" {
		queryHostName = strings.TrimSpace(r.Header.Get("X-Host-Name"))
	}
	if queryHostName == "" {
		queryHostName = strings.TrimSpace(r.Header.Get("X-Hostname"))
	}
	if queryHostName != "" && credCtx != nil {
		credCtx.HostName = queryHostName
	}

	if credCtx != nil {
		if credCtx.TokenString == "" {
			credCtx.TokenString = tokenString
		}
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		credCtx.BaseURL = fmt.Sprintf("%s://%s", scheme, r.Host)
		credCtx.RemoteIP = remoteIP
		credCtx.UserAgent = strings.TrimSpace(r.Header.Get("User-Agent"))
	}

	ctx := context.WithValue(WithClientIP(r.Context(), remoteIP), credCtxKey, credCtx)

	// 请求体设上限：不设的话任何能连上 /mcp 的人都能用超大请求体耗尽 Hub 的内存。
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   &rpcError{Code: -32700, Message: "Parse error"},
		})
		return
	}

	execRes := s.HandleRPCRequest(ctx, credCtx, &req)
	w.Header().Set("Content-Type", "application/json")
	if execRes.IsNotification {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
		return
	}
	json.NewEncoder(w).Encode(execRes.Response)
}

func (s *Server) resolveCredential(ctx context.Context, tokenString string, r *http.Request) (*CredentialContext, error) {
	// OAuth 访问令牌（rmo_）：只存哈希，走隐藏凭据闸门
	if oauth.IsAccessTokenFormat(tokenString) {
		// 关闭 OAuth（环境硬开关或策略总开关）= 立刻断开：已签发的访问令牌一并失效；
		// 授权记录与隐藏凭据保留，重新开启后恢复。要永久回收某个应用请在设置页「撤销」。
		if !oauth.ProtocolEnabled(s.cfg, s.store) {
			return nil, fmt.Errorf("Unauthorized: OAuth access is disabled on this hub")
		}
		_, grant, dbCred, err := s.store.FindOAuthAccessToken(ctx, tokenString)
		if err != nil {
			return nil, fmt.Errorf("internal error checking credentials: %w", err)
		}
		if dbCred != nil && grant != nil {
			want := oauth.ResourceURL(s.cfg, s.store, r)
			if strings.TrimSpace(grant.Resource) != "" && grant.Resource != want {
				return nil, fmt.Errorf("Unauthorized: token audience mismatch")
			}
			return s.admitOAuthGrantCredential(ctx, dbCred)
		}
		return nil, fmt.Errorf("Unauthorized: invalid or missing MCP token")
	}
	// P1: 查 DB MCPCredential
	if tokenString != "" {
		dbCred, err := s.store.FindCredentialByToken(ctx, tokenString)
		if err != nil {
			return nil, fmt.Errorf("internal error checking credentials: %w", err)
		}
		if dbCred != nil {
			// 能力 URL 凭据只能经它自己的网址使用，不接受请求头 / 查询串 / 旧式路径。
			if dbCred.Capability {
				return nil, fmt.Errorf("Unauthorized: credential %q is a capability URL credential and only works at its own /mcp/c/… address", dbCred.Name)
			}
			return s.admitCredential(ctx, dbCred, false)
		}
	}

	// P2: 匹配环境变量 Token
	if s.cfg.ConfigureToken != "" && tokenEqual(tokenString, s.cfg.ConfigureToken) {
		return &CredentialContext{
			CredentialName: "env:configure",
			Permissions:    model.Permissions{Configure: true},
			Source:         "env_token",
		}, nil
	}
	if s.cfg.ExecutionToken != "" && tokenEqual(tokenString, s.cfg.ExecutionToken) {
		return &CredentialContext{
			CredentialName: "env:execute",
			Permissions:    model.Permissions{Execute: true},
			Source:         "env_token",
		}, nil
	}
	if s.cfg.MCPToken != "" {
		if tokenEqual(tokenString, s.cfg.MCPToken) {
			var credID uint
			var hostName string
			if envCred, _ := s.store.EnsureEnvMCPCredential(ctx, s.cfg.MCPToken); envCred != nil {
				credID = envCred.ID
				hostName = envCred.HostName
			}
			return &CredentialContext{
				CredentialID:   credID,
				CredentialName: "env:mcp",
				HostName:       hostName,
				Permissions:    model.AllPermissions(),
				Source:         "env_token",
			}, nil
		}
		// 配置了全局 MCP Token 但未匹配任何 Token → 检查 DB 是否有凭据
		hasDBCred, _ := s.store.HasAnyCredential(ctx)
		if !hasDBCred {
			return nil, fmt.Errorf("Unauthorized: invalid or missing MCP token")
		}
		return nil, fmt.Errorf("Unauthorized: invalid or missing MCP token")
	}

	// P3: 无任何 Token 配置（env 为空且 DB 无凭据）→ 全开放。
	// 只要库里有凭据，就不再开放：缺令牌与令牌无效都要拒绝（过去无效令牌会落到开放访问，等于没有鉴权）。
	hasDBCred, _ := s.store.HasAnyCredential(ctx)
	if hasDBCred {
		if tokenString == "" {
			return nil, fmt.Errorf("Unauthorized: token required (credentials are configured)")
		}
		return nil, fmt.Errorf("Unauthorized: invalid or missing MCP token")
	}

	return &CredentialContext{
		CredentialName: "open_access",
		Permissions:    model.AllPermissions(),
		Source:         "open_access",
	}, nil
}

func (s *Server) dispatchTool(ctx context.Context, credCtx *CredentialContext, toolName string, args json.RawMessage) (any, bool, error) {
	if !credCtx.Permissions.AllowsTool(toolName) {
		return nil, true, fmt.Errorf("tool %q not allowed for credential %q", toolName, credCtx.CredentialName)
	}

	// 项目知识库的工作流范围闸（档位 off 时什么都不做；见 acl_gate.go）。
	ctx, gateErr := s.aclGate(ctx, credCtx, toolName, args)
	if gateErr != nil {
		return nil, true, gateErr
	}

	switch toolName {
	case "configure_task":
		res, err := s.handleConfigureTask(ctx, args)
		return res, err != nil, err
	case "report_progress":
		res, err := s.handleReportProgress(ctx, args)
		return res, err != nil, err
	case "interactive_feedback":
		res, err := s.handleInteractiveFeedback(ctx, args)
		return res, err != nil, err
	case "continue_feedback_session":
		res, err := s.handleContinueFeedbackSession(ctx, args)
		return res, err != nil, err
	case "get_session_image":
		res, err := s.handleGetSessionImage(ctx, args)
		return res, err != nil, err
	case "list_sessions":
		res, err := s.handleListSessions(ctx, args)
		return res, err != nil, err
	case "get_session_history":
		res, err := s.handleGetSessionHistory(ctx, args)
		return res, err != nil, err
	case "get_system_info":
		res, err := s.handleGetSystemInfo(ctx, args)
		return res, err != nil, err
	case "manage_skills":
		res, err := s.handleManageSkills(ctx, args)
		return res, err != nil, err
	case "workflow_context":
		res, err := s.handleWorkflowContextWith(ctx, credCtx, args)
		s.afterTaskChange(ctx, args)
		return res, err != nil, err
	case "task_board":
		res, err := s.handleTaskBoard(ctx, args)
		s.afterTaskChange(ctx, args)
		return res, err != nil, err
	case "project_docs":
		res, err := s.handleProjectDocs(ctx, args)
		return res, err != nil, err
	case "async_ask":
		res, err := s.handleAsyncAsk(ctx, args)
		return res, err != nil, err
	case "chat":
		res, err := s.handleChat(ctx, args)
		return res, err != nil, err
	case "branch":
		res, err := s.handleBranch(ctx, args)
		return res, err != nil, err
	case "session_params":
		res, err := s.handleSessionParams(ctx, args)
		return res, err != nil, err
	case "usage_guide":
		res, err := s.handleUsageGuide(ctx, args)
		return res, err != nil, err
	case "terminal_targets":
		return s.handleTerminalTargets(ctx, credCtx, args)
	case "terminal_exec":
		return s.handleTerminalExec(ctx, credCtx, args)
	case "terminal_run":
		return s.handleTerminalRun(ctx, credCtx, args)
	case "terminal_files":
		return s.handleTerminalFiles(ctx, credCtx, args)
	case "preset_run":
		return s.handlePresetRun(ctx, credCtx, args)
	case compatTargetsList, compatTargetInfo, compatExec, compatExecStart, compatJobStatus, compatJobOutput, compatJobCancel:
		// ExecMesh 兼容命名：与上面三个工具走同一个 Executor，只是名字与返回形状不同（AllowsTool 已按 TerminalCompat 位把关）。
		return s.handleTerminalCompat(ctx, credCtx, toolName, args)
	default:
		return nil, true, fmt.Errorf("unknown tool: %s", toolName)
	}
}
