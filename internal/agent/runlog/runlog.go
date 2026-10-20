// Package runlog 保存模型运行（非 PTY 的一次性命令）的完整输出。
//
// 输出只存在主机本地（INV-M4）：每一路（stdout / stderr）前 HeadBytes 字节完整保留，
// 之后只滚动保留最近 TailBytes 字节，中间被丢弃的部分只记录字节数。
// 这样既保住命令开头（报错通常在这里），也保住结尾（最终结果通常在这里），磁盘占用却有上限。
//
// 目录布局（<Dir> 通常是 <数据目录>/records/runs）：
//
//	<Dir>/<run id>/meta.json   运行信息与结果（原子更新）
//	<Dir>/<run id>/stdout.log  stdout 的开头部分（追加写）
//	<Dir>/<run id>/stdout.tail stdout 的结尾部分（周期性与结束时整体重写）
//	<Dir>/<run id>/stderr.*    同上
//
// 对运行中的运行，读取直接取自内存里的状态；已结束的运行取自文件。
//
// 磁盘写不下（写满、只读、句柄耗尽……）不能挡住命令本身（INV-R3）：Begin 先回收最旧的、Hub 已确认的运行腾出空间再试一次，
// 仍不行就退到「只在内存里保留」的运行：不落盘，只留预览与最近 TailBytes 字节（最多保留最近 maxMemDone 个已结束的这类运行供读取）。
package runlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 默认容量与保留策略（均可通过 Options 调整）。
const (
	DefaultHeadBytes = 8 << 20
	DefaultTailBytes = 256 << 10
	DefaultRetention = 7 * 24 * time.Hour
	DefaultQuota     = 256 << 20

	metaFile = "meta.json"
	// StateRunning 是 Meta.State 在运行期间的取值；结束后取 termproto.Run* 之一。
	StateRunning = "running"
)

var (
	ErrNotFound = errors.New("runlog: run not found")
	ErrExists   = errors.New("runlog: run already exists")
	ErrBadID    = errors.New("runlog: bad run id")
)

// Options 是存储的配置。零值字段取默认值。
type Options struct {
	Dir        string
	HeadBytes  int
	TailBytes  int
	Retention  time.Duration
	Quota      int64
	Now        func() time.Time
	Logf       func(format string, args ...any)
	Harden     func(dir string) error
	PreviewCap [2]int // 测试用：覆盖预览的 [开头, 结尾] 大小
}

// StreamMeta 是一路输出的统计。
type StreamMeta struct {
	Total int64 `json:"total"` // 该路产生的总字节数
	Head  int64 `json:"head"`  // 文件里保留的开头字节数
	Tail  int64 `json:"tail"`  // 文件里保留的结尾字节数（覆盖 [Total-Tail, Total)）
}

// Dropped 返回被丢弃的中间字节数。
func (m StreamMeta) Dropped() int64 {
	if d := m.Total - m.Head - m.Tail; d > 0 {
		return d
	}
	return 0
}

// Meta 是一次运行的信息与结果。
type Meta struct {
	ID       string     `json:"id"`
	Cmd      string     `json:"cmd"`
	Shell    string     `json:"shell,omitempty"`
	Cwd      string     `json:"cwd,omitempty"`
	PID      int        `json:"pid,omitempty"`
	Started  int64      `json:"started"` // Unix 毫秒
	State    string     `json:"state"`   // running | exited | signaled | timeout | cancelled | lost
	Ended    int64      `json:"ended,omitempty"`
	Code     *int       `json:"code,omitempty"`
	Signal   string     `json:"signal,omitempty"`
	DurMS    int64      `json:"dur_ms,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Out      StreamMeta `json:"out"`
	Err      StreamMeta `json:"err"`
	Reported bool       `json:"reported,omitempty"` // Hub 已确认收到结束事件

	// 以下四项在运行结束时一并落盘，使「重连后补报的结束事件」与首次上报的内容一致。
	// InlineOut/InlineErr 仅在对应输出不超过 RunInlineMax 时有值；Prev* 是合并两路输出的开头 / 结尾预览。
	InlineOut []byte `json:"inline_out,omitempty"`
	InlineErr []byte `json:"inline_err,omitempty"`
	PrevHead  []byte `json:"prev_head,omitempty"`
	PrevTail  []byte `json:"prev_tail,omitempty"`
}

func (m *Meta) stream(s byte) *StreamMeta {
	if s == termproto.StreamStderr {
		return &m.Err
	}
	return &m.Out
}

// Store 管理一台主机上的全部运行输出。
type Store struct {
	o    Options
	root string

	mu   sync.Mutex
	live map[string]*Writer
	// memDone 是已结束、但只在内存里保留输出的运行（磁盘写不下时才会出现），先进先出，最多 maxMemDone 个。
	memDone  map[string]*Writer
	memOrder []string
	// acked 记着「Hub 已确认收到结束事件，但这个标记没能写进磁盘」的运行（磁盘写满时 MarkReported 会失败）：
	// 本进程内不再补报它，空间紧张时也允许回收它。
	acked map[string]struct{}
}

// maxMemDone 是内存里最多保留多少个已结束的「只在内存里」的运行。
const maxMemDone = 16

// Open 打开（必要时创建）存储目录。
func Open(o Options) (*Store, error) {
	if o.Dir == "" {
		return nil, errors.New("runlog: empty dir")
	}
	if o.HeadBytes <= 0 {
		o.HeadBytes = DefaultHeadBytes
	}
	if o.TailBytes <= 0 {
		o.TailBytes = DefaultTailBytes
	}
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	if o.Quota <= 0 {
		o.Quota = DefaultQuota
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("runlog: %w", err)
	}
	if o.Harden != nil {
		if err := o.Harden(o.Dir); err != nil {
			return nil, fmt.Errorf("runlog: 收紧目录权限失败: %w", err)
		}
	}
	return &Store{o: o, root: o.Dir, live: make(map[string]*Writer), memDone: make(map[string]*Writer), acked: make(map[string]struct{})}, nil
}

func (s *Store) dir(id string) string { return filepath.Join(s.root, id) }

// Begin 为一次新运行创建目录与输出文件。同一 ID 已存在时返回 ErrExists。
// 磁盘上建不出来时不会让命令跑不了（INV-R3）：先回收旧运行腾空间再试一次，仍不行就退到只在内存里保留（见包注释）。
func (s *Store) Begin(meta Meta) (*Writer, error) {
	if !termproto.ValidRunID(meta.ID) {
		return nil, ErrBadID
	}
	if s.memWriter(meta.ID) != nil {
		return nil, ErrExists
	}
	w, err := s.begin(meta)
	if err == nil || errors.Is(err, ErrExists) {
		return w, err
	}
	if s.reclaim() > 0 {
		if w, err = s.begin(meta); err == nil || errors.Is(err, ErrExists) {
			return w, err
		}
	}
	s.o.Logf("无法在磁盘上保存运行输出（%s）：%v；这次运行只在内存里保留最近的输出", meta.ID[:8], err)
	return s.beginMemory(meta), nil
}

// begin 是 Begin 在磁盘上的那一半：失败时不留半成品。
func (s *Store) begin(meta Meta) (*Writer, error) {
	dir := s.dir(meta.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrExists
		}
		return nil, err
	}
	if s.o.Harden != nil {
		_ = s.o.Harden(dir)
	}
	meta.State = StateRunning
	if err := writeMeta(dir, &meta); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	w := &Writer{s: s, dir: dir, meta: meta}
	headCap, tailCap := termproto.RunPreviewHead, termproto.RunPreviewTail
	if s.o.PreviewCap[0] > 0 {
		headCap = s.o.PreviewCap[0]
	}
	if s.o.PreviewCap[1] > 0 {
		tailCap = s.o.PreviewCap[1]
	}
	w.prevCap = headCap
	w.prevTail = newRing(tailCap)
	for i, name := range [2]string{"stdout", "stderr"} {
		f, err := os.OpenFile(filepath.Join(dir, name+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			w.abort()
			return nil, err
		}
		w.st[i].file = f
		w.st[i].tail = newRing(s.o.TailBytes)
	}
	s.mu.Lock()
	s.live[meta.ID] = w
	s.mu.Unlock()
	return w, nil
}

// beginMemory 创建一个不落盘的写入器：所有输出只进内存里的结尾环，预览与内联输出照常。
func (s *Store) beginMemory(meta Meta) *Writer {
	meta.State = StateRunning
	w := &Writer{s: s, mem: true, meta: meta}
	w.prevCap, w.prevTail = termproto.RunPreviewHead, newRing(termproto.RunPreviewTail)
	if s.o.PreviewCap[0] > 0 {
		w.prevCap = s.o.PreviewCap[0]
	}
	if s.o.PreviewCap[1] > 0 {
		w.prevTail = newRing(s.o.PreviewCap[1])
	}
	for i := range w.st {
		w.st[i].tail = newRing(s.o.TailBytes)
	}
	s.mu.Lock()
	s.live[meta.ID] = w
	s.mu.Unlock()
	return w
}

// metaOf 返回一次已结束（或遗留）的运行的元信息：登记在内存里的（磁盘写不下时）以内存为准，否则读盘。
func (s *Store) metaOf(id string) (Meta, error) {
	if w := s.memWriter(id); w != nil {
		return w.snapshotMeta(), nil
	}
	return readMeta(s.dir(id))
}

// memWriter 返回已结束的「只在内存里」的运行的写入器。
func (s *Store) memWriter(id string) *Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memDone[id]
}

// keepInMemory 把刚结束的「只在内存里」的运行登记下来，超过 maxMemDone 个时挤掉最旧的。
func (s *Store) keepInMemory(w *Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.live, w.meta.ID)
	s.memDone[w.meta.ID] = w
	s.memOrder = append(s.memOrder, w.meta.ID)
	for len(s.memOrder) > maxMemDone {
		delete(s.memDone, s.memOrder[0])
		s.memOrder = s.memOrder[1:]
	}
}

// reclaim 在磁盘写不下时回收空间：从最旧的开始删除已结束、且 Hub 已确认收到结束事件的运行，删一半（至少一个）。
// 运行中的与结束事件还没被确认的一律不动。返回删除的运行数。
func (s *Store) reclaim() int {
	type ent struct {
		id    string
		ended int64
	}
	var list []ent
	for _, e := range s.entries() {
		if !termproto.ValidRunID(e) || s.liveWriter(e) != nil {
			continue
		}
		m, err := s.metaOf(e)
		if err != nil || m.State == StateRunning || !(m.Reported || s.isAcked(e)) {
			continue
		}
		list = append(list, ent{e, m.Ended})
	}
	if len(list) == 0 {
		return 0
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ended < list[j].ended })
	n := (len(list) + 1) / 2
	for _, e := range list[:n] {
		_ = os.RemoveAll(s.dir(e.id))
		s.mu.Lock()
		delete(s.memDone, e.id) // 回收就是不要了：内存里登记的也一并忘掉（memOrder 里的残留无害）
		s.mu.Unlock()
	}
	s.pruneAcked()
	return n
}

func (s *Store) liveWriter(id string) *Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

// Meta 返回一次运行的信息（运行中的取内存里的最新值）。
func (s *Store) Meta(id string) (Meta, error) {
	if !termproto.ValidRunID(id) {
		return Meta{}, ErrBadID
	}
	if w := s.liveWriter(id); w != nil {
		return w.snapshotMeta(), nil
	}
	if w := s.memWriter(id); w != nil {
		return w.snapshotMeta(), nil
	}
	return readMeta(s.dir(id))
}

// Page 是一次读取的结果。
type Page struct {
	Off     uint64 // 实际起点（起点落在被丢弃部分时会大于请求的偏移）
	Data    []byte
	Next    uint64 // 继续读取应使用的偏移
	Total   uint64 // 该路到目前为止产生的总字节数
	Gap     bool   // 请求的起点被丢弃，已从下一段可用内容开始
	EOF     bool   // 已读到末尾且运行已结束
	Running bool
}

// Read 从某一路输出的 off 起读取至多 max 字节。
func (s *Store) Read(id string, stream byte, off uint64, max int) (Page, error) {
	if !termproto.ValidRunID(id) {
		return Page{}, ErrBadID
	}
	if termproto.StreamName(stream) == "" {
		return Page{}, ErrBadID
	}
	if max <= 0 {
		max = termproto.DefaultRunRead
	}
	if max > termproto.MaxRunRead {
		max = termproto.MaxRunRead
	}
	if w := s.liveWriter(id); w != nil {
		return w.read(stream, off, max)
	}
	if w := s.memWriter(id); w != nil {
		return w.read(stream, off, max)
	}
	dir := s.dir(id)
	meta, err := readMeta(dir)
	if err != nil {
		return Page{}, err
	}
	sm := *meta.stream(stream)
	src := &fileSource{dir: dir, name: streamFile(stream)}
	return readRegions(src, sm.Total, sm.Head, sm.Tail, off, max, meta.State == StateRunning)
}

// Unreported 返回已结束但 Hub 尚未确认收到结束事件的运行（按结束时间从旧到新，最多 limit 条）。
func (s *Store) Unreported(limit int) []Meta {
	var out []Meta
	for _, e := range s.entries() {
		if !termproto.ValidRunID(e) || s.liveWriter(e) != nil || s.memWriter(e) != nil {
			continue // 内存里登记的由下面单独处理
		}
		m, err := readMeta(s.dir(e))
		if err != nil || m.State == StateRunning || m.Reported || s.isAcked(e) {
			continue
		}
		out = append(out, m)
	}
	s.mu.Lock()
	mem := make([]*Writer, 0, len(s.memDone))
	for _, w := range s.memDone {
		mem = append(mem, w)
	}
	s.mu.Unlock()
	for _, w := range mem {
		if m := w.snapshotMeta(); !m.Reported && m.State != StateRunning {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ended < out[j].Ended })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// MarkReported 记下 Hub 已确认收到结束事件。运行不存在时返回 ErrNotFound。
func (s *Store) MarkReported(id string) error {
	if !termproto.ValidRunID(id) {
		return ErrBadID
	}
	if w := s.liveWriter(id); w != nil {
		return nil // 还在运行，不可能已经结束
	}
	if w := s.memWriter(id); w != nil {
		w.markReported()
		return nil
	}
	dir := s.dir(id)
	m, err := readMeta(dir)
	if err != nil {
		return err
	}
	if m.Reported || m.State == StateRunning {
		return nil
	}
	m.Reported = true
	if err := writeMeta(dir, &m); err != nil {
		s.mu.Lock()
		s.acked[id] = struct{}{}
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Store) isAcked(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.acked[id]
	return ok
}

// pruneAcked 去掉目录已经不存在的运行的内存标记，免得它们无限增长。
func (s *Store) pruneAcked() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.acked))
	for id := range s.acked {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if _, err := os.Stat(s.dir(id)); err != nil {
			s.mu.Lock()
			delete(s.acked, id)
			s.mu.Unlock()
		}
	}
}

// RecoverOrphans 把上次没来得及收尾的运行（元信息仍是 running，但没有对应的写入器）标记为 lost，
// reason 说明为什么（主机重启 / 守护进程被停止 / 守护进程崩溃）。返回被标记的运行。
func (s *Store) RecoverOrphans(reason string) []Meta {
	var out []Meta
	now := s.o.Now()
	for _, e := range s.entries() {
		if !termproto.ValidRunID(e) || s.liveWriter(e) != nil {
			continue
		}
		dir := s.dir(e)
		m, err := readMeta(dir)
		if err != nil || m.State != StateRunning {
			continue
		}
		for _, st := range []byte{termproto.StreamStdout, termproto.StreamStderr} {
			sm := m.stream(st)
			name := streamFile(st)
			var head, tail int64
			if fi, err := os.Stat(filepath.Join(dir, name+".log")); err == nil {
				head = fi.Size()
			}
			if fi, err := os.Stat(filepath.Join(dir, name+".tail")); err == nil {
				tail = fi.Size()
			}
			// 守护进程崩溃时尾部文件可能比实际产生的少；总量只能按已落盘的估计。
			sm.Head, sm.Tail = head, tail
			sm.Total = max(sm.Total, head+tail)
		}
		// 输出很小、完整落在开头文件里时，补上内联全文：补报的结束事件才能与正常结束时一样直接带回结果。
		if m.Out.Total > 0 && m.Out.Total <= int64(termproto.RunInlineMax) && m.Out.Head >= m.Out.Total {
			m.InlineOut, _ = readFileAt(filepath.Join(dir, streamFile(termproto.StreamStdout)+".log"), 0, int(m.Out.Total))
		}
		if m.Err.Total > 0 && m.Err.Total <= int64(termproto.RunInlineMax) && m.Err.Head >= m.Err.Total {
			m.InlineErr, _ = readFileAt(filepath.Join(dir, streamFile(termproto.StreamStderr)+".log"), 0, int(m.Err.Total))
		}
		m.State = termproto.RunLost
		m.Reason = reason
		m.Ended = now.UnixMilli()
		if m.Started > 0 && m.Ended > m.Started {
			m.DurMS = m.Ended - m.Started
		}
		m.Reported = false
		if err := writeMeta(dir, &m); err != nil {
			s.o.Logf("标记遗留运行失败（%s）: %v", e[:8], err)
			continue
		}
		out = append(out, m)
	}
	return out
}

// Sweep 清理过期与超出配额的已结束运行（运行中的与尚未被 Hub 确认的结束事件之外的一律按时间和配额处理）。
// 返回删除的运行数。
func (s *Store) Sweep() int {
	type ent struct {
		id    string
		ended int64
		size  int64
	}
	now := s.o.Now()
	var list []ent
	var total int64
	removed := 0
	for _, e := range s.entries() {
		if !termproto.ValidRunID(e) {
			continue
		}
		if s.liveWriter(e) != nil {
			total += dirSize(s.dir(e))
			continue
		}
		dir := s.dir(e)
		m, err := s.metaOf(e)
		if err != nil {
			// 没有有效元信息的目录：留一天宽限（可能正在创建），之后清掉。
			if fi, serr := os.Stat(dir); serr == nil && now.Sub(fi.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(dir)
				removed++
			}
			continue
		}
		if m.State == StateRunning {
			continue // 刚被 RecoverOrphans 之外的路径遗留；留给下一轮
		}
		size := dirSize(dir)
		if m.Ended > 0 && now.Sub(time.UnixMilli(m.Ended)) > s.o.Retention {
			_ = os.RemoveAll(dir)
			removed++
			continue
		}
		total += size
		list = append(list, ent{id: e, ended: m.Ended, size: size})
	}
	if total > s.o.Quota {
		sort.Slice(list, func(i, j int) bool { return list[i].ended < list[j].ended })
		for _, e := range list {
			if total <= s.o.Quota {
				break
			}
			_ = os.RemoveAll(s.dir(e.id))
			total -= e.size
			removed++
		}
	}
	if removed > 0 {
		s.pruneAcked()
	}
	return removed
}

func (s *Store) entries() []string {
	des, err := os.ReadDir(s.root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(des))
	for _, de := range des {
		if de.IsDir() {
			out = append(out, de.Name())
		}
	}
	return out
}

func dirSize(dir string) int64 {
	var n int64
	des, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, de := range des {
		if fi, err := de.Info(); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
	}
	return n
}

func streamFile(stream byte) string {
	if stream == termproto.StreamStderr {
		return "stderr"
	}
	return "stdout"
}

func streamIdx(stream byte) int {
	if stream == termproto.StreamStderr {
		return 1
	}
	return 0
}

func writeMeta(dir string, m *Meta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, metaFile), data)
}

func readMeta(dir string) (Meta, error) {
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Meta{}, ErrNotFound
		}
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("runlog: 元信息损坏: %w", err)
	}
	return m, nil
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ---- 读取：按「开头 / 被丢弃的中间 / 结尾」三个区域定位 ----

// regionSource 给出开头与结尾两个区域的读取方式。
type regionSource interface {
	readHead(off int64, n int) ([]byte, error)
	readTail(off int64, n int) ([]byte, error) // off 相对结尾区域的起点
}

type fileSource struct {
	dir, name string
}

func (f *fileSource) readHead(off int64, n int) ([]byte, error) {
	return readFileAt(filepath.Join(f.dir, f.name+".log"), off, n)
}

func (f *fileSource) readTail(off int64, n int) ([]byte, error) {
	return readFileAt(filepath.Join(f.dir, f.name+".tail"), off, n)
}

func readFileAt(path string, off int64, n int) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer fh.Close()
	buf := make([]byte, n)
	got, err := fh.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:got], nil
}

// readRegions 在 [0,head) 开头区域、[total-tail,total) 结尾区域里定位并读取。
func readRegions(src regionSource, total, head, tail int64, off uint64, max int, running bool) (Page, error) {
	pg := Page{Total: uint64(total), Running: running, Off: off}
	if total < 0 {
		total = 0
	}
	o := int64(off)
	if off > uint64(total) {
		o = total
	}
	tailStart := total - tail
	switch {
	case o >= total:
		pg.Off, pg.Next = uint64(total), uint64(total)
	case o < head:
		n := int(min(int64(max), head-o))
		data, err := src.readHead(o, n)
		if err != nil {
			return pg, err
		}
		pg.Off, pg.Data, pg.Next = uint64(o), data, uint64(o)+uint64(len(data))
	case tail > 0 && o >= tailStart:
		n := int(min(int64(max), total-o))
		data, err := src.readTail(o-tailStart, n)
		if err != nil {
			return pg, err
		}
		pg.Off, pg.Data, pg.Next = uint64(o), data, uint64(o)+uint64(len(data))
	case tail > 0:
		// 起点落在被丢弃的中间部分：跳到结尾区域的开头。
		n := int(min(int64(max), tail))
		data, err := src.readTail(0, n)
		if err != nil {
			return pg, err
		}
		pg.Off, pg.Data, pg.Next, pg.Gap = uint64(tailStart), data, uint64(tailStart)+uint64(len(data)), true
	default:
		// 没有结尾区域（崩溃遗留）：只能报告已到末尾。
		pg.Off, pg.Next, pg.Gap = uint64(total), uint64(total), true
	}
	pg.EOF = !running && pg.Next >= uint64(total)
	return pg, nil
}
