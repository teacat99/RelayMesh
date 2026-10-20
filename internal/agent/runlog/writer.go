package runlog

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// streamState 是写入器里一路输出的状态。
type streamState struct {
	file    *os.File
	headLen int64
	total   int64
	tail    *ring
	dirty   bool   // 结尾环里有尚未写入 .tail 文件的内容
	first   []byte // 该路最前面的至多 RunInlineMax+1 字节：用来在结束时判断能否内联给 Hub
	diskErr bool
}

// Writer 是一次运行的输出写入器，可被两路输出的读取协程并发使用。
type Writer struct {
	s   *Store
	dir string
	// mem 表示这次运行只在内存里保留（磁盘写不下，见 Store.Begin）：没有目录与文件，
	// 所有输出只进结尾环，元信息也不落盘。
	mem bool
	// metaLost 表示结束时最终的元信息没能写进磁盘（多半是磁盘写满）：盘上的 meta.json 还停在「运行中」，
	// 以内存里的为准（写入器登记在 Store.memDone 里）。
	metaLost bool

	mu       sync.Mutex
	meta     Meta
	st       [2]streamState
	prevHead []byte // 合并两路输出（按到达顺序）的开头预览
	prevCap  int
	prevTail *ring
	closed   bool
	// metaDirty 表示自上次写元信息以来有新的输出（统计已过时）。
	metaDirty bool
}

// ID 返回运行 ID。
func (w *Writer) ID() string { return w.meta.ID }

// Write 追加一段输出，返回这段数据在该路输出中的起始偏移（用于给实时帧标注位置）。
// 写盘失败不会影响调用方：计数与实时流照常，只是这部分不会出现在文件里。
func (w *Writer) Write(stream byte, p []byte) uint64 {
	if len(p) == 0 {
		return 0
	}
	idx := streamIdx(stream)
	w.mu.Lock()
	defer w.mu.Unlock()
	st := &w.st[idx]
	start := uint64(st.total)
	if w.closed {
		return start
	}
	st.total += int64(len(p))
	w.metaDirty = true

	if len(st.first) <= termproto.RunInlineMax {
		room := termproto.RunInlineMax + 1 - len(st.first)
		st.first = append(st.first, p[:min(room, len(p))]...)
	}

	rest := p
	if !w.mem && st.headLen < int64(w.s.o.HeadBytes) {
		n := int(min(int64(len(rest)), int64(w.s.o.HeadBytes)-st.headLen))
		if st.file != nil && !st.diskErr {
			if _, err := st.file.Write(rest[:n]); err != nil {
				st.diskErr = true
				w.s.o.Logf("写入运行输出失败（%s %s）: %v", w.meta.ID[:8], streamFile(stream), err)
			}
		}
		st.headLen += int64(n)
		rest = rest[n:]
	}
	if len(rest) > 0 {
		st.tail.write(rest)
		st.dirty = true
	}

	// 合并预览。
	if len(w.prevHead) < w.prevCap {
		n := min(w.prevCap-len(w.prevHead), len(p))
		w.prevHead = append(w.prevHead, p[:n]...)
		if n < len(p) {
			w.prevTail.write(p[n:])
		}
	} else {
		w.prevTail.write(p)
	}
	return start
}

// Totals 返回两路输出到目前为止的总字节数。
func (w *Writer) Totals() (out, errBytes int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.st[0].total, w.st[1].total
}

// SetStarted 记下进程已启动后才知道的信息（PID、实际 shell 与工作目录、启动时间）。
func (w *Writer) SetStarted(pid int, shell, cwd string, started time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.meta.PID, w.meta.Shell, w.meta.Cwd = pid, shell, cwd
	w.meta.Started = started.UnixMilli()
	w.refreshStreamMetaLocked()
	if !w.mem {
		_ = writeMeta(w.dir, &w.meta)
	}
}

func (w *Writer) refreshStreamMetaLocked() {
	for i, sm := range []*StreamMeta{&w.meta.Out, &w.meta.Err} {
		st := &w.st[i]
		sm.Total, sm.Head, sm.Tail = st.total, st.headLen, int64(st.tail.len())
	}
}

func (w *Writer) snapshotMeta() Meta {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refreshStreamMetaLocked()
	return w.meta
}

// FlushTails 把结尾环整体写入 .tail 文件（有变化时）。运行较长时由持有者定期调用，
// 这样守护进程崩溃时磁盘上的结尾部分最多落后一个调用间隔。
func (w *Writer) FlushTails() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.mem {
		return
	}
	w.flushTailsLocked(false)
	if w.metaDirty {
		// 同时刷新元信息里的统计：崩溃后据此还原总量与「中间被丢弃了多少」。
		w.refreshStreamMetaLocked()
		_ = writeMeta(w.dir, &w.meta)
		w.metaDirty = false
	}
}

func (w *Writer) flushTailsLocked(force bool) {
	if w.mem {
		return
	}
	for i, name := range [2]string{"stdout", "stderr"} {
		st := &w.st[i]
		if st.tail.len() == 0 || (!st.dirty && !force) {
			continue
		}
		if err := atomicWrite(filepath.Join(w.dir, name+".tail"), st.tail.bytes()); err != nil {
			w.s.o.Logf("写入运行输出结尾失败（%s %s）: %v", w.meta.ID[:8], name, err)
			continue
		}
		st.dirty = false
	}
}

// Finish 描述运行的结局。
type Finish struct {
	State  string
	Code   *int
	Signal string
	Reason string
	Ended  time.Time
}

// Result 是 Finish 的返回：落盘后的元信息，以及随 run_end 上报的内联输出与预览。
type Result struct {
	Meta     Meta
	Out, Err []byte // 对应输出不超过 RunInlineMax 时给出全文，否则为 nil
	PrevHead []byte
	PrevTail []byte
}

// Finish 收尾：写下结尾文件与最终元信息，并把写入器从存储里注销。重复调用返回第一次的结果之后的快照。
func (w *Writer) Finish(f Finish) Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		w.flushTailsLocked(true)
		for i := range w.st {
			if w.st[i].file != nil {
				_ = w.st[i].file.Close()
				w.st[i].file = nil
			}
		}
		w.refreshStreamMetaLocked()
		w.meta.State = f.State
		w.meta.Code = f.Code
		w.meta.Signal = f.Signal
		w.meta.Reason = f.Reason
		w.meta.Ended = f.Ended.UnixMilli()
		if w.meta.Started > 0 && w.meta.Ended > w.meta.Started {
			w.meta.DurMS = w.meta.Ended - w.meta.Started
		}
		if t := w.st[0].total; t > 0 && t <= int64(termproto.RunInlineMax) {
			w.meta.InlineOut = append([]byte(nil), w.st[0].first[:t]...)
		}
		if t := w.st[1].total; t > 0 && t <= int64(termproto.RunInlineMax) {
			w.meta.InlineErr = append([]byte(nil), w.st[1].first[:t]...)
		}
		w.meta.PrevHead = append([]byte(nil), w.prevHead...)
		w.meta.PrevTail = w.prevTail.bytes()
		switch {
		case w.mem:
			w.s.keepInMemory(w)
		case writeMeta(w.dir, &w.meta) != nil:
			w.metaLost = true
			w.s.keepInMemory(w)
		default:
			w.s.mu.Lock()
			delete(w.s.live, w.meta.ID)
			w.s.mu.Unlock()
		}
	}
	res := Result{Meta: w.meta, PrevHead: append([]byte(nil), w.prevHead...), PrevTail: w.prevTail.bytes()}
	if t := w.st[0].total; t <= int64(termproto.RunInlineMax) {
		res.Out = append([]byte(nil), w.st[0].first[:t]...)
	}
	if t := w.st[1].total; t <= int64(termproto.RunInlineMax) {
		res.Err = append([]byte(nil), w.st[1].first[:t]...)
	}
	return res
}

// abort 在 Begin 失败时清理半成品。
func (w *Writer) abort() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	for i := range w.st {
		if w.st[i].file != nil {
			_ = w.st[i].file.Close()
			w.st[i].file = nil
		}
	}
	if !w.mem {
		_ = os.RemoveAll(w.dir)
	}
}

// Discard 丢弃一次根本没有真正开始的运行（例如进程没能启动）：删除目录并注销写入器，不留下任何记录。
func (w *Writer) Discard() {
	w.abort()
	w.s.mu.Lock()
	delete(w.s.live, w.meta.ID)
	w.s.mu.Unlock()
}

// read 读取运行中的输出：开头区域取自文件，结尾区域取自内存里的环。
func (w *Writer) read(stream byte, off uint64, max int) (Page, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := &w.st[streamIdx(stream)]
	if w.mem || w.metaLost {
		// 只在内存里（没有开头文件，headLen 恒为 0），或结束时元信息没能落盘：结尾取自内存里的环；
		// 后一种情况的开头仍取自文件（写盘失败时那一段可能不完整，读不到就是读不到）。
		src := &liveSource{w: w, st: st, name: streamFile(stream)}
		return readRegions(src, st.total, st.headLen, int64(st.tail.len()), off, max, !w.closed)
	}
	if st.file == nil {
		// 已 Finish：交回文件路径（调用方在注销之前拿到了写入器的极小窗口）。
		sm := *w.meta.stream(stream)
		return readRegions(&fileSource{dir: w.dir, name: streamFile(stream)}, sm.Total, sm.Head, sm.Tail, off, max, false)
	}
	src := &liveSource{w: w, st: st, name: streamFile(stream)}
	return readRegions(src, st.total, st.headLen, int64(st.tail.len()), off, max, true)
}

// liveSource 读取写入器内部的状态；调用时 Writer 的锁已被持有。
type liveSource struct {
	w    *Writer
	st   *streamState
	name string
}

func (l *liveSource) readHead(off int64, n int) ([]byte, error) {
	return readFileAt(filepath.Join(l.w.dir, l.name+".log"), off, n)
}

func (l *liveSource) readTail(off int64, n int) ([]byte, error) {
	return l.st.tail.slice(int(off), n), nil
}

// markReported 记下 Hub 已确认收到结束事件（只用于「只在内存里」的运行；落盘的运行把标记写进 meta.json）。
func (w *Writer) markReported() {
	w.mu.Lock()
	w.meta.Reported = true
	w.mu.Unlock()
}
