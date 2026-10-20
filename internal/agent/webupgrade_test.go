package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「Web 一键升级」的 Agent 一侧。要钉住的几件事：
//   - 没有主机所有者的本地开关，什么都不会发生；开关每次现读，下载期间被收回也算数；
//   - 下载地址只由 Agent 自己的服务端地址与平台推出，不跟随重定向；
//   - SHA-256 不一致、新程序跑不起来、版本不对，都不能交接，也不留下文件；
//   - 任何失败都如实报给 Hub，且不动已安装的程序（Agent 这一侧根本碰不到它）。

const (
	upgradeTestTicket  = "0123456789abcdef0123456789abcdef"
	upgradeTestID      = "00112233445566778899aabbccddeeff"
	upgradeTestVersion = "9.9.9"
	upgradeTestAsset   = "relaymesh-agent-linux-amd64"
)

type handoffCall struct {
	file    string
	version string
	data    []byte      // 交接那一刻暂存文件的内容
	mode    os.FileMode // 以及它的权限
}

// upgradeHarness 是一台假的服务端（同时提供升级文件下载与 Agent 的 WebSocket 入口）加一个配好的 upgrader。
type upgradeHarness struct {
	t       *testing.T
	srv     *httptest.Server
	target  ServerTarget
	payload []byte
	sum     string
	stage   string
	cfg     *SelfUpgrade
	u       *upgrader

	allowed   atomic.Bool
	hits      atomic.Int32 // 升级文件的请求次数
	elsewhere atomic.Int32 // 重定向目标被访问的次数
	mode      atomic.Value // string："" / chunked / redirect
	clock     atomic.Int64 // UnixNano：给 upgrader 的「现在」，测试可以拨快

	mu         sync.Mutex
	handoffs   []handoffCall
	handoffErr error
	script     func(ctx context.Context, ws *websocket.Conn)
}

func newUpgradeHarness(t *testing.T, payload []byte, tweak func(*SelfUpgrade)) *upgradeHarness {
	t.Helper()
	sum := sha256.Sum256(payload)
	h := &upgradeHarness{t: t, payload: payload, sum: hex.EncodeToString(sum[:])}
	h.mode.Store("")
	h.clock.Store(time.Now().UnixNano())
	h.allowed.Store(true)
	h.stage = filepath.Join(t.TempDir(), "upgrade")

	mux := http.NewServeMux()
	mux.HandleFunc("/i/", h.serveAsset)
	mux.HandleFunc("/somewhere-else", func(http.ResponseWriter, *http.Request) { h.elsewhere.Add(1) })
	mux.HandleFunc(AgentWSPath, func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:    []string{termproto.SubprotocolAgent},
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		h.mu.Lock()
		script := h.script
		h.mu.Unlock()
		if script != nil {
			script(r.Context(), ws)
		}
	})
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)

	target, err := ParseServer(h.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	h.target = target

	h.cfg = &SelfUpgrade{
		Allowed:  h.allowed.Load,
		StageDir: h.stage,
		Handoff: func(_ context.Context, bin, ver string) error {
			call := handoffCall{file: bin, version: ver}
			if st, err := os.Stat(bin); err == nil {
				call.mode = st.Mode()
			}
			call.data, _ = os.ReadFile(bin)
			h.mu.Lock()
			defer h.mu.Unlock()
			h.handoffs = append(h.handoffs, call)
			return h.handoffErr
		},
		Platform: func() (string, string) { return "linux", "amd64" },
		Verify:   func(context.Context, string) (string, error) { return upgradeTestVersion, nil },
		Now:      func() time.Time { return time.Unix(0, h.clock.Load()) },
	}
	if tweak != nil {
		tweak(h.cfg)
	}
	h.u = newUpgrader(h.cfg, "1.0.0", target, TrustOptions{Insecure: true}, t.Logf)
	return h
}

func (h *upgradeHarness) serveAsset(w http.ResponseWriter, r *http.Request) {
	h.hits.Add(1)
	if r.URL.Path != "/i/"+upgradeTestTicket+"/"+upgradeTestAsset {
		http.NotFound(w, r)
		return
	}
	switch h.mode.Load().(string) {
	case "redirect":
		http.Redirect(w, r, "/somewhere-else", http.StatusFound)
	case "chunked":
		// 先刷出一半：没有 Content-Length，只能靠边读边数来守住大小上限。
		half := len(h.payload) / 2
		_, _ = w.Write(h.payload[:half])
		w.(http.Flusher).Flush()
		_, _ = w.Write(h.payload[half:])
	default:
		_, _ = w.Write(h.payload)
	}
}

func (h *upgradeHarness) frame() termproto.Upgrade {
	return termproto.Upgrade{T: termproto.TUpgrade, ID: upgradeTestID, Version: upgradeTestVersion, Ticket: upgradeTestTicket, SHA256: h.sum}
}

func (h *upgradeHarness) run(m termproto.Upgrade) []termproto.UpgradeStatus {
	h.t.Helper()
	var got []termproto.UpgradeStatus
	h.u.run(context.Background(), m, func(s termproto.UpgradeStatus) { got = append(got, s) })
	return got
}

func (h *upgradeHarness) calls() []handoffCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]handoffCall(nil), h.handoffs...)
}

// waitCalls 等交接被记下来再读。升级流程是「先发 handoff 状态、再调 Handoff」（先告知 Hub 再交接，
// 交接成功后 Agent 很快就会被重启），测试这一侧读到状态帧时 Handoff 可能还没执行：
// 直接数 calls() 会偶发读到空（约 6% 的运行），所以经由状态帧 / 连接观察的测试要用它。
func (h *upgradeHarness) waitCalls(n int) []handoffCall {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := h.calls()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *upgradeHarness) setHandoffErr(err error) {
	h.mu.Lock()
	h.handoffErr = err
	h.mu.Unlock()
}

func (h *upgradeHarness) setScript(f func(ctx context.Context, ws *websocket.Conn)) {
	h.mu.Lock()
	h.script = f
	h.mu.Unlock()
}

// stageFiles 列出暂存目录里的文件名（目录不存在算空）。
func (h *upgradeHarness) stageFiles() []string {
	h.t.Helper()
	entries, err := os.ReadDir(h.stage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// wantStages 断言收到的状态序列（形如 "downloading/"、"failed/checksum"），并检查每条都原样带回了请求的 id。
func wantStages(t *testing.T, got []termproto.UpgradeStatus, want ...string) {
	t.Helper()
	var have []string
	for _, s := range got {
		if s.T != termproto.TUpgradeStatus || s.ID != upgradeTestID {
			t.Errorf("状态帧的类型与 id 应原样带回: %+v", s)
		}
		have = append(have, s.Stage+"/"+s.Code)
	}
	if strings.Join(have, " ") != strings.Join(want, " ") {
		t.Fatalf("状态序列 = %v，期望 %v", have, want)
	}
}

func TestUpgradeHappyPathHandsOffTheVerifiedBinary(t *testing.T) {
	payload := []byte("#!/bin/sh\necho pretend this is the new agent\n")
	h := newUpgradeHarness(t, payload, nil)

	wantStages(t, h.run(h.frame()), "downloading/", "handoff/")

	calls := h.calls()
	if len(calls) != 1 {
		t.Fatalf("应交接一次: %+v", calls)
	}
	c := calls[0]
	if c.version != upgradeTestVersion || c.file != filepath.Join(h.stage, stagedBinaryName) {
		t.Fatalf("交接的版本或路径不对: %+v", c)
	}
	if string(c.data) != string(payload) {
		t.Fatalf("交接时暂存文件的内容应与下载的完全一致: %q", c.data)
	}
	if runtime.GOOS != "windows" && c.mode.Perm() != 0o700 {
		t.Fatalf("暂存的新程序应只有所有者可执行，实际 %v", c.mode.Perm())
	}
	if names := h.stageFiles(); len(names) != 1 || names[0] != stagedBinaryName {
		t.Fatalf("暂存目录里只应留下新程序本身，不应有下载临时文件: %v", names)
	}
	if h.hits.Load() != 1 {
		t.Fatalf("只应下载一次: %d", h.hits.Load())
	}

	// 交接之后升级进程正在替换程序、重启服务：这段时间里再来的请求一律挡掉。
	wantStages(t, h.run(h.frame()), "refused/busy")
	if len(h.calls()) != 1 {
		t.Fatal("被挡掉的请求不应再交接")
	}
}

func TestUpgradeRefusalsNeverTouchTheNetworkOrDisk(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *upgradeHarness)
		frame func(m *termproto.Upgrade)
		tweak func(c *SelfUpgrade)
		want  string
	}{
		{name: "票据不合规（路径穿越）", frame: func(m *termproto.Upgrade) { m.Ticket = "../../etc/passwd" }, want: "refused/bad_request"},
		{name: "版本号里夹带路径", frame: func(m *termproto.Upgrade) { m.Version = "9.9/../x" }, want: "refused/bad_request"},
		{name: "校验和格式不对", frame: func(m *termproto.Upgrade) { m.SHA256 = "abc" }, want: "refused/bad_request"},
		{name: "id 缺失", frame: func(m *termproto.Upgrade) { m.ID = "" }, want: "refused/bad_request"},
		{name: "主机所有者没有允许", setup: func(h *upgradeHarness) { h.allowed.Store(false) }, want: "refused/not_allowed"},
		{name: "目标版本与当前相同", frame: func(m *termproto.Upgrade) { m.Version = "1.0.0" }, want: "refused/up_to_date"},
		{name: "目标版本比当前旧（Hub 回退过）", frame: func(m *termproto.Upgrade) { m.Version = "0.9.0" }, want: "refused/up_to_date"},
		{name: "不是常驻服务（没有交接能力）", tweak: func(c *SelfUpgrade) { c.Handoff = nil }, want: "refused/not_supported"},
		{name: "没有暂存目录", tweak: func(c *SelfUpgrade) { c.StageDir = "" }, want: "refused/not_supported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpgradeHarness(t, []byte("payload"), tc.tweak)
			if tc.setup != nil {
				tc.setup(h)
			}
			m := h.frame()
			if tc.frame != nil {
				tc.frame(&m)
			}
			got := h.run(m)
			if len(got) != 1 || got[0].Stage+"/"+got[0].Code != tc.want {
				t.Fatalf("应只回一条 %s: %+v", tc.want, got)
			}
			if tc.want == "refused/bad_request" && got[0].ID != m.ID {
				t.Fatalf("即使请求不合规也应带回它的 id（让 Hub 知道回的是哪次）: %+v", got[0])
			}
			if got[0].Detail == "" {
				t.Errorf("拒绝要给出人能看懂的原因: %+v", got[0])
			}
			if h.hits.Load() != 0 || len(h.calls()) != 0 {
				t.Fatalf("被拒绝的请求不应下载也不应交接: hits=%d calls=%v", h.hits.Load(), h.calls())
			}
			if names := h.stageFiles(); len(names) != 0 {
				t.Fatalf("被拒绝的请求不应在磁盘上留下任何东西: %v", names)
			}
		})
	}
}

func TestUpgradeUnsupportedPlatformHasNoAsset(t *testing.T) {
	h := newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.Platform = func() (string, string) { return "freebsd", "amd64" }
	})
	wantStages(t, h.run(h.frame()), "downloading/", "failed/not_supported")
	if h.hits.Load() != 0 {
		t.Fatal("没有对应构建的平台不应去下载")
	}
}

func TestUpgradeChecksumMismatchDiscardsTheDownload(t *testing.T) {
	h := newUpgradeHarness(t, []byte("the real payload"), nil)
	m := h.frame()
	m.SHA256 = strings.Repeat("0", 64)
	wantStages(t, h.run(m), "downloading/", "failed/checksum")
	if len(h.calls()) != 0 {
		t.Fatal("校验和不一致不能交接")
	}
	if names := h.stageFiles(); len(names) != 0 {
		t.Fatalf("校验失败后不应留下下载的文件: %v", names)
	}
}

func TestUpgradeDownloadFailures(t *testing.T) {
	payload := []byte("0123456789abcdefghij")
	cases := []struct {
		name       string
		payload    []byte
		mode       string
		frame      func(m *termproto.Upgrade)
		tweak      func(c *SelfUpgrade)
		closed     bool
		wantDetail string
	}{
		{name: "票据不对或窗口已关（404）", payload: payload, frame: func(m *termproto.Upgrade) { m.Ticket = strings.Repeat("a", 32) }, wantDetail: "404"},
		{name: "重定向不跟随", payload: payload, mode: "redirect", wantDetail: "302"},
		{name: "声明的大小就超过上限", payload: payload, tweak: func(c *SelfUpgrade) { c.MaxBytes = 5 }, wantDetail: "超过上限"},
		{name: "chunked 传输也守住上限", payload: payload, mode: "chunked", tweak: func(c *SelfUpgrade) { c.MaxBytes = int64(len(payload)) - 1 }, wantDetail: "超过上限"},
		{name: "空文件", payload: nil, wantDetail: "空的"},
		{name: "服务端不可达", payload: payload, closed: true, wantDetail: "连接服务端失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpgradeHarness(t, tc.payload, tc.tweak)
			h.mode.Store(tc.mode)
			if tc.closed {
				h.srv.Close()
			}
			m := h.frame()
			if tc.frame != nil {
				tc.frame(&m)
			}
			got := h.run(m)
			wantStages(t, got, "downloading/", "failed/download")
			if !strings.Contains(got[1].Detail, tc.wantDetail) {
				t.Fatalf("原因应包含 %q: %q", tc.wantDetail, got[1].Detail)
			}
			if len(h.calls()) != 0 {
				t.Fatal("下载失败不能交接")
			}
			if names := h.stageFiles(); len(names) != 0 {
				t.Fatalf("下载失败后不应留下文件: %v", names)
			}
			if h.elsewhere.Load() != 0 {
				t.Fatal("不应跟随服务端的重定向：下载只能发生在自己配置的服务端")
			}
		})
	}
}

func TestUpgradeVerifyFailuresRemoveTheStagedFile(t *testing.T) {
	cases := []struct {
		name   string
		verify func(context.Context, string) (string, error)
	}{
		{"新程序报出的版本不是期望的版本", func(context.Context, string) (string, error) { return "1.2.3", nil }},
		{"新程序在这台机器上跑不起来", func(context.Context, string) (string, error) { return "", errors.New("exec format error") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) { c.Verify = tc.verify })
			got := h.run(h.frame())
			wantStages(t, got, "downloading/", "failed/verify")
			if len(h.calls()) != 0 {
				t.Fatal("校验不通过不能交接")
			}
			if names := h.stageFiles(); len(names) != 0 {
				t.Fatalf("校验不通过不应留下暂存的程序: %v", names)
			}
		})
	}
}

func TestUpgradeHonoursOwnerRevokingDuringDownload(t *testing.T) {
	var h *upgradeHarness
	h = newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.Verify = func(context.Context, string) (string, error) {
			h.allowed.Store(false) // 下载与校验期间，所有者执行了 `web-upgrade off`
			return upgradeTestVersion, nil
		}
	})
	wantStages(t, h.run(h.frame()), "downloading/", "refused/not_allowed")
	if len(h.calls()) != 0 {
		t.Fatal("交接前所有者收回了授权，就不能交接")
	}
	if names := h.stageFiles(); len(names) != 0 {
		t.Fatalf("放弃后不应留下暂存的程序: %v", names)
	}
}

func TestUpgradeHandoffFailureCleansUpAndAllowsRetry(t *testing.T) {
	h := newUpgradeHarness(t, []byte("payload"), nil)
	h.setHandoffErr(errors.New("systemd-run: command not found"))

	got := h.run(h.frame())
	wantStages(t, got, "downloading/", "handoff/", "failed/handoff")
	if !strings.Contains(got[2].Detail, "systemd-run") {
		t.Fatalf("应带上交接失败的原因: %q", got[2].Detail)
	}
	if names := h.stageFiles(); len(names) != 0 {
		t.Fatalf("交接失败后不应留下暂存的程序: %v", names)
	}

	// 什么都没发生，就不该占着「已有升级在进行」的名额：修好之后可以马上重试。
	h.setHandoffErr(nil)
	wantStages(t, h.run(h.frame()), "downloading/", "handoff/")
}

func TestUpgradeBusyRefusalDoesNotReleaseTheRunningSlot(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h := newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.Verify = func(context.Context, string) (string, error) {
			once.Do(func() { close(entered) })
			<-release
			return upgradeTestVersion, nil
		}
	})

	done := make(chan []termproto.UpgradeStatus, 1)
	go func() { done <- h.run(h.frame()) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("第一次升级没有走到校验")
	}

	// 第一次还在进行：第二、第三次都应被挡掉——第二次的拒绝不能把第一次占着的名额清掉，否则第三次会混进来。
	wantStages(t, h.run(h.frame()), "refused/busy")
	wantStages(t, h.run(h.frame()), "refused/busy")
	if h.hits.Load() != 1 {
		t.Fatalf("被挡掉的请求不应再下载: %d", h.hits.Load())
	}

	close(release)
	select {
	case got := <-done:
		wantStages(t, got, "downloading/", "handoff/")
	case <-time.After(10 * time.Second):
		t.Fatal("第一次升级没有结束")
	}
	if len(h.calls()) != 1 {
		t.Fatalf("整个过程只应交接一次: %v", h.calls())
	}
}

func TestUpgradeHandoffHoldExpires(t *testing.T) {
	h := newUpgradeHarness(t, []byte("payload"), nil)
	wantStages(t, h.run(h.frame()), "downloading/", "handoff/")
	wantStages(t, h.run(h.frame()), "refused/busy")

	// 交接后过了很久 Agent 还活着，说明升级没有生效（升级进程已回滚或失败）：应当允许重新来过。
	h.clock.Add(int64(upgradeHandoffHold + time.Second))
	wantStages(t, h.run(h.frame()), "downloading/", "handoff/")
	if len(h.calls()) != 2 {
		t.Fatalf("保持期过后应能再次交接: %v", h.calls())
	}
}

// runFor 与 run 一样，但交接之后盯着升级进程结果的那一段最多只等 d（真实环境里它一直等到 Agent 被重启）。
func (h *upgradeHarness) runFor(m termproto.Upgrade, d time.Duration) []termproto.UpgradeStatus {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var got []termproto.UpgradeStatus
	h.u.run(ctx, m, func(s termproto.UpgradeStatus) { got = append(got, s) })
	return got
}

func fastOutcomePoll(t *testing.T) {
	t.Helper()
	old := upgradeOutcomePoll
	upgradeOutcomePoll = 5 * time.Millisecond
	t.Cleanup(func() { upgradeOutcomePoll = old })
}

func TestUpgradeReportsAnApplyFailureAfterHandoffAndAllowsRetry(t *testing.T) {
	fastOutcomePoll(t)
	var note atomic.Pointer[termproto.UpgradeNote]
	var applyFails atomic.Bool
	applyFails.Store(true)
	var h *upgradeHarness
	h = newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.Note = func() *termproto.UpgradeNote { return note.Load() }
		inner := c.Handoff
		c.Handoff = func(ctx context.Context, bin, ver string) error {
			err := inner(ctx, bin, ver)
			if applyFails.Load() {
				// 升级进程启动后没能完成替换（Agent 因此一直活着），留下了一份失败结果。
				note.Store(&termproto.UpgradeNote{
					Outcome: termproto.UpgradeOutcomeFailed, From: "1.0.0", To: ver,
					At: h.clock.Load()/int64(time.Second) + 1, Detail: "替换程序失败: no space left on device",
				})
			}
			return err
		}
	})

	got := h.runFor(h.frame(), 10*time.Second)
	wantStages(t, got, "downloading/", "handoff/", "failed/apply")
	if !strings.Contains(got[2].Detail, "no space left on device") {
		t.Fatalf("应把升级进程留下的原因带给 Hub，而不是让管理员干等到超时: %q", got[2].Detail)
	}

	// 升级进程已经明确失败：不必再等「交接后的保持期」，管理员可以马上重试（这次升级进程没有失败）。
	note.Store(nil)
	applyFails.Store(false)
	wantStages(t, h.runFor(h.frame(), 100*time.Millisecond), "downloading/", "handoff/")
	if len(h.calls()) != 2 {
		t.Fatalf("重试应能再次交接: %v", h.calls())
	}
}

func TestUpgradeOutcomeWatcherIgnoresStaleNotesAndSuccess(t *testing.T) {
	fastOutcomePoll(t)

	t.Run("上一次升级留下的旧结果不算", func(t *testing.T) {
		var h *upgradeHarness
		h = newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
			c.Note = func() *termproto.UpgradeNote {
				return &termproto.UpgradeNote{Outcome: termproto.UpgradeOutcomeFailed, At: h.clock.Load()/int64(time.Second) - 3600, Detail: "上次的失败"}
			}
		})
		wantStages(t, h.runFor(h.frame(), 150*time.Millisecond), "downloading/", "handoff/")
	})

	t.Run("成功的结果不需要报（Agent 马上就会被重启）", func(t *testing.T) {
		var h *upgradeHarness
		h = newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
			c.Note = func() *termproto.UpgradeNote {
				return &termproto.UpgradeNote{Outcome: termproto.UpgradeOutcomeOK, At: h.clock.Load()/int64(time.Second) + 1}
			}
		})
		start := time.Now()
		wantStages(t, h.runFor(h.frame(), 10*time.Second), "downloading/", "handoff/")
		if time.Since(start) > 5*time.Second {
			t.Fatal("看到成功结果就应结束，不必等到超时")
		}
	})

	t.Run("没有结果文件能力（Note 为 nil）时不盯", func(t *testing.T) {
		h := newUpgradeHarness(t, []byte("payload"), nil)
		start := time.Now()
		wantStages(t, h.runFor(h.frame(), 10*time.Second), "downloading/", "handoff/")
		if time.Since(start) > 5*time.Second {
			t.Fatal("没有 Note 时交接后应立即返回")
		}
	})
}

func TestSelfUpgradeStateAndAssetName(t *testing.T) {
	var nilCfg *SelfUpgrade
	if nilCfg.state() != "" {
		t.Fatal("没有升级能力就不声明")
	}
	if got := (&SelfUpgrade{}).state(); got != termproto.WebUpgradeOff {
		t.Fatalf("没有开关函数按「不允许」处理: %q", got)
	}
	if got := (&SelfUpgrade{Allowed: func() bool { return true }}).state(); got != termproto.WebUpgradeOn {
		t.Fatalf("允许时声明 on: %q", got)
	}

	cases := []struct {
		goos, goarch string
		want         string
		ok           bool
	}{
		{"linux", "amd64", "relaymesh-agent-linux-amd64", true},
		{"linux", "arm64", "relaymesh-agent-linux-arm64", true},
		{"darwin", "arm64", "relaymesh-agent-darwin-arm64", true},
		{"windows", "amd64", "relaymesh-agent-windows-amd64.exe", true},
		{"linux", "386", "", false},
		{"freebsd", "amd64", "", false},
		{"linux", "../../x", "", false},
	}
	for _, c := range cases {
		cfg := &SelfUpgrade{Platform: func() (string, string) { return c.goos, c.goarch }}
		name, ok := cfg.assetName()
		if ok != c.ok || (ok && name != c.want) {
			t.Errorf("%s/%s: assetName = %q,%v，期望 %q,%v", c.goos, c.goarch, name, ok, c.want, c.ok)
		}
	}
}

func TestParseVersionOutput(t *testing.T) {
	good := map[string]string{
		"RelayMesh Agent 1.2.3\n":                    "1.2.3",
		"RelayMesh Agent 1.6.3-dev-20261008-1649b9e": "1.6.3-dev-20261008-1649b9e",
		"  RelayMesh Agent   v2.0.0  \r\n":           "v2.0.0",
	}
	for in, want := range good {
		if got, err := parseVersionOutput(in); err != nil || got != want {
			t.Errorf("parseVersionOutput(%q) = %q, %v，期望 %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "RelayMesh Agent", "Other Agent 1.0", "RelayMesh Hub 1.0", "RelayMesh Agent 1.0\nsecond line", "Segmentation fault"} {
		if got, err := parseVersionOutput(in); err == nil {
			t.Errorf("parseVersionOutput(%q) 应报错，得到 %q", in, got)
		}
	}
}

func writeFakeAgent(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunVersionExecutesTheCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 shell 脚本冒充新程序，仅在类 Unix 上运行")
	}
	ctx := context.Background()
	if got, err := runVersion(ctx, writeFakeAgent(t, `echo "RelayMesh Agent 1.2.3"`)); err != nil || got != "1.2.3" {
		t.Fatalf("正常的新程序: %q, %v", got, err)
	}
	for name, body := range map[string]string{
		"退出码非零":  `echo "RelayMesh Agent 1.2.3"; exit 3`,
		"输出不认识":  `echo "hello"`,
		"多行输出":   `echo "RelayMesh Agent 1.2.3"; echo extra`,
		"什么也不输出": `true`,
	} {
		if got, err := runVersion(ctx, writeFakeAgent(t, body)); err == nil {
			t.Errorf("%s 应报错，得到 %q", name, got)
		}
	}
	if _, err := runVersion(ctx, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("文件不存在应报错")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := runVersion(cancelled, writeFakeAgent(t, `echo "RelayMesh Agent 1.2.3"`)); err == nil {
		t.Fatal("ctx 已取消应报错")
	}
}

func TestRunVersionDoesNotInheritAgentEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("仅在类 Unix 上运行")
	}
	t.Setenv("RELAYMESH_TEST_SECRET", "do-not-leak")
	p := writeFakeAgent(t, `if [ -n "$RELAYMESH_TEST_SECRET" ]; then echo "leaked"; else echo "RelayMesh Agent 1.0.0"; fi`)
	got, err := runVersion(context.Background(), p)
	if err != nil || got != "1.0.0" {
		t.Fatalf("新程序只需要打印版本号，不应继承 Agent 的环境（令牌之类）: %q, %v", got, err)
	}
}

func TestSweepStaleDownloads(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	write(".download-old", 3*time.Hour)
	write(".download-fresh", time.Minute)
	write(stagedBinaryName, 3*time.Hour) // 不是下载临时文件，不归它清
	write("unrelated", 3*time.Hour)
	if err := os.Mkdir(filepath.Join(dir, ".download-dir"), 0o700); err != nil {
		t.Fatal(err)
	}

	sweepStaleDownloads(dir, now)

	left := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left[e.Name()] = true
	}
	if left[".download-old"] {
		t.Error("崩溃遗留的旧下载临时文件应被清掉")
	}
	for _, keep := range []string{".download-fresh", stagedBinaryName, "unrelated", ".download-dir"} {
		if !left[keep] {
			t.Errorf("%s 不应被清掉", keep)
		}
	}
	sweepStaleDownloads(filepath.Join(dir, "missing"), now) // 目录不存在不崩溃
}

// ---------- relay：帧的入口 ----------

func upgradeFrameBytes(t *testing.T, m termproto.Upgrade) []byte {
	t.Helper()
	b, err := termproto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (g *relayRig) upgradeStatus() termproto.UpgradeStatus {
	g.t.Helper()
	typ, data := g.next()
	if typ != websocket.MessageText {
		g.t.Fatal("期望文本帧")
	}
	var st termproto.UpgradeStatus
	if err := json.Unmarshal(data, &st); err != nil || st.T != termproto.TUpgradeStatus {
		g.t.Fatalf("不是 upgrade_status: %s (%v)", data, err)
	}
	return st
}

func TestRelayRefusesUpgradeWithoutAnUpgrader(t *testing.T) {
	h := newUpgradeHarness(t, []byte("payload"), nil)
	g := newRelayRig(t, nil)
	g.r.handleText(upgradeFrameBytes(t, h.frame()))
	st := g.upgradeStatus()
	if st.ID != upgradeTestID || st.Stage != termproto.UpgradeStageRefused || st.Code != termproto.UpgradeCodeNotSupported {
		t.Fatalf("没有升级能力的 Agent 应明确拒绝，而不是沉默: %+v", st)
	}
	if h.hits.Load() != 0 {
		t.Fatal("拒绝时不应下载")
	}
}

func TestRelayRunsUpgradeAndReportsProgress(t *testing.T) {
	h := newUpgradeHarness(t, []byte("payload"), nil)
	g := newRelayRig(t, nil)
	g.r.upgrader = h.u

	g.r.handleText(upgradeFrameBytes(t, h.frame()))
	if st := g.upgradeStatus(); st.Stage != termproto.UpgradeStageDownloading || st.ID != upgradeTestID {
		t.Fatalf("第一条应是 downloading: %+v", st)
	}
	if st := g.upgradeStatus(); st.Stage != termproto.UpgradeStageHandoff {
		t.Fatalf("第二条应是 handoff: %+v", st)
	}
	if calls := h.waitCalls(1); len(calls) != 1 {
		t.Fatalf("应交接一次: %v", calls)
	}
}

func TestRelayIgnoresUpgradeWhilePending(t *testing.T) {
	// 还没被管理员启用的连接不该能触发本机的升级。
	h := newUpgradeHarness(t, []byte("payload"), nil)
	g := newRelayRig(t, nil)
	g.r.enabled.Store(false)
	g.r.upgrader = h.u
	g.r.handleText(upgradeFrameBytes(t, h.frame()))
	g.silent()
	if h.hits.Load() != 0 || len(h.calls()) != 0 {
		t.Fatal("待启用的连接不应触发升级")
	}
}

// ---------- 握手与重连 ----------

// helloCapturingHub 让 Hub 读到 Agent 的 hello 后回 welcome(enabled)，并把 hello 交给 onHello；之后一直读到连接结束。
func (h *upgradeHarness) helloCapturingHub(t *testing.T, onHello func(termproto.Hello), after func(ctx context.Context, ws *websocket.Conn)) {
	t.Helper()
	h.setScript(func(ctx context.Context, ws *websocket.Conn) {
		ch, _ := challenge(t, termproto.ProtocolVersion)
		sendJSON(t, ctx, ws, ch)
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var hello termproto.Hello
		if json.Unmarshal(data, &hello) != nil {
			return
		}
		onHello(hello)
		pub, _ := termproto.DecodeKey(hello.Pub, 32)
		sendJSON(t, ctx, ws, termproto.Welcome{
			T: termproto.TWelcome, Status: termproto.WelcomeEnabled, MachineCode: termproto.MachineCode(pub),
			Policy: termproto.Policy{Terminal: true},
		})
		if after != nil {
			after(ctx, ws)
			return
		}
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	})
}

func TestHelloAdvertisesWebUpgrade(t *testing.T) {
	allow := func(v bool) func() bool { return func() bool { return v } }
	cases := []struct {
		name      string
		upg       *SelfUpgrade
		temporary bool
		want      string
	}{
		{"没有提供升级能力（前台运行 / 不支持的系统）", nil, false, ""},
		{"常驻身份，所有者没有允许", &SelfUpgrade{Allowed: allow(false)}, false, termproto.WebUpgradeOff},
		{"常驻身份，所有者已允许", &SelfUpgrade{Allowed: allow(true)}, false, termproto.WebUpgradeOn},
		{"临时身份即使提供了也不声明（没有安装过任何东西）", &SelfUpgrade{Allowed: allow(true)}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu  sync.Mutex
				got termproto.Hello
			)
			target := stubHub(t, func(ctx context.Context, ws *websocket.Conn, _ *http.Request) {
				ch, _ := challenge(t, termproto.ProtocolVersion)
				sendJSON(t, ctx, ws, ch)
				_, data, err := ws.Read(ctx)
				if err != nil {
					return
				}
				var hello termproto.Hello
				if json.Unmarshal(data, &hello) != nil {
					return
				}
				mu.Lock()
				got = hello
				mu.Unlock()
				pub, _ := termproto.DecodeKey(hello.Pub, 32)
				sendJSON(t, ctx, ws, termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomePending, MachineCode: termproto.MachineCode(pub)})
				_ = ws.Close(websocket.StatusCode(termproto.CloseUnknownMachine), "bye")
			})
			a := newAgentFor(t, target, func(o *Options) {
				o.SelfUpgrade = tc.upg
				if !tc.temporary {
					o.Identity = persistentIdentity(t)
				}
			})
			connectWith(t, a)
			mu.Lock()
			defer mu.Unlock()
			if got.Meta.WebUpgrade != tc.want {
				t.Fatalf("hello.meta.web_upgrade = %q，期望 %q", got.Meta.WebUpgrade, tc.want)
			}
		})
	}
}

func TestHelloCarriesTheLastUpgradeNoteAndOnlineMarkerHook(t *testing.T) {
	note := termproto.UpgradeNote{Outcome: termproto.UpgradeOutcomeRolledBack, From: "1.0.0", To: "9.9.9", At: 1_800_000_000, Detail: "新版本没有连上服务端"}
	var connected atomic.Value
	h := newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.Note = func() *termproto.UpgradeNote { return &note }
		c.Connected = func(v string) { connected.Store(v) }
	})
	hellos := make(chan termproto.Hello, 1)
	h.helloCapturingHub(t, func(hello termproto.Hello) { hellos <- hello }, func(ctx context.Context, ws *websocket.Conn) {
		_ = ws.Close(websocket.StatusNormalClosure, "done")
	})
	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
	})
	connectWith(t, a)

	hello := <-hellos
	if hello.Meta.LastUpgrade == nil || *hello.Meta.LastUpgrade != note {
		t.Fatalf("握手应带上上一次升级的结果，Web 才看得到回滚了: %+v", hello.Meta.LastUpgrade)
	}
	if v, _ := connected.Load().(string); v != "1.0.0" {
		t.Fatalf("握手成功后应通知上线（升级进程靠它判断新版本连上了），得到 %q", v)
	}
}

func TestAgentRunsAnUpgradeRequestedByTheHub(t *testing.T) {
	statuses := make(chan termproto.UpgradeStatus, 8)
	var connected atomic.Int32
	h := newUpgradeHarness(t, []byte("a brand new agent"), func(c *SelfUpgrade) {
		c.Connected = func(string) { connected.Add(1) }
	})
	hellos := make(chan termproto.Hello, 1)
	h.helloCapturingHub(t, func(hello termproto.Hello) { hellos <- hello }, func(ctx context.Context, ws *websocket.Conn) {
		sendJSON(t, ctx, ws, h.frame())
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var st termproto.UpgradeStatus
			if json.Unmarshal(data, &st) != nil || st.T != termproto.TUpgradeStatus {
				continue
			}
			statuses <- st
			if st.Stage != termproto.UpgradeStageDownloading {
				_ = ws.Close(websocket.StatusNormalClosure, "done")
				return
			}
		}
	})
	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
	})
	connectWith(t, a)

	if hello := <-hellos; hello.Meta.WebUpgrade != termproto.WebUpgradeOn {
		t.Fatalf("hello 应声明 web_upgrade=on: %q", hello.Meta.WebUpgrade)
	}
	var stages []string
	close(statuses)
	for st := range statuses {
		if st.ID != upgradeTestID {
			t.Errorf("状态应带回请求的 id: %+v", st)
		}
		stages = append(stages, st.Stage)
	}
	if strings.Join(stages, ",") != "downloading,handoff" {
		t.Fatalf("Hub 应依次收到 downloading、handoff: %v", stages)
	}
	calls := h.waitCalls(1)
	if len(calls) != 1 || calls[0].version != upgradeTestVersion || string(calls[0].data) != "a brand new agent" {
		t.Fatalf("Agent 应下载、校验并交接目标版本: %+v", calls)
	}
	if connected.Load() != 1 {
		t.Fatalf("应只在握手成功时通知一次上线: %d", connected.Load())
	}
}

func TestAgentRefusesUpgradeFromHubWhenOwnerHasNotAllowedIt(t *testing.T) {
	statuses := make(chan termproto.UpgradeStatus, 4)
	h := newUpgradeHarness(t, []byte("payload"), nil)
	h.allowed.Store(false)
	h.helloCapturingHub(t, func(termproto.Hello) {}, func(ctx context.Context, ws *websocket.Conn) {
		sendJSON(t, ctx, ws, h.frame())
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var st termproto.UpgradeStatus
			if json.Unmarshal(data, &st) == nil && st.T == termproto.TUpgradeStatus {
				statuses <- st
				_ = ws.Close(websocket.StatusNormalClosure, "done")
				return
			}
		}
	})
	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
	})
	connectWith(t, a)

	st := <-statuses
	if st.Stage != termproto.UpgradeStageRefused || st.Code != termproto.UpgradeCodeNotAllowed {
		t.Fatalf("所有者没有允许时，Hub 即使发了请求也必须被拒绝: %+v", st)
	}
	if h.hits.Load() != 0 || len(h.calls()) != 0 {
		t.Fatal("被拒绝的请求不应下载也不应交接")
	}
}

func TestAgentReconnectsWhenTheLocalGateFlips(t *testing.T) {
	old := upgradeGateInterval
	upgradeGateInterval = 20 * time.Millisecond
	t.Cleanup(func() { upgradeGateInterval = old })

	h := newUpgradeHarness(t, []byte("payload"), nil)
	h.allowed.Store(false)
	hellos := make(chan termproto.Hello, 8)
	h.helloCapturingHub(t, func(hello termproto.Hello) { hellos <- hello }, nil)

	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
		o.MinBackoff = 10 * time.Millisecond
		o.MaxBackoff = 20 * time.Millisecond
		o.SlowRetry = time.Minute // 开关变化引起的重连若被当成故障，会落到这个慢节奏上，测试就会超时
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run 未在 ctx 取消后及时返回")
		}
	}()

	wait := func() termproto.Hello {
		select {
		case hello := <-hellos:
			return hello
		case <-time.After(10 * time.Second):
			t.Fatal("没有等到 Agent 的 hello")
			return termproto.Hello{}
		}
	}
	if hello := wait(); hello.Meta.WebUpgrade != termproto.WebUpgradeOff {
		t.Fatalf("起初应声明 off: %q", hello.Meta.WebUpgrade)
	}
	h.allowed.Store(true) // 主机所有者执行了 `web-upgrade on`
	if hello := wait(); hello.Meta.WebUpgrade != termproto.WebUpgradeOn {
		t.Fatalf("所有者打开开关后，Agent 应自己重新握手并声明 on（不必重启服务）: %q", hello.Meta.WebUpgrade)
	}
}

func TestRetryDelayForADeliberateReconnect(t *testing.T) {
	a := newAgentFor(t, ServerTarget{URL: "ws://127.0.0.1:1" + AgentWSPath, Host: "127.0.0.1:1"}, func(o *Options) {
		o.MinBackoff = time.Second
		o.SlowRetry = time.Minute
	})
	attempt := 5
	// 带 reason 又没有关闭码与错误，本会被当成「握手阶段的协议性失败」按慢节奏重试；主动重连必须马上来。
	got := a.retryDelay(connResult{welcomed: true, reconnect: true, reason: "开关变了"}, &attempt)
	if got != time.Second {
		t.Fatalf("主动重连应按最小间隔，而不是故障的慢节奏: %v", got)
	}
	if attempt != 5 {
		t.Fatalf("主动重连不应推进退避计数: %d", attempt)
	}
}
