package terminal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 服务端自带安装源（GET /i/:ticket/:name）的行为测试：票据与发现窗口的绑定、文件白名单、统一 404、限速与并发上限。

var ticketPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func withAgentDist(dir string) envOption {
	return func(o *Options, _ *testEnv) { o.AgentDistDir = dir }
}

// newDistDir 造一个分发目录：两个平台的假二进制，外加几个不应被分发的文件。
func newDistDir(t *testing.T) (dir string, linux, windows []byte) {
	t.Helper()
	dir = t.TempDir()
	linux = bytes.Repeat([]byte("linux-agent-bytes-"), 4096) // 72 KiB，足够测 Range
	windows = []byte("MZ-windows-agent")
	must := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("relaymesh-agent-linux-amd64", linux)
	must("relaymesh-agent-windows-amd64.exe", windows)
	must("secret.env", []byte("TOKEN=do-not-serve"))
	must("relaymesh-agent-linux-amd64.bak", []byte("old"))
	return dir, linux, windows
}

func (e *testEnv) installStatus(hdr ...string) map[string]any {
	e.t.Helper()
	res := e.call("GET", "/api/v1/terminal/status", nil, append([]string{"Authorization", humanAuth(testUser)}, hdr...)...)
	if res.Status != http.StatusOK {
		e.t.Fatalf("status: %d %s", res.Status, res.Raw)
	}
	inst, _ := res.Body["install"].(map[string]any)
	if inst == nil {
		e.t.Fatalf("status 里应有 install: %s", res.Raw)
	}
	return inst
}

func (e *testEnv) openWindow() string {
	e.t.Helper()
	e.openDiscovery(e.admin())
	ticket, _ := e.installStatus()["ticket"].(string)
	if !ticketPattern.MatchString(ticket) {
		e.t.Fatalf("窗口开启后应有 32 位十六进制票据，实际 %q", ticket)
	}
	return ticket
}

func TestInstallSourceUnavailableWithoutDist(t *testing.T) {
	e := newTestEnv(t)
	e.openDiscovery(e.admin())

	inst := e.installStatus()
	if inst["available"] != false {
		t.Fatalf("没有分发目录时 available 应为 false: %v", inst)
	}
	if _, has := inst["ticket"]; has {
		t.Fatal("没有安装源时不应发放票据")
	}
	// 即使猜中了票据，也没有东西可发。
	ticket := e.hub.currentInstallTicket()
	for _, name := range []string{"relaymesh-agent-linux-amd64", "checksums.txt"} {
		if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/"+name); st != http.StatusNotFound {
			t.Errorf("%s: 没有分发目录应为 404，实际 %d", name, st)
		}
	}
}

func TestInstallTicketFollowsDiscoveryWindow(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir))

	// 窗口关闭：状态里可用但没有票据。
	inst := e.installStatus()
	if inst["available"] != true {
		t.Fatalf("应可用: %v", inst)
	}
	if _, has := inst["ticket"]; has {
		t.Fatal("窗口关闭时不应返回票据")
	}
	if got := strings.Join(toStrings(inst["platforms"]), ","); got != "linux-amd64,windows-amd64" {
		t.Fatalf("platforms = %s", got)
	}

	first := e.openWindow()
	if again, _ := e.installStatus()["ticket"].(string); again != first {
		t.Fatal("同一个窗口内票据应保持不变")
	}
	// 窗口开着时再点一次「开启」只是顺延：票据不变，已经复制出去的命令继续可用。
	e.openDiscovery(e.admin())
	if again, _ := e.installStatus()["ticket"].(string); again != first {
		t.Fatal("顺延窗口不应换发票据")
	}
	if st, _, _ := e.callRaw("GET", "/i/"+first+"/install.sh"); st != http.StatusOK {
		t.Fatalf("窗口开启时票据应可用，实际 %d", st)
	}

	// 关闭窗口：票据立即作废。
	e.call("DELETE", "/api/v1/terminal/discovery", nil, e.admin()...)
	if _, has := e.installStatus()["ticket"]; has {
		t.Fatal("窗口关闭后不应再返回票据")
	}
	if st, _, _ := e.callRaw("GET", "/i/"+first+"/install.sh"); st != http.StatusNotFound {
		t.Fatalf("窗口关闭后票据应失效，实际 %d", st)
	}

	// 再次开启：换发新票据，旧票据不会复活。
	second := e.openWindow()
	if second == first {
		t.Fatal("窗口重新开启后必须换发新票据")
	}
	if st, _, _ := e.callRaw("GET", "/i/"+first+"/install.sh"); st != http.StatusNotFound {
		t.Fatalf("旧票据不应复活，实际 %d", st)
	}
	if st, _, _ := e.callRaw("GET", "/i/"+second+"/install.sh"); st != http.StatusOK {
		t.Fatalf("新票据应可用，实际 %d", st)
	}
}

func TestInstallTicketExpiresWithWindow(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir), withFakeClock())
	ticket := e.openWindow() // 窗口 5 分钟

	e.clock.Advance(4 * time.Minute)
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusOK {
		t.Fatalf("窗口内应可用，实际 %d", st)
	}
	e.clock.Advance(2 * time.Minute)
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusNotFound {
		t.Fatalf("窗口到期后票据应失效，实际 %d", st)
	}
}

func TestInstallServesWhitelistedFiles(t *testing.T) {
	dir, linux, windows := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir))
	spki := "sha256//" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	e.hub.SetTLSSPKI(spki)
	ticket := e.openWindow()
	base := "/i/" + ticket + "/"

	// 脚本：带前导，文本类型，不缓存。
	st, hdr, body := e.callRaw("GET", base+"install.sh")
	if st != http.StatusOK || !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain") {
		t.Fatalf("install.sh: %d %s", st, hdr.Get("Content-Type"))
	}
	if hdr.Get("Cache-Control") != "no-store" || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("响应头不对: %v", hdr)
	}
	if !bytes.Contains(body, []byte("RELAYMESH_HUB_TICKET='"+ticket+"'")) || !bytes.Contains(body, []byte("RELAYMESH_HUB_SPKI='"+spki+"'")) {
		t.Error("install.sh 的前导里应带票据与公钥指纹")
	}
	st, _, body = e.callRaw("GET", base+"install.ps1")
	if st != http.StatusOK || !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("install.ps1: %d，BOM 应保留", st)
	}
	if !bytes.Contains(body, []byte("$RelayMeshHub.Ticket = '"+ticket+"'")) {
		t.Error("install.ps1 的前导里应带票据")
	}

	// 二进制：字节一致、附件头、支持 Range 与 HEAD。
	st, hdr, body = e.callRaw("GET", base+"relaymesh-agent-linux-amd64")
	if st != http.StatusOK || !bytes.Equal(body, linux) {
		t.Fatalf("linux 二进制不一致: %d len=%d", st, len(body))
	}
	if got := hdr.Get("Content-Disposition"); got != `attachment; filename="relaymesh-agent-linux-amd64"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if hdr.Get("Accept-Ranges") != "bytes" {
		t.Error("应声明支持 Range（断点续传）")
	}
	st, hdr, body = e.callRaw("GET", base+"relaymesh-agent-linux-amd64", "Range", "bytes=10-19")
	if st != http.StatusPartialContent || !bytes.Equal(body, linux[10:20]) || !strings.HasPrefix(hdr.Get("Content-Range"), "bytes 10-19/") {
		t.Fatalf("Range 应返回 206 与对应字节: %d %q %v", st, body, hdr)
	}
	st, hdr, body = e.callRaw("HEAD", base+"relaymesh-agent-windows-amd64.exe")
	if st != http.StatusOK || len(body) != 0 || hdr.Get("Content-Length") != strconv.Itoa(len(windows)) {
		t.Fatalf("HEAD: %d len=%d CL=%s", st, len(body), hdr.Get("Content-Length"))
	}

	// checksums.txt：sha256sum 格式，只列出可分发的文件。
	st, _, body = e.callRaw("GET", base+"checksums.txt")
	if st != http.StatusOK {
		t.Fatalf("checksums.txt: %d", st)
	}
	hl := sha256.Sum256(linux)
	hw := sha256.Sum256(windows)
	want := hex.EncodeToString(hl[:]) + "  relaymesh-agent-linux-amd64\n" + hex.EncodeToString(hw[:]) + "  relaymesh-agent-windows-amd64.exe\n"
	if string(body) != want {
		t.Fatalf("checksums.txt =\n%s\nwant\n%s", body, want)
	}
}

func TestInstallEveryFailureLooksTheSame(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir))
	good := e.openWindow()

	type probe struct {
		name, path string
		routed     bool // 为 false 时请求根本匹配不到 /i/:ticket/:name（多出路径段），由全站默认的 404 处理
	}
	probes := []probe{
		{"错误票据", "/i/" + strings.Repeat("0", 32) + "/install.sh", true},
		{"票据位数不对", "/i/abc/install.sh", true},
		{"票据大小写不同", "/i/" + strings.ToUpper(good) + "/install.sh", true},
		{"不存在的文件名", "/i/" + good + "/nope.sh", true},
		{"目录里的杂物", "/i/" + good + "/secret.env", true},
		{"备份文件", "/i/" + good + "/relaymesh-agent-linux-amd64.bak", true},
		{"平台不在白名单", "/i/" + good + "/relaymesh-agent-freebsd-amd64", true},
		{"该平台没有文件", "/i/" + good + "/relaymesh-agent-darwin-arm64", true},
		{"路径穿越（编码斜杠）", "/i/" + good + "/..%2Fsecret", false},
		{"路径穿越（多一段）", "/i/" + good + "/x/../secret", false},
	}
	var wantBody string
	for _, p := range probes {
		st, hdr, body := e.callRaw("GET", p.path)
		if st != http.StatusNotFound {
			t.Errorf("%s: 应为 404，实际 %d", p.name, st)
		}
		if strings.Contains(string(body), "do-not-serve") {
			t.Fatalf("%s: 泄露了目录里的其他文件", p.name)
		}
		if !p.routed {
			continue
		}
		if wantBody == "" {
			wantBody = string(body)
		} else if string(body) != wantBody {
			t.Errorf("%s: 响应体与其他失败不一致（会泄露状态）: %q vs %q", p.name, body, wantBody)
		}
		if hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: 失败响应也不应被缓存", p.name)
		}
	}
	// 窗口关闭后，连「正确」的票据也是同样的 404。
	e.call("DELETE", "/api/v1/terminal/discovery", nil, e.admin()...)
	st, _, body := e.callRaw("GET", "/i/"+good+"/install.sh")
	if st != http.StatusNotFound || string(body) != wantBody {
		t.Errorf("窗口关闭后应是同样的 404: %d %q", st, body)
	}
}

func TestInstallRefusedWhenGateClosed(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir), withMode(ModeOff))
	// 总闸关闭时发现窗口无从开启；即便直接取得票据，也不能下载。
	e.hub.disc.open(e.hub.now(), time.Minute)
	ticket := e.hub.currentInstallTicket()
	if !ticketPattern.MatchString(ticket) {
		t.Fatal("测试前提：窗口应已开启")
	}
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusNotFound {
		t.Fatalf("总闸关闭时应为 404，实际 %d", st)
	}
}

func TestInstallAlwaysDiscoveryHasStableTicket(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir), withDiscoveryAlways())
	first, _ := e.installStatus()["ticket"].(string)
	if !ticketPattern.MatchString(first) {
		t.Fatalf("常开模式也应有票据: %q", first)
	}
	if again, _ := e.installStatus()["ticket"].(string); again != first {
		t.Fatal("常开模式的票据在进程生命周期内应保持不变")
	}
	if st, _, _ := e.callRaw("GET", "/i/"+first+"/checksums.txt"); st != http.StatusOK {
		t.Fatalf("常开模式下票据应可用，实际 %d", st)
	}
}

func TestInstallRateLimitPerSource(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir), withFakeClock())
	ticket := e.openWindow()

	bad := "/i/" + strings.Repeat("f", 32) + "/install.sh"
	var limited int
	for i := 0; i < installRatePerMinute+10; i++ {
		st, hdr, _ := e.callRaw("GET", bad)
		if st == http.StatusTooManyRequests {
			limited++
			if hdr.Get("Retry-After") == "" {
				t.Fatal("429 应带 Retry-After")
			}
		}
	}
	if limited != 10 {
		t.Fatalf("超过每分钟 %d 次后应被限速，实际被限 %d 次", installRatePerMinute, limited)
	}
	// 被限速期间，正确的票据同样被挡（限速先于票据判断，不给探测者区分的机会）。
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusTooManyRequests {
		t.Fatalf("限速期间应一律 429，实际 %d", st)
	}
	// 一分钟后恢复。
	e.clock.Advance(61 * time.Second)
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/install.sh"); st != http.StatusOK {
		t.Fatalf("窗口滚动后应恢复，实际 %d", st)
	}
}

func TestInstallTransferCapIsolatedFromScripts(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir))
	ticket := e.openWindow()

	for i := 0; i < cap(e.hub.installSem); i++ {
		e.hub.installSem <- struct{}{}
	}
	st, hdr, _ := e.callRaw("GET", "/i/"+ticket+"/relaymesh-agent-linux-amd64")
	if st != http.StatusServiceUnavailable || hdr.Get("Retry-After") == "" {
		t.Fatalf("传输名额占满时应 503 并带 Retry-After，实际 %d", st)
	}
	// 脚本与校验文件很小，不占传输名额。
	for _, name := range []string{"install.sh", "checksums.txt"} {
		if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/"+name); st != http.StatusOK {
			t.Errorf("%s 不应受传输名额影响，实际 %d", name, st)
		}
	}
	for i := 0; i < cap(e.hub.installSem); i++ {
		<-e.hub.installSem
	}
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/relaymesh-agent-linux-amd64"); st != http.StatusOK {
		t.Fatalf("名额释放后应恢复，实际 %d", st)
	}
}

func TestInstallDownloadsAreAudited(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir))
	ticket := e.openWindow()

	e.callRaw("GET", "/i/"+ticket+"/relaymesh-agent-linux-amd64")
	e.callRaw("GET", "/i/"+strings.Repeat("a", 32)+"/install.sh")

	res := e.call("GET", "/api/v1/terminal/audit?limit=100", nil, "Authorization", humanAuth(testUser))
	if res.Status != http.StatusOK {
		t.Fatalf("audit: %d", res.Status)
	}
	if !strings.Contains(res.Raw, EvInstallServe) {
		t.Fatalf("审计应有 %s: %s", EvInstallServe, res.Raw)
	}
	if !strings.Contains(res.Raw, "name=relaymesh-agent-linux-amd64") {
		t.Errorf("审计明细应含文件名: %s", res.Raw)
	}
	// 绝不能把票据写进审计。
	if strings.Contains(res.Raw, ticket) {
		t.Fatal("审计里不应出现票据")
	}
}

func TestInstallTicketsOnlyReachLoggedInUsers(t *testing.T) {
	dir, _, _ := newDistDir(t)
	e := newTestEnv(t, withAgentDist(dir))
	e.openWindow()

	anon := e.call("GET", "/api/v1/terminal/status", nil)
	if anon.Status != http.StatusUnauthorized || strings.Contains(anon.Raw, "ticket") {
		t.Fatalf("匿名访问 status 应被拒绝且不含票据: %d %s", anon.Status, anon.Raw)
	}
	mcp := e.call("GET", "/api/v1/terminal/status", nil, "Authorization", "Bearer mcp-valid-token")
	if strings.Contains(mcp.Raw, "ticket") {
		t.Fatalf("MCP 令牌不应看到票据: %d %s", mcp.Status, mcp.Raw)
	}
}

func TestInstallLimiterForgetsIdleSources(t *testing.T) {
	clock := newTestClock()
	l := newInstallLimiter(clock.Now)
	for i := 0; i < 5000; i++ {
		l.allow("10.0." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250))
	}
	clock.Advance(2 * time.Minute)
	l.allow("203.0.113.9") // 触发清理
	l.mu.Lock()
	n := len(l.rate)
	l.mu.Unlock()
	if n > 4096 {
		t.Fatalf("限速表应清理过期条目，仍有 %d 条", n)
	}
}

func TestInstallTicketsRotateOnlyOnEpochChange(t *testing.T) {
	var tk installTickets
	a := tk.forEpoch(1)
	if tk.forEpoch(1) != a {
		t.Fatal("同一纪元应返回同一票据")
	}
	if !tk.valid(1, a) || tk.valid(1, a+"x") || tk.valid(1, "") {
		t.Fatal("valid 判断不对")
	}
	b := tk.forEpoch(2)
	if b == a {
		t.Fatal("纪元变化应换发票据")
	}
	// 旧纪元的票据不能复活。
	if tk.valid(2, a) {
		t.Fatal("旧票据不应在新纪元有效")
	}
}

func TestClipNameNeutralisesControlCharacters(t *testing.T) {
	got := clipName("a\r\nb\x00" + strings.Repeat("x", 100))
	if strings.ContainsAny(got, "\r\n\x00") || len(got) > 64 {
		t.Fatalf("clipName = %q", got)
	}
}

// ---------- 小工具 ----------

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
