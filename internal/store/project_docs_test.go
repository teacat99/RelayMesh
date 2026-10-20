package store

import (
	"context"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestProjectDocCreateUpdateConflict(t *testing.T) {
	st, _ := New(":memory:")
	ctx := context.Background()
	wf := "wf-docs-test"
	res, err := st.CreateProjectDoc(ctx, ProjectDocInput{
		WorkflowID: wf, Path: "design/a.md", Body: "# A\nhello",
	}, DocActor{Kind: model.DocActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	body := "# A\nworld"
	_, err = st.UpdateProjectDoc(ctx, res.Doc.ID, ProjectDocPatch{Body: &body, ExpectedRev: 99}, DocActor{Kind: model.DocActorHuman})
	if err == nil || !strings.Contains(err.Error(), "版本冲突") {
		t.Fatalf("expected conflict, got %v", err)
	}
	same := res.Doc.Body
	r2, err := st.UpdateProjectDoc(ctx, res.Doc.ID, ProjectDocPatch{Body: &same}, DocActor{Kind: model.DocActorHuman})
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Unchanged {
		t.Fatal("expected unchanged")
	}
}

func TestProjectDocImportDryRun(t *testing.T) {
	st, _ := New(":memory:")
	ctx := context.Background()
	wf := "wf-import"
	out, err := st.ImportProjectDocs(ctx, wf, []ImportDocFile{
		{Path: "a.md", Content: "one"},
		{Path: "b.md", Content: "two"},
	}, "upsert", true, DocActor{Kind: model.DocActorImport})
	if err != nil || out.Created != 2 {
		t.Fatalf("%v %+v", err, out)
	}
	n, _ := st.CountProjectDocs(ctx, wf)
	if n != 0 {
		t.Fatalf("dry run should not persist, got %d", n)
	}
}

func TestProjectDocPathReject(t *testing.T) {
	st, _ := New(":memory:")
	_, err := st.CreateProjectDoc(context.Background(), ProjectDocInput{
		WorkflowID: "w", Path: "../evil.md", Body: "x",
	}, DocActor{Kind: model.DocActorHuman})
	if err == nil {
		t.Fatal("expected path error")
	}
	_ = docs.NormalizePath
}
