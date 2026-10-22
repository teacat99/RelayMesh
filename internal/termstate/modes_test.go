package termstate

import (
	"math/rand"
	"testing"
)

func feedAll(m *ModeTracker, chunks ...string) uint64 {
	var off uint64
	for _, c := range chunks {
		m.Feed(off, []byte(c))
		off += uint64(len(c))
	}
	return off
}

func TestAltScreenEnterExitAndOffset(t *testing.T) {
	m := NewModeTracker()
	pre := "user@host:~$ vim x\r\n"
	enter := "\x1b[?1049h"
	feedAll(m, pre, enter, "\x1b[Hhello")
	alt, off := m.Alt()
	if !alt {
		t.Fatal("应处于备用屏")
	}
	if want := uint64(len(pre) + len(enter)); off != want {
		t.Fatalf("进入备用屏的偏移应指向序列之后: got %d want %d", off, want)
	}
	feedAll(m, "\x1b[?1049l")
	if alt, _ := m.Alt(); alt {
		t.Fatal("应已退出备用屏")
	}
}

func TestAltScreenLegacyModes47And1047(t *testing.T) {
	for _, mode := range []string{"47", "1047"} {
		m := NewModeTracker()
		feedAll(m, "\x1b[?"+mode+"h")
		if alt, _ := m.Alt(); !alt {
			t.Fatalf("模式 %s 应进入备用屏", mode)
		}
		feedAll(m, "\x1b[?"+mode+"l")
		if alt, _ := m.Alt(); alt {
			t.Fatalf("模式 %s 应退出备用屏", mode)
		}
	}
}

func TestPrefixReflectsNonDefaultModes(t *testing.T) {
	m := NewModeTracker()
	if p := m.Prefix(false); p != nil {
		t.Fatalf("默认状态不应有恢复序列: %q", p)
	}
	feedAll(m, "\x1b[?2004h", "\x1b[?1000;1006h", "\x1b[?25l", "\x1b[?1h", "\x1b=")
	got := string(m.Prefix(false))
	for _, want := range []string{"\x1b[?1h", "\x1b[?25l", "\x1b[?1000h", "\x1b[?1006h", "\x1b[?2004h", "\x1b="} {
		if !contains(got, want) {
			t.Fatalf("恢复序列缺少 %q: %q", want, got)
		}
	}
	// 关闭后回到默认：不再输出
	feedAll(m, "\x1b[?2004l", "\x1b[?1000;1006l", "\x1b[?25h", "\x1b[?1l", "\x1b>")
	if p := m.Prefix(false); p != nil {
		t.Fatalf("恢复默认后不应有恢复序列: %q", p)
	}
	// includeAlt 会先切换到备用屏
	feedAll(m, "\x1b[?1049h")
	if p := string(m.Prefix(true)); p != "\x1b[?1049h" {
		t.Fatalf("got %q", p)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 无论输入如何在任意字节处被切成两块，结果都必须与一次性输入一致。
func TestSplitAtEveryByteBoundary(t *testing.T) {
	stream := "abc\x1b[?1049h\x1b[?2004h\x1b]0;title\x07\x1b[?1000;1006h\x1b[31mred\x1b[0m\x1b[?25l\x1b[?1049l\x1b[?1049h"
	whole := NewModeTracker()
	feedAll(whole, stream)
	wAlt, wOff := whole.Alt()
	wPrefix := string(whole.Prefix(wAlt))

	for i := 0; i <= len(stream); i++ {
		m := NewModeTracker()
		feedAll(m, stream[:i], stream[i:])
		alt, off := m.Alt()
		if alt != wAlt || off != wOff || string(m.Prefix(alt)) != wPrefix {
			t.Fatalf("在第 %d 字节处切分后结果不一致: alt=%v/%v off=%d/%d prefix=%q/%q", i, alt, wAlt, off, wOff, m.Prefix(alt), wPrefix)
		}
	}
	// 逐字节喂入
	m := NewModeTracker()
	for i := 0; i < len(stream); i++ {
		m.Feed(uint64(i), []byte{stream[i]})
	}
	if alt, off := m.Alt(); alt != wAlt || off != wOff || string(m.Prefix(alt)) != wPrefix {
		t.Fatal("逐字节喂入结果不一致")
	}
}

// 字符串序列（OSC / DCS / APC）里的文字即使长得像模式切换，也不能改变状态。
// 注意：字符串里出现真正的 ESC 字节会终止字符串并开启新序列（xterm.js 的解析器同样如此），
// 所以这里的载荷只含可打印字符。
func TestStringSequencesAreSkipped(t *testing.T) {
	cases := map[string]string{
		"OSC-BEL": "\x1b]0;[?1049h fake title\x07",
		"OSC-ST":  "\x1b]2;title [?1049h\x1b\\",
		"DCS":     "\x1bP1$r [?1049h\x1b\\",
		"APC":     "\x1b_Gi=1;[?2004h\x1b\\",
	}
	for name, seq := range cases {
		m := NewModeTracker()
		feedAll(m, seq)
		if alt, _ := m.Alt(); alt {
			t.Errorf("%s: 字符串内的文字不应切换备用屏", name)
		}
		if p := m.Prefix(false); p != nil {
			t.Errorf("%s: 字符串内的文字不应改变模式: %q", name, p)
		}
	}
	// 字符串被 ESC 打断后，新序列应当被识别
	m := NewModeTracker()
	feedAll(m, "\x1b]0;unterminated\x1b[?1049h")
	if alt, _ := m.Alt(); !alt {
		t.Error("被 ESC 打断的字符串之后的序列应当生效")
	}
}

func TestIgnoresNonPrivateAndMalformed(t *testing.T) {
	m := NewModeTracker()
	feedAll(m,
		"\x1b[1049h",          // 没有 '?'：不是 DEC 私有模式
		"\x1b[?h",             // 没有参数
		"\x1b[?;h",            // 只有分隔符
		"\x1b[?1049$h",        // 含中间字节：DECRQM 之类，不是设置
		"\x1b[?abch",          // 非数字
		"\x1b[?1049",          // 未终止
		"\x18\x1b[?1000\x1ah", // CAN/SUB 取消序列
	)
	if alt, _ := m.Alt(); alt {
		t.Fatal("不应进入备用屏")
	}
	if p := m.Prefix(false); p != nil {
		t.Fatalf("不应有任何模式变化: %q", p)
	}
}

func TestResetSequences(t *testing.T) {
	m := NewModeTracker()
	feedAll(m, "\x1b[?2004h\x1b[?1000h\x1b[?1049h")
	feedAll(m, "\x1b[!p") // DECSTR：模式复位，备用屏不变
	if p := m.Prefix(false); p != nil {
		t.Fatalf("DECSTR 后模式应回到默认: %q", p)
	}
	if alt, _ := m.Alt(); !alt {
		t.Fatal("DECSTR 不应退出备用屏")
	}
	feedAll(m, "\x1b[?2004h", "\x1bc") // RIS：全部重置并退出备用屏
	if p := m.Prefix(false); p != nil {
		t.Fatalf("RIS 后模式应回到默认: %q", p)
	}
	if alt, _ := m.Alt(); alt {
		t.Fatal("RIS 应退出备用屏")
	}
}

func TestCharsetDesignationDoesNotConfuse(t *testing.T) {
	m := NewModeTracker()
	feedAll(m, "\x1b(B\x1b)0\x1b[?2004h")
	if p := string(m.Prefix(false)); p != "\x1b[?2004h" {
		t.Fatalf("got %q", p)
	}
}

func TestAutowrapDefaultOnAndReportedWhenDisabled(t *testing.T) {
	m := NewModeTracker()
	feedAll(m, "\x1b[?7l")
	if p := string(m.Prefix(false)); p != "\x1b[?7l" {
		t.Fatalf("关闭自动换行应体现在恢复序列中: %q", p)
	}
}

// 随机字节与超长参数不得 panic，也不得导致无限增长。
func TestFuzzNoPanic(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	m := NewModeTracker()
	var off uint64
	buf := make([]byte, 4096)
	alphabet := []byte("\x1b[]?;0123456789hlPX^_\\\x07\x18\x1a()=>c!p $abcdefg")
	for i := 0; i < 500; i++ {
		for j := range buf {
			if rng.Intn(3) == 0 {
				buf[j] = byte(rng.Intn(256))
			} else {
				buf[j] = alphabet[rng.Intn(len(alphabet))]
			}
		}
		m.Feed(off, buf)
		off += uint64(len(buf))
		_ = m.Prefix(false)
	}
	long := append([]byte("\x1b[?"), make([]byte, 100000)...)
	for i := 3; i < len(long); i++ {
		long[i] = '1'
	}
	long = append(long, 'h')
	m.Feed(off, long)
	if len(m.params) > maxModeParamBytes {
		t.Fatalf("参数缓冲不应无限增长: %d", len(m.params))
	}
}
