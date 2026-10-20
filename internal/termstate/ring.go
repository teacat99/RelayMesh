package termstate

import "errors"

// ErrOverwritten 表示请求的偏移已经被环形缓冲覆盖。
var ErrOverwritten = errors.New("ring: offset overwritten")

// Ring 是按「累计字节偏移」寻址的环形缓冲：偏移为 o 的字节存放在 buf[o % size]。
//
// 偏移从 0 开始单调递增，永不回绕（uint64 足够），这让「断线重连从某偏移续传」「缺口显式标记」
// 都成为简单的整数比较。Ring 本身不是并发安全的，调用方（Session）用自己的锁保护它。
type Ring struct {
	buf  []byte
	size uint64
	end  uint64 // 累计写入字节数，也是下一个字节的偏移
}

// NewRing 创建容量为 size 字节的环形缓冲（size 至少为 1）。
func NewRing(size int) *Ring {
	if size < 1 {
		size = 1
	}
	return &Ring{buf: make([]byte, size), size: uint64(size)}
}

// End 返回累计写入的字节数（下一个字节的偏移）。
func (r *Ring) End() uint64 { return r.end }

// Base 返回仍保留在缓冲中的最旧字节的偏移。
func (r *Ring) Base() uint64 {
	if r.end > r.size {
		return r.end - r.size
	}
	return 0
}

// Write 追加数据，必要时覆盖最旧的数据。
func (r *Ring) Write(p []byte) {
	n := uint64(len(p))
	if n == 0 {
		return
	}
	start := r.end
	data := p
	if n > r.size {
		// 只有最后 size 字节可能留下来。
		data = p[n-r.size:]
		start = r.end + n - r.size
	}
	idx := start % r.size
	first := copy(r.buf[idx:], data)
	copy(r.buf, data[first:])
	r.end += n
}

// Read 从偏移 off 读取至多 len(dst) 字节，返回实际读取的字节数。
// off 早于 Base 时返回 ErrOverwritten；off 不小于 End 时返回 0。
func (r *Ring) Read(off uint64, dst []byte) (int, error) {
	if off < r.Base() {
		return 0, ErrOverwritten
	}
	if off >= r.end {
		return 0, nil
	}
	n := uint64(len(dst))
	if avail := r.end - off; n > avail {
		n = avail
	}
	idx := off % r.size
	first := r.size - idx
	if first > n {
		first = n
	}
	copy(dst, r.buf[idx:idx+first])
	copy(dst[first:n], r.buf[:n-first])
	return int(n), nil
}

// TailStart 返回「缓冲末尾 lines 行」的起点偏移：从末尾往回数换行符，第 lines+1 个换行符之后就是
// 倒数第 lines 行（连同末尾还没结束的那一行，例如提示符）的开头。起点总落在行首，不会切在转义序列或 UTF-8 字符中间
// （换行符是 C0 控制字符，不会出现在序列或多字节字符内部）。
//
// 缓冲里的换行不够多（整个缓冲不到 lines 行）时返回 Base，由调用方照常整体回放。
func (r *Ring) TailStart(lines int) uint64 {
	base := r.Base()
	if lines <= 0 || r.end <= base {
		return base
	}
	var buf [tailScan]byte
	need := lines + 1
	pos := r.end
	for pos > base {
		n := uint64(len(buf))
		if pos-base < n {
			n = pos - base
		}
		start := pos - n
		got, err := r.Read(start, buf[:n])
		if err != nil || uint64(got) != n {
			return base
		}
		for i := int(n) - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			need--
			if need == 0 {
				return start + uint64(i) + 1
			}
		}
		pos = start
	}
	return base
}

// tailScan 是 TailStart 每次回读的块大小。
const tailScan = 16 << 10

// ReplayStart 给出全新附着在主屏上的回放起点：在 AlignForReplay 之上叠加「只回放末尾若干行」。
//
// tail > 0、缓冲里的字节数不少于 minBytes、并且缓冲里的行数多于 tail 时，起点取末尾 tail 行的开头（trimmed = true）；
// 这是有意跳过的内容（浏览器可以从记录里按需读到它），不算「被覆盖」。其余情况等同于 AlignForReplay(from)。
// 缓冲还很小的会话照旧整体回放：回放本来就快，也保住完整的终端状态。
func (r *Ring) ReplayStart(from uint64, tail int, minBytes uint64) (start uint64, trimmed bool) {
	if base := r.Base(); tail > 0 && r.end > base && r.end-base >= minBytes {
		if t := r.TailStart(tail); t > base {
			return t, true
		}
	}
	return r.AlignForReplay(from), false
}

// alignScan 是回放起点对齐时向后扫描的最大字节数。
const alignScan = 4096

// AlignForReplay 返回一个不小于 from 的回放起点，避免从转义序列或 UTF-8 字符中间开始。
//
// 只有在 from 恰好是环形缓冲的头（缓冲已经回绕过）时才需要对齐：此时头部字节很可能落在某个
// 序列的中间。优先对齐到下一个换行之后；扫描范围内没有换行就只跳过 UTF-8 续字节。
func (r *Ring) AlignForReplay(from uint64) uint64 {
	if from == 0 || from != r.Base() || from >= r.end {
		return from
	}
	var tmp [alignScan]byte
	n, err := r.Read(from, tmp[:])
	if err != nil || n == 0 {
		return from
	}
	for i := 0; i < n; i++ {
		if tmp[i] == '\n' {
			return from + uint64(i) + 1
		}
	}
	i := 0
	for i < n && tmp[i]&0xC0 == 0x80 {
		i++
	}
	return from + uint64(i)
}
