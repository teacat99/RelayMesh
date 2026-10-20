package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/browser"
	"github.com/teacat99/RelayMesh/v2/internal/cert"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
	"github.com/teacat99/RelayMesh/v2/web"
)

func runServe() {
	cfg := config.LoadWithMode("serve")
	cfg.Version = version

	log.Printf("==================================================")
	log.Printf("  RelayMesh · Agent Communication & Feedback Relay")
	log.Printf("  Version: %s | DevMode: %v", version, cfg.DevMode)
	log.Printf("==================================================")

	// 1. Initialize SQLite Store
	st, err := store.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize store: %v", err)
	}
	log.Printf("Database initialized at %s", cfg.DBPath)

	// 解析 JWT 签名密钥：内置默认值 / 弱密钥一律替换为自动生成并持久化的随机密钥（必须早于 api.NewServer）。
	if err := applyJWTSecret(cfg, st); err != nil {
		log.Fatalf("Failed to resolve JWT signing secret: %v", err)
	}

	if cfg.MCPToken != "" {
		if _, err := st.EnsureEnvMCPCredential(context.Background(), cfg.MCPToken); err != nil {
			log.Printf("Warning: failed to ensure env MCP credential: %v", err)
		}
	}

	// 2. Prepare embedded frontend assets
	var staticFS fs.FS
	if distFS, err := fs.Sub(web.FS, "dist"); err == nil {
		staticFS = distFS
	} else {
		log.Printf("Warning: failed to load embedded web assets: %v", err)
	}

	// 3. Initialize HTTP & MCP Server
	server := api.NewServer(cfg, st, staticFS)
	server.Start()

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      server.Engine(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 600 * time.Second, // Long timeout for MCP interactive feedback
		IdleTimeout:  120 * time.Second,
	}

	// 4. Start HTTP Server in background
	go func() {
		log.Printf("RelayMesh HTTP server listening on http://%s", addr)
		log.Printf("  - Web UI: http://%s/", addr)
		log.Printf("  - MCP Endpoint: http://%s/mcp", addr)
		log.Printf("  - SSE Stream: http://%s/api/v1/events", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// 5. Start HTTPS Server (Dual-port for Microphone & Secure Web UI)
	var httpsServer *http.Server
	if cfg.TLSEnabled && cfg.HTTPSPort > 0 {
		tlsCert, err := cert.GetOrCreateTLSCertificate(cfg.TLSCertPath, cfg.TLSKeyPath)
		if err != nil {
			log.Printf("Warning: failed to setup TLS: %v", err)
		} else {
			// 叶子证书指纹供 Agent 安装命令的 --pin 参数使用（自签名证书场景下，用指纹代替系统 CA 校验服务端身份）。
			if hub := server.Terminal(); hub != nil && len(tlsCert.Certificate) > 0 {
				sum := sha256.Sum256(tlsCert.Certificate[0])
				hub.SetTLSPin("sha256:" + hex.EncodeToString(sum[:]))
				hub.SetHTTPSPort(cfg.HTTPSPort)
				// 公钥指纹（curl --pinnedpubkey 的格式）：自签名证书下，从服务端下载安装文件时用它校验服务端身份。
				if leaf, err := x509.ParseCertificate(tlsCert.Certificate[0]); err == nil {
					spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
					hub.SetTLSSPKI("sha256//" + base64.StdEncoding.EncodeToString(spki[:]))
				}
			}
			httpsAddr := fmt.Sprintf("%s:%d", cfg.Host, cfg.HTTPSPort)
			httpsServer = &http.Server{
				Addr:         httpsAddr,
				Handler:      server.Engine(),
				TLSConfig:    &tls.Config{Certificates: []tls.Certificate{tlsCert}},
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 600 * time.Second,
				IdleTimeout:  120 * time.Second,
			}
			go func() {
				log.Printf("RelayMesh HTTPS server listening on https://%s (Microphone & Secure Web UI)", httpsAddr)
				log.Printf("  - HTTPS Web UI: https://%s/", httpsAddr)
				if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
					log.Printf("HTTPS server error: %v", err)
				}
			}()
		}
	}

	// 6. Auto open browser strictly in local standalone desktop/WSL mode
	if cfg.AutoOpenBrowser && (cfg.Host == "127.0.0.1" || cfg.Host == "localhost" || cfg.Host == "0.0.0.0" || cfg.Host == "") {
		openHost := cfg.Host
		if openHost == "0.0.0.0" || openHost == "" {
			openHost = "localhost"
		}
		targetURL := fmt.Sprintf("http://%s:%d/", openHost, cfg.Port)
		browser.OpenURLAsync(targetURL, 800*time.Millisecond)
	}

	// 7. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Printf("Shutting down RelayMesh server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 先关闭 SSE 与终端 WebSocket（它们不受 http.Server.Shutdown 管理或不会被它中断），再排空 HTTP/HTTPS 服务器。
	server.Shutdown(ctx)

	// HTTP 与 HTTPS 并行排空：串行会让后一个端口被前一个的排空时间占住，新进程快速重启时抢不到端口。
	var wg sync.WaitGroup
	drain := func(name string, srv *http.Server) {
		defer wg.Done()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("%s Server forced to shutdown: %v", name, err)
			_ = srv.Close()
		}
	}
	wg.Add(1)
	go drain("HTTP", httpServer)
	if httpsServer != nil {
		wg.Add(1)
		go drain("HTTPS", httpsServer)
	}
	wg.Wait()

	log.Printf("RelayMesh exited cleanly.")
}
