package mcp

// 本机转发入口（stdio 多窗口，#190 方案 A）。
//
// 背景：stdio 直驱模式（`relaymesh mcp stdio`）由 AI 客户端拉起，一个窗口一个进程；这个进程同时持有数据库、
// 18775 端口和网页控制台。用户在编辑器里开第二个窗口时，第二个进程发现端口已被占用，过去只能报错退出。
//
// 现在的做法：第一个起来的进程是「主进程」（照旧持有数据库 / 端口 / 控制台），并多做两件事：
//  1. 在数据目录里写一份只有本人可读的接入凭证（随机钥匙，见 WriteLocalProxyInfo）；
//  2. 多开一个只认「本机回环 + 钥匙」的转发入口 LocalProxyPath（本文件的 ServeLocalProxy）。
//
// 之后的进程发现端口被占用、确认对面是 RelayMesh 的 stdio 主进程后，读同一份数据目录里的钥匙，把自己 stdin 上的
// 每条 JSON-RPC 消息转给主进程处理（见 LocalForwarder），各自按自己的 workflow_id 开会话。主进程退出时，剩下的进程
// 里谁先抢到端口谁接管成新的主进程（端口绑定本身就是仲裁，见 cmd/relaymesh/run_stdio.go）。
//
// 安全边界：
//   - 这个入口只存在于 stdio 主进程（服务端模式、Docker 与生产 Hub 永远没有：没设置钥匙时一律 404）；
//   - 必须来自本机回环地址，并带正确的钥匙；钥匙是 32 字节随机数，只放在数据目录（0700）下的 0600 文件里，
//     每次主进程启动都会重新生成；
//   - 钥匙不对的请求按次数限流（防刷日志与 CPU），正确的钥匙不受限流影响；
//   - 浏览器页面发不出这个请求：需要自定义请求头，会先触发预检，而 CORS 白名单里没有这个头。

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// LocalProxyPath 是 stdio 主进程上给「同机其它 stdio 进程」留的转发入口（POST，一次一条 JSON-RPC 消息）。
	// 刻意不放在 /mcp/ 之下：那个前缀归 `/mcp/*path`（路径里带令牌）所有。
	LocalProxyPath = "/mcp-local"
	// LocalProxyKeyHeader 携带钥匙。
	LocalProxyKeyHeader = "X-RelayMesh-Local-Key"
	// LocalProxyHostHeader 携带转发进程自己的主机名（RELAYMESH_HOST_NAME），让每个窗口按自己的身份开会话。
	LocalProxyHostHeader = "X-RelayMesh-Host-Name"
	// LocalProxyInfoFile 是接入凭证在数据目录里的文件名。
	LocalProxyInfoFile = "stdio-hub.json"

	localProxyMaxFails   = 20
	localProxyFailWindow = time.Minute
)

// LocalProxyInfo 是主进程写在数据目录里的接入凭证。Key 是机密：任何日志 / 报错都不得带上它。
type LocalProxyInfo struct {
	Key       string `json:"key"`
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	Version   string `json:"version"`
	StartedAt string `json:"started_at"`
}

// NewLocalProxyKey 生成一把 32 字节的随机钥匙（十六进制）。
func NewLocalProxyKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func localProxyInfoPath(dataDir string) string {
	return filepath.Join(dataDir, LocalProxyInfoFile)
}

// WriteLocalProxyInfo 把接入凭证原子地写进数据目录（文件权限 0600）。
func WriteLocalProxyInfo(dataDir string, info LocalProxyInfo) error {
	if strings.TrimSpace(info.Key) == "" {
		return errors.New("钥匙为空")
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	path := localProxyInfoPath(dataDir)
	// CreateTemp 创建的文件权限就是 0600；写完再改名，读的一方永远看不到写了一半的内容。
	tmp, err := os.CreateTemp(dataDir, ".stdio-hub-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Windows 上目标文件被别的进程打开时改名可能失败：退回直接覆盖写。
		_ = os.Remove(tmpName)
		return os.WriteFile(path, data, 0o600)
	}
	return nil
}

// ReadLocalProxyInfo 读取数据目录里的接入凭证。
func ReadLocalProxyInfo(dataDir string) (LocalProxyInfo, error) {
	data, err := os.ReadFile(localProxyInfoPath(dataDir))
	if err != nil {
		return LocalProxyInfo{}, err
	}
	var info LocalProxyInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return LocalProxyInfo{}, fmt.Errorf("接入凭证文件格式不对: %w", err)
	}
	if strings.TrimSpace(info.Key) == "" {
		return LocalProxyInfo{}, errors.New("接入凭证文件里没有钥匙")
	}
	return info, nil
}

// RemoveLocalProxyInfo 只在文件里还是这把钥匙时才删除：新的主进程可能已经换了新钥匙，不能被老进程的收尾删掉。
func RemoveLocalProxyInfo(dataDir, key string) {
	info, err := ReadLocalProxyInfo(dataDir)
	if err != nil || info.Key != key {
		return
	}
	_ = os.Remove(localProxyInfoPath(dataDir))
}

// localProxyGuard 给「钥匙不对」的请求限流：窗口内失败次数到上限后返回 429。
type localProxyGuard struct {
	mu          sync.Mutex
	windowStart time.Time
	fails       int
}

func (g *localProxyGuard) roll(now time.Time) {
	if now.Sub(g.windowStart) > localProxyFailWindow {
		g.windowStart = now
		g.fails = 0
	}
}

func (g *localProxyGuard) blocked(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.roll(now)
	return g.fails >= localProxyMaxFails
}

func (g *localProxyGuard) fail(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.roll(now)
	g.fails++
}

// SetLocalProxyKey 打开（非空）或关闭（空串）转发入口。只有 stdio 主进程会调用；其它场景不设置，入口一律 404。
func (s *Server) SetLocalProxyKey(key string) {
	s.localProxyMu.Lock()
	s.localProxyKey = key
	s.localProxyMu.Unlock()
}

func (s *Server) localProxyKeyValue() string {
	s.localProxyMu.RLock()
	defer s.localProxyMu.RUnlock()
	return s.localProxyKey
}

// isLoopbackRemote 判断请求的对端地址是不是本机回环（用 TCP 对端地址，不信任任何转发头）。
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func writeLocalProxyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      nil,
		Error:   &rpcError{Code: -32000, Message: message},
	})
}

// handleLocalMessage 用本机 stdio 的可信凭据处理一条消息，返回序列化好的响应（通知 / 无响应返回 nil）。
// stdio 主进程自己的 stdin 与转发入口共用这一条路径，两边的行为因此一致。
func (s *Server) handleLocalMessage(ctx context.Context, hostName string, req *jsonRPCRequest) []byte {
	credCtx := LocalStdioCredential(hostName)
	ctx = context.WithValue(ctx, credCtxKey, credCtx)
	execRes := s.HandleRPCRequest(ctx, credCtx, req)
	if execRes.IsNotification || execRes.Response == nil {
		return nil
	}
	data, err := json.Marshal(execRes.Response)
	if err != nil {
		return nil
	}
	return data
}

// ServeLocalProxy 处理同机其它 stdio 进程转发来的一条 JSON-RPC 消息：
//   - 请求：200 + 单行 JSON 响应；
//   - 通知（没有 id 或 notifications/*）：202，没有响应体；
//   - 客户端断开连接 = 取消这次调用（和 stdio 的 notifications/cancelled 等价，请求上下文随连接取消）。
func (s *Server) ServeLocalProxy(w http.ResponseWriter, r *http.Request) {
	key := s.localProxyKeyValue()
	if key == "" {
		// 没开：表现得像这个入口根本不存在（常驻服务、Docker、生产 Hub 永远走这条）。
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRemote(r.RemoteAddr) {
		writeLocalProxyError(w, http.StatusForbidden, "forbidden: this endpoint only accepts loopback connections")
		return
	}

	now := time.Now()
	got := r.Header.Get(LocalProxyKeyHeader)
	if subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
		if s.localProxyGuard.blocked(now) {
			w.Header().Set("Retry-After", "60")
			writeLocalProxyError(w, http.StatusTooManyRequests, "too many failed attempts")
			return
		}
		s.localProxyGuard.fail(now)
		writeLocalProxyError(w, http.StatusUnauthorized, "unauthorized: invalid or missing local key")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxStdioMessageSize))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(bytes.TrimSpace(body), &req); err != nil {
		// 与 stdio 一致：解析失败回一条 -32700，而不是 HTTP 错误。
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(StdioErrorResponse(nil, -32700, "Parse error"))
		return
	}

	// 取消由转发进程用「断开连接」表达，不会再转发 notifications/cancelled；收到了也只是忽略。
	if req.Method == "notifications/cancelled" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	hostName := strings.TrimSpace(r.Header.Get(LocalProxyHostHeader))
	if hostName == "" && s.cfg != nil {
		hostName = s.cfg.HostName
	}
	resp := s.handleLocalMessage(r.Context(), hostName, &req)

	notification := !req.HasID() || strings.HasPrefix(req.Method, "notifications/")
	if notification || resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}
