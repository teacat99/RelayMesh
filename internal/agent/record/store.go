package record

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 默认容量与保留策略（均可通过 Options 调整）。
const (
	DefaultSegmentBytes = 1 << 20
	DefaultSessionBytes = 64 << 20
	DefaultHostBytes    = 512 << 20
	DefaultRetention    = 30 * 24 * time.Hour
	DefaultMinFree      = 1 << 30
	defaultQueueBytes   = 4 << 20
	defaultFlushEvery   = 250 * time.Millisecond
	defaultGCInterval   = 10 * time.Minute
	diskCheckEvery      = 2 * time.Second
	hostCheckMinGap     = 5 * time.Second
	journalCompactAt    = 128 << 10
	minSegmentBytes     = 4 << 10
)

// 会话的录制状态（SessionInfo.Rec）。
const (
	StateOn        = "on"        // 正在录制
	StatePaused    = "paused"    // 暂停：磁盘不足或服务端策略临时关闭，恢复后会补缺口标记
	StateError     = "error"     // 写文件失败，重试中
	StateOff       = "off"       // 服务端策略关闭了录制，本会话没有录
	StateVeto      = "veto"      // 本机以 --no-record 一票否决
	StateIncognito = "incognito" // 创建会话时选择了无痕
)

// Options 是记录存储的配置。零值字段取默认值。
type Options struct {
	// Dir 是记录根目录（通常是 <数据目录>/records）。
	Dir string
	// Disabled 是本机一票否决：不录制任何会话（已有记录仍可读取，直到过期）。
	Disabled bool

	SegmentBytes int64         // 分段大小，默认 1 MiB
	SessionBytes int64         // 单会话保留的原始字节上限，默认 64 MiB（滚动，丢弃最旧分段）
	SessionLines int64         // 单会话至少保留最近多少行输出（按换行符计；超出后滚动丢弃最旧分段，实际多保留不超过一个分段）；0 = 不限制（默认）
	HostBytes    int64         // 整机磁盘占用上限，默认 512 MiB
	Retention    time.Duration // 会话结束后的保留期，默认 30 天
	MinFree      int64         // 磁盘剩余空间低于此值停止录制，默认 1 GiB；负数表示关闭保护
	QueueBytes   int64         // 每会话异步队列的字节上限，默认 4 MiB
	FlushEvery   time.Duration // 缓冲刷盘间隔，默认 250 ms
	GCInterval   time.Duration // 保留期与配额清理的周期，默认 10 分钟
	NoCompress   bool          // 关闭后台压缩（测试用）

	// RecoverReason 是启动时对「上次未正常收尾」的会话写入的结束原因，默认 holder_exit。
	RecoverReason string
	// DeferRecover 为 true 时 Open 不做启动恢复，由调用方在弄清每个会话的结束原因之后调用 Recover。
	DeferRecover bool

	Now       func() time.Time
	FreeSpace func(dir string) (uint64, error)
	// Harden 在目录创建之后收紧访问权限（Windows 上设置 ACL）；Unix 上目录权限已是 0700。
	Harden func(dir string) error
	Logf   func(format string, args ...any)
}

// Policy 是服务端下发的录制策略：只能比本机配置更严格——可以关闭、缩短保留、降低配额，
// 永远不能强制开启或放宽。
type Policy struct {
	Off           bool
	RetentionDays int
	SessionBytes  int64
	SessionLines  int64
	HostBytes     int64
}

// Store 管理一台主机上的全部会话记录。
type Store struct {
	o      Options
	root   string // <Dir>/sessions
	reader *Reader

	mu     sync.Mutex
	live   map[string]*Writer
	policy Policy
	closed bool

	// lineLimit 是当前生效的单会话行数上限（本机配置与服务端策略取更严的一方，0 表示不限制）。
	// 写入热路径每个事件都要读它，所以放在原子变量里，改配置时重算。
	lineLimit atomic.Int64

	compressQ chan compressJob
	hostKick  chan struct{}
	stop      chan struct{}
	wg        sync.WaitGroup
}

type compressJob struct{ path string }

// Open 打开（必要时创建）记录存储：收紧目录权限、收尾上次遗留的会话、启动后台压缩与清理。
func Open(o Options) (*Store, error) {
	if o.Dir == "" {
		return nil, errors.New("record: empty dir")
	}
	if o.SegmentBytes <= 0 {
		o.SegmentBytes = DefaultSegmentBytes
	}
	if o.SegmentBytes < minSegmentBytes {
		o.SegmentBytes = minSegmentBytes // 分段头与起点标记必须放得进一个分段
	}
	if o.SessionBytes <= 0 {
		o.SessionBytes = DefaultSessionBytes
	}
	if o.SessionLines < 0 {
		o.SessionLines = 0
	}
	if o.HostBytes <= 0 {
		o.HostBytes = DefaultHostBytes
	}
	if o.Retention <= 0 {
		o.Retention = DefaultRetention
	}
	if o.MinFree == 0 {
		o.MinFree = DefaultMinFree
	}
	if o.QueueBytes <= 0 {
		o.QueueBytes = defaultQueueBytes
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = defaultFlushEvery
	}
	if o.GCInterval <= 0 {
		o.GCInterval = defaultGCInterval
	}
	if o.RecoverReason == "" {
		o.RecoverReason = "holder_exit"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.FreeSpace == nil {
		o.FreeSpace = freeSpace
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}

	root := filepath.Join(o.Dir, "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create record dir: %w", err)
	}
	_ = os.Chmod(o.Dir, 0o700)
	_ = os.Chmod(root, 0o700)
	if o.Harden != nil {
		if err := o.Harden(o.Dir); err != nil {
			return nil, fmt.Errorf("harden record dir: %w", err)
		}
	}

	s := &Store{
		o:         o,
		root:      root,
		reader:    &Reader{root: root},
		live:      make(map[string]*Writer),
		compressQ: make(chan compressJob, 128),
		hostKick:  make(chan struct{}, 1),
		stop:      make(chan struct{}),
	}
	s.lineLimit.Store(o.SessionLines)
	if !o.DeferRecover {
		s.Recover(nil)
	}
	if !o.NoCompress {
		s.wg.Add(1)
		go s.compressLoop()
	}
	s.wg.Add(1)
	go s.maintainLoop()
	return s, nil
}

// Close 收尾仍在录制的会话并停止后台任务。
func (s *Store) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	ws := make([]*Writer, 0, len(s.live))
	for _, w := range s.live {
		ws = append(ws, w)
	}
	s.mu.Unlock()
	for _, w := range ws {
		w.End(nil, "", "holder_stopped")
	}
	close(s.stop)
	s.wg.Wait()
}

// Disabled 报告本机是否以一票否决关闭了录制。
func (s *Store) Disabled() bool { return s.o.Disabled }

// SessionDir 返回会话记录目录（不检查是否存在）。sid 必须通过 ValidSID。
func (s *Store) SessionDir(sid string) string { return filepath.Join(s.root, sid) }

func (s *Store) logf(format string, args ...any) { s.o.Logf(format, args...) }

// ---------- 有效配额 = min(本机, 服务端策略) ----------

func (s *Store) sessionBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.o.SessionBytes
	if p := s.policy.SessionBytes; p > 0 && p < v {
		v = p
	}
	return v
}

// SessionLineLimit 是生效的单会话行数上限：本机配置与服务端策略都为正时取较小者，只有一方为正时取那一方，
// 都没设则为 0（不限制）。服务端只能收紧、不能放宽——本机设了上限，策略再大也不会让它变松。
func (s *Store) SessionLineLimit() int64 { return s.lineLimit.Load() }

// refreshLineLimit 在配置或策略变化后重算生效的行数上限（调用方持有 s.mu）。
func (s *Store) refreshLineLimit() {
	v := s.o.SessionLines
	if p := s.policy.SessionLines; p > 0 && (v <= 0 || p < v) {
		v = p
	}
	s.lineLimit.Store(v)
}

func (s *Store) hostBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.o.HostBytes
	if p := s.policy.HostBytes; p > 0 && p < v {
		v = p
	}
	return v
}

func (s *Store) retention() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.o.Retention
	if d := time.Duration(s.policy.RetentionDays) * 24 * time.Hour; d > 0 && d < v {
		v = d
	}
	return v
}

func (s *Store) policyOff() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy.Off
}

// SetPolicy 应用服务端策略。关闭录制会让正在录制的会话暂停（之后恢复时补缺口标记）；
// 配额与保留期变严会立即触发一次清理。
func (s *Store) SetPolicy(p Policy) {
	s.mu.Lock()
	prev := s.policy
	s.policy = p
	s.refreshLineLimit()
	ws := make([]*Writer, 0, len(s.live))
	for _, w := range s.live {
		ws = append(ws, w)
	}
	s.mu.Unlock()
	if prev.Off != p.Off {
		for _, w := range ws {
			w.setPolicyOff(p.Off)
		}
	}
	if prev.SessionBytes != p.SessionBytes || prev.SessionLines != p.SessionLines {
		for _, w := range ws {
			w.ctl(ctlShrink, 0)
		}
	}
	s.kickHost()
}

// ---------- 开始录制 ----------

// Begin 为新会话开始录制。返回的 Writer 可能为 nil（nil 的 Writer 上所有方法都是无操作），
// 此时 state 说明原因：veto（本机否决）/ off（服务端策略关闭）/ incognito（无痕）/ error（目录不可写）。
func (s *Store) Begin(m Meta) (w *Writer, state string) {
	switch {
	case s.o.Disabled:
		return nil, StateVeto
	case m.Incognito:
		return nil, StateIncognito
	case s.policyOff():
		return nil, StateOff
	case !ValidSID(m.SID):
		return nil, StateError
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, StateError
	}
	wr, err := newWriter(s, m)
	if err != nil {
		s.logf("开始录制失败（会话 %s）: %v", m.SID[:8], err)
		return nil, StateError
	}
	s.mu.Lock()
	s.live[m.SID] = wr
	s.mu.Unlock()
	return wr, StateOn
}

func (s *Store) removeLive(sid string) {
	s.mu.Lock()
	delete(s.live, sid)
	s.mu.Unlock()
}

func (s *Store) isLive(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.live[sid]
	return ok
}

// ---------- 读取 ----------

// Reader 返回只读视图。
func (s *Store) Reader() *Reader { return s.reader }

// Info 返回会话记录的索引信息；会话仍在录制时先刷盘，让结果包含最新内容。
func (s *Store) Info(sid string) (*Info, error) {
	if w := s.liveWriter(sid); w != nil {
		w.Flush()
	}
	return s.reader.Info(sid)
}

// Read 读取一页记录内容，见 readPage。
func (s *Store) Read(sid string, seg, off uint32, max int) (*Page, error) {
	if w := s.liveWriter(sid); w != nil {
		w.Flush()
	}
	return s.reader.Read(sid, seg, off, max)
}

func (s *Store) liveWriter(sid string) *Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[sid]
}

// List 返回全部会话记录的索引信息（按创建时间升序）。
func (s *Store) List() []*Info { return s.reader.List() }

// Delete 删除一个已结束会话的全部记录；仍在录制的会话不可删除。
func (s *Store) Delete(sid string) error {
	if !ValidSID(sid) {
		return ErrNotFound
	}
	if s.isLive(sid) {
		return errors.New("record: session is still recording")
	}
	return os.RemoveAll(s.SessionDir(sid))
}

// ---------- 启动恢复 ----------

// Recover 收尾上次没有正常结束的会话（进程被杀、断电）：补上结束时间与原因，使其进入保留期计时。
// reasonFor 可按会话给出更精确的结束原因（例如主机重启）；返回空串或为 nil 时取 Options.RecoverReason。
func (s *Store) Recover(reasonFor func(sid string) string) {
	ents, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || !ValidSID(e.Name()) {
			continue
		}
		dir := filepath.Join(s.root, e.Name())
		m, err := readMeta(dir)
		if err != nil || m.EndedMS != 0 {
			continue
		}
		last := newestMod(dir)
		if last.IsZero() {
			last = s.o.Now()
		}
		m.EndedMS = last.UnixMilli()
		m.EndedReason = s.o.RecoverReason
		if reasonFor != nil {
			if r := reasonFor(m.SID); r != "" {
				m.EndedReason = r
			}
		}
		if err := writeMeta(dir, m); err != nil {
			s.logf("收尾遗留记录失败（%s）: %v", e.Name()[:8], err)
		}
	}
}

func newestMod(dir string) time.Time {
	var t time.Time
	ents, err := os.ReadDir(dir)
	if err != nil {
		return t
	}
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

// writeMeta 原子地写入 meta.json（0600）。
func writeMeta(dir string, m Meta) error {
	m.V = 1
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, metaFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, metaFile))
}

// ---------- 后台：压缩 ----------

func (s *Store) enqueueCompress(path string) {
	if s.o.NoCompress {
		return
	}
	select {
	case s.compressQ <- compressJob{path: path}:
	default: // 队列满：留给下一轮清理时补压缩
	}
}

func (s *Store) compressLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case j := <-s.compressQ:
			if err := compressFile(j.path, s.stop); err != nil {
				s.logf("压缩记录分段失败: %v", err)
			}
		}
	}
}

// compressFile 把 path（.rec）压缩为 path+".gz"，成功后删除原文件。
// 压缩是后台低优先级工作：按块处理并主动让出 CPU，绝不挤占终端本身。
func compressFile(path string, stop <-chan struct{}) error {
	in, err := os.Open(path)
	if err != nil {
		return nil // 已被配额清理或已压缩
	}
	defer in.Close()
	dst := path + ".gz"
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	gz, err := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err != nil {
		return err
	}
	buf := make([]byte, 64<<10)
	for n := 0; ; n++ {
		select {
		case <-stop:
			return errors.New("interrupted")
		default:
		}
		k, rerr := in.Read(buf)
		if k > 0 {
			if _, werr := gz.Write(buf[:k]); werr != nil {
				return werr
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return rerr
		}
		if n%2 == 1 {
			time.Sleep(time.Millisecond)
		}
	}
	// 先放掉自己的读句柄：Windows 上删除一个仍被本进程打开的文件会失败，
	// 若留到函数返回时才由 defer 关闭，原始分段会和 .gz 一起永远留在磁盘上。
	_ = in.Close()
	if err := gz.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return nil // 压缩期间被清理：丢弃结果
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	ok = true
	_ = os.Remove(path) // Windows 上若有别的读者正占用会失败：残留由下一轮清理处理
	return nil
}

// ---------- 后台：保留期、整机配额与残留清理 ----------

func (s *Store) kickHost() {
	select {
	case s.hostKick <- struct{}{}:
	default:
	}
}

func (s *Store) maintainLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.o.GCInterval)
	defer t.Stop()
	s.gcOnce()
	var last time.Time
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.gcOnce()
			last = time.Now()
		case <-s.hostKick:
			// 合并短时间内的多次触发，避免频繁扫描目录。
			if since := time.Since(last); since < hostCheckMinGap {
				select {
				case <-s.stop:
					return
				case <-time.After(hostCheckMinGap - since):
				}
			}
			s.enforceHost()
			last = time.Now()
		}
	}
}

type usage struct {
	sid     string
	dir     string
	disk    int64
	endedMS int64 // 0 表示未结束
	live    *Writer
}

func (s *Store) scanUsage() []usage {
	ents, err := os.ReadDir(s.root)
	if err != nil {
		return nil
	}
	var out []usage
	for _, e := range ents {
		if !e.IsDir() || !ValidSID(e.Name()) {
			continue
		}
		dir := filepath.Join(s.root, e.Name())
		u := usage{sid: e.Name(), dir: dir, live: s.liveWriter(e.Name())}
		if m, err := readMeta(dir); err == nil {
			u.endedMS = m.EndedMS
		}
		if files, err := os.ReadDir(dir); err == nil {
			for _, f := range files {
				if fi, err := f.Info(); err == nil && !f.IsDir() {
					u.disk += fi.Size()
				}
			}
		}
		out = append(out, u)
	}
	return out
}

// enforceHost 保证整机记录占用不超过上限：先整体清理最早结束的会话，仍超限时让仍在录制的
// 会话各自丢弃最旧的已封存分段。
func (s *Store) enforceHost() {
	limit := s.hostBytes()
	us := s.scanUsage()
	var total int64
	for _, u := range us {
		total += u.disk
	}
	if total <= limit {
		return
	}
	var ended []usage
	var live []usage
	for _, u := range us {
		if u.live != nil {
			live = append(live, u)
		} else if u.endedMS > 0 {
			ended = append(ended, u)
		}
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].endedMS < ended[j].endedMS })
	for _, u := range ended {
		if total <= limit {
			return
		}
		if err := os.RemoveAll(u.dir); err != nil {
			s.logf("按整机配额清理记录失败: %v", err)
			continue
		}
		total -= u.disk
	}
	sort.Slice(live, func(i, j int) bool { return live[i].disk > live[j].disk })
	for _, u := range live {
		if total <= limit {
			return
		}
		need := total - limit
		if need > u.disk {
			need = u.disk
		}
		u.live.ctl(ctlShrink, need)
		total -= need
	}
}

// gcOnce 清理过期会话，补压缩遗留的原始分段，并删除日志已标记清理但仍残留的文件。
func (s *Store) gcOnce() {
	now := s.o.Now()
	retention := s.retention()
	ents, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(s.root, e.Name())
		if !ValidSID(e.Name()) {
			continue
		}
		if s.isLive(e.Name()) {
			continue
		}
		meta, merr := readMeta(dir)
		var ref time.Time
		switch {
		case merr == nil && meta.EndedMS > 0:
			ref = time.UnixMilli(meta.EndedMS)
		case merr != nil:
			ref = newestMod(dir) // 没有元数据的残留目录：按最后修改时间计
		}
		if !ref.IsZero() && now.Sub(ref) > retention {
			if err := os.RemoveAll(dir); err != nil {
				s.logf("清理过期记录失败: %v", err)
			}
			continue
		}
		// 只整理已结束的会话。「没结束又不在 live 里」的目录有两种：Begin 刚建出目录、还没登记进 live 的
		// 新会话（写协程正在追加原始分段，此时压缩并删掉原文件，后续写入就落进已删除的文件，丢一个分段），
		// 以及上次崩溃遗留、等着 Recover 收尾的会话（Recover 要改写 meta.json，清理误删它的临时文件会让
		// 收尾失败）。两者都不该被这里碰；遗留会话收尾后，下一轮清理自然会整理它。
		if merr == nil && meta.EndedMS > 0 {
			s.tidyDir(dir)
		}
	}
	s.enforceHost()
}

// tidyDir 处理已结束会话目录里的残留：同时存在 .rec 与 .rec.gz 时删掉 .rec；
// 只有 .rec 的已封存分段补做压缩；日志标记为已清理的分段删掉。
func (s *Store) tidyDir(dir string) {
	dropped := map[uint32]bool{}
	for _, l := range readJournal(dir) {
		if l.K == "drop" {
			dropped[l.Seq] = true
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := map[string]bool{}
	for _, e := range ents {
		names[e.Name()] = true
	}
	if _, err := os.Stat(filepath.Join(dir, metaFile)); err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		seq, gz, ok := parseSegFile(name)
		if !ok {
			if filepath.Ext(name) == ".tmp" {
				_ = os.Remove(filepath.Join(dir, name)) // 压缩中断遗留
			}
			continue
		}
		path := filepath.Join(dir, name)
		switch {
		case dropped[seq]:
			_ = os.Remove(path)
		case !gz && names[name+".gz"]:
			_ = os.Remove(path)
		case !gz:
			s.enqueueCompress(path)
		}
	}
}
