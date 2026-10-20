package record

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var errPaused = errors.New("record: paused")

// lfByte 用来数输出里的换行符：终端回滚缓冲区的「行」就是以它为界。
var lfByte = []byte{'\n'}

// minLineChunk 是设了行数上限时分段提前封存的最小行数：太小会让分段文件过碎（每封存一次就要压缩一次）。
const minLineChunk = 500

// lineChunk 是设了行数上限 limit 时，一个分段容纳的输出行数的下限（上限的约十分之一）：
// 分段是丢弃的最小单位，分段够小，实际多保留的行数才能贴近上限（不超过上限的一成左右，再加最后一次写入带来的零头）。
func lineChunk(limit int64) int64 {
	if c := limit / 10; c > minLineChunk {
		return c
	}
	return minLineChunk
}

// Stats 是一个会话录制状态的快照（原子读取，可在持有其他锁时调用）。
type Stats struct {
	State    string // on | paused | error
	Reason   string // 暂停 / 出错的原因（Gap* 常量）
	Raw      int64  // 当前保留的原始字节数（含正在写的分段）
	FirstMS  int64  // 最早保留内容的时间（Unix 毫秒）
	LastMS   int64  // 最近一次写入的时间
	Gap      bool   // 记录不完整
	Dropped  int64  // 累计丢失 / 被清理的字节数
	Segments int
}

type ctlKind int

const (
	ctlFlush ctlKind = iota + 1
	ctlEnd
	ctlName
	ctlShrink
	ctlPolicy
)

type ctlReq struct {
	kind   ctlKind
	n      int64
	name   string
	off    bool
	code   *int
	signal string
	reason string
	done   chan struct{}
}

type item struct {
	kind       byte
	at         time.Time
	buf        *[]byte // 池化缓冲（输出事件），写完归还
	data       []byte
	size       int
	rows, cols uint16
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, 0, 32<<10); return &b }}

type segState struct {
	seq        uint32
	t0, t1     int64
	raw        int64
	lines      int64 // 本分段输出里的换行符个数，按行数限高时用；只在内存里记，不落盘
	rows, cols uint16
	sealed     bool
}

// Writer 把一个会话的终端显示内容异步写入记录。
//
// 所有写入方法都是非阻塞的：队列满时丢弃并在记录里留下「缺口」标记；磁盘错误只会让记录暂停，
// 绝不会向调用方（PTY 读循环）传播。*Writer 为 nil 时全部方法都是无操作。
type Writer struct {
	st  *Store
	sid string
	dir string

	dataCh chan item
	ctlCh  chan ctlReq
	done   chan struct{}

	queued   atomic.Int64
	lost     atomic.Int64
	closed   atomic.Bool
	rawTotal atomic.Int64
	firstMS  atomic.Int64
	lastMS   atomic.Int64
	gapFlag  atomic.Bool
	dropped  atomic.Int64
	segCount atomic.Int32
	stateV   atomic.Value // string
	reasonV  atomic.Value // string

	// 以下字段只由写协程访问。
	meta         Meta
	f            *os.File
	bw           *bufio.Writer
	jf           *os.File
	jsize        int64
	scratch      []byte
	nextSeq      uint32
	segRaw       int64
	segLines     int64 // 当前分段已写入的输出行数
	linesSum     int64 // 保留着的全部分段（含当前分段）的输出行数
	lastEvMS     int64
	rows, cols   uint16
	segs         []segState
	rawSum       int64
	dropRaw      int64
	gapBytes     int64
	gaps         int
	policyOff    bool
	autoPause    string
	pauseReason  string
	pauseDropped int64
	retryAt      time.Time
	backoff      time.Duration
	dirty        bool
	lastDisk     time.Time
	logAt        time.Time
}

func newWriter(st *Store, m Meta) (*Writer, error) {
	dir := st.SessionDir(m.SID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	m.CreatedMS = st.o.Now().UnixMilli()
	m.EndedMS, m.EndedReason, m.ExitCode, m.Signal = 0, "", nil, ""
	if err := writeMeta(dir, m); err != nil {
		return nil, err
	}
	jf, err := os.OpenFile(filepath.Join(dir, journalFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		st:      st,
		sid:     m.SID,
		dir:     dir,
		dataCh:  make(chan item, 1024),
		ctlCh:   make(chan ctlReq, 16),
		done:    make(chan struct{}),
		meta:    m,
		jf:      jf,
		nextSeq: 1,
		rows:    m.Rows,
		cols:    m.Cols,
		backoff: 2 * time.Second,
	}
	w.stateV.Store(StateOn)
	w.reasonV.Store("")
	go w.run()
	return w, nil
}

// ---------- 生产者接口（非阻塞）----------

func (w *Writer) alive() bool { return w != nil && !w.closed.Load() }

// Output 记录一段终端输出。p 会被复制，调用方可以立即复用。
func (w *Writer) Output(p []byte) {
	if !w.alive() || len(p) == 0 {
		return
	}
	n := int64(len(p))
	if w.queued.Add(n) > w.st.o.QueueBytes {
		w.queued.Add(-n)
		w.lost.Add(n)
		return
	}
	bp := bufPool.Get().(*[]byte)
	b := append((*bp)[:0], p...)
	*bp = b
	select {
	case w.dataCh <- item{kind: KindOutput, at: w.st.o.Now(), buf: bp, data: b, size: len(p)}:
	default:
		w.queued.Add(-n)
		w.lost.Add(n)
		bufPool.Put(bp)
	}
}

// Resize 记录窗口尺寸变化。
func (w *Writer) Resize(rows, cols uint16) {
	if !w.alive() {
		return
	}
	select {
	case w.dataCh <- item{kind: KindResize, at: w.st.o.Now(), rows: rows, cols: cols}:
	default:
	}
}

// Flush 等待已入队的事件写入并刷盘（读取进行中会话的记录前调用，让读者看到最新内容）。
func (w *Writer) Flush() {
	if !w.alive() {
		return
	}
	rq := ctlReq{kind: ctlFlush, done: make(chan struct{})}
	if !w.send(rq, time.Second) {
		return
	}
	select {
	case <-rq.done:
	case <-w.done:
	case <-time.After(2 * time.Second):
	}
}

// SetName 更新会话名称（写入 meta.json）。
func (w *Writer) SetName(name string) {
	if w.alive() {
		w.send(ctlReq{kind: ctlName, name: name}, 200*time.Millisecond)
	}
}

// End 结束录制：写入结束标记、封存最后一个分段、在 meta.json 里记下结束时间与原因。
// 最多等待数秒，写协程卡住也不会拖住调用方。
func (w *Writer) End(code *int, signal, reason string) {
	if w == nil {
		return
	}
	rq := ctlReq{kind: ctlEnd, code: code, signal: signal, reason: reason, done: make(chan struct{})}
	if !w.send(rq, 2*time.Second) {
		return
	}
	select {
	case <-rq.done:
	case <-w.done:
	case <-time.After(5 * time.Second):
	}
}

// Stats 返回当前录制状态。
func (w *Writer) Stats() Stats {
	if w == nil {
		return Stats{}
	}
	st, _ := w.stateV.Load().(string)
	rs, _ := w.reasonV.Load().(string)
	return Stats{
		State: st, Reason: rs,
		Raw: w.rawTotal.Load(), FirstMS: w.firstMS.Load(), LastMS: w.lastMS.Load(),
		Gap: w.gapFlag.Load(), Dropped: w.dropped.Load(), Segments: int(w.segCount.Load()),
	}
}

func (w *Writer) setPolicyOff(off bool) {
	if w.alive() {
		w.send(ctlReq{kind: ctlPolicy, off: off}, time.Second)
	}
}

// ctl 向写协程发送一个内部控制请求（Store 使用）。
func (w *Writer) ctl(kind ctlKind, n int64) {
	if w.alive() {
		w.send(ctlReq{kind: kind, n: n}, time.Second)
	}
}

func (w *Writer) send(rq ctlReq, wait time.Duration) bool {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case w.ctlCh <- rq:
		return true
	case <-w.done:
		return false
	case <-t.C:
		return false
	}
}

// ---------- 写协程 ----------

// retire 把本写协程标记为已结束，并从 Store 的「正在录制」表里摘掉（幂等）。
// 正常结束时 handleCtl 在通知 End 的调用方之前就调用它：否则 End 返回时写协程还没来得及退出，
// 调用方紧接着 Delete / 主机配额清理会撞上「仍在录制」（2026-10-20 在高负载下复现过
// TestDeleteRefusesLiveSessions 偶发失败）。写协程退出时的收尾里还会再调一次，保证异常退出也摘得掉。
func (w *Writer) retire() {
	w.closed.Store(true)
	w.st.removeLive(w.sid)
}

func (w *Writer) run() {
	defer func() {
		if r := recover(); r != nil {
			w.st.logf("记录写协程异常（已停止录制该会话）: %v", r)
		}
		w.retire()
		w.st.kickHost()
		close(w.done)
	}()
	tick := time.NewTicker(w.st.o.FlushEvery)
	defer tick.Stop()
	for {
		select {
		case it := <-w.dataCh:
			w.handle(it)
		case rq := <-w.ctlCh:
			if w.handleCtl(rq) {
				return
			}
		case <-tick.C:
			w.tick()
		}
	}
}

func (w *Writer) drain() {
	for {
		select {
		case it := <-w.dataCh:
			w.handle(it)
		default:
			return
		}
	}
}

func (w *Writer) handleCtl(rq ctlReq) (stop bool) {
	switch rq.kind {
	case ctlFlush:
		w.drain()
		w.flush(w.st.o.Now())
	case ctlName:
		w.meta.Name = rq.name
		if err := writeMeta(w.dir, w.meta); err != nil {
			w.logRate("更新记录元数据失败: %v", err)
		}
	case ctlShrink:
		w.shrink(rq.n)
	case ctlPolicy:
		w.applyPolicy(rq.off, w.st.o.Now())
	case ctlEnd:
		w.finish(rq)
		w.retire()
		if rq.done != nil {
			close(rq.done)
		}
		return true
	}
	if rq.done != nil {
		close(rq.done)
	}
	return false
}

func (w *Writer) handle(it item) {
	if it.size > 0 {
		w.queued.Add(-int64(it.size))
	}
	if n := w.lost.Swap(0); n > 0 {
		w.noteGap(GapBacklog, n, it.at, false)
	}
	switch it.kind {
	case KindOutput:
		data := it.data
		for len(data) > 0 {
			n := len(data)
			if n > MaxEventPayload {
				n = MaxEventPayload
			}
			w.emit(KindOutput, it.at, data[:n])
			data = data[n:]
		}
		if it.buf != nil {
			bufPool.Put(it.buf)
		}
	case KindResize:
		w.rows, w.cols = it.rows, it.cols
		w.emit(KindResize, it.at, encodeResize(it.rows, it.cols))
	}
}

func (w *Writer) paused() bool { return w.policyOff || w.autoPause != "" }

// emit 写入一个事件；暂停期间丢弃并统计丢失的输出字节。
func (w *Writer) emit(kind byte, at time.Time, payload []byte) {
	if !w.paused() && w.f == nil {
		_ = w.openSegment(at)
	}
	if w.paused() || w.f == nil {
		if kind == KindOutput {
			w.pauseDropped += int64(len(payload))
			w.dropped.Add(int64(len(payload)))
		}
		return
	}
	w.write(kind, at, payload)
}

func (w *Writer) write(kind byte, at time.Time, payload []byte) {
	ms := at.UnixMilli()
	var dt uint64
	if ms > w.lastEvMS {
		dt = uint64(ms - w.lastEvMS)
	}
	w.scratch = AppendEvent(w.scratch[:0], kind, dt, payload)
	if _, err := w.bw.Write(w.scratch); err != nil {
		w.failWrite(err, at)
		return
	}
	n := int64(len(w.scratch))
	w.segRaw += n
	w.rawSum += n
	w.rawTotal.Store(w.rawSum)
	if kind == KindOutput {
		if k := int64(bytes.Count(payload, lfByte)); k > 0 {
			w.segLines += k
			w.linesSum += k
		}
	}
	if ms > w.lastEvMS {
		w.lastEvMS = ms
	}
	w.lastMS.Store(w.lastEvMS)
	w.dirty = true
	if w.segRaw >= w.st.o.SegmentBytes || w.segLinesFull() {
		w.rollSegment(at)
	}
}

// segLinesFull 判断当前分段是否该因「行数上限」提前封存：设了上限时，分段只容纳上限的约十分之一（见 lineChunk）。
func (w *Writer) segLinesFull() bool {
	lim := w.st.SessionLineLimit()
	return lim > 0 && w.segLines >= lineChunk(lim)
}

// openSegment 创建下一个分段文件并写入文件头与分段起点标记。
func (w *Writer) openSegment(at time.Time) error {
	if w.diskLow() {
		w.pauseAuto(GapDiskLow, at)
		return errPaused
	}
	seq := w.nextSeq
	f, err := os.OpenFile(filepath.Join(w.dir, segName(seq)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		w.failWrite(err, at)
		return err
	}
	w.nextSeq++
	w.f = f
	w.bw = bufio.NewWriterSize(f, 64<<10)
	if _, err := w.bw.WriteString(segMagic); err != nil {
		w.failWrite(err, at)
		return err
	}
	ms := at.UnixMilli()
	w.segRaw = int64(len(segMagic))
	w.segLines = 0
	w.rawSum += w.segRaw
	w.lastEvMS = ms
	w.segs = append(w.segs, segState{seq: seq, t0: ms, rows: w.rows, cols: w.cols})
	w.segCount.Store(int32(len(w.segs)))
	if w.firstMS.Load() == 0 {
		w.firstMS.Store(ms)
	}
	w.journal(journalLine{K: "seg", Seq: seq, T0: ms, Rows: w.rows, Cols: w.cols})
	// 分段起点标记：让每个分段自描述，从任意分段开始回放都能还原窗口尺寸。
	w.write(KindMark, at, encodeMark(Mark{T: MarkSeg, Seq: seq, T0: ms, Rows: w.rows, Cols: w.cols}))
	return nil
}

// rollSegment 封存当前分段（之后由后台压缩），并按单会话配额清理最旧分段。
func (w *Writer) rollSegment(at time.Time) {
	w.closeSegment(at)
	w.enforceQuota()
	w.st.kickHost()
}

func (w *Writer) closeSegment(at time.Time) {
	if w.f == nil {
		return
	}
	ferr := w.bw.Flush()
	cerr := w.f.Close()
	path := w.f.Name()
	w.f, w.bw = nil, nil
	w.dirty = false
	if n := len(w.segs); n > 0 {
		s := &w.segs[n-1]
		s.sealed, s.raw, s.t1 = true, w.segRaw, w.lastEvMS
		s.lines = w.segLines
		w.journal(journalLine{K: "seal", Seq: s.seq, T1: s.t1, Raw: s.raw})
	}
	w.segRaw = 0
	w.segLines = 0
	if ferr != nil || cerr != nil {
		w.failWrite(errors.Join(ferr, cerr), at)
		return
	}
	w.st.enqueueCompress(path)
}

func (w *Writer) flush(at time.Time) {
	if w.bw != nil && w.dirty {
		if err := w.bw.Flush(); err != nil {
			w.failWrite(err, at)
			return
		}
		w.dirty = false
	}
}

// enforceQuota 丢弃最旧的已封存分段，直到单会话保留量（字节数，设了行数上限时还有行数）回到上限以内（至少保留一个分段）。
func (w *Writer) enforceQuota() {
	limit := w.st.sessionBytes()
	for w.rawSum > limit && len(w.segs) > 1 && w.segs[0].sealed {
		w.dropOldest()
	}
	// 行数上限（可选）：和字节上限互相独立，哪个先到就按哪个丢；同样只丢整段、至少保留一个分段。
	// 语义和终端的回滚行数一致：最近 lim 行一定保留；丢掉最旧的整段之后还剩不到 lim 行时就不丢，
	// 所以稳态下实际保留的行数落在 [lim, lim + 一个分段) 之内（设了上限之后写入的分段只有 lineChunk 行）。
	// 设置上限之前就已写下的分段可能大得多（最大 SegmentBytes）：它们要等后续输出累计满 lim 行之后才会被丢弃，
	// 所以刚设置 / 刚调小时保留的行数会暂时超出，随后新增约 2×lim 行输出即回到稳态。
	if lim := w.st.SessionLineLimit(); lim > 0 {
		for len(w.segs) > 1 && w.segs[0].sealed && w.linesSum-w.segs[0].lines >= lim {
			w.dropOldest()
		}
	}
}

// shrink 为整机配额腾出至少 need 字节的磁盘空间（need 为 0 时只按单会话配额重新检查）。
func (w *Writer) shrink(need int64) {
	if need <= 0 {
		w.enforceQuota()
		return
	}
	var freed int64
	for freed < need && len(w.segs) > 1 && w.segs[0].sealed {
		freed += w.segDisk(w.segs[0].seq)
		w.dropOldest()
	}
}

func (w *Writer) segDisk(seq uint32) int64 {
	for _, n := range []string{segNameGz(seq), segName(seq)} {
		if fi, err := os.Stat(filepath.Join(w.dir, n)); err == nil {
			return fi.Size()
		}
	}
	return 0
}

func (w *Writer) dropOldest() {
	s := w.segs[0]
	w.segs = w.segs[1:]
	for _, n := range []string{segName(s.seq), segNameGz(s.seq), segNameGz(s.seq) + ".tmp"} {
		_ = os.Remove(filepath.Join(w.dir, n))
	}
	w.rawSum -= s.raw
	w.linesSum -= s.lines
	w.dropRaw += s.raw
	w.dropped.Add(s.raw)
	w.rawTotal.Store(w.rawSum)
	w.segCount.Store(int32(len(w.segs)))
	w.gapFlag.Store(true)
	if len(w.segs) > 0 {
		w.firstMS.Store(w.segs[0].t0)
	}
	w.journal(journalLine{K: "drop", Seq: s.seq, Raw: s.raw})
	if w.jsize > journalCompactAt {
		w.compactJournal()
	}
}

// ---------- 暂停 / 恢复 / 缺口 ----------

func (w *Writer) updateState() {
	state, reason := StateOn, ""
	switch {
	case w.autoPause == GapWriteError:
		state, reason = StateError, GapWriteError
	case w.policyOff:
		state, reason = StatePaused, GapPolicyOff
	case w.autoPause != "":
		state, reason = StatePaused, w.autoPause
	}
	w.stateV.Store(state)
	w.reasonV.Store(reason)
}

func (w *Writer) beginPause(reason string) {
	if !w.paused() {
		w.pauseReason = reason
		w.pauseDropped = 0
	}
}

func (w *Writer) pauseAuto(reason string, at time.Time) {
	w.flush(at)
	w.beginPause(reason)
	w.autoPause = reason
	w.updateState()
}

func (w *Writer) applyPolicy(off bool, at time.Time) {
	if off == w.policyOff {
		return
	}
	if off {
		w.flush(at)
		w.beginPause(GapPolicyOff)
		w.policyOff = true
		w.updateState()
		return
	}
	w.policyOff = false
	w.updateState()
	w.maybeResume(at)
}

// maybeResume 在所有暂停原因都消除后恢复录制，并在记录里补一个缺口标记。
func (w *Writer) maybeResume(at time.Time) {
	if w.paused() {
		return
	}
	reason, n := w.pauseReason, w.pauseDropped
	w.pauseReason, w.pauseDropped = "", 0
	if reason == "" {
		return
	}
	w.noteGap(reason, n, at, true)
}

// noteGap 在记录里写入缺口标记并更新统计。
func (w *Writer) noteGap(reason string, bytes int64, at time.Time, counted bool) {
	w.gaps++
	w.gapBytes += bytes
	if !counted { // 队列溢出丢失的字节还没计入累计丢失量；暂停期间丢弃的字节在丢弃时已经计过
		w.dropped.Add(bytes)
	}
	w.gapFlag.Store(true)
	w.journal(journalLine{K: "gap", T0: at.UnixMilli(), Reason: reason, Bytes: bytes})
	w.emit(KindMark, at, encodeMark(Mark{T: MarkGap, Reason: reason, Bytes: bytes}))
}

// failWrite 处理写文件失败：关闭当前分段、进入 error 状态并按退避重试。绝不向外传播。
func (w *Writer) failWrite(err error, at time.Time) {
	w.logRate("记录写入失败（将重试）: %v", err)
	if w.f != nil {
		_ = w.f.Close()
		w.f, w.bw = nil, nil
		if n := len(w.segs); n > 0 && !w.segs[n-1].sealed {
			s := &w.segs[n-1]
			s.sealed, s.raw, s.t1 = true, w.segRaw, w.lastEvMS
			s.lines = w.segLines
		}
		w.segRaw = 0
		w.segLines = 0
	}
	w.beginPause(GapWriteError)
	w.autoPause = GapWriteError
	w.retryAt = at.Add(w.backoff)
	if w.backoff < time.Minute {
		w.backoff *= 2
	}
	w.dirty = false
	w.updateState()
}

func (w *Writer) logRate(format string, args ...any) {
	now := time.Now()
	if now.Sub(w.logAt) < 10*time.Second {
		return
	}
	w.logAt = now
	w.st.logf("会话 %s: "+format, append([]any{w.sid[:8]}, args...)...)
}

func (w *Writer) diskLow() bool {
	min := w.st.o.MinFree
	if min <= 0 {
		return false
	}
	free, err := w.st.o.FreeSpace(w.st.root)
	if err != nil {
		return false
	}
	return int64(free) < min
}

// tick 周期性刷盘、检查磁盘空间并在写错误后重试。
func (w *Writer) tick() {
	now := w.st.o.Now()
	w.flush(now)
	if now.Sub(w.lastDisk) >= diskCheckEvery {
		w.lastDisk = now
		w.checkDisk(now)
	}
	if w.autoPause == GapWriteError && !now.Before(w.retryAt) {
		w.autoPause = ""
		if err := w.openSegment(now); err == nil {
			w.backoff = 2 * time.Second
			w.updateState()
			w.maybeResume(now)
		}
	}
}

func (w *Writer) checkDisk(now time.Time) {
	min := w.st.o.MinFree
	if min <= 0 {
		return
	}
	free, err := w.st.o.FreeSpace(w.st.root)
	if err != nil {
		return
	}
	switch {
	case int64(free) < min && w.autoPause == "":
		w.pauseAuto(GapDiskLow, now)
	case w.autoPause == GapDiskLow && int64(free) >= min+min/8:
		w.autoPause = ""
		w.updateState()
		w.maybeResume(now)
	}
}

// ---------- 收尾 ----------

func (w *Writer) finish(rq ctlReq) {
	w.drain()
	now := w.st.o.Now()
	if w.autoPause == GapWriteError {
		w.autoPause = "" // 收尾是最后一次机会：尝试正常写入结束标记
	}
	if !w.paused() {
		w.emit(KindMark, now, encodeMark(Mark{T: MarkEnd, Reason: rq.reason, Code: rq.code, Signal: rq.signal}))
	}
	w.closeSegment(now)
	w.enforceQuota()
	w.meta.EndedMS = now.UnixMilli()
	w.meta.EndedReason = rq.reason
	w.meta.ExitCode = rq.code
	w.meta.Signal = rq.signal
	if err := writeMeta(w.dir, w.meta); err != nil {
		w.logRate("写入记录结束信息失败: %v", err)
	}
	if w.jf != nil {
		_ = w.jf.Close()
		w.jf = nil
	}
}

// ---------- 日志 ----------

func (w *Writer) journal(l journalLine) {
	if w.jf == nil {
		return
	}
	b, err := json.Marshal(l)
	if err != nil {
		return
	}
	b = append(b, '\n')
	n, err := w.jf.Write(b)
	w.jsize += int64(n)
	if err != nil {
		w.logRate("写入记录日志失败: %v", err)
	}
}

// compactJournal 用当前保留的分段重写日志，避免长时间滚动的会话让日志无限增长。
func (w *Writer) compactJournal() {
	path := filepath.Join(w.dir, journalFile)
	tmp := path + ".tmp"
	var buf []byte
	add := func(l journalLine) {
		b, _ := json.Marshal(l)
		buf = append(append(buf, b...), '\n')
	}
	add(journalLine{K: "base", Raw: w.dropRaw, Bytes: w.gapBytes, Gaps: w.gaps})
	for _, s := range w.segs {
		add(journalLine{K: "seg", Seq: s.seq, T0: s.t0, Rows: s.rows, Cols: s.cols})
		if s.sealed {
			add(journalLine{K: "seal", Seq: s.seq, T1: s.t1, Raw: s.raw})
		}
	}
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return
	}
	if w.jf != nil {
		_ = w.jf.Close()
		w.jf = nil
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	} else {
		w.jsize = int64(len(buf))
	}
	jf, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		w.logRate("重新打开记录日志失败: %v", err)
		return
	}
	w.jf = jf
}

// String 便于日志与测试。
func (s Stats) String() string {
	return fmt.Sprintf("%s raw=%d dropped=%d segs=%d", s.State, s.Raw, s.Dropped, s.Segments)
}
