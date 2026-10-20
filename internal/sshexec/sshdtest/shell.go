package sshdtest

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// 测试用的交互式 shell：支持伪终端请求、窗口调整，以及一个只有几条内建命令的迷你 shell。
// 它不依赖本机真实的 shell 与 PTY，行为完全可预期，适合断言「输入 → 输出 → 退出码」。
//
// 内建命令：
//
//	exit [N]     以 N（默认 0）退出
//	size         打印当前窗口大小「行 列」
//	whoami       打印登录用户名
//	echo ARGS    原样打印参数
//	big N        打印 N 行 "line-i"（成块写出，用于验证输出量大时的流控）
//	其他         打印 "command not found: X"

type winState struct {
	mu         sync.Mutex
	rows, cols int
}

func (w *winState) set(rows, cols int) {
	w.mu.Lock()
	w.rows, w.cols = rows, cols
	w.mu.Unlock()
}

func (w *winState) get() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rows, w.cols
}

// Pty 返回最近一次收到的伪终端请求（含窗口调整后的大小）。
func (s *Server) Pty() Pty {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pty
}

// Env 返回通过 env 请求收到并被接受的环境变量（副本）。
func (s *Server) Env() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.env))
	for k, v := range s.env {
		out[k] = v
	}
	return out
}

// Shells 返回累计启动的交互式 shell 数。
func (s *Server) Shells() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shells
}

// MuteKeepalive 让服务器不再应答 keepalive@openssh.com（模拟 NAT 静默丢包）：之后客户端的保活请求会一直等不到回复。
func (s *Server) MuteKeepalive(mute bool) {
	s.mu.Lock()
	s.muteKA = mute
	s.mu.Unlock()
}

// Keepalives 返回累计收到的保活请求数。
func (s *Server) Keepalives() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keepalives
}

// globalRequests 处理连接级请求：与 ssh.DiscardRequests 一样一律回绝，但会统计保活并可选择不予应答。
func (s *Server) globalRequests(reqs <-chan *ssh.Request) {
	for r := range reqs {
		if r.Type == "keepalive@openssh.com" {
			s.mu.Lock()
			s.keepalives++
			mute := s.muteKA
			s.mu.Unlock()
			if mute {
				continue
			}
		}
		if r.WantReply {
			_ = r.Reply(false, nil)
		}
	}
}

// miniShell 运行迷你 shell，返回退出码。输入端关闭（客户端断开 / 关闭通道）时以 0 退出。
func (s *Server) miniShell(ch ssh.Channel, win *winState) int {
	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(ch, format, args...)
	}
	write("welcome %s\r\n$ ", s.opts.User)
	var line []byte
	buf := make([]byte, 1024)
	for {
		n, err := ch.Read(buf)
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\n':
				write("\r\n")
				cmd := strings.TrimSpace(string(line))
				line = line[:0]
				if code, exit := s.shellCommand(write, cmd, win); exit {
					return code
				}
				write("$ ")
			case 0x7f, 0x08:
				if len(line) > 0 {
					line = line[:len(line)-1]
					write("\b \b")
				}
			case 0x04: // Ctrl-D
				return 0
			default:
				line = append(line, b)
				_, _ = ch.Write([]byte{b}) // 伪终端的回显
			}
		}
		if err != nil {
			return 0
		}
	}
}

func (s *Server) shellCommand(write func(string, ...any), cmd string, win *winState) (code int, exit bool) {
	if cmd == "" {
		return 0, false
	}
	name, rest, _ := strings.Cut(cmd, " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "exit":
		n, _ := strconv.Atoi(rest)
		return n, true
	case "size":
		rows, cols := win.get()
		write("%d %d\r\n", rows, cols)
	case "whoami":
		write("%s\r\n", s.opts.User)
	case "echo":
		write("%s\r\n", rest)
	case "big":
		n, _ := strconv.Atoi(rest)
		// 成块写出（约 2 KiB 一个 SSH 数据包），而不是一行一个包：真实程序刷屏时也是成块写出的。
		// 一行一包会产生三万个十来个字节的小帧，在竞态检测器下写协程跟不上，Hub 的 512 帧发送队列被撑满而
		// 以「client too slow」断开一个完全健康的客户端（任务 #133）。
		var batch strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&batch, "line-%d\r\n", i)
			if batch.Len() >= 2048 {
				write("%s", batch.String())
				batch.Reset()
			}
		}
		if batch.Len() > 0 {
			write("%s", batch.String())
		}
	default:
		write("command not found: %s\r\n", name)
	}
	return 0, false
}
