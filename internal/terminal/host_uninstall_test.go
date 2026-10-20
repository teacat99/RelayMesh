package terminal

import (
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 删除主机时「同时卸载设备端」：
//   - 只有登录 + 解锁的管理员能触发（路由的 guard），默认不卸载；
//   - 只向在线、声明了 Meta.Uninstall 的 Agent 下发，其余情形如实返回 offline / unsupported；
//   - uninstall 帧先于关闭策略到达（帧有序），随后连接才被关闭；
//   - 结果写进审计。

func upgradedMeta(host string) termproto.Meta {
	m := testMeta(host)
	m.Uninstall = true
	return m
}

func deleteHostURL(code string, uninstall bool) string {
	u := "/api/v1/terminal/hosts/" + code
	if uninstall {
		u += "?uninstall=1"
	}
	return u
}

func TestDeleteHostWithUninstallRequestsAgentBeforeClosing(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(upgradedMeta("uninst-ok"), "")
	hdr := e.admin()

	// 列表里能看到这台主机可以被同时卸载（界面据此提供勾选项）
	list := e.call("GET", "/api/v1/terminal/hosts", nil, hdr...)
	var seen bool
	for _, h := range list.Body["hosts"].([]any) {
		m := h.(map[string]any)
		if m["machine_code"] == a.code {
			seen = true
			if m["can_uninstall"] != true {
				t.Fatalf("在线且声明了能力的 Agent 应带 can_uninstall: %v", m)
			}
		}
	}
	if !seen {
		t.Fatalf("列表里应有这台主机: %s", list.Raw)
	}

	res := e.call("DELETE", deleteHostURL(a.code, true), nil, hdr...)
	if res.Status != 200 || res.Body["uninstall"] != "requested" {
		t.Fatalf("应返回已请求卸载: %d %s", res.Status, res.Raw)
	}
	if code := a.waitClosed(3 * time.Second); code != termproto.CloseDisabled {
		t.Fatalf("随后连接应以 CloseDisabled 关闭: %d", code)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.uninstalls) != 1 || !a.uninstalls[0].Purge {
		t.Fatalf("Agent 应收到一次卸载请求（删除主机后身份无用，purge）: %+v", a.uninstalls)
	}
	if got := strings.Join(a.frameOrder, ","); got != "uninstall,policy" {
		t.Fatalf("必须先收到卸载、再收到关闭策略: %s", got)
	}

	logs := e.auditEvents(EvHostDeleted)
	if len(logs) != 1 || !strings.Contains(logs[0].Detail, "uninstall=requested") {
		t.Fatalf("审计应记录卸载请求的结果: %+v", logs)
	}
	if rec, _ := e.store.GetTerminalHost(t.Context(), a.code); rec != nil {
		t.Fatal("主机记录应已删除")
	}
}

func TestDeleteHostDefaultsToNotUninstalling(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(upgradedMeta("uninst-default"), "")
	hdr := e.admin()

	res := e.call("DELETE", deleteHostURL(a.code, false), nil, hdr...)
	if res.Status != 200 {
		t.Fatalf("删除: %d %s", res.Status, res.Raw)
	}
	if _, has := res.Body["uninstall"]; has {
		t.Fatalf("没有请求卸载时响应里不应出现 uninstall 字段: %s", res.Raw)
	}
	a.waitClosed(3 * time.Second)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.uninstalls) != 0 {
		t.Fatalf("默认删除不应卸载设备端: %+v", a.uninstalls)
	}
	if logs := e.auditEvents(EvHostDeleted); len(logs) != 1 || strings.Contains(logs[0].Detail, "uninstall") {
		t.Fatalf("审计里不应出现 uninstall: %+v", logs)
	}
}

func TestDeleteHostUninstallUnsupportedAgentIsReportedHonestly(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("uninst-old"), "") // 旧版本 / 前台运行：不声明能力
	hdr := e.admin()

	list := e.call("GET", "/api/v1/terminal/hosts", nil, hdr...)
	for _, h := range list.Body["hosts"].([]any) {
		if m := h.(map[string]any); m["machine_code"] == a.code {
			if _, has := m["can_uninstall"]; has {
				t.Fatalf("没有声明能力的 Agent 不应带 can_uninstall: %v", m)
			}
		}
	}

	// 即使有人绕过界面直接带上参数，Hub 也不会向不支持的 Agent 发送卸载帧
	res := e.call("DELETE", deleteHostURL(a.code, true), nil, hdr...)
	if res.Status != 200 || res.Body["uninstall"] != "unsupported" {
		t.Fatalf("应如实返回 unsupported: %d %s", res.Status, res.Raw)
	}
	a.waitClosed(3 * time.Second)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.uninstalls) != 0 {
		t.Fatalf("不支持的 Agent 不应收到卸载帧: %+v", a.uninstalls)
	}
	if logs := e.auditEvents(EvHostDeleted); len(logs) != 1 || !strings.Contains(logs[0].Detail, "uninstall=unsupported") {
		t.Fatalf("审计应记录 unsupported: %+v", logs)
	}
}

func TestDeleteOfflineHostWithUninstallSaysOffline(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(upgradedMeta("uninst-off"), "")
	hdr := e.admin()
	a.close()
	eventually(t, 3*time.Second, "主机离线", func() bool { return e.hub.onlineAgent(a.code) == nil })

	res := e.call("DELETE", deleteHostURL(a.code, true), nil, hdr...)
	if res.Status != 200 || res.Body["uninstall"] != "offline" {
		t.Fatalf("离线主机无法下发，应如实返回 offline（界面据此提示手动卸载）: %d %s", res.Status, res.Raw)
	}
	if rec, _ := e.store.GetTerminalHost(t.Context(), a.code); rec != nil {
		t.Fatal("离线时主机记录仍应被删除")
	}
}

func TestDeleteHostUninstallRequiresUnlock(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(upgradedMeta("uninst-lock"), "")

	// 只登录、未解锁：与删除主机本身一样被拒绝，卸载帧绝不会下发
	locked := []string{"Authorization", humanAuth(testUser)}
	res := e.call("DELETE", deleteHostURL(a.code, true), nil, locked...)
	if res.Status == 200 {
		t.Fatalf("未解锁不应能删除 / 卸载: %d %s", res.Status, res.Raw)
	}
	a.mu.Lock()
	n := len(a.uninstalls)
	a.mu.Unlock()
	if n != 0 {
		t.Fatalf("被拒绝的请求不应下发卸载: %d", n)
	}
	if rec, _ := e.store.GetTerminalHost(t.Context(), a.code); rec == nil {
		t.Fatal("被拒绝时主机记录应保留")
	}
}
