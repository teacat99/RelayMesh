package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/mcp"
	"github.com/teacat99/RelayMesh/internal/store"
)

type Server struct {
	cfg       *config.Config
	store     *store.Store
	mcpServer *mcp.Server
	broker    *SSEBroker
	auth      *AuthHandler
	handler   *APIHandler
	engine    *gin.Engine
}

func NewServer(cfg *config.Config, st *store.Store, staticFS fs.FS) *Server {
	if !cfg.DevMode {
		gin.SetMode(gin.ReleaseMode)
	}

	broker := NewSSEBroker()
	mcpServer := mcp.NewServer(cfg, st, func(eventType string, data any) {
		broker.Broadcast(eventType, data)
	})

	auth := NewAuthHandler(cfg, st, nil)
	handler := NewAPIHandler(cfg, st, mcpServer, broker)

	engine := gin.Default()

	// CORS Middleware
	engine.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, Accept, X-Requested-With")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	// MCP Streamable HTTP Endpoint (支持 /mcp, /mcp/, /mcp/token/:token, /mcp/:token)
	engine.Any("/mcp", gin.WrapH(mcpServer))
	engine.Any("/mcp/*path", gin.WrapH(mcpServer))

	// SSE Events Stream (受鉴权保护，支持 Header 或 ?token=<jwt>)
	engine.GET("/api/v1/events", auth.Middleware(), broker.HandleSSE)
	engine.GET("/sse/events", auth.Middleware(), broker.HandleSSE)

	// Auth APIs
	authGroup := engine.Group("/api/v1/auth")
	{
		authGroup.POST("/login", auth.Login)
		authGroup.GET("/status", auth.Status)
	}

	// Protected / Business APIs
	v1 := engine.Group("/api/v1")
	v1.Use(auth.Middleware())
	{
		// Feedback Sessions
		v1.GET("/sessions/current", handler.GetCurrentSession)
		v1.GET("/sessions", handler.ListSessions)
		v1.GET("/sessions/:id", handler.GetSession)
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
		v1.POST("/sessions/:id/user_presence", handler.UpdateSessionUserPresence)

		// Queued & Appended Feedback (支持无交互期间提前追加留言与秒回直取)
		v1.POST("/workflows/:workflow_id/append", handler.AppendWorkflowFeedback)
		v1.GET("/workflows/:workflow_id/queued", handler.ListQueuedFeedbacks)
		v1.POST("/feedbacks/queued/:id/revoke", handler.RevokeQueuedFeedback)

		// Workflow Drafts (数据库级草稿持久化与跨设备同步)
		v1.GET("/workflows/:workflow_id/drafts", handler.GetWorkflowDraft)
		v1.PUT("/workflows/:workflow_id/drafts", handler.SaveWorkflowDraft)
		v1.DELETE("/workflows/:workflow_id/drafts", handler.DeleteWorkflowDraft)

		// Voice ASR Transcription
		v1.POST("/voice/transcribe", handler.TranscribeAudio)

		// System Settings
		v1.GET("/settings", handler.GetSettings)
		v1.PUT("/settings", handler.UpdateSettings)

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
		v1.PUT("/credentials/:id", handler.UpdateCredential)
		v1.DELETE("/credentials/:id", handler.DeleteCredential)
		v1.POST("/credentials/:id/regenerate", handler.RegenerateCredentialToken)

		// Workflow Phases
		v1.GET("/workflows/:workflow_id/phase", handler.GetWorkflowPhase)
		v1.PUT("/workflows/:workflow_id/phase", handler.SetWorkflowPhase)

		// Security & Rate Limiting & Credentials
		v1.GET("/auth/blocked_ips", auth.GetBlockedIPs)
		v1.POST("/auth/unblock_ip", auth.UnblockIP)
		v1.POST("/auth/clear_blocked_ips", auth.ClearAllBlockedIPs)
		v1.POST("/auth/change_credentials", auth.ChangeCredentials)
		v1.POST("/auth/reset_credentials", auth.ResetCredentials)

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

			// Check if file exists in staticFS
			f, err := staticFS.Open(strings.TrimPrefix(path, "/"))
			if err == nil {
				f.Close()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}

			// SPA fallback to index.html
			c.Request.URL.Path = "/"
			fileServer.ServeHTTP(c.Writer, c.Request)
		})
	}

	return &Server{
		cfg:       cfg,
		store:     st,
		mcpServer: mcpServer,
		broker:    broker,
		auth:      auth,
		handler:   handler,
		engine:    engine,
	}
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
