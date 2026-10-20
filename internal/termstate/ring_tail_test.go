package termstate

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// numberedLines 生成 n 行「<序号>\r\n」风格的输出（PTY 的换行就是 CRLF）。
func numberedLines(from, n int) []byte {
	var b bytes.Buffer
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&b, "line-%06d\r\n", i)
	}
	return b.Bytes()
}

func TestTailStart(t *testing.T) {
	t.Run("缓冲里的行数不够就返回缓冲头", func(t *testing.T) {
		r := NewRing(1 << 12)
		r.Write(numberedLines(0, 5))
		if got := r.TailStart(10); got != r.Base() {
			t.Fatalf("行数不够应返回 Base: got %d base %d", got, r.Base())
		}
		if got := r.TailStart(5); got != r.Base() {
			// 5 行 + 末尾空行需要 6 个换行，只有 5 个：整体回放
			t.Fatalf("got %d", got)
		}
	})
	t.Run("取末尾 N 行连同未结束的那一行", func(t *testing.T) {
		r := NewRing(1 << 12)
		r.Write(numberedLines(0, 100))
		r.Write([]byte("$ ")) // 提示符：末尾还没结束的一行
		got := r.TailStart(3)
		tail := readAllRing(t, r, got)
		want := "line-000098\r\nline-000099\r\n$ "
		// 倒数第 3 行是 line-000097
		want = "line-000097\r\n" + want
		if string(tail) != want {
			t.Fatalf("tail = %q\nwant  = %q", tail, want)
		}
		if got > 0 {
			prev := readAllRing(t, r, got-1)
			if prev[0] != '\n' {
				t.Fatalf("起点之前必须是换行，得到 %q", prev[0])
			}
		}
	})
	t.Run("末尾恰好以换行结束", func(t *testing.T) {
		r := NewRing(1 << 12)
		r.Write(numberedLines(0, 50))
		tail := readAllRing(t, r, r.TailStart(2))
		if string(tail) != "line-000048\r\nline-000049\r\n" {
			t.Fatalf("tail = %q", tail)
		}
	})
	t.Run("缓冲回绕后仍按行取", func(t *testing.T) {
		r := NewRing(256)
		r.Write(numberedLines(0, 200)) // 远超容量
		if r.Base() == 0 {
			t.Fatal("测试前提：缓冲应已回绕")
		}
		got := r.TailStart(4)
		tail := readAllRing(t, r, got)
		if string(tail) != "line-000196\r\nline-000197\r\nline-000198\r\nline-000199\r\n" {
			t.Fatalf("tail = %q", tail)
		}
	})
	t.Run("lines 非正数", func(t *testing.T) {
		r := NewRing(64)
		r.Write(numberedLines(0, 3))
		if got := r.TailStart(0); got != r.Base() {
			t.Fatalf("got %d", got)
		}
		if got := r.TailStart(-1); got != r.Base() {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("空缓冲", func(t *testing.T) {
		r := NewRing(64)
		if got := r.TailStart(5); got != 0 {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("跨越回读块边界（单行很长）", func(t *testing.T) {
		r := NewRing(1 << 20)
		long := bytes.Repeat([]byte("x"), tailScan*3+17)
		r.Write(numberedLines(0, 3))
		r.Write(long)
		r.Write([]byte("\r\n"))
		r.Write(numberedLines(3, 2))
		tail := readAllRing(t, r, r.TailStart(3))
		// 倒数第 3 行是那条超长的行
		if !bytes.HasPrefix(tail, long) || !bytes.HasSuffix(tail, []byte("line-000004\r\n")) {
			t.Fatalf("超长行应完整保留，tail 长度 %d", len(tail))
		}
	})
	t.Run("与逐行参考实现一致", func(t *testing.T) {
		rng := rand.New(rand.NewSource(7))
		for round := 0; round < 200; round++ {
			size := 64 + rng.Intn(2000)
			r := NewRing(size)
			var all []byte
			for i := 0; i < 1+rng.Intn(40); i++ {
				chunk := make([]byte, rng.Intn(120))
				for j := range chunk {
					switch rng.Intn(9) {
					case 0:
						chunk[j] = '\n'
					default:
						chunk[j] = byte('a' + rng.Intn(26))
					}
				}
				r.Write(chunk)
				all = append(all, chunk...)
			}
			lines := 1 + rng.Intn(12)
			got := r.TailStart(lines)
			// 参考：在仍留在缓冲里的那部分字节上，找第 lines+1 个（从后往前）换行
			kept := all
			if len(kept) > size {
				kept = kept[len(kept)-size:]
			}
			base := r.Base()
			want := base
			seen := 0
			for i := len(kept) - 1; i >= 0; i-- {
				if kept[i] == '\n' {
					seen++
					if seen == lines+1 {
						want = base + uint64(i) + 1
						break
					}
				}
			}
			if got != want {
				t.Fatalf("round %d lines=%d: got %d want %d (base %d end %d)", round, lines, got, want, base, r.End())
			}
		}
	})
}

func TestReplayStart(t *testing.T) {
	big := func(lines int) *Ring {
		r := NewRing(1 << 20)
		r.Write(numberedLines(0, lines))
		return r
	}
	t.Run("缓冲量不到门槛就整体回放", func(t *testing.T) {
		r := big(100) // 约 1.4 KiB
		start, trimmed := r.ReplayStart(0, 20, 64<<10)
		if start != 0 || trimmed {
			t.Fatalf("start=%d trimmed=%v", start, trimmed)
		}
	})
	t.Run("超过门槛只回放末尾", func(t *testing.T) {
		r := big(20000) // 约 280 KiB
		start, trimmed := r.ReplayStart(0, 50, 64<<10)
		if !trimmed || start == 0 {
			t.Fatalf("应裁剪: start=%d trimmed=%v", start, trimmed)
		}
		tail := readAllRing(t, r, start)
		if got := bytes.Count(tail, []byte("\n")); got != 50 {
			t.Fatalf("末尾应有 50 行，得到 %d", got)
		}
		if !bytes.HasPrefix(tail, []byte("line-019950")) {
			t.Fatalf("起点应是第 19950 行: %q", tail[:20])
		}
	})
	t.Run("tail 为 0 等同于 AlignForReplay", func(t *testing.T) {
		r := NewRing(256)
		r.Write(numberedLines(0, 100))
		start, trimmed := r.ReplayStart(r.Base(), 0, 0)
		if trimmed || start != r.AlignForReplay(r.Base()) {
			t.Fatalf("start=%d trimmed=%v", start, trimmed)
		}
	})
	t.Run("字节数够但行数不够就整体回放", func(t *testing.T) {
		r := NewRing(1 << 20)
		r.Write(numberedLines(0, 100))
		r.Write(bytes.Repeat([]byte("y"), 300<<10)) // 一行超长：字节数过了门槛，行数却很少
		start, trimmed := r.ReplayStart(0, 5000, 64<<10)
		if start != 0 || trimmed {
			t.Fatalf("行数不够应整体回放: start=%d trimmed=%v", start, trimmed)
		}
	})
	t.Run("缓冲已回绕且行数够：裁剪后不早于缓冲头", func(t *testing.T) {
		r := NewRing(64 << 10)
		r.Write(numberedLines(0, 20000)) // 约 280 KiB，缓冲只留最后 64 KiB
		if r.Base() == 0 {
			t.Fatal("测试前提：缓冲应已回绕")
		}
		start, trimmed := r.ReplayStart(r.Base(), 100, 32<<10)
		if !trimmed || start <= r.Base() || start >= r.End() {
			t.Fatalf("start=%d trimmed=%v base=%d end=%d", start, trimmed, r.Base(), r.End())
		}
	})
}
