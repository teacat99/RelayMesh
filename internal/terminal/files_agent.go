package terminal

import (
	"context"
	"errors"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// agentConn 的文件能力：向主机发送 file_req，等待 file_res（JSON）或 FrameFileData（read 的二进制数据）。
// 授权在 Agent 一侧强制执行（os.Root）；这里只做线上收发与对 Agent 回包的防御性校验——Agent 的回包是不可信输入。

var (
	errFileTimeout  = errors.New("host did not answer the file request in time")
	errFileTooMany  = errors.New("too many file requests in flight")
	errFileProtocol = errors.New("host sent an invalid file response")
	errFileTooBig   = errors.New("file request does not fit in a control frame")
)

// maxPendingFileReqs 是单个 Agent 连接上同时在途的文件请求数（略大于 Agent 一侧的并发上限，留给 close 之外的所有操作）。
const maxPendingFileReqs = 8

// 文件请求的期限（变量而非常量，仅为让测试能缩短等待）。
var (
	fileCallTimeout = 30 * time.Second // stat / open
	fileHashTimeout = 6 * time.Minute  // hash（Agent 一侧上限 5 分钟）
	filePushTimeout = 6 * time.Minute  // push_url（Agent 一侧上限 5 分钟）
	fileReadTimeout = 30 * time.Second // 一次 read 的总期限
	fileReadIdle    = 10 * time.Second // read 的相邻数据帧之间的最长间隔
)

type fileEvent struct {
	res   *termproto.FileRes
	chunk *termproto.FileChunk
}

type fileWaiter struct {
	ch chan fileEvent
}

// deliver 非阻塞投递：读循环绝不能等任何一个请求方。
func (w *fileWaiter) deliver(ev fileEvent) {
	select {
	case w.ch <- ev:
	default:
	}
}

func (a *agentConn) registerFileReq(frames int) (uint32, *fileWaiter, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fileWaits == nil {
		a.fileWaits = make(map[uint32]*fileWaiter)
	}
	if len(a.fileWaits) >= maxPendingFileReqs {
		return 0, nil, errFileTooMany
	}
	a.nextFileReq++
	if a.nextFileReq == 0 {
		a.nextFileReq = 1
	}
	req := a.nextFileReq
	w := &fileWaiter{ch: make(chan fileEvent, frames)}
	a.fileWaits[req] = w
	return req, w, nil
}

func (a *agentConn) unregisterFileReq(req uint32) {
	a.mu.Lock()
	delete(a.fileWaits, req)
	a.mu.Unlock()
}

func (a *agentConn) fileWaiterFor(req uint32) *fileWaiter {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fileWaits[req]
}

// onFileFrame 处理 read 的数据帧（读循环里调用）。
func (a *agentConn) onFileFrame(data []byte) {
	c, err := termproto.DecodeFileData(data)
	if err != nil {
		a.closeWith(termproto.CloseInvalidHandshake, "bad file data frame")
		return
	}
	w := a.fileWaiterFor(c.Req)
	if w == nil {
		return // 请求已超时或已取消
	}
	cp := c
	cp.Data = append([]byte(nil), c.Data...)
	w.deliver(fileEvent{chunk: &cp})
}

// onFileRes 处理 file_res（读循环里调用）。m 已经过 Sanitized。
func (a *agentConn) onFileRes(m termproto.FileRes) {
	if w := a.fileWaiterFor(m.Req); w != nil {
		cp := m
		w.deliver(fileEvent{res: &cp})
	}
}

// filesSupport 报告这台主机的文件能力：supported 要求 Agent 自报具备文件能力、且会话守护进程已就绪
// （一票否决的状态由守护进程上报，没有守护进程的状态就无法确认本机策略）；denied 是本机一票否决。
func (a *agentConn) filesSupport() (supported, denied bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := a.holder
	if !a.meta.Files || h == nil || h.State != termproto.HolderReady {
		return false, false
	}
	return true, h.DenyExec
}

// filesWholeFS 报告这个 Agent 是否认识「整个文件系统」哨兵（termproto.FileRootAll）。
// 旧版 Agent 把它当作无法解析的条目丢弃，于是一切路径都被拒绝——安全，但对模型毫无解释。
func (a *agentConn) filesWholeFS() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.meta.FilesAll
}

// sendFileReq 序列化并发出一个文件请求。
func (a *agentConn) sendFileReq(req termproto.FileReq) error {
	req.T = termproto.TFileReq
	if req.Problem() != "" { // 我们自己构造的请求不应该有形状问题；防御性检查
		return errFileProtocol
	}
	data, err := termproto.Marshal(req)
	if err != nil {
		return err
	}
	if len(data) > termproto.MaxControlFrame-4<<10 {
		return errFileTooBig
	}
	return a.write(websocket.MessageText, data)
}

// fileCall 发出一个请求并等待单个 JSON 应答。无论成败应答都以 FileRes 返回（OK=false 时带稳定错误码）；
// 只有传输层问题（超时、连接断开、请求放不进帧）才返回 error。
func (a *agentConn) fileCall(ctx context.Context, req termproto.FileReq, wait time.Duration) (termproto.FileRes, error) {
	if !a.isEnabled() {
		return termproto.FileRes{}, errAgentNotActive
	}
	id, w, err := a.registerFileReq(2)
	if err != nil {
		return termproto.FileRes{}, err
	}
	defer a.unregisterFileReq(id)
	req.Req = id
	if err := a.sendFileReq(req); err != nil {
		return termproto.FileRes{}, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case ev := <-w.ch:
			if ev.res == nil {
				return termproto.FileRes{}, errFileProtocol // 数据帧不该出现在这类请求的应答里
			}
			return *ev.res, nil
		case <-timer.C:
			return termproto.FileRes{}, errFileTimeout
		case <-ctx.Done():
			return termproto.FileRes{}, ctx.Err()
		case <-a.ctx.Done():
			return termproto.FileRes{}, errAgentClosed
		}
	}
}

// FileSlice 是一次 read 的结果。
type FileSlice struct {
	Data  []byte
	Off   uint64
	Total uint64 // 文件在打开时的大小
	EOF   bool
}

// fileRead 读取已打开文件的 [off, off+n)。成功时返回数据；Agent 报告失败时返回 (nil, *FileRes, nil)；
// 传输层或协议问题返回 error。对每个数据帧都校验偏移的连续性、总量一致与累计长度，不信任 Agent 的任何一个字段。
func (a *agentConn) fileRead(ctx context.Context, handle string, off uint64, n int) (*FileSlice, *termproto.FileRes, error) {
	if !a.isEnabled() {
		return nil, nil, errAgentNotActive
	}
	if n <= 0 || n > termproto.MaxFileRead {
		n = termproto.MaxFileRead
	}
	id, w, err := a.registerFileReq(n/termproto.MaxFileDataFrame + 4)
	if err != nil {
		return nil, nil, err
	}
	defer a.unregisterFileReq(id)
	if err := a.sendFileReq(termproto.FileReq{Req: id, Op: termproto.FileOpRead, Handle: handle, Off: off, Len: n}); err != nil {
		return nil, nil, err
	}

	out := &FileSlice{Off: off}
	next := off
	haveTotal := false
	total := time.NewTimer(fileReadTimeout)
	defer total.Stop()
	idle := time.NewTimer(fileReadIdle)
	defer idle.Stop()
	for {
		select {
		case ev := <-w.ch:
			if ev.res != nil {
				if ev.res.OK {
					return nil, nil, errFileProtocol // read 成功只会用数据帧应答
				}
				return nil, ev.res, nil
			}
			c := ev.chunk
			if c == nil || c.Off != next || (haveTotal && c.Total != out.Total) || len(out.Data)+len(c.Data) > n {
				return nil, nil, errFileProtocol
			}
			out.Total, haveTotal = c.Total, true
			out.Data = append(out.Data, c.Data...)
			next += uint64(len(c.Data))
			if c.Flags&termproto.FileFlagEOF != 0 {
				out.EOF = true
			}
			if c.Flags&termproto.FileFlagLast != 0 {
				return out, nil, nil
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(fileReadIdle)
		case <-idle.C:
			return nil, nil, errFileTimeout
		case <-total.C:
			return nil, nil, errFileTimeout
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-a.ctx.Done():
			return nil, nil, errAgentClosed
		}
	}
}

// fileClose 关闭 Agent 上的文件句柄（不等应答；Agent 一侧也会因空闲 / 断线自行回收）。
func (a *agentConn) fileClose(handle string) {
	if !termproto.ValidFileHandle(handle) {
		return
	}
	_ = a.sendFileReq(termproto.FileReq{Req: 0, Op: termproto.FileOpClose, Handle: handle})
}
