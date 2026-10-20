package mcp

// stdio 多窗口的转发端（#190 方案 A）：第二个及之后的 stdio 进程把每条消息转给主进程处理。
// 主进程一侧见 local_proxy.go；进程角色切换（跟随 / 接管）在 cmd/relaymesh/run_stdio.go。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	// ErrHubUnreachable：连不上（那个端口没有进程在听）。请求一定没有发出去，可以放心重试，或改由自己接管。
	ErrHubUnreachable = errors.New("连不上本机的 RelayMesh 主进程")
	// ErrHubLost：请求发出后连接断了（主进程中途退出）。这次调用有没有执行未知，不能自动重发。
	ErrHubLost = errors.New("RelayMesh 主进程在处理这次调用时断开了")
	// ErrNotStdioHub：端口那头不是 RelayMesh 的 stdio 主进程（别的程序、常驻服务，或版本太旧没有转发入口）。
	ErrNotStdioHub = errors.New("端口上的服务不是 RelayMesh 的 stdio 主进程")
	// ErrKeyRejected：钥匙文件读到了，但主进程不认（不是同一份数据目录，或主进程刚换人）。
	ErrKeyRejected = errors.New("本机转发入口不接受这把钥匙")
	// ErrNoKey：数据目录里读不到可用的接入凭证。
	ErrNoKey = errors.New("读不到本机转发入口的接入凭证")
)

// LocalHubInfo 是端口那头 /api/v1/auth/status 里我们关心的几个公开字段。
type LocalHubInfo struct {
	RunMode   string `json:"run_mode"`
	Version   string `json:"version"`
	ProjectID string `json:"project_id"`
}

// LocalForwarder 把 stdio 消息转发给同机另一个 stdio 进程里的 Hub。
type LocalForwarder struct {
	baseURL  string
	dataDir  string
	hostName string
	client   *http.Client

	mu  sync.Mutex
	key string // 缓存的钥匙；空 = 下次发送前重新读文件
}

// NewLocalForwarder 创建转发端。baseURL 形如 http://127.0.0.1:18775；dataDir 是读接入凭证的数据目录；
// hostName 是本进程自己的主机名，随每条消息带给主进程，让每个窗口按自己的身份开会话。
func NewLocalForwarder(baseURL, dataDir, hostName string) *LocalForwarder {
	transport := &http.Transport{
		// 只走本机回环：环境里的 HTTP(S)_PROXY 不应该被用上（Proxy 留空 = 不走代理）。
		DialContext:         (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     60 * time.Second,
	}
	return &LocalForwarder{
		baseURL:  strings.TrimRight(baseURL, "/"),
		dataDir:  dataDir,
		hostName: hostName,
		// 不设整体超时：interactive_feedback 这类调用本来就会等很久；取消靠传入的 ctx。
		client: &http.Client{Transport: transport},
	}
}

// BaseURL 返回主进程的根地址。
func (f *LocalForwarder) BaseURL() string { return f.baseURL }

// CloseIdleConnections 丢掉连接池里闲置的连接。主进程退出后，传输层要等读循环看到对端关闭才会把闲置连接作废；
// 在这个空档里发的请求会写进一条已经半关的连接，得到「发出去后断了」（ErrHubLost：结果未知、不能自动重发）。
// 真实场景里主进程退出与下一次调用之间隔着很久，空档早已过去；测试要「主进程刚走、立刻再调用」时，先调用它把空档收掉。
func (f *LocalForwarder) CloseIdleConnections() { f.client.CloseIdleConnections() }

// CheckKey 确认数据目录里能读到接入凭证（不发网络请求）。
func (f *LocalForwarder) CheckKey() error {
	_, err := f.currentKey()
	return err
}

// ResetKey 丢掉缓存的钥匙，下次发送前重新读文件（主进程换人后使用）。
func (f *LocalForwarder) ResetKey() {
	f.mu.Lock()
	f.key = ""
	f.mu.Unlock()
}

func (f *LocalForwarder) currentKey() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key != "" {
		return f.key, nil
	}
	info, err := ReadLocalProxyInfo(f.dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w：数据目录 %s 里没有接入凭证（主进程用的不是同一份数据目录，或版本太旧）", ErrNoKey, f.dataDir)
		}
		return "", fmt.Errorf("%w：%v", ErrNoKey, err)
	}
	f.key = info.Key
	return f.key, nil
}

// classifyForwardError 把传输层错误归成「请求没发出去」（ErrHubUnreachable）与「发出去后断了」（ErrHubLost）两类；
// 调用方自己取消（ctx 结束）时原样返回 ctx 的错误。
func classifyForwardError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return fmt.Errorf("%w：%v", ErrHubUnreachable, err)
	}
	return fmt.Errorf("%w：%v", ErrHubLost, err)
}

// Probe 读端口那头的 /api/v1/auth/status（匿名公开接口），用来确认对面是不是 RelayMesh 以及它的运行方式与版本。
func (f *LocalForwarder) Probe(ctx context.Context) (LocalHubInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+"/api/v1/auth/status", nil)
	if err != nil {
		return LocalHubInfo{}, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return LocalHubInfo{}, classifyForwardError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return LocalHubInfo{}, fmt.Errorf("%w：/api/v1/auth/status 返回 HTTP %d", ErrNotStdioHub, resp.StatusCode)
	}
	var info LocalHubInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return LocalHubInfo{}, fmt.Errorf("%w：无法解析 /api/v1/auth/status 的响应", ErrNotStdioHub)
	}
	return info, nil
}

// Forward 把一条消息转给主进程：请求返回主进程给出的单行 JSON 响应；通知返回 (nil, nil)。
// ctx 取消会中断这次 HTTP 调用，主进程那头的请求上下文随连接断开而取消（等价于 notifications/cancelled）。
func (f *LocalForwarder) Forward(ctx context.Context, msg StdioMessage) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		key, err := f.currentKey()
		if err != nil {
			return nil, err
		}
		status, body, err := f.post(ctx, key, msg.Raw)
		if err != nil {
			return nil, err
		}
		switch status {
		case http.StatusOK:
			body = bytes.TrimSpace(body)
			if len(body) == 0 {
				return nil, nil
			}
			if !json.Valid(body) {
				return nil, errors.New("本机转发入口返回了无法解析的响应")
			}
			return body, nil
		case http.StatusAccepted:
			return nil, nil
		case http.StatusUnauthorized:
			// 主进程可能刚换人（新钥匙）：重读一次文件，钥匙变了才值得再试。
			if attempt == 0 {
				f.ResetKey()
				if newKey, kerr := f.currentKey(); kerr == nil && newKey != key {
					continue
				}
			}
			return nil, ErrKeyRejected
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			return nil, ErrNotStdioHub
		default:
			return nil, fmt.Errorf("本机转发入口返回 HTTP %d", status)
		}
	}
}

func (f *LocalForwarder) post(ctx context.Context, key string, raw []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL+LocalProxyPath, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(LocalProxyKeyHeader, key)
	if f.hostName != "" {
		req.Header.Set(LocalProxyHostHeader, f.hostName)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, nil, classifyForwardError(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxStdioMessageSize+1))
	if err != nil {
		return 0, nil, classifyForwardError(ctx, err)
	}
	if len(body) > MaxStdioMessageSize {
		return 0, nil, fmt.Errorf("本机转发入口的响应超过 %d 字节", MaxStdioMessageSize)
	}
	return resp.StatusCode, body, nil
}
