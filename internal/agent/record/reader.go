package record

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ErrNotFound 表示没有这个会话的记录（从未录制、已被清理或已过保留期）。
var ErrNotFound = errors.New("record: not found")

var sidRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidSID 判断会话 ID 是否合法（128 位小写十六进制）。路径拼接前必须校验，杜绝目录穿越。
func ValidSID(sid string) bool { return sidRe.MatchString(sid) }

// 会话元数据文件与追加式日志的文件名。
const (
	metaFile    = "meta.json"
	journalFile = "index.jsonl"
)

// Meta 是 meta.json 的内容。
type Meta struct {
	V           int    `json:"v"`
	SID         string `json:"sid"`
	Name        string `json:"name,omitempty"`
	Shell       string `json:"shell,omitempty"`
	CreatedMS   int64  `json:"created_ms"`
	Rows        uint16 `json:"rows,omitempty"`
	Cols        uint16 `json:"cols,omitempty"`
	EndedMS     int64  `json:"ended_ms,omitempty"`
	EndedReason string `json:"ended_reason,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Signal      string `json:"signal,omitempty"`

	// Incognito 只是 Begin 的输入：无痕会话根本不会落盘，所以不写入文件。
	Incognito bool `json:"-"`
}

// SegInfo 描述一个仍保留的分段。
type SegInfo struct {
	Seq  uint32 `json:"seq"`
	T0   int64  `json:"t0"`             // 起点（Unix 毫秒）
	T1   int64  `json:"t1,omitempty"`   // 终点（Unix 毫秒）
	Raw  int64  `json:"raw"`            // 解压后的字节数
	Disk int64  `json:"disk"`           // 实际占用的磁盘字节数
	Rows uint16 `json:"rows,omitempty"` // 分段起点处的窗口尺寸
	Cols uint16 `json:"cols,omitempty"`
	Gz   bool   `json:"gz,omitempty"`
}

// Info 是一个会话记录的索引信息（不含任何终端内容）。
type Info struct {
	SID          string    `json:"sid"`
	Name         string    `json:"name,omitempty"`
	Shell        string    `json:"shell,omitempty"`
	CreatedMS    int64     `json:"created_ms"`
	EndedMS      int64     `json:"ended_ms,omitempty"`
	EndedReason  string    `json:"ended_reason,omitempty"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Signal       string    `json:"signal,omitempty"`
	Rows         uint16    `json:"rows,omitempty"` // 会话创建时的窗口尺寸
	Cols         uint16    `json:"cols,omitempty"`
	FirstMS      int64     `json:"first_ms,omitempty"`
	LastMS       int64     `json:"last_ms,omitempty"`
	RawBytes     int64     `json:"raw_bytes"`
	DiskBytes    int64     `json:"disk_bytes"`
	DroppedBytes int64     `json:"dropped_bytes,omitempty"` // 因配额被清理或因缺口丢失的字节数
	Gap          bool      `json:"gap,omitempty"`           // 记录不完整（开头被清理或中途有缺口）
	Head         bool      `json:"head,omitempty"`          // 最早的分段不是第 1 段：更早内容已被清理
	Segments     []SegInfo `json:"segments"`
}

// journalLine 是 index.jsonl 中的一行。
type journalLine struct {
	K      string `json:"k"` // seg | seal | drop | gap | base
	Seq    uint32 `json:"seq,omitempty"`
	T0     int64  `json:"t0,omitempty"`
	T1     int64  `json:"t1,omitempty"`
	Raw    int64  `json:"raw,omitempty"`
	Rows   uint16 `json:"rows,omitempty"`
	Cols   uint16 `json:"cols,omitempty"`
	Reason string `json:"reason,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Gaps   int    `json:"gaps,omitempty"`
}

func segName(seq uint32) string   { return fmt.Sprintf("seg-%06d.rec", seq) }
func segNameGz(seq uint32) string { return segName(seq) + ".gz" }

// parseSegFile 解析分段文件名；gz 表示已压缩。
func parseSegFile(name string) (seq uint32, gz bool, ok bool) {
	switch {
	case strings.HasSuffix(name, ".rec.gz"):
		gz = true
		name = strings.TrimSuffix(name, ".rec.gz")
	case strings.HasSuffix(name, ".rec"):
		name = strings.TrimSuffix(name, ".rec")
	default:
		return 0, false, false
	}
	if !strings.HasPrefix(name, "seg-") {
		return 0, false, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(name, "seg-"), 10, 32)
	if err != nil || n == 0 {
		return 0, false, false
	}
	return uint32(n), gz, true
}

type segFile struct {
	seq  uint32
	gz   bool
	path string
	size int64
}

// listSegments 列出目录里的分段文件（按序号升序）。同一序号同时存在 .rec 与 .rec.gz 时取 .gz
// （压缩刚完成、原文件尚未删掉的瞬间）。
func listSegments(dir string) ([]segFile, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	best := map[uint32]segFile{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		seq, gz, ok := parseSegFile(e.Name())
		if !ok {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		cur, exists := best[seq]
		if exists && cur.gz && !gz {
			continue
		}
		best[seq] = segFile{seq: seq, gz: gz, path: filepath.Join(dir, e.Name()), size: fi.Size()}
	}
	out := make([]segFile, 0, len(best))
	for _, f := range best {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out, nil
}

func readMeta(dir string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, ErrNotFound
		}
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("record: bad meta: %w", err)
	}
	return m, nil
}

func readJournal(dir string) []journalLine {
	f, err := os.Open(filepath.Join(dir, journalFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []journalLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		var l journalLine
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.K != "" {
			out = append(out, l)
		}
	}
	return out
}

// gzipRawSize 读取 gzip 尾部的 ISIZE（解压后大小对 2^32 取模，单段远小于此）。
func gzipRawSize(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < 8 {
		return 0
	}
	var b [4]byte
	if _, err := f.ReadAt(b[:], fi.Size()-4); err != nil {
		return 0
	}
	return int64(binary.LittleEndian.Uint32(b[:]))
}

// readInfo 汇总一个会话目录的索引信息：以磁盘上实际存在的分段为准，日志只补充时间、尺寸与缺口统计。
func readInfo(dir, sid string) (*Info, error) {
	meta, err := readMeta(dir)
	if err != nil {
		return nil, err
	}
	files, err := listSegments(dir)
	if err != nil {
		return nil, err
	}
	type jseg struct {
		t0, t1, raw int64
		rows, cols  uint16
		sealed      bool
		dropped     bool
	}
	js := map[uint32]*jseg{}
	var droppedBytes int64
	gap := false
	for _, l := range readJournal(dir) {
		switch l.K {
		case "base":
			droppedBytes += l.Raw + l.Bytes
			if l.Raw > 0 || l.Bytes > 0 || l.Gaps > 0 {
				gap = true
			}
		case "seg":
			js[l.Seq] = &jseg{t0: l.T0, rows: l.Rows, cols: l.Cols}
		case "seal":
			if g := js[l.Seq]; g != nil {
				g.t1, g.raw, g.sealed = l.T1, l.Raw, true
			}
		case "drop":
			droppedBytes += l.Raw
			gap = true
			if g := js[l.Seq]; g != nil {
				g.dropped = true
			}
		case "gap":
			droppedBytes += l.Bytes
			gap = true
		}
	}

	info := &Info{
		SID: sid, Name: meta.Name, Shell: meta.Shell,
		CreatedMS: meta.CreatedMS, EndedMS: meta.EndedMS, EndedReason: meta.EndedReason,
		ExitCode: meta.ExitCode, Signal: meta.Signal, Rows: meta.Rows, Cols: meta.Cols,
		DroppedBytes: droppedBytes, Gap: gap,
		Segments: []SegInfo{},
	}
	for _, f := range files {
		g := js[f.seq]
		if g != nil && g.dropped {
			continue // 日志说它已被清理，磁盘上的是残留
		}
		si := SegInfo{Seq: f.seq, Disk: f.size, Gz: f.gz}
		if g != nil {
			si.T0, si.T1, si.Rows, si.Cols = g.t0, g.t1, g.rows, g.cols
			if g.sealed {
				si.Raw = g.raw
			}
		}
		if g == nil || !g.sealed {
			// 没有封存记录：正在写入的分段，或崩溃遗留的分段。
			if f.gz {
				si.Raw = gzipRawSize(f.path)
			} else {
				si.Raw = f.size
			}
			if fi, err := os.Stat(f.path); err == nil {
				si.T1 = fi.ModTime().UnixMilli()
			}
		}
		info.Segments = append(info.Segments, si)
		info.RawBytes += si.Raw
		info.DiskBytes += si.Disk
	}
	if n := len(info.Segments); n > 0 {
		info.FirstMS = info.Segments[0].T0
		if info.FirstMS == 0 {
			info.FirstMS = meta.CreatedMS
		}
		info.LastMS = info.Segments[n-1].T1
		info.Head = info.Segments[0].Seq != 1
		if info.Head {
			info.Gap = true
		}
	}
	if info.EndedMS > 0 && info.LastMS < info.EndedMS {
		info.LastMS = info.EndedMS
	}
	return info, nil
}

// Page 是一次读取的结果：若干个完整事件的原始编码（与分段文件里的编码完全一致）。
type Page struct {
	Data    []byte `json:"-"`
	Seg     uint32 `json:"seg"` // 本页数据实际起点所在分段
	Off     uint32 `json:"off"` // 本页数据在该分段（解压后，含 4 字节文件头）内的偏移
	NextSeg uint32 `json:"next_seg"`
	NextOff uint32 `json:"next_off"`
	// EOF 表示已读到当前记录的末尾（会话仍在运行时之后可能还有新数据）。
	EOF bool `json:"eof"`
	// Skipped 表示请求的起点已被清理，数据从最早保留的分段开头开始。
	Skipped bool `json:"skipped,omitempty"`
}

// readPage 从 (seg, off) 开始读取至多 max 字节的完整事件。seg 为 0 表示从最早保留的分段开始。
// 只读：同一份文件可能正被写入进程追加，读到未写完的尾部事件时在它之前停下。
func readPage(dir string, seg, off uint32, max int) (*Page, error) {
	if max < minPageBytes {
		max = minPageBytes
	}
	if max > MaxPageBytes {
		max = MaxPageBytes
	}
	files, err := listSegments(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	pg := &Page{}
	if len(files) == 0 {
		pg.EOF = true
		pg.Seg, pg.NextSeg = seg, seg
		pg.Off, pg.NextOff = off, off
		return pg, nil
	}

	idx := 0
	switch {
	case seg == 0:
		off = 0
	case seg < files[0].seq:
		off, pg.Skipped = 0, true
	default:
		idx = sort.Search(len(files), func(i int) bool { return files[i].seq >= seg })
		if idx == len(files) {
			// 请求的分段比现有的都新（例如记录刚被清理重建）：没有数据。
			pg.EOF = true
			pg.Seg, pg.NextSeg = seg, seg
			pg.Off, pg.NextOff = off, off
			return pg, nil
		}
		if files[idx].seq != seg {
			off, pg.Skipped = 0, true
		}
	}
	pg.Seg, pg.Off = files[idx].seq, off

	var out []byte
	curSeq, curOff := files[idx].seq, off
	for ; idx < len(files); idx++ {
		f := files[idx]
		if f.seq != curSeq {
			curSeq, curOff = f.seq, 0
		}
		chunk, nextOff, atEnd, err := readSegment(f, curOff, max-len(out))
		if err != nil {
			if len(out) > 0 {
				break
			}
			return nil, err
		}
		out = append(out, chunk...)
		curOff = nextOff
		if !atEnd {
			// 预算用尽，游标停在这个分段中间。
			pg.Data, pg.NextSeg, pg.NextOff = out, curSeq, curOff
			return pg, nil
		}
		if idx+1 >= len(files) {
			pg.EOF = true
			break
		}
		if len(out)+minPageBytes > max {
			// 下一个分段放不下了：游标指向它的开头。
			pg.Data, pg.NextSeg, pg.NextOff = out, files[idx+1].seq, 0
			return pg, nil
		}
	}
	pg.Data, pg.NextSeg, pg.NextOff = out, curSeq, curOff
	return pg, nil
}

// readSegment 读取一个分段从 off 起至多 budget 字节的完整事件。
// atEnd 表示读到了文件末尾（没有更多完整事件），否则表示预算用尽。
func readSegment(f segFile, off uint32, budget int) (data []byte, next uint32, atEnd bool, err error) {
	fh, err := os.Open(f.path)
	if err != nil && f.gz == false && errors.Is(err, os.ErrNotExist) {
		// 刚好被压缩替换：改读 .gz。
		f = segFile{seq: f.seq, gz: true, path: f.path + ".gz"}
		fh, err = os.Open(f.path)
	}
	if err != nil {
		return nil, off, false, err
	}
	defer fh.Close()

	var r io.Reader = fh
	if f.gz {
		gr, gerr := gzip.NewReader(fh)
		if gerr != nil {
			return nil, off, false, gerr
		}
		defer gr.Close()
		r = gr
	}
	br := bufio.NewReaderSize(r, 64<<10)

	if off == 0 {
		hdr := make([]byte, len(segMagic))
		if _, err := io.ReadFull(br, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, 0, true, nil // 刚创建、文件头还没落盘
			}
			return nil, 0, false, err
		}
		if string(hdr) != segMagic {
			return nil, 0, false, errors.New("record: bad segment header")
		}
		off = uint32(len(segMagic))
	} else if _, err := io.CopyN(io.Discard, br, int64(off)); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, off, true, nil // 请求的偏移超出文件：当作已到末尾
		}
		return nil, off, false, err
	}

	pos := off
	for {
		hdr, perr := br.Peek(1 + 2*binary.MaxVarintLen64)
		if len(hdr) == 0 {
			return data, pos, true, nil
		}
		if perr != nil && !errors.Is(perr, io.EOF) && !errors.Is(perr, bufio.ErrBufferFull) {
			return data, pos, true, nil
		}
		// 先只解析头部算出事件长度，再决定是否放得进预算。
		total, ok := eventLen(hdr)
		if !ok {
			return data, pos, true, nil
		}
		if len(data)+total > budget {
			return data, pos, false, nil
		}
		buf, rerr := br.Peek(total)
		if rerr != nil {
			return data, pos, true, nil // 尾部事件还没写完
		}
		data = append(data, buf...)
		_, _ = br.Discard(total)
		pos += uint32(total)
	}
}

// eventLen 从事件头部算出整个事件占用的字节数；头部数据不足时返回 false。
func eventLen(hdr []byte) (int, bool) {
	if len(hdr) < 3 {
		return 0, false
	}
	p := 1
	_, n := binary.Uvarint(hdr[p:])
	if n <= 0 {
		return 0, false
	}
	p += n
	ln, n := binary.Uvarint(hdr[p:])
	if n <= 0 || ln > MaxEventPayload {
		return 0, false
	}
	return p + n + int(ln), true
}

// Reader 是记录目录的只读视图：不启动任何后台任务，也不会修改任何文件。
// Agent 进程用它读取守护进程写下的记录（守护进程停止时历史记录仍可读取）。
type Reader struct{ root string }

// NewReader 打开记录根目录（与 Options.Dir 相同）的只读视图。
func NewReader(dir string) *Reader { return &Reader{root: filepath.Join(dir, "sessions")} }

// Info 返回会话记录的索引信息。
func (r *Reader) Info(sid string) (*Info, error) {
	if !ValidSID(sid) {
		return nil, ErrNotFound
	}
	return readInfo(filepath.Join(r.root, sid), sid)
}

// Read 读取一页记录内容，见 readPage。
func (r *Reader) Read(sid string, seg, off uint32, max int) (*Page, error) {
	if !ValidSID(sid) {
		return nil, ErrNotFound
	}
	dir := filepath.Join(r.root, sid)
	if _, err := os.Stat(filepath.Join(dir, metaFile)); err != nil {
		return nil, ErrNotFound
	}
	return readPage(dir, seg, off, max)
}

// List 返回全部会话记录的索引信息（按创建时间升序）。
func (r *Reader) List() []*Info {
	ents, err := os.ReadDir(r.root)
	if err != nil {
		return nil
	}
	var out []*Info
	for _, e := range ents {
		if !e.IsDir() || !ValidSID(e.Name()) {
			continue
		}
		if info, err := readInfo(filepath.Join(r.root, e.Name()), e.Name()); err == nil {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedMS < out[j].CreatedMS })
	return out
}
