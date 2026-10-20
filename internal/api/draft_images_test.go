package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/store"
)

// 草稿图片（云存储）：粘贴即上传、提交时复用、草稿销毁时删除。

func draftImagePath(wid, key string) string {
	return "/api/v1/workflows/" + wid + "/draft-images/" + key
}

func diReq(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body == nil {
		buf = bytes.NewBuffer(nil)
	} else if s, ok := body.(string); ok {
		buf = bytes.NewBufferString(s)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = bytes.NewBuffer(raw)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Engine().ServeHTTP(w, req)
	return w
}

func b64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func putDraftImage(t *testing.T, srv *Server, wid, key, content string) *httptest.ResponseRecorder {
	t.Helper()
	return diReq(t, srv, http.MethodPut, draftImagePath(wid, key), map[string]any{
		"name": key + ".png", "format": "png", "data_type": "base64", "hash": "h-" + key, "width": 3, "height": 2, "data": b64Of(content),
	})
}

func listDraftImageKeys(t *testing.T, srv *Server, wid string) []string {
	t.Helper()
	w := diReq(t, srv, http.MethodGet, "/api/v1/workflows/"+wid+"/draft-images", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list draft images: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Images []struct {
			Key  string `json:"key"`
			Size int    `json:"size"`
			Data string `json:"data"`
		} `json:"images"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	keys := make([]string, 0, len(resp.Images))
	for _, im := range resp.Images {
		if im.Data != "" {
			t.Fatalf("列表只能给元数据，不能带正文: %s", w.Body.String())
		}
		keys = append(keys, im.Key)
	}
	return keys
}

func TestAPI_DraftImages_RoundTrip(t *testing.T) {
	srv, _ := setupTestAPIServer(t)

	if w := putDraftImage(t, srv, "wf-a", "k1", "first image bytes"); w.Code != http.StatusOK {
		t.Fatalf("上传应当成功: %d %s", w.Code, w.Body.String())
	}
	// 幂等：同一个内容键重复上传仍然只有一份
	if w := putDraftImage(t, srv, "wf-a", "k1", "first image bytes"); w.Code != http.StatusOK {
		t.Fatalf("重复上传应当成功: %d %s", w.Code, w.Body.String())
	}
	if keys := listDraftImageKeys(t, srv, "wf-a"); len(keys) != 1 || keys[0] != "k1" {
		t.Fatalf("重复上传后应只有一张: %v", keys)
	}
	// 不同工作流互不影响
	if keys := listDraftImageKeys(t, srv, "wf-b"); len(keys) != 0 {
		t.Fatalf("别的工作流不应看到这张图: %v", keys)
	}

	w := diReq(t, srv, http.MethodGet, draftImagePath("wf-a", "k1"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("读取失败: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Image struct {
			Key    string `json:"key"`
			Name   string `json:"name"`
			Format string `json:"format"`
			Hash   string `json:"hash"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Data   string `json:"data"`
		} `json:"image"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Image.Data != b64Of("first image bytes") || got.Image.Name != "k1.png" || got.Image.Format != "png" || got.Image.Hash != "h-k1" || got.Image.Width != 3 || got.Image.Height != 2 {
		t.Fatalf("读回的内容不对: %+v", got.Image)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("草稿图片是私有内容，不应被缓存: %q", cc)
	}

	if w := diReq(t, srv, http.MethodDelete, draftImagePath("wf-a", "k1"), nil); w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d", w.Code)
	}
	if w := diReq(t, srv, http.MethodGet, draftImagePath("wf-a", "k1"), nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "draft_image_not_found") {
		t.Fatalf("删除后应当 404: %d %s", w.Code, w.Body.String())
	}
	// 删除不存在的图片也算成功（客户端的清理是尽力而为，可以重复）
	if w := diReq(t, srv, http.MethodDelete, draftImagePath("wf-a", "k1"), nil); w.Code != http.StatusOK {
		t.Fatalf("重复删除应当成功: %d", w.Code)
	}
}

func TestAPI_DraftImages_Validation(t *testing.T) {
	srv, _ := setupTestAPIServer(t)

	cases := []struct {
		name string
		path string
		body any
		want int
	}{
		{"键里有空格", draftImagePath("wf-a", "bad%20key"), map[string]any{"data": b64Of("x")}, http.StatusBadRequest},
		{"键里有点", draftImagePath("wf-a", "a.b"), map[string]any{"data": b64Of("x")}, http.StatusBadRequest},
		{"键太长", draftImagePath("wf-a", strings.Repeat("a", 97)), map[string]any{"data": b64Of("x")}, http.StatusBadRequest},
		{"内容为空", draftImagePath("wf-a", "k"), map[string]any{"data": ""}, http.StatusBadRequest},
		{"不是 Base64", draftImagePath("wf-a", "k"), map[string]any{"data": "!!!not base64!!!"}, http.StatusBadRequest},
		{"请求体不是 JSON", draftImagePath("wf-a", "k"), "not json", http.StatusBadRequest},
		{"工作流标识太长", draftImagePath(strings.Repeat("w", 129), "k"), map[string]any{"data": b64Of("x")}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if w := diReq(t, srv, http.MethodPut, tc.path, tc.body); w.Code != tc.want {
			t.Errorf("%s: 期望 %d，实际 %d（%s）", tc.name, tc.want, w.Code, w.Body.String())
		}
	}
	if keys := listDraftImageKeys(t, srv, "wf-a"); len(keys) != 0 {
		t.Fatalf("被拒绝的上传不应留下任何内容: %v", keys)
	}

	// data URL 前缀会被剥掉；未知格式被清空而不是原样存下
	w := diReq(t, srv, http.MethodPut, draftImagePath("wf-a", "dataurl"), map[string]any{
		"format": "PNG; drop table", "data": "data:image/png;base64," + b64Of("with prefix"),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("带 data URL 前缀的上传应当成功: %d %s", w.Code, w.Body.String())
	}
	g := diReq(t, srv, http.MethodGet, draftImagePath("wf-a", "dataurl"), nil)
	if !strings.Contains(g.Body.String(), `"data":"`+b64Of("with prefix")+`"`) {
		t.Fatalf("前缀应被剥掉: %s", g.Body.String())
	}
	if strings.Contains(g.Body.String(), "drop table") {
		t.Fatalf("不合法的格式不能原样保存: %s", g.Body.String())
	}
	// 没有哈希时服务端补一个
	if !strings.Contains(g.Body.String(), `"hash":"`) {
		t.Fatalf("没传哈希时应由服务端补上: %s", g.Body.String())
	}
}

func TestAPI_DraftImages_QuotaPerWorkflow(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	for i := 0; i < store.MaxDraftImagesPerWorkflow; i++ {
		if err := st.PutDraftImage(ctx, model.DraftImage{WorkflowID: "wf-q", Key: fmt.Sprintf("k%d", i), Data: "QQ=="}); err != nil {
			t.Fatalf("预置第 %d 张失败: %v", i, err)
		}
	}
	w := putDraftImage(t, srv, "wf-q", "one-too-many", "x")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "draft_image_rejected") {
		t.Fatalf("超过张数上限应被拒绝: %d %s", w.Code, w.Body.String())
	}
	// 已经存在的键重新确认不受张数上限影响
	if w := putDraftImage(t, srv, "wf-q", "k0", "x"); w.Code != http.StatusOK {
		t.Fatalf("重新确认已有的图片应当成功: %d %s", w.Code, w.Body.String())
	}
	// 别的工作流不受影响
	if w := putDraftImage(t, srv, "wf-other", "k0", "x"); w.Code != http.StatusOK {
		t.Fatalf("别的工作流应当成功: %d %s", w.Code, w.Body.String())
	}
}

func newPendingSession(t *testing.T, st *store.Store, workflowID string) *model.FeedbackSession {
	t.Helper()
	sess, err := st.CreateFeedbackSession(context.Background(), store.CreateSessionInput{
		WorkflowID: workflowID, ProjectDirectory: "/p", Title: "t", Summary: "s", TimeoutSeconds: 600,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func TestAPI_DraftImages_SubmitReusesUploadedImage(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	sess := newPendingSession(t, st, "wf-s")
	putDraftImage(t, srv, "wf-s", "kA", "image A")
	putDraftImage(t, srv, "wf-s", "kB", "image B")
	putDraftImage(t, srv, "wf-s", "kKeep", "stays in the draft")

	w := diReq(t, srv, http.MethodPost, "/api/v1/sessions/"+sess.ID+"/submit", map[string]any{
		"response_text": "see images",
		"images": []map[string]any{
			{"draft_key": "kA", "name": "a-renamed.png"},
			{"name": "inline.png", "format": "png", "data": b64Of("inline body")},
			// 同时带正文与 draft_key：以自带的正文为准，但这个键同样算已复用
			{"draft_key": "kB", "name": "b.png", "data": b64Of("explicit body wins")},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("提交应当成功: %d %s", w.Code, w.Body.String())
	}
	got, err := st.GetFeedbackSession(ctx, sess.ID)
	if err != nil || got == nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if len(got.Images) != 3 {
		t.Fatalf("会话应有 3 张图片: %+v", got.Images)
	}
	if got.Images[0].Data != b64Of("image A") || got.Images[0].Name != "a-renamed.png" || got.Images[0].Format != "png" || got.Images[0].Hash == "" || got.Images[0].Width != 3 {
		t.Fatalf("复用的图片应带上草稿图片的正文与元数据（请求里给的字段优先）: %+v", got.Images[0])
	}
	if got.Images[1].Data != b64Of("inline body") {
		t.Fatalf("自带正文的图片应原样保留: %+v", got.Images[1])
	}
	if got.Images[2].Data != b64Of("explicit body wins") {
		t.Fatalf("同时带正文与键时以正文为准: %+v", got.Images[2])
	}
	for i, im := range got.Images {
		if im.Hash == "" {
			t.Fatalf("第 %d 张图片缺少哈希", i)
		}
	}
	// 被复用的草稿图片已删除，其余保留
	if keys := listDraftImageKeys(t, srv, "wf-s"); len(keys) != 1 || keys[0] != "kKeep" {
		t.Fatalf("提交后只应剩下没被引用的草稿图片: %v", keys)
	}
	// 会话里的图片能照常访问（不依赖草稿图片）
	iw := diReq(t, srv, http.MethodGet, "/api/v1/sessions/"+sess.ID+"/images/0", nil)
	if iw.Code != http.StatusOK || iw.Body.String() != "image A" {
		t.Fatalf("会话图片应能直接读取: %d %q", iw.Code, iw.Body.String())
	}
}

func TestAPI_DraftImages_SubmitMissingKeyChangesNothing(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	sess := newPendingSession(t, st, "wf-m")
	putDraftImage(t, srv, "wf-m", "present", "here")

	w := diReq(t, srv, http.MethodPost, "/api/v1/sessions/"+sess.ID+"/submit", map[string]any{
		"response_text": "x",
		"images":        []map[string]any{{"draft_key": "present"}, {"draft_key": "gone"}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺图时应当 400: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string   `json:"code"`
		Keys []string `json:"keys"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != "draft_image_missing" || len(resp.Keys) != 1 || resp.Keys[0] != "gone" {
		t.Fatalf("应给出机器可读的缺失信息（客户端据此改用自带正文重试）: %s", w.Body.String())
	}
	got, _ := st.GetFeedbackSession(ctx, sess.ID)
	if got.Status != "pending" {
		t.Fatalf("提交失败不能改变会话状态: %s", got.Status)
	}
	if keys := listDraftImageKeys(t, srv, "wf-m"); len(keys) != 1 || keys[0] != "present" {
		t.Fatalf("提交失败不能消耗草稿图片: %v", keys)
	}
}

func TestAPI_DraftImages_SubmitDraftWorkflowOverride(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	sess := newPendingSession(t, st, "wf-real")
	// 草稿绑定在另一个标识下（例如旧数据里工作流标识为空，草稿用会话 ID 当键）
	putDraftImage(t, srv, "draft-bound", "k", "from another binding")

	w := diReq(t, srv, http.MethodPost, "/api/v1/sessions/"+sess.ID+"/submit", map[string]any{
		"response_text":     "x",
		"draft_workflow_id": "draft-bound",
		"images":            []map[string]any{{"draft_key": "k"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("指定草稿所在工作流后应当成功: %d %s", w.Code, w.Body.String())
	}
	got, _ := st.GetFeedbackSession(ctx, sess.ID)
	if len(got.Images) != 1 || got.Images[0].Data != b64Of("from another binding") {
		t.Fatalf("应取到指定工作流里的草稿图片: %+v", got.Images)
	}
	if keys := listDraftImageKeys(t, srv, "draft-bound"); len(keys) != 0 {
		t.Fatalf("复用后应删除: %v", keys)
	}
}

func TestAPI_DraftImages_AppendToPendingAndQueue(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()

	// 没有进行中的会话：暂存到队列，图片也要带正文进去
	putDraftImage(t, srv, "wf-q1", "kq", "queued image")
	w := diReq(t, srv, http.MethodPost, "/api/v1/workflows/wf-q1/append", map[string]any{
		"response_text": "later", "images": []map[string]any{{"draft_key": "kq", "name": "q.png"}},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"type":"queued"`) {
		t.Fatalf("应当入队: %d %s", w.Code, w.Body.String())
	}
	queued, err := st.ListPendingQueuedFeedbacks(ctx, "wf-q1")
	if err != nil || len(queued) != 1 || len(queued[0].Images) != 1 || queued[0].Images[0].Data != b64Of("queued image") || queued[0].Images[0].Hash == "" {
		t.Fatalf("队列里的反馈应带上图片正文与哈希: %+v err=%v", queued, err)
	}
	if keys := listDraftImageKeys(t, srv, "wf-q1"); len(keys) != 0 {
		t.Fatalf("入队后草稿图片应已删除: %v", keys)
	}

	// 有进行中的会话：直接并入会话
	sess := newPendingSession(t, st, "wf-q2")
	putDraftImage(t, srv, "wf-q2", "kp", "pending image")
	w = diReq(t, srv, http.MethodPost, "/api/v1/workflows/wf-q2/append", map[string]any{
		"response_text": "now", "images": []map[string]any{{"draft_key": "kp"}},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"type":"session"`) {
		t.Fatalf("应并入会话: %d %s", w.Code, w.Body.String())
	}
	got, _ := st.GetFeedbackSession(ctx, sess.ID)
	if len(got.Images) != 1 || got.Images[0].Data != b64Of("pending image") {
		t.Fatalf("会话应带上图片正文: %+v", got.Images)
	}

	// 缺图：两条路径都不能静默丢图
	w = diReq(t, srv, http.MethodPost, "/api/v1/workflows/wf-q3/append", map[string]any{
		"response_text": "x", "images": []map[string]any{{"draft_key": "nope"}},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "draft_image_missing") {
		t.Fatalf("入队路径缺图也应报 draft_image_missing: %d %s", w.Code, w.Body.String())
	}
	if q, _ := st.ListPendingQueuedFeedbacks(ctx, "wf-q3"); len(q) != 0 {
		t.Fatalf("缺图时不能留下半成品反馈: %+v", q)
	}
}

func TestAPI_DraftImages_DeletingTheDraftDeletesItsImages(t *testing.T) {
	srv, _ := setupTestAPIServer(t)
	putDraftImage(t, srv, "wf-d", "k1", "one")
	putDraftImage(t, srv, "wf-d", "k2", "two")
	putDraftImage(t, srv, "wf-other", "k1", "other workflow")
	if w := diReq(t, srv, http.MethodPut, "/api/v1/workflows/wf-d/drafts", map[string]any{"active_index": 0, "drafts_json": `{"activeIndex":0,"drafts":[{"id":"1","images":[{"key":"k1"},{"key":"k2"}]}]}`}); w.Code != http.StatusOK {
		t.Fatalf("保存草稿失败: %d", w.Code)
	}
	if w := diReq(t, srv, http.MethodDelete, "/api/v1/workflows/wf-d/drafts", nil); w.Code != http.StatusOK {
		t.Fatalf("删除草稿失败: %d", w.Code)
	}
	if keys := listDraftImageKeys(t, srv, "wf-d"); len(keys) != 0 {
		t.Fatalf("草稿销毁后其图片应一并删除: %v", keys)
	}
	if keys := listDraftImageKeys(t, srv, "wf-other"); len(keys) != 1 {
		t.Fatalf("别的工作流的图片不受影响: %v", keys)
	}
}

func TestAPI_DraftImages_DeletingTheLastSessionCleansUp(t *testing.T) {
	srv, st := setupTestAPIServer(t)
	ctx := context.Background()
	sess := newPendingSession(t, st, "wf-gone")
	putDraftImage(t, srv, "wf-gone", "k", "leftover")
	if _, err := st.SaveWorkflowDraft(ctx, "wf-gone", 0, `{"drafts":[{"images":[{"key":"k"}]}]}`); err != nil {
		t.Fatalf("保存草稿失败: %v", err)
	}
	if _, err := st.DeleteFeedbackSession(ctx, sess.ID); err != nil {
		t.Fatalf("删除会话失败: %v", err)
	}
	if keys := listDraftImageKeys(t, srv, "wf-gone"); len(keys) != 0 {
		t.Fatalf("工作流清空后草稿图片应一并清理: %v", keys)
	}
}
