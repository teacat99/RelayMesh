package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/mcp"
)

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// stdioTestConfig 构造一份隔离的 stdio 配置：临时数据目录、空闲端口、关掉 HTTPS。
func stdioTestConfig(t *testing.T) *config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "localappdata"))
	t.Setenv("RELAYMESH_PORT", strconv.Itoa(freeLoopbackPort(t)))
	t.Setenv("RELAYMESH_HOST_NAME", "unit-test-host")
	cfg := config.LoadWithMode("stdio")
	cfg.Version = version
	cfg.TLSEnabled = false
	return cfg
}

func mustStartHub(t *testing.T, cfg *config.Config) *stdioHub {
	t.Helper()
	hub, err := startStdioHub(cfg)
	if err != nil {
		t.Fatalf("启动主进程失败：%v", err)
	}
	t.Cleanup(hub.Close)
	return hub
}

func baseURL(cfg *config.Config) string { return fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port) }

func newTestFollower(cfg *config.Config, hostName string) *stdioFollower {
	return newStdioFollower(cfg, mcp.NewLocalForwarder(baseURL(cfg), cfg.DataDir, hostName))
}

func toolsListMsg(id int) mcp.StdioMessage {
	return mcp.StdioMessage{
		Raw:    []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list"}`, id)),
		Method: "tools/list",
		ID:     float64(id),
	}
}

func assertToolsList(t *testing.T, resp []byte, err error, id int) {
	t.Helper()
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	var parsed struct {
		ID     float64 `json:"id"`
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
	}
	if jerr := json.Unmarshal(resp, &parsed); jerr != nil || int(parsed.ID) != id || len(parsed.Result.Tools) == 0 {
		t.Fatalf("响应不对（id=%d）：%v %s", id, jerr, resp)
	}
}

func httpGetStatus(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失败：%v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestStartStdioHubArbitratesThePort(t *testing.T) {
	cfg := stdioTestConfig(t)
	hubA := mustStartHub(t, cfg)

	// 主进程把接入凭证写进数据目录：只有本人可读，端口 / 进程号对得上
	info, err := mcp.ReadLocalProxyInfo(cfg.DataDir)
	if err != nil {
		t.Fatalf("读接入凭证失败：%v", err)
	}
	if info.Port != cfg.Port || info.PID != os.Getpid() || len(info.Key) != 64 {
		t.Fatalf("接入凭证内容不对：port=%d pid=%d keylen=%d", info.Port, info.PID, len(info.Key))
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(cfg.DataDir, mcp.LocalProxyInfoFile))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("接入凭证文件权限应为 0600：%v %v", err, st)
		}
	}

	// 控制台公开状态接口报 stdio；转发入口在真实路由上可用（带钥匙）也只对回环开放
	code, body := httpGetStatus(t, baseURL(cfg)+"/api/v1/auth/status")
	if code != 200 || !strings.Contains(body, `"run_mode":"stdio"`) {
		t.Fatalf("auth/status 应报 run_mode=stdio：%d %s", code, body)
	}
	if code, _ := httpGetStatus(t, baseURL(cfg)+mcp.LocalProxyPath); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 转发入口应是 405（钥匙在文件里、这里没带，但方法先于钥匙检查），实际 %d", code)
	}

	// 端口已被占用：第二个进程得到 errPortBusy，而不是把第一个的东西弄坏
	if _, err := startStdioHub(cfg); !errors.Is(err, errPortBusy) {
		t.Fatalf("端口被占用时应返回 errPortBusy，实际 %v", err)
	}
	if _, err := mcp.ReadLocalProxyInfo(cfg.DataDir); err != nil {
		t.Fatalf("第二个进程不该动第一个的接入凭证：%v", err)
	}

	// 主进程收尾：凭证撤掉、端口放开，别人可以接管
	hubA.Close()
	if _, err := mcp.ReadLocalProxyInfo(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatalf("收尾后接入凭证应被删除，实际 %v", err)
	}
	hubB, err := startStdioHub(cfg)
	if err != nil {
		t.Fatalf("端口放开后应能再次启动：%v", err)
	}
	hubB.Close()
}

func TestStdioFollowerForwardsThenPromotesWhenHubIsGone(t *testing.T) {
	cfg := stdioTestConfig(t)
	hubA := mustStartHub(t, cfg)
	f := newTestFollower(cfg, "window-b")
	t.Cleanup(f.close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.attachToHub(ctx); err != nil {
		t.Fatalf("接入主进程失败：%v", err)
	}
	if f.localHandler() != nil {
		t.Fatalf("接入主进程后不应已经是主进程")
	}

	resp, err := f.dispatch(ctx, toolsListMsg(1))
	assertToolsList(t, resp, err, 1)

	// 主进程所在窗口关闭：下一次调用发现没人在听，自己抢到端口接管，并在本进程内处理这次调用
	hubA.Close()
	// 真实场景里窗口关闭与下一次调用之间隔着很久，闲置连接早已被传输层作废；这里是「刚关就调用」，
	// 机器忙时传输层还没来得及看到对端关闭，请求会写进半关的连接，得到 ErrHubLost（结果未知、按设计不自动重发，
	// 那条路径由 TestDescribeForwardError 与 internal/mcp 的转发用例覆盖）。先把闲置连接丢掉，让这条用例只验证「没人在听 → 接管」。
	// 压力下原写法 35/100 次失败（4 核 + 8 个忙循环），这样写 100/100 通过。
	f.fw.CloseIdleConnections()
	resp, err = f.dispatch(ctx, toolsListMsg(2))
	assertToolsList(t, resp, err, 2)
	if f.localHandler() == nil {
		t.Fatalf("主进程消失后，转发进程应已接管成主进程")
	}
	if code, body := httpGetStatus(t, baseURL(cfg)+"/api/v1/auth/status"); code != 200 || !strings.Contains(body, `"run_mode":"stdio"`) {
		t.Fatalf("接管后控制台应恢复：%d %s", code, body)
	}
	if _, err := mcp.ReadLocalProxyInfo(cfg.DataDir); err != nil {
		t.Fatalf("接管后应写出新的接入凭证：%v", err)
	}
}

// 主进程退出后，没有新请求也要自己发现并接管（网页控制台不能一直黑着）。
func TestStdioFollowerWatchPromotesWithoutNewRequests(t *testing.T) {
	cfg := stdioTestConfig(t)
	hubA := mustStartHub(t, cfg)
	f := newTestFollower(cfg, "")
	t.Cleanup(f.close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.attachToHub(ctx); err != nil {
		t.Fatal(err)
	}
	go f.watch(ctx)

	hubA.Close()
	deadline := time.Now().Add(15 * time.Second)
	for f.localHandler() == nil {
		if time.Now().After(deadline) {
			t.Fatalf("主进程退出后 15 秒内转发进程仍没有接管")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code, _ := httpGetStatus(t, baseURL(cfg)+"/api/v1/auth/status"); code != 200 {
		t.Fatalf("接管后控制台应恢复，实际 %d", code)
	}
}

// 三个转发进程同时发现主进程没了：端口绑定仲裁，恰好一个成为新主进程，其余的接到它身上继续工作。
func TestStdioFollowersRaceToPromoteExactlyOneWins(t *testing.T) {
	cfg := stdioTestConfig(t)
	hubA := mustStartHub(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	followers := make([]*stdioFollower, 3)
	for i := range followers {
		followers[i] = newTestFollower(cfg, fmt.Sprintf("window-%d", i))
		t.Cleanup(followers[i].close)
		if err := followers[i].attachToHub(ctx); err != nil {
			t.Fatalf("转发进程 %d 接入失败：%v", i, err)
		}
		// 每个进程都先用过一次，缓存了旧钥匙（换主进程后必须自己重读）
		resp, err := followers[i].dispatch(ctx, toolsListMsg(10+i))
		assertToolsList(t, resp, err, 10+i)
	}

	hubA.Close()

	var wg sync.WaitGroup
	var winners int32
	start := make(chan struct{})
	for _, f := range followers {
		wg.Add(1)
		go func(f *stdioFollower) {
			defer wg.Done()
			<-start
			if f.tryPromote() {
				atomic.AddInt32(&winners, 1)
			}
		}(f)
	}
	close(start)
	wg.Wait()
	if winners != 1 {
		t.Fatalf("三个进程同时接管，应恰好一个成功，实际 %d", winners)
	}

	leaders := 0
	for i, f := range followers {
		resp, err := f.dispatch(ctx, toolsListMsg(20+i))
		assertToolsList(t, resp, err, 20+i)
		if f.localHandler() != nil {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("接管后应只有一个主进程，实际 %d", leaders)
	}
}

// assertNoStdioLeftovers：数据目录里没有接入凭证文件，端口也是空的。
func assertNoStdioLeftovers(t *testing.T, cfg *config.Config) {
	t.Helper()
	if info, err := mcp.ReadLocalProxyInfo(cfg.DataDir); err == nil {
		t.Fatalf("不该留下接入凭证文件：port=%d pid=%d", info.Port, info.PID)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		t.Fatalf("端口应是空的（没有人在听）：%v", err)
	}
	_ = ln.Close()
}

// 收尾之后不再接管（#248）：close 之后再调 tryPromote（退出途中还在跑的监视协程 / 通知派发协程会这么做）必须被拒绝，
// 不能在空闲的端口上再拉起一个没人关的 Hub。
func TestFollowerNeverPromotesAfterClose(t *testing.T) {
	cfg := stdioTestConfig(t)
	f := newTestFollower(cfg, "window-closed")
	f.close()
	if f.tryPromote() {
		t.Fatalf("close 之后不该再接管")
	}
	if f.localHandler() != nil {
		t.Fatalf("close 之后不该有本地处理器")
	}
	assertNoStdioLeftovers(t, cfg)
}

// 已经接管成主进程的转发进程在收尾（窗口关闭）期间，监视协程不能再次接管（#248，stdio-multi M7b 偶发红的根因）。
//
// 现场：收尾时 Hub 先关掉监听口、再等进行中的请求最多 3 秒（网页控制台的 SSE 长连接常年占着，所以这 3 秒是常态）；
// 这段时间里端口是空的、f.local 也已经清掉。监视协程升级之后还要再等一个 tick 才会发现「已经接管」而退出——
// 窗口恰好在这一个 tick 之内关闭时，它会在这个空窗里以为「主进程没了」，再拉起一个新 Hub；进程带着它退出，
// 数据目录里就留下 stdio-hub.json。这里用 80 毫秒的 tick 与一条一直占着的连接把这个空窗摆出来。
func TestPromotedFollowerDoesNotRepromoteWhileClosing(t *testing.T) {
	old := watchInterval
	watchInterval = 80 * time.Millisecond
	t.Cleanup(func() { watchInterval = old })

	cfg := stdioTestConfig(t)
	f := newTestFollower(cfg, "window-closing")
	t.Cleanup(f.close)
	if !f.tryPromote() {
		t.Fatalf("空端口上应能接管")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.watch(ctx) // 第一个 tick 在 80 毫秒后：下面的 close 要赶在它之前开始

	// 占住一条连接，让 Hub 关闭时的优雅等待拖满 3 秒（http.Server.Shutdown 不会把刚建立的连接当成空闲连接收掉）
	conn, err := net.Dial("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(30 * time.Millisecond) // 让服务端把这条连接登记下来

	f.close() // 返回时 Hub 已关完；这 3 秒里监视协程的 tick 至少触发一次
	assertNoStdioLeftovers(t, cfg)
}

// fakePortOwner 在 cfg.Port 上起一个只回答 /api/v1/auth/status 的假服务，模拟「占着端口的不是 stdio 主进程」。
func fakePortOwner(t *testing.T, cfg *config.Config, handler http.HandlerFunc) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func TestAttachToHubExplainsWhoOwnsThePort(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"常驻服务（serve 模式）", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"run_mode":"serve","version":"1.6.3","project_id":"p"}`))
		}, "serve 模式"},
		{"旧版 RelayMesh（没有 run_mode）", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"version":"1.6.0","project_id":"p"}`))
		}, "旧版"},
		{"别的程序（非 JSON）", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) }, "另一个程序"},
		{"别的程序（404）", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "另一个程序"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := stdioTestConfig(t)
			fakePortOwner(t, cfg, c.handler)
			f := newTestFollower(cfg, "")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			start := time.Now()
			err := f.attachToHub(ctx)
			if err == nil {
				t.Fatalf("端口被不是 stdio 主进程的服务占用时应报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("报错应说明占用者（含 %q），实际：%v", c.want, err)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatalf("这类错误应当立刻失败，实际等了 %v", time.Since(start))
			}
			if !strings.Contains(err.Error(), strconv.Itoa(cfg.Port)) {
				t.Fatalf("报错应带上端口号：%v", err)
			}
		})
	}
}

func TestAttachToHubExplainsMissingKey(t *testing.T) {
	cfg := stdioTestConfig(t)
	// 对面自称 stdio 主进程，但本进程的数据目录里没有它的接入凭证（用了另一份数据目录）
	fakePortOwner(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"run_mode":"stdio","version":"` + version + `","project_id":"p"}`))
	})
	f := newTestFollower(cfg, "")
	err := f.attachToHub(context.Background())
	if err == nil || !strings.Contains(err.Error(), "数据目录") {
		t.Fatalf("读不到接入凭证时应提示数据目录，实际：%v", err)
	}
}

func TestPortBusyDetection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_, listenErr := net.Listen("tcp", addr)
	if listenErr == nil {
		t.Fatal("同一个端口不应能绑定两次")
	}
	if !portBusy(listenErr, addr) {
		t.Fatalf("端口被占用应被识别：%v", listenErr)
	}
	_ = ln.Close()
	// 没人占的端口上的别的错误（这里随便造一个）不算「端口被占用」
	if portBusy(errors.New("permission denied"), addr) {
		t.Fatalf("端口没人占用时不应判定为被占用")
	}
}

func TestDescribeForwardError(t *testing.T) {
	cases := map[error]string{
		mcp.ErrHubLost:        "重新发起",
		mcp.ErrHubUnreachable: "连不上",
		mcp.ErrNotStdioHub:    "不是 RelayMesh 的 stdio 主进程",
		mcp.ErrKeyRejected:    "数据目录",
		mcp.ErrNoKey:          "数据目录",
	}
	for err, want := range cases {
		wrapped := fmt.Errorf("%w：细节", err)
		if got := describeForwardError(wrapped, 18775); !strings.Contains(got, want) {
			t.Fatalf("%v 的说明应包含 %q，实际 %q", err, want, got)
		}
	}
}
