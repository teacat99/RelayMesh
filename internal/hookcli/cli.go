package hookcli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// usageLine 是用法提示；事件子命令的清单跟着 eventSpecs 走，不会漏。
func usageLine() string {
	subs := make([]string, 0, len(eventSpecs))
	for _, spec := range eventSpecs {
		subs = append(subs, spec.Sub)
	}
	return "用法：relaymesh-hook <事件> | install | uninstall | status | version | doctor\n事件（由 Cursor 的 hook 调用）：" + strings.Join(subs, " | ") + "\n"
}

// Main 是 relaymesh-hook 的入口；返回进程退出码。
func Main(args []string) int {
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, usageLine())
		return 2
	}
	cmd := args[1]
	if specForSub(cmd) != nil {
		stdin, _ := io.ReadAll(os.Stdin)
		EmitHookOutput(RunHook(cmd, stdin))
		return 0
	}
	switch cmd {
	case "report":
		// 内部子命令：由 hook 进程分离出来的子进程执行，把一条状态上报发给 Hub（见 async.go）。
		stdin, _ := io.ReadAll(os.Stdin)
		return RunReport(stdin)
	case "install":
		return runInstallArgs(args[2:])
	case "uninstall":
		return runUninstallArgs(args[2:])
	case "status":
		return runStatusArgs(args[2:])
	case "doctor":
		fs := flag.NewFlagSet("doctor", flag.ExitOnError)
		project := fs.String("project", "", "")
		_ = fs.Parse(args[2:])
		return RunDoctor(*project)
	case "version":
		fmt.Println(Version())
		return 0
	default:
		fmt.Fprint(os.Stderr, usageLine())
		return 2
	}
}

func runInstallArgs(argv []string) int {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	workflow := fs.String("workflow", "", "")
	mcpURL := fs.String("mcp-url", "", "")
	mcpServer := fs.String("mcp-server", "", "")
	tokenEnv := fs.String("token-env", "", "")
	interval := fs.Float64("interval", -1, "")
	project := fs.String("project", "", "")
	scope := fs.String("scope", "project", "project|user")
	dryRun := fs.Bool("dry-run", false, "")
	preview := fs.Bool("preview", false, "")
	noStatus := fs.Bool("no-status", false, "")
	command := fs.String("command", "", "")
	user := fs.Bool("user", false, "")
	_ = fs.Parse(argv)
	var iv *float64
	if *interval >= 0 {
		iv = interval
	}
	userScope := *user || strings.EqualFold(*scope, "user")
	return RunInstall(InstallOptions{
		Workflow:  *workflow,
		McpURL:    *mcpURL,
		McpServer: *mcpServer,
		TokenEnv:  *tokenEnv,
		Interval:  iv,
		Project:   *project,
		UserScope: userScope,
		DryRun:    *dryRun,
		Preview:   *preview,
		NoStatus:  *noStatus,
		Command:   *command,
	})
}

func runUninstallArgs(argv []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	project := fs.String("project", "", "")
	user := fs.Bool("user", false, "")
	purge := fs.Bool("purge", false, "")
	scope := fs.String("scope", "project", "")
	_ = fs.Parse(argv)
	userScope := *user || strings.EqualFold(*scope, "user")
	return RunUninstall(*project, userScope, *purge)
}

func runStatusArgs(argv []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	project := fs.String("project", "", "")
	jsonOut := fs.Bool("json", false, "")
	_ = fs.Parse(argv)
	return RunStatus(*project, *jsonOut)
}
