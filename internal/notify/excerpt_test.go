package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExcerptStripsMarkdown(t *testing.T) {
	md := "# 标题\n\n> 引用一行\n- **粗体**条目\n1. 有序 `code` 项\n\n```go\nfmt.Println(\"不该出现\")\n```\n\n正文收尾"
	got := Excerpt(md)
	want := "标题 引用一行 粗体条目 有序 code 项 正文收尾"
	if got != want {
		t.Fatalf("得到 %q，期望 %q", got, want)
	}
	if strings.Contains(got, "不该出现") || strings.Contains(got, "```") || strings.Contains(got, "#") {
		t.Fatalf("代码围栏与标记不应保留：%q", got)
	}
}

func TestExcerptLimits(t *testing.T) {
	// 最多取前 6 个非空行。
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "行"+string(rune('0'+i%10)))
	}
	got := Excerpt(strings.Join(lines, "\n"))
	if strings.Count(got, "行") != 6 {
		t.Fatalf("应只取前 6 行：%q", got)
	}
	// 按字符数截断（含省略号），且不破坏多字节字符。
	long := Excerpt(strings.Repeat("汉", 500))
	if n := utf8.RuneCountInString(long); n != MaxPreviewRunes || !strings.HasSuffix(long, "…") || !utf8.ValidString(long) {
		t.Fatalf("长文本应截到 %d 字并以省略号结尾：%d %q", MaxPreviewRunes, n, long)
	}
	// 空输入与只有围栏的输入返回空串。
	for _, in := range []string{"", "   \n\n", "```\ncode only\n```"} {
		if got := Excerpt(in); got != "" {
			t.Errorf("%q 应得到空串，得到 %q", in, got)
		}
	}
	// 未闭合的围栏：其后的内容都被丢弃（与围栏的语义一致）。
	if got := Excerpt("前文\n```\n未闭合"); got != "前文" {
		t.Errorf("未闭合围栏后的内容应丢弃：%q", got)
	}
}
