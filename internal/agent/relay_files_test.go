package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/agent/files"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// relayRig 把一个 relay 接到一条真实的 WebSocket 上：测试扮演 Hub，直接把控制帧交给 relay 处理，
// 再从另一端读取 Agent 实际发出的帧。
type relayRig struct {
	t      *testing.T
	r      *relay
	hub    *websocket.Conn
	frames chan rigFrame
}

type rigFrame struct {
	typ  websocket.MessageType
	data []byte
}

func newRelayRig(t *testing.T, svc *files.Service) *relayRig {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		c.SetReadLimit(1 << 20)
		accepted <- c
		<-req.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		t.Fatal(err)
	}
	hub := <-accepted
	t.Cleanup(func() { _ = hub.CloseNow(); _ = client.CloseNow() })
	r := newRelay(ctx, client, nil, true, t.Logf)
	r.holderKnown.Store(true) // 守护进程已连接且没有否决
	r.setFiles(svc)
	g := &relayRig{t: t, r: r, hub: hub, frames: make(chan rigFrame, 256)}
	// 单独的读协程：WebSocket 的读取一旦因超时取消就会关闭连接，所以「等一会儿看有没有帧」要靠通道而不是读超时。
	go func() {
		for {
			typ, data, err := hub.Read(ctx)
			if err != nil {
				close(g.frames)
				return
			}
			g.frames <- rigFrame{typ, data}
		}
	}()
	return g
}

func (g *relayRig) send(m termproto.FileReq) {
	g.t.Helper()
	m.T = termproto.TFileReq
	data, err := json.Marshal(m)
	if err != nil {
		g.t.Fatal(err)
	}
	g.r.handleText(data)
}

func (g *relayRig) next() (websocket.MessageType, []byte) {
	g.t.Helper()
	select {
	case f, ok := <-g.frames:
		if !ok {
			g.t.Fatal("连接已关闭，没有收到 Agent 的帧")
		}
		return f.typ, f.data
	case <-time.After(5 * time.Second):
		g.t.Fatal("等待 Agent 的帧超时")
	}
	return 0, nil
}

func (g *relayRig) res() termproto.FileRes {
	g.t.Helper()
	typ, data := g.next()
	if typ != websocket.MessageText {
		g.t.Fatalf("期望文本帧，收到二进制帧")
	}
	var res termproto.FileRes
	if err := json.Unmarshal(data, &res); err != nil || res.T != termproto.TFileRes {
		g.t.Fatalf("不是 file_res: %s (%v)", data, err)
	}
	return res
}

// silent 断言在短时间内 Agent 没有发出任何帧。
func (g *relayRig) silent() {
	g.t.Helper()
	select {
	case f, ok := <-g.frames:
		if ok {
			g.t.Fatalf("不应有应答，却收到: %q", f.data)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func fileFixture(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root, outside = filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, outside
}

func newFileService(t *testing.T, policy files.URLPolicy) *files.Service {
	t.Helper()
	s := files.New(files.Config{URL: policy})
	t.Cleanup(s.Shutdown)
	return s
}

func TestRelayFileStatHashOpenReadFlow(t *testing.T) {
	root, _ := fileFixture(t)
	content := strings.Repeat("0123456789abcdef", 10000) // 160000 字节：跨多个数据帧
	if err := os.WriteFile(filepath.Join(root, "big.bin"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	g := newRelayRig(t, newFileService(t, files.URLPolicy{}))
	p := filepath.Join(root, "big.bin")

	g.send(termproto.FileReq{Req: 1, Op: termproto.FileOpStat, Roots: []string{root}, Path: p})
	st := g.res()
	if !st.OK || st.Req != 1 || st.Stat == nil || !st.Stat.Exists || st.Stat.Size != int64(len(content)) {
		t.Fatalf("stat = %+v", st)
	}

	g.send(termproto.FileReq{Req: 2, Op: termproto.FileOpHash, Roots: []string{root}, Path: p})
	h := g.res()
	if !h.OK || len(h.SHA256) != 64 || h.Size != int64(len(content)) {
		t.Fatalf("hash = %+v", h)
	}

	g.send(termproto.FileReq{Req: 3, Op: termproto.FileOpOpen, Roots: []string{root}, Path: p})
	op := g.res()
	if !op.OK || !termproto.ValidFileHandle(op.Handle) || op.Size != int64(len(content)) {
		t.Fatalf("open = %+v", op)
	}

	// 一次请求读 1 MiB：数据被拆成不超过 32 KiB 的二进制帧，最后一帧带 Last 与 EOF
	g.send(termproto.FileReq{Req: 4, Op: termproto.FileOpRead, Handle: op.Handle, Off: 0, Len: termproto.MaxFileRead})
	var got []byte
	frames := 0
	for {
		typ, data := g.next()
		if typ != websocket.MessageBinary {
			t.Fatalf("读取应只返回二进制帧: %s", data)
		}
		c, err := termproto.DecodeFileData(data)
		if err != nil {
			t.Fatal(err)
		}
		if c.Req != 4 || c.Off != uint64(len(got)) || c.Total != uint64(len(content)) || len(c.Data) > termproto.MaxFileDataFrame {
			t.Fatalf("frame = req %d off %d total %d len %d（已收 %d）", c.Req, c.Off, c.Total, len(c.Data), len(got))
		}
		got = append(got, c.Data...)
		frames++
		if c.Flags&termproto.FileFlagLast != 0 {
			if c.Flags&termproto.FileFlagEOF == 0 {
				t.Fatalf("读到文件末尾的最后一帧应带 EOF")
			}
			break
		}
	}
	if string(got) != content || frames < 5 {
		t.Fatalf("内容不一致或没有分帧 (%d 字节, %d 帧)", len(got), frames)
	}

	// 越界读取：一个空的 EOF|Last 帧
	g.send(termproto.FileReq{Req: 5, Op: termproto.FileOpRead, Handle: op.Handle, Off: uint64(len(content)) + 100, Len: 1000})
	_, data := g.next()
	c, err := termproto.DecodeFileData(data)
	if err != nil || len(c.Data) != 0 || c.Flags != termproto.FileFlagEOF|termproto.FileFlagLast {
		t.Fatalf("越界读取 = %+v, %v", c, err)
	}

	// 关闭句柄不需要应答；之后读取返回 bad_handle
	g.send(termproto.FileReq{Req: 6, Op: termproto.FileOpClose, Handle: op.Handle})
	g.silent()
	g.send(termproto.FileReq{Req: 7, Op: termproto.FileOpRead, Handle: op.Handle, Off: 0, Len: 10})
	if bad := g.res(); bad.OK || bad.Code != termproto.FileErrBadHandle || bad.Req != 7 {
		t.Fatalf("关闭后读取 = %+v", bad)
	}
}

func TestRelayFileAuthorizationFailuresAreStableCodes(t *testing.T) {
	root, outside := fileFixture(t)
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := newRelayRig(t, newFileService(t, files.URLPolicy{}))
	roots := []string{root}

	g.send(termproto.FileReq{Req: 1, Op: termproto.FileOpStat, Roots: roots, Path: filepath.Join(outside, "secret")})
	if r := g.res(); r.OK || r.Code != termproto.FileErrNotAllowed {
		t.Fatalf("目录外路径 = %+v", r)
	}
	g.send(termproto.FileReq{Req: 2, Op: termproto.FileOpOpen, Roots: roots, Path: filepath.Join(root, "..", "outside", "secret")})
	if r := g.res(); r.OK || r.Code != termproto.FileErrNotAllowed {
		t.Fatalf(".. 逃逸 = %+v", r)
	}
	g.send(termproto.FileReq{Req: 3, Op: "rm", Roots: roots, Path: root})
	if r := g.res(); r.OK || r.Code != termproto.FileErrBadRequest {
		t.Fatalf("未知操作 = %+v", r)
	}
	g.send(termproto.FileReq{Req: 4, Op: termproto.FileOpStat, Roots: roots, Path: "relative"})
	if r := g.res(); r.OK || r.Code != termproto.FileErrBadPath {
		t.Fatalf("相对路径 = %+v", r)
	}
	g.send(termproto.FileReq{Req: 5, Op: termproto.FileOpStat, Path: filepath.Join(root, "x")}) // 没有允许目录
	if r := g.res(); r.OK || r.Code != termproto.FileErrNotAllowed {
		t.Fatalf("没有允许目录 = %+v", r)
	}
}

func TestRelayFileVetoAndHolderState(t *testing.T) {
	root, _ := fileFixture(t)
	req := termproto.FileReq{Op: termproto.FileOpStat, Roots: []string{root}, Path: root}

	// Agent 自己的一票否决
	g := newRelayRig(t, newFileService(t, files.URLPolicy{}))
	g.r.denyExec = true
	req.Req = 1
	g.send(req)
	if r := g.res(); r.OK || r.Code != termproto.FileErrDenied || !strings.Contains(r.Msg, "--deny-model-exec") {
		t.Fatalf("Agent 否决 = %+v", r)
	}

	// 守护进程上的否决（外置守护进程可能单独设置）
	g = newRelayRig(t, newFileService(t, files.URLPolicy{}))
	g.r.holderDeny.Store(true)
	req.Req = 2
	g.send(req)
	if r := g.res(); r.OK || r.Code != termproto.FileErrDenied {
		t.Fatalf("守护进程否决 = %+v", r)
	}

	// 从未见过守护进程：无法确认本机策略，按否决处理
	g = newRelayRig(t, newFileService(t, files.URLPolicy{}))
	g.r.holderKnown.Store(false)
	req.Req = 3
	g.send(req)
	if r := g.res(); r.OK || r.Code != termproto.FileErrDenied {
		t.Fatalf("未知策略 = %+v", r)
	}

	// 否决不阻止关闭句柄（关闭永远允许，且没有应答）
	g.send(termproto.FileReq{Req: 4, Op: termproto.FileOpClose, Handle: strings.Repeat("a", 32)})
	g.silent()

	// 没有否决时一切正常
	g = newRelayRig(t, newFileService(t, files.URLPolicy{}))
	req.Req = 5
	g.send(req)
	if r := g.res(); !r.OK || r.Stat == nil || r.Stat.Type != termproto.FileTypeDir {
		t.Fatalf("正常 = %+v", r)
	}

	// 没有文件服务
	g = newRelayRig(t, nil)
	g.r.files = nil
	req.Req = 6
	g.send(req)
	if r := g.res(); r.OK || r.Code != termproto.FileErrDenied {
		t.Fatalf("没有文件服务 = %+v", r)
	}
}

func TestRelayFileRequestsIgnoredUntilEnabled(t *testing.T) {
	root, _ := fileFixture(t)
	g := newRelayRig(t, newFileService(t, files.URLPolicy{}))
	g.r.enabled.Store(false)
	g.send(termproto.FileReq{Req: 1, Op: termproto.FileOpStat, Roots: []string{root}, Path: root})
	g.silent()
}

func TestRelayFileBackpressure(t *testing.T) {
	root, _ := fileFixture(t)
	g := newRelayRig(t, newFileService(t, files.URLPolicy{}))
	for i := 0; i < cap(g.r.fileSem); i++ {
		g.r.fileSem <- struct{}{}
	}
	g.send(termproto.FileReq{Req: 1, Op: termproto.FileOpStat, Roots: []string{root}, Path: root})
	if r := g.res(); r.OK || r.Code != termproto.FileErrBusy {
		t.Fatalf("并发满时应返回 busy: %+v", r)
	}
	<-g.r.fileSem
	g.send(termproto.FileReq{Req: 2, Op: termproto.FileOpStat, Roots: []string{root}, Path: root})
	if r := g.res(); !r.OK {
		t.Fatalf("释放一个名额后应恢复: %+v", r)
	}
}

func TestRelayFilePushURL(t *testing.T) {
	root, _ := fileFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("pushed")) }))
	t.Cleanup(srv.Close)
	roots := []string{root}
	dest := filepath.Join(root, "got.txt")

	// 默认策略：回环地址一律拒绝
	g := newRelayRig(t, newFileService(t, files.URLPolicy{}))
	g.send(termproto.FileReq{Req: 1, Op: termproto.FileOpPushURL, Roots: roots, Path: dest, URL: srv.URL + "/x"})
	if r := g.res(); r.OK || r.Code != termproto.FileErrURLBlocked {
		t.Fatalf("默认策略应拒绝回环地址: %+v", r)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("被拒绝的网址不应产生文件")
	}

	// 本机拥有者放开私网后可以
	g = newRelayRig(t, newFileService(t, files.URLPolicy{AllowPrivate: true}))
	g.send(termproto.FileReq{Req: 2, Op: termproto.FileOpPushURL, Roots: roots, Path: dest, URL: srv.URL + "/x?token=SECRET"})
	r := g.res()
	if !r.OK || r.Size != 6 || r.Status != 200 || len(r.SHA256) != 64 || strings.Contains(r.Source, "SECRET") {
		t.Fatalf("push_url = %+v", r)
	}
	if b, _ := os.ReadFile(dest); string(b) != "pushed" {
		t.Fatalf("文件内容 = %q", b)
	}
	// 已存在且没有 overwrite
	g.send(termproto.FileReq{Req: 3, Op: termproto.FileOpPushURL, Roots: roots, Path: dest, URL: srv.URL + "/x"})
	if r := g.res(); r.OK || r.Code != termproto.FileErrExists {
		t.Fatalf("已存在 = %+v", r)
	}
	g.send(termproto.FileReq{Req: 4, Op: termproto.FileOpPushURL, Roots: roots, Path: dest, URL: srv.URL + "/x", Overwrite: true, SHA256: strings.Repeat("0", 64)})
	if r := g.res(); r.OK || r.Code != termproto.FileErrHashMismatch {
		t.Fatalf("摘要不符 = %+v", r)
	}
}
