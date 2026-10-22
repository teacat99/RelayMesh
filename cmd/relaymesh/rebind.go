package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// relaymesh rebind-workflow：项目换了目录、或搬到另一台机器之后，把旧工作流改绑到新的「项目目录 + 主机」，
// 让跟踪的会话跟着走，而不是留在原地、在新位置「查不到 / 写不进」。
//
// 它直接操作数据库文件（与 reset-auth 同类的运维命令），默认只预演；加 --apply 才写入，
// 并且写入前一定先在数据库同目录生成一份快照。改绑刻意不做成 MCP 工具：它会绕开 D-161 的跨项目隔离。

const rebindUsage = `用法：
  relaymesh rebind-workflow --list
  relaymesh rebind-workflow --workflow <id> --to-dir <新项目目录> [--to-host <新主机名>] [--apply]

选项：
  --list              列出每个工作流当前绑定的目录 / 主机，以及各凭据登记的主机名（不含令牌）
  --workflow <id>     要改绑的工作流
  --to-dir <目录>     新的项目目录，必须是绝对路径
  --to-host <主机名>  新的主机名；不填则保持原主机。会话的主机取自所用凭据登记的主机名，请与之保持一致
  --apply             真正写入。不加则只预演，不改动任何数据
  --db <路径>         数据库文件；默认取 RELAYMESH_DB_PATH 对应的路径

说明：
  - 写入前会在数据库同目录生成快照 <db>.rebind-<工作流>-<时间>.bak，改绑可原路改回，也可用快照整体恢复。
  - 只改这个工作流名下会话与排队反馈的目录 / 主机，不动正文、不动更新时间、不动其他工作流。
`

type rebindOptions struct {
	DB       string
	List     bool
	Workflow string
	ToDir    string
	ToHost   string
	Apply    bool
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// runRebindWorkflow 是命令入口：解析参数、打开数据库、交给 executeRebind。返回进程退出码。
func runRebindWorkflow(args []string) int {
	return runRebindWorkflowTo(args, os.Stdout, os.Stderr)
}

func runRebindWorkflowTo(args []string, out, errw io.Writer) int {
	opts, code := parseRebindArgs(args, errw)
	if code >= 0 {
		return code
	}

	dbPath := opts.DB
	if dbPath == "" {
		dbPath = config.LoadWithMode("serve").DBPath
	}
	// store.New 会在路径不存在时新建空库；运维命令绝不能因为路径写错而悄悄造出一个空数据库。
	if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
		fmt.Fprintf(errw, "错误：数据库文件不存在或不是文件：%s\n", dbPath)
		return 1
	}
	st, err := store.New(dbPath)
	if err != nil {
		fmt.Fprintf(errw, "错误：无法打开数据库 %s：%v\n", dbPath, err)
		return 1
	}
	defer st.Close()
	return executeRebind(context.Background(), st, dbPath, opts, out, errw, time.Now)
}

// parseRebindArgs 解析并校验参数；返回的 code >= 0 表示应当直接以该退出码结束（含 -h）。
func parseRebindArgs(args []string, errw io.Writer) (rebindOptions, int) {
	var opts rebindOptions
	fs := flag.NewFlagSet("rebind-workflow", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.DB, "db", "", "")
	fs.BoolVar(&opts.List, "list", false, "")
	fs.StringVar(&opts.Workflow, "workflow", "", "")
	fs.StringVar(&opts.ToDir, "to-dir", "", "")
	fs.StringVar(&opts.ToHost, "to-host", "", "")
	fs.BoolVar(&opts.Apply, "apply", false, "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(errw, rebindUsage)
			return opts, 0
		}
		fmt.Fprintf(errw, "错误：%v\n\n%s", err, rebindUsage)
		return opts, 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errw, "错误：多余的参数 %q\n\n%s", fs.Args(), rebindUsage)
		return opts, 2
	}
	if opts.List {
		if opts.Workflow != "" || opts.ToDir != "" || opts.ToHost != "" || opts.Apply {
			fmt.Fprintf(errw, "错误：--list 不能和改绑参数一起使用\n\n%s", rebindUsage)
			return opts, 2
		}
		return opts, -1
	}
	if strings.TrimSpace(opts.Workflow) == "" || strings.TrimSpace(opts.ToDir) == "" {
		fmt.Fprintf(errw, "错误：必须同时给出 --workflow 与 --to-dir（或使用 --list 查看现状）\n\n%s", rebindUsage)
		return opts, 2
	}
	return opts, -1
}

// executeRebind 执行 --list / 预演 / 改绑，并把过程与回滚办法打印出来。
func executeRebind(ctx context.Context, st *store.Store, dbPath string, opts rebindOptions, out, errw io.Writer, now func() time.Time) int {
	if opts.List {
		return listBindings(ctx, st, out, errw)
	}

	req := store.RebindRequest{WorkflowID: opts.Workflow, ToDir: opts.ToDir, ToHost: opts.ToHost}
	plan, err := st.RebindWorkflow(ctx, req, false)
	if err != nil {
		fmt.Fprintf(errw, "错误：%v\n", err)
		return 1
	}

	fmt.Fprintf(out, "工作流 %s\n", plan.Before.WorkflowID)
	fmt.Fprintf(out, "  当前：%s\n", describeBinding(plan.Before))
	target := "目录 " + plan.TargetDir
	if plan.TargetHost != "" {
		target += "；主机 " + plan.TargetHost
	} else {
		target += "；主机保持不变"
	}
	fmt.Fprintf(out, "  目标：%s\n", target)
	fmt.Fprintf(out, "  将改写：会话 %d 条，排队反馈 %d 条\n", plan.SessionsToMove, plan.QueuedToMove)

	for _, w := range rebindWarnings(ctx, st, plan) {
		fmt.Fprintf(out, "  注意：%s\n", w)
	}

	if plan.Noop() {
		fmt.Fprintln(out, "已经绑定在目标上，无需改写。")
		return 0
	}
	if !opts.Apply {
		fmt.Fprintln(out, "仅预演，没有写入任何数据。确认无误后加上 --apply 执行（执行前会先生成数据库快照）。")
		return 0
	}

	snapshot := fmt.Sprintf("%s.rebind-%s-%s.bak", dbPath, unsafeFileChars.ReplaceAllString(plan.Before.WorkflowID, "_"), now().Format("20060102-150405"))
	if err := st.SnapshotTo(ctx, snapshot); err != nil {
		fmt.Fprintf(errw, "错误：生成快照失败，已放弃改绑（未写入任何数据）：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "已生成快照：%s\n", snapshot)

	res, err := st.RebindWorkflow(ctx, req, true)
	if err != nil {
		fmt.Fprintf(errw, "错误：改绑失败，事务已回滚（数据未变）：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "已改绑：会话 %d 条，排队反馈 %d 条。\n", res.SessionsToMove, res.QueuedToMove)
	fmt.Fprintf(out, "  现在：%s\n", describeBinding(res.After))

	fmt.Fprintln(out, "回滚办法：")
	if len(plan.Before.Dirs) == 1 {
		hostArg := ""
		if len(plan.Before.Hosts) == 1 {
			hostArg = fmt.Sprintf(" --to-host %q", plan.Before.Hosts[0])
		}
		fmt.Fprintf(out, "  1) 原路改回：relaymesh rebind-workflow --workflow %q --to-dir %q%s --apply\n", plan.Before.WorkflowID, plan.Before.Dirs[0], hostArg)
	} else {
		fmt.Fprintln(out, "  1) 改绑前这个工作流绑定在多个目录 / 主机上，无法一条命令原路改回，请用快照恢复。")
	}
	fmt.Fprintf(out, "  2) 整体恢复：先停止服务，删除 %s-wal 与 %s-shm，再用快照 %s 覆盖主库文件。\n", dbPath, dbPath, snapshot)
	return 0
}

func listBindings(ctx context.Context, st *store.Store, out, errw io.Writer) int {
	list, err := st.ListWorkflowBindings(ctx)
	if err != nil {
		fmt.Fprintf(errw, "错误：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "共 %d 个工作流（最近活跃的在前）：\n", len(list))
	for i, b := range list {
		fmt.Fprintf(out, "[%d] %s\n    %s\n", i+1, b.WorkflowID, describeBinding(b))
	}

	creds, err := st.ListCredentialHosts(ctx)
	if err != nil {
		fmt.Fprintf(errw, "错误：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "\n各凭据登记的主机名（改绑的 --to-host 应与目标机器所用凭据的主机名一致）：\n")
	if len(creds) == 0 {
		fmt.Fprintln(out, "  （没有凭据）")
	}
	for _, c := range creds {
		host := c.HostName
		if host == "" {
			host = "（未登记）"
		}
		state := ""
		if !c.Active {
			state = "  [已停用]"
		}
		fmt.Fprintf(out, "  %s → %s%s\n", c.Name, host, state)
	}
	return 0
}

func describeBinding(b store.WorkflowBinding) string {
	dirs := "（空）"
	if len(b.Dirs) > 0 {
		dirs = strings.Join(b.Dirs, "、")
	}
	hosts := "（未记录）"
	if len(b.Hosts) > 0 {
		hosts = strings.Join(b.Hosts, "、")
	}
	return fmt.Sprintf("目录 %s；主机 %s；会话 %d 条（等待回复 %d 条），排队反馈 %d 条", dirs, hosts, b.Sessions, b.Pending, b.Queued)
}

// rebindWarnings 给出不阻断、但值得运维看一眼的提示。
func rebindWarnings(ctx context.Context, st *store.Store, plan *store.RebindResult) []string {
	var out []string
	if plan.Before.Pending > 0 {
		out = append(out, fmt.Sprintf("这个工作流还有 %d 条等待回复的会话。改绑后，仍在旧目录里运行的 AI 再调用 interactive_feedback 会被项目隔离（D-161）拒绝；请在新目录里继续这个工作流。", plan.Before.Pending))
	}
	if len(plan.Before.Dirs) > 1 || len(plan.Before.Hosts) > 1 {
		out = append(out, "这个工作流目前绑定在多个目录或主机上，改绑会把它们统一收敛到目标。")
	}
	if plan.TargetHost != "" {
		if creds, err := st.ListCredentialHosts(ctx); err == nil {
			known := false
			for _, c := range creds {
				if c.HostName == plan.TargetHost {
					known = true
					break
				}
			}
			if !known {
				out = append(out, fmt.Sprintf("目标主机 %q 没有出现在任何凭据登记的主机名里。新位置的会话主机取自所用凭据，两者不一致时按主机过滤会查不到；可先用 --list 核对。", plan.TargetHost))
			}
		}
	}
	return out
}
