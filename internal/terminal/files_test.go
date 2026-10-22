package terminal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func withFiles() envOption { return func(o *Options, _ *testEnv) { o.FilesEnabled = true } }

// ---------- 假主机：在假 Agent 之上模拟 file_req 的行为 ----------
//
// 路径授权的权威实现在 Agent（internal/agent/files，有自己的测试）。这里的假主机只做最朴素的前缀判断，
// 目的是验证 Hub 一侧：下发的 roots 对不对、闸门有没有拦住、票据怎么用、应答怎么被当作不可信输入处理。

type fileSim struct {
	a *fakeAgent

	mu      sync.Mutex
	files   map[string][]byte
	handles map[string]string
	reqs    []termproto.FileReq
	closes  int

	failOp      map[string]string // 操作 → 强制返回的失败码
	silent      bool              // 不回复任何请求
	holdRead    chan struct{}     // 非 nil 时 read 会先在这里等待
	readStarted chan struct{}     // read 开始时通知（非阻塞）
	badOffset   bool              // read 的数据帧偏移不连续
	cutAt       int               // >0 时，起点不小于它的 read 失败（模拟下载中途出错）
	pushNote    string            // push_url 写入的内容后缀（用于验证）
}

func attachFileSim(a *fakeAgent) *fileSim {
	s := &fileSim{a: a, files: map[string][]byte{}, handles: map[string]string{}, failOp: map[string]string{}}
	prev := a.extra
	a.extra = func(t string, data []byte) {
		if t == termproto.TFileReq {
			var req termproto.FileReq
			_ = json.Unmarshal(data, &req)
			s.mu.Lock()
			s.reqs = append(s.reqs, req)
			s.mu.Unlock()
			go s.serve(req)
			return
		}
		if prev != nil {
			prev(t, data)
		}
	}
	return s
}

func inRoots(path string, roots []string) bool {
	for _, r := range roots {
		if r == termproto.FileRootAll {
			return true // 「允许整个文件系统」：真正的 Agent 在这里只要求规范的绝对路径
		}
		r = strings.TrimRight(r, "/")
		if path == r || strings.HasPrefix(path, r+"/") {
			return true
		}
	}
	return false
}

func (s *fileSim) put(path string, data []byte) {
	s.mu.Lock()
	s.files[path] = data
	s.mu.Unlock()
}

func (s *fileSim) requests(op string) []termproto.FileReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []termproto.FileReq
	for _, r := range s.reqs {
		if r.Op == op {
			out = append(out, r)
		}
	}
	return out
}

func (s *fileSim) serve(req termproto.FileReq) {
	reply := func(r termproto.FileRes) {
		r.T, r.Req = termproto.TFileRes, req.Req
		s.a.send(r)
	}
	fail := func(code, msg string) { reply(termproto.FileRes{OK: false, Code: code, Msg: msg}) }

	s.mu.Lock()
	forced, silent := s.failOp[req.Op], s.silent
	s.mu.Unlock()
	if silent {
		return
	}
	if forced != "" {
		fail(forced, "forced "+forced)
		return
	}

	switch req.Op {
	case termproto.FileOpStat:
		if !inRoots(req.Path, req.Roots) {
			fail(termproto.FileErrNotAllowed, "outside the allowed directories")
			return
		}
		s.mu.Lock()
		data, ok := s.files[req.Path]
		s.mu.Unlock()
		if !ok {
			reply(termproto.FileRes{OK: true, Stat: &termproto.FileStat{Exists: false}})
			return
		}
		reply(termproto.FileRes{OK: true, Stat: &termproto.FileStat{Exists: true, Type: termproto.FileTypeFile, Size: int64(len(data)), Mode: "0644", MTime: 1_700_000_000}})
	case termproto.FileOpHash:
		if !inRoots(req.Path, req.Roots) {
			fail(termproto.FileErrNotAllowed, "outside the allowed directories")
			return
		}
		s.mu.Lock()
		data, ok := s.files[req.Path]
		s.mu.Unlock()
		if !ok {
			fail(termproto.FileErrNotFound, "no such file")
			return
		}
		sum := sha256.Sum256(data)
		reply(termproto.FileRes{OK: true, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))})
	case termproto.FileOpOpen:
		if !inRoots(req.Path, req.Roots) {
			fail(termproto.FileErrNotAllowed, "outside the allowed directories")
			return
		}
		s.mu.Lock()
		data, ok := s.files[req.Path]
		s.mu.Unlock()
		if !ok {
			fail(termproto.FileErrNotFound, "no such file")
			return
		}
		if req.Max > 0 && int64(len(data)) > req.Max {
			fail(termproto.FileErrTooLarge, "too large")
			return
		}
		raw := make([]byte, 16)
		_, _ = rand.Read(raw)
		h := hex.EncodeToString(raw)
		s.mu.Lock()
		s.handles[h] = req.Path
		s.mu.Unlock()
		reply(termproto.FileRes{OK: true, Handle: h, Size: int64(len(data))})
	case termproto.FileOpRead:
		s.serveRead(req)
	case termproto.FileOpClose:
		s.mu.Lock()
		delete(s.handles, req.Handle)
		s.closes++
		s.mu.Unlock()
		reply(termproto.FileRes{OK: true})
	case termproto.FileOpPushURL:
		if !inRoots(req.Path, req.Roots) {
			fail(termproto.FileErrNotAllowed, "outside the allowed directories")
			return
		}
		body := []byte("downloaded:" + s.pushNote)
		s.mu.Lock()
		s.files[req.Path] = body
		s.mu.Unlock()
		sum := sha256.Sum256(body)
		reply(termproto.FileRes{OK: true, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)), Status: 200, Source: "https://files.example.test", Redirects: 1})
	}
}

func (s *fileSim) serveRead(req termproto.FileReq) {
	s.mu.Lock()
	path, ok := s.handles[req.Handle]
	data := s.files[path]
	hold, started, bad, cut := s.holdRead, s.readStarted, s.badOffset, s.cutAt
	s.mu.Unlock()
	if !ok {
		s.a.send(termproto.FileRes{T: termproto.TFileRes, Req: req.Req, OK: false, Code: termproto.FileErrBadHandle, Msg: "no such handle"})
		return
	}
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if hold != nil {
		select {
		case <-hold:
		case <-time.After(10 * time.Second):
			return
		}
	}
	off := int(req.Off)
	if cut > 0 && off >= cut {
		s.a.send(termproto.FileRes{T: termproto.TFileRes, Req: req.Req, OK: false, Code: termproto.FileErrIO, Msg: "disk error"})
		return
	}
	end := min(len(data), off+req.Len)
	skew := uint64(0)
	if bad {
		skew = 1
	}
	if off >= end {
		flags := termproto.FileFlagLast
		if off >= len(data) {
			flags |= termproto.FileFlagEOF
		}
		s.a.sendBinary(termproto.EncodeFileData(termproto.FileChunk{Req: req.Req, Flags: flags, Off: uint64(off) + skew, Total: uint64(len(data))}))
		return
	}
	for pos := off; pos < end; {
		n := min(end-pos, termproto.MaxFileDataFrame)
		var flags byte
		if pos+n >= len(data) {
			flags |= termproto.FileFlagEOF
		}
		if pos+n >= end {
			flags |= termproto.FileFlagLast
		}
		s.a.sendBinary(termproto.EncodeFileData(termproto.FileChunk{Req: req.Req, Flags: flags, Off: uint64(pos) + skew, Total: uint64(len(data)), Data: data[pos : pos+n]}))
		pos += n
	}
}

// ---------- 夹具 ----------

type filesFixture struct {
	env *testEnv
	a   *fakeAgent
	sim *hostSim
	fs  *fileSim
	adm []string
	svc *FileService

	credSeq int
}

func newFilesFixture(t *testing.T, opts ...envOption) *filesFixture {
	t.Helper()
	// 默认是认识「整个文件系统」的新版 Agent；要模拟旧版 Agent 用 newFilesFixtureWith。
	return newFilesFixtureWith(t, func(m *termproto.Meta) { m.FilesAll = true }, opts...)
}

// newFilesFixtureWith 与 newFilesFixture 相同，但允许调整 Agent 握手时自报的能力。
func newFilesFixtureWith(t *testing.T, tweakMeta func(*termproto.Meta), opts ...envOption) *filesFixture {
	t.Helper()
	env := newTestEnv(t, append([]envOption{withExec(), withFiles()}, opts...)...)
	meta := testMeta("files-host")
	meta.Files = true
	if tweakMeta != nil {
		tweakMeta(&meta)
	}
	a := env.enrollAgent(meta, "files box")
	sim := attachSim(a)
	f := &filesFixture{env: env, a: a, sim: sim, fs: attachFileSim(a), adm: env.admin(), svc: env.hub.Files()}
	sim.reportHolder(false, true)
	eventually(t, 2*time.Second, "Hub 识别主机的文件能力", func() bool {
		ag := env.hub.onlineAgent(a.code)
		if ag == nil {
			return false
		}
		ok, _ := ag.filesSupport()
		return ok
	})
	f.setModel(t, map[string]any{"allow_model": true})
	return f
}

func (f *filesFixture) setModel(t *testing.T, body map[string]any) {
	t.Helper()
	if res := f.env.call("POST", "/api/v1/terminal/hosts/"+f.a.code+"/model", body, f.adm...); res.Status != 200 {
		t.Fatalf("设置模型访问失败: %d %s", res.Status, res.Raw)
	}
}

// newCred 在库里创建一个带文件权限的令牌（默认授权这台主机与 /srv/data）。下载端点会回库重新核对它。
func (f *filesFixture) newCred(t *testing.T, mut func(c *model.MCPCredential)) *model.MCPCredential {
	t.Helper()
	f.credSeq++
	c := &model.MCPCredential{
		Name:           fmt.Sprintf("files-agent-%d", f.credSeq),
		Permissions:    model.Permissions{Terminal: true, TerminalFiles: true},
		TerminalHosts:  model.StringList{f.a.code},
		TerminalLimits: model.TerminalLimits{AllowedPaths: []string{"/srv/data"}},
	}
	if mut != nil {
		mut(c)
	}
	if err := f.env.store.CreateCredential(context.Background(), c); err != nil {
		t.Fatalf("创建令牌: %v", err)
	}
	return c
}

func callerOf(c *model.MCPCredential) termexec.Caller {
	return termexec.Caller{
		CredentialID: c.ID, CredentialName: c.Name, Credential: c, Permissions: c.Permissions,
		TokenString: c.Token, Source: "db_credential", RemoteIP: "10.0.0.5", UserAgent: "TestClient/1.0",
	}
}

func (f *filesFixture) files(t *testing.T, c *model.MCPCredential, req termexec.FileRequest) (any, error) {
	t.Helper()
	if req.Target == "" {
		req.Target = f.a.code
	}
	return f.svc.Files(context.Background(), callerOf(c), req)
}

func (f *filesFixture) audits(t *testing.T) []model.TerminalAuditLog {
	t.Helper()
	logs, err := f.env.store.ListTerminalAudit(context.Background(), 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	return logs
}

func (f *filesFixture) auditsOf(t *testing.T, event string) []model.TerminalAuditLog {
	var out []model.TerminalAuditLog
	for _, l := range f.audits(t) {
		if l.Event == event {
			out = append(out, l)
		}
	}
	return out
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ---------- 闸门流水线 ----------

func TestFilesGatePipeline(t *testing.T) {
	t.Run("文件开关关闭时静默拒绝且不写审计", func(t *testing.T) {
		env := newTestEnv(t, withExec()) // 没有 withFiles
		c := &model.MCPCredential{ID: 1, Permissions: model.Permissions{TerminalFiles: true}, TerminalLimits: model.TerminalLimits{AllowedPaths: []string{"/srv"}}}
		_, err := env.hub.Files().Files(context.Background(), callerOf(c), termexec.FileRequest{Action: "stat", Path: "/srv/x"})
		wantCode(t, err, termexec.CodeFilesOff)
		if logs, _ := env.store.ListTerminalAudit(context.Background(), 50, 0); len(logs) != 0 {
			t.Fatalf("关闭时不应写任何审计: %+v", logs)
		}
	})

	t.Run("文件开关开着但模型执行总开关关闭：仍然关闭", func(t *testing.T) {
		env := newTestEnv(t, withFiles()) // 没有 withExec
		if env.hub.Files().enabled {
			t.Fatal("没有 ExecEnabled 时文件能力不得启用")
		}
	})

	f := newFilesFixture(t)
	f.fs.put("/srv/data/a.txt", []byte("hello"))
	cred := f.newCred(t, nil)
	stat := termexec.FileRequest{Action: "stat", Path: "/srv/data/a.txt"}
	deniedBefore := len(f.auditsOf(t, EvExecDenied))

	if _, err := f.files(t, cred, stat); err != nil {
		t.Fatalf("基线应通过: %v", err)
	}

	t.Run("令牌没有文件权限", func(t *testing.T) {
		c := f.newCred(t, func(c *model.MCPCredential) { c.Permissions = model.Permissions{Terminal: true} })
		_, err := f.files(t, c, stat)
		wantCode(t, err, termexec.CodeNotAllowed)
	})
	t.Run("令牌没有允许目录", func(t *testing.T) {
		// 库里不可能存在这样的令牌（校验会拒绝），但 Hub 不能依赖这一点：它是第二道防线。
		c := f.newCred(t, nil)
		c.TerminalLimits.AllowedPaths = nil
		_, err := f.files(t, c, stat)
		wantCode(t, err, termexec.CodeNotAllowed)
		if len(f.fs.requests(termproto.FileOpStat)) != 1 {
			t.Fatal("没有允许目录时不得向主机发出请求")
		}
	})
	t.Run("令牌强度不足", func(t *testing.T) {
		_, err := f.svc.Files(context.Background(), func() termexec.Caller {
			c := callerOf(cred)
			c.TokenString = "short"
			return c
		}(), stat)
		wantCode(t, err, termexec.CodeWeakToken)
	})
	t.Run("目标不在令牌的主机白名单里", func(t *testing.T) {
		c := f.newCred(t, func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"RM-AAAA-BBBB-CCCC-DDDD"} })
		_, err := f.files(t, c, stat)
		if err == nil {
			t.Fatal("应被拒绝")
		}
	})
	t.Run("参数形状", func(t *testing.T) {
		for name, req := range map[string]termexec.FileRequest{
			"未知 action":   {Action: "delete", Path: "/srv/data/a"},
			"缺少 path":     {Action: "stat"},
			"push 缺 url":  {Action: "push_url", Path: "/srv/data/x"},
			"push 非 http": {Action: "push_url", Path: "/srv/data/x", URL: "ftp://example.com/x"},
			"push 非法摘要":   {Action: "push_url", Path: "/srv/data/x", URL: "https://example.com/x", SHA256: "xyz"},
			"路径含控制字符":     {Action: "stat", Path: "/srv/data/a\nb"},
		} {
			_, err := f.files(t, cred, req)
			wantCode(t, err, termexec.CodeBadRequest)
			_ = name
		}
	})

	t.Run("主机不允许模型访问", func(t *testing.T) {
		f.setModel(t, map[string]any{"allow_model": false})
		defer f.setModel(t, map[string]any{"allow_model": true})
		_, err := f.files(t, cred, stat)
		wantCode(t, err, termexec.CodeHostOff)
	})
	t.Run("全局暂停", func(t *testing.T) {
		if r := f.env.call("POST", "/api/v1/terminal/exec/pause", map[string]any{"scope": "all"}, "Authorization", humanAuth(testUser)); r.Status != 200 {
			t.Fatalf("暂停: %d %s", r.Status, r.Raw)
		}
		_, err := f.files(t, cred, stat)
		wantCode(t, err, termexec.CodePaused)
		if r := f.env.call("POST", "/api/v1/terminal/exec/resume", map[string]any{"scope": "all"}, f.adm...); r.Status != 200 {
			t.Fatalf("恢复: %d %s", r.Status, r.Raw)
		}
		if _, err := f.files(t, cred, stat); err != nil {
			t.Fatalf("恢复后应可用: %v", err)
		}
	})
	t.Run("本机一票否决（--deny-model-exec）", func(t *testing.T) {
		f.sim.reportHolder(true, true)
		eventually(t, 2*time.Second, "Hub 得知本机否决", func() bool {
			_, denied := f.env.hub.onlineAgent(f.a.code).filesSupport()
			return denied
		})
		before := len(f.fs.requests(termproto.FileOpStat))
		_, err := f.files(t, cred, stat)
		wantCode(t, err, termexec.CodeHostDenied)
		if len(f.fs.requests(termproto.FileOpStat)) != before {
			t.Fatal("被本机否决时不得向主机发出请求")
		}
		f.sim.reportHolder(false, true)
		eventually(t, 2*time.Second, "否决撤销", func() bool {
			_, denied := f.env.hub.onlineAgent(f.a.code).filesSupport()
			return !denied
		})
	})
	t.Run("守护进程不可用：本机策略无法确认，视为不支持", func(t *testing.T) {
		f.a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderUnavailable})
		eventually(t, 2*time.Second, "Hub 得知守护进程不可用", func() bool {
			ok, _ := f.env.hub.onlineAgent(f.a.code).filesSupport()
			return !ok
		})
		_, err := f.files(t, cred, stat)
		wantCode(t, err, termexec.CodeHostLegacy)
		f.sim.reportHolder(false, true)
		eventually(t, 2*time.Second, "守护进程恢复", func() bool {
			ok, _ := f.env.hub.onlineAgent(f.a.code).filesSupport()
			return ok
		})
	})
	t.Run("文件传输要求 auto 模式，stat / hash 不受影响", func(t *testing.T) {
		f.setModel(t, map[string]any{"exec_mode": "approve"})
		defer f.setModel(t, map[string]any{"exec_mode": "auto"})
		for _, req := range []termexec.FileRequest{
			{Action: "pull_prepare", Path: "/srv/data/a.txt"},
			{Action: "push_url", Path: "/srv/data/new.bin", URL: "https://example.com/x"},
		} {
			_, err := f.files(t, cred, req)
			wantCode(t, err, termexec.CodeModeDenied)
		}
		if _, err := f.files(t, cred, stat); err != nil {
			t.Fatalf("approve 模式下 stat 应可用: %v", err)
		}
		if _, err := f.files(t, cred, termexec.FileRequest{Action: "hash", Path: "/srv/data/a.txt"}); err != nil {
			t.Fatalf("approve 模式下 hash 应可用: %v", err)
		}
		// 令牌自己的模式上限同样生效。
		f.setModel(t, map[string]any{"exec_mode": "auto"})
		capped := f.newCred(t, func(c *model.MCPCredential) { c.TerminalModeCap = model.TerminalModeApprove })
		_, err := f.files(t, capped, termexec.FileRequest{Action: "pull_prepare", Path: "/srv/data/a.txt"})
		wantCode(t, err, termexec.CodeModeDenied)
	})
	t.Run("主机离线", func(t *testing.T) {
		g := newFilesFixture(t)
		c := g.newCred(t, nil)
		g.a.cancel()
		eventually(t, 3*time.Second, "主机离线", func() bool { return g.env.hub.onlineAgent(g.a.code) == nil })
		_, err := g.files(t, c, stat)
		wantCode(t, err, termexec.CodeHostOffline)
	})

	if got := len(f.auditsOf(t, EvExecDenied)); got <= deniedBefore {
		t.Fatalf("被拒绝的调用应写入审计: before=%d after=%d", deniedBefore, got)
	}
}

// 终端总闸（Web 密码 + 合格的 JWT 密钥）关闭时，文件能力整体不可用，已签发的票据也不能再用。
func TestFilesTerminalGateClosed(t *testing.T) {
	f := newFilesFixture(t)
	f.fs.put("/srv/data/a.txt", []byte("hello"))
	cred := f.newCred(t, nil)
	pull := f.prepare(t, cred, "/srv/data/a.txt")

	f.env.creds.set("", "", nil) // 清空 Web 密码 → 总闸关闭
	eventually(t, 3*time.Second, "总闸关闭", func() bool {
		_, err := f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/srv/data/a.txt"})
		te, ok := err.(*termexec.Error)
		return ok && te.Code == termexec.CodeDisabled
	})
	for _, req := range []termexec.FileRequest{
		{Action: "stat", Path: "/srv/data/a.txt"},
		{Action: "hash", Path: "/srv/data/a.txt"},
		{Action: "pull_prepare", Path: "/srv/data/a.txt"},
		{Action: "push_url", Path: "/srv/data/n", URL: "https://example.com/x"},
	} {
		_, err := f.files(t, cred, req)
		wantCode(t, err, termexec.CodeDisabled)
	}
	if r, b, _ := f.get(t, pull.URL); r.StatusCode == 200 || strings.Contains(string(b), "hello") {
		t.Fatalf("总闸关闭后票据不能再下载: %d %q", r.StatusCode, b)
	}
}

func TestFilesAgentWithoutFileSupportIsUnsupported(t *testing.T) {
	env := newTestEnv(t, withExec(), withFiles())
	a := env.enrollAgent(testMeta("old-host"), "old box") // Meta.Files 为 false：老版本 Agent
	sim := attachSim(a)
	sim.reportHolder(false, true)
	adm := env.admin()
	if r := env.call("POST", "/api/v1/terminal/hosts/"+a.code+"/model", map[string]any{"allow_model": true}, adm...); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	eventually(t, 2*time.Second, "holder", func() bool {
		ag := env.hub.onlineAgent(a.code)
		return ag != nil && ag.holder != nil
	})
	c := &model.MCPCredential{ID: 3, Name: "x", IsActive: true, Permissions: model.Permissions{TerminalFiles: true},
		TerminalHosts: model.StringList{a.code}, TerminalLimits: model.TerminalLimits{AllowedPaths: []string{"/srv"}}}
	caller := callerOf(c)
	caller.TokenString = execToken
	_, err := env.hub.Files().Files(context.Background(), caller, termexec.FileRequest{Action: "stat", Target: a.code, Path: "/srv/a"})
	wantCode(t, err, termexec.CodeHostLegacy)
}

// ---------- stat / hash ----------

func TestFilesStatAndHash(t *testing.T) {
	f := newFilesFixture(t)
	body := randomBytes(300_000)
	f.fs.put("/srv/data/blob.bin", body)
	cred := f.newCred(t, func(c *model.MCPCredential) {
		c.TerminalLimits.AllowedPaths = []string{"/srv/data", "~/work"}
	})

	res, err := f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/srv/data/blob.bin"})
	if err != nil {
		t.Fatal(err)
	}
	st := res.(termexec.FileStatResult)
	if !st.Exists || st.Type != "file" || st.Size != int64(len(body)) || st.Mode != "0644" || st.MTime != "2023-11-14T22:13:20Z" || st.Target != f.a.code {
		t.Fatalf("stat = %+v", st)
	}
	// 令牌的允许目录原样下发（含 ~，由 Agent 展开）。
	reqs := f.fs.requests(termproto.FileOpStat)
	if len(reqs) != 1 || strings.Join(reqs[0].Roots, "|") != "/srv/data|~/work" || reqs[0].Path != "/srv/data/blob.bin" {
		t.Fatalf("下发的请求 = %+v", reqs)
	}

	// 不存在不是错误。
	res, err = f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/srv/data/missing"})
	if err != nil || res.(termexec.FileStatResult).Exists {
		t.Fatalf("不存在应是 exists=false: %+v %v", res, err)
	}

	res, err = f.files(t, cred, termexec.FileRequest{Action: "hash", Path: "/srv/data/blob.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if h := res.(termexec.FileHashResult); h.SHA256 != sha256Hex(body) || h.Size != int64(len(body)) {
		t.Fatalf("hash = %+v", h)
	}
	_, err = f.files(t, cred, termexec.FileRequest{Action: "hash", Path: "/srv/data/missing"})
	wantCode(t, err, termproto.FileErrNotFound)

	// 越界：主机拒绝 -> path_not_allowed，且写入「被拒绝」审计（可追溯探测行为）。
	_, err = f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/etc/passwd"})
	wantCode(t, err, termexec.CodePathDenied)
	found := false
	for _, l := range f.auditsOf(t, EvExecDenied) {
		// 明细里带着被请求的路径，便于取证「模型到底想碰哪里」。
		if strings.Contains(l.Detail, "files:stat") && strings.Contains(l.Detail, termexec.CodePathDenied) && l.Actor == cred.Name &&
			strings.Contains(l.Detail, "path=/etc/passwd") {
			found = true
		}
	}
	if !found {
		t.Fatalf("越界应写 exec_denied 审计: %+v", f.auditsOf(t, EvExecDenied))
	}

	// 成功的操作有 file_op 审计；其中有路径、没有内容。
	ops := f.auditsOf(t, EvFileOp)
	if len(ops) == 0 {
		t.Fatal("应有 file_op 审计")
	}
	for _, l := range ops {
		if l.ActorKind != ActorModel || l.MachineCode != f.a.code {
			t.Fatalf("审计字段 = %+v", l)
		}
	}
}

func TestFilesAgentFailuresMapToStableCodes(t *testing.T) {
	f := newFilesFixture(t)
	f.fs.put("/srv/data/a", []byte("x"))
	cred := f.newCred(t, nil)
	for op, req := range map[string]termexec.FileRequest{
		termproto.FileOpStat: {Action: "stat", Path: "/srv/data/a"},
		termproto.FileOpHash: {Action: "hash", Path: "/srv/data/a"},
		termproto.FileOpOpen: {Action: "pull_prepare", Path: "/srv/data/a"},
	} {
		for code, want := range map[string]string{
			termproto.FileErrDenied:     termexec.CodeHostDenied,
			termproto.FileErrNotAllowed: termexec.CodePathDenied,
			termproto.FileErrPermission: termproto.FileErrPermission,
			termproto.FileErrNotFile:    termproto.FileErrNotFile,
			termproto.FileErrTooLarge:   termproto.FileErrTooLarge,
		} {
			f.fs.mu.Lock()
			f.fs.failOp[op] = code
			f.fs.mu.Unlock()
			_, err := f.files(t, cred, req)
			wantCode(t, err, want)
		}
	}
	// 主机回了一个未知的错误码：被归一化，不会原样透传。
	f.fs.mu.Lock()
	f.fs.failOp[termproto.FileOpStat] = "../../etc/shadow"
	f.fs.mu.Unlock()
	_, err := f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/srv/data/a"})
	wantCode(t, err, termproto.FileErrIO)
}

func TestFilesSilentHostTimesOut(t *testing.T) {
	overrideGlobal(t, &fileCallTimeout, 300*time.Millisecond)

	f := newFilesFixture(t)
	cred := f.newCred(t, nil)
	f.fs.mu.Lock()
	f.fs.silent = true
	f.fs.mu.Unlock()
	start := time.Now()
	_, err := f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/srv/data/a"})
	wantCode(t, err, termproto.FileErrTimeout)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("超时应按期限返回，用了 %v", time.Since(start))
	}
	// 超时之后不留下在途请求。
	ag := f.env.hub.onlineAgent(f.a.code)
	ag.mu.Lock()
	n := len(ag.fileWaits)
	ag.mu.Unlock()
	if n != 0 {
		t.Fatalf("超时后仍有 %d 个在途请求", n)
	}
}

// ---------- 拉取（pull_prepare + 下载） ----------

func (f *filesFixture) prepare(t *testing.T, c *model.MCPCredential, path string) termexec.FilePullResult {
	t.Helper()
	res, err := f.files(t, c, termexec.FileRequest{Action: "pull_prepare", Path: path})
	if err != nil {
		t.Fatalf("pull_prepare: %v", err)
	}
	return res.(termexec.FilePullResult)
}

func (f *filesFixture) get(t *testing.T, url string) (*http.Response, []byte, error) {
	t.Helper()
	resp, err := f.env.srv.Client().Get(f.env.srv.URL + url)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

func TestFilesPullPrepareAndDownload(t *testing.T) {
	f := newFilesFixture(t, withFakeClock())
	body := randomBytes(2*1024*1024 + 12345) // 跨越多次 1 MiB 读取与多个 32 KiB 数据帧
	f.fs.put("/srv/data/dir/大文件 v1.2.bin", body)
	cred := f.newCred(t, nil)

	pull := f.prepare(t, cred, "/srv/data/dir/大文件 v1.2.bin")
	if !strings.HasPrefix(pull.URL, "/files/dl/") || len(pull.URL) != len("/files/dl/")+fileTicketLen || pull.Method != "GET" ||
		pull.ExpiresIn != 120 || pull.Size != int64(len(body)) || pull.Hint == "" {
		t.Fatalf("pull_prepare 结果 = %+v", pull)
	}
	if f.svc.tickets.count() != 1 {
		t.Fatalf("应有一张票据: %d", f.svc.tickets.count())
	}
	// prepare 只是验证：它打开的句柄已被放掉。
	eventually(t, 2*time.Second, "句柄被放掉", func() bool {
		f.fs.mu.Lock()
		defer f.fs.mu.Unlock()
		return len(f.fs.handles) == 0 && f.fs.closes >= 1
	})

	resp, got, err := f.get(t, pull.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("下载: %v %+v", err, resp)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("下载内容不一致: got=%d want=%d", len(got), len(body))
	}
	if resp.ContentLength != int64(len(body)) || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("响应头 = %v", resp.Header)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="`) || !strings.Contains(cd, "filename*=UTF-8''%E5%A4%A7") || strings.ContainsAny(strings.SplitN(cd, "; filename*", 2)[0], "\r\n") {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	// 一次性：重放得到与「无效票据」完全相同的应答。
	replay, replayBody, _ := f.get(t, pull.URL)
	unknown, unknownBody, _ := f.get(t, "/files/dl/"+strings.Repeat("A", fileTicketLen))
	if replay.StatusCode != 404 || unknown.StatusCode != 404 || string(replayBody) != string(unknownBody) {
		t.Fatalf("重放 = %d %q，未知 = %d %q", replay.StatusCode, replayBody, unknown.StatusCode, unknownBody)
	}
	for _, bad := range []string{"short", strings.Repeat("!", fileTicketLen), strings.Repeat("A", fileTicketLen+1)} {
		if r, b, _ := f.get(t, "/files/dl/"+bad); r.StatusCode != 404 || string(b) != string(unknownBody) {
			t.Fatalf("无效票据 %q 应得到同样的 404: %d %q", bad, r.StatusCode, b)
		}
	}

	// 审计：签发与下载各一条；任何审计字段与库里的任何记录都不含票据或内容。
	pulls, downloads := f.auditsOf(t, EvFilePull), f.auditsOf(t, EvFileDownload)
	if len(pulls) != 1 || len(downloads) != 1 || downloads[0].Result != ResultOK || downloads[0].MachineCode != f.a.code ||
		!strings.Contains(downloads[0].Detail, "bytes=2109497") || downloads[0].Actor != cred.Name {
		t.Fatalf("审计 pull=%+v download=%+v", pulls, downloads)
	}
	ticket := strings.TrimPrefix(pull.URL, "/files/dl/")
	blob, _ := json.Marshal(f.audits(t))
	for _, secret := range []string{ticket, string(body[:64])} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("审计泄露了票据或内容")
		}
	}
}

func TestFilesTicketExpiresAndIsRevoked(t *testing.T) {
	f := newFilesFixture(t, withFakeClock())
	f.fs.put("/srv/data/a.txt", []byte("hello world"))
	cred := f.newCred(t, nil)

	// 过期
	pull := f.prepare(t, cred, "/srv/data/a.txt")
	f.env.clock.Advance(121 * time.Second)
	if r, _, _ := f.get(t, pull.URL); r.StatusCode != 404 {
		t.Fatalf("过期的票据应是 404: %d", r.StatusCode)
	}
	// 未过期（119 秒）仍可用
	pull = f.prepare(t, cred, "/srv/data/a.txt")
	f.env.clock.Advance(119 * time.Second)
	if r, b, _ := f.get(t, pull.URL); r.StatusCode != 200 || string(b) != "hello world" {
		t.Fatalf("119 秒内应可用: %d %q", r.StatusCode, b)
	}

	// 令牌被修改 / 删除 / 重新生成：名下的票据立即作废，别的令牌不受影响。
	other := f.newCred(t, func(c *model.MCPCredential) { c.Name = "other" })
	keep := f.prepare(t, other, "/srv/data/a.txt")
	for _, reason := range []string{"updated", "deleted", "regenerated"} {
		p := f.prepare(t, cred, "/srv/data/a.txt")
		f.env.hub.MCPCredentialChanged(cred.ID, reason)
		if r, _, _ := f.get(t, p.URL); r.StatusCode != 404 {
			t.Fatalf("%s 之后票据应作废: %d", reason, r.StatusCode)
		}
	}
	if r, b, _ := f.get(t, keep.URL); r.StatusCode != 200 || string(b) != "hello world" {
		t.Fatalf("别的令牌的票据不应受影响: %d", r.StatusCode)
	}
}

// 票据只是「曾经被授权」的证据，下载时所有闸门都要重新核对。
func TestFilesDownloadRechecksEveryGate(t *testing.T) {
	f := newFilesFixture(t)
	f.fs.put("/srv/data/a.txt", []byte("secret-ish"))
	f.fs.put("/srv/other/b.txt", []byte("elsewhere"))
	ctx := context.Background()

	check := func(name string, mutate func(cred *model.MCPCredential), restore func(cred *model.MCPCredential)) {
		t.Run(name, func(t *testing.T) {
			cred := f.newCred(t, func(c *model.MCPCredential) {
				c.TerminalLimits.AllowedPaths = []string{"/srv/data", "/srv/other"}
			})
			pull := f.prepare(t, cred, "/srv/data/a.txt")
			mutate(cred)
			r, b, _ := f.get(t, pull.URL)
			if r.StatusCode != 403 || strings.Contains(string(b), "secret-ish") {
				t.Fatalf("闸门关闭后下载应被拒绝: %d %q", r.StatusCode, b)
			}
			if restore != nil {
				restore(cred)
			}
			// 被拒绝的下载也消耗了票据。
			if r, _, _ := f.get(t, pull.URL); r.StatusCode != 404 {
				t.Fatalf("票据应已作废: %d", r.StatusCode)
			}
			fails := 0
			for _, l := range f.auditsOf(t, EvFileDownload) {
				if l.Result == ResultFail {
					fails++
				}
			}
			if fails == 0 {
				t.Fatal("被拒绝的下载应写审计")
			}
		})
	}

	check("令牌被停用（绕过 API 钩子，只改库）", func(c *model.MCPCredential) {
		if _, err := f.env.store.UpdateCredential(ctx, c.ID, map[string]any{"is_active": false}); err != nil {
			t.Fatal(err)
		}
	}, nil)
	check("令牌收回文件权限", func(c *model.MCPCredential) {
		if _, err := f.env.store.UpdateCredential(ctx, c.ID, map[string]any{"permissions": model.Permissions{Terminal: true}}); err != nil {
			t.Fatal(err)
		}
	}, nil)
	check("令牌的允许目录被收窄到不含该文件", func(c *model.MCPCredential) {
		if _, err := f.env.store.UpdateCredential(ctx, c.ID, map[string]any{"terminal_limits": map[string]any{"allowed_paths": []any{"/srv/other"}}}); err != nil {
			t.Fatal(err)
		}
	}, nil)
	check("令牌被移出主机授权", func(c *model.MCPCredential) {
		if _, err := f.env.store.UpdateCredential(ctx, c.ID, map[string]any{"terminal_hosts": []any{"RM-AAAA-BBBB-CCCC-DDDD"}}); err != nil {
			t.Fatal(err)
		}
	}, nil)
	check("主机收回模型访问", func(*model.MCPCredential) {
		f.setModel(t, map[string]any{"allow_model": false})
	}, func(*model.MCPCredential) { f.setModel(t, map[string]any{"allow_model": true}) })
	check("主机执行模式降为 approve", func(*model.MCPCredential) {
		f.setModel(t, map[string]any{"exec_mode": "approve"})
	}, func(*model.MCPCredential) { f.setModel(t, map[string]any{"exec_mode": "auto"}) })
	check("全局暂停", func(*model.MCPCredential) {
		f.env.call("POST", "/api/v1/terminal/exec/pause", map[string]any{"scope": "all"}, "Authorization", humanAuth(testUser))
	}, func(*model.MCPCredential) {
		f.env.call("POST", "/api/v1/terminal/exec/resume", map[string]any{"scope": "all"}, f.adm...)
	})
	check("本机一票否决", func(*model.MCPCredential) {
		f.sim.reportHolder(true, true)
		eventually(t, 2*time.Second, "否决生效", func() bool {
			_, denied := f.env.hub.onlineAgent(f.a.code).filesSupport()
			return denied
		})
	}, func(*model.MCPCredential) {
		f.sim.reportHolder(false, true)
		eventually(t, 2*time.Second, "否决撤销", func() bool {
			_, denied := f.env.hub.onlineAgent(f.a.code).filesSupport()
			return !denied
		})
	})

	t.Run("文件在签发后被换成了别的东西", func(t *testing.T) {
		cred := f.newCred(t, nil)
		pull := f.prepare(t, cred, "/srv/data/a.txt")
		f.fs.put("/srv/data/a.txt", []byte("secret-ish but longer now"))
		defer f.fs.put("/srv/data/a.txt", []byte("secret-ish"))
		if r, b, _ := f.get(t, pull.URL); r.StatusCode != 409 || strings.Contains(string(b), "secret-ish") {
			t.Fatalf("大小变化应是 409: %d %q", r.StatusCode, b)
		}
	})
}

func TestFilesDownloadDisabledAfterFilesTurnedOff(t *testing.T) {
	f := newFilesFixture(t)
	f.fs.put("/srv/data/a.txt", []byte("x"))
	cred := f.newCred(t, nil)
	pull := f.prepare(t, cred, "/srv/data/a.txt")
	f.svc.enabled = false // 模拟「关闭开关」（开关本身只在启动时读取，这里直接验证端点的行为）
	if r, _, _ := f.get(t, pull.URL); r.StatusCode != 404 {
		t.Fatalf("文件能力关闭后下载端点应是 404: %d", r.StatusCode)
	}
}

func TestFilesDownloadConcurrencyCaps(t *testing.T) {
	f := newFilesFixture(t)
	f.fs.put("/srv/data/a.txt", randomBytes(100_000))
	cred := f.newCred(t, nil)
	hold := make(chan struct{})
	started := make(chan struct{}, 8)
	f.fs.mu.Lock()
	f.fs.holdRead, f.fs.readStarted = hold, started
	f.fs.mu.Unlock()

	p1, p2, p3 := f.prepare(t, cred, "/srv/data/a.txt"), f.prepare(t, cred, "/srv/data/a.txt"), f.prepare(t, cred, "/srv/data/a.txt")
	var wg sync.WaitGroup
	results := make([]int, 2)
	for i, p := range []termexec.FilePullResult{p1, p2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _, _ := f.get(t, p.URL)
			if r != nil {
				results[i] = r.StatusCode
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("两个下载没有同时进行")
		}
	}
	// 同一台主机已有 2 个下载在进行：第 3 个被限流，且它的票据没有被烧掉。
	r, _, _ := f.get(t, p3.URL)
	if r.StatusCode != 503 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("超出每主机下载上限应是 503: %d", r.StatusCode)
	}
	close(hold)
	wg.Wait()
	if results[0] != 200 || results[1] != 200 {
		t.Fatalf("在进行的下载应完成: %v", results)
	}
	if r, b, _ := f.get(t, p3.URL); r.StatusCode != 200 || len(b) != 100_000 {
		t.Fatalf("被限流的票据之后仍可用: %d", r.StatusCode)
	}
}

func TestFilesDownloadAbortsWhenTheHostMisbehaves(t *testing.T) {
	t.Run("中途读取失败：连接被中止，客户端看到长度不符", func(t *testing.T) {
		f := newFilesFixture(t)
		f.fs.put("/srv/data/a.bin", randomBytes(2*1024*1024+5))
		cred := f.newCred(t, nil)
		f.fs.mu.Lock()
		f.fs.cutAt = 1 << 20
		f.fs.mu.Unlock()
		pull := f.prepare(t, cred, "/srv/data/a.bin")
		_, b, err := f.get(t, pull.URL)
		if err == nil || len(b) >= 2*1024*1024 {
			t.Fatalf("中途失败应让客户端读到不完整的响应: err=%v got=%d", err, len(b))
		}
		eventually(t, 2*time.Second, "下载失败审计", func() bool {
			for _, l := range f.auditsOf(t, EvFileDownload) {
				if l.Result == ResultFail && strings.Contains(l.Detail, "outcome=read") {
					return true
				}
			}
			return false
		})
	})
	t.Run("数据帧偏移不连续：视为协议错误，不输出任何字节", func(t *testing.T) {
		f := newFilesFixture(t)
		f.fs.put("/srv/data/a.bin", randomBytes(70_000))
		cred := f.newCred(t, nil)
		f.fs.mu.Lock()
		f.fs.badOffset = true
		f.fs.mu.Unlock()
		pull := f.prepare(t, cred, "/srv/data/a.bin")
		_, b, _ := f.get(t, pull.URL)
		if len(b) != 0 {
			t.Fatalf("协议错误时不应输出内容: %d 字节", len(b))
		}
	})
}

// ---------- 写入（push_url） ----------

func TestFilesPushURL(t *testing.T) {
	f := newFilesFixture(t)
	cred := f.newCred(t, nil)
	f.fs.pushNote = "v1"
	url := "https://files.example.test/pkg/app.tar.gz?sig=SECRETSIG&expires=1"
	want := sha256Hex([]byte("downloaded:v1"))

	res, err := f.files(t, cred, termexec.FileRequest{Action: "push_url", Path: "/srv/data/app.tar.gz", URL: url, SHA256: strings.ToUpper(want), Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	push := res.(termexec.FilePushResult)
	if push.SHA256 != want || push.Size != int64(len("downloaded:v1")) || push.Status != 200 || push.Source != "https://files.example.test" || push.Redirects != 1 || push.Path != "/srv/data/app.tar.gz" {
		t.Fatalf("push 结果 = %+v", push)
	}
	reqs := f.fs.requests(termproto.FileOpPushURL)
	if len(reqs) != 1 || reqs[0].URL != url || reqs[0].SHA256 != want || !reqs[0].Overwrite || reqs[0].Max != termproto.MaxFilePush ||
		len(reqs[0].Roots) != 1 || reqs[0].Roots[0] != "/srv/data" {
		t.Fatalf("下发的请求 = %+v", reqs)
	}

	// 审计里有路径与来源主机，没有网址的路径与查询串（里面常有签名）。
	pushes := f.auditsOf(t, EvFilePush)
	if len(pushes) != 1 || pushes[0].Result != ResultOK || !strings.Contains(pushes[0].Detail, "src=https://files.example.test") || !strings.Contains(pushes[0].Detail, want[:12]) {
		t.Fatalf("file_push 审计 = %+v", pushes)
	}
	blob, _ := json.Marshal(f.audits(t))
	for _, secret := range []string{"SECRETSIG", "pkg/app.tar.gz"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("审计泄露了网址里的 %q", secret)
		}
	}

	// 越界 / 网址被拒 / 摘要不符 / 已存在：稳定错误码，失败写审计。
	_, err = f.files(t, cred, termexec.FileRequest{Action: "push_url", Path: "/etc/cron.d/x", URL: url})
	wantCode(t, err, termexec.CodePathDenied)
	for code, want := range map[string]string{
		termproto.FileErrURLBlocked:   termproto.FileErrURLBlocked,
		termproto.FileErrHashMismatch: termproto.FileErrHashMismatch,
		termproto.FileErrExists:       termproto.FileErrExists,
		termproto.FileErrTooLarge:     termproto.FileErrTooLarge,
		termproto.FileErrDownload:     termproto.FileErrDownload,
	} {
		f.fs.mu.Lock()
		f.fs.failOp[termproto.FileOpPushURL] = code
		f.fs.mu.Unlock()
		_, err := f.files(t, cred, termexec.FileRequest{Action: "push_url", Path: "/srv/data/y", URL: url})
		wantCode(t, err, want)
	}
	failed := 0
	for _, l := range f.auditsOf(t, EvFilePush) {
		if l.Result == ResultFail {
			failed++
		}
	}
	if failed < 5 {
		t.Fatalf("失败的 push 应写审计: %d", failed)
	}
}

func TestFilesPerTokenConcurrencyCap(t *testing.T) {
	overrideGlobal(t, &fileCallTimeout, 2*time.Second) // 要在建环境之前：改包级变量的测试保持串行
	f := newFilesFixture(t)
	f.fs.put("/srv/data/a", []byte("x"))
	cred := f.newCred(t, nil)
	f.fs.mu.Lock()
	f.fs.silent = true // 请求都挂着不回，占住名额
	f.fs.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < fileMaxInflightPerCred; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/srv/data/a"})
		}()
	}
	eventually(t, 2*time.Second, "名额被占满", func() bool {
		f.svc.mu.Lock()
		defer f.svc.mu.Unlock()
		return f.svc.perCred[cred.ID] == fileMaxInflightPerCred
	})
	_, err := f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/srv/data/a"})
	wantCode(t, err, termexec.CodeBusy)
	wg.Wait()
	// 名额被释放
	f.fs.mu.Lock()
	f.fs.silent = false
	f.fs.mu.Unlock()
	if _, err := f.files(t, cred, termexec.FileRequest{Action: "stat", Path: "/srv/data/a"}); err != nil {
		t.Fatalf("名额应已释放: %v", err)
	}
}

// ---------- terminal_targets 的文件提示与状态 ----------

func TestFilesTargetsAndStatus(t *testing.T) {
	f := newFilesFixture(t)
	cred := f.newCred(t, nil)
	res, err := f.env.hub.Exec().Targets(context.Background(), callerOf(cred), "")
	if err != nil {
		t.Fatal(err)
	}
	tr := res.(*termexec.TargetsResult)
	if len(tr.Targets) != 1 || !tr.Targets[0].Files || tr.Targets[0].FilesBlocked != "" ||
		tr.Limits == nil || len(tr.Limits.AllowedPaths) != 1 || tr.Limits.AllowedPaths[0] != "/srv/data" {
		t.Fatalf("targets = %+v limits=%+v", tr.Targets, tr.Limits)
	}

	// 不带文件权限的令牌看不到任何文件字段。
	plain := f.newCred(t, func(c *model.MCPCredential) {
		c.Permissions = model.Permissions{Terminal: true}
		c.TerminalLimits = model.TerminalLimits{}
	})
	res, _ = f.env.hub.Exec().Targets(context.Background(), callerOf(plain), "")
	if t0 := res.(*termexec.TargetsResult).Targets[0]; t0.Files || t0.FilesBlocked != "" {
		t.Fatalf("不带文件权限的令牌不应看到文件字段: %+v", t0)
	}

	// 只带文件权限的令牌：可以看主机清单（它需要知道主机与允许目录），但执行侧一律不可用，且不能执行命令。
	filesOnly := f.newCred(t, func(c *model.MCPCredential) { c.Permissions = model.Permissions{TerminalFiles: true} })
	res, err = f.env.hub.Exec().Targets(context.Background(), callerOf(filesOnly), "")
	if err != nil {
		t.Fatalf("只有文件权限的令牌应能读取主机清单: %v", err)
	}
	fo := res.(*termexec.TargetsResult)
	if len(fo.Targets) != 1 || !fo.Targets[0].Files || fo.Targets[0].Usable || fo.Targets[0].Blocked != termexec.CodeNotAllowed ||
		fo.Limits == nil || len(fo.Limits.AllowedPaths) != 1 || !strings.Contains(fo.Hint, "只有文件权限") {
		t.Fatalf("文件档 targets = %+v limits=%+v hint=%q", fo.Targets, fo.Limits, fo.Hint)
	}
	_, err = f.env.hub.Exec().Exec(context.Background(), callerOf(filesOnly), termexec.ExecRequest{Target: f.a.code, Command: "echo hi"})
	wantCode(t, err, termexec.CodeNotAllowed)
	// 有执行权限的令牌提示里带上文件字段的含义；没有文件权限的不带。
	if !strings.Contains(tr.Hint, "files=true") {
		t.Fatalf("执行 + 文件令牌的提示应解释 files 字段: %q", tr.Hint)
	}

	// 本机否决时：Files=false，并给出原因码。
	f.sim.reportHolder(true, true)
	eventually(t, 2*time.Second, "否决生效", func() bool {
		_, denied := f.env.hub.onlineAgent(f.a.code).filesSupport()
		return denied
	})
	res, _ = f.env.hub.Exec().Targets(context.Background(), callerOf(cred), "")
	if t0 := res.(*termexec.TargetsResult).Targets[0]; t0.Files || t0.FilesBlocked != termexec.CodeHostDenied {
		t.Fatalf("本机否决时应提示原因: %+v", t0)
	}

	st := f.env.call("GET", "/api/v1/terminal/exec/status", nil, "Authorization", humanAuth(testUser))
	if st.Body["files_enabled"] != true || st.Body["enabled"] != true {
		t.Fatalf("status = %s", st.Raw)
	}
	off := newTestEnv(t, withExec())
	if st := off.call("GET", "/api/v1/terminal/exec/status", nil, "Authorization", humanAuth(testUser)); st.Body["files_enabled"] != false {
		t.Fatalf("未开启时 files_enabled 应为 false: %s", st.Raw)
	}
}

// ---------- 票据与辅助函数 ----------

func TestFileTicketStore(t *testing.T) {
	clock := newTestClock()
	ts := newFileTickets(clock.Now)

	tok, ok := ts.issue(fileTicket{CredentialID: 1, Machine: "m", Path: "/a", Roots: []string{"/"}})
	if !ok || len(tok) != fileTicketLen {
		t.Fatalf("issue = %q %v", tok, ok)
	}
	if tk := ts.take(tok); tk == nil || tk.Path != "/a" || tk.Expires.Sub(clock.Now()) != fileTicketTTL {
		t.Fatalf("take = %+v", tk)
	}
	if ts.take(tok) != nil {
		t.Fatal("票据只能用一次")
	}

	// 内存里不存明文票据。
	tok, _ = ts.issue(fileTicket{CredentialID: 1})
	ts.mu.Lock()
	for k := range ts.m {
		if strings.Contains(string(k[:]), tok) {
			t.Fatal("不应保存明文票据")
		}
	}
	ts.mu.Unlock()
	clock.Advance(fileTicketTTL)
	if ts.take(tok) != nil {
		t.Fatal("到期即作废（边界值也算过期）")
	}

	// 上限：每令牌 16 张、总共 256 张；过期的不占名额。
	for i := 0; i < fileTicketPerCred; i++ {
		if _, ok := ts.issue(fileTicket{CredentialID: 5}); !ok {
			t.Fatalf("第 %d 张应成功", i)
		}
	}
	if _, ok := ts.issue(fileTicket{CredentialID: 5}); ok {
		t.Fatal("超过每令牌上限应失败")
	}
	if _, ok := ts.issue(fileTicket{CredentialID: 6}); !ok {
		t.Fatal("别的令牌不受影响")
	}
	if n := ts.revokeCredential(5); n != fileTicketPerCred {
		t.Fatalf("revoke = %d", n)
	}
	clock.Advance(time.Hour)
	for i := 0; i < fileTicketMax; i++ {
		if _, ok := ts.issue(fileTicket{CredentialID: uint(100 + i/10)}); !ok {
			t.Fatalf("第 %d 张应成功", i)
		}
	}
	if _, ok := ts.issue(fileTicket{CredentialID: 9999}); ok {
		t.Fatal("超过总量上限应失败")
	}
	clock.Advance(time.Hour)
	if _, ok := ts.issue(fileTicket{CredentialID: 9999}); !ok {
		t.Fatal("过期的票据应被清理，释放名额")
	}

	// restore 只放回未过期的票据。
	tok, _ = ts.issue(fileTicket{CredentialID: 7, Path: "/r"})
	tk := ts.take(tok)
	ts.restore(tok, tk)
	if again := ts.take(tok); again == nil || again.Path != "/r" {
		t.Fatal("restore 之后应还能取到")
	}
	ts.restore(tok, &fileTicket{Expires: clock.Now().Add(-time.Second)})
	if ts.take(tok) != nil {
		t.Fatal("过期的票据不应被放回")
	}

	// 无效输入：长度、字符集、空串。
	for _, bad := range []string{"", "x", strings.Repeat("A", fileTicketLen-1), strings.Repeat("A", fileTicketLen+1), strings.Repeat("?", fileTicketLen)} {
		if ts.take(bad) != nil {
			t.Fatalf("%q 应无效", bad)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	cases := map[string]string{
		"/srv/data/report.pdf":              `attachment; filename="report.pdf"`,
		`C:\Users\dev\a b.txt`:              `attachment; filename="a b.txt"`,
		"/srv/data/":                        `attachment; filename="download"`,
		`/srv/x";evil=1.txt`:                `attachment; filename="x__evil=1.txt"; filename*=UTF-8''x%22%3Bevil%3D1.txt`,
		"/srv/日本語.txt":                      `attachment; filename="___.txt"; filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E.txt`,
		"/srv/line\r\nbreak.txt":            `attachment; filename="line__break.txt"; filename*=UTF-8''line%0D%0Abreak.txt`,
		"/srv/100%.txt":                     `attachment; filename="100_.txt"; filename*=UTF-8''100%25.txt`,
		"/srv/" + string([]byte{0xff, 'a'}): `attachment; filename="download"`,
	}
	for in, want := range cases {
		if got := contentDisposition(in); got != want {
			t.Errorf("contentDisposition(%q)\n got  %s\n want %s", in, got, want)
		}
	}
	long := contentDisposition("/srv/" + strings.Repeat("a", 500) + ".txt")
	if len(long) > 500 || strings.ContainsAny(long, "\r\n") {
		t.Errorf("过长的文件名应被截断: %d", len(long))
	}
}

func TestCredentialStillAllowsFiles(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	base := func() *model.MCPCredential {
		return &model.MCPCredential{
			IsActive: true, Permissions: model.Permissions{TerminalFiles: true},
			TerminalHosts: model.StringList{"m1"}, TerminalLimits: model.TerminalLimits{AllowedPaths: []string{"/srv"}},
		}
	}
	if !credentialStillAllowsFiles(base(), "m1", now) {
		t.Fatal("基线应允许")
	}
	mut := map[string]func(c *model.MCPCredential){
		"停用":     func(c *model.MCPCredential) { c.IsActive = false },
		"过期":     func(c *model.MCPCredential) { c.ExpiresAt = &past },
		"收回文件权限": func(c *model.MCPCredential) { c.Permissions.TerminalFiles = false },
		"移出主机授权": func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"m2"} },
		"没有允许目录": func(c *model.MCPCredential) { c.TerminalLimits.AllowedPaths = nil },
		"不存在的令牌": nil,
	}
	for name, m := range mut {
		c := base()
		if m != nil {
			m(c)
		} else {
			c = nil
		}
		if credentialStillAllowsFiles(c, "m1", now) {
			t.Errorf("%s 后不应再允许", name)
		}
	}
	c := base()
	c.ExpiresAt = &future
	if !credentialStillAllowsFiles(c, "m1", now) {
		t.Fatal("未到期应允许")
	}
}
