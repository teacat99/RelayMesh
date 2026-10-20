package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func putImg(t *testing.T, s *Store, wid, key, data string) {
	t.Helper()
	if err := s.PutDraftImage(context.Background(), model.DraftImage{WorkflowID: wid, Key: key, Name: key + ".png", Format: "png", Data: data}); err != nil {
		t.Fatalf("PutDraftImage(%s,%s): %v", wid, key, err)
	}
}

func ageDraftImage(t *testing.T, s *Store, wid, key string, age time.Duration) {
	t.Helper()
	if err := s.DB().Model(&model.DraftImage{}).Where("workflow_id = ? AND key = ?", wid, key).UpdateColumn("updated_at", time.Now().Add(-age)).Error; err != nil {
		t.Fatalf("age image: %v", err)
	}
}

func imageKeys(t *testing.T, s *Store, wid string) []string {
	t.Helper()
	list, err := s.ListDraftImages(context.Background(), wid)
	if err != nil {
		t.Fatalf("ListDraftImages: %v", err)
	}
	keys := make([]string, 0, len(list))
	for _, i := range list {
		keys = append(keys, i.Key)
	}
	return keys
}

func TestStore_DraftImage_PutIsIdempotentAndRefreshesTime(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()
	putImg(t, s, "wf", "k", "AAAA")
	ageDraftImage(t, s, "wf", "k", 48*time.Hour)
	putImg(t, s, "wf", "k", "AAAA")

	list, _ := s.ListDraftImages(ctx, "wf")
	if len(list) != 1 {
		t.Fatalf("同一个键只应有一条: %+v", list)
	}
	if time.Since(list[0].UpdatedAt) > time.Minute {
		t.Fatalf("重新确认应刷新写入时间: %v", list[0].UpdatedAt)
	}
	if list[0].Size != 4 {
		t.Fatalf("Size 应是正文长度: %+v", list[0])
	}
	got, err := s.GetDraftImage(ctx, "wf", "k")
	if err != nil || got == nil || got.Data != "AAAA" {
		t.Fatalf("读回失败: %+v err=%v", got, err)
	}
	if none, err := s.GetDraftImage(ctx, "wf", "missing"); err != nil || none != nil {
		t.Fatalf("不存在应返回 nil, nil: %+v err=%v", none, err)
	}
}

func TestStore_DraftImage_Validation(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()
	for name, img := range map[string]model.DraftImage{
		"没有工作流":  {Key: "k", Data: "AA"},
		"没有键":    {WorkflowID: "wf", Data: "AA"},
		"没有内容":   {WorkflowID: "wf", Key: "k"},
		"超过单张上限": {WorkflowID: "wf", Key: "k", Data: strings.Repeat("A", MaxDraftImageChars+1)},
	} {
		err := s.PutDraftImage(ctx, img)
		if err == nil {
			t.Errorf("%s：应当被拒绝", name)
		} else if ae, ok := err.(*AppError); !ok || ae.Kind != ErrInvalidInput {
			t.Errorf("%s：应是 invalid_input，实际 %T %v", name, err, err)
		}
	}
	if keys := imageKeys(t, s, "wf"); len(keys) != 0 {
		t.Fatalf("被拒绝的上传不应留下内容: %v", keys)
	}
}

func TestStore_DraftImage_QuotaCountAndTotal(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()
	for i := 0; i < MaxDraftImagesPerWorkflow; i++ {
		putImg(t, s, "wf", fmt.Sprintf("k%d", i), "AA")
	}
	if err := s.PutDraftImage(ctx, model.DraftImage{WorkflowID: "wf", Key: "extra", Data: "AA"}); err == nil {
		t.Fatal("超过张数上限应被拒绝")
	}
	// 覆盖已有的键不占新名额
	putImg(t, s, "wf", "k0", "BB")

	// 总量上限：用大图撑到上限边缘
	chunk := strings.Repeat("A", MaxDraftImageChars)
	for i := 0; i < MaxDraftImageCharsPerWorkflow/MaxDraftImageChars; i++ {
		if err := s.PutDraftImage(ctx, model.DraftImage{WorkflowID: "wf-big", Key: fmt.Sprintf("b%d", i), Data: chunk}); err != nil {
			t.Fatalf("总量未超限时应当成功（第 %d 张）: %v", i, err)
		}
	}
	if err := s.PutDraftImage(ctx, model.DraftImage{WorkflowID: "wf-big", Key: "over", Data: "AA"}); err == nil {
		t.Fatal("总量超过上限应被拒绝")
	}
	// 同一个键换成更大的正文时，旧正文不重复计入
	if err := s.PutDraftImage(ctx, model.DraftImage{WorkflowID: "wf-big", Key: "b0", Data: chunk}); err != nil {
		t.Fatalf("覆盖已有的键不应因为总量被拒绝: %v", err)
	}
}

func TestStore_DraftImage_DeleteAndResolve(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()
	putImg(t, s, "wf", "a", "AA")
	putImg(t, s, "wf", "b", "BB")
	putImg(t, s, "other", "a", "CC")

	rows, err := s.ResolveDraftImages(ctx, "wf", []string{"a", "b", "missing"})
	if err != nil || len(rows) != 2 || rows["a"].Data != "AA" || rows["b"].Data != "BB" {
		t.Fatalf("按键取回失败: %+v err=%v", rows, err)
	}
	if n, err := s.DeleteDraftImages(ctx, "wf", []string{"a", "nope"}); err != nil || n != 1 {
		t.Fatalf("删除应只删存在的: n=%d err=%v", n, err)
	}
	if n, _ := s.DeleteDraftImages(ctx, "wf", nil); n != 0 {
		t.Fatalf("空列表不删任何东西: %d", n)
	}
	if keys := imageKeys(t, s, "wf"); len(keys) != 1 || keys[0] != "b" {
		t.Fatalf("剩余: %v", keys)
	}
	if keys := imageKeys(t, s, "other"); len(keys) != 1 {
		t.Fatalf("别的工作流不受影响: %v", keys)
	}
	if n, err := s.DeleteAllDraftImages(ctx, "wf"); err != nil || n != 1 {
		t.Fatalf("整体删除: n=%d err=%v", n, err)
	}
	if keys := imageKeys(t, s, "other"); len(keys) != 1 {
		t.Fatalf("整体删除不能波及别的工作流: %v", keys)
	}
}

func TestDraftReferencedKeys(t *testing.T) {
	refs := DraftReferencedKeys(`{"activeIndex":1,"drafts":[{"id":"1","images":[{"key":"a"},{"key":""},{"name":"no key"}]},{"id":"2","images":[{"key":"b"},{"key":"a"}]},{"id":"3"}]}`)
	if len(refs) != 2 {
		t.Fatalf("应只有 a、b: %v", refs)
	}
	for _, k := range []string{"a", "b"} {
		if _, ok := refs[k]; !ok {
			t.Fatalf("缺少 %s: %v", k, refs)
		}
	}
	for _, bad := range []string{"", "   ", "not json", `{"drafts":"oops"}`, `[]`} {
		if got := DraftReferencedKeys(bad); len(got) != 0 {
			t.Fatalf("%q 应按没有引用处理: %v", bad, got)
		}
	}
}

func TestStore_SweepDraftImages(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()
	day := 24 * time.Hour

	// wf-keep：草稿引用 kept，不引用 orphan；两张都已遗弃很久
	putImg(t, s, "wf-keep", "kept", "AA")
	putImg(t, s, "wf-keep", "orphan", "BB")
	putImg(t, s, "wf-keep", "fresh", "CC")
	if _, err := s.SaveWorkflowDraft(ctx, "wf-keep", 0, `{"drafts":[{"images":[{"key":"kept"}]}]}`); err != nil {
		t.Fatal(err)
	}
	ageDraftImage(t, s, "wf-keep", "kept", 3*day)
	ageDraftImage(t, s, "wf-keep", "orphan", 3*day)
	// wf-nodraft：根本没有草稿记录
	putImg(t, s, "wf-nodraft", "x", "DD")
	ageDraftImage(t, s, "wf-nodraft", "x", 3*day)

	n, err := s.SweepDraftImages(ctx, time.Now().Add(-day))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("应清理 orphan 与 wf-nodraft/x 共 2 张，实际 %d", n)
	}
	if keys := imageKeys(t, s, "wf-keep"); len(keys) != 2 {
		t.Fatalf("被草稿引用的、刚写入的都应保留: %v", keys)
	}
	got := map[string]bool{}
	for _, k := range imageKeys(t, s, "wf-keep") {
		got[k] = true
	}
	if !got["kept"] || !got["fresh"] || got["orphan"] {
		t.Fatalf("保留的不对: %v", got)
	}
	if keys := imageKeys(t, s, "wf-nodraft"); len(keys) != 0 {
		t.Fatalf("没有草稿记录的遗弃图片应被清理: %v", keys)
	}
	// 没有遗弃的东西时是空操作
	if n, err := s.SweepDraftImages(ctx, time.Now().Add(-day)); err != nil || n != 0 {
		t.Fatalf("再次清理应无事可做: n=%d err=%v", n, err)
	}
}
