package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/store"
)

const amWF = "wf-async-rest"

func amPath() string { return "/api/v1/workflows/" + amWF + "/async-mode" }

func amView(t *testing.T, res wiredResp) map[string]any {
	t.Helper()
	v, ok := res.Body["async_mode"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 async_mode: %d %s", res.Code, res.Raw)
	}
	return v
}

// 开关与档位只给登录的 Web 用户：匿名 401，MCP 令牌 403，URL 令牌不被接受。
func TestAsyncModeREST_OnlyForWebUsers(t *testing.T) {
	e := newCfEnv(t)
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", amPath(), nil},
		{"PUT", amPath(), map[string]any{"enabled": true}},
		{"POST", amPath() + "/release-gate", nil},
		{"GET", "/api/v1/auth-profiles", nil},
		{"POST", "/api/v1/auth-profiles", map[string]any{"name": "x", "level": "L1", "rules": "r"}},
		{"PUT", "/api/v1/auth-profiles/1", map[string]any{"rules": "r"}},
		{"POST", "/api/v1/auth-profiles/1/default", nil},
		{"DELETE", "/api/v1/auth-profiles/1", nil},
	}
	tokens := map[string]string{"环境变量 MCP 令牌": wiredMCPToken, "凭据库令牌": e.cred}
	for _, rt := range routes {
		if res := wiredCall(e.srv, rt.method, rt.path, rt.body); res.Code != http.StatusUnauthorized {
			t.Errorf("匿名 %s %s 应 401，实际 %d", rt.method, rt.path, res.Code)
		}
		for name, tok := range tokens {
			res := wiredCall(e.srv, rt.method, rt.path, rt.body, "Authorization", "Bearer "+tok)
			if res.Code != http.StatusForbidden || res.str("code") != "mcp_token_forbidden" {
				t.Errorf("%s 访问 %s %s 应 403 mcp_token_forbidden，实际 %d %s", name, rt.method, rt.path, res.Code, res.Raw)
			}
		}
	}
	// 被拒绝的请求什么也没改。
	v := amView(t, e.call("GET", amPath(), nil))
	if v["enabled"] != false || v["ever_enabled"] != false {
		t.Fatalf("被拒绝的请求不该改变状态: %v", v)
	}
}

func TestAsyncModeREST_EnableNeedsHookThenWorksAndDegrades(t *testing.T) {
	e := newCfEnv(t)

	// 初始：没开启过、没见过 hook。
	v := amView(t, e.call("GET", amPath(), nil))
	if v["enabled"] != false || v["effective"] != false || v["mode"] != "sync" || v["can_enable"] != false {
		t.Fatalf("初始状态不对: %v", v)
	}
	if _, has := v["reason"]; has {
		t.Fatalf("从没开启过的工作流没有降级原因: %v", v)
	}
	prof, _ := v["profile"].(map[string]any)
	if prof["name"] != "标准授权" || prof["is_default"] != true {
		t.Fatalf("应带默认档位: %v", prof)
	}

	// 没见过 hook：开启被拒绝（409），并且什么都没写进去。
	res := e.call("PUT", amPath(), map[string]any{"enabled": true})
	if res.Code != http.StatusConflict || !strings.Contains(res.str("error"), "hook") {
		t.Fatalf("没见过 hook 应 409 并说明: %d %s", res.Code, res.Raw)
	}
	if v := amView(t, e.call("GET", amPath(), nil)); v["enabled"] != false || v["ever_enabled"] != false {
		t.Fatalf("被拒绝的开启不该留下痕迹: %v", v)
	}

	// 有 hook 事件之后可以开启，立刻生效；收件箱状态接口带同样的 async_mode。
	e.srv.store.RecordHookSeen(amWF, time.Now())
	res = e.call("PUT", amPath(), map[string]any{"enabled": true})
	if res.Code != http.StatusOK {
		t.Fatalf("开启失败: %d %s", res.Code, res.Raw)
	}
	v = amView(t, res)
	if v["enabled"] != true || v["effective"] != true || v["mode"] != "async" || v["can_enable"] != true || v["enabled_at"] == nil {
		t.Fatalf("开启后的状态不对: %v", v)
	}
	if v["hook_fresh_minutes"] != float64(60) {
		t.Fatalf("应告知 hook 新鲜度窗口: %v", v)
	}
	inbox := e.call("GET", "/api/v1/workflows/"+amWF+"/inbox", nil)
	if inbox.Code != http.StatusOK {
		t.Fatalf("收件箱状态: %d %s", inbox.Code, inbox.Raw)
	}
	if iv := amView(t, inbox); iv["effective"] != true {
		t.Fatalf("收件箱状态里的 async_mode 应与开关同源: %v", iv)
	}

	// 关闭：永远允许；ever_enabled 保留，原因是 disabled。
	v = amView(t, e.call("PUT", amPath(), map[string]any{"enabled": false}))
	if v["enabled"] != false || v["effective"] != false || v["ever_enabled"] != true || v["reason"] != "disabled" {
		t.Fatalf("关闭后的状态不对: %v", v)
	}
	if !strings.Contains(fmt.Sprint(v["reason_text"]), "关闭") {
		t.Fatalf("应带人话原因: %v", v)
	}
}

func TestAsyncModeREST_PickProfile(t *testing.T) {
	e := newCfEnv(t)
	res := e.call("POST", "/api/v1/auth-profiles", map[string]any{"name": "谨慎", "level": "L0", "rules": "只读分析，写入先问"})
	if res.Code != http.StatusCreated {
		t.Fatalf("建档位: %d %s", res.Code, res.Raw)
	}
	item, _ := res.Body["item"].(map[string]any)
	id, _ := item["id"].(float64)

	// 没开启过的工作流也能先选档位，不改变「从未开启」。
	v := amView(t, e.call("PUT", amPath(), map[string]any{"profile_id": id}))
	if v["profile_id"] != id || v["ever_enabled"] != false {
		t.Fatalf("选档位不该改变开启状态: %v", v)
	}
	if prof, _ := v["profile"].(map[string]any); prof["name"] != "谨慎" {
		t.Fatalf("应解析出选中的档位: %v", v)
	}

	// 档位不存在 → 404；0 → 跟随默认。
	if res := e.call("PUT", amPath(), map[string]any{"profile_id": 99999}); res.Code != http.StatusNotFound {
		t.Fatalf("不存在的档位应 404: %d %s", res.Code, res.Raw)
	}
	v = amView(t, e.call("PUT", amPath(), map[string]any{"profile_id": 0}))
	if prof, _ := v["profile"].(map[string]any); prof["name"] != "标准授权" {
		t.Fatalf("profile_id=0 应回到默认档位: %v", v)
	}

	// 空请求 / 坏请求。
	if res := e.call("PUT", amPath(), map[string]any{}); res.Code != http.StatusBadRequest {
		t.Fatalf("空请求应 400: %d", res.Code)
	}
}

func TestAuthProfilesREST_CRUDAndGuards(t *testing.T) {
	e := newCfEnv(t)

	res := e.call("GET", "/api/v1/auth-profiles", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("列表: %d %s", res.Code, res.Raw)
	}
	items, _ := res.Body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应只有种子档位: %v", res.Body)
	}
	seed, _ := items[0].(map[string]any)
	seedID := seed["id"].(float64)
	if res.Body["default_id"] != seedID {
		t.Fatalf("default_id 应指向种子档位: %v", res.Body)
	}
	limits, _ := res.Body["limits"].(map[string]any)
	if limits["max"] != float64(12) || limits["name_max"] != float64(24) || limits["rules_max"] != float64(240) {
		t.Fatalf("应带校验口径: %v", limits)
	}

	// 校验：等级只有 L0 / L1（不可逆永远要问，没有 L2 档位）、名称唯一、规则不能为空。
	bad := []struct {
		name string
		body map[string]any
		code int
	}{
		{"L2 档位", map[string]any{"name": "放飞", "level": "L2", "rules": "什么都做"}, http.StatusBadRequest},
		{"空规则", map[string]any{"name": "空", "level": "L1", "rules": "  "}, http.StatusBadRequest},
		{"空名称", map[string]any{"name": " ", "level": "L1", "rules": "r"}, http.StatusBadRequest},
		{"重名", map[string]any{"name": "标准授权", "level": "L1", "rules": "r"}, http.StatusConflict},
		{"规则过长", map[string]any{"name": "长", "level": "L1", "rules": strings.Repeat("字", 241)}, http.StatusBadRequest},
	}
	for _, tc := range bad {
		if res := e.call("POST", "/api/v1/auth-profiles", tc.body); res.Code != tc.code {
			t.Errorf("%s: 应 %d，实际 %d %s", tc.name, tc.code, res.Code, res.Raw)
		}
	}

	// 新建 / 改 / 设默认 / 删。
	res = e.call("POST", "/api/v1/auth-profiles", map[string]any{"name": "稳妥", "level": "l0", "rules": "只读，写入先问"})
	if res.Code != http.StatusCreated {
		t.Fatalf("建档位: %d %s", res.Code, res.Raw)
	}
	created, _ := res.Body["item"].(map[string]any)
	id := created["id"].(float64)
	if created["level"] != "L0" || created["is_default"] != false {
		t.Fatalf("等级应规整成大写、新档位不是默认: %v", created)
	}
	path := fmt.Sprintf("/api/v1/auth-profiles/%d", int(id))
	if res := e.call("PUT", path, map[string]any{"rules": "只读分析"}); res.Code != http.StatusOK {
		t.Fatalf("改档位: %d %s", res.Code, res.Raw)
	} else if item, _ := res.Body["item"].(map[string]any); item["rules"] != "只读分析" || item["name"] != "稳妥" {
		t.Fatalf("只改给出的字段: %v", item)
	}
	if res := e.call("POST", path+"/default", nil); res.Code != http.StatusOK {
		t.Fatalf("设默认: %d %s", res.Code, res.Raw)
	}
	list := e.call("GET", "/api/v1/auth-profiles", nil)
	if list.Body["default_id"] != id {
		t.Fatalf("默认应换成新档位: %v", list.Body)
	}
	// 默认档位不能删。
	if res := e.call("DELETE", path, nil); res.Code != http.StatusConflict {
		t.Fatalf("默认档位不能删: %d %s", res.Code, res.Raw)
	}
	// 非默认可以删；删后工作流选用它的回落到默认。
	seedPath := fmt.Sprintf("/api/v1/auth-profiles/%d", int(seedID))
	e.call("PUT", amPath(), map[string]any{"profile_id": seedID})
	if res := e.call("DELETE", seedPath, nil); res.Code != http.StatusOK {
		t.Fatalf("删非默认档位: %d %s", res.Code, res.Raw)
	}
	v := amView(t, e.call("GET", amPath(), nil))
	if prof, _ := v["profile"].(map[string]any); prof["name"] != "稳妥" {
		t.Fatalf("选用的档位被删后应回落默认: %v", v)
	}
	// 坏编号。
	if res := e.call("DELETE", "/api/v1/auth-profiles/abc", nil); res.Code != http.StatusBadRequest {
		t.Fatalf("坏编号应 400: %d", res.Code)
	}
	if res := e.call("DELETE", "/api/v1/auth-profiles/424242", nil); res.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d %s", res.Code, res.Raw)
	}
}

func TestAuthProfilesREST_CountCap(t *testing.T) {
	e := newCfEnv(t)
	// 种子档位占 1 档，再建 11 档到上限 12，第 13 档被拒绝。
	for i := 0; i < 11; i++ {
		res := e.call("POST", "/api/v1/auth-profiles", map[string]any{"name": fmt.Sprintf("档%d", i), "level": "L1", "rules": "r"})
		if res.Code != http.StatusCreated {
			t.Fatalf("第 %d 档应能建: %d %s", i+2, res.Code, res.Raw)
		}
	}
	if res := e.call("POST", "/api/v1/auth-profiles", map[string]any{"name": "多余", "level": "L1", "rules": "r"}); res.Code != http.StatusConflict {
		t.Fatalf("超过上限应 409: %d %s", res.Code, res.Raw)
	}
}

// 严格收尾门（#232 D2）的 REST：视图带门的状态与挡着的任务；开关可改；「本次放行」让窗口立刻接着走；
// 没有窗口 / 门已关时放行返回 409 并说明；放行不动任务。
func TestAsyncModeREST_StrictWrapupGate(t *testing.T) {
	e := newCfEnv(t)
	ctx := context.Background()
	agent := store.WorkActor{Kind: "agent", WorkflowID: amWF}
	for i, title := range []string{"待办甲", "进行中乙"} {
		in := store.WorkItemInput{WorkflowID: amWF, Title: title}
		if i == 1 {
			in.State = "doing"
		}
		if _, err := e.srv.store.CreateWorkItem(ctx, in, agent); err != nil {
			t.Fatal(err)
		}
	}
	e.srv.store.RecordHookSeen(amWF, time.Now())
	if res := e.call("PUT", amPath(), map[string]any{"enabled": true}); res.Code != http.StatusOK {
		t.Fatalf("开启失败: %d %s", res.Code, res.Raw)
	}
	v := amView(t, e.call("GET", amPath(), nil))
	if v["strict_wrapup"] != true || v["gate_total"] != float64(2) || v["gate_released"] != nil {
		t.Fatalf("默认应开着收尾门、挡着 2 个任务: %v", v)
	}
	gate, _ := v["gate_tasks"].([]any)
	if len(gate) != 2 {
		t.Fatalf("应列出 2 个挡着的任务: %v", v["gate_tasks"])
	}
	first, _ := gate[0].(map[string]any)
	if first["seq"] != float64(1) || first["state"] != "todo" || first["title"] != "待办甲" {
		t.Fatalf("按编号升序、带状态与标题: %v", first)
	}

	// 没有窗口在等：放行没有意义，409 并说明，什么都不留
	res := e.call("POST", amPath()+"/release-gate", nil)
	if res.Code != http.StatusConflict || !strings.Contains(res.str("error"), "没有回复窗口") {
		t.Fatalf("没有窗口时放行应 409: %d %s", res.Code, res.Raw)
	}

	// 智能体发了消息，窗口攒着不走；用户放行后立刻接着走
	if _, err := e.srv.store.ExtendReplyWindow(ctx, amWF, 5*time.Minute, time.Now()); err != nil {
		t.Fatal(err)
	}
	v = amView(t, e.call("GET", amPath(), nil))
	if v["reply_window_paused"] != true || v["active_tasks"] != float64(2) || v["reply_window_bank_sec"] != float64(300) {
		t.Fatalf("放行前窗口应暂停: %v", v)
	}
	res = e.call("POST", amPath()+"/release-gate", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("放行失败: %d %s", res.Code, res.Raw)
	}
	v = amView(t, res)
	if v["gate_released"] != true || v["reply_window_until"] == nil || v["reply_window_paused"] != nil || v["active_tasks"] != nil || v["gate_total"] != float64(2) {
		t.Fatalf("放行后窗口应在走、仍如实列出 2 个任务: %v", v)
	}
	open := 0
	for _, state := range []string{"todo", "doing"} {
		items, _ := e.srv.store.ListWorkItems(ctx, store.WorkItemFilter{WorkflowID: amWF, States: []string{state}, Limit: 10})
		open += len(items)
	}
	if open != 2 {
		t.Fatalf("放行不该动任务: %d", open)
	}

	// 关掉收尾门：视图里 strict_wrapup=false、没有 gate_*；此时放行 409
	res = e.call("PUT", amPath(), map[string]any{"strict_wrapup": false})
	if res.Code != http.StatusOK {
		t.Fatalf("关门失败: %d %s", res.Code, res.Raw)
	}
	v = amView(t, res)
	if v["strict_wrapup"] != false || v["gate_total"] != nil || v["gate_tasks"] != nil || v["gate_released"] != nil {
		t.Fatalf("门关着时不该带 gate 信息: %v", v)
	}
	if res := e.call("POST", amPath()+"/release-gate", nil); res.Code != http.StatusConflict || !strings.Contains(res.str("error"), "已经关闭") {
		t.Fatalf("门关着时放行应 409: %d %s", res.Code, res.Raw)
	}
	// 再打开：回到严格口径
	if v := amView(t, e.call("PUT", amPath(), map[string]any{"strict_wrapup": true})); v["strict_wrapup"] != true || v["gate_total"] != float64(2) {
		t.Fatalf("重新开门后应恢复: %v", v)
	}
	// 空请求：提示三个可改的字段
	if res := e.call("PUT", amPath(), map[string]any{}); res.Code != http.StatusBadRequest || !strings.Contains(res.str("error"), "strict_wrapup") {
		t.Fatalf("空请求应 400 并提示可改字段: %d %s", res.Code, res.Raw)
	}
	// 没有开过异步的工作流：放行 404，不会凭空建出模式行
	if res := e.call("POST", "/api/v1/workflows/wf-never-async/async-mode/release-gate", nil); res.Code != http.StatusNotFound {
		t.Fatalf("没开过异步应 404: %d %s", res.Code, res.Raw)
	}
}
