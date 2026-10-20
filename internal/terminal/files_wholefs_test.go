package terminal

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「允许整个文件系统」（TerminalLimits.AllowWholeFS）在 Hub 一侧的行为：
// 下发给 Agent 的是哨兵而不是目录；旧版 Agent 在发请求之前就有明确的说明；审计留下标记；
// 收回开关立即生效（包括已经签发的下载票据）。路径授权本身的权威实现在 Agent（internal/agent/files 有测试）。

func wholeFSCred(f *filesFixture, t *testing.T, keepDirs ...string) *model.MCPCredential {
	t.Helper()
	return f.newCred(t, func(c *model.MCPCredential) {
		c.TerminalLimits = model.TerminalLimits{AllowWholeFS: true, AllowedPaths: keepDirs}
	})
}

func TestFileRootsPrefersTheWholeFSSwitch(t *testing.T) {
	dirs := []string{"/srv/data", "~/work"}
	got := fileRoots(model.TerminalLimits{AllowedPaths: dirs})
	if strings.Join(got, "|") != "/srv/data|~/work" {
		t.Fatalf("普通令牌应原样下发允许目录: %v", got)
	}
	got[0] = "/tampered"
	if dirs[0] != "/srv/data" {
		t.Fatal("下发的切片必须是副本")
	}
	// 开关优先：保留着的目录设置不起作用，只下发哨兵（Agent 不会看到任何目录）
	got = fileRoots(model.TerminalLimits{AllowWholeFS: true, AllowedPaths: dirs})
	if len(got) != 1 || got[0] != termproto.FileRootAll {
		t.Fatalf("开启整个文件系统时只应下发哨兵: %v", got)
	}
	if got := fileRoots(model.TerminalLimits{AllowWholeFS: true}); len(got) != 1 || got[0] != termproto.FileRootAll {
		t.Fatalf("没有目录设置时同样只下发哨兵: %v", got)
	}
	if got := fileRoots(model.TerminalLimits{}); len(got) != 0 {
		t.Fatalf("没有任何范围时不应下发任何东西: %v", got)
	}
}

func TestFilesWholeFSSendsTheSentinelAndReachesAnywhere(t *testing.T) {
	f := newFilesFixture(t)
	f.fs.put("/etc/hostname", []byte("box"))
	f.fs.put("/srv/data/a.txt", []byte("hello"))
	restricted := f.newCred(t, nil)
	whole := wholeFSCred(f, t, "/srv/data") // 目录设置被保留着，但不起作用

	// 对照：普通令牌仍被限制在目录里
	_, err := f.files(t, restricted, termexec.FileRequest{Action: "stat", Path: "/etc/hostname"})
	wantCode(t, err, termexec.CodePathDenied)

	// 整个文件系统：目录之外的路径可达，下发的 roots 恰好只有哨兵
	res, err := f.files(t, whole, termexec.FileRequest{Action: "stat", Path: "/etc/hostname"})
	if err != nil {
		t.Fatalf("整个文件系统应可达: %v", err)
	}
	if st := res.(termexec.FileStatResult); !st.Exists || st.Size != 3 {
		t.Fatalf("stat = %+v", st)
	}
	reqs := f.fs.requests(termproto.FileOpStat)
	last := reqs[len(reqs)-1]
	if len(last.Roots) != 1 || last.Roots[0] != termproto.FileRootAll || last.Path != "/etc/hostname" {
		t.Fatalf("下发的请求 = %+v", last)
	}
	if _, err := f.files(t, whole, termexec.FileRequest{Action: "hash", Path: "/etc/hostname"}); err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := f.files(t, whole, termexec.FileRequest{Action: "push_url", Path: "/var/tmp/x.bin", URL: "https://files.example.test/x"}); err != nil {
		t.Fatalf("push_url: %v", err)
	}
	if pushes := f.fs.requests(termproto.FileOpPushURL); len(pushes) != 1 || len(pushes[0].Roots) != 1 || pushes[0].Roots[0] != termproto.FileRootAll {
		t.Fatalf("push_url 下发的请求 = %+v", pushes)
	}

	// 审计：整个文件系统的访问带着标记，普通令牌的访问不带
	if _, err := f.files(t, restricted, termexec.FileRequest{Action: "stat", Path: "/srv/data/a.txt"}); err != nil {
		t.Fatal(err)
	}
	var wholeOps, plainOps int
	for _, l := range f.auditsOf(t, EvFileOp) {
		switch l.Actor {
		case whole.Name:
			if !strings.Contains(l.Detail, auditScopeWholeFS) {
				t.Fatalf("整个文件系统令牌的访问审计缺少 %s: %+v", auditScopeWholeFS, l)
			}
			wholeOps++
		case restricted.Name:
			if strings.Contains(l.Detail, auditScopeWholeFS) {
				t.Fatalf("普通令牌的审计不应带整个文件系统标记: %+v", l)
			}
			plainOps++
		}
	}
	if wholeOps == 0 || plainOps == 0 {
		t.Fatalf("应各有访问记录: whole=%d plain=%d %+v", wholeOps, plainOps, f.auditsOf(t, EvFileOp))
	}
	pushes := f.auditsOf(t, EvFilePush)
	if len(pushes) != 1 || !strings.Contains(pushes[0].Detail, auditScopeWholeFS) {
		t.Fatalf("push 审计应带标记: %+v", pushes)
	}
}

// 整个文件系统不需要任何允许目录；文件权限位关闭时开关不产生任何访问。
func TestFilesWholeFSNeedsNoDirectoriesAndRespectsThePermissionBit(t *testing.T) {
	f := newFilesFixture(t)
	f.fs.put("/etc/hostname", []byte("box"))
	whole := wholeFSCred(f, t) // 没有任何目录
	if _, err := f.files(t, whole, termexec.FileRequest{Action: "stat", Path: "/etc/hostname"}); err != nil {
		t.Fatalf("只有开关也应可用: %v", err)
	}

	noPerm := f.newCred(t, func(c *model.MCPCredential) {
		c.Permissions = model.Permissions{Terminal: true} // 没有文件权限位
		c.TerminalLimits = model.TerminalLimits{AllowWholeFS: true}
	})
	before := len(f.fs.requests(termproto.FileOpStat))
	_, err := f.files(t, noPerm, termexec.FileRequest{Action: "stat", Path: "/etc/hostname"})
	wantCode(t, err, termexec.CodeNotAllowed)
	if len(f.fs.requests(termproto.FileOpStat)) != before {
		t.Fatal("没有文件权限位时不得向主机发请求，开关不能代替权限位")
	}
	if f.svc.offeredTo(callerOf(noPerm)) {
		t.Fatal("没有文件权限位时服务端不向它提供文件能力")
	}

	// 两者都没有（库里不会有，但 Hub 是第二道防线）：拒绝
	none := f.newCred(t, nil)
	none.TerminalLimits = model.TerminalLimits{}
	_, err = f.files(t, none, termexec.FileRequest{Action: "stat", Path: "/etc/hostname"})
	wantCode(t, err, termexec.CodeNotAllowed)
}

// 旧版 Agent 不认识哨兵，会拒绝一切路径：Hub 在发请求之前就说清楚原因。
func TestFilesWholeFSOnAnOldAgentIsExplained(t *testing.T) {
	f := newFilesFixtureWith(t, nil) // Files=true，但没有 FilesAll
	f.fs.put("/etc/hostname", []byte("box"))
	f.fs.put("/srv/data/a.txt", []byte("hello"))
	whole := wholeFSCred(f, t, "/srv/data")
	restricted := f.newCred(t, nil)
	before := len(f.fs.requests(termproto.FileOpStat))

	_, err := f.files(t, whole, termexec.FileRequest{Action: "stat", Path: "/srv/data/a.txt"})
	wantCode(t, err, termexec.CodeHostLegacy)
	if !strings.Contains(err.Error(), "允许整个文件系统") || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("应解释是 Agent 版本的问题: %v", err)
	}
	if len(f.fs.requests(termproto.FileOpStat)) != before {
		t.Fatal("旧版 Agent 不应收到整个文件系统的请求")
	}
	denied := false
	for _, l := range f.auditsOf(t, EvExecDenied) {
		if strings.Contains(l.Detail, "files:stat") && strings.Contains(l.Detail, termexec.CodeHostLegacy) {
			denied = true
		}
	}
	if !denied {
		t.Fatal("被拒绝应写审计")
	}

	// 同一台旧版主机上，普通的目录令牌不受影响
	if _, err := f.files(t, restricted, termexec.FileRequest{Action: "stat", Path: "/srv/data/a.txt"}); err != nil {
		t.Fatalf("目录令牌在旧版 Agent 上应照常工作: %v", err)
	}

	// terminal_targets 也提前说明：不可用，原因 host_legacy
	res, err := f.env.hub.Exec().Targets(context.Background(), callerOf(whole), "")
	if err != nil {
		t.Fatal(err)
	}
	tr := res.(*termexec.TargetsResult)
	if t0 := tr.Targets[0]; t0.Files || t0.FilesBlocked != termexec.CodeHostLegacy {
		t.Fatalf("旧版 Agent 上整个文件系统令牌应提示 host_legacy: %+v", t0)
	}
	res, _ = f.env.hub.Exec().Targets(context.Background(), callerOf(restricted), "")
	if t0 := res.(*termexec.TargetsResult).Targets[0]; !t0.Files || t0.FilesBlocked != "" {
		t.Fatalf("目录令牌不受影响: %+v", t0)
	}

	// 下载端点同样先核对（票据不可能在旧版 Agent 上签发，这里直接验证端点：手工放一张票据）
	if f.svc.scopeBlock(whole.TerminalLimits, f.env.hub.onlineAgent(f.a.code)) != termexec.CodeHostLegacy {
		t.Fatal("scopeBlock 应在旧版 Agent 上拦下整个文件系统")
	}
	if f.svc.scopeBlock(restricted.TerminalLimits, f.env.hub.onlineAgent(f.a.code)) != "" {
		t.Fatal("scopeBlock 不应拦目录令牌")
	}
}

func TestFilesWholeFSTargetsEcho(t *testing.T) {
	f := newFilesFixture(t)
	restricted := f.newCred(t, nil)
	whole := wholeFSCred(f, t, "/srv/data") // 保留着目录设置

	res, err := f.env.hub.Exec().Targets(context.Background(), callerOf(whole), "")
	if err != nil {
		t.Fatal(err)
	}
	tr := res.(*termexec.TargetsResult)
	if !tr.Targets[0].Files || tr.Limits == nil || !tr.Limits.WholeFS || len(tr.Limits.AllowedPaths) != 0 {
		t.Fatalf("整个文件系统的令牌应回显 whole_fs 且不列出被保留的目录: %+v", tr.Limits)
	}
	if !strings.Contains(tr.Hint, "whole_fs") {
		t.Fatalf("提示应解释 whole_fs: %q", tr.Hint)
	}

	res, _ = f.env.hub.Exec().Targets(context.Background(), callerOf(restricted), "")
	tr = res.(*termexec.TargetsResult)
	if tr.Limits == nil || tr.Limits.WholeFS || len(tr.Limits.AllowedPaths) != 1 {
		t.Fatalf("目录令牌的回显 = %+v", tr.Limits)
	}
}

func TestFilesWholeFSPullAndDownloadFollowTheCurrentSwitch(t *testing.T) {
	f := newFilesFixture(t)
	body := []byte("host-name-file")
	f.fs.put("/etc/hostname", body)
	whole := wholeFSCred(f, t)

	pull := f.prepare(t, whole, "/etc/hostname")
	r, got, err := f.get(t, pull.URL)
	if err != nil || r.StatusCode != 200 || string(got) != string(body) {
		t.Fatalf("整个文件系统的下载: %v %+v %q", err, r, got)
	}
	opens := f.fs.requests(termproto.FileOpOpen)
	for _, o := range opens {
		if len(o.Roots) != 1 || o.Roots[0] != termproto.FileRootAll {
			t.Fatalf("下载时重新授权也应只下发哨兵: %+v", o)
		}
	}
	downloads := f.auditsOf(t, EvFileDownload)
	if len(downloads) != 1 || !strings.Contains(downloads[0].Detail, auditScopeWholeFS) {
		t.Fatalf("下载审计应带整个文件系统标记: %+v", downloads)
	}

	// 签发之后开关被收回：已签发的票据立即失效（下载用令牌「当前」的范围，而不是签发时的快照）
	pull = f.prepare(t, whole, "/etc/hostname")
	if _, err := f.env.store.UpdateCredential(context.Background(), whole.ID, map[string]any{
		"terminal_limits": map[string]any{"allowed_paths": []any{"/srv/data"}},
	}); err != nil {
		t.Fatal(err)
	}
	if r, b, _ := f.get(t, pull.URL); r.StatusCode != http.StatusForbidden || strings.Contains(string(b), string(body)) {
		t.Fatalf("开关收回后票据应被拒绝: %d %q", r.StatusCode, b)
	}
}

func TestCredentialStillAllowsFilesWithWholeFS(t *testing.T) {
	now := time.Now()
	c := &model.MCPCredential{
		IsActive: true, Permissions: model.Permissions{TerminalFiles: true},
		TerminalHosts: model.StringList{"m1"}, TerminalLimits: model.TerminalLimits{AllowWholeFS: true},
	}
	if !credentialStillAllowsFiles(c, "m1", now) {
		t.Fatal("只有整个文件系统开关（没有目录）也应允许")
	}
	c.TerminalLimits = model.TerminalLimits{}
	if credentialStillAllowsFiles(c, "m1", now) {
		t.Fatal("什么范围都没有时不应允许")
	}
}

// ---------- 重新验证密码（新鲜解锁） ----------

func freshRoute(e *testEnv) []string {
	e.engine.POST("/test/fresh", func(c *gin.Context) {
		if !e.hub.RequireFreshUnlock(c, "令牌 #7「x」：开启「允许整个文件系统」") {
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return []string{"Authorization", humanAuth(testUser)}
}

func TestRequireFreshUnlock(t *testing.T) {
	e := newTestEnv(t, withFakeClock())
	login := freshRoute(e)
	tok := e.unlock(testUser)
	unlocked := append(append([]string{}, login...), unlockHeader, tok)

	// 刚换取的解锁令牌：通过
	if r := e.call("POST", "/test/fresh", nil, unlocked...); r.Status != http.StatusOK {
		t.Fatalf("刚解锁应通过: %d %s", r.Status, r.Raw)
	}
	// 窗口之内仍然新鲜
	e.clock.Advance(FreshUnlockWindow - 10*time.Second)
	if r := e.call("POST", "/test/fresh", nil, unlocked...); r.Status != http.StatusOK {
		t.Fatalf("窗口内应通过: %d %s", r.Status, r.Raw)
	}
	// 超过窗口：令牌本身仍然有效（普通解锁照常），但不再算「刚刚输入过密码」
	e.clock.Advance(30 * time.Second)
	r := e.call("POST", "/test/fresh", nil, unlocked...)
	if r.Status != http.StatusForbidden || r.str("code") != "unlock_fresh_required" {
		t.Fatalf("过了窗口应要求重新输入密码: %d %s", r.Status, r.Raw)
	}
	if secs, _ := r.Body["fresh_window_seconds"].(float64); int(secs) != int(FreshUnlockWindow/time.Second) {
		t.Fatalf("应告知窗口长度: %s", r.Raw)
	}
	if st := e.call("GET", "/api/v1/terminal/status", nil, unlocked...); st.Body["unlocked"] != true {
		t.Fatalf("新鲜度不足不应让令牌本身失效: %s", st.Raw)
	}
	// 重新输入密码换取新令牌：通过
	fresh := append(append([]string{}, login...), unlockHeader, e.unlock(testUser))
	if r := e.call("POST", "/test/fresh", nil, fresh...); r.Status != http.StatusOK {
		t.Fatalf("重新解锁后应通过: %d %s", r.Status, r.Raw)
	}

	// 区分原因：没有解锁令牌是 unlock_required，不是 unlock_fresh_required；未登录是 401
	if r := e.call("POST", "/test/fresh", nil, login...); r.Status != http.StatusForbidden || r.str("code") != "unlock_required" {
		t.Fatalf("没有解锁令牌应是 unlock_required: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/test/fresh", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d %s", r.Status, r.Raw)
	}

	// 被拒绝的那次写了（限流的）审计，并说明是哪个操作；成功不写审计（成功由调用方在改动落地后写）
	var denies []model.TerminalAuditLog
	logs, err := e.store.ListTerminalAudit(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if l.Event == EvFilesScope {
			denies = append(denies, l)
		}
	}
	if len(denies) != 1 || denies[0].Result != ResultDeny || !strings.Contains(denies[0].Detail, "令牌 #7") || denies[0].Actor != testUser {
		t.Fatalf("审计 = %+v", denies)
	}
}

func TestFilesScopeChangedAudit(t *testing.T) {
	e := newTestEnv(t)
	e.hub.FilesScopeChanged("admin", "203.0.113.9", ResultOK, "令牌 #3「ci」：开启「允许整个文件系统」")
	logs, err := e.store.ListTerminalAudit(context.Background(), 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Event != EvFilesScope || logs[0].Result != ResultOK || logs[0].Actor != "admin" ||
		logs[0].IP != "203.0.113.9" || !strings.Contains(logs[0].Detail, "开启") {
		t.Fatalf("审计 = %+v", logs)
	}
	var nilHub *Hub
	nilHub.FilesScopeChanged("a", "b", ResultOK, "x") // nil 安全：没有终端子系统时不应 panic
}
