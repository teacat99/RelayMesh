package terminal

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func lifecyclePath(code, sid string) string {
	if sid == "" {
		return "/api/v1/terminal/hosts/" + code + "/lifecycle"
	}
	return "/api/v1/terminal/hosts/" + code + "/sessions/" + sid + "/lifecycle"
}

// hubSession 通过 REST 读取 Hub 视角下的会话信息。
func (e *testEnv) hubSession(code, sid string) (termproto.SessionInfo, bool) {
	e.t.Helper()
	res := e.call("GET", "/api/v1/terminal/hosts/"+code+"/sessions", nil, e.admin()...)
	if res.Status != 200 {
		e.t.Fatalf("会话列表: %d %s", res.Status, res.Raw)
	}
	var body struct {
		Sessions []termproto.SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(res.Raw), &body); err != nil {
		e.t.Fatal(err)
	}
	for _, s := range body.Sessions {
		if s.SID == sid {
			return s, true
		}
	}
	return termproto.SessionInfo{}, false
}

func TestHostLifecycleDefaultsToPermanentAndPushesPolicy(t *testing.T) {
	e := newTestEnv(t)
	meta := testMeta("life-host")
	a := e.enrollAgent(meta, "")
	hdr := e.admin()

	// 默认：永久保持，批准时下发的策略不带空闲回收，也不再带固定的断开保留期。
	if a.welcome.Policy.IdleSec != 0 || a.welcome.Policy.DetachedTTLSec != 0 {
		t.Fatalf("默认应永久保持: %+v", a.welcome.Policy)
	}
	hosts := e.call("GET", "/api/v1/terminal/hosts", nil, hdr...)
	if hosts.Status != 200 {
		t.Fatalf("主机列表: %d %s", hosts.Status, hosts.Raw)
	}

	res := e.call("POST", lifecyclePath(a.code, ""), map[string]int{"idle_seconds": 86400}, hdr...)
	if res.Status != 200 {
		t.Fatalf("设置主机默认生命周期: %d %s", res.Status, res.Raw)
	}
	host, _ := res.Body["host"].(map[string]any)
	if host["idle_sec"] != float64(86400) {
		t.Fatalf("主机视图应反映 idle_sec: %s", res.Raw)
	}
	eventually(t, 2*time.Second, "Agent 收到新策略", func() bool {
		p, ok := lastPolicy(a)
		return ok && p.IdleSec == 86400 && p.Terminal
	})
	if n := len(e.auditEvents(EvLifecycle)); n != 1 {
		t.Fatalf("应有 1 条生命周期审计: %d", n)
	}
	// 重复设置相同的值：不再审计。
	e.call("POST", lifecyclePath(a.code, ""), map[string]int{"idle_seconds": 86400}, hdr...)
	if n := len(e.auditEvents(EvLifecycle)); n != 1 {
		t.Fatalf("重复设置不应再次审计: %d", n)
	}

	// 重连后的 welcome 仍带这份策略（策略保存在 Hub）。
	a.close()
	b := e.connectEnabled(a.priv, meta)
	if b.welcome.Policy.IdleSec != 86400 {
		t.Fatalf("重连后策略应保留: %+v", b.welcome.Policy)
	}

	// 改回永久保持。
	if res := e.call("POST", lifecyclePath(a.code, ""), map[string]int{"idle_seconds": 0}, hdr...); res.Status != 200 {
		t.Fatalf("改回永久保持: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "Agent 收到永久保持的策略", func() bool {
		p, ok := lastPolicy(b)
		return ok && p.IdleSec == 0
	})
}

func TestLifecycleRejectsOutOfRangeValuesAndUnknownHosts(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("life-range"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "main")

	for _, v := range []int{-1, 1, 59, 3599, 30*24*3600 + 1, 1 << 40} {
		if res := e.call("POST", lifecyclePath(a.code, ""), map[string]int{"idle_seconds": v}, hdr...); res.Status != http.StatusBadRequest {
			t.Fatalf("主机默认 %d 秒应被拒绝: %d %s", v, res.Status, res.Raw)
		}
		if res := e.call("POST", lifecyclePath(a.code, sid), map[string]int{"idle_seconds": v}, hdr...); res.Status != http.StatusBadRequest {
			t.Fatalf("会话 %d 秒应被拒绝: %d %s", v, res.Status, res.Raw)
		}
		if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions", map[string]any{"idle_seconds": v}, hdr...); res.Status != http.StatusBadRequest {
			t.Fatalf("新建会话 %d 秒应被拒绝: %d %s", v, res.Status, res.Raw)
		}
	}
	for _, v := range []int{0, 3600, 7 * 24 * 3600, 30 * 24 * 3600} {
		if res := e.call("POST", lifecyclePath(a.code, ""), map[string]int{"idle_seconds": v}, hdr...); res.Status != 200 {
			t.Fatalf("主机默认 %d 秒应被接受: %d %s", v, res.Status, res.Raw)
		}
	}

	unknownPub, _ := newFakeIdentity(t)
	if res := e.call("POST", lifecyclePath(termproto.MachineCode(unknownPub), ""), map[string]int{"idle_seconds": 3600}, hdr...); res.Status != http.StatusNotFound {
		t.Fatalf("未知主机应 404: %d %s", res.Status, res.Raw)
	}
	// 未解锁、未登录的访问都被挡住。
	loginOnly := []string{"Authorization", humanAuth(testUser)}
	if res := e.call("POST", lifecyclePath(a.code, ""), map[string]int{"idle_seconds": 3600}, loginOnly...); res.Status != http.StatusForbidden {
		t.Fatalf("修改生命周期需要解锁: %d %s", res.Status, res.Raw)
	}
	if code, _, _ := e.callRaw("POST", lifecyclePath(a.code, sid), "Authorization", "Bearer mcp-token", unlockHeader, "x"); code != http.StatusUnauthorized {
		t.Fatalf("非人类凭据应 401: %d", code)
	}
}

func TestSessionLifecycleIsForwardedToTheAgent(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("life-session"), "")
	hdr := e.admin()
	sid := e.openSession(a, hdr, "main")

	res := e.call("POST", lifecyclePath(a.code, sid), map[string]int{"idle_seconds": 7200}, hdr...)
	if res.Status != 200 {
		t.Fatalf("设置会话生命周期: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "Agent 收到会话生命周期", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.lifecycle) == 1 && a.lifecycle[0].SID == sid && a.lifecycle[0].IdleSec != nil && *a.lifecycle[0].IdleSec == 7200
	})
	eventually(t, 2*time.Second, "Hub 会话列表反映新的生命周期", func() bool {
		info, ok := e.hubSession(a.code, sid)
		return ok && info.IdleOwn && info.Idle == 7200
	})

	// null = 改回沿用主机默认；0 = 永久保持，两者在线路上必须区分。
	if res := e.call("POST", lifecyclePath(a.code, sid), map[string]any{"idle_seconds": nil}, hdr...); res.Status != 200 {
		t.Fatalf("改回沿用主机默认: %d %s", res.Status, res.Raw)
	}
	if res := e.call("POST", lifecyclePath(a.code, sid), map[string]int{"idle_seconds": 0}, hdr...); res.Status != 200 {
		t.Fatalf("设为永久保持: %d %s", res.Status, res.Raw)
	}
	eventually(t, 2*time.Second, "两次修改都到达 Agent", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.lifecycle) == 3
	})
	a.mu.Lock()
	inherit, permanent := a.lifecycle[1], a.lifecycle[2]
	a.mu.Unlock()
	if inherit.IdleSec != nil {
		t.Fatalf("沿用主机默认应以缺省字段表示: %+v", inherit)
	}
	if permanent.IdleSec == nil || *permanent.IdleSec != 0 {
		t.Fatalf("永久保持应以 0 表示: %+v", permanent)
	}
	if n := len(e.auditEvents(EvLifecycle)); n != 3 {
		t.Fatalf("每次修改都应审计: %d", n)
	}

	// 不存在的会话 / 已结束的会话 / 格式不对的会话 ID。
	if res := e.call("POST", lifecyclePath(a.code, randHex(16)), map[string]int{"idle_seconds": 3600}, hdr...); res.Status != http.StatusNotFound {
		t.Fatalf("未知会话应 404: %d %s", res.Status, res.Raw)
	}
	if res := e.call("POST", lifecyclePath(a.code, "not-a-sid"), map[string]int{"idle_seconds": 3600}, hdr...); res.Status != http.StatusBadRequest {
		t.Fatalf("格式不对应 400: %d %s", res.Status, res.Raw)
	}
}

func TestOpenSessionCarriesLifecycleAndWorkingDirectory(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("life-open"), "")
	hdr := e.admin()

	res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions", map[string]any{
		"name": "rebuilt", "idle_seconds": 86400, "cwd": "/srv/app",
	}, hdr...)
	if res.Status != 200 {
		t.Fatalf("新建会话: %d %s", res.Status, res.Raw)
	}
	a.mu.Lock()
	open := a.opens[len(a.opens)-1]
	a.mu.Unlock()
	if open.IdleSec == nil || *open.IdleSec != 86400 || open.Cwd != "/srv/app" {
		t.Fatalf("Open 帧应带生命周期与目录: %+v", open)
	}

	// 缺省：不带这两个字段，沿用主机默认。
	if res := e.call("POST", "/api/v1/terminal/hosts/"+a.code+"/sessions", map[string]any{"name": "plain"}, hdr...); res.Status != 200 {
		t.Fatalf("新建会话: %d %s", res.Status, res.Raw)
	}
	a.mu.Lock()
	open = a.opens[len(a.opens)-1]
	a.mu.Unlock()
	if open.IdleSec != nil || open.Cwd != "" {
		t.Fatalf("缺省时不应带生命周期或目录: %+v", open)
	}
}
