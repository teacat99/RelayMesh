package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizePin(t *testing.T) {
	const hexFP = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	want := "sha256:" + hexFP
	ok := []string{
		"sha256:" + hexFP,
		"SHA256:" + strings.ToUpper(hexFP),
		hexFP,
		"  " + hexFP + "  ",
		"sha256:" + strings.Join(splitEvery(hexFP, 2), ":"), // OpenSSL 风格的冒号分隔
	}
	for _, in := range ok {
		got, err := NormalizePin(in)
		if err != nil || got != want {
			t.Errorf("NormalizePin(%q) = %q, %v，期望 %q", in, got, err, want)
		}
	}
	bad := []string{"", "sha256:", "abc", hexFP[:62], hexFP + "00", strings.Repeat("zz", 32)}
	for _, in := range bad {
		if _, err := NormalizePin(in); err == nil {
			t.Errorf("NormalizePin(%q) 应报错", in)
		}
	}
}

func splitEvery(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		k := min(n, len(s))
		out = append(out, s[:k])
		s = s[k:]
	}
	return out
}

func TestValidateRefusesPlaintextWithoutInsecure(t *testing.T) {
	plain, _ := ParseServer("http://10.0.0.5:18775")
	if err := (TrustOptions{}).Validate(plain); err == nil {
		t.Fatal("明文 ws:// 在没有 --insecure 时必须被拒绝")
	}
	if err := (TrustOptions{Insecure: true}).Validate(plain); err != nil {
		t.Fatalf("显式 --insecure 应允许: %v", err)
	}
	secure, _ := ParseServer("https://relay.example.com")
	if err := (TrustOptions{}).Validate(secure); err != nil {
		t.Fatalf("wss 默认应允许: %v", err)
	}
	if err := (TrustOptions{Pins: []string{"nonsense"}}).Validate(secure); err == nil {
		t.Fatal("非法指纹应在启动时被拒绝，而不是连接时才发现")
	}
}

// tlsFixture 是一台自签名证书的 HTTPS 服务器；Agent 默认（系统 CA）不应信任它。
func tlsFixture(t *testing.T) (target ServerTarget, fp string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(srv.Close)
	tg, err := ParseServer(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return tg, Fingerprint(srv.Certificate().Raw)
}

func probe(t *testing.T, o TrustOptions, target ServerTarget) error {
	t.Helper()
	client, err := o.HTTPClient(target)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpURL := "https" + strings.TrimPrefix(target.URL, "wss")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func TestPinnedFingerprint(t *testing.T) {
	target, fp := tlsFixture(t)

	if err := probe(t, TrustOptions{Pins: []string{fp}}, target); err != nil {
		t.Fatalf("正确的指纹应允许连接: %v", err)
	}
	// 多个指纹（证书轮换窗口）：命中任一即可。
	if err := probe(t, TrustOptions{Pins: []string{"sha256:" + strings.Repeat("ab", 32), fp}}, target); err != nil {
		t.Fatalf("多指纹应命中任一: %v", err)
	}
	err := probe(t, TrustOptions{Pins: []string{"sha256:" + strings.Repeat("ab", 32)}}, target)
	if err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("错误的指纹必须被拒绝并说明原因，得到 %v", err)
	}
	// 无任何信任配置：自签名证书被系统 CA 拒绝（绝不静默降级）。
	if err := probe(t, TrustOptions{}, target); err == nil {
		t.Fatal("没有 pin / TOFU 时，自签名证书必须被拒绝")
	}
}

func TestTOFU(t *testing.T) {
	target, fp := tlsFixture(t)
	known := filepath.Join(t.TempDir(), "cfg", "known_servers")

	prompts := 0
	accept := func(host, got string) bool {
		prompts++
		if host != target.Host || got != fp {
			t.Errorf("Prompt 收到的主机 / 指纹不对: %s %s", host, got)
		}
		return true
	}

	// 首次：询问人类并记录。
	if err := probe(t, TrustOptions{KnownServersPath: known, Prompt: accept}, target); err != nil {
		t.Fatalf("同意信任后应能连接: %v", err)
	}
	if prompts != 1 {
		t.Fatalf("首次应询问一次，得到 %d 次", prompts)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(known)
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("已信任服务器记录不应对他人可读写: %o", perm)
		}
	}
	raw, _ := os.ReadFile(known)
	if !strings.Contains(string(raw), target.Host+" "+fp) {
		t.Fatalf("记录内容不对: %q", raw)
	}

	// 之后：指纹一致，不再询问。
	never := func(string, string) bool {
		t.Error("已记录的服务器不应再次询问")
		return false
	}
	if err := probe(t, TrustOptions{KnownServersPath: known, Prompt: never}, target); err != nil {
		t.Fatalf("已信任的服务器应直接放行: %v", err)
	}

	// 指纹变化：一律拒绝，且绝不自动更新记录、也不询问（否则中间人只需让人点一次「是」）。
	if err := os.WriteFile(known, []byte(target.Host+" sha256:"+strings.Repeat("cd", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := probe(t, TrustOptions{KnownServersPath: known, Prompt: never}, target)
	if err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("指纹变化必须被拒绝，得到 %v", err)
	}
	after, _ := os.ReadFile(known)
	if strings.Contains(string(after), fp) {
		t.Fatal("指纹不一致时不能自动覆盖记录")
	}
}

func TestTOFUDeclined(t *testing.T) {
	target, _ := tlsFixture(t)
	known := filepath.Join(t.TempDir(), "known_servers")
	deny := func(string, string) bool { return false }
	err := probe(t, TrustOptions{KnownServersPath: known, Prompt: deny}, target)
	if err == nil || !strings.Contains(err.Error(), "未获得") {
		t.Fatalf("拒绝信任后必须中止连接，得到 %v", err)
	}
	if _, statErr := os.Stat(known); statErr == nil {
		t.Fatal("拒绝信任后不应写入记录")
	}
}

func TestKnownServersLastEntryWinsAndRejectsBadHost(t *testing.T) {
	k := &KnownServers{Path: filepath.Join(t.TempDir(), "known")}
	if _, found, err := k.Lookup("a:1"); found || err != nil {
		t.Fatalf("文件不存在时应为未找到: %v %v", found, err)
	}
	if err := k.Add("a:1", "sha256:aa"); err != nil {
		t.Fatal(err)
	}
	if err := k.Add("b:2", "sha256:bb"); err != nil {
		t.Fatal(err)
	}
	if err := k.Add("a:1", "sha256:cc"); err != nil {
		t.Fatal(err)
	}
	if fp, found, _ := k.Lookup("a:1"); !found || fp != "sha256:cc" {
		t.Fatalf("后出现的记录应覆盖先出现的: %s", fp)
	}
	if fp, found, _ := k.Lookup("b:2"); !found || fp != "sha256:bb" {
		t.Fatalf("b:2 记录不对: %s", fp)
	}
	if err := k.Add("bad host", "sha256:dd"); err == nil {
		t.Fatal("含空白的主机名会破坏记录格式，必须拒绝")
	}
}
