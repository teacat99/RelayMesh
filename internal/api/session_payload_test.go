package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/internal/config"
	"github.com/teacat99/RelayMesh/internal/model"
	"github.com/teacat99/RelayMesh/internal/store"
)

// 1x1 透明 PNG，仅用于构造「带图片的会话」
const payloadTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

type fakeLiveSource struct {
	active    bool
	startedAt time.Time
	timeout   int
	keepalive *time.Time
}

func (f *fakeLiveSource) GetActiveWaiterInfo(string) (bool, *time.Time, int) {
	if !f.active {
		return false, nil, 0
	}
	started := f.startedAt
	return true, &started, f.timeout
}

func (f *fakeLiveSource) GetLastKeepaliveTime(string) *time.Time { return f.keepalive }

func TestSessionEventPayload_NilSession(t *testing.T) {
	if got := sessionEventPayload(nil, nil); got != nil {
		t.Fatalf("expected nil payload for nil session, got %+v", got)
	}
}

func TestSessionEventPayload_DoesNotMutateSource(t *testing.T) {
	src := &model.FeedbackSession{
		ID:     "sess-1",
		Status: "completed",
		Images: model.SessionImages{{Name: "a.png", Format: "png", Data: payloadTestPNG}},
	}

	got := sessionEventPayload(nil, src)

	if got == src {
		t.Fatalf("payload must be a copy, not the source pointer")
	}
	if src.Images[0].Data != payloadTestPNG {
		t.Fatalf("source image data must stay intact, got %q", src.Images[0].Data)
	}
	if src.Images[0].URL != "" || src.Images[0].Hash != "" {
		t.Fatalf("source image must not be projected: %+v", src.Images[0])
	}
	if got.Images[0].Data != "" {
		t.Fatalf("payload must strip base64 data, got %d bytes", len(got.Images[0].Data))
	}
	if got.Images[0].URL == "" || got.Images[0].Hash == "" {
		t.Fatalf("payload image must carry hash and url reference: %+v", got.Images[0])
	}
}

func TestSessionEventPayload_LiveFields(t *testing.T) {
	started := time.Now().Add(-90 * time.Second)
	kept := time.Now().Add(-10 * time.Second)

	t.Run("active waiter is reflected", func(t *testing.T) {
		live := &fakeLiveSource{active: true, startedAt: started, timeout: 300, keepalive: &kept}
		got := sessionEventPayload(live, &model.FeedbackSession{ID: "sess-live", Status: "pending"})
		if !got.IsMCPActive || got.MCPActiveAt == nil || !got.MCPActiveAt.Equal(started) || got.MCPTimeoutSec != 300 {
			t.Fatalf("active waiter fields missing: %+v", got)
		}
		if got.LastKeepaliveAt == nil || !got.LastKeepaliveAt.Equal(kept) {
			t.Fatalf("last keepalive missing: %+v", got.LastKeepaliveAt)
		}
	})

	t.Run("stale live values on the input never leak into the payload", func(t *testing.T) {
		stale := time.Now().Add(-time.Hour)
		src := &model.FeedbackSession{
			ID: "sess-stale", Status: "pending",
			IsMCPActive: true, MCPActiveAt: &stale, MCPTimeoutSec: 99, LastKeepaliveAt: &stale,
		}
		got := sessionEventPayload(&fakeLiveSource{}, src)
		if got.IsMCPActive || got.MCPActiveAt != nil || got.MCPTimeoutSec != 0 || got.LastKeepaliveAt != nil {
			t.Fatalf("inactive session must not keep stale live fields: %+v", got)
		}
		if !src.IsMCPActive || src.MCPTimeoutSec != 99 {
			t.Fatalf("source must not be modified: %+v", src)
		}
	})

	t.Run("nil live source yields zero live fields", func(t *testing.T) {
		got := sessionEventPayload(nil, &model.FeedbackSession{ID: "sess-none", IsMCPActive: true})
		if got.IsMCPActive {
			t.Fatalf("no live source must not report an active waiter")
		}
	})
}

// subscribeBroker 订阅 SSE 代理并返回接收通道，用于断言广播载荷。
func subscribeBroker(t *testing.T, b *SSEBroker) chan string {
	t.Helper()
	ch := make(chan string, 64)
	b.clientsMu.Lock()
	b.clients[ch] = true
	b.clientsMu.Unlock()
	t.Cleanup(func() {
		b.clientsMu.Lock()
		delete(b.clients, ch)
		b.clientsMu.Unlock()
	})
	return ch
}

// nextEvent 读取指定类型的下一个 SSE 事件并解析 data 为通用 map；超时失败。
func nextEvent(t *testing.T, ch chan string, eventType string) map[string]any {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-ch:
			if !strings.HasPrefix(msg, "event: "+eventType+"\n") {
				continue
			}
			idx := strings.Index(msg, "data: ")
			if idx < 0 {
				t.Fatalf("malformed SSE message: %q", msg)
			}
			var out map[string]any
			raw := strings.TrimSpace(msg[idx+len("data: "):])
			if err := json.Unmarshal([]byte(raw), &out); err != nil {
				t.Fatalf("invalid event data %q: %v", raw, err)
			}
			return out
		case <-deadline:
			t.Fatalf("timed out waiting for %q event", eventType)
		}
	}
}

func firstImage(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	images, ok := payload["images"].([]any)
	if !ok || len(images) == 0 {
		t.Fatalf("payload has no images: %+v", payload["images"])
	}
	img, _ := images[0].(map[string]any)
	return img
}

// REST 会话事件（重命名）需要携带实时字段，且不得把图片 Base64 推给浏览器。
func TestSessionBroadcast_RESTEventsCarryNormalizedPayload(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	ch := subscribeBroker(t, srv.handler.broker)

	sess, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{WorkflowID: "wf-payload", Title: "T", Summary: "S"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := st.SubmitFeedback(ctx, store.SubmitFeedbackInput{
		SessionID:    sess.ID,
		ResponseText: "with image",
		Images:       []model.SessionImage{{Name: "p.png", Format: "png", Data: payloadTestPNG}},
	}); err != nil {
		t.Fatalf("submit feedback: %v", err)
	}
	srv.mcpServer.RecordKeepaliveResponse(sess.ID)

	body := bytes.NewBufferString(`{"title":"Renamed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/rename", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("rename failed: %d %s", w.Code, w.Body.String())
	}

	payload := nextEvent(t, ch, "session_updated")
	if payload["title"] != "Renamed" {
		t.Fatalf("unexpected title in payload: %v", payload["title"])
	}
	if payload["last_keepalive_at"] == nil {
		t.Fatalf("payload must carry last_keepalive_at, got %+v", payload)
	}
	img := firstImage(t, payload)
	if _, hasData := img["data"]; hasData {
		t.Fatalf("payload image must not carry base64 data: %+v", img)
	}
	if url, _ := img["url"].(string); url == "" {
		t.Fatalf("payload image must carry a url reference: %+v", img)
	}
}

// 撤回 / 保活 / 取消 / 归档这些此前直接广播原始模型的事件，同样必须带实时字段。
func TestSessionBroadcast_FormerlyRawEventsCarryLiveFields(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	ch := subscribeBroker(t, srv.handler.broker)

	sess, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{WorkflowID: "wf-live", Title: "T", Summary: "S"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	srv.mcpServer.RecordKeepaliveResponse(sess.ID)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/keepalive", nil)
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("keepalive failed: %d %s", w.Code, w.Body.String())
	}
	payload := nextEvent(t, ch, "session_keepalive")
	if payload["last_keepalive_at"] == nil {
		t.Fatalf("session_keepalive payload must carry last_keepalive_at: %+v", payload)
	}
	if _, ok := payload["is_mcp_active"]; !ok {
		t.Fatalf("session_keepalive payload must carry is_mcp_active: %+v", payload)
	}

	// 提交后撤回：session_updated 载荷必须是撤回后的权威状态（pending、无回复、无图片）
	submit := bytes.NewBufferString(`{"response_text":"hello"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/submit", submit)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("submit failed: %d %s", w.Code, w.Body.String())
	}
	completed := nextEvent(t, ch, "session_completed")
	if completed["status"] != "completed" || completed["response_text"] != "hello" {
		t.Fatalf("unexpected session_completed payload: %+v", completed)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/revoke", nil)
	w = httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke failed: %d %s", w.Code, w.Body.String())
	}
	revoked := nextEvent(t, ch, "session_updated")
	if revoked["status"] != "pending" || revoked["response_text"] != "" {
		t.Fatalf("unexpected revoke payload: %+v", revoked)
	}
	if revoked["consumed_by_ai"] != false {
		t.Fatalf("revoked session must not be consumed: %+v", revoked)
	}
}

// MCP 侧消费反馈后广播的 session_updated 也必须经过同样的载荷规整（历史上这里会把用户截图 Base64 原样推给所有浏览器）。
func TestSessionBroadcast_MCPConsumeEventIsNormalized(t *testing.T) {
	cfg := &config.Config{
		ProjectID: "test-payload-proj",
		JWTSecret: "test-secret-payload",
		MCPToken:  "tok-payload",
	}
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer st.Close()
	srv := NewServer(cfg, st, nil)
	ch := subscribeBroker(t, srv.handler.broker)
	ctx := context.Background()

	sess, err := st.CreateFeedbackSession(ctx, store.CreateSessionInput{WorkflowID: "wf-mcp", Title: "T", Summary: "S"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := st.SubmitFeedback(ctx, store.SubmitFeedbackInput{
		SessionID:    sess.ID,
		ResponseText: "early reply",
		Images:       []model.SessionImage{{Name: "p.png", Format: "png", Data: payloadTestPNG}},
	}); err != nil {
		t.Fatalf("submit feedback: %v", err)
	}

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"continue_feedback_session","arguments":{"session_id":"` + sess.ID + `"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp?token=tok-payload", bytes.NewBufferString(call))
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mcp call failed: %d %s", w.Code, w.Body.String())
	}

	payload := nextEvent(t, ch, "session_updated")
	if payload["consumed_by_ai"] != true {
		t.Fatalf("expected consumed_by_ai=true in MCP broadcast: %+v", payload)
	}
	img := firstImage(t, payload)
	if _, hasData := img["data"]; hasData {
		t.Fatalf("MCP broadcast must not leak base64 image data: %+v", img)
	}
	if url, _ := img["url"].(string); url == "" {
		t.Fatalf("MCP broadcast image must carry a url reference: %+v", img)
	}
}
