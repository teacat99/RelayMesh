package terminal

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 主机对新建模型授权的默认态（D-201 / P7）：
// 默认态只在「新增主机」时授予现有令牌、在「新建令牌」时预填，写成令牌名单里的明确条目；运行期间不做继承。

// mkModelToken 创建一个模型令牌（直接写库，绕开 REST 的解锁要求）。
func (e *testEnv) mkModelToken(name string, mut func(*model.MCPCredential)) *model.MCPCredential {
	e.t.Helper()
	c := &model.MCPCredential{Name: name, IsActive: true, Permissions: model.Permissions{Terminal: true}}
	if mut != nil {
		mut(c)
	}
	if err := e.store.CreateCredential(context.Background(), c); err != nil {
		e.t.Fatalf("创建令牌 %s: %v", name, err)
	}
	return c
}

func (e *testEnv) tokenHosts(name string) []string {
	e.t.Helper()
	creds, err := e.store.ListCredentials(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range creds {
		if c.Name == name {
			return slices.Clone([]string(c.TerminalHosts))
		}
	}
	e.t.Fatalf("没有名为 %s 的令牌", name)
	return nil
}

func grantedNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	g, _ := body["grant"].(map[string]any)
	if g == nil {
		return nil
	}
	list, _ := g["granted"].([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out
}

// seedModelTokens 建一组覆盖各类边界的令牌：普通、已有其他主机、通配、能力 URL、没有终端权限。
func seedModelTokens(e *testEnv) {
	e.mkModelToken("普通", nil)
	e.mkModelToken("已有其他主机", func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"RM-OTHER-0001"} })
	e.mkModelToken("通配", func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"*"} })
	e.mkModelToken("能力URL", func(c *model.MCPCredential) {
		c.Capability = true
		c.TerminalHosts = model.StringList{"RM-OTHER-0002"}
	})
	e.mkModelToken("无终端权限", func(c *model.MCPCredential) { c.Permissions = model.Permissions{Feedback: true} })
}

func hostViewOf(t *testing.T, e *testEnv, code string) map[string]any {
	t.Helper()
	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	list, _ := res.Body["hosts"].([]any)
	for _, v := range list {
		if m, ok := v.(map[string]any); ok && m["machine_code"] == code {
			return m
		}
	}
	t.Fatalf("主机列表里没有 %s: %s", code, res.Raw)
	return nil
}

func auditHas(t *testing.T, e *testEnv, event, sub string) bool {
	t.Helper()
	logs, err := e.store.ListTerminalAudit(context.Background(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if l.Event == event && strings.Contains(l.Detail, sub) {
			return true
		}
	}
	return false
}

func TestNewHostDefaultAllowGrantsExistingModelTokens(t *testing.T) {
	e := newTestEnv(t)
	seedModelTokens(e)

	a := e.enrollAgentWith(testMeta("build-01"), map[string]any{"remark": "构建机", "default_for_new_models": "allow"})

	if got := grantedNames(t, a.enableBody); !slices.Equal(got, []string{"已有其他主机", "普通"}) {
		t.Fatalf("响应应列出被授予的令牌（已有其他主机、普通），得到 %v", got)
	}
	if hosts := e.tokenHosts("普通"); !slices.Equal(hosts, []string{a.code}) {
		t.Fatalf("普通令牌应获得明确条目 %s，得到 %v", a.code, hosts)
	}
	if hosts := e.tokenHosts("已有其他主机"); !slices.Equal(hosts, []string{"RM-OTHER-0001", a.code}) {
		t.Fatalf("既有条目应保留并追加新主机，得到 %v", hosts)
	}
	if hosts := e.tokenHosts("通配"); !slices.Equal(hosts, []string{"*"}) {
		t.Fatalf("通配令牌不应被改写，得到 %v", hosts)
	}
	if hosts := e.tokenHosts("能力URL"); !slices.Equal(hosts, []string{"RM-OTHER-0002"}) {
		t.Fatalf("能力 URL 令牌从不被自动授予，得到 %v", hosts)
	}
	if hosts := e.tokenHosts("无终端权限"); len(hosts) != 0 {
		t.Fatalf("没有终端权限的令牌不应被授予，得到 %v", hosts)
	}

	if v := hostViewOf(t, e, a.code); v["default_for_new_models"] != "allow" {
		t.Fatalf("主机视图应显示 allow: %v", v["default_for_new_models"])
	}
	if !auditHas(t, e, EvModelGrant, "reason=new_host granted=2") {
		t.Fatal("应写入「按默认态授予」的审计记录，含授予数量")
	}
}

func TestNewHostDefaultsToDenyAndTouchesNothing(t *testing.T) {
	e := newTestEnv(t)
	seedModelTokens(e)

	a := e.enrollAgent(testMeta("build-02"), "默认不授予")

	if _, has := a.enableBody["grant"]; has {
		t.Fatalf("默认态不授予时，响应不应带 grant: %v", a.enableBody)
	}
	if hosts := e.tokenHosts("普通"); len(hosts) != 0 {
		t.Fatalf("令牌名单应保持不变，得到 %v", hosts)
	}
	if v := hostViewOf(t, e, a.code); v["default_for_new_models"] != "deny" {
		t.Fatalf("默认应为 deny: %v", v["default_for_new_models"])
	}
	if auditHas(t, e, EvModelGrant, "") {
		t.Fatal("没有发生授予，不应有授予审计")
	}
}

func TestEnableRejectsUnknownDefaultState(t *testing.T) {
	e := newTestEnv(t)
	hdr := e.admin()
	res := e.call("POST", "/api/v1/terminal/hosts/RM-AAAA-BBBB-CCCC-DDDD/enable", map[string]any{"default_for_new_models": "maybe"}, hdr...)
	if res.Status != 400 {
		t.Fatalf("非法默认态应返回 400: %d %s", res.Status, res.Raw)
	}
}

func TestReEnableNeverRewritesTokenLists(t *testing.T) {
	e := newTestEnv(t)
	e.mkModelToken("普通", nil)
	hdr := e.admin()

	a := e.enrollAgentWith(testMeta("build-03"), map[string]any{"default_for_new_models": "allow"})
	if hosts := e.tokenHosts("普通"); !slices.Equal(hosts, []string{a.code}) {
		t.Fatalf("首次新增应授予: %v", hosts)
	}

	// 管理员后来把这台主机从令牌里移除（这是已确认的事实），停用再启用不应把它加回来。
	creds, _ := e.store.ListCredentials(context.Background())
	if _, err := e.store.UpdateCredential(context.Background(), creds[0].ID, map[string]any{"terminal_hosts": []any{}}); err != nil {
		t.Fatal(err)
	}
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/disable", nil, hdr...); res.Status != 200 {
		t.Fatalf("停用失败: %d %s", res.Status, res.Raw)
	}
	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/enable", map[string]any{"default_for_new_models": "allow"}, hdr...)
	if res.Status != 200 {
		t.Fatalf("重新启用失败: %d %s", res.Status, res.Raw)
	}
	if _, has := res.Body["grant"]; has {
		t.Fatalf("重新启用不应授予: %s", res.Raw)
	}
	if hosts := e.tokenHosts("普通"); len(hosts) != 0 {
		t.Fatalf("重新启用不应改写令牌名单，得到 %v", hosts)
	}
}

func TestHostModelDefaultFlagOnlyAffectsFuture(t *testing.T) {
	e := newTestEnv(t)
	e.mkModelToken("普通", nil)
	a := e.enrollAgent(testMeta("build-04"), "")
	hdr := e.admin()
	url := "/api/v1/terminal/hosts/" + a.code + "/model"

	// 需要解锁：只有登录不够。
	if res := e.call("POST", url, map[string]any{"default_for_new_models": "allow"}, "Authorization", humanAuth(testUser)); res.Status == 200 {
		t.Fatalf("修改默认态应要求解锁: %s", res.Raw)
	}
	if res := e.call("POST", url, map[string]any{"default_for_new_models": "sometimes"}, hdr...); res.Status != 400 {
		t.Fatalf("非法取值应返回 400: %d %s", res.Status, res.Raw)
	}
	res := e.call("POST", url, map[string]any{"default_for_new_models": "allow"}, hdr...)
	if res.Status != 200 {
		t.Fatalf("修改默认态失败: %d %s", res.Status, res.Raw)
	}
	if host, _ := res.Body["host"].(map[string]any); host == nil || host["default_for_new_models"] != "allow" {
		t.Fatalf("响应应带更新后的主机视图: %s", res.Raw)
	}
	if hosts := e.tokenHosts("普通"); len(hosts) != 0 {
		t.Fatalf("改默认态不应改动现有令牌，得到 %v", hosts)
	}
	codes, err := e.store.DefaultAllowHostCodes(context.Background())
	if err != nil || !slices.Equal(codes, []string{a.code}) {
		t.Fatalf("新建令牌的预填集合应包含该主机: %v %v", codes, err)
	}
	if !auditHas(t, e, EvHostModel, "default_new=allow") {
		t.Fatal("审计应记录默认态的变化")
	}

	// 改回 deny：预填集合清空。
	if res := e.call("POST", url, map[string]any{"default_for_new_models": "deny"}, hdr...); res.Status != 200 {
		t.Fatalf("改回 deny 失败: %d %s", res.Status, res.Raw)
	}
	if codes, _ := e.store.DefaultAllowHostCodes(context.Background()); len(codes) != 0 {
		t.Fatalf("deny 后预填集合应为空: %v", codes)
	}
}

func TestGrantModelsPreviewThenApply(t *testing.T) {
	e := newTestEnv(t)
	seedModelTokens(e)
	a := e.enrollAgent(testMeta("build-05"), "")
	hdr := e.admin()
	login := []string{"Authorization", humanAuth(testUser)}
	url := "/api/v1/terminal/hosts/" + a.code + "/grant-models"

	// 预览只需登录，且不写任何数据。
	res := e.call("GET", url, nil, login...)
	if res.Status != 200 {
		t.Fatalf("预览失败: %d %s", res.Status, res.Raw)
	}
	if got := grantedNames(t, res.Body); !slices.Equal(got, []string{"已有其他主机", "普通"}) {
		t.Fatalf("预览应列出会被授予的令牌，得到 %v", got)
	}
	if hosts := e.tokenHosts("普通"); len(hosts) != 0 {
		t.Fatalf("预览不应写入: %v", hosts)
	}

	// 执行需要解锁。
	if res := e.call("POST", url, nil, login...); res.Status == 200 {
		t.Fatalf("授予应要求解锁: %s", res.Raw)
	}
	res = e.call("POST", url, nil, hdr...)
	if res.Status != 200 {
		t.Fatalf("授予失败: %d %s", res.Status, res.Raw)
	}
	if got := grantedNames(t, res.Body); !slices.Equal(got, []string{"已有其他主机", "普通"}) {
		t.Fatalf("授予结果不符: %v", got)
	}
	if hosts := e.tokenHosts("普通"); !slices.Equal(hosts, []string{a.code}) {
		t.Fatalf("普通令牌应获得条目: %v", hosts)
	}
	if !auditHas(t, e, EvModelGrant, "reason=manual granted=2") {
		t.Fatal("手动授予应写审计")
	}

	// 幂等：再次授予没有新增。
	res = e.call("POST", url, nil, hdr...)
	if got := grantedNames(t, res.Body); len(got) != 0 {
		t.Fatalf("再次授予不应有新增: %v", got)
	}
	if hosts := e.tokenHosts("普通"); !slices.Equal(hosts, []string{a.code}) {
		t.Fatalf("重复授予不应产生重复条目: %v", hosts)
	}
}

func TestGrantModelsRejectsUnusableHosts(t *testing.T) {
	e := newTestEnv(t)
	e.mkModelToken("普通", nil)
	a := e.enrollAgent(testMeta("build-06"), "")
	hdr := e.admin()

	if res := e.call("POST", "/api/v1/terminal/hosts/RM-AAAA-BBBB-CCCC-DDDD/grant-models", nil, hdr...); res.Status != 404 {
		t.Fatalf("不存在的主机应返回 404: %d %s", res.Status, res.Raw)
	}
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/disable", nil, hdr...); res.Status != 200 {
		t.Fatalf("停用失败: %d %s", res.Status, res.Raw)
	}
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/grant-models", nil, hdr...); res.Status != 404 {
		t.Fatalf("已停用的主机不应被授予: %d %s", res.Status, res.Raw)
	}
	if hosts := e.tokenHosts("普通"); len(hosts) != 0 {
		t.Fatalf("被拒绝的请求不应改动令牌: %v", hosts)
	}
}

// 待启用的持久主机可以预览（确认启用时告诉管理员「将授予 N 个模型」），但不能直接授予；未知主机一律 404。
func TestGrantPreviewWorksForPendingHostsButApplyDoesNot(t *testing.T) {
	e := newTestEnv(t)
	e.mkModelToken("普通", nil)
	hdr := e.admin()
	login := []string{"Authorization", humanAuth(testUser)}
	e.openDiscovery(hdr)

	_, priv := newFakeIdentity(t)
	a, _, err := e.dialAgent(priv, testMeta("pending-01"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.handshake(testMeta("pending-01")); err != nil {
		t.Fatal(err)
	}
	a.start()
	t.Cleanup(a.close)
	eventually(t, 2*time.Second, "出现在待启用列表", func() bool { _, ok := e.hub.pending.get(a.code); return ok })

	url := "/api/v1/terminal/hosts/" + a.code + "/grant-models"
	res := e.call("GET", url, nil, login...)
	if res.Status != 200 {
		t.Fatalf("待启用主机应可预览: %d %s", res.Status, res.Raw)
	}
	if got := grantedNames(t, res.Body); !slices.Equal(got, []string{"普通"}) {
		t.Fatalf("预览结果不符: %v", got)
	}
	if res := e.call("POST", url, nil, hdr...); res.Status != 404 {
		t.Fatalf("待启用主机不能直接授予: %d %s", res.Status, res.Raw)
	}
	if hosts := e.tokenHosts("普通"); len(hosts) != 0 {
		t.Fatalf("预览与被拒绝的授予都不应写入: %v", hosts)
	}
	if res := e.call("GET", "/api/v1/terminal/hosts/RM-AAAA-BBBB-CCCC-DDDD/grant-models", nil, login...); res.Status != 404 {
		t.Fatalf("未知主机应返回 404: %d %s", res.Status, res.Raw)
	}
}
