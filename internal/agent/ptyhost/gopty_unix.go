//go:build !windows

package ptyhost

import (
	"os"
	"syscall"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

// killGrace 是「挂断」之后等待进程自行退出、再强制结束的时间。
const killGrace = 2 * time.Second

// afterStart 在子进程启动成功后关闭父进程持有的从端（slave）。
//
// 子进程已经拿到了自己的副本；父进程若继续持有从端，主端的 Read 永远不会在子进程退出后返回 EIO，
// 也就无法通过读到 EOF 判断输出已排空。
func afterStart(p pty.Pty) {
	if up, ok := p.(pty.UnixPty); ok {
		_ = up.Slave().Close()
	}
}

// killTree 向子进程所在的进程组发送 SIGHUP，宽限期后仍未退出则 SIGKILL。
// 子进程是独立会话的首进程（go-pty 设置了 Setsid），进程组号等于它的 pid。
func killTree(cmd *pty.Cmd, done <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	select {
	case <-done:
		return
	default:
	}
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	go func() {
		timer := time.NewTimer(killGrace)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		}
	}()
}

func exitStatus(ps *os.ProcessState) ExitStatus {
	if ps == nil {
		return ExitStatus{Code: -1}
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ExitStatus{Code: -1, Signal: ws.Signal().String()}
	}
	return ExitStatus{Code: ps.ExitCode()}
}

// openIO 为伪终端主端建立可轮询的读写通道。
//
// go-pty（经 creack/pty）在创建伪终端时调用了 (*os.File).Fd()，这会把主端文件永久切回阻塞模式：
// 此后 SetReadDeadline 无效，Close 也无法唤醒阻塞在 read(2) 里的 goroutine。
// 这里复制一份文件描述符并按非阻塞方式重新包装，得到一个接入运行时轮询器的 *os.File。
// 原主端文件仍保留，仅用于 TIOCSWINSZ（调整尺寸）这类 ioctl，两者指向同一个打开的文件描述。
func openIO(p pty.Pty) (ioFile, error) {
	up, ok := p.(pty.UnixPty)
	if !ok {
		return nil, ErrUnsupported
	}
	nfd, err := syscall.Dup(int(up.Master().Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(nfd)
	if err := syscall.SetNonblock(nfd, true); err != nil {
		_ = syscall.Close(nfd)
		return nil, err
	}
	f := os.NewFile(uintptr(nfd), "ptmx")
	if f == nil {
		_ = syscall.Close(nfd)
		return nil, ErrUnsupported
	}
	return f, nil
}

func closePty(p pty.Pty) error {
	// 从端可能已在 afterStart 中关闭，重复关闭的错误没有意义。
	_ = p.Close()
	return nil
}
