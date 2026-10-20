package termproto

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestSanitizeText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"普通主机名", "my-host.local", 64, "my-host.local"},
		{"中文保留", "开发机-北京", 64, "开发机-北京"},
		{"emoji 保留", "box 🚀", 64, "box 🚀"},
		{"首尾空白", "  a b  ", 64, "a b"},
		{"空白折叠", "a \t\n  b", 64, "a b"}, // 制表与换行属于控制字符被去除；连续普通空格折叠为一个
		{"ESC 序列", "\x1b[2J\x1b]0;title\x07pwn", 64, "[2J]0;titlepwn"},
		{"NUL 与 CR/LF", "a\x00b\r\nc", 64, "abc"},
		{"双向控制符", "abc\u202edef\u2066x\u2069", 64, "abcdefx"},
		{"零宽字符", "a\u200bb\u200c\u200dc\ufeffd\u2060e", 64, "abcde"},
		{"行段分隔符", "a\u2028b\u2029c", 64, "abc"},
		{"私用区", "a\ue000b", 64, "ab"},
		{"非法 UTF-8", "a\xff\xfeb\xc3", 64, "ab"},
		{"NBSP 与全角空格归一", "a\u00a0\u3000b", 64, "a b"},
		{"按 rune 截断", "一二三四五六", 3, "一二三"},
		{"截断后不留尾随空格", "ab cd", 3, "ab"},
		{"空串", "", 10, ""},
		{"全是控制字符", "\x01\x02\x03", 10, ""},
		{"HTML 不转义（界面按纯文本渲染）", "<b>x</b>", 64, "<b>x</b>"},
		{"组合记号堆叠受限", "a" + strings.Repeat("\u0301", 50), 64, "a\u0301\u0301\u0301"},
		{"不允许组合记号开头", "\u0301abc", 64, "abc"},
	}
	for _, c := range cases {
		if got := SanitizeText(c.in, c.max); got != c.want {
			t.Errorf("%s: SanitizeText(%q,%d) = %q，期望 %q", c.name, c.in, c.max, got, c.want)
		}
	}
}

func TestSanitizeTextUnlimited(t *testing.T) {
	long := strings.Repeat("a", 10000)
	if got := SanitizeText(long, 0); len(got) != 10000 {
		t.Fatalf("maxRunes<=0 表示不限长，实际 %d", len(got))
	}
}

// 性质测试：无论输入是什么，输出必须是合法 UTF-8、只含可显示字符、不超长、首尾无空白。
func FuzzSanitizeText(f *testing.F) {
	for _, s := range []string{"", "host", "\x1b[0m", "\u202e", "a\xffb", strings.Repeat("\u0301", 100), "  x  ", "一二三"} {
		f.Add(s, 8)
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 1 || max > 256 {
			max = 16
		}
		got := SanitizeText(s, max)
		if !utf8.ValidString(got) {
			t.Fatalf("输出必须是合法 UTF-8: %q", got)
		}
		if utf8.RuneCountInString(got) > max {
			t.Fatalf("输出超长: %d > %d", utf8.RuneCountInString(got), max)
		}
		if got != strings.TrimSpace(got) {
			t.Fatalf("首尾不应有空白: %q", got)
		}
		for _, r := range got {
			if !unicode.IsGraphic(r) {
				t.Fatalf("输出含不可显示字符 U+%04X: %q", r, got)
			}
			if r != ' ' && unicode.Is(unicode.Zs, r) {
				t.Fatalf("空白必须归一为普通空格 U+%04X", r)
			}
		}
		if SanitizeText(got, max) != got {
			t.Fatalf("净化应幂等: %q", got)
		}
	})
}
