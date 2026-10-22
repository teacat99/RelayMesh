//go:build e2e && !windows

package e2e

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 「允许整个文件系统」的真实进程端到端：Hub、守护进程、Agent 各自独立。
// 开关默认关闭；开启需要「刚刚重新输入密码」；开启后令牌能读写允许目录之外的路径，但相对路径之类仍是错误；
// 关闭后立即回到原先的目录范围，开关开着时签发的票据一并作废；整个过程都在审计里。
func TestRealFilesWholeFilesystem(t *testing.T) {
	payload := randBytes(300<<10 + 11)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/blob" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	origin := "http://127.0.0.1:" + strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)

	l := newFilesLab(t, filesLabOpts{hubEnv: filesOn()})
	m := l.files
	credURL := fmt.Sprintf("/api/v1/credentials/%d", l.credID)

	inside := l.put("a.txt", []byte("inside"))
	secret := "TOP-SECRET-" + randHex(8)
	secretPath := l.putIn(l.private, "secret.txt", []byte(secret))

	// 1) 默认关闭：目录之外一律拒绝，targets 里也没有 whole_fs。
	m.mustDeny("stat", secretPath, nil, "path_not_allowed")
	tg, _ := m.tool("terminal_targets", map[string]any{})
	if tg.obj("limits")["whole_fs"] == true {
		t.Fatalf("默认不应是整个文件系统: %v", tg["limits"])
	}

	// 2) 开启：写入「允许整个文件系统」，同时保留原来的目录设置（关闭后回到它们）。
	l.h.api.reunlock()
	on := map[string]any{"terminal_limits": map[string]any{"allowed_paths": []string{"~/work"}, "allow_whole_fs": true}}
	if c := l.h.api.raw("PUT", credURL, on, nil, true); c != 200 {
		t.Fatalf("开启整个文件系统: HTTP %d", c)
	}
	tg, _ = m.tool("terminal_targets", map[string]any{})
	limits := tg.obj("limits")
	if limits["whole_fs"] != true {
		t.Fatalf("targets 应回显 whole_fs: %v", tg["limits"])
	}
	if _, has := limits["allowed_paths"]; has {
		t.Fatalf("开关开着时不应再回显被忽略的目录设置: %v", limits)
	}

	// 3) 开启后：目录之外的文件可读（stat / hash / 下载），根目录可见，内容一致。
	st := m.mustOK("stat", secretPath, nil)
	if st["exists"] != true || st.num("size") != int64(len(secret)) {
		t.Fatalf("目录之外的 stat 不符: %v", st)
	}
	if hs := m.mustOK("hash", secretPath, nil); hs.str("sha256") != sum256([]byte(secret)) {
		t.Fatalf("目录之外的 hash 不符: %v", hs)
	}
	if root := m.mustOK("stat", "/", nil); root["exists"] != true || root.str("type") != "dir" {
		t.Fatalf("根目录的 stat 不符: %v", root)
	}
	pp := m.mustOK("pull_prepare", secretPath, nil)
	if code, _, body := httpGet(t, pp.str("url")); code != 200 || string(body) != secret {
		t.Fatalf("目录之外的下载不符: HTTP %d", code)
	}
	// 仍然是精确的绝对路径：相对路径、~用户、含 .. 的写法按原有规则处理，不会被「整个文件系统」放宽成猜测。
	m.mustDeny("stat", "private/secret.txt", nil, "bad_path", "path_not_allowed")
	m.mustDeny("stat", "~root/.ssh", nil, "bad_path", "path_not_allowed")

	// 4) 开启后：目录之外可写（push_url 到允许目录之外），权限仍是 0600，内容一致。
	l.relaunchAgent("agent2", "--file-url-allow-private")
	dst := filepath.Join(l.private, "pushed.bin")
	ok := m.mustOK("push_url", dst, map[string]any{"url": origin + "/blob", "sha256": sum256(payload)})
	if ok.num("size") != int64(len(payload)) {
		t.Fatalf("push_url 结果不符: %v", ok)
	}
	if got, err := os.ReadFile(dst); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("目录之外写入的内容不符: err=%v len=%d", err, len(got))
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o600 {
		t.Fatalf("新文件权限 = %v，期望 0600", info.Mode().Perm())
	}

	// 5) 审计：开启有一条 files_scope；文件操作带 scope=whole_fs；任何地方都没有票据、文件内容、解锁令牌。
	ticket := pp.str("url")
	scopeOn, tagged := 0, 0
	for _, e := range l.audit() {
		if e.Event == terminal.EvFilesScope && e.Result == terminal.ResultOK && strings.Contains(e.Detail, "开启") {
			scopeOn++
		}
		if (e.Event == terminal.EvFileOp || e.Event == terminal.EvFilePull || e.Event == terminal.EvFileDownload || e.Event == terminal.EvFilePush) && strings.Contains(e.Detail, "scope=whole_fs") {
			tagged++
		}
	}
	if scopeOn != 1 || tagged < 4 {
		t.Fatalf("审计不符: files_scope(开启)=%d scope 标记=%d: %+v", scopeOn, tagged, l.audit())
	}
	l.requireNoLeak("整个文件系统", ticketOf(ticket), secret, l.h.api.unlock, string(payload[:64]))

	// 6) 关闭：刹车只需要普通解锁。立即回到目录范围；开关开着时签发、尚未使用的票据作废。
	stale := m.mustOK("pull_prepare", secretPath, nil).str("url")
	off := map[string]any{"terminal_limits": map[string]any{"allowed_paths": []string{"~/work"}}}
	if c := l.h.api.raw("PUT", credURL, off, nil, true); c != 200 {
		t.Fatalf("关闭整个文件系统: HTTP %d", c)
	}
	if code, _, _ := httpGet(t, stale); code != http.StatusNotFound {
		t.Fatalf("关闭后旧票据应作废，实际 HTTP %d", code)
	}
	m.mustDeny("stat", secretPath, nil, "path_not_allowed")
	m.mustDeny("push_url", filepath.Join(l.private, "again.bin"), map[string]any{"url": origin + "/blob"}, "path_not_allowed")
	if l.fileExists(filepath.Join(l.private, "again.bin")) {
		t.Fatalf("关闭后不应再能写到目录之外")
	}
	m.mustOK("stat", inside, nil)
	tg, _ = m.tool("terminal_targets", map[string]any{})
	if tg.obj("limits")["whole_fs"] == true {
		t.Fatalf("关闭后 targets 不应再回显 whole_fs: %v", tg["limits"])
	}
	scopeOff := 0
	for _, e := range l.audit() {
		if e.Event == terminal.EvFilesScope && e.Result == terminal.ResultOK && strings.Contains(e.Detail, "关闭") {
			scopeOff++
		}
	}
	if scopeOff != 1 {
		t.Fatalf("关闭应有且只有一条 files_scope 审计，实际 %d", scopeOff)
	}
}
