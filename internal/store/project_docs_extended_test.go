package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/docs"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestProjectDocConcurrentSaveOneWins(t *testing.T) {
	st, _ := New(":memory:")
	ctx := context.Background()
	wf := "wf-conc"
	res, err := st.CreateProjectDoc(ctx, ProjectDocInput{
		WorkflowID: wf, Path: "a.md", Body: "v0",
	}, DocActor{Kind: model.DocActorHuman})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	bodies := []string{"v1", "v2"}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := bodies[i]
			_, errs[i] = st.UpdateProjectDoc(ctx, res.Doc.ID, ProjectDocPatch{
				Body: &b, ExpectedRev: 1, Summary: "race",
			}, DocActor{Kind: model.DocActorHuman})
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, e := range errs {
		if e == nil {
			wins++
		} else if !strings.Contains(e.Error(), "版本冲突") {
			t.Fatalf("unexpected: %v", e)
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one win, got %d errs=%v", wins, errs)
	}
}

func TestProjectDocRevisionTrimKeepsCurrent(t *testing.T) {
	st, _ := New(":memory:")
	ctx := context.Background()
	wf := "wf-rev"
	res, err := st.CreateProjectDoc(ctx, ProjectDocInput{
		WorkflowID: wf, Path: "a.md", Body: "start",
	}, DocActor{Kind: model.DocActorHuman})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < docs.RevisionKeep+5; i++ {
		b := strings.Repeat("x", i+1)
		_, err = st.UpdateProjectDoc(ctx, res.Doc.ID, ProjectDocPatch{Body: &b}, DocActor{Kind: model.DocActorHuman})
		if err != nil {
			t.Fatal(err)
		}
	}
	cur, err := st.GetProjectDocByID(ctx, res.Doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	revs, err := st.ListProjectDocRevisions(ctx, res.Doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) > docs.RevisionKeep {
		t.Fatalf("trim: %d revisions", len(revs))
	}
	found := false
	for _, r := range revs {
		if r.Rev == cur.Rev && r.Body == cur.Body {
			found = true
		}
	}
	if !found {
		t.Fatal("current rev missing from history")
	}
}

func TestProjectDocBodyTooLarge(t *testing.T) {
	st, _ := New(":memory:")
	big := strings.Repeat("a", docs.BodyMaxBytes+1)
	_, err := st.CreateProjectDoc(context.Background(), ProjectDocInput{
		WorkflowID: "w", Path: "big.md", Body: big,
	}, DocActor{Kind: model.DocActorHuman})
	if err == nil {
		t.Fatal("expected body limit")
	}
}

func TestProjectDocImportBatchLimits(t *testing.T) {
	st, _ := New(":memory:")
	ctx := context.Background()
	var files []ImportDocFile
	for i := 0; i < docs.ImportMaxFiles+1; i++ {
		files = append(files, ImportDocFile{Path: fmt.Sprintf("batch/%d.md", i), Content: "c"})
	}
	_, err := st.ImportProjectDocs(ctx, "w", files, "create", false, DocActor{Kind: model.DocActorImport})
	if err == nil {
		t.Fatal("expected import file count limit")
	}
}

func TestProjectDocPathControlChars(t *testing.T) {
	st, _ := New(":memory:")
	_, err := st.CreateProjectDoc(context.Background(), ProjectDocInput{
		WorkflowID: "w", Path: "bad\x00.md", Body: "x",
	}, DocActor{Kind: model.DocActorHuman})
	if err == nil {
		t.Fatal("control char path")
	}
}

func TestProjectDocSectionReplaceViaStore(t *testing.T) {
	st, _ := New(":memory:")
	ctx := context.Background()
	body := "# T\n\n## Sec\nold\n"
	res, err := st.CreateProjectDoc(ctx, ProjectDocInput{
		WorkflowID: "w", Path: "s.md", Body: body,
	}, DocActor{Kind: model.DocActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	nb, err := docs.ReplaceSection(body, "Sec", "## Sec\nnew")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateProjectDoc(ctx, res.Doc.ID, ProjectDocPatch{Body: &nb}, DocActor{Kind: model.DocActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetProjectDocByID(ctx, res.Doc.ID)
	if !strings.Contains(got.Body, "new") {
		t.Fatalf("%q", got.Body)
	}
}
