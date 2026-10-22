package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

func TestEtagMatches(t *testing.T) {
	etag := weakETag([]byte("内容"))
	if !strings.HasPrefix(etag, `W/"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("应是弱校验符: %s", etag)
	}
	if etag != weakETag([]byte("内容")) || etag == weakETag([]byte("内容 ")) {
		t.Fatalf("同样内容同一个 ETag，不同内容不同: %s", etag)
	}
	strong := strings.TrimPrefix(etag, "W/")
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"空头", "", false},
		{"弱 = 弱", etag, true},
		{"弱比较：对方是强校验符也算命中", strong, true},
		{"星号", "*", true},
		{"多个里命中一个", `"other", ` + etag + `, W/"x"`, true},
		{"首尾空白", "  " + etag + "  ", true},
		{"不同", `W/"deadbeef"`, false},
		{"前缀相同但不等", strings.TrimSuffix(etag, `"`) + `0"`, false},
	}
	for _, tc := range cases {
		if got := etagMatches(tc.header, etag); got != tc.want {
			t.Errorf("%s: etagMatches(%q) = %v，应为 %v", tc.name, tc.header, got, tc.want)
		}
	}
	if etagMatches(etag, "") {
		t.Error("服务端没有 ETag 时不能命中")
	}
}

// 整表序列化与改造前的 c.JSON(200, gin.H{"sessions": …}) 逐字节一致；ETag 不含 last_keepalive_at，其余字段入版本。
func TestMarshalSessionsPayloadVersion(t *testing.T) {
	t1 := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(40 * time.Second)
	mk := func(keepalive *time.Time) []model.FeedbackSession {
		return []model.FeedbackSession{
			{ID: "a", WorkflowID: "wf", Summary: "第一轮 <b>&</b>", Status: "pending", IsMCPActive: true, LastKeepaliveAt: keepalive},
			{ID: "b", WorkflowID: "wf", Summary: "第二轮", Status: "completed"},
		}
	}

	rows := mk(&t1)
	want, err := json.Marshal(gin.H{"sessions": rows})
	if err != nil {
		t.Fatal(err)
	}
	body, etag1, err := marshalSessionsPayload(rows)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(want) {
		t.Fatalf("正文必须与改造前逐字节一致:\n got=%s\nwant=%s", body, want)
	}
	if rows[0].LastKeepaliveAt != &t1 {
		t.Fatal("序列化版本时临时清掉的 last_keepalive_at 必须还原")
	}
	if !strings.Contains(string(body), `"last_keepalive_at"`) {
		t.Fatalf("响应正文里要带 last_keepalive_at: %s", body)
	}

	// 心跳时间变了：正文变、ETag 不变
	rows2 := mk(&t2)
	body2, etag2, err := marshalSessionsPayload(rows2)
	if err != nil {
		t.Fatal(err)
	}
	if etag1 != etag2 {
		t.Fatalf("last_keepalive_at 不应进入版本: %s vs %s", etag1, etag2)
	}
	if string(body) == string(body2) {
		t.Fatal("正文里的 last_keepalive_at 应随心跳变化")
	}

	// 没有心跳（没有 Agent 在等）与有心跳的版本一致：等待者开始 / 结束只靠 is_mcp_active 等字段体现
	body3, etag3, err := marshalSessionsPayload(mk(nil))
	if err != nil {
		t.Fatal(err)
	}
	if etag3 != etag1 || strings.Contains(string(body3), "last_keepalive_at") {
		t.Fatalf("无心跳行的版本应与有心跳时一致，且正文不含该字段: %s vs %s", etag3, etag1)
	}

	// 其余字段全部入版本
	mutations := map[string]func(r []model.FeedbackSession){
		"正文":           func(r []model.FeedbackSession) { r[1].Summary += "。" },
		"状态":           func(r []model.FeedbackSession) { r[0].Status = "completed" },
		"等待者在线":        func(r []model.FeedbackSession) { r[0].IsMCPActive = false },
		"等待者开始时间":      func(r []model.FeedbackSession) { r[0].MCPActiveAt = &t1 },
		"多一行":          func(r []model.FeedbackSession) { r[0].ID = "a2" },
		"回复内容":         func(r []model.FeedbackSession) { r[1].ResponseText = "好" },
		"更新时间":         func(r []model.FeedbackSession) { r[1].UpdatedAt = t2 },
		"用户消息追加":       func(r []model.FeedbackSession) { r[1].UserMessages = []string{"追加"} },
		"等待者超时设置":      func(r []model.FeedbackSession) { r[0].MCPTimeoutSec = 600 },
		"带心跳的行变化也要入版本": func(r []model.FeedbackSession) { r[0].Title = "标题" },
	}
	for name, mutate := range mutations {
		rows := mk(&t1)
		mutate(rows)
		_, etag, err := marshalSessionsPayload(rows)
		if err != nil {
			t.Fatal(err)
		}
		if etag == etag1 {
			t.Errorf("%s 变化后 ETag 必须变化", name)
		}
	}
}

func rawGet(srv *Server, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	return w
}

// GET /workflows/{id}/sessions：带 ETag 与 no-cache，If-None-Match 命中回 304（无正文、无 Content-Encoding），
// 内容变化或换了接口形状就回 200。
func TestWorkflowSessionsConditionalGet(t *testing.T) {
	srv := newWiredServer(t, nil)
	tok := wiredLogin(t, srv)
	auth := []string{"Authorization", "Bearer " + tok}
	ctx := context.Background()
	const wf = "wf-etag"
	path := "/api/v1/workflows/" + wf + "/sessions"

	sess, err := srv.store.CreateFeedbackSession(ctx, store.CreateSessionInput{SessionID: "s-etag-1", WorkflowID: wf, ProjectDirectory: "/tmp/etag", Title: "t", Summary: "第一轮", TimeoutSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}

	first := rawGet(srv, path, auth...)
	if first.Code != http.StatusOK {
		t.Fatalf("首次应 200: %d %s", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("应带弱 ETag: %q", etag)
	}
	if cc := first.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("Cache-Control 应为 private, no-cache: %q", cc)
	}
	var parsed struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &parsed); err != nil || len(parsed.Sessions) != 1 || parsed.Sessions[0]["session_id"] != "s-etag-1" {
		t.Fatalf("正文形状应保持 {sessions:[…]}: %v %s", err, first.Body.String())
	}

	// 条件请求命中：304，无正文
	hit := rawGet(srv, path, append([]string{"If-None-Match", etag}, auth...)...)
	if hit.Code != http.StatusNotModified || hit.Body.Len() != 0 {
		t.Fatalf("命中应 304 且无正文: %d len=%d", hit.Code, hit.Body.Len())
	}
	if hit.Header().Get("ETag") != etag {
		t.Fatalf("304 也要带回 ETag: %q", hit.Header().Get("ETag"))
	}

	// 走 gzip：200 压缩，304 不带 Content-Encoding
	gz200 := rawGet(srv, path, append([]string{"Accept-Encoding", "gzip"}, auth...)...)
	if gz200.Code != http.StatusOK || gz200.Header().Get("Content-Encoding") != "gzip" || gz200.Header().Get("ETag") != etag {
		t.Fatalf("gzip 200: code=%d enc=%q etag=%q（同一份内容在压缩与不压缩下 ETag 必须一致）", gz200.Code, gz200.Header().Get("Content-Encoding"), gz200.Header().Get("ETag"))
	}
	gz304 := rawGet(srv, path, append([]string{"Accept-Encoding", "gzip", "If-None-Match", etag}, auth...)...)
	if gz304.Code != http.StatusNotModified || gz304.Header().Get("Content-Encoding") != "" || gz304.Body.Len() != 0 {
		t.Fatalf("gzip 下 304 不能带 Content-Encoding 与正文: code=%d enc=%q len=%d", gz304.Code, gz304.Header().Get("Content-Encoding"), gz304.Body.Len())
	}
	if gz304.Header().Get("Vary") == "" {
		t.Fatal("304 应保留 Vary: Accept-Encoding")
	}

	// 别人的 ETag 不命中
	if miss := rawGet(srv, path, append([]string{"If-None-Match", `W/"nope"`}, auth...)...); miss.Code != http.StatusOK {
		t.Fatalf("不同 ETag 应 200: %d", miss.Code)
	}

	// Agent 的心跳只改 last_keepalive_at：ETag 不变（页面重连 / 切回时仍是 304），而 200 正文里带上它
	srv.mcpServer.RecordKeepaliveResponse(sess.ID)
	if kept := rawGet(srv, path, append([]string{"If-None-Match", etag}, auth...)...); kept.Code != http.StatusNotModified {
		t.Fatalf("只有心跳变化时应仍命中 304: %d %s", kept.Code, kept.Body.String())
	}
	if full := rawGet(srv, path, auth...); full.Header().Get("ETag") != etag || !strings.Contains(full.Body.String(), "last_keepalive_at") {
		t.Fatalf("心跳后不带条件的 200 应带 last_keepalive_at 且 ETag 不变: etag=%q body=%s", full.Header().Get("ETag"), full.Body.String())
	}

	// 会话内容变化：新 ETag，旧 ETag 不再命中
	if _, err := srv.store.SubmitFeedback(ctx, store.SubmitFeedbackInput{SessionID: sess.ID, ResponseText: "收到"}); err != nil {
		t.Fatal(err)
	}
	changed := rawGet(srv, path, append([]string{"If-None-Match", etag}, auth...)...)
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == etag {
		t.Fatalf("内容变化后应 200 且 ETag 变化: %d %q", changed.Code, changed.Header().Get("ETag"))
	}

	// 鉴权不受影响：没有凭据照旧 401（不能因为带了 If-None-Match 就绕过）
	if anon := rawGet(srv, path, "If-None-Match", etag); anon.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d", anon.Code)
	}
}
