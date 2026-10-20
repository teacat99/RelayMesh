package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/cert"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/mcp"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/web"
)

// errPortBusy 表示端口已被占用：本进程不是第一个起来的 stdio 进程（或端口被别的程序占了）。
var errPortBusy = errors.New("端口已被占用")

// runStdio 是 `relaymesh mcp stdio` 的入口。
//
// 一个机器上可以同时开多个 stdio 进程（编辑器里开了多个窗口、全局 MCP 配置）：
//   - 第一个绑定端口成功的进程是「主进程」：持有数据库、端口与网页控制台，并开着本机转发入口；
//   - 之后的进程发现端口已被占用，确认对面是 RelayMesh 的 stdio 主进程后，作为「转发进程」把自己 stdin 上的
//     消息转给主进程（见 stdio_follow.go），各自按自己的 workflow_id 开会话；
//   - 主进程退出后，剩下的转发进程里谁先抢到端口谁接管成新的主进程。
func runStdio() {
	// STDIO-001: Guarantee stdout purity. All logs, banners, and errors MUST go to stderr.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime)
	gin.DefaultWriter = os.Stderr
	gin.DefaultErrorWriter = os.Stderr

	cfg := config.LoadWithMode("stdio")
	cfg.Version = version

	hub, err := startStdioHub(cfg)
	switch {
	case err == nil:
		runStdioLeader(hub)
	case errors.Is(err, errPortBusy):
		runStdioFollower(cfg)
	default:
		log.Fatalf("[RelayMesh stdio] %v", err)
	}
}

// stdioSignalContext 返回在收到 SIGINT / SIGTERM 时取消的上下文。
func stdioSignalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			log.Printf("[RelayMesh stdio] Received signal %v, shutting down...", sig)
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// runStdioLeader 在本进程内直接处理 stdin 上的消息，直到 stdin 关闭或收到终止信号，然后优雅收尾。
func runStdioLeader(hub *stdioHub) {
	ctx, cancel := stdioSignalContext()
	defer cancel()

	// Run stdio Transport (Blocks until stdin EOF or ctx cancellation)
	if err := mcp.RunStdio(ctx, hub.server.MCPServer(), os.Stdin, os.Stdout); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[RelayMesh stdio] stdio transport exited with error: %v", err)
	}

	// Graceful Shutdown on EOF / Termination
	hub.Close()
	log.Printf("[RelayMesh stdio] Exited cleanly.")
}

// stdioHub 是 stdio 主进程持有的全部资源：数据库、API / MCP 服务、HTTP(S) 监听与接入凭证。
type stdioHub struct {
	cfg         *config.Config
	store       *store.Store
	server      *api.Server
	httpServer  *http.Server
	httpsServer *http.Server
	localKey    string
	closeOnce   sync.Once
}

// portBusy 判断一次监听失败是不是「端口已被占用」。
// 优先看系统错误码（Unix 的 EADDRINUSE、Windows 的 WSAEADDRINUSE=10048）；认不出时再试着连一下：连得上就是有人占着。
func portBusy(err error, addr string) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	var errno syscall.Errno
	if runtime.GOOS == "windows" && errors.As(err, &errno) && errno == 10048 {
		return true
	}
	conn, derr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if derr != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// startStdioHub 抢占端口并把 Hub 的其余部分拉起来。
//
// 端口绑定本身就是「谁是主进程」的仲裁：多个进程同时起来（或主进程退出后剩下的进程同时想接管）也只会有一个
// 绑定成功，其余得到 errPortBusy。监听器一直持有到开始服务，中间不存在「检查完端口又被别人抢走」的空窗。
func startStdioHub(cfg *config.Config) (*stdioHub, error) {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if portBusy(err, addr) {
			return nil, fmt.Errorf("%w: %v", errPortBusy, err)
		}
		return nil, fmt.Errorf("无法监听 %s: %w", addr, err)
	}
	started := false
	defer func() {
		if !started {
			_ = ln.Close()
		}
	}()

	log.Printf("[RelayMesh stdio] Starting RelayMesh MCP stdio server (v%s)", version)
	log.Printf("[RelayMesh stdio] Data directory: %s", cfg.DataDir)

	// Initialize SQLite Store
	st, err := store.New(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to initialize store at %s: %w", cfg.DBPath, err)
	}
	defer func() {
		if !started {
			_ = st.Close()
		}
	}()

	// 解析 JWT 签名密钥：内置默认值 / 弱密钥一律替换为自动生成并持久化的随机密钥（必须早于 api.NewServer）。
	if err := applyJWTSecret(cfg, st); err != nil {
		return nil, fmt.Errorf("Failed to resolve JWT signing secret: %w", err)
	}

	if cfg.MCPToken != "" {
		if _, err := st.EnsureEnvMCPCredential(context.Background(), cfg.MCPToken); err != nil {
			log.Printf("[RelayMesh stdio] Warning: failed to ensure env MCP credential: %v", err)
		}
	}

	// Prepare embedded frontend assets
	var staticFS fs.FS
	if distFS, err := fs.Sub(web.FS, "dist"); err == nil {
		staticFS = distFS
	} else {
		log.Printf("[RelayMesh stdio] Warning: failed to load embedded web assets: %v", err)
	}

	// Initialize HTTP & MCP Server (Sharing the same mcp.Server instance)
	server := api.NewServer(cfg, st, staticFS)

	// 本机转发入口：先开钥匙、写好接入凭证，再开始服务——其它窗口只要连得上，就一定读得到凭证。
	localKey, err := mcp.NewLocalProxyKey()
	if err != nil {
		return nil, fmt.Errorf("生成本机转发钥匙失败: %w", err)
	}
	server.MCPServer().SetLocalProxyKey(localKey)
	if err := mcp.WriteLocalProxyInfo(cfg.DataDir, mcp.LocalProxyInfo{
		Key:       localKey,
		PID:       os.Getpid(),
		Port:      cfg.Port,
		Version:   version,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		// 不致命：本窗口照常工作，只是其它窗口接入不了（它们会收到清楚的报错）。
		log.Printf("[RelayMesh stdio] Warning: 写接入凭证失败，其它窗口将无法接入本进程: %v", err)
	}

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      server.Engine(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 600 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		log.Printf("[RelayMesh stdio] Local control plane ready at http://%s/", addr)
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[RelayMesh stdio] HTTP server error: %v", err)
		}
	}()

	hub := &stdioHub{
		cfg:        cfg,
		store:      st,
		server:     server,
		httpServer: httpServer,
		localKey:   localKey,
	}

	// Optional HTTPS for secure mic access
	if cfg.TLSEnabled && cfg.HTTPSPort > 0 {
		httpsAddr := fmt.Sprintf("%s:%d", cfg.Host, cfg.HTTPSPort)
		if hln, herr := net.Listen("tcp", httpsAddr); herr == nil {
			if tlsCert, cerr := cert.GetOrCreateTLSCertificate(cfg.TLSCertPath, cfg.TLSKeyPath); cerr == nil {
				hub.httpsServer = &http.Server{
					Addr:         httpsAddr,
					Handler:      server.Engine(),
					TLSConfig:    &tls.Config{Certificates: []tls.Certificate{tlsCert}},
					ReadTimeout:  30 * time.Second,
					WriteTimeout: 600 * time.Second,
					IdleTimeout:  120 * time.Second,
				}
				go func() {
					log.Printf("[RelayMesh stdio] HTTPS control plane ready at https://%s/", httpsAddr)
					if err := hub.httpsServer.ServeTLS(hln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
						log.Printf("[RelayMesh stdio] HTTPS server error: %v", err)
					}
				}()
			} else {
				_ = hln.Close()
				log.Printf("[RelayMesh stdio] HTTPS disabled: failed to load TLS certificate: %v", cerr)
			}
		}
	}

	started = true
	return hub, nil
}

// Close 优雅关闭：先停监听（给进行中的请求最多 3 秒），再撤掉接入凭证并关闭数据库。可重复调用。
func (h *stdioHub) Close() {
	h.closeOnce.Do(func() {
		log.Printf("[RelayMesh stdio] Shutting down control plane...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		// 先让进行中的请求自然结束；3 秒后仍没结束的（比如其它窗口转发来的、正在等用户答复的调用）直接断开连接，
		// 请求上下文随之取消——转发进程会立刻看到「主进程断开」，并由别的进程接管，而不是在关掉数据库后继续跑。
		if err := h.httpServer.Shutdown(shutdownCtx); err != nil {
			_ = h.httpServer.Close()
		}
		if h.httpsServer != nil {
			if err := h.httpsServer.Shutdown(shutdownCtx); err != nil {
				_ = h.httpsServer.Close()
			}
		}
		h.server.MCPServer().SetLocalProxyKey("")
		mcp.RemoveLocalProxyInfo(h.cfg.DataDir, h.localKey)
		_ = h.store.Close()
	})
}
