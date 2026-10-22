//go:build e2e && !windows

package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 文件能力（terminal_files）的真实进程端到端：Hub、会话守护进程、Agent 是三个独立的操作系统进程，
// 模型一侧用真实的 MCP HTTP 调用，下载一侧用不带任何凭据的普通 HTTP 请求（和模型拿到下载地址后的做法一致）。
// 主机上的文件、符号链接与权限位都是真实的；push_url 的来源是本进程里起的 HTTP 服务。

func filesOn() []string {
	return []string{"RELAYMESH_TERMINAL_EXEC=on", "RELAYMESH_TERMINAL_FILES=on"}
}

type filesLabOpts struct {
	hubEnv     []string
	holderArgs []string
	agentArgs  []string
}

type filesLab struct {
	*execLab
	opts    filesLabOpts
	home    string // Agent 进程的 HOME；令牌的允许目录 ~/work 在它下面
	root    string // home/work：令牌允许的目录
	private string // home/private：同一账户可读、但不在允许目录内
	credID  int
	files   *mcpClient // 绑定本主机、允许目录为 ~/work、模式上限 auto 的文件令牌
}

// newFilesLab 起一个完整环境并发出一把只带文件权限的令牌。
func newFilesLab(t *testing.T, o filesLabOpts) *filesLab {
	t.Helper()
	hubBin, agentBin := binaries(t)
	h := startHub(t, hubBin, o.hubEnv...)
	d := newHostDirs(t)
	hp := startHolder(t, agentBin, d, o.holderArgs...)

	home, err := os.MkdirTemp("", "rmfh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if home, err = filepath.EvalSymlinks(home); err != nil {
		t.Fatal(err)
	}
	l := &filesLab{
		execLab: &execLab{t: t, h: h, agentBin: agentBin, d: d, hp: hp},
		opts:    o, home: home,
		root: filepath.Join(home, "work"), private: filepath.Join(home, "private"),
	}
	for _, p := range []string{l.root, l.private} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	l.launchAgent("agent")
	l.code = h.api.enrollOnly()
	waitHost(t, h, l.code, "主机上线且守护进程就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	l.setHostMode("auto")
	tok, id := l.newFileToken("files-e2e", []string{"~/work"}, "auto")
	l.credID = id
	l.files = l.client(tok)
	return l
}

// launchAgent 以本实验的 HOME 启动 Agent（~ 在主机上展开成这个目录）。
func (l *filesLab) launchAgent(name string) {
	l.t.Helper()
	args := []string{"run", "--server", l.h.agentURL(), "--pin", l.h.pin, "--data-dir", l.d.data, "--config-dir", l.d.cfg,
		"--log-file", filepath.Join(l.d.logs, name+".log"), "--holder", "external"}
	l.ag = startProc(l.t, name, l.agentBin, []string{"HOME=" + l.home}, append(args, l.opts.agentArgs...)...)
}

// relaunchAgent 换一组 Agent 参数重新启动（主机身份不变）。
func (l *filesLab) relaunchAgent(name string, agentArgs ...string) {
	l.t.Helper()
	l.ag.term(10 * time.Second)
	waitHost(l.t, l.h, l.code, "旧 Agent 的连接已断开", func(v terminal.HostView) bool { return !v.Online })
	l.opts.agentArgs = agentArgs
	l.launchAgent(name)
	waitHost(l.t, l.h, l.code, "主机重新上线", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
}

func (l *filesLab) setHostMode(mode string) {
	l.t.Helper()
	if c := l.h.api.call("POST", "/hosts/"+l.code+"/model", map[string]any{"allow_model": true, "exec_mode": mode}, nil); c != 200 {
		l.t.Fatalf("设置主机执行模式 %s 失败: HTTP %d", mode, c)
	}
}

// newFileToken 签发一把只带文件权限、绑定本主机的令牌；返回令牌与凭据 ID。
func (l *filesLab) newFileToken(name string, paths []string, modeCap string) (string, int) {
	l.t.Helper()
	body := map[string]any{
		"name": name, "permissions": map[string]any{"terminal_files": true}, "terminal_hosts": []string{l.code},
		"terminal_limits": map[string]any{"allowed_paths": paths},
	}
	if modeCap != "" {
		body["terminal_mode_cap"] = modeCap
	}
	var out struct {
		Token      string `json:"token"`
		Credential struct {
			ID int `json:"id"`
		} `json:"credential"`
	}
	if c := l.h.api.raw("POST", "/api/v1/credentials", body, &out, true); c != 200 || out.Token == "" {
		l.t.Fatalf("创建文件令牌失败: HTTP %d", c)
	}
	return out.Token, out.Credential.ID
}

func (l *filesLab) client(token string) *mcpClient {
	return &mcpClient{t: l.t, base: l.h.base(), token: token, hc: &http.Client{Timeout: 120 * time.Second}}
}

// put 在允许目录里写一个文件（0600）；返回绝对路径。
func (l *filesLab) put(rel string, data []byte) string { return l.putIn(l.root, rel, data) }

func (l *filesLab) putIn(dir, rel string, data []byte) string {
	l.t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		l.t.Fatal(err)
	}
	return p
}

type auditEntry struct {
	Event  string `json:"event"`
	Result string `json:"result"`
	Actor  string `json:"actor"`
	Detail string `json:"detail"`
}

func (l *filesLab) audit() []auditEntry {
	l.t.Helper()
	var out struct {
		Logs []auditEntry `json:"logs"`
	}
	if c := l.h.api.raw("GET", "/api/v1/terminal/audit?limit=500", nil, &out, true); c != 200 {
		l.t.Fatalf("读取审计: HTTP %d", c)
	}
	return out.Logs
}

func (l *filesLab) auditCount(event string) int {
	n := 0
	for _, e := range l.audit() {
		if e.Event == event {
			n++
		}
	}
	return n
}

// requireNoLeak 要求审计里没有出现任何一个禁止出现的字符串（票据、文件内容、网址的路径与查询串）。
func (l *filesLab) requireNoLeak(what string, banned ...string) {
	l.t.Helper()
	for _, e := range l.audit() {
		for _, b := range banned {
			if b != "" && strings.Contains(e.Detail+e.Actor, b) {
				l.t.Fatalf("%s：审计事件 %s 泄露了不应出现的内容: %q", what, e.Event, e.Detail)
			}
		}
	}
	for _, b := range banned {
		if b == "" {
			continue
		}
		if where, found := l.dbContains(b); found {
			l.t.Fatalf("%s：Hub 的 %s 里出现了不应落地的内容", what, where)
		}
	}
}

// file 调用 terminal_files。
func (m *mcpClient) file(action, path string, extra map[string]any) (toolRes, bool) {
	m.t.Helper()
	args := map[string]any{"action": action, "path": path}
	for k, v := range extra {
		args[k] = v
	}
	return m.tool("terminal_files", args)
}

// mustOK 要求调用成功。
func (m *mcpClient) mustOK(action, path string, extra map[string]any) toolRes {
	m.t.Helper()
	res, isErr := m.file(action, path, extra)
	if isErr {
		m.t.Fatalf("terminal_files %s %s 被拒绝: %v", action, path, res)
	}
	return res
}

// mustDeny 要求调用被拒绝且错误码属于 codes 之一；返回错误码。
func (m *mcpClient) mustDeny(action, path string, extra map[string]any, codes ...string) string {
	m.t.Helper()
	res, isErr := m.file(action, path, extra)
	if !isErr {
		m.t.Fatalf("terminal_files %s %s 应被拒绝，实际成功: %v", action, path, res)
	}
	got := res.errCode()
	for _, c := range codes {
		if got == c {
			return got
		}
	}
	m.t.Fatalf("terminal_files %s %s 错误码 = %q，期望 %v（%v）", action, path, got, codes, res)
	return ""
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// httpGet 不带任何凭据地取一个网址；错误信息里不带网址（下载地址本身就是凭证）。
func httpGet(t *testing.T, target string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Get(target)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		t.Fatalf("GET 失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func ticketOf(downloadURL string) string { return downloadURL[strings.LastIndex(downloadURL, "/")+1:] }

// listDir 返回目录里的名字（排序后）。
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// ---------- 用例 ----------

// 读取路径：发现、stat、hash、pull_prepare + 一次性下载、统一的 404、审计与日志不泄露。
func TestRealFilesReadPath(t *testing.T) {
	l := newFilesLab(t, filesLabOpts{hubEnv: filesOn()})
	m := l.files

	small := []byte("hello files\n")
	smallPath := l.put("a.txt", small)
	big := randBytes(3<<20 + 123) // 多个 1 MiB 读取窗口与几百个 32 KiB 数据帧
	bigPath := l.put("sub/big.bin", big)
	secret := "TOP-SECRET-" + randHex(8)
	l.putIn(l.private, "secret.txt", []byte(secret))

	// 发现：只带文件权限的令牌有 terminal_targets（只读）与 terminal_files，没有执行类工具。
	if got := strings.Join(m.toolNames(), ","); got != "terminal_files,terminal_targets" {
		t.Fatalf("tools/list = %s", got)
	}
	tg, isErr := m.tool("terminal_targets", map[string]any{})
	list, _ := tg["targets"].([]any)
	if isErr || len(list) != 1 {
		t.Fatalf("terminal_targets: %v", tg)
	}
	target := toolRes(list[0].(map[string]any))
	if target.str("machine_code") != l.code || target["files"] != true || target["usable"] != false || target.str("blocked") != "not_allowed" || target.str("mode") != "auto" {
		t.Fatalf("文件档的目标信息不符: %v", target)
	}
	if paths, _ := tg.obj("limits")["allowed_paths"].([]any); len(paths) != 1 || paths[0] != "~/work" {
		t.Fatalf("limits.allowed_paths 不符: %v", tg["limits"])
	}
	if res, isErr := m.tool("terminal_exec", map[string]any{"target": l.code, "command": "id"}); !isErr {
		t.Fatalf("只有文件权限的令牌不应能执行命令: %v", res)
	}

	// stat：绝对路径与 ~ 写法等价；不存在的路径是正常答案；目录有自己的类型。
	st := m.mustOK("stat", smallPath, nil)
	if st["exists"] != true || st.str("type") != "file" || st.num("size") != int64(len(small)) || st.str("path") != smallPath || st.str("mtime") == "" {
		t.Fatalf("stat 结果不符: %v", st)
	}
	if st2 := m.mustOK("stat", "~/work/a.txt", map[string]any{"target": l.code}); st2.num("size") != int64(len(small)) {
		t.Fatalf("~ 写法的 stat 不符: %v", st2)
	}
	if miss := m.mustOK("stat", filepath.Join(l.root, "nope.txt"), nil); miss["exists"] != false {
		t.Fatalf("不存在的路径应是 exists=false: %v", miss)
	}
	if dir := m.mustOK("stat", filepath.Join(l.root, "sub"), nil); dir.str("type") != "dir" {
		t.Fatalf("目录类型不符: %v", dir)
	}

	// hash：大文件的校验值与本地计算一致；对目录给出明确的错误码。
	hs := m.mustOK("hash", bigPath, nil)
	if hs.str("sha256") != sum256(big) || hs.num("size") != int64(len(big)) {
		t.Fatalf("hash 不符: %v", hs)
	}
	m.mustDeny("hash", filepath.Join(l.root, "sub"), nil, "is_directory")

	// pull_prepare：绝对地址、一次性票据；下载不带任何凭据，内容逐字节一致。
	pp := m.mustOK("pull_prepare", bigPath, nil)
	dl := pp.str("url")
	if !strings.HasPrefix(dl, l.h.base()+"/files/dl/") || pp.str("method") != "GET" || pp.num("size") != int64(len(big)) {
		t.Fatalf("pull_prepare 结果不符（地址已省略）: method=%v size=%v", pp["method"], pp["size"])
	}
	if exp := pp.num("expires_in_seconds"); exp <= 0 || exp > 120 {
		t.Fatalf("票据有效期 = %d", exp)
	}
	code, hdr, body := httpGet(t, dl)
	if code != 200 || sum256(body) != sum256(big) || hdr.Get("Content-Length") != strconv.Itoa(len(big)) {
		t.Fatalf("下载不符: HTTP %d len=%d", code, len(body))
	}
	if !strings.Contains(hdr.Get("Content-Disposition"), "big.bin") || !strings.Contains(hdr.Get("Cache-Control"), "no-store") || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("下载响应头不符: %v", hdr)
	}

	// 一次性：重放得到 404；编造的票据得到完全相同的 404（不能借此探测票据是否存在过）。
	rc, _, replay := httpGet(t, dl)
	fc, _, forged := httpGet(t, l.h.base()+"/files/dl/"+base64.RawURLEncoding.EncodeToString(randBytes(32)))
	if rc != http.StatusNotFound || fc != http.StatusNotFound || !bytes.Equal(replay, forged) {
		t.Fatalf("重放 / 编造的票据应得到一致的 404: %d %q / %d %q", rc, lastN(string(replay), 80), fc, lastN(string(forged), 80))
	}
	if bytes.Contains(replay, []byte("big.bin")) || bytes.Contains(replay, big[:32]) {
		t.Fatalf("404 响应不应带任何文件信息")
	}

	// 审计：各类事件都有记录；任何地方都没有票据、文件内容。票据只存在于模型拿到的网址里。
	for _, ev := range []string{terminal.EvFileOp, terminal.EvFilePull, terminal.EvFileDownload} {
		if l.auditCount(ev) == 0 {
			t.Fatalf("审计缺少事件 %s", ev)
		}
	}
	l.requireNoLeak("读取路径", ticketOf(dl), secret, string(small))
	if logBytes, _ := os.ReadFile(l.h.proc.log); !bytes.Contains(logBytes, []byte("/files/dl/***")) {
		t.Fatalf("访问日志里应只留下脱敏后的 /files/dl/***:\n%s", tail(l.h.proc.log, 15))
	}
}

// 边界：越界、符号链接逃逸、前缀欺骗、每把令牌各自的目录、模式、暂停，以及凭据变更对票据与目录的即时生效。
func TestRealFilesBoundaries(t *testing.T) {
	l := newFilesLab(t, filesLabOpts{hubEnv: filesOn()})
	m := l.files

	aPath := l.put("a.txt", []byte("alpha"))
	xPath := l.put("sub/x.txt", []byte("xray"))
	secret := "TOP-SECRET-" + randHex(8)
	secretPath := l.putIn(l.private, "secret.txt", []byte(secret))
	evilPath := l.putIn(l.home+"/work-evil", "y.txt", []byte("evil")) // 与 work 同前缀的兄弟目录
	if err := os.Symlink(secretPath, filepath.Join(l.root, "link-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(l.private, filepath.Join(l.root, "link-dir")); err != nil {
		t.Fatal(err)
	}

	// 越界一律 path_not_allowed，且响应里没有任何被保护文件的信息。
	for name, p := range map[string]string{
		"目录之外":      secretPath,
		"同前缀兄弟目录":   evilPath,
		"经 .. 回到外面": l.root + "/sub/../../private/secret.txt",
		"经符号链接目录":   filepath.Join(l.root, "link-dir", "secret.txt"),
	} {
		for _, action := range []string{"stat", "hash", "pull_prepare"} {
			res, isErr := m.file(action, p, nil)
			if !isErr {
				t.Fatalf("%s：%s 不应成功: %v", name, action, res)
			}
			if code := res.errCode(); code != "path_not_allowed" && code != "bad_path" && code != "bad_request" {
				t.Fatalf("%s：%s 错误码 = %q", name, action, code)
			}
			if strings.Contains(fmt.Sprint(res), secret) || res["exists"] != nil {
				t.Fatalf("%s：%s 的响应泄露了信息: %v", name, action, res)
			}
		}
	}
	// 指向允许目录之外的符号链接本身：不被跟随，拿不到目标的任何内容。
	if st, isErr := m.file("stat", filepath.Join(l.root, "link-file"), nil); !isErr && st.str("type") != "symlink" {
		t.Fatalf("符号链接的 stat 应只描述链接本身: %v", st)
	}
	m.mustDeny("hash", filepath.Join(l.root, "link-file"), nil, "is_symlink", "path_not_allowed")
	m.mustDeny("pull_prepare", filepath.Join(l.root, "link-file"), nil, "is_symlink", "path_not_allowed")

	// 参数错误：相对路径、过长路径、未知动作、未知主机。
	m.mustDeny("stat", "a.txt", nil, "bad_path", "bad_request")
	m.mustDeny("stat", "/"+strings.Repeat("a", 2000), nil, "bad_path", "bad_request")
	m.mustDeny("chmod", aPath, nil, "bad_request")
	m.mustDeny("stat", aPath, map[string]any{"target": "no-such-host"}, "host_not_found")

	// 每把令牌各自的允许目录：窄令牌只能看到 ~/work/sub。
	narrowTok, _ := l.newFileToken("narrow", []string{"~/work/sub"}, "auto")
	n := l.client(narrowTok)
	if st := n.mustOK("stat", xPath, nil); st["exists"] != true {
		t.Fatalf("窄令牌应能看到自己目录里的文件: %v", st)
	}
	n.mustDeny("stat", aPath, nil, "path_not_allowed")
	if st := m.mustOK("stat", aPath, nil); st["exists"] != true {
		t.Fatalf("宽令牌不受影响: %v", st)
	}

	// 执行模式：主机改成需批准后，传输被拒（mode_not_allowed），查看不受影响；恢复后传输恢复。
	l.setHostMode("approve")
	m.mustDeny("pull_prepare", aPath, nil, "mode_not_allowed")
	m.mustDeny("push_url", aPath, map[string]any{"url": "https://example.com/x"}, "mode_not_allowed")
	m.mustOK("stat", aPath, nil)
	m.mustOK("hash", aPath, nil)
	l.setHostMode("auto")
	m.mustOK("pull_prepare", aPath, nil)
	// 令牌自己的模式上限同样生效。
	capTok, _ := l.newFileToken("cap-approve", []string{"~/work"}, "approve")
	c := l.client(capTok)
	c.mustDeny("pull_prepare", aPath, nil, "mode_not_allowed")
	c.mustOK("hash", aPath, nil)

	// 暂停：全部文件操作被拒（需要管理员恢复）。
	if c := l.h.api.raw("POST", "/api/v1/terminal/exec/pause", map[string]any{"scope": "all"}, nil, false); c != 200 {
		t.Fatalf("暂停: HTTP %d", c)
	}
	m.mustDeny("stat", aPath, nil, "paused")
	m.mustDeny("pull_prepare", aPath, nil, "paused")
	if c := l.h.api.raw("POST", "/api/v1/terminal/exec/resume", map[string]any{"scope": "all"}, nil, true); c != 200 {
		t.Fatalf("恢复: HTTP %d", c)
	}
	m.mustOK("stat", aPath, nil)

	// 凭据变更即时生效：任何变更都让未使用的票据作废；收窄目录后，已签发的票据即使没被撤销也不能再下载。
	other := m.mustOK("pull_prepare", aPath, nil).str("url")
	if c := l.h.api.raw("PUT", fmt.Sprintf("/api/v1/credentials/%d", l.credID), map[string]any{"note": "changed"}, nil, true); c != 200 {
		t.Fatalf("修改凭据: HTTP %d", c)
	}
	if code, _, _ := httpGet(t, other); code != http.StatusNotFound {
		t.Fatalf("凭据变更后票据应作废，实际 HTTP %d", code)
	}
	fresh := m.mustOK("pull_prepare", aPath, nil).str("url")
	if c := l.h.api.raw("PUT", fmt.Sprintf("/api/v1/credentials/%d", l.credID), map[string]any{"terminal_limits": map[string]any{"allowed_paths": []string{"~/work/sub"}}}, nil, true); c != 200 {
		t.Fatalf("收窄目录: HTTP %d", c)
	}
	if code, _, _ := httpGet(t, fresh); code != http.StatusNotFound {
		t.Fatalf("目录收窄后旧票据不应再能下载，实际 HTTP %d", code)
	}
	m.mustDeny("stat", aPath, nil, "path_not_allowed") // Agent 拿到的是最新的目录，不是缓存
	m.mustOK("stat", xPath, nil)

	// 被拒绝的访问进了审计：原因码、令牌名和被请求的路径（取证用）都在，但没有被保护文件的内容。
	denied := 0
	for _, e := range l.audit() {
		if e.Event == terminal.EvExecDenied && strings.Contains(e.Detail, "tool=files:") && strings.Contains(e.Detail, "reason=path_not_allowed") {
			if !strings.Contains(e.Detail, " path="+l.home) {
				t.Fatalf("越界审计应带上被请求的路径: %q", e.Detail)
			}
			denied++
		}
	}
	if denied == 0 {
		t.Fatalf("越界访问应有审计记录: %+v", l.audit())
	}
	l.requireNoLeak("边界", secret, ticketOf(other), ticketOf(fresh))
}

// push_url：默认拒绝内网 / 回环地址（连接都不会发出），主机拥有者显式放开后才能取，随后的校验、覆盖、大小、清理都在真实进程里走一遍。
func TestRealFilesPushURL(t *testing.T) {
	payload := randBytes(1<<20 + 7)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/blob":
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
		case "/redirect":
			http.Redirect(w, r, "/blob", http.StatusFound)
		case "/huge": // 超过 256 MiB 的流（不带 Content-Length）
			zero := make([]byte, 1<<20)
			for i := 0; i < 257; i++ {
				if _, err := w.Write(zero); err != nil {
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	origin := "http://127.0.0.1:" + port

	l := newFilesLab(t, filesLabOpts{hubEnv: filesOn()})
	m := l.files
	dst := filepath.Join(l.root, "pushed.bin")

	// 1) 默认策略：回环 / 私网 / 链路本地 / 非 http 的网址一律拒绝，而且请求根本没有发出。
	for name, u := range map[string]string{
		"回环地址":      origin + "/blob",
		"localhost": "http://localhost:" + port + "/blob",
		"IPv6 回环":   "http://[::1]:" + port + "/blob",
		"云元数据地址":    "http://169.254.169.254/latest/meta-data/",
		"私网地址":      "http://10.255.255.1/x",
		"file 协议":   "file:///etc/hostname",
	} {
		m.mustDeny("push_url", dst, map[string]any{"url": u}, "url_blocked", "bad_request")
		if hits.Load() != 0 || l.fileExists(dst) {
			t.Fatalf("%s：默认策略下不应发出请求 / 不应写入（hits=%d）", name, hits.Load())
		}
	}

	// 2) 主机拥有者在本机显式放开私网后才能取。
	l.relaunchAgent("agent2", "--file-url-allow-private")
	ok := m.mustOK("push_url", dst, map[string]any{"url": origin + "/blob?sig=QUERYSECRET", "sha256": sum256(payload)})
	if ok.num("size") != int64(len(payload)) || ok.str("sha256") != sum256(payload) || ok.num("http_status") != 200 || ok.str("source") != origin {
		t.Fatalf("push_url 结果不符: %v", ok)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("主机上的文件内容不符: err=%v len=%d", err, len(got))
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o600 {
		t.Fatalf("新文件权限 = %v，期望 0600", info.Mode().Perm())
	}

	// 3) 已存在需要 overwrite；校验不符时不留任何痕迹，原文件保持不变。
	m.mustDeny("push_url", dst, map[string]any{"url": origin + "/blob"}, "exists")
	m.mustDeny("push_url", dst, map[string]any{"url": origin + "/blob", "overwrite": true, "sha256": strings.Repeat("0", 64)}, "hash_mismatch")
	if after, _ := os.ReadFile(dst); !bytes.Equal(after, payload) {
		t.Fatalf("校验失败后原文件被改动")
	}
	m.mustOK("push_url", dst, map[string]any{"url": origin + "/blob", "overwrite": true})
	fresh := filepath.Join(l.root, "fresh.bin")
	m.mustDeny("push_url", fresh, map[string]any{"url": origin + "/blob", "sha256": strings.Repeat("f", 64)}, "hash_mismatch")
	if l.fileExists(fresh) {
		t.Fatalf("校验失败时不应留下目标文件")
	}

	// 4) 来源出错、重定向、越界目标、不存在的父目录。
	m.mustDeny("push_url", fresh, map[string]any{"url": origin + "/missing"}, "download_failed")
	redir := m.mustOK("push_url", filepath.Join(l.root, "via-redirect.bin"), map[string]any{"url": origin + "/redirect"})
	if redir.num("redirects") != 1 || redir.num("size") != int64(len(payload)) {
		t.Fatalf("重定向结果不符: %v", redir)
	}
	before := hits.Load()
	m.mustDeny("push_url", filepath.Join(l.private, "out.bin"), map[string]any{"url": origin + "/blob"}, "path_not_allowed")
	m.mustDeny("push_url", filepath.Join(l.root, "nodir", "x.bin"), map[string]any{"url": origin + "/blob"}, "not_found")
	if hits.Load() != before {
		t.Fatalf("目标路径不合法时不应去下载（先校验路径，再动网络）")
	}
	if l.fileExists(filepath.Join(l.private, "out.bin")) || l.fileExists(filepath.Join(l.root, "nodir")) {
		t.Fatalf("越界 / 缺失父目录的写入不应有任何落地")
	}

	// 5) 大小上限：超过 256 MiB 的流被中止，目标与临时文件都不留。
	m.mustDeny("push_url", filepath.Join(l.root, "huge.bin"), map[string]any{"url": origin + "/huge"}, "too_large")
	if names := listDir(t, l.root); !sameNames(names, []string{"pushed.bin", "via-redirect.bin"}) {
		t.Fatalf("失败的写入后目录里不应有多余文件（含临时文件）: %v", names)
	}

	// 6) 审计：写入有记录，但不含网址的路径 / 查询串，也不含文件内容。
	if l.auditCount(terminal.EvFilePush) == 0 {
		t.Fatalf("审计缺少 %s", terminal.EvFilePush)
	}
	l.requireNoLeak("push_url", "QUERYSECRET", "/blob", string(payload[:64]))

	// 7) 需批准模式下不会去下载。
	before = hits.Load()
	l.setHostMode("approve")
	m.mustDeny("push_url", filepath.Join(l.root, "later.bin"), map[string]any{"url": origin + "/blob"}, "mode_not_allowed")
	if hits.Load() != before {
		t.Fatalf("模式不允许时不应去下载")
	}
}

func (l *filesLab) fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, "\x00") == strings.Join(y, "\x00")
}

// 总闸与本机一票否决：文件开关默认关闭；开着也得执行总开关同时开；主机本机否决（--deny-model-exec）一并拦住文件。
func TestRealFilesGatesAndVeto(t *testing.T) {
	// 文件开关没开（只开了模型执行）：工具存在，但一律 files_disabled；下载端点没有任何票据可用。
	t.Run("文件开关默认关闭", func(t *testing.T) {
		l := newFilesLab(t, filesLabOpts{hubEnv: execOn()})
		p := l.put("a.txt", []byte("alpha"))
		l.files.mustDeny("stat", p, nil, "files_disabled")
		l.files.mustDeny("pull_prepare", p, nil, "files_disabled")
		l.files.mustDeny("push_url", p, map[string]any{"url": "https://example.com/x"}, "files_disabled")
		var st struct {
			Enabled      bool `json:"enabled"`
			FilesEnabled bool `json:"files_enabled"`
		}
		if c := l.h.api.raw("GET", "/api/v1/terminal/exec/status", nil, &st, true); c != 200 || !st.Enabled || st.FilesEnabled {
			t.Fatalf("exec/status: HTTP %d %+v", c, st)
		}
		if code, _, _ := httpGet(t, l.h.base()+"/files/dl/"+base64.RawURLEncoding.EncodeToString(randBytes(32))); code != http.StatusNotFound {
			t.Fatalf("文件开关关闭时下载端点应 404，实际 %d", code)
		}
		tg, _ := l.files.tool("terminal_targets", map[string]any{})
		if list, _ := tg["targets"].([]any); len(list) != 1 || toolRes(list[0].(map[string]any))["files"] != nil {
			t.Fatalf("文件开关关闭时目标列表不应出现 files 字段: %v", tg)
		}
	})

	// 只开文件、不开模型执行：同样不可用（文件能力依附于执行总开关）。
	t.Run("依附于执行总开关", func(t *testing.T) {
		l := newFilesLab(t, filesLabOpts{hubEnv: []string{"RELAYMESH_TERMINAL_FILES=on"}})
		l.files.mustDeny("stat", l.put("a.txt", []byte("alpha")), nil, "files_disabled", "disabled")
	})

	// 主机本机的一票否决：文件操作与命令执行一样被拦在 Hub 之外，且目标列表如实说明原因。
	t.Run("本机一票否决", func(t *testing.T) {
		l := newFilesLab(t, filesLabOpts{hubEnv: filesOn(), holderArgs: []string{"--deny-model-exec"}})
		p := l.put("a.txt", []byte("alpha"))
		for _, action := range []string{"stat", "hash", "pull_prepare"} {
			l.files.mustDeny(action, p, nil, "host_denied")
		}
		l.files.mustDeny("push_url", p, map[string]any{"url": "https://example.com/x", "overwrite": true}, "host_denied")
		tg, _ := l.files.tool("terminal_targets", map[string]any{})
		list, _ := tg["targets"].([]any)
		if len(list) != 1 || toolRes(list[0].(map[string]any)).str("files_blocked") != "host_denied" || toolRes(list[0].(map[string]any))["files"] == true {
			t.Fatalf("目标列表应说明本机否决: %v", tg)
		}
		if got, _ := os.ReadFile(p); string(got) != "alpha" {
			t.Fatalf("否决下文件不应被改动")
		}
	})
}

// 能力 URL 凭据（只能填一个网址的连接器）也可以叠加文件权限：同一个工具、同一套闸门；
// 下载地址按这次请求的来源补全，审计带通道标记。
func TestRealFilesViaCapabilityURL(t *testing.T) {
	l := newFilesLab(t, filesLabOpts{hubEnv: filesOn()})
	cred, code := l.newCapabilityCred("cap-files", map[string]any{
		"permissions":       map[string]any{"terminal": true, "terminal_files": true},
		"terminal_limits":   map[string]any{"allowed_paths": []string{"~/work"}},
		"terminal_mode_cap": "auto",
	})
	if code != 200 || cred.secret == "" {
		t.Fatalf("创建带文件权限的能力 URL 凭据: HTTP %d", code)
	}
	m := l.capClient(cred.secret)
	if got := strings.Join(m.toolNames(), ","); got != "terminal_exec,terminal_files,terminal_run,terminal_targets" {
		t.Fatalf("能力 URL 的 tools/list = %s", got)
	}

	content := []byte("via capability " + randHex(6))
	p := l.put("a.txt", content)
	if st := m.mustOK("stat", p, nil); st["exists"] != true || st.num("size") != int64(len(content)) {
		t.Fatalf("stat: %v", st)
	}
	dl := m.mustOK("pull_prepare", p, nil).str("url")
	if !strings.HasPrefix(dl, l.h.base()+"/files/dl/") {
		t.Fatalf("下载地址应补全成这次请求的来源（地址已省略）")
	}
	if code, _, body := httpGet(t, dl); code != 200 || !bytes.Equal(body, content) {
		t.Fatalf("下载: HTTP %d len=%d", code, len(body))
	}
	m.mustDeny("stat", filepath.Join(l.private, "x"), nil, "path_not_allowed")

	tagged := false
	for _, e := range l.audit() {
		if e.Event == terminal.EvFileOp && e.Actor == "cap-files" && strings.Contains(e.Detail, "channel=capability") {
			tagged = true
		}
	}
	if !tagged {
		t.Fatalf("文件操作的审计应带通道标记 channel=capability: %+v", l.audit())
	}
	l.requireNoLeak("能力 URL", ticketOf(dl), string(content))
}
