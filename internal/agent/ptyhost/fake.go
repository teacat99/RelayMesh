package ptyhost

import (
	"bytes"
	"io"
	"os"
	"sync"
	"time"
)

// FakeSpawner 是内存里的假终端工厂，供单元测试驱动会话逻辑（不依赖真实 PTY 与子进程）。
// 生产代码不使用它。
type FakeSpawner struct {
	mu    sync.Mutex
	terms []*FakeTerminal
	// Err 非空时 Spawn 返回该错误。
	Err error
	// HoldTTY 为 true 时，进程退出后读端不会自动 EOF——模拟「后台进程仍握着终端」的场景。
	HoldTTY bool
}

// Spawn 实现 Spawner。
func (f *FakeSpawner) Spawn(spec Spec) (Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	rows, cols := DefaultSize(spec.Rows, spec.Cols)
	t := &FakeTerminal{
		Spec:   spec,
		pid:    1000 + len(f.terms),
		rows:   rows,
		cols:   cols,
		exited: make(chan struct{}),
		hold:   f.HoldTTY,
	}
	t.cond = sync.NewCond(&t.mu)
	f.terms = append(f.terms, t)
	return t, nil
}

// Terminals 返回迄今创建的全部假终端（按创建顺序）。
func (f *FakeSpawner) Terminals() []*FakeTerminal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*FakeTerminal(nil), f.terms...)
}

// FakeTerminal 是 Terminal 的内存实现。测试通过 Emit 制造输出、Exit 结束进程、Input 检查收到的输入。
type FakeTerminal struct {
	Spec Spec

	mu       sync.Mutex
	cond     *sync.Cond
	pending  []byte
	closed   bool
	ended    bool // 进程已退出且（非 hold 时）读端已到尾
	hold     bool
	deadline time.Time
	pid      int
	rows     uint16
	cols     uint16
	input    bytes.Buffer
	resizes  [][2]uint16
	killed   int

	exited   chan struct{}
	exitOnce sync.Once
	status   ExitStatus
}

// Emit 模拟子进程向终端输出数据。
func (t *FakeTerminal) Emit(data []byte) {
	t.mu.Lock()
	t.pending = append(t.pending, data...)
	t.mu.Unlock()
	t.cond.Broadcast()
}

// Exit 模拟子进程退出。
func (t *FakeTerminal) Exit(st ExitStatus) {
	t.exitOnce.Do(func() {
		t.mu.Lock()
		t.status = st
		if !t.hold {
			t.ended = true
		}
		t.mu.Unlock()
		close(t.exited)
		t.cond.Broadcast()
	})
}

// Input 返回迄今写入终端的全部输入。
func (t *FakeTerminal) Input() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.input.Bytes()...)
}

// Size 返回当前尺寸。
func (t *FakeTerminal) Size() (rows, cols uint16) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rows, t.cols
}

// Resizes 返回全部 Resize 调用记录（行、列）。
func (t *FakeTerminal) Resizes() [][2]uint16 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([][2]uint16(nil), t.resizes...)
}

// KillCount 返回 Kill 被调用的次数。
func (t *FakeTerminal) KillCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.killed
}

// IsClosed 返回 Close 是否已被调用。
func (t *FakeTerminal) IsClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func (t *FakeTerminal) Read(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for {
		if len(t.pending) > 0 {
			n := copy(b, t.pending)
			t.pending = t.pending[n:]
			return n, nil
		}
		if t.closed {
			return 0, io.EOF
		}
		if t.ended {
			return 0, io.EOF
		}
		if !t.deadline.IsZero() && !time.Now().Before(t.deadline) {
			return 0, os.ErrDeadlineExceeded
		}
		t.cond.Wait()
	}
}

func (t *FakeTerminal) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, os.ErrClosed
	}
	t.input.Write(b)
	return len(b), nil
}

func (t *FakeTerminal) Resize(rows, cols uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows, t.cols = rows, cols
	t.resizes = append(t.resizes, [2]uint16{rows, cols})
	return nil
}

func (t *FakeTerminal) Pid() int { return t.pid }

func (t *FakeTerminal) Wait() ExitStatus {
	<-t.exited
	return t.status
}

// Kill 模拟被信号终止。
func (t *FakeTerminal) Kill() {
	t.mu.Lock()
	t.killed++
	t.mu.Unlock()
	t.Exit(ExitStatus{Code: -1, Signal: "hangup"})
}

func (t *FakeTerminal) SetReadDeadline(tm time.Time) error {
	t.mu.Lock()
	t.deadline = tm
	t.mu.Unlock()
	if !tm.IsZero() {
		time.AfterFunc(time.Until(tm)+time.Millisecond, t.cond.Broadcast)
	}
	t.cond.Broadcast()
	return nil
}

func (t *FakeTerminal) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.cond.Broadcast()
	return nil
}
