package termstate

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

func readAllRing(t *testing.T, r *Ring, from uint64) []byte {
	t.Helper()
	out := make([]byte, r.End()-from)
	n, err := r.Read(from, out)
	if err != nil {
		t.Fatalf("Read(%d): %v", from, err)
	}
	return out[:n]
}

func TestRingBasic(t *testing.T) {
	r := NewRing(8)
	if r.Base() != 0 || r.End() != 0 {
		t.Fatalf("初始状态不符: base=%d end=%d", r.Base(), r.End())
	}
	r.Write([]byte("abc"))
	r.Write([]byte("def"))
	if r.End() != 6 || r.Base() != 0 {
		t.Fatalf("base=%d end=%d", r.Base(), r.End())
	}
	if got := readAllRing(t, r, 0); string(got) != "abcdef" {
		t.Fatalf("got %q", got)
	}
	if got := readAllRing(t, r, 4); string(got) != "ef" {
		t.Fatalf("got %q", got)
	}
	if n, err := r.Read(6, make([]byte, 4)); n != 0 || err != nil {
		t.Fatalf("在 End 处读应返回 0, nil: n=%d err=%v", n, err)
	}
	if n, err := r.Read(99, make([]byte, 4)); n != 0 || err != nil {
		t.Fatalf("越过 End 读应返回 0, nil: n=%d err=%v", n, err)
	}
}

func TestRingWrapAndOverwrite(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("0123456789")) // 10 字节，容量 8 → 保留 "23456789"
	if r.Base() != 2 || r.End() != 10 {
		t.Fatalf("base=%d end=%d", r.Base(), r.End())
	}
	if got := readAllRing(t, r, 2); string(got) != "23456789" {
		t.Fatalf("got %q", got)
	}
	if _, err := r.Read(1, make([]byte, 4)); !errors.Is(err, ErrOverwritten) {
		t.Fatalf("已覆盖的偏移应返回 ErrOverwritten: %v", err)
	}
	// 再追加，写指针跨越边界
	r.Write([]byte("AB"))
	if got := readAllRing(t, r, 4); string(got) != "456789AB" {
		t.Fatalf("got %q", got)
	}
	// 一次写入远大于容量：只保留最后 8 字节，偏移仍按总量累计
	r.Write(bytes.Repeat([]byte("x"), 20))
	r.Write([]byte("END"))
	if r.End() != 10+2+20+3 {
		t.Fatalf("end=%d", r.End())
	}
	if got := readAllRing(t, r, r.Base()); string(got) != "xxxxxEND" {
		t.Fatalf("got %q", got)
	}
}

// 与参考模型（无限长切片）对拍：任意写入序列后，缓冲里保留的区间必须与参考一致。
func TestRingMatchesReferenceModel(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for round := 0; round < 200; round++ {
		size := 1 + rng.Intn(64)
		r := NewRing(size)
		var ref []byte
		for i := 0; i < 1+rng.Intn(30); i++ {
			chunk := make([]byte, rng.Intn(3*size+1))
			rng.Read(chunk)
			r.Write(chunk)
			ref = append(ref, chunk...)

			if r.End() != uint64(len(ref)) {
				t.Fatalf("end=%d ref=%d", r.End(), len(ref))
			}
			base := r.Base()
			if want := uint64(0); len(ref) > size {
				want = uint64(len(ref) - size)
				if base != want {
					t.Fatalf("base=%d want=%d", base, want)
				}
			}
			got := readAllRing(t, r, base)
			if !bytes.Equal(got, ref[base:]) {
				t.Fatalf("round %d: 缓冲内容与参考不一致（size=%d len=%d）", round, size, len(ref))
			}
			// 随机起点的局部读取
			if r.End() > base {
				off := base + uint64(rng.Intn(int(r.End()-base)))
				part := make([]byte, 1+rng.Intn(size))
				n, err := r.Read(off, part)
				if err != nil || !bytes.Equal(part[:n], ref[off:off+uint64(n)]) {
					t.Fatalf("局部读取不一致 off=%d n=%d err=%v", off, n, err)
				}
			}
		}
	}
}

func TestAlignForReplay(t *testing.T) {
	t.Run("缓冲未回绕不对齐", func(t *testing.T) {
		r := NewRing(64)
		r.Write([]byte("hello\nworld"))
		if got := r.AlignForReplay(0); got != 0 {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("起点不在缓冲头部不对齐", func(t *testing.T) {
		r := NewRing(8)
		r.Write([]byte("0123456789abc"))
		if got := r.AlignForReplay(r.Base() + 1); got != r.Base()+1 {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("回绕后对齐到下一个换行之后", func(t *testing.T) {
		r := NewRing(16)
		r.Write([]byte("XXXX\nline-two\nline-three\n"))
		base := r.Base()
		got := r.AlignForReplay(base)
		rest := readAllRing(t, r, got)
		if len(rest) == 0 || rest[len(rest)-1] != '\n' {
			t.Fatalf("对齐后内容异常: %q", rest)
		}
		// 对齐点之前的那个字节必须是换行
		prev := readAllRing(t, r, got-1)
		if prev[0] != '\n' {
			t.Fatalf("对齐点之前应是换行，得到 %q", prev[0])
		}
	})
	t.Run("没有换行时跳过 UTF-8 续字节", func(t *testing.T) {
		r := NewRing(6)
		// "终端" = E7 BB 88 E7 AB AF；容量 6 后再写一个字节让头部落在字符中间
		r.Write([]byte("终端"))
		r.Write([]byte("Z")) // 保留 BB 88 E7 AB AF 5A，头部 BB 是续字节
		got := r.AlignForReplay(r.Base())
		if got != r.Base()+2 { // 跳过 BB 88
			t.Fatalf("应跳过两个续字节: base=%d got=%d", r.Base(), got)
		}
	})
}
