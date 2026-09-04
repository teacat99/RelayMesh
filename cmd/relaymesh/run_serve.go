package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/teacat99/RelayMesh/internal/api"
	"github.com/teacat99/RelayMesh/internal/browser"
	"github.com/teacat99/RelayMesh/internal/cert"
	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/store"
	"github.com/teacat99/RelayMesh/web"
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

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP Server forced to shutdown: %v", err)
	}
	if httpsServer != nil {
		if err := httpsServer.Shutdown(ctx); err != nil {
			log.Printf("HTTPS Server forced to shutdown: %v", err)
		}
	}

	log.Printf("RelayMesh exited cleanly.")
}
