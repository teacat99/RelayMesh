package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/mcp"
)

// stdio 多窗口的「转发进程」一侧（#190 方案 A；主进程一侧见 run_stdio.go 与 internal/mcp/local_proxy.go）。
//
// 转发进程：端口已被一个 RelayMesh 的 stdio 主进程占用时，本进程不再报错退出，而是把 stdin 上的每条 JSON-RPC
// 消息转给主进程处理，响应原样写回 stdout（stdout 上仍然只有 JSON-RPC）。主进程退出（窗口关闭）后，转发进程每秒
// 看一眼端口：没人听了就尝试抢端口，抢到的接管成新的主进程（原地升级，不用重启、不丢 stdin 上的会话），没抢到的
// 重新接到新主进程上。端口绑定是唯一的仲裁，所以不会出现两个主进程。

const (
	// followerAttachTimeout：启动时等对面的主进程可以服务的最长时间（它可能刚绑定端口，还在初始化数据库）。
	followerAttachTimeout = 12 * time.Second
	// followerWatchInterval：转发进程检查主进程是否还在的间隔。
	followerWatchInterval = time.Second
	// followerRetryWindow：一次调用遇到「主进程暂时不在」时，最多等新主进程出现多久。
	followerRetryWindow = 15 * time.Second
)

// sleepContext 睡 d；ctx 先结束则返回 false。
func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// stdioFollower 是转发进程的状态：平时把消息转给主进程；升级为主进程后改为在本进程内直接处理。
type stdioFollower struct {
	cfg  *config.Config
	fw   *mcp.LocalForwarder
	addr string

	mu             sync.Mutex
	hub            *stdioHub        // 升级成主进程后非空
	local          mcp.StdioHandler // 升级成主进程后非空：本进程内直接处理
	lastPromoteErr string           // 上一次接管失败的原因（去重日志）
}

func newStdioFollower(cfg *config.Config, fw *mcp.LocalForwarder) *stdioFollower {
	return &stdioFollower{
		cfg:  cfg,
		fw:   fw,
		addr: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
	}
}

func (f *stdioFollower) localHandler() mcp.StdioHandler {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.local
}

// tryPromote 尝试接管成主进程：抢到端口返回 true；没抢到（别的进程先接管了，或端口被别的程序占着）返回 false。
func (f *stdioFollower) tryPromote() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hub != nil {
		return true
	}
	hub, err := startStdioHub(f.cfg)
	if err != nil {
		if !errors.Is(err, errPortBusy) {
			if msg := err.Error(); msg != f.lastPromoteErr {
				f.lastPromoteErr = msg
				log.Printf("[RelayMesh stdio] 主进程已退出，但本进程接管失败：%v", err)
			}
		}
		return false
	}
	f.hub = hub
	f.local = mcp.LocalStdioHandler(hub.server.MCPServer())
	log.Printf("[RelayMesh stdio] 原主进程已退出，本进程接管为新的主进程（控制台 http://%s/）", f.addr)
	return true
}

// close 在本进程退出时调用：升级成过主进程就把 Hub 优雅关掉（别的转发进程随后会接管）。
func (f *stdioFollower) close() {
	f.mu.Lock()
	hub := f.hub
	f.hub = nil
	f.local = nil
	f.mu.Unlock()
	if hub != nil {
		hub.Close()
	}
}

// watch 每秒看一眼主进程的端口：连不上就尝试接管。端口绑定是唯一的仲裁——主进程只是卡了一下时抢不到端口，
// 什么都不会发生；主进程真的没了才会抢到。接管成功后不再需要监视。
func (f *stdioFollower) watch(ctx context.Context) {
	ticker := time.NewTicker(followerWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if f.localHandler() != nil {
			return
		}
		conn, err := net.DialTimeout("tcp", f.addr, 800*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			continue
		}
		f.tryPromote()
	}
}

// dispatch 处理一条消息：已接管就本进程内处理，否则转发给主进程；遇到「主进程暂时不在」时在 followerRetryWindow
// 内等待 / 接管后重试。请求在连接断开（结果未知）时不会自动重发，以免同一次调用执行两遍。
func (f *stdioFollower) dispatch(ctx context.Context, msg mcp.StdioMessage) ([]byte, error) {
	deadline := time.Now().Add(followerRetryWindow)
	for {
		if local := f.localHandler(); local != nil {
			return local(ctx, msg), nil
		}
		resp, err := f.fw.Forward(ctx, msg)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		retry := false
		switch {
		case errors.Is(err, mcp.ErrHubUnreachable):
			// 请求一定没发出去：能抢到端口就自己当主进程来处理它，否则等别人接管。
			if f.tryPromote() {
				continue
			}
			retry = true
		case errors.Is(err, mcp.ErrKeyRejected), errors.Is(err, mcp.ErrNoKey):
			// 主进程可能刚换人（新钥匙）：重读凭证再试。
			f.fw.ResetKey()
			retry = true
		}
		if !retry || time.Now().After(deadline) {
			return nil, err
		}
		if !sleepContext(ctx, 200*time.Millisecond) {
			return nil, ctx.Err()
		}
	}
}

// handle 是交给 stdio 传输层的处理器。
func (f *stdioFollower) handle(ctx context.Context, msg mcp.StdioMessage) []byte {
	if msg.IsNotification {
		// 通知尽力而为：不能阻塞读循环（连不上主进程时会等），也不产生响应。
		go func() { _, _ = f.dispatch(ctx, msg) }()
		return nil
	}
	resp, err := f.dispatch(ctx, msg)
	if err == nil {
		return resp
	}
	if ctx.Err() != nil {
		return nil // 被客户端取消或进程在退出：不再回复
	}
	return mcp.StdioErrorResponse(msg.ID, -32000, describeForwardError(err, f.cfg.Port))
}

// describeForwardError 把转发失败的原因说成人（和模型）能直接照着做的话。
func describeForwardError(err error, port int) string {
	switch {
	case errors.Is(err, mcp.ErrHubLost):
		return "RelayMesh 主进程在处理这次调用时断开了（多半是它所在的窗口刚关闭）。其它窗口会自动接管主进程，请稍后重新发起这次调用。"
	case errors.Is(err, mcp.ErrHubUnreachable):
		return fmt.Sprintf("连不上本机 :%d 的 RelayMesh 主进程，也没能接管它。请稍后重试；若一直如此，请检查这个端口是否被别的程序占用。", port)
	case errors.Is(err, mcp.ErrNotStdioHub):
		return fmt.Sprintf("本机 :%d 上现在的服务不是 RelayMesh 的 stdio 主进程（可能被别的程序占用，或版本太旧没有多窗口支持）。", port)
	case errors.Is(err, mcp.ErrKeyRejected), errors.Is(err, mcp.ErrNoKey):
		return fmt.Sprintf("读不到或不被接受主进程的接入凭证（%v）。请确认所有窗口由同一个系统用户启动、使用同一份数据目录（由 HOME / XDG_DATA_HOME 决定）。", err)
	default:
		return err.Error()
	}
}

// stdioPortOwnerAdvice 说明端口被占用而本进程又无法接入时该怎么办。
func stdioPortOwnerAdvice(port int, info mcp.LocalHubInfo, probeErr error) string {
	switch {
	case probeErr == nil && info.RunMode == "serve":
		return fmt.Sprintf("端口 :%d 已被一个常驻的 RelayMesh（serve 模式，版本 %s）占用，stdio 进程无法接入它。\n"+
			"请改用它的 HTTP MCP 地址（http://127.0.0.1:%d/mcp，需要令牌时见设置页），或用环境变量 RELAYMESH_PORT 给 stdio 换一个端口。",
			port, info.Version, port)
	case probeErr == nil && info.RunMode == "" && info.ProjectID != "":
		return fmt.Sprintf("端口 :%d 已被一个旧版 RelayMesh（版本 %s，没有 stdio 多窗口支持）占用。\n"+
			"请关闭那个旧窗口（或升级它）后重试，或用环境变量 RELAYMESH_PORT 给本进程换一个端口。", port, info.Version)
	default:
		return fmt.Sprintf("端口 :%d 已被另一个程序占用（不是 RelayMesh 的 stdio 进程）。\n"+
			"请关闭它，或用环境变量 RELAYMESH_PORT 给 stdio 换一个端口；也可以改用已在运行的 RelayMesh 的 HTTP MCP 地址（http://127.0.0.1:%d/mcp）。", port, port)
	}
}

// attachToHub 在启动时确认端口那头是 RelayMesh 的 stdio 主进程（它可能刚绑定端口、还在初始化，所以要等一会儿）。
// 端口恰好在这时空出来（主进程刚退出）就直接接管。返回 nil 表示可以开始工作（已接入或已接管）。
func (f *stdioFollower) attachToHub(ctx context.Context) error {
	deadline := time.Now().Add(followerAttachTimeout)
	var lastErr error
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		info, err := f.fw.Probe(probeCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}

		switch {
		case err == nil:
			if info.RunMode != "stdio" {
				return errors.New(stdioPortOwnerAdvice(f.cfg.Port, info, nil))
			}
			if kerr := f.fw.CheckKey(); kerr != nil {
				return fmt.Errorf("端口 :%d 上是 RelayMesh 的 stdio 进程，但本进程无法接入它：%v\n"+
					"通常是它用了另一份数据目录：请让所有窗口由同一个系统用户启动、使用同一份数据目录（由 HOME / XDG_DATA_HOME 决定，本进程当前为 %s）。",
					f.cfg.Port, kerr, f.cfg.DataDir)
			}
			if info.Version != "" && info.Version != version {
				log.Printf("[RelayMesh stdio] 注意：主进程版本为 v%s，本进程为 v%s；调用按主进程的行为处理。", info.Version, version)
			}
			log.Printf("[RelayMesh stdio] 端口 :%d 已有 RelayMesh 主进程（v%s）在运行：本窗口作为转发进程接入，会话汇总到同一个控制台 http://%s/",
				f.cfg.Port, info.Version, f.addr)
			return nil
		case errors.Is(err, mcp.ErrHubUnreachable):
			// 端口刚好空出来了：自己接管。
			if f.tryPromote() {
				return nil
			}
			lastErr = err
		case errors.Is(err, mcp.ErrNotStdioHub):
			return errors.New(stdioPortOwnerAdvice(f.cfg.Port, mcp.LocalHubInfo{}, err))
		default:
			// 对面接了连接但没及时回应（还在初始化）：继续等。
			lastErr = err
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("等了 %s，端口 :%d 那头仍然没有响应（%v）。请确认占用端口的程序，或用环境变量 RELAYMESH_PORT 换一个端口。",
				followerAttachTimeout, f.cfg.Port, lastErr)
		}
		if !sleepContext(ctx, 300*time.Millisecond) {
			return ctx.Err()
		}
	}
}

// runStdioFollower 是端口已被占用时的运行方式：接入已有的 stdio 主进程；接入不了就带着清楚的说明退出（退出码 1）。
func runStdioFollower(cfg *config.Config) {
	base := fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port)
	fw := mcp.NewLocalForwarder(base, cfg.DataDir, cfg.HostName)
	f := newStdioFollower(cfg, fw)

	ctx, cancel := stdioSignalContext()
	defer cancel()

	if err := f.attachToHub(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "[RelayMesh] Error: %v\n", err)
		os.Exit(1)
	}

	go f.watch(ctx)

	transport := mcp.NewStdioTransportWithHandler(f.handle, os.Stdin, os.Stdout)
	if err := transport.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[RelayMesh stdio] stdio transport exited with error: %v", err)
	}

	f.close()
	log.Printf("[RelayMesh stdio] Exited cleanly.")
}
