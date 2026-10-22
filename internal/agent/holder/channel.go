package holder

import (
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// frameSink 是数据通道向 Hub 发送帧的出口（生产中由 link 实现，测试中可替换）。
type frameSink interface {
	sendJSON(v any) error
	sendBinary(data []byte) error
	// channelEnded 在通道结束后通知出口（从它的通道表中移除）。
	channelEnded(c *channel)
}

// channel 是一条附着在会话上的数据通道，对应 Hub 侧的一个浏览器终端连接。
//
// 发送采用「拉取」模型：每条通道有自己的发送协程，按偏移从环形缓冲读取并发送，
// 已发送未确认的字节数不超过 AttachWindow。读取 PTY 的协程在窗口耗尽时暂停（见 Session.windowFullLocked），
// 因此缓冲里属于附着方的未确认数据不会被覆盖。
type channel struct {
	sess *Session
	sink frameSink
	id   uint32

	// 以下字段由 sess.mu 保护。
	sent      uint64 // 下一个要发送的偏移
	acked     uint64 // 浏览器已确认渲染到的偏移
	fullSince time.Time

	done    chan struct{}
	endOnce sync.Once
}

// start 启动发送协程（必须在 attached 应答写出之后调用，保证输出帧排在应答之后）。
func (c *channel) start() { go c.run() }

// run 是发送循环。
func (c *channel) run() {
	s := c.sess
	buf := make([]byte, termproto.MaxDataPayload)
	for {
		s.mu.Lock()
		var n int
		var off uint64
		for {
			select {
			case <-c.done:
				s.mu.Unlock()
				return
			default:
			}
			base, end := s.ring.Base(), s.ring.End()
			if c.sent < base {
				// 正常情况下背压保证不会发生；万一缓冲被越过，通知对端重新附着以获得显式的 truncated 标记。
				s.mu.Unlock()
				c.end("overrun")
				return
			}
			avail := end - c.sent
			if avail == 0 && s.state == termproto.SessionExited {
				exit := s.exit
				killed := s.killed
				s.mu.Unlock()
				c.finishExited(exit, killed)
				return
			}
			window := uint64(termproto.AttachWindow)
			inFlight := c.sent - c.acked
			if avail > 0 && inFlight < window {
				take := avail
				if left := window - inFlight; take > left {
					take = left
				}
				if take > uint64(len(buf)) {
					take = uint64(len(buf))
				}
				n, _ = s.ring.Read(c.sent, buf[:take])
				off = c.sent
				c.sent += uint64(n)
				break
			}
			s.cond.Wait()
		}
		s.mu.Unlock()

		if err := c.sink.sendBinary(termproto.EncodeOutput(c.id, off, buf[:n])); err != nil {
			c.end("") // 连接已不可用：静默结束，会话继续存活
			return
		}
	}
}

// finishExited 在已退出会话的输出全部发送完毕后，通知 Hub 会话结束并释放通道。
func (c *channel) finishExited(exit exitInfo, killed bool) {
	_ = c.sink.sendJSON(termproto.Exit{
		T:      termproto.TExit,
		SID:    c.sess.id,
		Code:   exit.code,
		Signal: exit.signal,
	})
	reason := "exited"
	if killed {
		reason = "killed"
	}
	c.end(reason)
}

// ack 处理浏览器的确认（经 Hub 转发）：推进已确认偏移并唤醒发送协程与读协程。
func (c *channel) ack(offset uint64) {
	s := c.sess
	s.mu.Lock()
	if offset > c.sent {
		offset = c.sent // 不可能确认尚未发送的数据
	}
	if offset > c.acked {
		c.acked = offset
		c.fullSince = time.Time{}
	}
	s.mu.Unlock()
	s.cond.Broadcast()
}

// end 结束通道（幂等）。reason 非空表示由 Agent 主动释放，需要通知 Hub（detached）；
// 为空表示 Hub 已知晓（对端要求 detach，或连接本身已断开）。
func (c *channel) end(reason string) {
	c.endOnce.Do(func() {
		s := c.sess
		close(c.done)
		s.mu.Lock()
		delete(s.chans, c)
		if len(s.chans) == 0 {
			s.lastDetach = s.mgr.now()
		}
		s.mu.Unlock()
		s.cond.Broadcast()
		if reason != "" {
			_ = c.sink.sendJSON(termproto.Detached{T: termproto.TDetached, Ch: c.id, Reason: reason})
		}
		c.sink.channelEnded(c)
		s.mgr.changed()
	})
}
