package store

import (
	"context"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestDocLinksOutAndBack(t *testing.T) {
	st, _ := New(":memory:")
	ctx := context.Background()
	act := DocActor{Kind: model.DocActorAgent}
	mk := func(wf, p, body string) *model.ProjectDoc {
		r, err := st.CreateProjectDoc(ctx, ProjectDocInput{WorkflowID: wf, Path: p, Body: body}, act)
		if err != nil {
			t.Fatal(err)
		}
		return r.Doc
	}
	a := mk("wf1", "design/a.md", "# A\n见 [[design/b]] 与 [[missing/x]]")
	b := mk("wf1", "design/b.md", "# B\n回看 [A](./a.md)")
	mk("wf1", "notes/n.md", "提到 [[design/b]]")
	mk("wf2", "design/b.md", "# 另一个工作流的同名文档\n[[design/a]]")

	la, err := st.DocLinks(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(la.Outlinks) != 2 || !la.Outlinks[0].Exists || la.Outlinks[0].Path != "design/b.md" || la.Outlinks[1].Exists {
		t.Fatalf("outlinks wrong: %+v", la.Outlinks)
	}
	if len(la.Backlinks) != 1 || la.Backlinks[0].Path != "design/b.md" || la.Backlinks[0].WorkflowID != "wf1" {
		t.Fatalf("a backlinks should only be wf1 b (wf2 is a different workflow): %+v", la.Backlinks)
	}
	lb, _ := st.DocLinks(ctx, b)
	if len(lb.Backlinks) != 2 {
		t.Fatalf("b backlinks: %+v", lb.Backlinks)
	}
	if len(lb.Outlinks) != 1 || lb.Outlinks[0].ID != a.ID {
		t.Fatalf("b outlinks: %+v", lb.Outlinks)
	}
}
