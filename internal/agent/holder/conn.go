package holder

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// MsgType 区分控制帧（JSON 文本）与数据帧（二进制）；与 WebSocket 的两种消息类型一一对应，
// 这样 Agent 才能把 Hub 的帧原样转发给守护进程，而不必重新编码。
type MsgType uint8

const (
	MsgText   MsgType = 1
	MsgBinary MsgType = 2

	// 以下两种只在字节流上存在，由 streamConn 自行处理，不会交给上层。
	msgPing MsgType = 3
	msgPong MsgType = 4
)

// Conn 是一条能收发整帧消息的连接。守护进程与 Agent 之间只有两种实现：本地套接字流与进程内管道。
// 方法的语义与 WebSocket 连接一致：Read 阻塞到有消息、连接断开或 ctx 取消；Write 可并发调用；
// Ping 需要对端的读循环在运行才能收到应答。
type Conn interface {
	Read(ctx context.Context) (MsgType, []byte, error)
	Write(ctx context.Context, typ MsgType, data []byte) error
	Ping(ctx context.Context) error
	Close() error
}

const (
	// MaxIPCMessage 是本地连接上单条消息的净荷上限（输出帧 ≤ 32 KiB、控制帧 ≤ 64 KiB，留足余量）。
	MaxIPCMessage = 1 << 20
	// ipcHeaderLen 是流上每条消息的头：[类型 1B][净荷长度 u32]。
	ipcHeaderLen = 5
	// inboundQueue 是读协程到消费者之间的队列长度：队列满时读协程停下，内核套接字缓冲随之形成背压。
	inboundQueue = 64
	// pongWriteTimeout 是应答对端 ping 时写 pong 的最长时间。
	pongWriteTimeout = 5 * time.Second
)

var (
	errFrameTooLarge = errors.New("holder ipc: message too large")
	errBadFrameType  = errors.New("holder ipc: unknown frame type")
	// ErrConnClosed 表示连接已被本端关闭。
	ErrConnClosed = errors.New("holder ipc: connection closed")
)

type inbound struct {
	typ  MsgType
	data []byte
}

// streamConn 把面向字节流的 net.Conn 包装成按消息收发的 Conn。
// 帧格式：[类型 1B][净荷长度 u32 BE][净荷]。
type streamConn struct {
	nc net.Conn

	// wsem 是写权（容量 1）：保证一条消息的头与净荷连续写出。
	// 用通道而不是 sync.Mutex，是为了让排队等写权的调用方也能被自己的上下文或连接关闭唤醒——
	// 否则一个卡在管道写入上的写者会把后面所有人（包括应答心跳的 pong）一并拖住。
	wsem chan struct{}

	in      chan inbound
	pong    chan struct{} // 收到了对端对本端 ping 的应答
	pingReq chan struct{} // 对端发来了 ping，等待 pongLoop 应答（容量 1：连续到来的合并为一次应答）
	done    chan struct{}

	once sync.Once
	err  error // 在 done 关闭之前写入
}

// NewStreamConn 包装一条 net.Conn 并启动读协程与应答协程。
func NewStreamConn(nc net.Conn) Conn {
	c := &streamConn{
		nc:      nc,
		wsem:    make(chan struct{}, 1),
		in:      make(chan inbound, inboundQueue),
		pong:    make(chan struct{}, 1),
		pingReq: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	go c.pongLoop()
	return c
}

// NewPipe 返回两个互相连通的进程内 Conn（内嵌模式使用）。
func NewPipe() (Conn, Conn) {
	a, b := net.Pipe()
	return NewStreamConn(a), NewStreamConn(b)
}

func (c *streamConn) fail(err error) {
	c.once.Do(func() {
		if err == nil {
			err = io.EOF
		}
		c.err = err
		close(c.done)
		_ = c.nc.Close()
	})
}

func (c *streamConn) readLoop() {
	var hdr [ipcHeaderLen]byte
	for {
		if _, err := io.ReadFull(c.nc, hdr[:]); err != nil {
			c.fail(err)
			return
		}
		typ := MsgType(hdr[0])
		n := binary.BigEndian.Uint32(hdr[1:])
		if n > MaxIPCMessage {
			c.fail(errFrameTooLarge)
			return
		}
		var payload []byte
		if n > 0 {
			payload = make([]byte, n)
			if _, err := io.ReadFull(c.nc, payload); err != nil {
				c.fail(err)
				return
			}
		}
		switch typ {
		case msgPing:
			// 读循环绝不能自己去写：交给 pongLoop。见 pongLoop 的说明。
			select {
			case c.pingReq <- struct{}{}:
			default: // 已有一个待应答的 ping，合并
			}
		case msgPong:
			select {
			case c.pong <- struct{}{}:
			default:
			}
		case MsgText, MsgBinary:
			select {
			case c.in <- inbound{typ: typ, data: payload}:
			case <-c.done:
				return
			}
		default:
			c.fail(errBadFrameType)
			return
		}
	}
}

// pongLoop 专门负责应答对端的 ping。
//
// 应答不能放在读循环里写：读循环必须始终在读。进程内管道（net.Pipe，内嵌模式）是同步的，写入要等对端读走才返回，
// 两端的读循环若恰好同时收到对方的 ping 并各自去写 pong，就会互相等对方来读而卡死；
// 守护进程与 Agent 的心跳都是 15 秒，内嵌模式下两个定时器几乎同时触发，这个窗口并不罕见。
// 真实套接字有内核缓冲，不会暴露这个问题。
func (c *streamConn) pongLoop() {
	for {
		select {
		case <-c.done:
			return
		case <-c.pingReq:
			ctx, cancel := context.WithTimeout(context.Background(), pongWriteTimeout)
			_ = c.write(ctx, msgPong, nil)
			cancel()
		}
	}
}

func (c *streamConn) Read(ctx context.Context) (MsgType, []byte, error) {
	select {
	case m := <-c.in:
		return m.typ, m.data, nil
	case <-c.done:
		// 连接已断开，但断开之前收到的消息仍要先交付。
		select {
		case m := <-c.in:
			return m.typ, m.data, nil
		default:
		}
		return 0, nil, c.err
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (c *streamConn) Write(ctx context.Context, typ MsgType, data []byte) error {
	if typ != MsgText && typ != MsgBinary {
		return errBadFrameType
	}
	return c.write(ctx, typ, data)
}

func (c *streamConn) write(ctx context.Context, typ MsgType, data []byte) error {
	if len(data) > MaxIPCMessage {
		return errFrameTooLarge
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.wsem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err() // 只是没等到写权，没有写出半条消息，连接仍可用
	case <-c.done:
		return c.err
	}
	defer func() { <-c.wsem }()
	select {
	case <-c.done:
		return c.err
	default:
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.nc.SetWriteDeadline(dl)
	} else {
		_ = c.nc.SetWriteDeadline(time.Time{})
	}
	var hdr [ipcHeaderLen]byte
	hdr[0] = byte(typ)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(data)))
	bufs := net.Buffers{hdr[:], data}
	if _, err := bufs.WriteTo(c.nc); err != nil {
		// 写超时或出错可能留下半条消息，连接无法继续使用。
		c.fail(err)
		return err
	}
	return nil
}

func (c *streamConn) Ping(ctx context.Context) error {
	// 丢弃上一轮遗留的应答，避免把旧 pong 当成这一次的。
	select {
	case <-c.pong:
	default:
	}
	if err := c.write(ctx, msgPing, nil); err != nil {
		return err
	}
	select {
	case <-c.pong:
		return nil
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *streamConn) Close() error {
	c.fail(ErrConnClosed)
	return nil
}
