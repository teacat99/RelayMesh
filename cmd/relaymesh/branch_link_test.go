package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func runLink(t *testing.T, st *store.Store, dbPath string, opts branchLinkOptions) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := executeBranchLink(context.Background(), st, dbPath, opts, &out, &errw, fixedNow)
	return code, out.String(), errw.String()
}

func runUnlink(t *testing.T, st *store.Store, dbPath string, opts branchUnlinkOptions) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := executeBranchUnlink(context.Background(), st, dbPath, opts, &out, &errw, fixedNow)
	return code, out.String(), errw.String()
}

func branchRows(t *testing.T, st *store.Store) []model.WorkflowBranch {
	t.Helper()
	rows, err := st.ListAllBranches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestBranchLinkList(t *testing.T) {
	dbPath, st := newRebindDB(t)
	code, out, errOut := runLink(t, st, dbPath, branchLinkOptions{List: true})
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d err=%q", code, errOut)
	}
	for _, want := range []string{"分支关系共 0 条", "（没有）", "wf-main", "wf-other", "最近活跃的工作流"} {
		if !strings.Contains(out, want) {
			t.Errorf("--list 输出缺少 %q:\n%s", want, out)
		}
	}
	creds, err := st.ListCredentials(context.Background())
	if err != nil || len(creds) != 1 || creds[0].Token == "" {
		t.Fatalf("前置条件：应有一个带令牌的凭据 %+v %v", creds, err)
	}
	if strings.Contains(out, creds[0].Token) {
		t.Fatalf("--list 输出不应包含令牌")
	}
}

func TestBranchLinkDryRunWritesNothing(t *testing.T) {
	dbPath, st := newRebindDB(t)
	code, out, errOut := runLink(t, st, dbPath, branchLinkOptions{Main: "wf-main", Branch: "wf-other"})
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d err=%q", code, errOut)
	}
	for _, want := range []string{"仅预演", "将新建这条关系", "wf-main", "wf-other", "visible", "抄送"} {
		if !strings.Contains(out, want) {
			t.Errorf("预演输出缺少 %q:\n%s", want, out)
		}
	}
	if n := len(branchRows(t, st)); n != 0 {
		t.Fatalf("预演不应写关系行，得到 %d", n)
	}
	if snaps, _ := filepath.Glob(dbPath + ".branch-link-*"); len(snaps) != 0 {
		t.Fatalf("预演不应生成快照: %v", snaps)
	}
}

func TestBranchLinkApplyTakesSnapshotThenLinks(t *testing.T) {
	dbPath, st := newRebindDB(t)
	code, out, errOut := runLink(t, st, dbPath, branchLinkOptions{Main: "wf-main", Branch: "wf-other", Apply: true})
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d err=%q\n%s", code, errOut, out)
	}
	snaps, _ := filepath.Glob(dbPath + ".branch-link-wf-other-20261009-083000.bak")
	if len(snaps) != 1 {
		t.Fatalf("应生成一份带分支与时间的快照，实际 %v\n%s", snaps, out)
	}
	// 快照里还没有这条关系
	snap, err := store.New(snaps[0])
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if n := len(branchRows(t, snap)); n != 0 {
		t.Fatalf("快照应保存建立之前的状态（无关系），得到 %d", n)
	}
	rows := branchRows(t, st)
	if len(rows) != 1 {
		t.Fatalf("现库应有 1 条关系: %+v", rows)
	}
	r := rows[0]
	if r.ParentWorkflowID != "wf-main" || r.BranchWorkflowID != "wf-other" || r.CreatedBy != model.BranchCreatedByMigrate || !r.Active() {
		t.Fatalf("关系不对: %+v", r)
	}
	if r.Expose.PresetName() != model.ExposePresetVisible || !r.CopyUserNotes {
		t.Fatalf("迁移默认应是仅可见 + 抄送: %+v", r)
	}
	for _, want := range []string{"已生成快照", "已新建", "回滚办法", `relaymesh branch-unlink --branch "wf-other" --apply`, "整体恢复"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 审计里有一条 branch.link，且主体是迁移命令
	var evs []model.AuditEvent
	st.DB().Where("action = ?", store.AuditBranchLink).Find(&evs)
	if len(evs) != 1 || evs[0].ActorRef != "branch-link" {
		t.Fatalf("应记一条迁移命令发起的 branch.link 审计: %+v", evs)
	}
	// 两个会话各自的历史不受影响
	b, _ := st.WorkflowBindingOf(context.Background(), "wf-main")
	if b.Sessions != 3 {
		t.Fatalf("建立关系不应动会话: %+v", b)
	}
}

func TestBranchLinkApplyIsIdempotent(t *testing.T) {
	dbPath, st := newRebindDB(t)
	if code, out, errOut := runLink(t, st, dbPath, branchLinkOptions{Main: "wf-main", Branch: "wf-other", Apply: true}); code != 0 {
		t.Fatalf("第一次: exit=%d err=%q\n%s", code, errOut, out)
	}
	code, out, errOut := runLink(t, st, dbPath, branchLinkOptions{Main: "wf-main", Branch: "wf-other", Apply: true})
	if code != 0 || errOut != "" || !strings.Contains(out, "无需改动") {
		t.Fatalf("第二次应是空操作: exit=%d err=%q\n%s", code, errOut, out)
	}
	if n := len(branchRows(t, st)); n != 1 {
		t.Fatalf("应仍只有 1 条关系，得到 %d", n)
	}
	if snaps, _ := filepath.Glob(dbPath + ".branch-link-*"); len(snaps) != 1 {
		t.Fatalf("空操作不应再生成快照: %v", snaps)
	}
}

func TestBranchLinkRejectsViolationsBeforeSnapshot(t *testing.T) {
	dbPath, st := newRebindDB(t)
	cases := []struct {
		name string
		opts branchLinkOptions
		want string
	}{
		{"自己连自己", branchLinkOptions{Main: "wf-main", Branch: "wf-main", Apply: true}, "自己的主会话"},
		{"分支不存在", branchLinkOptions{Main: "wf-main", Branch: "wf-ghost", Apply: true}, "wf-ghost"},
		{"主会话不存在", branchLinkOptions{Main: "wf-ghost", Branch: "wf-other", Apply: true}, "wf-ghost"},
	}
	for _, c := range cases {
		code, out, errOut := runLink(t, st, dbPath, c.opts)
		if code != 1 || !strings.Contains(errOut, c.want) {
			t.Errorf("%s：应以 1 退出并提示 %q，exit=%d err=%q out=%q", c.name, c.want, code, errOut, out)
		}
	}
	if snaps, _ := filepath.Glob(dbPath + ".branch-link-*"); len(snaps) != 0 {
		t.Fatalf("校验不过时不应生成快照: %v", snaps)
	}
	if n := len(branchRows(t, st)); n != 0 {
		t.Fatalf("不应写入任何关系，得到 %d", n)
	}
}

func TestBranchLinkExposeAndNoCopy(t *testing.T) {
	dbPath, st := newRebindDB(t)
	code, out, errOut := runLink(t, st, dbPath, branchLinkOptions{Main: "wf-main", Branch: "wf-other", Expose: "collab", NoCopy: true, Apply: true})
	if code != 0 {
		t.Fatalf("exit=%d err=%q\n%s", code, errOut, out)
	}
	rows := branchRows(t, st)
	if len(rows) != 1 || rows[0].Expose.PresetName() != model.ExposePresetCollab || rows[0].CopyUserNotes {
		t.Fatalf("应是协作预设且不抄送: %+v", rows)
	}
	if !strings.Contains(out, "抄送：关闭") {
		t.Fatalf("预演 / 结果应说明抄送关闭:\n%s", out)
	}
}

func TestBranchUnlinkPreviewThenApply(t *testing.T) {
	dbPath, st := newRebindDB(t)
	if code, out, errOut := runLink(t, st, dbPath, branchLinkOptions{Main: "wf-main", Branch: "wf-other", Apply: true}); code != 0 {
		t.Fatalf("前置：exit=%d err=%q\n%s", code, errOut, out)
	}

	code, out, errOut := runUnlink(t, st, dbPath, branchUnlinkOptions{Branch: "wf-other"})
	if code != 0 || errOut != "" || !strings.Contains(out, "仅预演") || !strings.Contains(out, "将解除") {
		t.Fatalf("预演: exit=%d err=%q\n%s", code, errOut, out)
	}
	if n := len(branchRows(t, st)); n != 1 {
		t.Fatalf("预演不应删除，得到 %d", n)
	}
	if snaps, _ := filepath.Glob(dbPath + ".branch-unlink-*"); len(snaps) != 0 {
		t.Fatalf("预演不应生成快照: %v", snaps)
	}

	code, out, errOut = runUnlink(t, st, dbPath, branchUnlinkOptions{Branch: "wf-other", Apply: true})
	if code != 0 || errOut != "" {
		t.Fatalf("应用: exit=%d err=%q\n%s", code, errOut, out)
	}
	snaps, _ := filepath.Glob(dbPath + ".branch-unlink-wf-other-20261009-083000.bak")
	if len(snaps) != 1 {
		t.Fatalf("应生成快照: %v\n%s", snaps, out)
	}
	snap, err := store.New(snaps[0])
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if n := len(branchRows(t, snap)); n != 1 {
		t.Fatalf("快照应保存解除之前的状态（1 条关系），得到 %d", n)
	}
	if n := len(branchRows(t, st)); n != 0 {
		t.Fatalf("应已解除，得到 %d", n)
	}
	for _, want := range []string{"已解除", "回滚办法", `relaymesh branch-link --main "wf-main" --branch "wf-other" --apply`} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 两个会话本身仍在
	b, _ := st.WorkflowBindingOf(context.Background(), "wf-other")
	if b.Sessions != 1 {
		t.Fatalf("解除关系不应删会话: %+v", b)
	}

	code, _, errOut = runUnlink(t, st, dbPath, branchUnlinkOptions{Branch: "wf-other", Apply: true})
	if code != 1 || !strings.Contains(errOut, "不是任何会话的分支") {
		t.Fatalf("再次解除应报错: exit=%d err=%q", code, errOut)
	}
}

func TestParseBranchLinkArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"没有参数", nil, 2},
		{"只有 main", []string{"--main", "a"}, 2},
		{"只有 branch", []string{"--branch", "b"}, 2},
		{"list 与建立参数混用", []string{"--list", "--main", "a"}, 2},
		{"list 与 apply 混用", []string{"--list", "--apply"}, 2},
		{"未知预设", []string{"--main", "a", "--branch", "b", "--expose", "turbo"}, 2},
		{"多余的位置参数", []string{"--main", "a", "--branch", "b", "extra"}, 2},
		{"未知选项", []string{"--nope"}, 2},
		{"帮助", []string{"-h"}, 0},
		{"list", []string{"--list"}, -1},
		{"完整参数", []string{"--main", "a", "--branch", "b", "--expose", "managed", "--no-copy", "--apply", "--db", "/tmp/x.db"}, -1},
	}
	for _, c := range cases {
		var errw bytes.Buffer
		_, code := parseBranchLinkArgs(c.args, &errw)
		if code != c.wantCode {
			t.Errorf("%s：期望退出码 %d，得到 %d（%s）", c.name, c.wantCode, code, errw.String())
		}
	}
	opts, code := parseBranchLinkArgs([]string{"--main", "a", "--branch", "b", "--expose", "managed", "--no-copy", "--apply"}, &bytes.Buffer{})
	if code != -1 || opts.Main != "a" || opts.Branch != "b" || opts.Expose != "managed" || !opts.NoCopy || !opts.Apply {
		t.Fatalf("参数解析结果不对: %+v code=%d", opts, code)
	}
}

func TestParseBranchUnlinkArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"没有参数", nil, 2},
		{"多余的位置参数", []string{"--branch", "b", "x"}, 2},
		{"帮助", []string{"-h"}, 0},
		{"预演", []string{"--branch", "b"}, -1},
		{"应用", []string{"--branch", "b", "--apply"}, -1},
	}
	for _, c := range cases {
		var errw bytes.Buffer
		_, code := parseBranchUnlinkArgs(c.args, &errw)
		if code != c.wantCode {
			t.Errorf("%s：期望退出码 %d，得到 %d（%s）", c.name, c.wantCode, code, errw.String())
		}
	}
}
