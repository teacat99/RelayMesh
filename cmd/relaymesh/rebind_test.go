package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// newRebindDB 建一个带两个工作流与一个凭据的真实库文件，返回库路径与仍打开着的 Store。
func newRebindDB(t *testing.T) (string, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "relaymesh.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	mk := func(id, wf, dir, host string, complete bool) {
		if _, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{
			SessionID:          id,
			WorkflowID:         wf,
			ProjectDirectory:   dir,
			CredentialHostName: host,
			Summary:            "交付 " + id,
		}); err != nil {
			t.Fatalf("创建会话 %s: %v", id, err)
		}
		if complete {
			if _, err := st.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: id, ResponseText: "ok"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("s1", "wf-main", "/home/u/proj", "wsl-box", true)
	mk("s2", "wf-main", "/home/u/proj", "wsl-box", true)
	mk("s3", "wf-main", "/home/u/proj", "wsl-box", false) // 等待回复
	mk("s4", "wf-other", "/home/u/other", "wsl-box", true)

	cred := &model.MCPCredential{Name: "devhost-mcp", HostName: "devhost", IsActive: true}
	if err := st.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	return dbPath, st
}

func fixedNow() time.Time { return time.Date(2026, 10, 9, 8, 30, 0, 0, time.UTC) }

func run(t *testing.T, st *store.Store, dbPath string, opts rebindOptions) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := executeRebind(context.Background(), st, dbPath, opts, &out, &errw, fixedNow)
	return code, out.String(), errw.String()
}

func TestRebindList(t *testing.T) {
	dbPath, st := newRebindDB(t)
	code, out, errOut := run(t, st, dbPath, rebindOptions{List: true})
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d err=%q", code, errOut)
	}
	for _, want := range []string{"wf-main", "wf-other", "/home/u/proj", "wsl-box", "等待回复 1 条", "devhost-mcp → devhost"} {
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

func TestRebindDryRunDoesNotWrite(t *testing.T) {
	dbPath, st := newRebindDB(t)
	code, out, errOut := run(t, st, dbPath, rebindOptions{Workflow: "wf-main", ToDir: "/home/user/proj", ToHost: "devhost"})
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d err=%q", code, errOut)
	}
	for _, want := range []string{"仅预演", "将改写：会话 3 条", "等待回复的会话", "/home/user/proj"} {
		if !strings.Contains(out, want) {
			t.Errorf("预演输出缺少 %q:\n%s", want, out)
		}
	}
	b, _ := st.WorkflowBindingOf(context.Background(), "wf-main")
	if b.Dirs[0] != "/home/u/proj" || b.Hosts[0] != "wsl-box" {
		t.Fatalf("预演不应改动数据: %+v", b)
	}
	if snaps, _ := filepath.Glob(dbPath + ".rebind-*"); len(snaps) != 0 {
		t.Fatalf("预演不应生成快照: %v", snaps)
	}
}

func TestRebindApplyTakesSnapshotThenRebinds(t *testing.T) {
	dbPath, st := newRebindDB(t)
	code, out, errOut := run(t, st, dbPath, rebindOptions{Workflow: "wf-main", ToDir: "/home/user/proj", ToHost: "devhost", Apply: true})
	if code != 0 || errOut != "" {
		t.Fatalf("exit=%d err=%q\n%s", code, errOut, out)
	}

	snaps, _ := filepath.Glob(dbPath + ".rebind-wf-main-20261009-083000.bak")
	if len(snaps) != 1 {
		t.Fatalf("应生成一份带工作流与时间的快照，实际 %v\n%s", snaps, out)
	}
	// 快照里仍是改绑前的状态。
	snap, err := store.New(snaps[0])
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	old, err := snap.WorkflowBindingOf(context.Background(), "wf-main")
	if err != nil || old.Dirs[0] != "/home/u/proj" || old.Hosts[0] != "wsl-box" {
		t.Fatalf("快照应保存改绑前的状态: %+v %v", old, err)
	}

	got, _ := st.WorkflowBindingOf(context.Background(), "wf-main")
	if got.Dirs[0] != "/home/user/proj" || got.Hosts[0] != "devhost" {
		t.Fatalf("现库应已改绑: %+v", got)
	}
	other, _ := st.WorkflowBindingOf(context.Background(), "wf-other")
	if other.Dirs[0] != "/home/u/other" || other.Hosts[0] != "wsl-box" {
		t.Fatalf("其他工作流不应被改动: %+v", other)
	}

	for _, want := range []string{"已生成快照", "已改绑：会话 3 条", "回滚办法", `--to-dir "/home/u/proj" --to-host "wsl-box" --apply`, "-wal"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestRebindApplyIsNoopWhenAlreadyBound(t *testing.T) {
	dbPath, st := newRebindDB(t)
	opts := rebindOptions{Workflow: "wf-main", ToDir: "/home/user/proj", ToHost: "devhost", Apply: true}
	if code, _, errOut := run(t, st, dbPath, opts); code != 0 {
		t.Fatalf("首次改绑失败: %s", errOut)
	}
	before, _ := filepath.Glob(dbPath + ".rebind-*")

	later := func() time.Time { return fixedNow().Add(time.Hour) }
	var out, errw bytes.Buffer
	code := executeRebind(context.Background(), st, dbPath, opts, &out, &errw, later)
	if code != 0 || !strings.Contains(out.String(), "无需改写") {
		t.Fatalf("已绑定时应提示无需改写: exit=%d\n%s%s", code, out.String(), errw.String())
	}
	if after, _ := filepath.Glob(dbPath + ".rebind-*"); len(after) != len(before) {
		t.Fatalf("空操作不应再生成快照: %v -> %v", before, after)
	}
}

func TestRebindWarnsAboutUnknownTargetHost(t *testing.T) {
	dbPath, st := newRebindDB(t)
	_, out, _ := run(t, st, dbPath, rebindOptions{Workflow: "wf-other", ToDir: "/srv/other", ToHost: "typo-host"})
	if !strings.Contains(out, `"typo-host" 没有出现在任何凭据登记的主机名里`) {
		t.Fatalf("目标主机不在凭据里时应提醒:\n%s", out)
	}
	if strings.Contains(out, "等待回复的会话") {
		t.Fatalf("没有等待中的会话时不应出现该提醒:\n%s", out)
	}
}

func TestRebindFailuresLeaveDataUntouched(t *testing.T) {
	dbPath, st := newRebindDB(t)
	cases := []struct {
		name string
		opts rebindOptions
	}{
		{"工作流不存在", rebindOptions{Workflow: "wf-ghost", ToDir: "/srv/x", Apply: true}},
		{"相对路径", rebindOptions{Workflow: "wf-main", ToDir: "proj", Apply: true}},
		{"根目录", rebindOptions{Workflow: "wf-main", ToDir: "/", Apply: true}},
	}
	for _, tc := range cases {
		code, _, errOut := run(t, st, dbPath, tc.opts)
		if code != 1 || errOut == "" {
			t.Errorf("%s：应以退出码 1 失败并给出原因，exit=%d err=%q", tc.name, code, errOut)
		}
	}
	b, _ := st.WorkflowBindingOf(context.Background(), "wf-main")
	if b.Dirs[0] != "/home/u/proj" {
		t.Fatalf("失败不应改动数据: %+v", b)
	}
	if snaps, _ := filepath.Glob(dbPath + ".rebind-*"); len(snaps) != 0 {
		t.Fatalf("校验失败时不应生成快照: %v", snaps)
	}
}

func TestParseRebindArgs(t *testing.T) {
	var errw bytes.Buffer
	if opts, code := parseRebindArgs([]string{"--workflow", "w", "--to-dir", "/d", "--to-host", "h", "--apply", "--db", "x.db"}, &errw); code != -1 ||
		opts.Workflow != "w" || opts.ToDir != "/d" || opts.ToHost != "h" || !opts.Apply || opts.DB != "x.db" {
		t.Fatalf("参数解析不对: %+v code=%d", opts, code)
	}
	if _, code := parseRebindArgs([]string{"--list"}, &errw); code != -1 {
		t.Fatalf("--list 应通过，code=%d", code)
	}
	for name, args := range map[string][]string{
		"缺少 to-dir":   {"--workflow", "w"},
		"缺少 workflow": {"--to-dir", "/d"},
		"什么都没给":       {},
		"list 混用":     {"--list", "--workflow", "w"},
		"未知参数":        {"--nope"},
		"多余的位置参数":     {"--workflow", "w", "--to-dir", "/d", "extra"},
	} {
		errw.Reset()
		if _, code := parseRebindArgs(args, &errw); code != 2 || !strings.Contains(errw.String(), "用法") {
			t.Errorf("%s：应以退出码 2 失败并打印用法，code=%d err=%q", name, code, errw.String())
		}
	}
	errw.Reset()
	if _, code := parseRebindArgs([]string{"-h"}, &errw); code != 0 || !strings.Contains(errw.String(), "用法") {
		t.Errorf("-h 应打印用法并以 0 退出，code=%d", code)
	}
}

func TestRunRebindRefusesMissingDatabaseWithoutCreatingIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "relaymesh.db")
	var out, errw bytes.Buffer
	code := runRebindWorkflowTo([]string{"--db", missing, "--list"}, &out, &errw)
	if code != 1 || !strings.Contains(errw.String(), "数据库文件不存在") {
		t.Fatalf("库不存在时应拒绝: exit=%d err=%q", code, errw.String())
	}
	if _, err := os.Stat(filepath.Dir(missing)); err == nil {
		t.Fatalf("不应因为路径写错而悄悄创建目录 / 空库")
	}
}

func TestRunRebindEndToEndOnFileDatabase(t *testing.T) {
	dbPath, st := newRebindDB(t)
	st.Close() // 命令自己打开库，和线上「另起进程」的方式一致

	var out, errw bytes.Buffer
	code := runRebindWorkflowTo([]string{"--db", dbPath, "--workflow", "wf-main", "--to-dir", "/home/user/proj", "--to-host", "devhost", "--apply"}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit=%d err=%q\n%s", code, errw.String(), out.String())
	}

	reopened, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	b, err := reopened.WorkflowBindingOf(context.Background(), "wf-main")
	if err != nil || b.Dirs[0] != "/home/user/proj" || b.Hosts[0] != "devhost" {
		t.Fatalf("重新打开后应是改绑后的绑定: %+v %v", b, err)
	}
}
