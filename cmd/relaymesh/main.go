package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/store"
)

var version = "dev"

func printUsage() {
	fmt.Fprintf(os.Stderr, `RelayMesh · Agent Communication & Feedback Relay (Version: %s)

Usage:
  relaymesh [command]

Available Commands:
  serve         Start background HTTP/HTTPS server and Web UI (default when no args provided)
  mcp stdio     Start stdio MCP server with local Web control plane
  reset-auth    Reset admin web login credentials to environment defaults
  version       Print RelayMesh version
  help          Show this help message

Examples:
  relaymesh                     # Start standard HTTP/HTTPS service
  relaymesh serve               # Explicitly start standard service
  relaymesh mcp stdio           # Start native stdio MCP server for Cursor / Claude
  relaymesh reset-auth          # Reset authentication credentials
  relaymesh version             # Display version
`, version)
}

func main() {
	args := os.Args[1:]

	// 0. Default invocation: "relaymesh" (no args) -> run standard serve
	if len(args) == 0 {
		runServe()
		return
	}

	switch args[0] {
	case "serve":
		runServe()

	case "mcp":
		if len(args) > 1 && args[1] == "stdio" {
			runStdio()
		} else {
			fmt.Fprintf(os.Stderr, "Error: unknown mcp subcommand %q. Did you mean 'relaymesh mcp stdio'?\n\n", args[1:])
			printUsage()
			os.Exit(1)
		}

	case "version", "--version", "-v":
		fmt.Printf("RelayMesh %s\n", version)

	case "reset-auth", "--reset-auth":
		cfg := config.LoadWithMode("serve")
		cfg.Version = version
		st, err := store.New(cfg.DBPath)
		if err != nil {
			log.Fatalf("Failed to open database %s: %v", cfg.DBPath, err)
		}
		defer st.Close()
		if err := st.ResetAuthCredentials(context.Background()); err != nil {
			log.Fatalf("Failed to reset auth credentials: %v", err)
		}
		fmt.Printf("✅ [RelayMesh] 账号与密码已成功重置！\n")
		fmt.Printf("   生效账号: %s\n", cfg.WebUsername)
		if cfg.WebPassword != "" {
			fmt.Printf("   生效密码: (已从环境变量 RELAYMESH_WEB_PASSWORD 同步)\n")
		} else {
			fmt.Printf("   生效状态: 免密私有直连模式\n")
		}

	case "help", "--help", "-h":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown command %q\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}
