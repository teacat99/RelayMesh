package mcp

import (
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 用户在输入框里粘贴图片时，界面会在文字里同步插入「【图 N】」标记（D-201）。
// 附件图片块必须明确告诉 AI：正文里的标记与下面列出的第 N 张图一一对应。
func TestFormatSessionImagesBlock_RefMarkerHint(t *testing.T) {
	srv := setupTestMCPServer(t)

	images := model.SessionImages{
		{Name: "a.png", Format: "png", Hash: "h1"},
		{Name: "b.png", Format: "png", Hash: "h2"},
	}

	t.Run("正文带有图片标记时输出对应提示，且标记写法与清单一致", func(t *testing.T) {
		sess := &model.FeedbackSession{
			ID:           "sess-ref-hint",
			ResponseText: "请对照【图 1】和【图 2】的差异",
			Images:       images,
		}
		block := srv.formatSessionImagesBlock(sess, nil, false)
		if !strings.Contains(block, "正文里的「【图 N】」标记指向下面列出的第 N 张图片") {
			t.Fatalf("正文带有「【图 N】」标记时应输出对应关系提示，实际：\n%s", block)
		}
		for _, want := range []string{"- 【图 1】(编号 #1): a.png", "- 【图 2】(编号 #2): b.png"} {
			if !strings.Contains(block, want) {
				t.Fatalf("清单应包含 %q，实际：\n%s", want, block)
			}
		}
		// 提示必须出现在清单之前，AI 先读到对应关系再读到清单
		if strings.Index(block, "标记指向") > strings.Index(block, "- 【图 1】") {
			t.Fatalf("对应关系提示应位于图片清单之前，实际：\n%s", block)
		}
	})

	t.Run("正文没有图片标记时不输出提示，避免无谓的噪声", func(t *testing.T) {
		sess := &model.FeedbackSession{
			ID:           "sess-no-ref",
			ResponseText: "看一下附件",
			Images:       images,
		}
		block := srv.formatSessionImagesBlock(sess, nil, false)
		if strings.Contains(block, "标记指向") {
			t.Fatalf("正文没有图片标记时不应输出提示，实际：\n%s", block)
		}
		if !strings.Contains(block, "=== 附件图片 (共 2 张) ===") {
			t.Fatalf("应保留附件标题，实际：\n%s", block)
		}
	})

	t.Run("没有图片时整块为空", func(t *testing.T) {
		sess := &model.FeedbackSession{ID: "sess-empty", ResponseText: "【图 1】"}
		if block := srv.formatSessionImagesBlock(sess, nil, false); block != "" {
			t.Fatalf("没有图片时应返回空串，实际：%q", block)
		}
	})
}
