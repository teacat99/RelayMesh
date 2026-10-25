package oauth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/api"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 接入窗口（#239）的整条链路：自动注册默认不是常开的，管理员在设置里开一个限时窗口，窗口期间客户端才能登记。
// 这里走真实的路由栈（登录、终端解锁、审计），和浏览器里发生的一样。

type windowEnv struct {
	t      *testing.T
	st     *store.Store
	engine http.Handler
	jwt    string
	unlock string
}

// newWindowEnv 起一个授权服务器已开启的 Hub；mutate 可以改策略（默认是限时窗口）。
func newWindowEnv(t *testing.T, mutate func(p *model.OAuthPolicy)) *windowEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetOAuthPolicy(context.Background())
	p.Enabled = true
	if mutate != nil {
		mutate(&p)
	}
	if err := st.SaveOAuthPolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	engine := api.NewServer(oauthTestCfg(), st, nil).Engine()
	jwt := loginJWT(t, engine, "admin", "oauth-test-pass")
	return &windowEnv{t: t, st: st, engine: engine, jwt: jwt, unlock: terminalUnlock(t, engine, jwt, "oauth-test-pass")}
}

// call 以管理员身份调接口；unlocked 为假时不带终端解锁令牌。返回状态码与解析后的 JSON（解析不了给空 map）。
func (e *windowEnv) call(method, path string, body any, unlocked bool) (int, map[string]any) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.jwt)
	if unlocked {
		req.Header.Set("X-Terminal-Unlock", e.unlock)
	}
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// register 以匿名客户端的身份调 POST /oauth/register（和 ChatGPT / Claude 一样不带任何凭据）。
func (e *windowEnv) register(name string, redirects ...string) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{
		"client_name": name, "redirect_uris": redirects,
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// dcr 读 GET /api/v1/oauth/policy 里 effective.dcr（设置页的接入窗口卡片读的就是它）。
func (e *windowEnv) dcr() map[string]any {
	e.t.Helper()
	code, body := e.call(http.MethodGet, "/api/v1/oauth/policy", nil, false)
	if code != http.StatusOK {
		e.t.Fatalf("读策略失败 %d %v", code, body)
	}
	eff, _ := body["effective"].(map[string]any)
	d, ok := eff["dcr"].(map[string]any)
	if !ok {
		e.t.Fatalf("effective 里没有 dcr：%v", eff)
	}
	return d
}

// putPolicy 读出当前策略、让 mutate 改一改、再保存回去（需要解锁，和设置页一样）。
func (e *windowEnv) putPolicy(mutate func(p map[string]any)) {
	e.t.Helper()
	_, body := e.call(http.MethodGet, "/api/v1/oauth/policy", nil, false)
	p, _ := body["policy"].(map[string]any)
	mutate(p)
	if code, res := e.call(http.MethodPut, "/api/v1/oauth/policy", map[string]any{"policy": p}, true); code != http.StatusOK {
		e.t.Fatalf("保存策略失败 %d %v", code, res)
	}
}

func (e *windowEnv) auditEvents(event string) []model.TerminalAuditLog {
	e.t.Helper()
	rows, err := e.st.ListTerminalAudit(context.Background(), 500, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []model.TerminalAuditLog
	for _, r := range rows {
		if r.Event == event {
			out = append(out, r)
		}
	}
	return out
}

func remaining(t *testing.T, d map[string]any) int {
	t.Helper()
	v, ok := d["remaining_seconds"].(float64)
	if !ok {
		t.Fatalf("没有 remaining_seconds：%v", d)
	}
	return int(v)
}

func TestDCRWindow_ClosedByDefaultThenOpenThenClosed(t *testing.T) {
	e := newWindowEnv(t, nil)

	// 默认是限时窗口、窗口没开：登记被拒，并告诉客户端（以及用户）去开接入窗口。
	code, body := e.register("连接器甲", "https://chatgpt.com/connector_platform_oauth_redirect")
	if code != http.StatusForbidden || body["error"] != "registration_window_closed" {
		t.Fatalf("窗口没开时应当 403 registration_window_closed，得到 %d %v", code, body)
	}
	if desc, _ := body["error_description"].(string); !strings.Contains(desc, "接入窗口") {
		t.Fatalf("拒绝时应当说明去开接入窗口：%v", body)
	}
	d := e.dcr()
	if d["mode"] != "window" || d["open"] != false || d["default_minutes"] != float64(10) || d["max_minutes"] != float64(60) {
		t.Fatalf("默认状态不对：%v", d)
	}

	// 没解锁开不了窗口，登记依然被拒。
	if code, _ := e.call(http.MethodPost, "/api/v1/oauth/window", map[string]any{}, false); code != http.StatusForbidden {
		t.Fatalf("没解锁时开窗口应当 403，得到 %d", code)
	}
	if code, _ := e.register("连接器甲", "https://chatgpt.com/cb"); code != http.StatusForbidden {
		t.Fatalf("没解锁时窗口不应被打开")
	}

	// 解锁后开窗口：默认 10 分钟，登记被受理。
	code, body = e.call(http.MethodPost, "/api/v1/oauth/window", map[string]any{}, true)
	if code != http.StatusOK {
		t.Fatalf("开窗口失败 %d %v", code, body)
	}
	d, _ = body["dcr"].(map[string]any)
	if d["open"] != true || d["mode"] != "window" {
		t.Fatalf("开窗口之后应当是开着的：%v", d)
	}
	if r := remaining(t, d); r < 590 || r > 600 {
		t.Fatalf("默认应当是 10 分钟，剩余 %d 秒", r)
	}
	if got := e.dcr(); got["open"] != true {
		t.Fatalf("设置页读到的状态应当是开着的：%v", got)
	}
	if code, body := e.register("连接器甲", "https://chatgpt.com/connector_platform_oauth_redirect"); code != http.StatusCreated {
		t.Fatalf("窗口开着时应当受理登记，得到 %d %v", code, body)
	}

	// 没解锁关不了窗口；解锁后关闭立刻生效，登记又被拒。
	if code, _ := e.call(http.MethodDelete, "/api/v1/oauth/window", nil, false); code != http.StatusForbidden {
		t.Fatalf("没解锁时关窗口应当 403，得到 %d", code)
	}
	if e.dcr()["open"] != true {
		t.Fatalf("被拒绝的关闭请求不应该关掉窗口")
	}
	code, body = e.call(http.MethodDelete, "/api/v1/oauth/window", nil, true)
	if code != http.StatusOK {
		t.Fatalf("关窗口失败 %d %v", code, body)
	}
	if d, _ = body["dcr"].(map[string]any); d["open"] != false {
		t.Fatalf("关闭之后应当是关着的：%v", d)
	}
	if code, body := e.register("连接器乙", "https://chatgpt.com/cb"); code != http.StatusForbidden || body["error"] != "registration_window_closed" {
		t.Fatalf("关闭之后应当又被拒，得到 %d %v", code, body)
	}
	// 关一个本来就关着的窗口也是成功（幂等），而且不再记审计。
	if code, _ := e.call(http.MethodDelete, "/api/v1/oauth/window", nil, true); code != http.StatusOK {
		t.Fatalf("重复关闭应当仍然成功，得到 %d", code)
	}

	// 审计：开启与关闭各一条，带操作者与来源地址；关闭本来就关着的窗口不记。
	opens := e.auditEvents("oauth_window_open")
	if len(opens) != 1 || opens[0].Actor == "" || opens[0].IP == "" || !strings.HasPrefix(opens[0].Detail, "open until=") {
		t.Fatalf("开启窗口应当记一条带操作者与来源的审计：%+v", opens)
	}
	closes := e.auditEvents("oauth_window_close")
	if len(closes) != 1 || closes[0].Actor == "" || closes[0].Detail != "manual" {
		t.Fatalf("关闭窗口应当只记一条：%+v", closes)
	}
}

func TestDCRWindow_OpenResetsAndExtendAccumulates(t *testing.T) {
	e := newWindowEnv(t, nil)
	open := func(body map[string]any) map[string]any {
		t.Helper()
		code, res := e.call(http.MethodPost, "/api/v1/oauth/window", body, true)
		if code != http.StatusOK {
			t.Fatalf("开 / 续窗口失败 %d %v", code, res)
		}
		d, _ := res["dcr"].(map[string]any)
		return d
	}
	if r := remaining(t, open(map[string]any{"minutes": 10})); r < 590 || r > 600 {
		t.Fatalf("开 10 分钟：剩余 %d 秒", r)
	}
	// 续期在剩余时间上累加。
	if r := remaining(t, open(map[string]any{"minutes": 10, "extend": true})); r < 1190 || r > 1200 {
		t.Fatalf("续 10 分钟应当累加到约 20 分钟：剩余 %d 秒", r)
	}
	// 总剩余不超过 60 分钟，请求的时长再大也一样。
	if r := remaining(t, open(map[string]any{"minutes": 999, "extend": true})); r < 3590 || r > 3600 {
		t.Fatalf("总剩余应当封顶 60 分钟：剩余 %d 秒", r)
	}
	// 「开启」是重置：从现在起 5 分钟，而不是累加。
	if r := remaining(t, open(map[string]any{"minutes": 5})); r < 290 || r > 300 {
		t.Fatalf("重新开启应当重置为 5 分钟：剩余 %d 秒", r)
	}
	// 窗口没开着时「续期」等同于打开。
	e.call(http.MethodDelete, "/api/v1/oauth/window", nil, true)
	if r := remaining(t, open(map[string]any{"minutes": 3, "extend": true})); r < 170 || r > 180 {
		t.Fatalf("关着时续期应当等同于打开 3 分钟：剩余 %d 秒", r)
	}
	// 没带请求体等于开默认时长；请求体不是合法 JSON 才是 400。
	e.call(http.MethodDelete, "/api/v1/oauth/window", nil, true)
	if r := remaining(t, open(nil)); r < 590 || r > 600 {
		t.Fatalf("没带请求体应当开默认 10 分钟：剩余 %d 秒", r)
	}
	if code, _ := e.call(http.MethodPost, "/api/v1/oauth/window", map[string]any{"minutes": "十分钟"}, true); code != http.StatusBadRequest {
		t.Fatalf("minutes 不是数字应当 400，得到 %d", code)
	}
	// 审计里区分开启与续期。
	var sawExtend bool
	for _, r := range e.auditEvents("oauth_window_open") {
		if strings.HasPrefix(r.Detail, "extend until=") {
			sawExtend = true
		}
	}
	if !sawExtend {
		t.Fatalf("续期应当在审计里标明 extend")
	}
}

func TestDCRWindow_NotApplicableModesGive409(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(p *model.OAuthPolicy)
		wantCode     string
		registerWant int
		registerErr  string
	}{
		{"自动注册关闭", func(p *model.OAuthPolicy) { p.AllowDCR = false }, "dcr_off", http.StatusForbidden, "registration_disabled"},
		{"自动注册常开", func(p *model.OAuthPolicy) { p.DCRAlways = true }, "dcr_always", http.StatusCreated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newWindowEnv(t, tc.mutate)
			code, body := e.call(http.MethodPost, "/api/v1/oauth/window", map[string]any{}, true)
			if code != http.StatusConflict || body["code"] != tc.wantCode {
				t.Fatalf("应当 409 %s，得到 %d %v", tc.wantCode, code, body)
			}
			code, body = e.register("连接器", "https://chatgpt.com/cb")
			if code != tc.registerWant || (tc.registerErr != "" && body["error"] != tc.registerErr) {
				t.Fatalf("登记应当 %d %q，得到 %d %v", tc.registerWant, tc.registerErr, code, body)
			}
		})
	}

	t.Run("授权服务器没开", func(t *testing.T) {
		e := newWindowEnv(t, func(p *model.OAuthPolicy) { p.Enabled = false })
		code, body := e.call(http.MethodPost, "/api/v1/oauth/window", map[string]any{}, true)
		if code != http.StatusConflict || body["code"] != "oauth_disabled" {
			t.Fatalf("总开关关着时应当 409 oauth_disabled，得到 %d %v", code, body)
		}
		if d := e.dcr(); d["open"] != false {
			t.Fatalf("总开关关着时不可能受理登记：%v", d)
		}
	})
}

// 窗口只属于「限时窗口」方式：改成别的方式、或者关掉总开关，窗口一并关闭，免得以后切回来时冒出一个早被忘记的窗口。
func TestDCRWindow_LeavingWindowModeClosesIt(t *testing.T) {
	e := newWindowEnv(t, nil)
	openWindow := func() {
		t.Helper()
		if code, body := e.call(http.MethodPost, "/api/v1/oauth/window", map[string]any{}, true); code != http.StatusOK {
			t.Fatalf("开窗口失败 %d %v", code, body)
		}
	}

	openWindow()
	e.putPolicy(func(p map[string]any) { p["dcr_always"] = true })
	if d := e.dcr(); d["mode"] != "always" || d["open"] != true {
		t.Fatalf("常开时应当随时受理：%v", d)
	}
	e.putPolicy(func(p map[string]any) { p["dcr_always"] = false })
	if d := e.dcr(); d["mode"] != "window" || d["open"] != false {
		t.Fatalf("从常开切回限时窗口时，之前的窗口不应还开着：%v", d)
	}

	openWindow()
	e.putPolicy(func(p map[string]any) { p["allow_dcr"] = false })
	if d := e.dcr(); d["mode"] != "off" || d["open"] != false {
		t.Fatalf("关闭自动注册后不应受理：%v", d)
	}
	e.putPolicy(func(p map[string]any) { p["allow_dcr"] = true })
	if d := e.dcr(); d["mode"] != "window" || d["open"] != false {
		t.Fatalf("重新打开自动注册后窗口应当是关着的：%v", d)
	}

	openWindow()
	e.putPolicy(func(p map[string]any) { p["enabled"] = false })
	e.putPolicy(func(p map[string]any) { p["enabled"] = true })
	if d := e.dcr(); d["open"] != false {
		t.Fatalf("关掉总开关再打开后窗口应当是关着的：%v", d)
	}
}

type clientRow struct {
	ID            string   `json:"id"`
	ClientName    string   `json:"client_name"`
	RedirectHosts []string `json:"redirect_hosts"`
	Untrusted     bool     `json:"untrusted"`
	Loopback      bool     `json:"loopback"`
	CreatedAt     string   `json:"created_at"`
	CreatedIP     string   `json:"created_ip"`
}

func (e *windowEnv) clients() ([]clientRow, float64) {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/clients", nil)
	req.Header.Set("Authorization", "Bearer "+e.jwt)
	w := httptest.NewRecorder()
	e.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		e.t.Fatalf("读清单失败 %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Clients     []clientRow `json:"clients"`
		ExpireHours float64     `json:"expire_hours"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		e.t.Fatal(err)
	}
	if res.Clients == nil {
		e.t.Fatalf("没有登记时 clients 应当是空数组而不是 null：%s", w.Body.String())
	}
	return res.Clients, res.ExpireHours
}

func findClient(rows []clientRow, name string) *clientRow {
	for i := range rows {
		if rows[i].ClientName == name {
			return &rows[i]
		}
	}
	return nil
}

var clientRefRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestDCRWindow_ListAndDeleteUnusedClients(t *testing.T) {
	e := newWindowEnv(t, func(p *model.OAuthPolicy) { p.DCRAlways = true })
	if rows, hours := e.clients(); len(rows) != 0 || hours != 24 {
		t.Fatalf("一开始应当没有登记、过期 24 小时：%v %v", rows, hours)
	}

	if code, body := e.register("本地工具", "http://127.0.0.1:53682/callback"); code != http.StatusCreated {
		t.Fatalf("登记失败 %d %v", code, body)
	}
	if code, body := e.register("ChatGPT 连接器", "https://chatgpt.com/connector_platform_oauth_redirect"); code != http.StatusCreated {
		t.Fatalf("登记失败 %d %v", code, body)
	}
	rows, _ := e.clients()
	if len(rows) != 2 {
		t.Fatalf("应当有两条登记：%+v", rows)
	}
	local, official := findClient(rows, "本地工具"), findClient(rows, "ChatGPT 连接器")
	if local == nil || official == nil {
		t.Fatalf("清单里缺登记：%+v", rows)
	}
	if !local.Untrusted || !local.Loopback || len(local.RedirectHosts) != 1 || local.RedirectHosts[0] != "127.0.0.1" {
		t.Fatalf("本机回环登记的标记不对：%+v", local)
	}
	if official.Untrusted || official.Loopback || len(official.RedirectHosts) != 1 || official.RedirectHosts[0] != "chatgpt.com" {
		t.Fatalf("官方域名登记不该被标成陌生来源：%+v", official)
	}
	for _, r := range rows {
		if !clientRefRE.MatchString(r.ID) {
			t.Fatalf("登记的指认应当是 32 位十六进制：%q", r.ID)
		}
		if r.CreatedIP == "" || r.CreatedAt == "" {
			t.Fatalf("登记应当带来源地址与时间：%+v", r)
		}
	}
	// 完整的 client_id 与来源地址之外的内部字段不外泄：清单行里只有约定的字段。
	raw := httptest.NewRecorder()
	rq := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/clients", nil)
	rq.Header.Set("Authorization", "Bearer "+e.jwt)
	e.engine.ServeHTTP(raw, rq)
	if strings.Contains(raw.Body.String(), "client_id") || strings.Contains(raw.Body.String(), "redirect_uris") {
		t.Fatalf("清单不该带完整的 client_id 与回调地址：%s", raw.Body.String())
	}

	// 删除：没解锁不行，id 形状不对是 400，删成功后清单里没了，再删是 404。
	del := func(id string, unlocked bool) int {
		code, _ := e.call(http.MethodDelete, "/api/v1/oauth/clients/"+id, nil, unlocked)
		return code
	}
	if code := del(local.ID, false); code != http.StatusForbidden {
		t.Fatalf("没解锁删登记应当 403，得到 %d", code)
	}
	if code := del("not-a-valid-id", true); code != http.StatusBadRequest {
		t.Fatalf("id 形状不对应当 400，得到 %d", code)
	}
	if code := del(strings.Repeat("0", 32), true); code != http.StatusNotFound {
		t.Fatalf("不存在的登记应当 404，得到 %d", code)
	}
	if code := del(local.ID, true); code != http.StatusOK {
		t.Fatalf("删除登记失败 %d", code)
	}
	if rows, _ := e.clients(); len(rows) != 1 || rows[0].ClientName != "ChatGPT 连接器" {
		t.Fatalf("删除之后应当只剩一条：%+v", rows)
	}
	if code := del(local.ID, true); code != http.StatusNotFound {
		t.Fatalf("重复删除应当 404，得到 %d", code)
	}
	if got := e.auditEvents("oauth_client_delete"); len(got) != 1 || got[0].Detail != "本地工具" || got[0].Actor == "" {
		t.Fatalf("删除应当记一条带操作者与客户端名的审计：%+v", got)
	}
}

// 一条登记一旦走完授权（拿到过令牌）就不再是「刚登记」：不在清单里，也不能从这里删（要撤销请到「已授权的应用」）。
func TestDCRWindow_AuthorizedClientIsNotListedNorDeletable(t *testing.T) {
	e := newWindowEnv(t, func(p *model.OAuthPolicy) { p.DCRAlways = true })
	clientID, redirectURI, resource, verifier, challenge := registerTestClient(t, e.engine)
	rows, _ := e.clients()
	if len(rows) != 1 {
		t.Fatalf("授权之前应当在清单里：%+v", rows)
	}
	ref := rows[0].ID

	code := approveFlow(t, e.engine, e.jwt, e.unlock, clientID, redirectURI, resource, challenge, verifier)
	// 批准了但令牌还没兑换：授权码与授权记录都有了，登记仍是「还没用过」。
	if rows, _ := e.clients(); len(rows) != 1 {
		t.Fatalf("兑换令牌之前仍应当在清单里：%+v", rows)
	}
	postToken(t, e.engine, code, clientID, redirectURI, resource, verifier, http.StatusOK)

	if rows, _ := e.clients(); len(rows) != 0 {
		t.Fatalf("拿到令牌之后不该再出现在清单里：%+v", rows)
	}
	if c, _ := e.call(http.MethodDelete, "/api/v1/oauth/clients/"+ref, nil, true); c != http.StatusNotFound {
		t.Fatalf("已授权的登记不能从这里删，应当 404，得到 %d", c)
	}
}

// 删掉一条登记时，它正在进行中的授权也要一并作废：管理员刚删掉的登记，不能还在批准页上被批准、或用已批准的授权码换到令牌。
func TestDCRWindow_DeleteInvalidatesPendingAndApprovedAuthorizations(t *testing.T) {
	e := newWindowEnv(t, func(p *model.OAuthPolicy) { p.DCRAlways = true })

	t.Run("批准页还开着", func(t *testing.T) {
		clientID, redirectURI, resource, _, challenge := registerTestClient(t, e.engine)
		reqID := startPendingAuthorize(t, e.engine, clientID, redirectURI, resource, challenge)
		rows, _ := e.clients()
		var ref string
		for _, r := range rows {
			if strings.HasSuffix(clientID, "/oauth/clients/"+r.ID) {
				ref = r.ID
			}
		}
		if ref == "" {
			t.Fatalf("清单里找不到刚登记的客户端：%+v", rows)
		}
		if c, _ := e.call(http.MethodDelete, "/api/v1/oauth/clients/"+ref, nil, true); c != http.StatusOK {
			t.Fatalf("删除失败 %d", c)
		}
		if c, _ := e.call(http.MethodGet, "/api/v1/oauth/requests/"+reqID, nil, false); c != http.StatusNotFound {
			t.Fatalf("登记删除后，它的待决授权请求应当不存在，得到 %d", c)
		}
		dec := map[string]any{"approve": true, "nonce": reqID, "terminal_hosts": []string{"*"}, "terminal_mode_cap": "approve"}
		if c, _ := e.call(http.MethodPost, "/api/v1/oauth/requests/"+reqID+"/decision", dec, true); c == http.StatusOK {
			t.Fatalf("登记删除后不应还能批准")
		}
	})

	t.Run("已批准、令牌还没兑换", func(t *testing.T) {
		clientID, redirectURI, resource, verifier, challenge := registerTestClient(t, e.engine)
		code := approveFlow(t, e.engine, e.jwt, e.unlock, clientID, redirectURI, resource, challenge, verifier)
		rows, _ := e.clients()
		var ref string
		for _, r := range rows {
			if strings.HasSuffix(clientID, "/oauth/clients/"+r.ID) {
				ref = r.ID
			}
		}
		if ref == "" {
			t.Fatalf("清单里找不到刚登记的客户端：%+v", rows)
		}
		if c, _ := e.call(http.MethodDelete, "/api/v1/oauth/clients/"+ref, nil, true); c != http.StatusOK {
			t.Fatalf("删除失败 %d", c)
		}
		// 授权码换不到令牌。
		form := url.Values{}
		form.Set("grant_type", "authorization_code")
		form.Set("code", code)
		form.Set("redirect_uri", redirectURI)
		form.Set("client_id", clientID)
		form.Set("code_verifier", verifier)
		form.Set("resource", resource)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		e.engine.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Fatalf("登记删除后，已批准的授权码不应还能换到令牌：%s", w.Body.String())
		}
		// 批准时建好的授权记录被撤销，不留下任何还活着的授权。
		_, grants := e.call(http.MethodGet, "/api/v1/oauth/grants", nil, false)
		list, _ := grants["grants"].([]any)
		if len(list) == 0 {
			t.Fatalf("应当有一条授权记录：%v", grants)
		}
		for _, g := range list {
			if row, _ := g.(map[string]any); row["revoked"] != true {
				t.Fatalf("登记删除后，它名下的授权应当全部被撤销：%v", row)
			}
		}
	})

	// 删除之后用这个 client_id 发起授权，不会再走到批准页。
	t.Run("已删除的 client_id 不能再发起授权", func(t *testing.T) {
		clientID, redirectURI, resource, _, challenge := registerTestClient(t, e.engine)
		rows, _ := e.clients()
		for _, r := range rows {
			if strings.HasSuffix(clientID, "/oauth/clients/"+r.ID) {
				e.call(http.MethodDelete, "/api/v1/oauth/clients/"+r.ID, nil, true)
			}
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, buildAuthorizeURL(clientID, redirectURI, resource, challenge, "st"), nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		e.engine.ServeHTTP(w, req)
		if w.Code == http.StatusFound && strings.Contains(w.Header().Get("Location"), "req=") {
			t.Fatalf("已删除的登记不应再走到批准页：%s", w.Header().Get("Location"))
		}
	})
}

// 回归（#239）：本服务签发的 DCR client_id 在 https 对外地址下以 https:// 开头，过去批准页把它当成 CIMD、
// 拿本服务自己的域名去比对，于是真正的 ChatGPT / Claude 也被标上「不在默认信任列表中」。
func TestConsentWarning_DCRClientUnderHTTPSIssuer(t *testing.T) {
	e := newWindowEnv(t, func(p *model.OAuthPolicy) { p.DCRAlways = true })
	const resource = "https://hub.example.test/mcp"
	challenge := pkceS256(strings.Repeat("c", 43))

	warn := func(redirect string) bool {
		t.Helper()
		code, body := e.register("连接器 "+redirect, redirect)
		if code != http.StatusCreated {
			t.Fatalf("登记失败 %d %v", code, body)
		}
		clientID, _ := body["client_id"].(string)
		if !strings.HasPrefix(clientID, "https://hub.example.test/oauth/clients/") {
			t.Fatalf("测试前提不成立：client_id 应当以 https 对外地址开头，得到 %q", clientID)
		}
		reqID := startPendingAuthorize(t, e.engine, clientID, redirect, resource, challenge)
		c, res := e.call(http.MethodGet, "/api/v1/oauth/requests/"+reqID, nil, false)
		if c != http.StatusOK {
			t.Fatalf("读待决授权失败 %d %v", c, res)
		}
		view, _ := res["request"].(map[string]any)
		w, _ := view["warn_untrusted"].(bool)
		return w
	}

	if warn("https://chatgpt.com/connector_platform_oauth_redirect") {
		t.Errorf("官方 ChatGPT 回调不该被标成陌生来源")
	}
	if warn("https://claude.ai/api/mcp/auth_callback") {
		t.Errorf("官方 Claude 回调不该被标成陌生来源")
	}
	if !warn("https://tools.example.com/cb") {
		t.Errorf("陌生域名的回调必须被标成陌生来源")
	}
}

// 环境变量把 OAuth 整个关掉（RELAYMESH_OAUTH=off）时没有授权服务，读策略仍然可用，接入窗口只是「没在服务」。
func TestDCRWindow_PolicyReadableWhenOAuthOffByEnv(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	cfg := oauthTestCfg()
	cfg.OAuthOff = true
	engine := api.NewServer(cfg, st, nil).Engine()
	e := &windowEnv{t: t, st: st, engine: engine, jwt: loginJWT(t, engine, "admin", "oauth-test-pass")}
	d := e.dcr()
	if d["open"] != false || d["mode"] != "window" {
		t.Fatalf("环境变量关闭时接入窗口不会受理任何登记：%v", d)
	}
	// 窗口与登记清单依赖授权服务本身，关着时这些路由不存在。
	if c, _ := e.call(http.MethodPost, "/api/v1/oauth/window", map[string]any{}, false); c != http.StatusNotFound {
		t.Fatalf("环境变量关闭时窗口接口应当不存在，得到 %d", c)
	}
}
