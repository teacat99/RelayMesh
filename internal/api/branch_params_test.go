package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 会话参数（#236 P3）的接口层：放宽授权要终端解锁（总闸关着时压回）、主会话改过的参数记录与撤销。
// 取值与规则的细节在 internal/model、internal/store 的测试里，这里只验接口：状态码、请求 / 响应形状、广播、谁能做。

// paramsWiredFixture 是真实装配的服务（设了密码，终端总闸开着），带主会话 wf-main 与一个无关会话 wf-solo。
// 返回服务、只带登录的请求头与「登录 + 终端已解锁」的请求头。
func paramsWiredFixture(t *testing.T) (srv *Server, login, unlocked []string) {
	t.Helper()
	srv = newWiredServer(t, nil)
	jwt := wiredLogin(t, srv)
	login = []string{"Authorization", "Bearer " + jwt}
	unlocked = append(append([]string{}, login...), "X-Terminal-Unlock", wiredUnlock(t, srv, jwt))
	for _, wf := range []string{branchMain, "wf-solo"} {
		if _, err := srv.store.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
			SessionID: "s-" + wf, WorkflowID: wf, EnvHostName: "h", ProjectDirectory: "/code/a", Title: "标题-" + wf, Summary: "s",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return srv, login, unlocked
}

func createBranchWired(srv *Server, hdr []string, title string, preset string) wiredResp {
	body := map[string]any{"title": title, "parent_workflow_id": branchMain}
	if preset != "" {
		body["expose_preset"] = preset
	}
	return wiredCall(srv, http.MethodPost, "/api/v1/workflows", body, hdr...)
}

func branchExposeOf(t *testing.T, res wiredResp) map[string]any {
	t.Helper()
	br, _ := res.Body["branch"].(map[string]any)
	ex, _ := br["expose"].(map[string]any)
	if ex == nil {
		t.Fatalf("响应里没有 branch.expose: %s", res.Raw)
	}
	return ex
}

func relationExpose(t *testing.T, st *store.Store, branch string) model.BranchExpose {
	t.Helper()
	rel, err := st.BranchOf(context.Background(), branch)
	if err != nil || rel == nil {
		t.Fatalf("读不到 %s 的关系: %+v %v", branch, rel, err)
	}
	return rel.Expose
}

func clampedOf(res wiredResp) []string {
	raw, ok := res.Body["clamped"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

func putBranch(srv *Server, hdr []string, branch string, body map[string]any) wiredResp {
	return wiredCall(srv, http.MethodPut, "/api/v1/workflows/"+branch+"/branch", body, hdr...)
}

func wantClamped(t *testing.T, label string, res wiredResp, want ...string) {
	t.Helper()
	got := clampedOf(res)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s：clamped = %v，期望 %v（%s）", label, got, want, res.Raw)
	}
	if _, has := res.Body["clamped"]; has != (len(want) > 0) {
		t.Fatalf("%s：clamped 字段只在有压回时出现（%s）", label, res.Raw)
	}
}

// 终端总闸开着：放宽「改参数」档位 / 打开「启动」要终端解锁，收紧与其它项只需登录；被拒绝时什么都不变。
func TestBranchWiden_GateOpenRequiresUnlock(t *testing.T) {
	srv, login, unlocked := paramsWiredFixture(t)
	ctx := context.Background()

	// 新建：协作（缺省）与托管相对「什么都没开放」是放宽。没解锁：403 unlock_required，什么都没建。
	for _, preset := range []string{"", "collab", "managed"} {
		res := createBranchWired(srv, login, "拒绝-"+preset, preset)
		if res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
			t.Fatalf("没解锁建 %q 预设的分支应 403 unlock_required: %d %s", preset, res.Code, res.Raw)
		}
	}
	if rows, err := srv.store.ListAllBranches(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("被拒绝的新建不该留下任何关系: %+v %v", rows, err)
	}

	// 仅可见不含任何放宽：只要登录。
	vis := createBranchWired(srv, login, "仅可见分支", "visible")
	if vis.Code != http.StatusCreated {
		t.Fatalf("仅可见只需登录: %d %s", vis.Code, vis.Raw)
	}
	if ex := branchExposeOf(t, vis); ex["params"] != "off" || ex["launch"] != false {
		t.Fatalf("仅可见不开放改参数与启动: %v", ex)
	}
	if _, has := vis.Body["clamped"]; has {
		t.Fatalf("没有压回时不该有 clamped: %s", vis.Raw)
	}
	bVis := vis.str("workflow_id")

	// 解锁后：协作 → 改常驻规则；托管 → 改全部参数 + 启动。
	col := createBranchWired(srv, unlocked, "协作分支", "")
	if col.Code != http.StatusCreated {
		t.Fatalf("解锁后建协作分支应 201: %d %s", col.Code, col.Raw)
	}
	if ex := branchExposeOf(t, col); ex["params"] != "resident" || ex["launch"] != false {
		t.Fatalf("协作预设应开放常驻规则: %v", ex)
	}
	bCol := col.str("workflow_id")
	man := createBranchWired(srv, unlocked, "托管分支", "managed")
	if man.Code != http.StatusCreated {
		t.Fatalf("解锁后建托管分支应 201: %d %s", man.Code, man.Raw)
	}
	if ex := branchExposeOf(t, man); ex["params"] != "all" || ex["launch"] != true {
		t.Fatalf("托管预设应开放全部参数与启动: %v", ex)
	}
	bMan := man.str("workflow_id")
	if _, has := man.Body["clamped"]; has {
		t.Fatalf("解锁后不该有 clamped: %s", man.Raw)
	}

	t.Run("改设置：调高档位要解锁，拒绝时关系不变", func(t *testing.T) {
		res := putBranch(srv, login, bVis, map[string]any{"expose_preset": "collab"})
		if res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
			t.Fatalf("仅可见 → 协作没解锁应 403: %d %s", res.Code, res.Raw)
		}
		if got := relationExpose(t, srv.store, bVis); got.PresetName() != "visible" {
			t.Fatalf("被拒绝的修改不该落库: %+v", got)
		}
		res = putBranch(srv, unlocked, bVis, map[string]any{"expose_preset": "collab"})
		if res.Code != http.StatusOK || relationExpose(t, srv.store, bVis).Params != "resident" {
			t.Fatalf("解锁后应改成协作: %d %s", res.Code, res.Raw)
		}
		if _, has := res.Body["clamped"]; has {
			t.Fatalf("解锁后不该有 clamped: %s", res.Raw)
		}
	})

	t.Run("改设置：收紧只要登录，再放宽又要解锁", func(t *testing.T) {
		res := putBranch(srv, login, bMan, map[string]any{"expose_preset": "collab"})
		if res.Code != http.StatusOK {
			t.Fatalf("托管 → 协作是收紧，只需登录: %d %s", res.Code, res.Raw)
		}
		if got := relationExpose(t, srv.store, bMan); got.Params != "resident" || got.Launch {
			t.Fatalf("应收紧成改常驻规则、不能启动: %+v", got)
		}
		if res := putBranch(srv, login, bMan, map[string]any{"expose_preset": "managed"}); res.Code != http.StatusForbidden {
			t.Fatalf("再改回托管要解锁: %d %s", res.Code, res.Raw)
		}
		if res := putBranch(srv, login, bMan, map[string]any{"expose_preset": "visible"}); res.Code != http.StatusOK || relationExpose(t, srv.store, bMan).Params != "off" {
			t.Fatalf("改成仅可见只需登录: %d %s", res.Code, res.Raw)
		}
	})

	t.Run("改设置：显式取值同样按放宽判断", func(t *testing.T) {
		base := model.BranchExpose{History: "full", Decide: true, Say: true, Params: "resident", Tasks: "assign"}
		raise := func(mut func(*model.BranchExpose)) map[string]any {
			e := base
			mut(&e)
			return map[string]any{"expose": e}
		}
		if res := putBranch(srv, login, bCol, raise(func(e *model.BranchExpose) { e.Params = "resident_phase" })); res.Code != http.StatusForbidden || res.str("code") != "unlock_required" {
			t.Fatalf("档位上调要解锁: %d %s", res.Code, res.Raw)
		}
		if res := putBranch(srv, login, bCol, raise(func(e *model.BranchExpose) { e.Launch = true })); res.Code != http.StatusForbidden {
			t.Fatalf("只打开启动也要解锁: %d %s", res.Code, res.Raw)
		}
		if got := relationExpose(t, srv.store, bCol); got != base {
			t.Fatalf("被拒绝后关系不该变: %+v", got)
		}
		if res := putBranch(srv, unlocked, bCol, raise(func(e *model.BranchExpose) { e.Launch = true })); res.Code != http.StatusOK || !relationExpose(t, srv.store, bCol).Launch {
			t.Fatalf("解锁后应能打开启动: %d %s", res.Code, res.Raw)
		}
		if res := putBranch(srv, login, bCol, raise(func(e *model.BranchExpose) { e.Launch = false })); res.Code != http.StatusOK || relationExpose(t, srv.store, bCol).Launch {
			t.Fatalf("关掉启动是收紧，只需登录: %d %s", res.Code, res.Raw)
		}
	})

	t.Run("与授权无关的放宽与抄送开关不要解锁", func(t *testing.T) {
		// bMan 此刻是仅可见：读历史加深、可发消息、可定夺、任务可指派都不涉及改参数与启动。
		open := model.BranchExpose{History: "full", Decide: true, Say: true, Params: "off", Tasks: "assign"}
		if res := putBranch(srv, login, bMan, map[string]any{"expose": open}); res.Code != http.StatusOK {
			t.Fatalf("不涉及改参数 / 启动的放宽只需登录: %d %s", res.Code, res.Raw)
		}
		if got := relationExpose(t, srv.store, bMan); got != open {
			t.Fatalf("应按请求落库: %+v", got)
		}
		if res := putBranch(srv, login, bMan, map[string]any{"copy_user_notes": false}); res.Code != http.StatusOK {
			t.Fatalf("改抄送开关只需登录: %d %s", res.Code, res.Raw)
		}
	})

	t.Run("错误请求照旧由 store 报错，不先要求解锁", func(t *testing.T) {
		cases := []struct {
			name string
			wf   string
			body map[string]any
			want int
		}{
			{"预设不认识", bVis, map[string]any{"expose_preset": "bogus"}, http.StatusBadRequest},
			{"预设与取值都带", bVis, map[string]any{"expose_preset": "collab", "expose": model.BranchExpose{History: "full", Params: "all", Tasks: "assign"}}, http.StatusBadRequest},
			{"取值不合法", bVis, map[string]any{"expose": model.BranchExpose{History: "full", Params: "everything", Tasks: "assign"}}, http.StatusBadRequest},
			{"什么都没带", bVis, map[string]any{}, http.StatusBadRequest},
			{"不是分支", "wf-solo", map[string]any{"expose_preset": "managed"}, http.StatusNotFound},
		}
		for _, c := range cases {
			if res := putBranch(srv, login, c.wf, c.body); res.Code != c.want {
				t.Errorf("%s：状态码 %d，期望 %d（%s）", c.name, res.Code, c.want, res.Raw)
			}
		}
		if res := putBranch(srv, nil, bVis, map[string]any{"expose_preset": "visible"}); res.Code != http.StatusUnauthorized {
			t.Errorf("没登录应 401: %d", res.Code)
		}
	})
}

// 终端总闸关着（这个测试服务没设密码）：没人能解锁，不拒绝整个请求，放宽的两项压回当前取值、其余照办，响应说明压回了什么。
func TestBranchWiden_GateClosedClampsAndReports(t *testing.T) {
	srv, st := branchAPIFixture(t)
	ctx := context.Background()
	create := func(title string, preset string) (string, wiredResp) {
		body := map[string]any{"title": title, "parent_workflow_id": branchMain}
		if preset != "" {
			body["expose_preset"] = preset
		}
		code, out, raw := createWorkflowCall(t, srv, body)
		if code != http.StatusCreated {
			t.Fatalf("总闸关着建分支应 201（压回而不是拒绝）: %d %s", code, raw)
		}
		res := wiredResp{Code: code, Body: out, Raw: raw}
		return res.str("workflow_id"), res
	}
	put := func(branch string, body map[string]any) wiredResp {
		code, out, raw := ntfyCall(t, srv, http.MethodPut, "/api/v1/workflows/"+branch+"/branch", body)
		return wiredResp{Code: code, Body: out, Raw: raw}
	}
	collabNoParams := model.BranchExpose{History: "full", Decide: true, Say: true, Params: "off", Tasks: "assign"}

	t.Run("新建协作：改参数压回 off，其余照协作", func(t *testing.T) {
		bwf, res := create("协作被压回", "")
		wantClamped(t, "新建协作", res, "params")
		if got := relationExpose(t, st, bwf); got != collabNoParams {
			t.Fatalf("应是「协作但不能改参数」: %+v", got)
		}
		if ex := branchExposeOf(t, res); ex["params"] != "off" || ex["history"] != "full" || ex["say"] != true || ex["decide"] != true {
			t.Fatalf("响应里的开放范围要与落库一致: %v", ex)
		}
	})
	t.Run("新建托管：改参数与启动都压回", func(t *testing.T) {
		bwf, res := create("托管被压回", "managed")
		wantClamped(t, "新建托管", res, "params", "launch")
		want := collabNoParams
		if got := relationExpose(t, st, bwf); got != want {
			t.Fatalf("托管压回后应与协作压回一致: %+v", got)
		}
	})
	t.Run("新建仅可见：没有放宽，不报压回", func(t *testing.T) {
		bwf, res := create("仅可见", "visible")
		wantClamped(t, "新建仅可见", res)
		if relationExpose(t, st, bwf).PresetName() != "visible" {
			t.Fatalf("仅可见应原样落库")
		}
	})

	// 后面的子测试各用一个分支，直接用 store 铺好起点（绕过接口，才能造出「总闸开着时设好的档位」）。
	setExpose := func(branch string, e model.BranchExpose) {
		t.Helper()
		if _, err := st.UpdateBranchSettings(ctx, branch, store.UpdateBranchInput{Expose: &e}); err != nil {
			t.Fatal(err)
		}
	}
	vis, _ := create("改设置-仅可见", "visible")

	t.Run("改设置：协作预设压回改参数", func(t *testing.T) {
		res := put(vis, map[string]any{"expose_preset": "collab"})
		if res.Code != http.StatusOK {
			t.Fatalf("应 200: %d %s", res.Code, res.Raw)
		}
		wantClamped(t, "仅可见 → 协作", res, "params")
		if got := relationExpose(t, st, vis); got != collabNoParams {
			t.Fatalf("其余项应照协作: %+v", got)
		}
	})
	t.Run("改设置：压回的是「相对当前」放宽的那部分", func(t *testing.T) {
		// 当前已经是 resident：协作预设不放宽（不报压回），托管预设只放宽「启动」与「全部参数」。
		setExpose(vis, model.BranchExpose{History: "full", Decide: true, Say: true, Params: "resident", Tasks: "assign"})
		res := put(vis, map[string]any{"expose_preset": "collab"})
		wantClamped(t, "resident → 协作（没放宽）", res)
		res = put(vis, map[string]any{"expose_preset": "managed"})
		wantClamped(t, "resident → 托管", res, "params", "launch")
		if got := relationExpose(t, st, vis); got.Params != "resident" || got.Launch {
			t.Fatalf("应保持 resident 且不能启动: %+v", got)
		}
		// 当前已经是 all：托管只放宽「启动」，改参数档位保持 all。
		setExpose(vis, model.BranchExpose{History: "full", Decide: true, Say: true, Params: "all", Tasks: "assign"})
		res = put(vis, map[string]any{"expose_preset": "managed"})
		wantClamped(t, "all → 托管", res, "launch")
		if got := relationExpose(t, st, vis); got.Params != "all" || got.Launch {
			t.Fatalf("应保持 all 且不能启动: %+v", got)
		}
	})
	t.Run("改设置：收紧照办，不报压回", func(t *testing.T) {
		res := put(vis, map[string]any{"expose_preset": "visible"})
		if res.Code != http.StatusOK {
			t.Fatalf("应 200: %d %s", res.Code, res.Raw)
		}
		wantClamped(t, "收紧", res)
		if relationExpose(t, st, vis).PresetName() != "visible" {
			t.Fatalf("应收紧成仅可见")
		}
	})
	t.Run("错误请求不被压回掩盖", func(t *testing.T) {
		both := map[string]any{"expose_preset": "collab", "expose": collabNoParams}
		if res := put(vis, both); res.Code != http.StatusBadRequest {
			t.Fatalf("预设与取值都带应 400: %d %s", res.Code, res.Raw)
		}
		if res := put("wf-solo", map[string]any{"expose_preset": "managed"}); res.Code != http.StatusNotFound {
			t.Fatalf("不是分支应 404: %d %s", res.Code, res.Raw)
		}
	})
}

// 主会话经 session_params 改过的参数：列表（不含全文、带「现在能不能撤销」）、撤销（只要登录）、广播与各种拒绝。
func TestBranchParamEvents_ListAndRevert(t *testing.T) {
	srv, login, unlocked := paramsWiredFixture(t)
	ctx := context.Background()
	st := srv.store

	created := createBranchWired(srv, unlocked, "被主会话改参数的分支", "")
	if created.Code != http.StatusCreated {
		t.Fatalf("建分支: %d %s", created.Code, created.Raw)
	}
	bwf := created.str("workflow_id")
	// 把分支的「改参数」开到最高，主会话才能改标题与阶段（起点直接用 store 铺，绕过解锁）。
	all := model.BranchExpose{History: "full", Decide: true, Say: true, Params: "all", Tasks: "assign"}
	if _, err := st.UpdateBranchSettings(ctx, bwf, store.UpdateBranchInput{Expose: &all}); err != nil {
		t.Fatal(err)
	}
	apply := func(patch store.SessionParamsPatch) model.BranchParamEvent {
		t.Helper()
		res, err := st.ApplySessionParams(ctx, branchMain, bwf, patch, time.Now())
		if err != nil || len(res.Applied) != 1 {
			t.Fatalf("主会话写参数: %+v %v", res, err)
		}
		return res.Applied[0]
	}
	strp := func(s string) *string { return &s }
	listURL := "/api/v1/workflows/" + bwf + "/branch/params-events"
	itoa := func(id uint) string { return strconv.FormatUint(uint64(id), 10) }
	revertURL := func(id uint) string {
		return listURL + "/" + itoa(id) + "/revert"
	}

	first := apply(store.SessionParamsPatch{Resident: strp("规则：先写测试再改代码")})
	second := apply(store.SessionParamsPatch{Resident: strp("规则：改完必须跑回归")})
	titleEv := apply(store.SessionParamsPatch{Title: strp("主会话改的标题")})

	t.Run("列表：新的在前，不含全文，每条说明现在能不能撤销", func(t *testing.T) {
		if res := wiredCall(srv, http.MethodGet, listURL, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("没登录应 401: %d", res.Code)
		}
		res := wiredCall(srv, http.MethodGet, listURL, nil, login...)
		if res.Code != http.StatusOK {
			t.Fatalf("应 200: %d %s", res.Code, res.Raw)
		}
		rows, _ := res.Body["events"].([]any)
		if len(rows) != 3 {
			t.Fatalf("应有三条记录: %s", res.Raw)
		}
		byID := map[float64]map[string]any{}
		for i, r := range rows {
			row, _ := r.(map[string]any)
			byID[row["id"].(float64)] = row
			if i == 0 && row["id"].(float64) != float64(titleEv.ID) {
				t.Fatalf("新的在前: %s", res.Raw)
			}
		}
		if row := byID[float64(second.ID)]; row["revertable"] != true || row["field"] != "resident" || row["field_label"] != "常驻规则" || row["by"] != "agent:"+branchMain || row["told"] != false {
			t.Fatalf("最新一条常驻规则应可撤销: %v", row)
		}
		if row := byID[float64(first.ID)]; row["revertable"] != false || !strings.Contains(row["revert_blocked"].(string), "不是最新") {
			t.Fatalf("同字段更旧的一条不可撤销并写明原因: %v", row)
		}
		for _, leak := range []string{"old_value", "new_value", "规则：先写测试再改代码", "规则：改完必须跑回归"} {
			if strings.Contains(res.Raw, leak) {
				t.Fatalf("列表不该带旧值 / 新值全文（%q）: %s", leak, res.Raw)
			}
		}
		if one := wiredCall(srv, http.MethodGet, listURL+"?limit=1", nil, login...); len(one.Body["events"].([]any)) != 1 {
			t.Fatalf("limit 应生效: %s", one.Raw)
		}
		if res := wiredCall(srv, http.MethodGet, "/api/v1/workflows/wf-solo/branch/params-events", nil, login...); res.Code != http.StatusNotFound {
			t.Fatalf("不是分支应 404: %d %s", res.Code, res.Raw)
		}
	})

	t.Run("会话页消息流里有对应的事件行", func(t *testing.T) {
		res := wiredCall(srv, http.MethodGet, "/api/v1/workflows/"+bwf+"/branch-feed", nil, login...)
		feed, _ := res.Body["branch_feed"].(map[string]any)
		items, _ := feed["items"].([]any)
		seen, revertable := 0, 0
		for _, it := range items {
			row, _ := it.(map[string]any)
			if row["kind"] == "event" && row["event"] == "params_changed" {
				seen++
				if row["field"] == nil || row["by"] != "agent:"+branchMain {
					t.Fatalf("参数事件行要带 field 与写入者: %v", row)
				}
				if row["revertable"] == true { // 不可撤销时字段省略（界面按 false 处理）
					revertable++
				}
			}
		}
		if seen != 3 || revertable != 2 {
			t.Fatalf("消息流里应有 3 条 params_changed 事件行（其中 2 条可撤销：最新的常驻规则与标题），得到 %d / %d: %s", seen, revertable, res.Raw)
		}
	})

	t.Run("撤销：只要登录，成功后广播并改回旧值", func(t *testing.T) {
		if res := wiredCall(srv, http.MethodPost, revertURL(second.ID), nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("没登录应 401: %d", res.Code)
		}
		sse := listenSSE(t, srv)
		res := wiredCall(srv, http.MethodPost, revertURL(second.ID), nil, login...) // 只带登录，没解锁
		if res.Code != http.StatusOK {
			t.Fatalf("撤销是收紧类操作，只要登录: %d %s", res.Code, res.Raw)
		}
		rev, _ := res.Body["reverted"].(map[string]any)
		ev, _ := res.Body["event"].(map[string]any)
		if rev["id"].(float64) != float64(second.ID) || rev["reverted_at"] == nil || rev["reverted_by"] == "" || rev["reverted_by"] == nil {
			t.Fatalf("reverted 应是被撤销的原事件并带撤销标记: %v", rev)
		}
		if ev["by"] != model.BranchParamByHuman || ev["revert_of"].(float64) != float64(second.ID) || ev["field"] != "resident" {
			t.Fatalf("event 应是用户的撤销记录: %v", ev)
		}
		joined := strings.Join(drainSSE(sse), "\n")
		for _, want := range []string{`"change":"params_reverted"`, "workflow_prompts_updated"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("应广播 %s:\n%s", want, joined)
			}
		}
		// 常驻规则改回了上一版的内容（以新版本存，历史不改写）。
		if got, err := st.GetWorkflowPrompt(ctx, bwf); err != nil || got.Resident != "规则：先写测试再改代码" {
			t.Fatalf("常驻规则应改回旧值: %+v %v", got, err)
		}
		// 撤销之后列表的状态跟着变：这条已撤销、不能再撤；撤掉最新的之后，更旧的一条成了最新的未撤销写入，当前值也正好是它写入的值，又能撤销了。
		list := wiredCall(srv, http.MethodGet, listURL, nil, login...)
		for _, r := range list.Body["events"].([]any) {
			row := r.(map[string]any)
			switch row["id"].(float64) {
			case float64(second.ID):
				if row["revertable"] != false || row["reverted_at"] == nil {
					t.Fatalf("已撤销的事件不可再撤销并带 reverted_at: %v", row)
				}
			case float64(first.ID):
				if row["revertable"] != true || row["revert_blocked"] != nil {
					t.Fatalf("最新的撤掉之后，更旧的一条应变成可撤销: %v", row)
				}
			}
		}
	})

	t.Run("撤销：各种拒绝", func(t *testing.T) {
		cases := []struct {
			name string
			url  string
			want int
			text string
		}{
			{"已经撤销过", revertURL(second.ID), http.StatusConflict, "已经撤销过"},
			{"撤销记录本身", revertURL(second.ID + 100), http.StatusNotFound, ""}, // 占位，下面按真实编号覆盖
			{"编号不是数字", listURL + "/abc/revert", http.StatusBadRequest, "event_id"},
			{"编号是 0", listURL + "/0/revert", http.StatusBadRequest, "event_id"},
			{"不存在的事件", revertURL(99999), http.StatusNotFound, ""},
			{"别的工作流", "/api/v1/workflows/wf-solo/branch/params-events/" + itoa(titleEv.ID) + "/revert", http.StatusNotFound, ""},
		}
		// 找到撤销记录（revert_of 指向 second 的那一条）。
		rows, _ := st.ListBranchParamEvents(ctx, bwf, 50)
		for _, r := range rows {
			if r.RevertOf == second.ID {
				cases[1].url = revertURL(r.ID)
				cases[1].want = http.StatusBadRequest
				cases[1].text = "撤销记录"
			}
		}
		for _, c := range cases {
			res := wiredCall(srv, http.MethodPost, c.url, nil, login...)
			if res.Code != c.want || (c.text != "" && !strings.Contains(res.Raw, c.text)) {
				t.Errorf("%s：%d %s，期望 %d 且含 %q", c.name, res.Code, res.Raw, c.want, c.text)
			}
		}
	})

	t.Run("接着撤销更旧的一条：常驻规则回到最初没有的状态", func(t *testing.T) {
		res := wiredCall(srv, http.MethodPost, revertURL(first.ID), nil, login...)
		if res.Code != http.StatusOK {
			t.Fatalf("更旧的一条在最新的撤掉之后应能撤销: %d %s", res.Code, res.Raw)
		}
		if got, err := st.GetWorkflowPrompt(ctx, bwf); err != nil || got.Resident != "" {
			t.Fatalf("常驻规则应回到没有: %+v %v", got, err)
		}
		if res := wiredCall(srv, http.MethodPost, revertURL(first.ID), nil, login...); res.Code != http.StatusConflict {
			t.Fatalf("再撤一次应 409: %d %s", res.Code, res.Raw)
		}
	})

	t.Run("撤销标题：广播 session_updated，标题改回", func(t *testing.T) {
		sse := listenSSE(t, srv)
		res := wiredCall(srv, http.MethodPost, revertURL(titleEv.ID), nil, login...)
		if res.Code != http.StatusOK {
			t.Fatalf("撤销标题: %d %s", res.Code, res.Raw)
		}
		if joined := strings.Join(drainSSE(sse), "\n"); !strings.Contains(joined, "session_updated") || !strings.Contains(joined, `"change":"params_reverted"`) {
			t.Fatalf("应广播 session_updated 与 branch_changed(params_reverted):\n%s", joined)
		}
		if got := st.WorkflowTitleOf(ctx, bwf); got == "主会话改的标题" {
			t.Fatalf("标题应改回旧值: %q", got)
		}
	})

	t.Run("分支被结束之后仍能撤销", func(t *testing.T) {
		ev := apply(store.SessionParamsPatch{Phase: strp("dev")})
		if res := wiredCall(srv, http.MethodPost, "/api/v1/workflows/"+bwf+"/branch/close", nil, login...); res.Code != http.StatusOK {
			t.Fatalf("结束分支: %d %s", res.Code, res.Raw)
		}
		sse := listenSSE(t, srv)
		if res := wiredCall(srv, http.MethodPost, revertURL(ev.ID), nil, login...); res.Code != http.StatusOK {
			t.Fatalf("结束后仍应能撤销: %d %s", res.Code, res.Raw)
		}
		if joined := strings.Join(drainSSE(sse), "\n"); !strings.Contains(joined, "phase_changed") {
			t.Fatalf("撤销阶段应广播 phase_changed:\n%s", joined)
		}
	})
}
