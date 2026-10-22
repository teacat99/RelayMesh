package docs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"design/foo", "design/foo.md", false},
		{"  bar\\baz  ", "bar/baz.md", false},
		{"", "", true},
		{"../x", "", true},
		{"/abs", "", true},
		{"readme.MD", "readme.MD", false},
	}
	for _, c := range cases {
		got, err := NormalizePath(c.in)
		if c.err {
			if err == nil {
				t.Fatalf("NormalizePath(%q) expected error", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("NormalizePath(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestParseHeadingsSkipsFence(t *testing.T) {
	body := "# Real\n\n```\n# Fake\n```\n\n## Sub 标题"
	hs := ParseHeadings(body)
	if len(hs) != 2 {
		t.Fatalf("got %d headings", len(hs))
	}
	if hs[0].Anchor != "real" {
		t.Fatalf("anchor %q", hs[0].Anchor)
	}
	if hs[1].Anchor != "sub-标题" && hs[1].Anchor != "sub-" {
		// CJK preserved
		if !strings.Contains(hs[1].Anchor, "标题") {
			t.Fatalf("cjk anchor %q", hs[1].Anchor)
		}
	}
}

func TestSectionGetReplace(t *testing.T) {
	body := "# A\nline1\n\n## B\nb1\n\n## C\nc1"
	sec, ok := GetSection(body, "B")
	if !ok || !strings.Contains(sec, "b1") {
		t.Fatalf("GetSection B: %q %v", sec, ok)
	}
	newBody, err := ReplaceSection(body, "B", "## B\nupdated")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(newBody, "updated") || strings.Contains(newBody, "b1") {
		t.Fatalf("replace failed: %s", newBody)
	}
}

func TestParseDecisions(t *testing.T) {
	body := "text\n```decision\n{\"id\":\"s1\",\"title\":\"Q?\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"b\",\"label\":\"B\"}],\"default\":\"a\",\"risk\":\"L1\"}\n```\n"
	ds := ParseDecisions(body)
	if len(ds) != 1 || len(ds[0].Errors) > 0 {
		t.Fatalf("%+v", ds)
	}
	bad := ParseDecisions("```decision\n{}\n```")
	if len(bad[0].Errors) == 0 {
		t.Fatal("expected errors")
	}
}

func TestScoreDocument(t *testing.T) {
	score, snip, _ := ScoreDocument("oauth", "spec/oauth.md", "OAuth", "OpenAI OAuth flow", nil)
	if score < 4 || snip == "" {
		t.Fatalf("score=%v snip=%q", score, snip)
	}
}

func TestBlueprintGaps(t *testing.T) {
	items := Blueprint(map[string]bool{"README.md": true}, false)
	if CountBlueprintGaps(items) >= len(defaultBlueprint) {
		t.Fatal("expected some gaps filled")
	}
}

func TestParseDecisionsEdgeCases(t *testing.T) {
	unclosed := ParseDecisions("x\n```decision\n{\"id\":\"a\",\"title\":\"t\",\"options\":[{\"id\":\"1\",\"label\":\"1\"},{\"id\":\"2\",\"label\":\"2\"}],\"default\":\"1\"}\n")
	if len(unclosed) != 1 || len(unclosed[0].Errors) == 0 {
		t.Fatalf("unclosed fence: %+v", unclosed)
	}
	badJSON := ParseDecisions("```decision\nnot json\n```\n")
	if len(badJSON[0].Errors) == 0 {
		t.Fatal("bad json")
	}
	dup := ParseDecisions("```decision\n{\"id\":\"x\",\"title\":\"t\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"b\",\"label\":\"B\"}],\"default\":\"a\"}\n```\n```decision\n{\"id\":\"x\",\"title\":\"t2\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"b\",\"label\":\"B\"}],\"default\":\"a\"}\n```\n")
	if d := UniqueDecisionIDs(dup); len(d) == 0 {
		t.Fatal("expected duplicate id")
	}
	fewOpts := ParseDecisions("```decision\n{\"id\":\"f\",\"title\":\"t\",\"options\":[{\"id\":\"a\",\"label\":\"A\"}],\"default\":\"a\"}\n```\n")
	if len(fewOpts[0].Errors) == 0 {
		t.Fatal("need 2 options")
	}
	many := make([]map[string]any, 0, 9)
	for i := 0; i < 9; i++ {
		many = append(many, map[string]any{"id": fmt.Sprintf("o%d", i), "label": "L"})
	}
	b, _ := json.Marshal(map[string]any{"id": "m", "title": "t", "options": many, "default": "o0"})
	tooMany := ParseDecisions("```decision\n" + string(b) + "\n```\n")
	if len(tooMany[0].Errors) == 0 {
		t.Fatal("max 8 options")
	}
	badDef := ParseDecisions("```decision\n{\"id\":\"d\",\"title\":\"t\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"b\",\"label\":\"B\"}],\"default\":\"z\"}\n```\n")
	if len(badDef[0].Errors) == 0 {
		t.Fatal("bad default")
	}
	badRisk := ParseDecisions("```decision\n{\"id\":\"r\",\"title\":\"t\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"b\",\"label\":\"B\"}],\"default\":\"a\",\"risk\":\"L9\"}\n```\n")
	if len(badRisk[0].Errors) == 0 {
		t.Fatal("bad risk")
	}
	crlf := ParseDecisions("```decision\r\n{\"id\":\"c\",\"title\":\"t\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"b\",\"label\":\"B\"}],\"default\":\"a\"}\r\n```\r\n")
	if len(crlf) != 1 || len(crlf[0].Errors) > 0 {
		t.Fatalf("crlf: %+v", crlf)
	}
	nestedTicks := ParseDecisions("# doc\n```decision\n{\"id\":\"n\",\"title\":\"tick ``` test\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"b\",\"label\":\"B\"}],\"default\":\"a\"}\n```\n")
	if len(nestedTicks) != 1 {
		t.Fatalf("nested backticks in json: %+v", nestedTicks)
	}
	indented := ParseDecisions("  - item\n    ```decision\n{\"id\":\"i\",\"title\":\"t\",\"options\":[{\"id\":\"a\",\"label\":\"A\"},{\"id\":\"b\",\"label\":\"B\"}],\"default\":\"a\"}\n    ```\n")
	if len(indented) != 1 {
		t.Fatalf("indented list fence: %+v", indented)
	}
}

// 替换中间的一节后，下一个标题前仍然要有空行（曾经因为节体被去掉末尾换行而紧贴在上一节最后一行后面）。
func TestReplaceSectionKeepsBlankLineBeforeNextHeading(t *testing.T) {
	body := "# T\n\nintro\n\n## B\nb1\n\n## C\nc1\n"
	got, err := ReplaceSection(body, "B", "## B\n- x\n- y\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "# T\n\nintro\n\n## B\n- x\n- y\n\n## C\nc1\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// 替换最后一节：不多出尾部空行，也不影响前面的节。
	got, err = ReplaceSection(body, "C", "## C\nc2")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# T\n\nintro\n\n## B\nb1\n\n## C\nc2"; got != want {
		t.Fatalf("last section: got %q want %q", got, want)
	}
	// 子节随父节一起被替换，之后的同级标题前同样有空行。
	body2 := "## A\na\n\n### A1\na1\n\n## B\nb\n"
	got, err = ReplaceSection(body2, "A", "## A\nnew")
	if err != nil {
		t.Fatal(err)
	}
	if want := "## A\nnew\n\n## B\nb\n"; got != want {
		t.Fatalf("nested: got %q want %q", got, want)
	}
}

func TestReplaceSectionDuplicateHeadings(t *testing.T) {
	body := "## A\n1\n## A\n2\n## B\n3"
	newBody, err := ReplaceSection(body, "A", "## A\nx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(newBody, "x") || strings.Contains(newBody, "\n1\n") {
		t.Fatalf("should replace first A section only: %q", newBody)
	}
}
