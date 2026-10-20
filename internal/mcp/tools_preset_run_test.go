package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// preset_run（#179）的 MCP 一侧：权限位、项目管理 Agent 的判定、审计与回执形状。
// 预设的资格、闸门与频率账本在 Hub 一侧（internal/terminal/agent_run_test.go），这里用假的 Runner 验证 MCP 层自己的规则。

type presetCoded struct{ code, msg string }

func (e *presetCoded) Error() string     { return e.msg }
func (e *presetCoded) ErrorCode() string { return e.code }

type fakePresetRunner struct {
	mu      sync.Mutex
	calls   []string
	actors  []PresetRunActor
	runErr  error
	listErr error
}

func (f *fakePresetRunner) rec(call string, a PresetRunActor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.actors = append(f.actors, a)
}

func (f *fakePresetRunner) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakePresetRunner) List(_ context.Context, a PresetRunActor) (any, any, error) {
	f.rec("list", a)
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	return []map[string]any{{"preset": "p-kid", "name": "子会话", "can_run": true}}, map[string]any{"max_per_hour": 10}, nil
}

func (f *fakePresetRunner) Status(_ context.Context, a PresetRunActor, preset string) (any, error) {
	f.rec("status:"+preset, a)
	return map[string]any{"preset": preset, "can_run": true}, nil
}

func (f *fakePresetRunner) Run(_ context.Context, a PresetRunActor, preset string) (*PresetRunOutcome, error) {
	f.rec("run:"+preset, a)
	if f.runErr != nil {
		return nil, f.runErr
	}
	res := map[string]any{"preset": preset, "run_id": "run-1", "state": "running", "workflow_id": "wf-b", "name": "子会话"}
	return &PresetRunOutcome{Result: res, Preset: preset, Name: "子会话", WorkflowID: "wf-b", RunID: "run-1", State: "running"}, nil
}

// presetFx 是 policyFixture + 一个带「运行预设」位的凭据 + 假 Runner。wf-a 是项目管理 Agent（持有 tasks 授权）。
type presetFx struct {
	*policyFixture
	runner *fakePresetRunner
	token  string // 带 Launcher 位（与治理类权限）的凭据
}

func newPresetFx(t *testing.T) *presetFx {
	t.Helper()
	f := newPolicyFixture(t)
	r := &fakePresetRunner{}
	f.srv.SetPresetRunner(r)
	perms := model.AllPermissions()
	perms.Launcher = true
	cred := &model.MCPCredential{Name: "mgr-agent", Permissions: perms}
	if err := f.srv.store.CreateCredential(context.Background(), cred); err != nil {
		t.Fatal(err)
	}
	// 身份 = （凭据、主机、目录）：授权要记在「这把凭据」下的身份上，所以用这把凭据重新认一遍各工作流的身份。
	for wf, dir := range f.dirs {
		id, _ := f.srv.store.ResolveCallerIdentity(context.Background(), wf, cred.ID, "h", dir, timeNow())
		if id == 0 {
			t.Fatalf("%s 应得到身份", wf)
		}
		f.ids[wf] = id
	}
	fx := &presetFx{policyFixture: f, runner: r, token: cred.Token}
	f.grantTasks("wf-a")
	return fx
}

// presetCall 以带 Launcher 位的凭据调用 preset_run；返回文本与 isError。
func (fx *presetFx) presetCall(wf string, extra map[string]any) (string, bool) {
	fx.t.Helper()
	return fx.presetCallWith(fx.token, fx.as(wf, extra))
}

func (fx *presetFx) presetCallWith(token string, args map[string]any) (string, bool) {
	fx.t.Helper()
	return fx.callTool(token, "preset_run", args)
}

// callTool 以给定令牌调用任意工具；返回文本与 isError。
func (fx *presetFx) callTool(token, tool string, args map[string]any) (string, bool) {
	fx.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	code, resp := rpcFrom(fx.t, fx.srv, token, "", string(body))
	if code != http.StatusOK {
		fx.t.Fatalf("preset_run HTTP %d: %#v", code, resp)
	}
	if resp.Error != nil {
		return resp.Error.Message, true
	}
	b, _ := json.Marshal(resp.Result)
	var res struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &res); err != nil || len(res.Content) == 0 {
		fx.t.Fatalf("preset_run 响应异常: %v %s", err, b)
	}
	return res.Content[0].Text, res.IsError
}

func (fx *presetFx) codeOf(text string) string {
	fx.t.Helper()
	var p struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &p); err != nil || p.Error.Code == "" || strings.TrimSpace(p.Error.Message) == "" {
		fx.t.Fatalf("应是带 code 与 message 的错误 JSON: %v %s", err, text)
	}
	return p.Error.Code
}

func (fx *presetFx) launcherAudits() []model.AuditEvent {
	fx.t.Helper()
	items, err := fx.srv.store.ListAuditEvents(context.Background(), store.AuditFilter{Action: model.AuditLauncherRun, Limit: 100})
	if err != nil {
		fx.t.Fatal(err)
	}
	return items
}

// ---------- 权限位：工具可见性、指引 ----------

func TestPresetRunToolIsVisibleOnlyWithTheLauncherBit(t *testing.T) {
	srv, _ := setupWorkItemMCP(t)
	plain := newOrdinary(t, srv, "plain", model.AllPermissions())
	withBit := model.AllPermissions()
	withBit.Launcher = true
	launcher := newOrdinary(t, srv, "launcher", withBit)
	termWithBit := newOrdinary(t, srv, "term-launcher", model.Permissions{Terminal: true, Launcher: true})

	names := func(token string) []string {
		_, resp := rpcFrom(t, srv, token, "", capToolsList)
		return capToolNames(resp)
	}
	has := func(list []string, n string) bool {
		for _, x := range list {
			if x == n {
				return true
			}
		}
		return false
	}
	if has(names(plain), "preset_run") {
		t.Fatalf("没有运行预设位的凭据不该看到 preset_run: %v", names(plain))
	}
	if !has(names(launcher), "preset_run") {
		t.Fatalf("带运行预设位的凭据应看到 preset_run: %v", names(launcher))
	}
	if !has(names(termWithBit), "preset_run") {
		t.Fatalf("ExecMesh 档加运行预设位也应看到 preset_run: %v", names(termWithBit))
	}

	// 没有这一位：调用也被分发层拒绝（与 tools/list 由同一个位决定）。
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"preset_run","arguments":{"action":"list","workflow_id":"wf-x"}}}`
	_, resp := rpcFrom(t, srv, plain, "", body)
	b, _ := json.Marshal(resp)
	if !strings.Contains(string(b), "not allowed") {
		t.Fatalf("没有这一位的凭据调用应被拒绝: %s", b)
	}
	if model.AllPermissions().AllowsTool("preset_run") {
		t.Fatal("AllPermissions 不该包含运行预设位")
	}

	// 指引：带这一位的令牌收到一节说明，没有的没有；ExecMesh 档也带上。
	if got := initializeInstructions(t, srv, launcher); !strings.Contains(got, "运行子会话的预设（preset_run）") || !strings.Contains(got, "9b.") {
		t.Fatalf("带运行预设位的治理令牌指引里应有这一节: %s", got)
	}
	if got := initializeInstructions(t, srv, plain); strings.Contains(got, "preset_run") {
		t.Fatalf("没有这一位的令牌指引里不该有 preset_run")
	}
	if got := initializeInstructions(t, srv, termWithBit); !strings.Contains(got, "运行子会话的预设（preset_run）") || strings.Contains(got, "9b.") {
		t.Fatalf("ExecMesh 档令牌指引末尾应有这一节（不带序号）: %s", got)
	}
	// 指引与工具说明都不应暗示可以传提示词 / 命令。
	for _, def := range presetRunToolDefinitions(withBit) {
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"prompt", "command", "model", "posture", "conversation_id", "bin"} {
			if _, ok := schema.Properties[banned]; ok {
				t.Fatalf("preset_run 不该有参数 %q（预设内容全由人定死）", banned)
			}
		}
	}
}

// ---------- 项目管理 Agent 的判定 ----------

func TestPresetRunRequiresTheProjectManager(t *testing.T) {
	fx := newPresetFx(t)

	// 项目管理 Agent（持有 tasks 授权）：放行，Runner 收到的是它的身份、名字、工作流与项目。
	text, isErr := fx.presetCall("wf-a", map[string]any{"action": "list"})
	if isErr {
		t.Fatalf("管理 Agent 应能 list: %s", text)
	}
	var out struct {
		Project struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"project"`
		Presets []map[string]any `json:"presets"`
		Limits  map[string]any   `json:"limits"`
		Hint    string           `json:"hint"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil || out.Project.ID != fx.project.ID || out.Project.Name != "P" ||
		len(out.Presets) != 1 || out.Limits["max_per_hour"] == nil || out.Hint == "" {
		t.Fatalf("list 回执不对: %v %s", err, text)
	}
	if calls := fx.runner.called(); len(calls) != 1 || calls[0] != "list" {
		t.Fatalf("Runner 应被调用一次: %v", calls)
	}
	a := fx.runner.actors[0]
	if a.IdentityID != fx.ids["wf-a"] || a.WorkflowID != "wf-a" || a.ProjectID != fx.project.ID || a.Label == "" {
		t.Fatalf("Runner 收到的调用方不对: %+v", a)
	}

	// 普通 Agent：同一个凭据（有运行预设位）也不行——两道门缺一不可；Runner 一次都没被调用，项目审计留下拒绝。
	text, isErr = fx.presetCall("wf-b", map[string]any{"action": "run", "preset": "p-kid"})
	if !isErr || fx.codeOf(text) != "not_project_manager" || !strings.Contains(text, "任务管理") {
		t.Fatalf("普通 Agent 应被拒绝并告诉怎么办: %v %s", isErr, text)
	}
	text, isErr = fx.presetCall("wf-b", map[string]any{"action": "list"})
	if !isErr || fx.codeOf(text) != "not_project_manager" {
		t.Fatalf("普通 Agent 连 list 也不行（列表本身就是信息）: %s", text)
	}
	if calls := fx.runner.called(); len(calls) != 1 {
		t.Fatalf("被拒绝的调用不该到 Runner: %v", calls)
	}
	audits := fx.launcherAudits()
	if len(audits) != 1 || audits[0].Result != model.AuditDeny || audits[0].ScopeKind != model.ScopeTasks ||
		audits[0].ProjectID != fx.project.ID || !strings.Contains(audits[0].Detail, "not_project_manager") {
		t.Fatalf("应记一条被拒绝的项目审计（同一原因不重复记）: %+v", audits)
	}

	// 管理 Agent 的判定与项目的访问控制档位无关：off / enforce 档照样是管理 Agent。
	for _, mode := range []string{"off", "enforce", "audit"} {
		fx.mode(mode)
		if text, isErr := fx.presetCall("wf-a", map[string]any{"action": "status", "preset": "p-kid"}); isErr {
			t.Fatalf("%s 档下管理 Agent 应能 status: %s", mode, text)
		}
		if text, isErr := fx.presetCall("wf-c", map[string]any{"action": "status", "preset": "p-kid"}); !isErr || fx.codeOf(text) != "not_project_manager" {
			t.Fatalf("%s 档下普通 Agent 仍被拒绝: %s", mode, text)
		}
	}

	// 撤销授权立刻失效。
	grants, err := fx.srv.store.ListGrants(context.Background(), fx.project.ID, false)
	if err != nil || len(grants) != 1 {
		t.Fatalf("应有一条授权: %v %+v", err, grants)
	}
	if _, err := fx.srv.store.RevokeGrant(context.Background(), fx.project.ID, grants[0].ID); err != nil {
		t.Fatal(err)
	}
	if text, isErr := fx.presetCall("wf-a", map[string]any{"action": "list"}); !isErr || fx.codeOf(text) != "not_project_manager" {
		t.Fatalf("撤销授权后不再是管理 Agent: %s", text)
	}
}

func TestPresetRunRejectsCallersThatAreNotInAProjectOrUnknown(t *testing.T) {
	fx := newPresetFx(t)
	before := len(fx.runner.called())

	// 项目外的工作流：没有可管理的项目。
	text, isErr := fx.presetCall("wf-solo", map[string]any{"action": "list"})
	if !isErr || fx.codeOf(text) != "not_in_project" {
		t.Fatalf("项目外的工作流应是 not_in_project: %s", text)
	}
	// 认不出身份：没有会话、也没带目录。
	text, isErr = fx.presetCallWith(fx.token, map[string]any{"action": "list", "workflow_id": "wf-ghost"})
	if !isErr || fx.codeOf(text) != "identity_unknown" {
		t.Fatalf("认不出身份应是 identity_unknown: %s", text)
	}
	// 没带 workflow_id。
	text, isErr = fx.presetCallWith(fx.token, map[string]any{"action": "list"})
	if !isErr || fx.codeOf(text) != "workflow_id_required" {
		t.Fatalf("没带 workflow_id: %s", text)
	}
	// 参数错误。
	text, isErr = fx.presetCall("wf-a", map[string]any{"action": "launch"})
	if !isErr || fx.codeOf(text) != "bad_action" {
		t.Fatalf("未知动作: %s", text)
	}
	for _, act := range []string{"run", "status"} {
		text, isErr = fx.presetCall("wf-a", map[string]any{"action": act})
		if !isErr || fx.codeOf(text) != "preset_required" {
			t.Fatalf("%s 没带 preset: %s", act, text)
		}
	}
	if got := len(fx.runner.called()); got != before {
		t.Fatalf("这些拒绝都不该到 Runner: %v", fx.runner.called())
	}

	// 已归档的项目：不再有管理 Agent。工作流建过任务之后它的板是「粘性」的，目录归属变了也仍指着原来的项目——
	// 这时只能靠对项目本身的归档判断拦住（光靠目录解析拦不住）。
	if text, isErr := fx.callTool(fx.token, "task_board", fx.as("wf-a", map[string]any{
		"action": "batch", "ops": []any{map[string]any{"title": "让板变成粘性的", "kind": "feature"}},
	})); isErr {
		t.Fatalf("建任务失败: %s", text)
	}
	if err := fx.srv.store.ArchiveProject(context.Background(), fx.project.ID); err != nil {
		t.Fatal(err)
	}
	text, isErr = fx.presetCall("wf-a", map[string]any{"action": "list"})
	if !isErr || fx.codeOf(text) != "not_in_project" {
		t.Fatalf("已归档的项目应是 not_in_project: %s", text)
	}
}

func TestPresetRunUnavailableWithoutRunner(t *testing.T) {
	fx := newPresetFx(t)
	fx.srv.SetPresetRunner(nil)
	text, isErr := fx.presetCall("wf-a", map[string]any{"action": "list"})
	if !isErr || fx.codeOf(text) != "terminal_unavailable" {
		t.Fatalf("终端功能不可用应明确报错: %s", text)
	}
}

// ---------- 运行与审计 ----------

func TestPresetRunRunRecordsProjectAuditAndPassesErrorsThrough(t *testing.T) {
	fx := newPresetFx(t)

	text, isErr := fx.presetCall("wf-a", map[string]any{"action": "run", "preset": "p-kid"})
	if isErr {
		t.Fatalf("管理 Agent 应能运行: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil || res["run_id"] != "run-1" || res["state"] != "running" {
		t.Fatalf("回执应原样带上运行结果: %v %s", err, text)
	}
	audits := fx.launcherAudits()
	if len(audits) != 1 {
		t.Fatalf("成功的运行应记一条项目审计: %+v", audits)
	}
	a := audits[0]
	if a.Result != model.AuditAllow || a.ScopeKind != model.ScopeTasks || a.Target != "preset:p-kid" || a.ProjectID != fx.project.ID ||
		!strings.HasPrefix(a.ActorRef, "identity:") {
		t.Fatalf("审计记录不对: %+v", a)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(a.Detail), &detail); err != nil ||
		detail["run_id"] != "run-1" || detail["workflow_id"] != "wf-b" || detail["preset"] != "p-kid" || detail["name"] != "子会话" {
		t.Fatalf("审计说明不对: %v %s", err, a.Detail)
	}
	for k := range detail {
		switch k {
		case "preset", "name", "workflow_id", "run_id", "state":
		default:
			t.Fatalf("审计说明里不该有 %q（只记预设、工作流、运行编号与状态）", k)
		}
	}

	// 再运行一次：允许的运行每次都记（不去重）。
	if _, isErr := fx.presetCall("wf-a", map[string]any{"action": "run", "preset": "p-kid"}); isErr {
		t.Fatal("第二次运行应成功")
	}
	if got := len(fx.launcherAudits()); got != 2 {
		t.Fatalf("每次成功的运行都应记审计: %d", got)
	}

	// Runner 的带码拒绝：原样以 {error:{code,message}} 返回并记 deny；同一原因 30 分钟内只记一条。
	fx.runner.runErr = &presetCoded{"agent_run_hourly", "过去一小时你已经发起了 10 次运行，每小时最多 10 次：请稍后再试"}
	for i := 0; i < 3; i++ {
		text, isErr = fx.presetCall("wf-a", map[string]any{"action": "run", "preset": "p-kid"})
		if !isErr || fx.codeOf(text) != "agent_run_hourly" || !strings.Contains(text, "每小时最多 10 次") {
			t.Fatalf("应原样带出拒绝码与原因: %s", text)
		}
	}
	denies := 0
	for _, e := range fx.launcherAudits() {
		if e.Result == model.AuditDeny {
			denies++
			if !strings.Contains(e.Detail, "agent_run_hourly") || e.Target != "preset:p-kid" {
				t.Fatalf("拒绝审计不对: %+v", e)
			}
		}
	}
	if denies != 1 {
		t.Fatalf("同一原因的拒绝应去重成一条: %d", denies)
	}
	// 不同的原因 / 不同的预设各记各的（否则第一条拒绝之后 30 分钟里别的原因都从审计里消失）。
	fx.runner.runErr = &presetCoded{"agent_run_preset_gap", "同一个预设两次运行之间至少隔 5 分钟"}
	if text, isErr = fx.presetCall("wf-a", map[string]any{"action": "run", "preset": "p-kid"}); !isErr || fx.codeOf(text) != "agent_run_preset_gap" {
		t.Fatalf("应带出新的拒绝码: %s", text)
	}
	if text, isErr = fx.presetCall("wf-a", map[string]any{"action": "run", "preset": "p-other"}); !isErr || fx.codeOf(text) != "agent_run_preset_gap" {
		t.Fatalf("别的预设同样被拒: %s", text)
	}
	codes := map[string]int{}
	for _, e := range fx.launcherAudits() {
		if e.Result == model.AuditDeny {
			var d struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal([]byte(e.Detail), &d)
			codes[d.Code+"@"+e.Target]++
		}
	}
	for _, want := range []string{"agent_run_hourly@preset:p-kid", "agent_run_preset_gap@preset:p-kid", "agent_run_preset_gap@preset:p-other"} {
		if codes[want] != 1 {
			t.Fatalf("拒绝审计里应各有一条 %s: %v", want, codes)
		}
	}

	// 不带码的错误（数据库故障之类）：普通的工具错误，不伪装成业务拒绝，也不记拒绝审计（拒绝审计只记业务上的拒绝）。
	fx.runner.runErr = errors.New("database is locked")
	before := len(fx.launcherAudits())
	text, isErr = fx.presetCall("wf-a", map[string]any{"action": "run", "preset": "p-other"})
	if !isErr || !strings.Contains(text, "database is locked") {
		t.Fatalf("内部错误应原样报告: %v %s", isErr, text)
	}
	if got := len(fx.launcherAudits()); got != before {
		t.Fatalf("内部错误不该记成拒绝审计: %d → %d", before, got)
	}
}

func TestPresetRunListErrorIsReported(t *testing.T) {
	fx := newPresetFx(t)
	fx.runner.listErr = &presetCoded{"terminal_unavailable", "终端功能在此服务上不可用"}
	text, isErr := fx.presetCall("wf-a", map[string]any{"action": "list"})
	if !isErr || fx.codeOf(text) != "terminal_unavailable" {
		t.Fatalf("list 的带码错误应原样返回: %s", text)
	}
}

// 令牌强度：与终端工具一致，弱令牌不能代表「运行预设」。创建 / 更新时服务端已拒绝弱令牌；
// 这里验证运行时的兜底（数据库被手工改动、或令牌后来被判定为弱）：直接以弱令牌的凭据上下文调用处理函数。
func TestPresetRunRejectsWeakTokenAtRuntime(t *testing.T) {
	fx := newPresetFx(t)
	raw, _ := json.Marshal(fx.as("wf-a", map[string]any{"action": "list"}))
	for name, token := range map[string]string{"空令牌": "", "太短": "short-token", "占位符": "changeme-changeme-changeme-changeme-changeme"} {
		out, isErr, err := fx.srv.handlePresetRun(context.Background(),
			&CredentialContext{CredentialID: 1, TokenString: token, Permissions: model.Permissions{Launcher: true}, Source: "db_credential"}, raw)
		if err != nil || !isErr {
			t.Fatalf("%s：应以 isError 拒绝: err=%v isErr=%v", name, err, isErr)
		}
		res, ok := out.(toolCallResult)
		if !ok || len(res.Content) != 1 {
			t.Fatalf("%s：应返回单条文本结果: %#v", name, out)
		}
		if code := fx.codeOf(res.Content[0].Text); code != "weak_token" {
			t.Fatalf("%s：错误码应为 weak_token，得到 %s", name, code)
		}
	}
	if got := fx.runner.called(); len(got) != 0 {
		t.Fatalf("弱令牌不能走到运行器: %v", got)
	}
	// 没有凭据上下文（理论上不会发生）同样拒绝，而不是空指针。
	if _, isErr, err := fx.srv.handlePresetRun(context.Background(), nil, raw); err != nil || !isErr {
		t.Fatalf("没有凭据上下文应以 isError 拒绝: err=%v isErr=%v", err, isErr)
	}
}
