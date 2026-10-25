package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// relaymesh branch-link / branch-unlink：把已有的两个会话（工作流）连成「主会话 ↔ 分支」，或解除这层关系（#236）。
//
// 用途是迁移：以前两个会话已经各自在跑、靠人手动协调，现在想让其中一个成为另一个的分支，不必重建。
// 与 rebind-workflow 同类：直接操作数据库文件，默认只预演，--apply 才写入，写入前一定先在数据库同目录生成快照。
// 它刻意不做成 MCP 工具：把一个会话挂到另一个会话名下会改变两边的可见范围，必须由人在服务器上确认。
// 迁移建立的关系默认只开放最小范围（仅可见：主会话能看到分支的状态与历史摘要，不能给它发消息）；要放宽，到网页的分支设置里改。

const branchLinkUsage = `用法：
  relaymesh branch-link --list
  relaymesh branch-link --main <主会话工作流> --branch <分支工作流> [--expose visible|collab|managed] [--no-copy] [--apply]
  relaymesh branch-unlink --branch <分支工作流> [--apply]

选项：
  --list              列出现有的分支关系（含已结束的），以及最近活跃的工作流（用来挑编号，不含正文）
  --main <id>         主会话的工作流编号
  --branch <id>       分支的工作流编号（它的主会话只能有一个；分支不能再有分支，只允许一层）
  --expose <预设>     向主会话开放的范围：visible 仅可见（默认）/ collab 协作 / managed 托管
  --no-copy           不把用户在分支页里的发言抄送给主会话（默认抄送）
  --apply             真正写入。不加则只预演，不改动任何数据
  --db <路径>         数据库文件；默认取 RELAYMESH_DB_PATH 对应的路径

说明：
  - 预演跑的是与写入完全相同的校验（两个工作流都存在、不是同一个、只允许一层、活动分支数上限），所以预演通过写入就会通过。
  - 写入前会在数据库同目录生成快照 <db>.branch-link-<分支>-<时间>.bak；关系可用 branch-unlink 解除，也可用快照整体恢复。
  - 已结束的关系再连同一个主会话 = 重新打开；连另一个主会话 = 冲突，需要先 branch-unlink。
  - 关系不改动两个会话各自的历史与凭据；解除关系也不删除任何会话。
`

const branchUnlinkUsage = `用法：
  relaymesh branch-unlink --branch <分支工作流> [--apply] [--db <路径>]

说明：
  - 解除这个分支与它主会话之间的关系（删除关系行；两个会话本身与它们的历史都不动）。
  - 两个会话之间已经发出的分支消息仍留在各自的收件箱里，只是之后不再有「主会话 / 分支」的标注与互发能力。
  - 默认只预演；--apply 才写入，写入前先生成数据库快照。
`

type branchLinkOptions struct {
	DB     string
	List   bool
	Main   string
	Branch string
	Expose string
	NoCopy bool
	Apply  bool
}

type branchUnlinkOptions struct {
	DB     string
	Branch string
	Apply  bool
}

// openOpsStore 打开运维命令要操作的数据库；路径不存在时拒绝（store.New 会在路径不存在时新建空库，运维命令绝不能悄悄造出一个）。
func openOpsStore(dbPath string, errw io.Writer) (*store.Store, string, int) {
	if dbPath == "" {
		dbPath = config.LoadWithMode("serve").DBPath
	}
	if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
		fmt.Fprintf(errw, "错误：数据库文件不存在或不是文件：%s\n", dbPath)
		return nil, dbPath, 1
	}
	st, err := store.New(dbPath)
	if err != nil {
		fmt.Fprintf(errw, "错误：无法打开数据库 %s：%v\n", dbPath, err)
		return nil, dbPath, 1
	}
	return st, dbPath, -1
}

func runBranchLink(args []string) int { return runBranchLinkTo(args, os.Stdout, os.Stderr) }

func runBranchUnlink(args []string) int { return runBranchUnlinkTo(args, os.Stdout, os.Stderr) }

func runBranchLinkTo(args []string, out, errw io.Writer) int {
	opts, code := parseBranchLinkArgs(args, errw)
	if code >= 0 {
		return code
	}
	st, dbPath, code := openOpsStore(opts.DB, errw)
	if code >= 0 {
		return code
	}
	defer st.Close()
	return executeBranchLink(context.Background(), st, dbPath, opts, out, errw, time.Now)
}

func runBranchUnlinkTo(args []string, out, errw io.Writer) int {
	opts, code := parseBranchUnlinkArgs(args, errw)
	if code >= 0 {
		return code
	}
	st, dbPath, code := openOpsStore(opts.DB, errw)
	if code >= 0 {
		return code
	}
	defer st.Close()
	return executeBranchUnlink(context.Background(), st, dbPath, opts, out, errw, time.Now)
}

// parseBranchLinkArgs 解析并校验参数；返回的 code >= 0 表示应当直接以该退出码结束（含 -h）。
func parseBranchLinkArgs(args []string, errw io.Writer) (branchLinkOptions, int) {
	var opts branchLinkOptions
	fs := flag.NewFlagSet("branch-link", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.DB, "db", "", "")
	fs.BoolVar(&opts.List, "list", false, "")
	fs.StringVar(&opts.Main, "main", "", "")
	fs.StringVar(&opts.Branch, "branch", "", "")
	fs.StringVar(&opts.Expose, "expose", "", "")
	fs.BoolVar(&opts.NoCopy, "no-copy", false, "")
	fs.BoolVar(&opts.Apply, "apply", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(errw, branchLinkUsage)
			return opts, 0
		}
		fmt.Fprintf(errw, "错误：%v\n\n%s", err, branchLinkUsage)
		return opts, 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errw, "错误：多余的参数 %q\n\n%s", fs.Args(), branchLinkUsage)
		return opts, 2
	}
	if opts.List {
		if opts.Main != "" || opts.Branch != "" || opts.Expose != "" || opts.NoCopy || opts.Apply {
			fmt.Fprintf(errw, "错误：--list 不能和建立关系的参数一起使用\n\n%s", branchLinkUsage)
			return opts, 2
		}
		return opts, -1
	}
	if strings.TrimSpace(opts.Main) == "" || strings.TrimSpace(opts.Branch) == "" {
		fmt.Fprintf(errw, "错误：必须同时给出 --main 与 --branch（或使用 --list 查看现状）\n\n%s", branchLinkUsage)
		return opts, 2
	}
	if e := strings.TrimSpace(opts.Expose); e != "" {
		if _, ok := model.BranchExposePreset(e); !ok {
			fmt.Fprintf(errw, "错误：--expose 只能是 visible / collab / managed，收到 %q\n\n%s", opts.Expose, branchLinkUsage)
			return opts, 2
		}
	}
	return opts, -1
}

func parseBranchUnlinkArgs(args []string, errw io.Writer) (branchUnlinkOptions, int) {
	var opts branchUnlinkOptions
	fs := flag.NewFlagSet("branch-unlink", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.DB, "db", "", "")
	fs.StringVar(&opts.Branch, "branch", "", "")
	fs.BoolVar(&opts.Apply, "apply", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(errw, branchUnlinkUsage)
			return opts, 0
		}
		fmt.Fprintf(errw, "错误：%v\n\n%s", err, branchUnlinkUsage)
		return opts, 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errw, "错误：多余的参数 %q\n\n%s", fs.Args(), branchUnlinkUsage)
		return opts, 2
	}
	if strings.TrimSpace(opts.Branch) == "" {
		fmt.Fprintf(errw, "错误：必须给出 --branch\n\n%s", branchUnlinkUsage)
		return opts, 2
	}
	return opts, -1
}

// presetLabel 把开放范围的预设名写成「id（中文名）」；没有对应预设的组合叫「自定义」。
func presetLabel(name string) string {
	switch name {
	case model.ExposePresetVisible:
		return "visible（仅可见）"
	case model.ExposePresetCollab:
		return "collab（协作）"
	case model.ExposePresetManaged:
		return "managed（托管）"
	}
	return "自定义"
}

// wfLabel 把工作流写成「标题（编号）」；没有标题只写编号。
func wfLabel(ctx context.Context, st *store.Store, wf string) string {
	if t := strings.TrimSpace(st.WorkflowTitleOf(ctx, wf)); t != "" {
		return fmt.Sprintf("「%s」（%s）", t, wf)
	}
	return wf
}

func describeBranch(ctx context.Context, st *store.Store, b model.WorkflowBranch) string {
	state := "生效"
	if !b.Active() {
		state = "已结束"
	}
	preset := presetLabel(b.Expose.PresetName())
	copyNotes := "抄送用户发言"
	if !b.CopyUserNotes {
		copyNotes = "不抄送用户发言"
	}
	return fmt.Sprintf("[%s] 主会话 %s ← 分支 %s；开放范围 %s（%s）；%s；创建者 %s，%s",
		state, wfLabel(ctx, st, b.ParentWorkflowID), wfLabel(ctx, st, b.BranchWorkflowID), preset, b.Expose.Summary(), copyNotes,
		b.CreatedBy, b.CreatedAt.Format("2006-01-02 15:04"))
}

// executeBranchLink 执行 --list / 预演 / 写入，并把过程与回滚办法打印出来。
func executeBranchLink(ctx context.Context, st *store.Store, dbPath string, opts branchLinkOptions, out, errw io.Writer, now func() time.Time) int {
	if opts.List {
		return listBranches(ctx, st, out, errw)
	}
	in := store.LinkBranchInput{
		ParentWorkflowID: strings.TrimSpace(opts.Main),
		BranchWorkflowID: strings.TrimSpace(opts.Branch),
		CreatedBy:        model.BranchCreatedByMigrate,
		SkipRateLimit:    true,
	}
	if e := strings.TrimSpace(opts.Expose); e != "" {
		p, _ := model.BranchExposePreset(e)
		in.Expose = &p
	}
	if opts.NoCopy {
		off := false
		in.CopyUserNotes = &off
	}

	plan, err := st.PlanBranchLink(ctx, in)
	if err != nil {
		fmt.Fprintf(errw, "错误：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "主会话：%s\n", wfLabel(ctx, st, in.ParentWorkflowID))
	fmt.Fprintf(out, "分支：  %s\n", wfLabel(ctx, st, in.BranchWorkflowID))
	b := plan.Branch
	fmt.Fprintf(out, "开放范围：%s（%s）\n", presetLabel(b.Expose.PresetName()), b.Expose.Summary())
	if b.CopyUserNotes {
		fmt.Fprintln(out, "抄送：用户在分支页里的发言会抄送给主会话")
	} else {
		fmt.Fprintln(out, "抄送：关闭")
	}
	switch {
	case plan.Noop():
		fmt.Fprintln(out, "这对关系已经存在且生效，无需改动。")
		return 0
	case plan.Reopened:
		fmt.Fprintln(out, "结果：将重新打开一条已结束的关系（沿用原来的开放范围，除非命令里显式给了 --expose / --no-copy）。")
	default:
		fmt.Fprintln(out, "结果：将新建这条关系。")
	}
	if !opts.Apply {
		fmt.Fprintln(out, "仅预演，没有写入任何数据。确认无误后加上 --apply 执行（执行前会先生成数据库快照）。")
		return 0
	}

	snapshot := fmt.Sprintf("%s.branch-link-%s-%s.bak", dbPath, unsafeFileChars.ReplaceAllString(in.BranchWorkflowID, "_"), now().Format("20060102-150405"))
	if err := st.SnapshotTo(ctx, snapshot); err != nil {
		fmt.Fprintf(errw, "错误：生成快照失败，已放弃（未写入任何数据）：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "已生成快照：%s\n", snapshot)
	res, err := st.LinkBranch(ctx, in)
	if err != nil {
		fmt.Fprintf(errw, "错误：写入失败，事务已回滚（数据未变）：%v\n", err)
		return 1
	}
	verb := "已新建"
	if res.Reopened {
		verb = "已重新打开"
	}
	fmt.Fprintf(out, "%s：%s\n", verb, describeBranch(ctx, st, res.Branch))
	fmt.Fprintln(out, "回滚办法：")
	fmt.Fprintf(out, "  1) 解除关系：relaymesh branch-unlink --branch %q --apply\n", in.BranchWorkflowID)
	fmt.Fprintf(out, "  2) 整体恢复：先停止服务，删除 %s-wal 与 %s-shm，再用快照 %s 覆盖主库文件。\n", dbPath, dbPath, snapshot)
	return 0
}

func listBranches(ctx context.Context, st *store.Store, out, errw io.Writer) int {
	rows, err := st.ListAllBranches(ctx)
	if err != nil {
		fmt.Fprintf(errw, "错误：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "分支关系共 %d 条（最新的在前）：\n", len(rows))
	if len(rows) == 0 {
		fmt.Fprintln(out, "  （没有）")
	}
	for i, b := range rows {
		fmt.Fprintf(out, "[%d] %s\n", i+1, describeBranch(ctx, st, b))
	}

	list, err := st.ListWorkflowBindings(ctx)
	if err != nil {
		fmt.Fprintf(errw, "错误：%v\n", err)
		return 1
	}
	const show = 30
	fmt.Fprintf(out, "\n最近活跃的工作流（共 %d 个，列出最近 %d 个，用来挑 --main / --branch 的编号）：\n", len(list), min(show, len(list)))
	for i, w := range list {
		if i >= show {
			break
		}
		fmt.Fprintf(out, "  %s\n", wfLabel(ctx, st, w.WorkflowID))
	}
	return 0
}

// executeBranchUnlink 执行 branch-unlink：预演只显示将被解除的关系；--apply 先快照再删除。
func executeBranchUnlink(ctx context.Context, st *store.Store, dbPath string, opts branchUnlinkOptions, out, errw io.Writer, now func() time.Time) int {
	branch := strings.TrimSpace(opts.Branch)
	row, err := st.UnlinkBranch(ctx, branch, true, model.BranchCreatedByMigrate)
	if err != nil {
		fmt.Fprintf(errw, "错误：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "将解除：%s\n", describeBranch(ctx, st, *row))
	if !opts.Apply {
		fmt.Fprintln(out, "仅预演，没有写入任何数据。确认无误后加上 --apply 执行（执行前会先生成数据库快照）。")
		return 0
	}
	snapshot := fmt.Sprintf("%s.branch-unlink-%s-%s.bak", dbPath, unsafeFileChars.ReplaceAllString(branch, "_"), now().Format("20060102-150405"))
	if err := st.SnapshotTo(ctx, snapshot); err != nil {
		fmt.Fprintf(errw, "错误：生成快照失败，已放弃（未写入任何数据）：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "已生成快照：%s\n", snapshot)
	if _, err := st.UnlinkBranch(ctx, branch, false, model.BranchCreatedByMigrate); err != nil {
		fmt.Fprintf(errw, "错误：解除失败，事务已回滚（数据未变）：%v\n", err)
		return 1
	}
	fmt.Fprintln(out, "已解除。")
	fmt.Fprintln(out, "回滚办法：")
	fmt.Fprintf(out, "  1) 重新建立：relaymesh branch-link --main %q --branch %q --apply\n", row.ParentWorkflowID, row.BranchWorkflowID)
	fmt.Fprintf(out, "  2) 整体恢复：先停止服务，删除 %s-wal 与 %s-shm，再用快照 %s 覆盖主库文件。\n", dbPath, dbPath, snapshot)
	return 0
}
