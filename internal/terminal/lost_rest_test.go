package terminal

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 主机页「会话已不存在」提示可以手动忽略（第六批 #28）：只需要登录；忽略由 Hub 记住，
// 所以守护进程之后再次上报同样的丢失会话也不会重新出现；新的丢失照常提醒。

func lostSID(c byte) string { return strings.Repeat(string(c), 32) }

func (e *testEnv) hostLost(code string) []string {
	e.t.Helper()
	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	if res.Status != http.StatusOK {
		e.t.Fatalf("主机列表: %d %s", res.Status, res.Raw)
	}
	hosts, _ := res.Body["hosts"].([]any)
	for _, raw := range hosts {
		h, _ := raw.(map[string]any)
		if h["machine_code"] != code {
			continue
		}
		holder, _ := h["holder"].(map[string]any)
		lost, _ := holder["lost"].([]any)
		out := make([]string, 0, len(lost))
		for _, l := range lost {
			m, _ := l.(map[string]any)
			sid, _ := m["sid"].(string)
			out = append(out, sid)
		}
		return out
	}
	e.t.Fatalf("主机列表里没有 %s: %s", code, res.Raw)
	return nil
}

func (e *testEnv) dismissLost(code string, body map[string]any) apiResult {
	e.t.Helper()
	return e.call("POST", "/api/v1/terminal/hosts/"+code+"/lost/dismiss", body, "Authorization", humanAuth(testUser))
}

func reportLost(a *fakeAgent, sids ...string) {
	at := time.Now().Add(-time.Hour).Unix()
	lost := make([]termproto.LostSession, 0, len(sids))
	for _, s := range sids {
		lost = append(lost, termproto.LostSession{SID: s, Name: "n-" + s[:4], Reason: termproto.LostHolderStopped, At: at})
	}
	a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Lost: lost})
}

func TestDismissLostHidesOneAndSurvivesRereport(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("lost-host"), "")
	s1, s2 := lostSID('1'), lostSID('2')

	reportLost(a, s1, s2)
	eventually(t, 3*time.Second, "两条丢失提示可见", func() bool { return len(e.hostLost(a.code)) == 2 })

	res := e.dismissLost(a.code, map[string]any{"sids": []string{s1}})
	if res.Status != http.StatusOK {
		t.Fatalf("忽略失败: %d %s", res.Status, res.Raw)
	}
	if n, _ := res.Body["changed"].(float64); int(n) != 1 {
		t.Fatalf("changed 应为 1: %s", res.Raw)
	}
	if got := e.hostLost(a.code); len(got) != 1 || got[0] != s2 {
		t.Fatalf("只应剩 %s: %v", s2, got)
	}

	// 守护进程重连后会把记着的丢失会话再报一遍：被忽略的不能回来，新的丢失照常出现。
	s3 := lostSID('3')
	reportLost(a, s1, s2, s3)
	eventually(t, 3*time.Second, "新的丢失出现", func() bool { return len(e.hostLost(a.code)) == 2 })
	got := e.hostLost(a.code)
	if strings.Contains(strings.Join(got, ","), s1) {
		t.Fatalf("被忽略的 %s 不应重新出现: %v", s1, got)
	}
}

func TestDismissLostAllClearsOnlyThisHost(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("host-a"), "")
	b := e.enrollAgent(testMeta("host-b"), "")
	sa1, sa2, sb1 := lostSID('a'), lostSID('b'), lostSID('c')

	reportLost(a, sa1, sa2)
	reportLost(b, sb1)
	eventually(t, 3*time.Second, "两台主机的提示都可见", func() bool {
		return len(e.hostLost(a.code)) == 2 && len(e.hostLost(b.code)) == 1
	})

	res := e.dismissLost(a.code, map[string]any{"all": true})
	if res.Status != http.StatusOK {
		t.Fatalf("全部清除失败: %d %s", res.Status, res.Raw)
	}
	if n, _ := res.Body["changed"].(float64); int(n) != 2 {
		t.Fatalf("changed 应为 2: %s", res.Raw)
	}
	if got := e.hostLost(a.code); len(got) != 0 {
		t.Fatalf("host-a 应已清空: %v", got)
	}
	if got := e.hostLost(b.code); len(got) != 1 {
		t.Fatalf("host-b 不应受影响: %v", got)
	}

	// 幂等：再清一次不报错，也没有变化。
	if res = e.dismissLost(a.code, map[string]any{"all": true}); res.Status != http.StatusOK {
		t.Fatalf("重复清除应成功: %d %s", res.Status, res.Raw)
	} else if n, _ := res.Body["changed"].(float64); int(n) != 0 {
		t.Fatalf("重复清除 changed 应为 0: %s", res.Raw)
	}
}

func TestDismissLostRejectsBadInput(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("bad-input"), "")

	tooMany := make([]string, termproto.MaxLostSessions+1)
	for i := range tooMany {
		tooMany[i] = lostSID(byte('a' + i%6))
	}
	cases := map[string]map[string]any{
		"什么都没给":  {},
		"空列表":    {"sids": []string{}},
		"含引号":    {"sids": []string{"abc'; DROP TABLE x;--"}},
		"含斜杠":    {"sids": []string{"../../etc/passwd"}},
		"空字符串":   {"sids": []string{""}},
		"超过单次上限": {"sids": tooMany},
	}
	for name, body := range cases {
		if res := e.dismissLost(a.code, body); res.Status != http.StatusBadRequest {
			t.Errorf("%s: 应为 400，实际 %d %s", name, res.Status, res.Raw)
		}
	}
	if res := e.dismissLost("RM-ZZZZ-ZZZZ-ZZZZ-ZZZZ", map[string]any{"sids": []string{lostSID('f')}}); res.Status != http.StatusNotFound {
		t.Errorf("不存在的主机应为 404，实际 %d %s", res.Status, res.Raw)
	}
}

func TestDismissLostNeedsLoginButNotUnlock(t *testing.T) {
	e := newTestEnv(t)
	a := e.enrollAgent(testMeta("auth-host"), "")
	body := map[string]any{"sids": []string{lostSID('9')}}
	path := "/api/v1/terminal/hosts/" + a.code + "/lost/dismiss"
	if res := e.call("POST", path, body); res.Status != http.StatusUnauthorized {
		t.Fatalf("未登录应为 401，实际 %d %s", res.Status, res.Raw)
	}
	if res := e.call("POST", path, body, "Authorization", humanAuth(testUser)); res.Status != http.StatusOK {
		t.Fatalf("仅登录应可忽略，实际 %d %s", res.Status, res.Raw)
	}
}
