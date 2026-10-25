package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/cli"
	"github.com/teacat99/RelayMesh/v2/internal/buildinfo"
	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// version 由发布流水线通过 -ldflags "-X main.version=…" 注入；没有注入（go run / go install …@vX.Y.Z）时，
// init 里改用 Go 工具链记在二进制里的模块版本（是正式 / 预发布标签才采用，否则仍是 dev），见 internal/buildinfo。
var version = "dev"

func init() { version = buildinfo.Resolve(version) }

// rev 是「Agent 侧代码指纹」（见 internal/agentrev），构建时通过 -ldflags "-X main.rev=…" 注入，只给 `relaymesh agent` 子命令自报用
// （服务端自己不读它：服务端判断主机要不要升级，看的是分发目录里的 REV 文件）。没有注入时为空，服务端回退到比版本号。
var rev = ""

func printUsage() {
	fmt.Fprintf(os.Stderr, `RelayMesh · Agent Communication & Feedback Relay (Version: %s)

Usage:
  relaymesh [command]

Available Commands:
  serve         Start background HTTP/HTTPS server and Web UI (default when no args provided)
  mcp stdio     Start stdio MCP server with local Web control plane
  agent         Run / install the terminal Agent on this machine (see "relaymesh agent help")
  reset-auth    Reset admin web login credentials to environment defaults
  rebind-workflow  Move a workflow to a new project directory / host after migrating a project
  branch-link   Turn an existing session into a branch of another (and "branch-unlink" to undo)
  version       Print RelayMesh version
  help          Show this help message

Examples:
  relaymesh                     # Start standard HTTP/HTTPS service
  relaymesh serve               # Explicitly start standard service
  relaymesh mcp stdio           # Start native stdio MCP server for Cursor / Claude
  relaymesh agent run --server https://relay.example.com   # Let the Web terminal reach this machine
  relaymesh reset-auth          # Reset authentication credentials
  relaymesh rebind-workflow --list   # Show where each workflow is bound (dry-run by default, see --help)
  relaymesh branch-link --list  # Show main/branch session relations (dry-run by default, see --help)
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

	case "agent":
		agent.Version = version
		agent.Rev = rev
		os.Exit(cli.Run(context.Background(), args[1:], cli.Env{
			Version:     version,
			Rev:         rev,
			Prog:        "relaymesh agent",
			ServiceArgs: []string{"agent"},
		}))

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

	case "rebind-workflow":
		os.Exit(runRebindWorkflow(args[1:]))

	case "branch-link":
		os.Exit(runBranchLink(args[1:]))

	case "branch-unlink":
		os.Exit(runBranchUnlink(args[1:]))

	case "help", "--help", "-h":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Error: unknown command %q\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}
