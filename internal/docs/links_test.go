package docs

import (
	"reflect"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	body := "见 [[design/a]] 和 [[design/b.md|B 文档]]，还有 [[design/a#节]] 重复。\n" +
		"相对 [同目录](./c.md) [上级](../x/d) 外链 [e](https://example.com/f.md) 图 ![p](p.png) 锚点 [t](#top)\n" +
		"引用 [[artifact:12]]\n" +
		"```\n[[in/fence]]\n```\n行内 `[[in/code]]` 不算。\n"
	got := ExtractLinks("design/sub/self.md", body)
	want := []string{"design/a.md", "design/b.md", "design/sub/c.md", "design/x/d.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractLinks = %v, want %v", got, want)
	}
}

func TestExtractLinksRootDoc(t *testing.T) {
	got := ExtractLinks("readme.md", "[[a/b]] [c](d.md)")
	want := []string{"a/b.md", "d.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
