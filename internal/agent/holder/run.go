package holder

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
	"github.com/teacat99/RelayMesh/v2/internal/agent/record"
)

// drainPollInterval 是排空关停时检查「是否还有运行中的会话与模型命令」的间隔。
const drainPollInterval = 500 * time.Millisecond

// ServeListener 在 ln 上接受本地连接，交给 host 服务，直到 ctx 取消或监听出错。
// 每条连接先核对对端用户，同时在服务的连接数有上限。
func ServeListener(ctx context.Context, ln net.Listener, host *Host, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	sem := make(chan struct{}, maxConns)
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if err := verifyPeer(nc); err != nil {
			logf("拒绝本地连接: %v", err)
			_ = nc.Close()
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			logf("本地连接数已达上限，拒绝新连接")
			_ = nc.Close()
			continue
		}
		go func() {
			defer func() { <-sem }()
			_ = host.Serve(ctx, NewStreamConn(nc))
		}()
	}
}

// RunOptions 是外置守护进程（`holder run`）的运行参数。
type RunOptions struct {
	// DataDir 是 Agent 的数据目录；守护进程的状态与套接字都放在其下的 holder/ 里。
	DataDir string
	// Socket 覆盖默认套接字路径（空则按 DataDir 推算）。
	Socket  string
	Version string
	// Rev 是守护进程二进制的 Agent 侧代码指纹（见 termproto.ValidAgentRev）；空表示非发布构建。
	Rev    string
	Limits Limits
	// Record 是终端记录的配置（零值取默认；目录默认为 <DataDir>/records）。Record.Disabled 是本机一票否决。
	Record record.Options
	// Exec 是模型执行的配置（零值取默认；输出目录默认为 <DataDir>/runs）。Exec.DenyExec 是本机一票否决。
	Exec ExecOptions
	// SSH 非 nil 表示开启了 SSH 网关（--ssh-gateway）。
	SSH  *SSHOptions
	Logf func(string, ...any)
	// HardenDir 收紧目录访问权限（Windows 设置 ACL）；Unix 为 nil。
	HardenDir func(string) error

	// 以下字段供测试注入。
	Spawner ptyhost.Spawner
	Shell   ShellFunc
	// Ready 在开始监听之后被调用，参数是实际使用的套接字路径。
	Ready func(socket string)
}

// Run 运行外置守护进程直到 ctx 被取消或收到关停请求。返回前会终止全部会话并保存状态：
// 守护进程被有意停止就是在结束会话，下次启动会据此报告「守护进程被停止」。
func Run(ctx context.Context, opts RunOptions) error {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	stateDir := StateDir(opts.DataDir)
	socket := opts.Socket
	if socket == "" {
		socket = SocketPath(opts.DataDir)
	}
	if err := ensureSocketDir(stateDir, opts.HardenDir); err != nil {
		return fmt.Errorf("prepare holder dir: %w", err)
	}
	ro := opts.Record
	if ro.Dir == "" {
		ro.Dir = RecordDir(opts.DataDir)
	}
	if ro.Harden == nil {
		ro.Harden = opts.HardenDir
	}
	eo := opts.Exec
	if eo.Dir == "" {
		eo.Dir = RunsDir(opts.DataDir)
	}
	if eo.Harden == nil {
		eo.Harden = opts.HardenDir
	}
	sshOpts := opts.SSH
	if sshOpts != nil && sshOpts.CredDir == "" {
		// 网关的凭据库默认放在数据目录下；复制一份再补全，不改动调用方传入的对象。
		cp := *sshOpts
		cp.CredDir = SSHCredDir(opts.DataDir)
		if cp.Harden == nil {
			cp.Harden = opts.HardenDir
		}
		sshOpts = &cp
	}
	// 先占住本地套接字，拿到它才有资格去动状态文件与终端记录：NewHost 会读写状态、给「上次遗留」的记录补上结束信息、
	// 回收模型命令的孤儿记录——这些只有「唯一的那个守护进程」才能做。若放在占用检查之前，第二个实例即使随后因
	// 「已经在运行」退出，也已经把第一个实例里仍在运行的会话在记录里标成了「守护进程退出」，并用自己的空状态覆盖了状态文件
	// （真实事故：在装有正式 Agent 的机器上误起了第二个守护进程）。
	ln, err := Listen(socket, opts.HardenDir)
	if err != nil {
		return err
	}
	host, err := NewHost(HostOptions{
		Manager:    ManagerOptions{Spawner: opts.Spawner, Shell: opts.Shell, Limits: opts.Limits, Logf: opts.Logf, SSH: sshOpts},
		Record:     &ro,
		Exec:       &eo,
		Version:    opts.Version,
		Rev:        opts.Rev,
		Persistent: true,
		StateDir:   stateDir,
		Logf:       opts.Logf,
	})
	if err != nil {
		// 套接字是上面刚由本实例创建的（没有别的守护进程占着它），失败就把它撤掉，别留一个没人服务的套接字。
		_ = ln.Close()
		_ = os.Remove(socket)
		return err
	}
	host.Start()

	sctx, cancel := context.WithCancel(ctx)
	var serveRes error
	served := make(chan struct{})
	go func() {
		serveRes = ServeListener(sctx, ln, host, opts.Logf)
		close(served)
	}()
	// 无论从哪条路径返回，都要先让监听停下并删除套接字文件：Go 的 UnixListener 关闭时在部分平台
	// （实测 Windows）不会删除文件，而 Run 返回后进程通常立刻退出，异步关闭根本来不及执行。
	defer func() {
		cancel()
		_ = ln.Close()
		select {
		case <-served:
		case <-time.After(3 * time.Second):
		}
		_ = os.Remove(socket)
	}()

	opts.Logf("会话守护进程已就绪（版本 %s，本地套接字 %s）", opts.Version, socket)
	if opts.Ready != nil {
		opts.Ready(socket)
	}

	var drain <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			opts.Logf("收到停止信号，终止全部会话并退出")
			host.Close(true)
			return nil
		case <-served:
			host.Close(false)
			return fmt.Errorf("local listener failed: %w", serveRes)
		case mode := <-host.ShutdownRequests():
			if mode == ShutdownNow {
				opts.Logf("收到关停请求，终止全部会话并退出")
				host.Close(true)
				return nil
			}
			sessions, runs := host.RunningWork()
			opts.Logf("收到排空请求：不再接受新会话和新命令，等待运行中的会话（%d 个）与模型命令（%d 条）结束", sessions, runs)
			t := time.NewTicker(drainPollInterval)
			defer t.Stop()
			drain = t.C
		case <-drain:
			// 会话与模型命令分属两个管理器，必须两边都归零才能退出：只数会话会在命令还在跑时把它们终止。
			if sessions, runs := host.RunningWork(); sessions == 0 && runs == 0 {
				opts.Logf("所有会话和模型命令都已结束，守护进程退出")
				host.Close(true)
				return nil
			}
		}
	}
}
