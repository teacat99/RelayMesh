package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/internal/api"
	"github.com/teacat99/RelayMesh/internal/cert"
	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/mcp"
	"github.com/teacat99/RelayMesh/internal/store"
	"github.com/teacat99/RelayMesh/web"
)

func runStdio() {
	// STDIO-001: Guarantee stdout purity. All logs, banners, and errors MUST go to stderr.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime)
	gin.DefaultWriter = os.Stderr
	gin.DefaultErrorWriter = os.Stderr

	cfg := config.LoadWithMode("stdio")
	cfg.Version = version

	// 1. Port Conflict Check: Fail Fast if port is already in use
	if err := checkPortAvailable(cfg.Host, cfg.Port); err != nil {
		fmt.Fprintf(os.Stderr, "[RelayMesh] Error: Local control plane port :%d is already in use.\n", cfg.Port)
		fmt.Fprintf(os.Stderr, "Use the existing Streamable HTTP MCP endpoint (http://127.0.0.1:%d/mcp) or stop the existing RelayMesh instance.\n", cfg.Port)
		os.Exit(1)
	}

	log.Printf("[RelayMesh stdio] Starting RelayMesh MCP stdio server (v%s)", version)
	log.Printf("[RelayMesh stdio] Data directory: %s", cfg.DataDir)

	// 2. Initialize SQLite Store
	st, err := store.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("[RelayMesh stdio] Failed to initialize store at %s: %v", cfg.DBPath, err)
	}
	defer st.Close()

	if cfg.MCPToken != "" {
		if _, err := st.EnsureEnvMCPCredential(context.Background(), cfg.MCPToken); err != nil {
			log.Printf("[RelayMesh stdio] Warning: failed to ensure env MCP credential: %v", err)
		}
	}

	// 3. Prepare embedded frontend assets
	var staticFS fs.FS
	if distFS, err := fs.Sub(web.FS, "dist"); err == nil {
		staticFS = distFS
	} else {
		log.Printf("[RelayMesh stdio] Warning: failed to load embedded web assets: %v", err)
	}

	// 4. Initialize HTTP & MCP Server (Sharing the same mcp.Server instance)
	server := api.NewServer(cfg, st, staticFS)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      server.Engine(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 600 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 5. Start Localhost Control Plane in background
	go func() {
		log.Printf("[RelayMesh stdio] Local control plane ready at http://%s/", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[RelayMesh stdio] HTTP server error: %v", err)
		}
	}()

	// 6. Optional HTTPS for secure mic access
	var httpsServer *http.Server
	if cfg.TLSEnabled && cfg.HTTPSPort > 0 {
		if checkPortAvailable(cfg.Host, cfg.HTTPSPort) == nil {
			tlsCert, err := cert.GetOrCreateTLSCertificate(cfg.TLSCertPath, cfg.TLSKeyPath)
			if err == nil {
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
					log.Printf("[RelayMesh stdio] HTTPS control plane ready at https://%s/", httpsAddr)
					if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
						log.Printf("[RelayMesh stdio] HTTPS server error: %v", err)
					}
				}()
			}
		}
	}

	// 7. Lifecycle Context & Signal Handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// 8. Run stdio Transport (Blocks until stdin EOF or ctx cancellation)
	if err := mcp.RunStdio(ctx, server.MCPServer(), os.Stdin, os.Stdout); err != nil && err != context.Canceled {
		log.Printf("[RelayMesh stdio] stdio transport exited with error: %v", err)
	}

	// 9. Graceful Shutdown on EOF / Termination
	log.Printf("[RelayMesh stdio] Shutting down control plane...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()

	_ = httpServer.Shutdown(shutdownCtx)
	if httpsServer != nil {
		_ = httpsServer.Shutdown(shutdownCtx)
	}

	log.Printf("[RelayMesh stdio] Exited cleanly.")
}

func checkPortAvailable(host string, port int) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	_ = ln.Close()
	return nil
}
