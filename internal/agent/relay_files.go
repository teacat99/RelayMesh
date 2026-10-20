package agent

import (
	"context"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/agent/files"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// maxConcurrentFileReqs 是同时受理的文件请求数。文件请求只碰磁盘与网络、不碰终端，但必须有界：
// 一个失控的调用方不能把这台主机的 I/O 占满。
const maxConcurrentFileReqs = 6

// hashDeadline 是一次 SHA-256 计算的时限；push_url 的整体时限由 files 包自己控制。
const hashDeadline = 5 * time.Minute

// FileOptions 是文件能力的本机选项。本机只有两个旋钮：一票否决（Options.Exec.DenyExec，与模型执行共用）和这里的网址范围。
type FileOptions struct {
	// AllowPrivateURLs 允许 push_url 访问回环 / 私网 / 链路本地地址（默认不允许，防止借这台主机探测内网或元数据服务）。
	// 只有这台主机的拥有者能在本机启动参数里打开它，服务端无法设置。
	AllowPrivateURLs bool
}

// setFiles 设置文件服务；必须在 relay 开始处理帧之前调用。
func (r *relay) setFiles(s *files.Service) { r.files = s }

func (r *relay) failFile(req uint32, code, msg string) {
	_ = r.sendJSON(termproto.FileRes{T: termproto.TFileRes, Req: req, Code: code, Msg: msg})
}

func (r *relay) failFileErr(req uint32, e *files.Error) { r.failFile(req, e.Code, e.Msg) }

// filesVetoed 判断本机是否一票否决了模型对文件的访问：Agent 自己的 --deny-model-exec，
// 或守护进程上的同名选项（外置守护进程可能单独设置了它）。还没有见过守护进程的状态时按否决处理——
// 宁可短暂拒绝，也不在没有确认本机策略的情况下放行。
func (r *relay) filesVetoed() (bool, string) {
	switch {
	case r.denyExec, r.holderDeny.Load():
		return true, "本机用 --deny-model-exec 禁止了模型访问（包括文件）"
	case !r.holderKnown.Load():
		return true, "会话守护进程尚未就绪，无法确认本机策略，请稍后重试"
	}
	return false, ""
}

// handleFileReq 受理一次文件请求：在独立协程里执行，绝不阻塞 Hub 连接的读循环。
// 路径授权（允许目录、符号链接、特殊文件）全部在 files 包里强制执行；这里只做形状校验、一票否决与并发约束。
func (r *relay) handleFileReq(m termproto.FileReq) {
	if msg := m.Problem(); msg != "" {
		r.failFile(m.Req, termproto.FileErrBadRequest, msg)
		return
	}
	if m.Op == termproto.FileOpClose { // 关闭句柄永远允许，也不需要应答
		if r.files != nil {
			r.files.Close(m.Handle)
		}
		return
	}
	if veto, msg := r.filesVetoed(); veto {
		r.failFile(m.Req, termproto.FileErrDenied, msg)
		return
	}
	if r.files == nil {
		r.failFile(m.Req, termproto.FileErrDenied, "这台主机没有提供文件能力")
		return
	}
	select {
	case r.fileSem <- struct{}{}:
	default:
		r.failFile(m.Req, termproto.FileErrBusy, "这台主机正在处理太多文件请求，请稍后重试")
		return
	}
	go func() {
		defer func() { <-r.fileSem }()
		r.serveFileReq(m)
	}()
}

func (r *relay) serveFileReq(m termproto.FileReq) {
	svc := r.files
	ok := termproto.FileRes{T: termproto.TFileRes, Req: m.Req, OK: true}
	switch m.Op {
	case termproto.FileOpStat:
		st, e := svc.Stat(m.Roots, m.Path)
		if e != nil {
			r.failFileErr(m.Req, e)
			return
		}
		ok.Stat = &st
	case termproto.FileOpHash:
		ctx, cancel := context.WithTimeout(r.ctx, hashDeadline)
		defer cancel()
		sum, size, e := svc.Hash(ctx, m.Roots, m.Path, m.Max)
		if e != nil {
			r.failFileErr(m.Req, e)
			return
		}
		ok.SHA256, ok.Size = sum, size
	case termproto.FileOpOpen:
		h, size, e := svc.Open(m.Roots, m.Path, m.Max)
		if e != nil {
			r.failFileErr(m.Req, e)
			return
		}
		ok.Handle, ok.Size = h, size
	case termproto.FileOpRead:
		r.serveFileRead(m)
		return
	case termproto.FileOpPushURL:
		res, e := svc.PushURL(r.ctx, m.Roots, m.Path, m.URL, files.PushOpts{Overwrite: m.Overwrite, SHA256: m.SHA256, Max: m.Max})
		if e != nil {
			r.failFileErr(m.Req, e)
			return
		}
		ok.Size, ok.SHA256, ok.Status, ok.Source, ok.Redirects = res.Size, res.SHA256, res.Status, res.Source, res.Redirects
	default:
		r.failFile(m.Req, termproto.FileErrBadRequest, "未知的文件操作")
		return
	}
	_ = r.sendJSON(ok)
}

// serveFileRead 读取一段数据并拆成不超过 MaxFileDataFrame 的二进制帧发送。
// 最后一帧带 Last 标志；读到文件末尾的那一帧同时带 EOF。读不出数据（例如越界）时发一个空的 EOF|Last 帧收尾。
func (r *relay) serveFileRead(m termproto.FileReq) {
	data, eof, total, e := r.files.Read(m.Handle, m.Off, m.Len)
	if e != nil {
		r.failFileErr(m.Req, e)
		return
	}
	off := m.Off
	for {
		n := len(data)
		if n > termproto.MaxFileDataFrame {
			n = termproto.MaxFileDataFrame
		}
		chunk := data[:n]
		data = data[n:]
		var flags byte
		if len(data) == 0 {
			flags |= termproto.FileFlagLast
			if eof {
				flags |= termproto.FileFlagEOF
			}
		}
		frame := termproto.EncodeFileData(termproto.FileChunk{Req: m.Req, Flags: flags, Off: off, Total: total, Data: chunk})
		if err := r.write(websocket.MessageBinary, frame); err != nil {
			return
		}
		off += uint64(n)
		if flags&termproto.FileFlagLast != 0 {
			return
		}
	}
}
