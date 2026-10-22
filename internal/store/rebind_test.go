package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// seedRebindWorkflow 造一个工作流：done 条已完成的会话，pending 为真时再多一条等待回复的会话，
// 外加 queued 条排队反馈。会话的主机取自 host（模拟凭据登记的主机名）。
func seedRebindWorkflow(t *testing.T, st *Store, workflow, dir, host string, done int, pending bool, queued int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < done; i++ {
		id := workflow + "-s" + string(rune('a'+i))
		if _, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
			SessionID:          id,
			WorkflowID:         workflow,
			ProjectDirectory:   dir,
			CredentialHostName: host,
			Summary:            "交付 " + id,
		}); err != nil {
			t.Fatalf("创建会话 %s: %v", id, err)
		}
		if _, err := st.SubmitFeedback(ctx, SubmitFeedbackInput{SessionID: id, ResponseText: "ok"}); err != nil {
			t.Fatalf("完成会话 %s: %v", id, err)
		}
		time.Sleep(3 * time.Millisecond)
	}
	if pending {
		if _, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
			SessionID:          workflow + "-pending",
			WorkflowID:         workflow,
			ProjectDirectory:   dir,
			CredentialHostName: host,
			Summary:            "等待回复",
		}); err != nil {
			t.Fatalf("创建等待中的会话: %v", err)
		}
	}
	for i := 0; i < queued; i++ {
		if err := st.db.Create(&model.QueuedFeedback{
			WorkflowID:       workflow,
			HostName:         host,
			ProjectDirectory: dir,
			ResponseText:     "排队的反馈",
			Status:           "pending",
		}).Error; err != nil {
			t.Fatalf("创建排队反馈: %v", err)
		}
	}
}

func appErrKind(err error) ErrorKind {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae.Kind
	}
	return ""
}

func TestWorkflowBindingOf(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-bind", "/home/u/proj", "wsl-box", 2, true, 1)

	b, err := st.WorkflowBindingOf(ctx, "wf-bind")
	if err != nil {
		t.Fatal(err)
	}
	if b.Sessions != 3 || b.Pending != 1 || b.Queued != 1 {
		t.Fatalf("计数不对: %+v", b)
	}
	if len(b.Dirs) != 1 || b.Dirs[0] != "/home/u/proj" || len(b.Hosts) != 1 || b.Hosts[0] != "wsl-box" {
		t.Fatalf("目录 / 主机不对: %+v", b)
	}

	if _, err := st.WorkflowBindingOf(ctx, "wf-nope"); appErrKind(err) != ErrNotFound {
		t.Fatalf("不存在的工作流应返回 NotFound，实际 %v", err)
	}
	if _, err := st.WorkflowBindingOf(ctx, "  "); appErrKind(err) != ErrInvalidInput {
		t.Fatalf("空 workflow_id 应返回 InvalidInput，实际 %v", err)
	}
}

func TestRebindWorkflowDryRunWritesNothing(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-dry", "/home/u/proj", "wsl-box", 2, false, 1)

	res, err := st.RebindWorkflow(ctx, RebindRequest{WorkflowID: "wf-dry", ToDir: "/home/user/proj", ToHost: "devhost"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || res.SessionsToMove != 2 || res.QueuedToMove != 1 {
		t.Fatalf("预演结果不对: %+v", res)
	}
	got, _ := st.WorkflowBindingOf(ctx, "wf-dry")
	if got.Dirs[0] != "/home/u/proj" || got.Hosts[0] != "wsl-box" {
		t.Fatalf("预演不应写入任何数据: %+v", got)
	}
}

func TestRebindWorkflowApplyMovesOnlyThatWorkflow(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-move", "/home/u/proj", "wsl-box", 2, true, 2)
	seedRebindWorkflow(t, st, "wf-other", "/home/u/proj", "wsl-box", 1, false, 1)

	beforeSess, err := st.GetFeedbackSession(ctx, "wf-move-sa")
	if err != nil {
		t.Fatal(err)
	}

	res, err := st.RebindWorkflow(ctx, RebindRequest{WorkflowID: "wf-move", ToDir: "/home/user/proj/", ToHost: "devhost"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.SessionsToMove != 3 || res.QueuedToMove != 2 {
		t.Fatalf("执行结果不对: %+v", res)
	}
	if res.TargetDir != "/home/user/proj" {
		t.Fatalf("目标目录应去掉末尾斜杠，实际 %q", res.TargetDir)
	}
	if len(res.After.Dirs) != 1 || res.After.Dirs[0] != "/home/user/proj" || res.After.Hosts[0] != "devhost" {
		t.Fatalf("改绑后画像不对: %+v", res.After)
	}
	if res.After.Sessions != 3 || res.After.Pending != 1 || res.After.Queued != 2 {
		t.Fatalf("改绑不应改变行数 / 状态: %+v", res.After)
	}

	// 其他工作流原封不动。
	other, _ := st.WorkflowBindingOf(ctx, "wf-other")
	if other.Dirs[0] != "/home/u/proj" || other.Hosts[0] != "wsl-box" {
		t.Fatalf("其他工作流被误改: %+v", other)
	}

	// 更新时间保持不变，不会让它「看起来刚刚活跃」。
	afterSess, _ := st.GetFeedbackSession(ctx, "wf-move-sa")
	if !afterSess.UpdatedAt.Equal(beforeSess.UpdatedAt) {
		t.Fatalf("改绑不应改动 updated_at: %v -> %v", beforeSess.UpdatedAt, afterSess.UpdatedAt)
	}
	if afterSess.Summary != beforeSess.Summary || afterSess.ResponseText != beforeSess.ResponseText {
		t.Fatalf("改绑不应改动正文")
	}
}

// 改绑要解决的就是这个问题：项目换了目录后，旧工作流在新目录下被 D-161 拒绝；改绑后放行。
func TestRebindWorkflowUnblocksNewDirectory(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-d161", "/home/u/proj", "wsl-box", 1, false, 0)

	in := CreateSessionInput{
		WorkflowID:         "wf-d161",
		ProjectDirectory:   "/home/user/proj",
		CredentialHostName: "devhost",
		Summary:            "在新目录继续",
	}
	if _, err := st.CreateFeedbackSession(ctx, in); appErrKind(err) != ErrConflict {
		t.Fatalf("改绑前，新目录写入旧工作流应被 D-161 拒绝，实际 %v", err)
	}

	if _, err := st.RebindWorkflow(ctx, RebindRequest{WorkflowID: "wf-d161", ToDir: "/home/user/proj", ToHost: "devhost"}, true); err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateFeedbackSession(ctx, in)
	if err != nil {
		t.Fatalf("改绑后新目录写入应放行: %v", err)
	}
	if sess.WorkflowID != "wf-d161" || sess.HostName != "devhost" {
		t.Fatalf("新会话应落在原工作流与新主机: %+v", sess)
	}

	// 不带 workflow_id 时，新目录下的调用也应自动继承这个工作流（按项目目录自愈）。
	sess2, err := st.CreateFeedbackSession(ctx, CreateSessionInput{
		ProjectDirectory:   "/home/user/proj",
		CredentialHostName: "devhost",
		Summary:            "不带 workflow_id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess2.WorkflowID != "wf-d161" {
		t.Fatalf("应按项目目录继承改绑后的工作流，实际 %q", sess2.WorkflowID)
	}
}

func TestRebindWorkflowEmptyHostKeepsHost(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-keep", "/home/u/proj", "wsl-box", 1, false, 1)

	res, err := st.RebindWorkflow(ctx, RebindRequest{WorkflowID: "wf-keep", ToDir: "/srv/proj"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.After.Dirs[0] != "/srv/proj" || res.After.Hosts[0] != "wsl-box" {
		t.Fatalf("目标主机为空时应保留原主机: %+v", res.After)
	}
}

func TestRebindWorkflowIdempotentAndReversible(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-rev", "/home/u/proj", "wsl-box", 2, false, 1)

	req := RebindRequest{WorkflowID: "wf-rev", ToDir: "/home/user/proj", ToHost: "devhost"}
	first, err := st.RebindWorkflow(ctx, req, true)
	if err != nil || !first.Applied {
		t.Fatalf("首次改绑失败: %+v %v", first, err)
	}

	again, err := st.RebindWorkflow(ctx, req, true)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Noop() || again.Applied {
		t.Fatalf("已经在目标上时应是空操作: %+v", again)
	}

	// 用 Before 里记下的原值就能原路改回。
	back, err := st.RebindWorkflow(ctx, RebindRequest{
		WorkflowID: "wf-rev",
		ToDir:      first.Before.Dirs[0],
		ToHost:     first.Before.Hosts[0],
	}, true)
	if err != nil || !back.Applied {
		t.Fatalf("改回失败: %+v %v", back, err)
	}
	got, _ := st.WorkflowBindingOf(ctx, "wf-rev")
	if got.Dirs[0] != "/home/u/proj" || got.Hosts[0] != "wsl-box" {
		t.Fatalf("没有原路改回: %+v", got)
	}
}

func TestRebindWorkflowRepairsMixedBindings(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-mixed", "/home/u/proj", "wsl-box", 2, false, 0)
	// 人为造出历史遗留的不一致：其中一条会话落在另一个目录 / 主机。
	if err := st.db.Model(&model.FeedbackSession{}).Where("id = ?", "wf-mixed-sa").
		UpdateColumns(map[string]any{"project_directory": "/old/place", "host_name": "old-host"}).Error; err != nil {
		t.Fatal(err)
	}
	before, _ := st.WorkflowBindingOf(ctx, "wf-mixed")
	if len(before.Dirs) != 2 || len(before.Hosts) != 2 {
		t.Fatalf("前置条件：应有两个目录 / 主机: %+v", before)
	}

	res, err := st.RebindWorkflow(ctx, RebindRequest{WorkflowID: "wf-mixed", ToDir: "/home/user/proj", ToHost: "devhost"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.After.Dirs) != 1 || len(res.After.Hosts) != 1 {
		t.Fatalf("改绑后应收敛为唯一绑定: %+v", res.After)
	}
}

func TestRebindWorkflowRejectsBadInput(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-bad", "/home/u/proj", "wsl-box", 1, false, 0)

	cases := []struct {
		name string
		req  RebindRequest
		kind ErrorKind
	}{
		{"空目录", RebindRequest{WorkflowID: "wf-bad", ToDir: "  "}, ErrInvalidInput},
		{"相对路径", RebindRequest{WorkflowID: "wf-bad", ToDir: "projects/x"}, ErrInvalidInput},
		{"点目录", RebindRequest{WorkflowID: "wf-bad", ToDir: "."}, ErrInvalidInput},
		{"根目录", RebindRequest{WorkflowID: "wf-bad", ToDir: "/"}, ErrInvalidInput},
		{"归一化后是根目录", RebindRequest{WorkflowID: "wf-bad", ToDir: "/a/.."}, ErrInvalidInput},
		{"盘符根目录", RebindRequest{WorkflowID: "wf-bad", ToDir: `C:\`}, ErrInvalidInput},
		{"控制字符目录", RebindRequest{WorkflowID: "wf-bad", ToDir: "/srv/a\nb"}, ErrInvalidInput},
		{"超长目录", RebindRequest{WorkflowID: "wf-bad", ToDir: "/" + strings.Repeat("a", 600)}, ErrInvalidInput},
		{"超长主机", RebindRequest{WorkflowID: "wf-bad", ToDir: "/srv/a", ToHost: strings.Repeat("h", 200)}, ErrInvalidInput},
		{"控制字符主机", RebindRequest{WorkflowID: "wf-bad", ToDir: "/srv/a", ToHost: "h\x00"}, ErrInvalidInput},
		{"空工作流", RebindRequest{WorkflowID: "", ToDir: "/srv/a"}, ErrInvalidInput},
		{"不存在的工作流", RebindRequest{WorkflowID: "wf-ghost", ToDir: "/srv/a"}, ErrNotFound},
	}
	for _, tc := range cases {
		for _, apply := range []bool{false, true} {
			if _, err := st.RebindWorkflow(ctx, tc.req, apply); appErrKind(err) != tc.kind {
				t.Errorf("%s（apply=%v）应返回 %s，实际 %v", tc.name, apply, tc.kind, err)
			}
		}
	}
	got, _ := st.WorkflowBindingOf(ctx, "wf-bad")
	if got.Dirs[0] != "/home/u/proj" || got.Hosts[0] != "wsl-box" {
		t.Fatalf("非法输入不应改动任何数据: %+v", got)
	}
}

func TestNormalizeRebindTarget(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/user/RelayMesh", "/home/user/RelayMesh"},
		{"  /home/user/RelayMesh/  ", "/home/user/RelayMesh"},
		{"/a//b/../c", "/a/c"},
		{`D:\work\proj\`, `D:\work\proj`},
		{`d:/work/proj/`, `d:/work/proj`},
	}
	for _, tc := range cases {
		got, _, err := normalizeRebindTarget(tc.in, "")
		if err != nil || got != tc.want {
			t.Errorf("normalizeRebindTarget(%q) = %q, %v，期望 %q", tc.in, got, err, tc.want)
		}
	}
}

func TestListWorkflowBindingsMostRecentFirst(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	seedRebindWorkflow(t, st, "wf-old", "/a", "h1", 1, false, 0)
	time.Sleep(10 * time.Millisecond)
	seedRebindWorkflow(t, st, "wf-new", "/b", "h2", 1, false, 0)

	list, err := st.ListWorkflowBindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].WorkflowID != "wf-new" || list[1].WorkflowID != "wf-old" {
		t.Fatalf("应按最近活跃排序: %+v", list)
	}
}

func TestListCredentialHostsNeverCarriesToken(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	cred := &model.MCPCredential{Name: "devhost-mcp", HostName: "devhost", IsActive: true}
	if err := st.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if cred.Token == "" {
		t.Fatal("前置条件：凭据应已生成令牌")
	}

	list, err := st.ListCredentialHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "devhost-mcp" || list[0].HostName != "devhost" || !list[0].Active {
		t.Fatalf("凭据主机视图不对: %+v", list)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), cred.Token) || strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Fatalf("凭据主机视图不应含令牌: %s", raw)
	}
}

func TestSnapshotToProducesReadableCopyAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	st, err := New(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seedRebindWorkflow(t, st, "wf-snap", "/home/u/proj", "wsl-box", 2, false, 1)

	dest := filepath.Join(dir, "snap.db")
	if err := st.SnapshotTo(context.Background(), dest); err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if err := st.SnapshotTo(context.Background(), dest); err == nil {
		t.Fatal("目标文件已存在时应拒绝覆盖")
	}
	if err := st.SnapshotTo(context.Background(), " "); appErrKind(err) != ErrInvalidInput {
		t.Fatalf("空路径应返回 InvalidInput，实际 %v", err)
	}

	snap, err := New(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	b, err := snap.WorkflowBindingOf(context.Background(), "wf-snap")
	if err != nil || b.Sessions != 2 || b.Queued != 1 || b.Dirs[0] != "/home/u/proj" {
		t.Fatalf("快照内容不对: %+v %v", b, err)
	}
}
