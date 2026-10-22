package store

import (
	"context"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 列表只要元数据时（OmitBody）不读正文列；其它列（标题 / 标签 / 版本）照常；有 Query 时仍读正文来打分（#177）。
func TestListProjectDocs_OmitBody(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()
	actor := DocActor{Kind: model.DocActorHuman}
	mk := func(path, title, body string, tags []string) {
		t.Helper()
		if _, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: "wf-omit", Path: path, Title: title, Body: body, Tags: tags}, actor); err != nil {
			t.Fatalf("创建 %s 失败: %v", path, err)
		}
	}
	mk("design/a.md", "方案甲", "# 方案甲\n"+strings.Repeat("正文 ", 200)+"独特关键词Zeta", []string{"alpha", "beta"})
	mk("notes/b.md", "笔记乙", "# 笔记乙\n普通内容", nil)

	full, err := st.ListProjectDocs(ctx, ProjectDocFilter{WorkflowID: "wf-omit"})
	if err != nil || len(full) != 2 {
		t.Fatalf("基线列表应有 2 篇，得到 %d（err=%v）", len(full), err)
	}
	for _, d := range full {
		if d.Body == "" {
			t.Fatalf("不带 OmitBody 时应读出正文：%s", d.Path)
		}
	}

	light, err := st.ListProjectDocs(ctx, ProjectDocFilter{WorkflowID: "wf-omit", OmitBody: true})
	if err != nil || len(light) != 2 {
		t.Fatalf("OmitBody 列表应有 2 篇，得到 %d（err=%v）", len(light), err)
	}
	byPath := map[string]model.ProjectDoc{}
	for _, d := range light {
		byPath[d.Path] = d
		if d.Body != "" {
			t.Errorf("OmitBody 时不应带正文：%s（%d 字节）", d.Path, len(d.Body))
		}
	}
	a := byPath["design/a.md"]
	if a.Title != "方案甲" || a.Rev < 1 || len(a.Tags) != 2 || a.Tags[0] != "alpha" {
		t.Errorf("OmitBody 不该影响其它列：title=%q rev=%d tags=%v", a.Title, a.Rev, a.Tags)
	}
	if a.ID == 0 || a.WorkflowID != "wf-omit" || a.Status == "" || a.Kind == "" {
		t.Errorf("元数据列应齐全：%+v", a)
	}

	// 带 Query：打分要读正文，所以哪怕 OmitBody 也要能搜到「只出现在正文里」的词。
	hit, err := st.ListProjectDocs(ctx, ProjectDocFilter{WorkflowID: "wf-omit", Query: "独特关键词zeta", OmitBody: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hit) != 1 || hit[0].Path != "design/a.md" {
		t.Fatalf("带 Query 时应仍按正文命中：%+v", hit)
	}
}
