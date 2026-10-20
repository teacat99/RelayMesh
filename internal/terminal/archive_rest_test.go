package terminal

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// 侧栏归档：只需要登录；已归档的 key 随主机列表返回，重复与撤销都是幂等的。

func (e *testEnv) archived() []string {
	e.t.Helper()
	res := e.call("GET", "/api/v1/terminal/hosts", nil, "Authorization", humanAuth(testUser))
	if res.Status != http.StatusOK {
		e.t.Fatalf("主机列表: %d %s", res.Status, res.Raw)
	}
	raw, ok := res.Body["archived"].([]any)
	if !ok {
		e.t.Fatalf("响应里应有 archived 数组: %s", res.Raw)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

func (e *testEnv) setArchived(archived bool, keys ...string) apiResult {
	e.t.Helper()
	return e.call("POST", "/api/v1/terminal/archive", map[string]any{"keys": keys, "archived": archived}, "Authorization", humanAuth(testUser))
}

func TestArchiveRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	if got := e.archived(); len(got) != 0 {
		t.Fatalf("初始不应有归档: %v", got)
	}

	a, b := "RM-AAAA-BBBB-CCCC-DDDD:0123456789abcdef", "r:0123456789abcdef01234567"
	if res := e.setArchived(true, a, b, a); res.Status != http.StatusOK {
		t.Fatalf("归档失败: %d %s", res.Status, res.Raw)
	} else if n, _ := res.Body["changed"].(float64); int(n) != 2 {
		t.Fatalf("重复的 key 只应算一次，changed = %v", res.Body["changed"])
	}
	got := e.archived()
	if len(got) != 2 {
		t.Fatalf("应有 2 条归档: %v", got)
	}
	if !reflect.DeepEqual(map[string]bool{got[0]: true, got[1]: true}, map[string]bool{a: true, b: true}) {
		t.Fatalf("归档内容不对: %v", got)
	}

	// 幂等：再归档一次不算变化。
	if res := e.setArchived(true, a); res.Status != http.StatusOK {
		t.Fatalf("重复归档应成功: %d %s", res.Status, res.Raw)
	} else if n, _ := res.Body["changed"].(float64); int(n) != 0 {
		t.Fatalf("重复归档 changed 应为 0，实际 %v", res.Body["changed"])
	}

	// 撤销其中一条；撤销一条本来就没归档的也不报错。
	if res := e.setArchived(false, a, "RM-ZZZZ-ZZZZ-ZZZZ-ZZZZ:ffff"); res.Status != http.StatusOK {
		t.Fatalf("取消归档失败: %d %s", res.Status, res.Raw)
	} else if n, _ := res.Body["changed"].(float64); int(n) != 1 {
		t.Fatalf("取消归档 changed 应为 1，实际 %v", res.Body["changed"])
	}
	if got := e.archived(); len(got) != 1 || got[0] != b {
		t.Fatalf("取消后应只剩 %s: %v", b, got)
	}
}

func TestArchiveRejectsBadInput(t *testing.T) {
	e := newTestEnv(t)
	tooMany := make([]string, archiveMaxKeysPerCall+1)
	for i := range tooMany {
		tooMany[i] = "r:" + strings.Repeat("a", 10) + string(rune('a'+i%26)) + strings.Repeat("0", i%7)
	}
	cases := map[string][]string{
		"空列表":    {},
		"空字符串":   {""},
		"含空格":    {"r:abc def"},
		"含斜杠":    {"../etc/passwd"},
		"含引号":    {"r:abc'; DROP TABLE x;--"},
		"超长":     {strings.Repeat("a", archiveKeyMaxLen+1)},
		"超过单次上限": tooMany,
	}
	for name, keys := range cases {
		res := e.setArchived(true, keys...)
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s: 应为 400，实际 %d %s", name, res.Status, res.Raw)
		}
	}
	if got := e.archived(); len(got) != 0 {
		t.Fatalf("非法请求不应写入任何归档: %v", got)
	}
}

func TestArchiveNeedsLoginButNotUnlock(t *testing.T) {
	e := newTestEnv(t)
	body := map[string]any{"keys": []string{"r:abc"}, "archived": true}
	if res := e.call("POST", "/api/v1/terminal/archive", body); res.Status != http.StatusUnauthorized {
		t.Fatalf("未登录应为 401，实际 %d %s", res.Status, res.Raw)
	}
	// 只登录、没解锁：整理清单是低风险操作，应当可以。
	if res := e.call("POST", "/api/v1/terminal/archive", body, "Authorization", humanAuth(testUser)); res.Status != http.StatusOK {
		t.Fatalf("仅登录应可归档，实际 %d %s", res.Status, res.Raw)
	}
}
