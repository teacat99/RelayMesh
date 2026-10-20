package ptyhost

import (
	"fmt"
	"io"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

// NewSpawner 返回基于 go-pty 的真实伪终端实现（Unix 使用 PTY，Windows 使用 ConPTY）。
func NewSpawner() Spawner { return goSpawner{} }

type goSpawner struct{}

// ioFile 是可设置读期限、且 Close 能唤醒阻塞中 Read 的读写通道。
type ioFile interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
}

type goTerminal struct {
	p    pty.Pty
	rw   ioFile // 非 nil 时用它收发数据（Unix）；nil 时直接使用 p（Windows ConPTY）
	cmd  *pty.Cmd
	pid  int
	done chan struct{}

	status ExitStatus

	closeOnce sync.Once
	closeErr  error
}

// Spawn 创建伪终端并在其中启动进程。
func (goSpawner) Spawn(spec Spec) (Terminal, error) {
	if spec.Path == "" {
		return nil, fmt.Errorf("ptyhost: empty command path")
	}
	p, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("ptyhost: create pty: %w", err)
	}
	// 必须在 openIO 之前：openIO 会把主端复制为非阻塞，而这里读取从端的属性不影响它。
	enableUTF8Input(p)
	rw, err := openIO(p)
	if err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("ptyhost: open pty io: %w", err)
	}
	fail := func() {
		if rw != nil {
			_ = rw.Close()
		}
		_ = p.Close()
	}
	rows, cols := DefaultSize(spec.Rows, spec.Cols)
	// 必须在启动进程之前设置尺寸，shell 启动时读到的才是正确值。
	if err := p.Resize(int(cols), int(rows)); err != nil {
		fail()
		return nil, fmt.Errorf("ptyhost: set initial size: %w", err)
	}

	cmd := p.Command(spec.Path, spec.Args...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	if err := cmd.Start(); err != nil {
		fail()
		return nil, fmt.Errorf("ptyhost: start %s: %w", spec.Path, err)
	}

	t := &goTerminal{p: p, rw: rw, cmd: cmd, done: make(chan struct{})}
	if cmd.Process != nil {
		t.pid = cmd.Process.Pid
	}
	afterStart(p)

	go func() {
		_ = cmd.Wait()
		t.status = exitStatus(cmd.ProcessState)
		close(t.done)
	}()
	return t, nil
}

func (t *goTerminal) Read(b []byte) (int, error) {
	if t.rw != nil {
		return t.rw.Read(b)
	}
	return t.p.Read(b)
}

func (t *goTerminal) Write(b []byte) (int, error) {
	if t.rw != nil {
		return t.rw.Write(b)
	}
	return t.p.Write(b)
}

func (t *goTerminal) Pid() int { return t.pid }

func (t *goTerminal) Resize(rows, cols uint16) error {
	rows, cols = DefaultSize(rows, cols)
	// go-pty 的参数顺序是（宽=列，高=行）。
	return t.p.Resize(int(cols), int(rows))
}

func (t *goTerminal) Wait() ExitStatus {
	<-t.done
	return t.status
}

func (t *goTerminal) Kill() { killTree(t.cmd, t.done) }

func (t *goTerminal) SetReadDeadline(tm time.Time) error {
	if t.rw != nil {
		return t.rw.SetReadDeadline(tm)
	}
	return ErrUnsupported
}

func (t *goTerminal) Close() error {
	t.closeOnce.Do(func() {
		if t.rw != nil {
			_ = t.rw.Close() // 先关读写通道：唤醒阻塞中的 Read / Write
		}
		t.closeErr = closePty(t.p)
	})
	return t.closeErr
}
