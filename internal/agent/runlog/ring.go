package runlog

// ring 是固定容量的字节环：写入超出容量时只保留最新的 cap 字节。
type ring struct {
	buf   []byte
	start int // 最旧字节在 buf 中的位置
	n     int // 当前保存的字节数
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]byte, capacity)}
}

func (r *ring) len() int { return r.n }

func (r *ring) write(p []byte) {
	c := len(r.buf)
	if len(p) >= c {
		copy(r.buf, p[len(p)-c:])
		r.start, r.n = 0, c
		return
	}
	end := (r.start + r.n) % c
	first := copy(r.buf[end:], p)
	if first < len(p) {
		copy(r.buf, p[first:])
	}
	r.n += len(p)
	if r.n > c {
		drop := r.n - c
		r.start = (r.start + drop) % c
		r.n = c
	}
}

// bytes 返回按时间顺序排列的副本。
func (r *ring) bytes() []byte {
	out := make([]byte, r.n)
	c := len(r.buf)
	first := copy(out, r.buf[r.start:min(r.start+r.n, c)])
	if first < r.n {
		copy(out[first:], r.buf[:r.n-first])
	}
	return out
}

// slice 返回从最旧字节起偏移 off 处开始、至多 max 字节的副本。
func (r *ring) slice(off, max int) []byte {
	if off < 0 || off >= r.n || max <= 0 {
		return nil
	}
	n := min(max, r.n-off)
	out := make([]byte, n)
	c := len(r.buf)
	pos := (r.start + off) % c
	first := copy(out, r.buf[pos:min(pos+n, c)])
	if first < n {
		copy(out[first:], r.buf[:n-first])
	}
	return out
}
