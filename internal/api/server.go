package api

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/cursorhook"
	"github.com/teacat99/RelayMesh/v2/internal/mcp"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/notify"
	"github.com/teacat99/RelayMesh/v2/internal/ntfy"
	"github.com/teacat99/RelayMesh/v2/internal/oauth"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

type Server struct {
	cfg             *config.Config
	store           *store.Store
	mcpServer       *mcp.Server
	broker          *SSEBroker
	auth            *AuthHandler
	handler         *APIHandler
	engine          *gin.Engine
	terminal        *terminal.Hub // 初始化失败时为 nil（终端功能整体不可用）
	terminalLimiter *RateLimiter
	ntfy            *ntfy.Notifier
	notifier        *gateNotifier // 统一通知闸的接线层（见 notify_gate.go）

	// 任务清单的后台整理（见 work_items_maintenance.go）
	workMaintMu     sync.Mutex
	workMaintCancel context.CancelFunc

	// 草稿图片的后台清理（见 draft_images.go）
	draftMaintMu     sync.Mutex
	draftMaintCancel context.CancelFunc
}

func NewServer(cfg *config.Config, st *store.Store, staticFS fs.FS) *Server {
	if !cfg.DevMode {
		gin.SetMode(gin.ReleaseMode)
	}

	broker := NewSSEBroker()
	broker.sessionWorkflow = func(sessionID string) string {
		sess, err := st.GetFeedbackSession(context.Background(), sessionID)
		if err != nil || sess == nil {
			return ""
		}
		return sess.WorkflowID
	}
	var mcpServer *mcp.Server
	mcpServer = mcp.NewServer(cfg, st, func(eventType string, data any) {
		// MCP 侧直接广播的会话对象（例如 AI 消费反馈后的 session_updated）与 REST 广播走同一套载荷规整：
		// 补齐实时字段并剥离图片 Base64，避免把用户上传的截图原样推送给每个浏览器。
		if sess, ok := data.(*model.FeedbackSession); ok {
			var live sessionLiveSource
			if mcpServer != nil {
				live = mcpServer
			}
			data = sessionEventPayload(live, sess)
		}
		broker.Broadcast(eventType, data)
	})

	// ntfy 推送：新反馈请求发生时（见 mcp.RequestNotifier）、模型发起的命令运行结束时（见下面的
	// terminalHub.SetRunEndHook），异步推到手机 / 桌面上的 ntfy 应用。
	// 延迟推送在发出前复核「请求是否仍在等人」，所以这里把会话查询接进去；运行结束的推送里要写主机名，
	// 同样在真正要推送时才去查（见 ntfyHostName）。
	ntfyNotifier := ntfy.New(ntfy.Options{
		Load: st.GetNtfyConfig,
		StillPending: func(ctx context.Context, sessionID string) bool {
			sess, err := st.GetFeedbackSession(ctx, sessionID)
			return err == nil && sess != nil && sess.Status == "pending"
		},
		HostName: ntfyHostName(st),
		EnvToken: cfg.NtfyToken,
		Sender:   ntfy.NewSender("RelayMesh/" + cfg.Version),
	})
	// 统一通知闸（#115）：所有「要不要打扰你」的判断汇到这里，按偏好（免打扰 / 会话静音）决策后
	// 分发到浏览器（SSE notify）与 ntfy 两条互不影响的通道。老三类来源（阻塞待答 / 运行结束 / 守护告警）
	// 迁入闸，新来源（notice / async_ask）也走闸。
	gateNotify := newGateNotifier(notify.NewGate(notify.Options{Load: st.GetNotifyPrefs, Broadcast: broker.Broadcast}), ntfyNotifier, st)
	mcpServer.SetRequestNotifier(gateNotify)
	mcpServer.SetEventNotifier(gateNotify)
	mcpServer.SetGroupNotifier(gateNotify)

	auth := NewAuthHandler(cfg, st, nil)
	handler := NewAPIHandler(cfg, st, mcpServer, broker)
	handler.SetNtfy(ntfyNotifier)
	terminalHub, terminalLimiter := newTerminalHub(cfg, st, auth)

	var oauthSvc *oauth.Service
	if !cfg.OAuthOff {
		var auditFn oauth.AuditFn
		var revokeFn oauth.RevokeFn
		if terminalHub != nil {
			auditFn = func(event, result, detail string) {
				terminalHub.RecordOAuthAudit(event, result, detail)
			}
			revokeFn = func(credID uint, reason string) {
				terminalHub.MCPCredentialChanged(credID, reason)
			}
		}
		oauthSvc = oauth.NewService(cfg, st, auditFn, revokeFn)
		handler.oauth = oauthSvc
	}
	if terminalHub != nil {
		mcpServer.SetTerminalExecutor(terminalHub.Executor())
		mcpServer.SetTerminalGuardHub(terminalHub)
		mcpServer.SetPresetRunner(presetRunner{hub: terminalHub})
		handler.SetTerminal(terminalHub)
		terminalHub.SetRunEndHook(gateNotify.RunEndHook())
		terminalHub.SetGuardHook(gateNotify.GuardHook())
		terminalHub.SetAutoResumeHook(gateNotify.AutoResumeHook())
		gateNotify.broadcast = broker.Broadcast
		terminalHub.SetScreenHook(gateNotify.ScreenHook())
		// Hub 读非交互运行的输出推进了 Agent 会话的状态（#129）：和 hook 汇报一样，推一条 agent_state 让 Web 重新取。
		terminalHub.SetAgentStateHook(func(ev terminal.AgentStateEvent) {
			broker.Broadcast(agentStateEventName, map[string]any{
				"workflow_id": ev.WorkflowID, "identity_id": ev.IdentityID, "session_id": ev.SessionID,
				"state": ev.State, "sub": ev.Sub, "source": ev.Source, "since": ev.Since,
			})
		})
		terminalHub.SetWakeHook(func(workflowID string) {
			broker.Broadcast("conversation_changed", map[string]string{"workflow_id": workflowID})
		})
	}

	// 访问日志与 panic 恢复日志使用脱敏格式：token / ticket 等查询参数与 /mcp/<令牌>、/mcp/c/<令牌> 路径不得以明文进入日志（反代日志另行处理）。
	engine := gin.New()
	engine.Use(gin.LoggerWithConfig(gin.LoggerConfig{Formatter: RedactingLogFormatter}), RedactingRecovery())

	// 可信代理与客户端 IP：登录反爆破等以 c.ClientIP() 为键的逻辑依赖它，必须收窄 Gin「信任全部代理」的默认值。
	ConfigureTrustedProxies(engine, cfg.TrustedProxies)

	// 安全响应头与 CSP（S0.2）：前端 Markdown 已做 DOMPurify 净化，这里是浏览器侧的兜底防线。
	cspMode, cspWarning := ResolveCSPMode(cfg.CSPMode)
	if cspWarning != "" {
		log.Printf("[security] %s", cspWarning)
	}
	log.Printf("[security] 内容安全策略 (CSP): %s", cspMode)
	engine.Use(SecurityHeadersMiddleware(cspMode))

	// CORS Middleware
	engine.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, Accept, X-Requested-With, X-Terminal-Unlock")
		c.Header("Access-Control-Expose-Headers", "X-Renewed-Token")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	// Gzip 动态传输压缩中间件（大幅缩减弱网/小带宽下的 JSON 传输体积，跳过 SSE 实时流）
	engine.Use(GzipMiddleware())

	// MCP Streamable HTTP Endpoint (支持 /mcp, /mcp/, /mcp/token/:token, /mcp/:token)
	// 经可信代理解析后的客户端 IP 交给 MCP 服务，用于令牌来源白名单与「最近使用」记录。
	mcpEntry := func(c *gin.Context) {
		ctx := mcp.WithClientIP(c.Request.Context(), c.ClientIP())
		mcpServer.ServeHTTP(c.Writer, c.Request.WithContext(ctx))
	}
	engine.Any("/mcp", mcpEntry)
	engine.Any("/mcp/*path", mcpEntry)
	// stdio 主进程的本机转发入口（#190：同机其它 stdio 窗口把消息转给这里）。只有 stdio 主进程设置了钥匙才会响应，
	// 常驻服务 / Docker / 生产 Hub 上一律 404；必须来自本机回环并带正确的钥匙（见 internal/mcp/local_proxy.go）。
	engine.Any(mcp.LocalProxyPath, func(c *gin.Context) {
		mcpServer.ServeLocalProxy(c.Writer, c.Request)
	})

	if oauthSvc != nil {
		oauthSvc.Mount(engine)
	}

	// SSE Events Stream（受鉴权保护）：EventSource 无法自定义请求头，因此除 Authorization 头外，
	// 还接受一次性票据 ?ticket=（推荐）与受环境变量控制的旧版 ?token= 兼容通道；其他接口一律只认请求头。
	engine.GET("/api/v1/events", auth.StreamMiddleware(), broker.HandleSSE)
	engine.GET("/sse/events", auth.StreamMiddleware(), broker.HandleSSE)
	// HTML Artifact 渲染页：在 iframe 里加载，带不了请求头，只认绑定文档的一次性票据；响应头自带沙箱 CSP。
	engine.GET("/api/v1/artifacts/:id/render", auth.ArtifactRenderMiddleware(), handler.RenderArtifact)

	// Web 终端（Hub 自带多层闸门：总闸 / 人类 JWT / 解锁令牌 / 通用 Agent 入口的预认证防护，不走 auth.Middleware）
	if terminalHub != nil {
		terminalHub.Mount(engine)
	}

	// Auth APIs
	authGroup := engine.Group("/api/v1/auth")
	{
		authGroup.POST("/login", auth.Login)
		authGroup.GET("/status", auth.Status)
	}

	// Cursor hook 的安装入口（追加指令收件箱的第一层发现机制）：公开只读，不含任何密钥，
	// 让项目里一条 curl 就能装上 Go 二进制（relaymesh-hook）。历史上还提供过 Python 脚本
	// （/hooks/cursor/relaymesh-inbox.py），已移除。
	engine.GET(cursorhook.InstallShPath, handler.ServeHookInstallSh)
	engine.GET(cursorhook.InstallPs1Path, handler.ServeHookInstallPs1)
	engine.GET(cursorhook.HookBinPrefix+":name", handler.ServeHookBinary)
	engine.HEAD(cursorhook.HookBinPrefix+":name", handler.ServeHookBinary)

	// Public / Unauthenticated APIs
	publicV1 := engine.Group("/api/v1")
	{
		// 图片静态只读资源：内容寻址哈希不可变强缓存，支持直接匿名访问与外链展示
		publicV1.GET("/sessions/:id/images/:index", handler.GetSessionImage)
	}

	// Protected / Business APIs
	v1 := engine.Group("/api/v1")
	v1.Use(auth.Middleware())
	{
		// Feedback Sessions
		v1.GET("/sessions/current", handler.GetCurrentSession)
		v1.GET("/sessions", handler.ListSessions)
		v1.GET("/sessions/:id", handler.GetSession)
		v1.DELETE("/sessions/:id", handler.DeleteSession)
		v1.POST("/sessions/restore", handler.RestoreSession)
		v1.POST("/sessions/:id/submit", handler.SubmitFeedback)
		v1.POST("/sessions/:id/revoke", handler.RevokeSession)
		v1.POST("/sessions/:id/cancel", handler.CancelSession)
		v1.POST("/sessions/:id/keepalive", handler.KeepaliveSession)
		v1.POST("/sessions/:id/archive", handler.ArchiveSession)
		v1.POST("/sessions/:id/unarchive", handler.UnarchiveSession)
		v1.POST("/sessions/:id/rename", handler.RenameSession)
		v1.POST("/sessions/:id/prompt_wait", handler.UpdateSessionPromptWait)
		v1.POST("/sessions/:id/max_checks", handler.UpdateSessionMaxChecks)
		v1.POST("/sessions/:id/wait_countdown", handler.UpdateSessionWaitCountdown)
		v1.POST("/sessions/:id/presence", handler.SessionPresence)

		// 网页手动新建会话：占位会话 + 启动提示词（#174）
		v1.POST("/workflows", handler.CreateWorkflow)

		// Queued & Appended Feedback (支持无交互期间提前追加留言与秒回直取)
		v1.GET("/workflows/:workflow_id/sessions", handler.GetWorkflowSessions)
		v1.GET("/workflows/:workflow_id/sessions/bodies", handler.GetWorkflowSessionBodies)
		v1.POST("/workflows/:workflow_id/append", handler.AppendWorkflowFeedback)
		v1.GET("/workflows/:workflow_id/queued", handler.ListQueuedFeedbacks)
		v1.POST("/feedbacks/queued/:id/revoke", handler.RevokeQueuedFeedback)

		// 追加指令收件箱：每条追加的状态、智能体最近一次交互，以及手动开关
		v1.GET("/workflows/:workflow_id/inbox", handler.GetWorkflowInbox)
		v1.GET("/inbox/config", handler.GetInboxConfig)
		v1.PUT("/inbox/config", handler.UpdateInboxConfig)

		// 统一消息流（#116）：1:1 会话的消息、已读游标、发送与列表摘要
		v1.GET("/conversations/:id/messages", handler.ListConversationMessages)
		v1.POST("/conversations/:id/messages", handler.SendConversationMessage)
		v1.POST("/conversations/:id/read", handler.MarkConversationRead)
		v1.GET("/conversation-summaries", handler.ConversationSummaries)

		// 群聊（#117）：建群 / 拉人 / 移人 / 设置 / 归档都只有这里的入口（只有你能做），Agent 在 MCP 里没有对应工具
		v1.GET("/groups", handler.ListGroups)
		v1.GET("/groups/candidates", handler.GroupCandidates)
		v1.POST("/groups", handler.CreateGroup)
		v1.GET("/groups/:id", handler.GetGroup)
		v1.PUT("/groups/:id", handler.UpdateGroup)
		v1.POST("/groups/:id/members", handler.AddGroupMembers)
		v1.DELETE("/groups/:id/members/:ref", handler.RemoveGroupMember)
		v1.PUT("/groups/:id/members/:ref/policy", handler.SetGroupMemberPolicy)

		// Agent 身份与状态（#113）：身份清单与改名、会话与当前状态
		v1.GET("/identities", handler.ListAgentIdentities)
		v1.PUT("/identities/:id", handler.UpdateAgentIdentity)
		v1.GET("/agent-sessions", handler.ListAgentSessions)

		// 异步对话模式（#42）：按工作流开关 + 全局的授权档位
		v1.GET("/workflows/:workflow_id/async-mode", handler.GetAsyncMode)
		v1.PUT("/workflows/:workflow_id/async-mode", handler.UpdateAsyncMode)
		v1.POST("/workflows/:workflow_id/reply-window", handler.ReplyWindow)
		v1.GET("/auth-profiles", handler.ListAuthProfiles)
		v1.POST("/auth-profiles", handler.CreateAuthProfile)
		v1.PUT("/auth-profiles/:id", handler.UpdateAuthProfile)
		v1.POST("/auth-profiles/:id/default", handler.SetDefaultAuthProfile)
		v1.DELETE("/auth-profiles/:id", handler.DeleteAuthProfile)

		// Workflow Drafts (数据库级草稿持久化与跨设备同步)
		v1.GET("/workflows/:workflow_id/drafts", handler.GetWorkflowDraft)
		v1.PUT("/workflows/:workflow_id/drafts", handler.SaveWorkflowDraft)
		v1.DELETE("/workflows/:workflow_id/drafts", handler.DeleteWorkflowDraft)

		// 草稿图片（云存储）：粘贴即上传，提交时被会话直接复用，草稿销毁时删除
		v1.GET("/workflows/:workflow_id/draft-images", handler.ListDraftImages)
		v1.PUT("/workflows/:workflow_id/draft-images/:key", handler.PutDraftImage)
		v1.GET("/workflows/:workflow_id/draft-images/:key", handler.GetDraftImage)
		v1.DELETE("/workflows/:workflow_id/draft-images/:key", handler.DeleteDraftImage)

		// Voice ASR Transcription
		v1.POST("/voice/transcribe", handler.TranscribeAudio)

		// System Settings
		v1.GET("/settings", handler.GetSettings)
		v1.PUT("/settings", handler.UpdateSettings)

		// ntfy 推送设置（凭据只写不读，所以不放进上面的全局设置）
		v1.GET("/notify/ntfy", handler.GetNtfy)
		v1.PUT("/notify/ntfy", handler.UpdateNtfy)
		v1.POST("/notify/ntfy/test", handler.TestNtfy)

		// 通知偏好（#115）：免打扰、ntfy 预览模式、会话级策略
		v1.GET("/notify/prefs", handler.GetNotifyPrefs)
		v1.PUT("/notify/prefs", handler.UpdateNotifyPrefs)
		v1.PUT("/notify/conversations", handler.SetNotifyConversation)
		v1.DELETE("/notify/conversations", handler.ResetNotifyConversation)

		// User Norms (Skills)
		v1.GET("/norms", handler.ListNorms)
		v1.GET("/norms/:name", handler.GetNorm)
		v1.POST("/norms", handler.CreateNorm)
		v1.PUT("/norms/:name", handler.UpdateNorm)
		v1.DELETE("/norms/:name", handler.DeleteNorm)

		// MCP Credentials
		v1.GET("/credentials", handler.ListCredentials)
		v1.GET("/credentials/:id", handler.GetCredential)
		v1.POST("/credentials", handler.CreateCredential)
		v1.POST("/credentials/preview", handler.PreviewCredentialTools)
		v1.PUT("/credentials/:id", handler.UpdateCredential)
		v1.DELETE("/credentials/:id", handler.DeleteCredential)
		v1.POST("/credentials/:id/regenerate", handler.RegenerateCredentialToken)
		handler.registerOAuthRoutes(v1)
		// 查看旧令牌：必须再次提交 Web 密码（见 credentials_reveal.go），与登录共用反爆破额度。
		v1.POST("/credentials/:id/reveal", auth.RevealCredentialToken)

		// Workflow Phases
		v1.GET("/workflows/:workflow_id/phase", handler.GetWorkflowPhase)
		v1.PUT("/workflows/:workflow_id/phase", handler.SetWorkflowPhase)

		// Workflow Sheet (会话工作表 / 文档)
		v1.GET("/workflows/:workflow_id/checkpoints", handler.ListWorkflowCheckpoints)
		v1.GET("/workflows/:workflow_id/sheet", handler.GetWorkflowSheet)
		v1.PUT("/workflows/:workflow_id/sheet", handler.SaveWorkflowSheet)

		// 项目文档库
		v1.GET("/workflows/:workflow_id/docs", handler.ListProjectDocs)
		v1.POST("/workflows/:workflow_id/docs", handler.CreateProjectDoc)
		v1.GET("/workflows/:workflow_id/docs/search", handler.SearchProjectDocs)
		v1.GET("/workflows/:workflow_id/docs/blueprint", handler.GetProjectDocsBlueprint)
		v1.GET("/workflows/:workflow_id/docs/export.zip", handler.ExportProjectDocsZip)
		// 带斜杠的路径走 query（Gin 的 :doc 无法匹配嵌套路径；%2F 会被解码成 / 导致 404）
		v1.GET("/workflows/:workflow_id/docs/content", handler.GetProjectDoc)
		v1.PUT("/workflows/:workflow_id/docs/content", handler.UpdateProjectDoc)
		v1.DELETE("/workflows/:workflow_id/docs/content", handler.DeleteProjectDoc)
		v1.POST("/workflows/:workflow_id/docs/content/archive", handler.ArchiveProjectDoc)
		v1.GET("/workflows/:workflow_id/docs/content/revisions", handler.ListProjectDocRevisions)
		v1.GET("/workflows/:workflow_id/docs/content/links", handler.GetProjectDocLinks)
		v1.GET("/workflows/:workflow_id/docs/:doc/links", handler.GetProjectDocLinks)
		v1.POST("/workflows/:workflow_id/docs/content/revisions/:rev/restore", handler.RestoreProjectDocRevision)
		v1.POST("/workflows/:workflow_id/docs/content/comments", handler.PostProjectDocComment)
		v1.GET("/workflows/:workflow_id/docs/:doc", handler.GetProjectDoc)
		v1.PUT("/workflows/:workflow_id/docs/:doc", handler.UpdateProjectDoc)
		v1.DELETE("/workflows/:workflow_id/docs/:doc", handler.DeleteProjectDoc)
		v1.POST("/workflows/:workflow_id/docs/:doc/archive", handler.ArchiveProjectDoc)
		v1.GET("/workflows/:workflow_id/docs/:doc/revisions", handler.ListProjectDocRevisions)
		v1.POST("/workflows/:workflow_id/docs/:doc/revisions/:rev/restore", handler.RestoreProjectDocRevision)
		v1.POST("/workflows/:workflow_id/docs/:doc/comments", handler.PostProjectDocComment)

		// 项目知识库（#114）：只有登录的「人」能调用；Agent 经 MCP，没有这些入口。
		v1.GET("/projects", handler.ListProjects)
		v1.POST("/projects", handler.CreateProject)
		v1.GET("/projects/suggestions", handler.ProjectSuggestions)
		v1.GET("/projects/:id", handler.GetProject)
		v1.PUT("/projects/:id", handler.UpdateProject)
		v1.DELETE("/projects/:id", handler.ArchiveProject)
		v1.POST("/projects/:id/dirs", handler.AddProjectDir)
		v1.DELETE("/projects/:id/dirs/:dir_id", handler.RemoveProjectDir)
		v1.PUT("/projects/:id/modules", handler.UpsertProjectModule)
		v1.DELETE("/projects/:id/modules", handler.DeleteProjectModule)
		v1.GET("/projects/:id/migration", handler.ProjectMigration)
		v1.POST("/projects/:id/migration", handler.ApplyProjectMigration)
		v1.GET("/projects/:id/docs", handler.ListProjectKnowledge)
		v1.GET("/projects/:id/docs/search", handler.SearchProjectKnowledge)
		v1.GET("/projects/:id/docs/export.zip", handler.ExportProjectKnowledgeZip)
		v1.GET("/projects/:id/grants", handler.ListProjectGrants)
		v1.POST("/projects/:id/grants", handler.CreateProjectGrant)
		v1.DELETE("/projects/:id/grants/:grant_id", handler.RevokeProjectGrant)
		v1.GET("/projects/:id/module-page", handler.ProjectModulePage)
		v1.GET("/projects/:id/blueprint", handler.ProjectBlueprint)
		v1.GET("/artifacts/:id", handler.GetArtifact)
		v1.POST("/artifacts/:id/ticket", auth.IssueArtifactTicket)
		v1.GET("/artifacts/:id/data", handler.ListArtifactData)
		v1.GET("/artifacts/:id/data/:key", handler.GetArtifactData)
		v1.PUT("/artifacts/:id/data/:key", handler.PutArtifactData)
		v1.DELETE("/artifacts/:id/data/:key", handler.DeleteArtifactData)
		v1.GET("/artifacts/:id/comments", handler.ListArtifactComments)
		v1.POST("/artifacts/:id/comments", handler.PostArtifactComment)
		v1.POST("/artifacts/:id/comments/:cid/resolve", handler.ResolveArtifactComment)
		v1.GET("/workflows/:workflow_id/project", handler.WorkflowProject)
		v1.GET("/audit", handler.ListAudit)
		v1.GET("/audit/export.csv", handler.ExportAuditCSV)

		// Security & Rate Limiting & Credentials
		v1.GET("/auth/blocked_ips", auth.GetBlockedIPs)
		v1.POST("/auth/unblock_ip", auth.UnblockIP)
		v1.POST("/auth/clear_blocked_ips", auth.ClearAllBlockedIPs)
		v1.POST("/auth/sse_ticket", auth.IssueSSETicket)
		v1.POST("/auth/change_credentials", auth.ChangeCredentials)
		v1.POST("/auth/reset_credentials", auth.ResetCredentials)

		// 任务清单（WorkItem）：独立于会话的工作清单，与下面托管自驾的编排任务是两回事
		v1.GET("/work-items", handler.ListWorkItems)
		v1.POST("/work-items", handler.CreateWorkItem)
		v1.GET("/work-items/assignees", handler.ListWorkItemAssignees)
		v1.GET("/work-items/:id", handler.GetWorkItem)
		v1.GET("/workflows/:workflow_id/work-updates", handler.GetWorkflowUpdates)

		v1.GET("/workflows/:workflow_id/confirms/board", handler.GetConfirmsBoard)
		v1.GET("/workflows/:workflow_id/confirms", handler.ListConfirms)
		v1.GET("/workflows/:workflow_id/confirms/:ref", handler.GetConfirm)
		v1.POST("/workflows/:workflow_id/confirms/answer-batch", handler.AnswerConfirmBatch)
		v1.POST("/workflows/:workflow_id/confirms/:ref/answer", handler.AnswerConfirm)
		v1.POST("/workflows/:workflow_id/confirms/:ref/dismiss", handler.DismissConfirm)
		v1.PUT("/work-items/:id", handler.UpdateWorkItem)
		v1.POST("/work-items/:id/improve", handler.ImproveWorkItem)
		v1.DELETE("/work-items/:id", handler.DeleteWorkItem)
		v1.POST("/work-items/:id/sessions", handler.LinkWorkItemSession)
		v1.DELETE("/work-items/:id/sessions/:session_id", handler.UnlinkWorkItemSession)
		// 任务的模块规划：每个工作流一棵最多两层的树（模块 → 功能）
		v1.GET("/work-modules", handler.ListWorkModules)
		v1.POST("/work-modules", handler.CreateWorkModule)
		v1.PUT("/work-modules/:id", handler.UpdateWorkModule)
		v1.POST("/work-modules/:id/move", handler.MoveWorkModule)
		v1.DELETE("/work-modules/:id", handler.DeleteWorkModule)

		// Tasks & Orchestration
		v1.GET("/tasks", handler.ListTasks)
		v1.POST("/tasks", handler.CreateTask)
		v1.GET("/tasks/:id", handler.GetTask)
		v1.PUT("/tasks/:id/stages", handler.UpdateTaskStages)
		v1.GET("/tasks/:id/reports", handler.ReadReports)
		v1.GET("/tasks/:id/feedbacks", handler.ReadTaskFeedbacks)
		v1.POST("/tasks/:id/feedbacks", handler.SendTaskFeedback)
		v1.POST("/tasks/:id/ack", handler.AckReports)
	}

	// Static Web Assets / Embedded SPA
	if staticFS != nil {
		fileServer := http.FileServer(http.FS(staticFS))
		engine.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/mcp") {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			// /.well-known/ 与 /hooks/ 下没匹配到的路径（含 %2f 之类畸形写法）一律干净的 404，
			// 不要落到 SPA 兜底页上返回 200 text/html。
			if strings.HasPrefix(path, "/.well-known/") || strings.HasPrefix(path, "/hooks/") {
				c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
				return
			}
			// OAuth 协议端点在 oauth.Service 上注册；/oauth/consent 是前端 SPA 路由。
			if strings.HasPrefix(path, "/oauth/") && path != "/oauth/consent" {
				c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
				return
			}
			if path == "/oauth/consent" {
				c.Header("X-Frame-Options", "DENY")
				c.Header("Content-Security-Policy", "frame-ancestors 'none'")
			}

			// Check if file exists in staticFS
			name := strings.TrimPrefix(path, "/")
			f, err := staticFS.Open(name)
			if err == nil {
				f.Close()
				// 缓存头见 static_cache.go：带哈希的产物缓存一年，其余 no-cache + ETag（回访省掉整套前端的重复下载）。
				staticCacheHeaders(c.Writer.Header(), staticFS, name)
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}

			// SPA fallback to index.html（页面本身永远 no-cache：发新版后第一次打开就生效）
			staticCacheHeaders(c.Writer.Header(), staticFS, "index.html")
			c.Request.URL.Path = "/"
			fileServer.ServeHTTP(c.Writer, c.Request)
		})
	}

	return &Server{
		cfg:             cfg,
		store:           st,
		mcpServer:       mcpServer,
		broker:          broker,
		auth:            auth,
		handler:         handler,
		engine:          engine,
		terminal:        terminalHub,
		terminalLimiter: terminalLimiter,
		ntfy:            ntfyNotifier,
		notifier:        gateNotify,
	}
}

// Start 启动需要后台协程的组件（终端总闸监视与周期清理）。应在开始接受连接之前调用；重复调用无副作用。
func (s *Server) Start() {
	if s.terminal != nil {
		s.terminal.Start()
	}
	s.startWorkItemMaintenance()
	s.startDraftImageMaintenance()
}

// Shutdown 优雅关闭被 HTTP 服务器「劫持」的长连接（终端 WebSocket）：http.Server.Shutdown 不会处理它们，
// 因此必须在 http.Server.Shutdown 之前调用，让终端连接带着关闭码结束。
func (s *Server) Shutdown(ctx context.Context) {
	s.stopWorkItemMaintenance()
	s.stopDraftImageMaintenance()
	s.ntfy.Stop()
	if s.broker != nil {
		s.broker.Close()
	}
	if s.terminal != nil {
		s.terminal.Shutdown(ctx)
	}
	if s.terminalLimiter != nil {
		s.terminalLimiter.Close()
	}
}

// Terminal 返回终端 Hub（可能为 nil）。
func (s *Server) Terminal() *terminal.Hub {
	return s.terminal
}

func (s *Server) Engine() *gin.Engine {
	return s.engine
}

func (s *Server) MCPServer() *mcp.Server {
	return s.mcpServer
}

func (s *Server) Store() *store.Store {
	return s.store
}

func (s *Server) Broker() *SSEBroker {
	return s.broker
}

func (s *Server) Run(addr string) error {
	return s.engine.Run(addr)
}
