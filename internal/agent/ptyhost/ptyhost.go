// Package ptyhost 把「伪终端 + 子进程」的平台细节隔离在一个窄接口之后。
//
// Agent 的会话、缓冲、流控与协议逻辑只依赖这里的接口，因此：
//   - 单元测试可以用内存里的假终端驱动（见 fake.go），不依赖真实 PTY；
//   - 如果日后需要替换底层 PTY 库（体积、依赖或平台兼容性问题），只改这一个包。
package ptyhost

import (
	"errors"
	"io"
	"time"
)

// Spec 描述要在伪终端里启动的进程。
type Spec struct {
	Path string   // 可执行文件路径
	Args []string // 参数（不含 argv[0]）
	Env  []string // 完整环境（KEY=VALUE）；nil 表示继承当前进程环境
	Dir  string   // 工作目录；空表示继承
	Rows uint16   // 初始尺寸，0 表示 24×80
	Cols uint16
}

// ExitStatus 是子进程的退出信息。
type ExitStatus struct {
	// Code 是退出码；被信号终止时为 -1。
	Code int
	// Signal 是终止进程的信号名（如 "hangup"、"killed"），正常退出为空。
	Signal string
}

// Terminal 是一个已经启动了子进程的伪终端（主端视角）。
//
// 并发约定：Read / Write / Resize / Kill / SetReadDeadline 可以来自不同的 goroutine；
// Wait 可被多次调用，均在进程退出后返回同一结果。
type Terminal interface {
	// Read 读取子进程输出；Write 向子进程写入输入。
	io.ReadWriter

	// Resize 调整终端尺寸（行、列）。
	Resize(rows, cols uint16) error

	// Pid 返回子进程号（供展示）。
	Pid() int

	// Wait 阻塞到子进程退出。
	Wait() ExitStatus

	// Kill 尽力终止子进程及其进程树：先礼貌地挂断，仍未退出则强制结束。不阻塞。
	Kill()

	// SetReadDeadline 为 Read 设置期限，用于进程退出后有界地排空残余输出。
	// 平台不支持时返回错误，调用方应改用 Close 来解除阻塞的 Read。
	SetReadDeadline(t time.Time) error

	// Close 释放伪终端资源并解除阻塞中的 Read / Write（幂等）。
	Close() error
}

// Spawner 创建伪终端会话。
type Spawner interface {
	Spawn(spec Spec) (Terminal, error)
}

// ErrUnsupported 表示当前平台不支持请求的操作。
var ErrUnsupported = errors.New("ptyhost: unsupported on this platform")

// DefaultSize 把 0 值尺寸规整为 24×80。
func DefaultSize(rows, cols uint16) (uint16, uint16) {
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	return rows, cols
}
