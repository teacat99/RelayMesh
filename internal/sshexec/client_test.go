package sshexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec/sshdtest"
	"golang.org/x/crypto/ssh"
)

const testPassword = "pw-Zk3-unit-test-only"

func testKeyPEM(t *testing.T, passphrase string) (string, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), signer
}

func passwordSpec(srv *sshdtest.Server, user string) Spec {
	return Spec{Addr: srv.Addr, User: user, Cred: Credential{Kind: AuthPassword, Password: testPassword}, HostKey: NewHostKey(srv.HostKey())}
}

func TestNormalizeAddr(t *testing.T) {
	ok := map[string]string{
		"example.com":        "example.com:22",
		" example.com:2222 ": "example.com:2222",
		"10.0.0.5":           "10.0.0.5:22",
		"10.0.0.5:22":        "10.0.0.5:22",
		"[::1]:2200":         "[::1]:2200",
		"::1":                "[::1]:22",
		"my_host.local":      "my_host.local:22",
	}
	for in, want := range ok {
		got, err := NormalizeAddr(in)
		if err != nil || got != want {
			t.Errorf("NormalizeAddr(%q) = %q, %v；期望 %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "   ", "user@host", "ssh://host", "host/path", "host:0", "host:99999", "host:abc", "ho st",
		"ho\nst", "-bad.example.com", "bad-.example.com", "a..b", "host:22:33", "[::1", strings.Repeat("a", 300),
		"host%eth0", "host?x", "host#x", "host\\x",
	} {
		if got, err := NormalizeAddr(in); err == nil {
			t.Errorf("NormalizeAddr(%q) 应被拒绝，得到 %q", in, got)
		} else if !errors.Is(err, ErrBadAddress) {
			t.Errorf("NormalizeAddr(%q) 应返回 ErrBadAddress，得到 %v", in, err)
		}
	}
}

func TestValidUser(t *testing.T) {
	for _, u := range []string{"root", "deploy-1", "a.b_c", "svc$"} {
		if !ValidUser(u) {
			t.Errorf("%q 应合法", u)
		}
	}
	for _, u := range []string{"", "a b", "a;b", "a\nb", "a@b", strings.Repeat("x", 65), "-rf"[:0]} {
		if ValidUser(u) {
			t.Errorf("%q 应不合法", u)
		}
	}
}

func TestCredentialIsRedactedEverywhere(t *testing.T) {
	c := Credential{Kind: AuthPassword, Password: "s3cr3t-do-not-leak"}
	k := Credential{Kind: AuthKey, PrivateKey: "-----BEGIN PRIVATE KEY-----LEAKME", Passphrase: "pass-do-not-leak"}
	for _, v := range []Credential{c, k} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
			out := fmt.Sprintf(f, v)
			if strings.Contains(out, "do-not-leak") || strings.Contains(out, "LEAKME") {
				t.Fatalf("格式 %s 泄露了凭据: %s", f, out)
			}
		}
	}
	spec := Spec{Addr: "h:22", User: "u", Cred: c}
	if strings.Contains(fmt.Sprint(spec), "do-not-leak") || strings.Contains(fmt.Sprintf("%+v", spec), "do-not-leak") {
		t.Fatal("Spec 的格式化输出泄露了凭据")
	}
}

func TestCredentialValidateAndRoundTrip(t *testing.T) {
	plainPEM, _ := testKeyPEM(t, "")
	encPEM, _ := testKeyPEM(t, "open sesame")

	good := []Credential{
		{Kind: AuthPassword, Password: "abc"},
		{Kind: AuthKey, PrivateKey: plainPEM},
		{Kind: AuthKey, PrivateKey: encPEM, Passphrase: "open sesame"},
	}
	for _, c := range good {
		raw, err := c.Marshal()
		if err != nil {
			t.Fatalf("%s 应通过校验: %v", c.Kind, err)
		}
		back, err := ParseCredential(raw)
		if err != nil || back != c {
			t.Fatalf("往返不一致: %v", err)
		}
	}

	bad := map[string]struct {
		c    Credential
		want error
	}{
		"空密码":      {Credential{Kind: AuthPassword}, ErrBadCredential},
		"密码过长":     {Credential{Kind: AuthPassword, Password: strings.Repeat("x", 2000)}, ErrBadCredential},
		"密码含 NUL":  {Credential{Kind: AuthPassword, Password: "a\x00b"}, ErrBadCredential},
		"密码带私钥":    {Credential{Kind: AuthPassword, Password: "a", PrivateKey: "k"}, ErrBadCredential},
		"私钥带密码":    {Credential{Kind: AuthKey, Password: "a", PrivateKey: plainPEM}, ErrBadCredential},
		"私钥为空":     {Credential{Kind: AuthKey}, ErrBadCredential},
		"私钥无法解析":   {Credential{Kind: AuthKey, PrivateKey: "not a key"}, ErrBadCredential},
		"加密私钥缺口令":  {Credential{Kind: AuthKey, PrivateKey: encPEM}, ErrPassphrase},
		"加密私钥口令错误": {Credential{Kind: AuthKey, PrivateKey: encPEM, Passphrase: "wrong"}, ErrBadCredential},
		"未知方式":     {Credential{Kind: "token", Password: "a"}, ErrBadCredential},
		"私钥过大":     {Credential{Kind: AuthKey, PrivateKey: strings.Repeat("A", 20<<10)}, ErrBadCredential},
	}
	for name, tc := range bad {
		if err := tc.c.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%s：期望 %v，得到 %v", name, tc.want, err)
		}
	}
	if _, err := ParseCredential([]byte(`{"kind":"nope"}`)); err == nil {
		t.Error("未知方式的密文内容不应被还原")
	}
	if _, err := ParseCredential([]byte(`not json`)); err == nil {
		t.Error("坏 JSON 不应被还原")
	}
}

func TestProbeReturnsHostKeyWithoutAuthenticating(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword})
	hk, err := Probe(context.Background(), srv.Addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if hk.Fingerprint != ssh.FingerprintSHA256(srv.HostKey()) || hk.Type != "ssh-ed25519" {
		t.Fatalf("探测到的密钥不对: %s %s", hk.Type, hk.Fingerprint)
	}
	if srv.AuthAttempts() != 0 {
		t.Fatal("探测不应发出任何认证")
	}
	back, err := DecodeHostKey(hk.Encode())
	if err != nil || back.Fingerprint != hk.Fingerprint {
		t.Fatalf("编码往返失败: %v", err)
	}
}

func TestProbeFailures(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			_ = c.Close()
		}
	}()
	defer ln.Close()
	if _, err := Probe(context.Background(), ln.Addr().String(), 2*time.Second); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("非 SSH 服务应返回 ErrUnreachable，得到 %v", err)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := closed.Addr().String()
	_ = closed.Close()
	if _, err := Probe(context.Background(), addr, 2*time.Second); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("端口关闭应返回 ErrUnreachable，得到 %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Probe(ctx, addr, 2*time.Second); err == nil {
		t.Fatal("已取消的上下文应失败")
	}
}

func TestDialPinnedAndPasswordAuth(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{User: "deploy", Password: testPassword})
	c, err := Dial(context.Background(), passwordSpec(srv, "deploy"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Exec(context.Background(), "echo ok", nil, 0)
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "ok" || res.Exit != 0 {
		t.Fatalf("exec 结果异常: %+v %v", res, err)
	}
}

func TestDialMismatchNeverSendsCredentials(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{User: "deploy", Password: testPassword})
	pinned := NewHostKey(srv.HostKey())
	srv.SetHostSigner(sshdtest.NewSigner(t)) // 主机被替换 / 中间人
	_, err := Dial(context.Background(), Spec{Addr: srv.Addr, User: "deploy", Cred: Credential{Kind: AuthPassword, Password: testPassword}, HostKey: pinned}, 3*time.Second)
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("应返回 ErrHostKeyMismatch，得到 %v", err)
	}
	if srv.AuthAttempts() != 0 {
		t.Fatal("主机密钥不匹配时不应向对方发出任何认证信息")
	}
}

func TestDialRequiresPinnedKey(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword})
	_, err := Dial(context.Background(), Spec{Addr: srv.Addr, User: "tester", Cred: Credential{Kind: AuthPassword, Password: testPassword}}, 2*time.Second)
	if !errors.Is(err, ErrNoHostKey) {
		t.Fatalf("没有固定主机密钥必须拒绝连接，得到 %v", err)
	}
	if srv.Connections() != 0 {
		t.Fatal("拒绝连接意味着根本不应该建立 TCP 连接")
	}
}

func TestDialAuthFailure(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword})
	spec := passwordSpec(srv, "tester")
	spec.Cred.Password = "wrong-password"
	if _, err := Dial(context.Background(), spec, 3*time.Second); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("应返回 ErrAuthFailed，得到 %v", err)
	}
	spec = passwordSpec(srv, "someone-else")
	if _, err := Dial(context.Background(), spec, 3*time.Second); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("用户不对应返回 ErrAuthFailed，得到 %v", err)
	}
}

// 没有启用 PAM 的 OpenSSH 会声明支持 keyboard-interactive，却不发任何提问就拒绝。
// 这时 x/crypto 报的是「unexpected message type 51」而不是「unable to authenticate」，
// 它仍然是认证失败（用户该看到「SSH 认证失败」，而不是一串协议层错误）；正确的口令则照常通过。
func TestDialAuthFailureWhenKeyboardInteractiveHasNoPrompts(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword, KbdNoPrompt: true})
	spec := passwordSpec(srv, "tester")
	spec.Cred.Password = "wrong-password"
	_, err := Dial(context.Background(), spec, 3*time.Second)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("口令错误应返回 ErrAuthFailed，得到 %v", err)
	}
	if errors.Is(err, ErrUnreachable) {
		t.Fatalf("认证失败不能被当成主机不可达：%v", err)
	}
	spec.Cred.Password = testPassword
	c, err := Dial(context.Background(), spec, 3*time.Second)
	if err != nil {
		t.Fatalf("正确的口令必须仍然能登录：%v", err)
	}
	_ = c.Close()
}

func TestIsAuthRejected(t *testing.T) {
	for msg, want := range map[string]bool{
		"ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain": true,
		"ssh: handshake failed: ssh: unexpected message type 51 (expected 60)":                                               true,
		"ssh: handshake failed: ssh: unexpected message type 510 (expected 60)":                                              false,
		"ssh: handshake failed: EOF": false,
		"ssh: handshake failed: read tcp 127.0.0.1:1->127.0.0.1:2: connection reset by peer": false,
	} {
		if got := isAuthRejected(errors.New(msg)); got != want {
			t.Errorf("isAuthRejected(%q) = %v，期望 %v", msg, got, want)
		}
	}
}

func TestDialKeyAuth(t *testing.T) {
	keyPEM, signer := testKeyPEM(t, "")
	encPEM, encSigner := testKeyPEM(t, "pass-phrase-1")
	srv := sshdtest.Start(t, sshdtest.Options{AuthorizedKeys: []ssh.PublicKey{signer.PublicKey(), encSigner.PublicKey()}})
	hk := NewHostKey(srv.HostKey())
	for name, cred := range map[string]Credential{
		"无口令": {Kind: AuthKey, PrivateKey: keyPEM},
		"有口令": {Kind: AuthKey, PrivateKey: encPEM, Passphrase: "pass-phrase-1"},
	} {
		c, err := Dial(context.Background(), Spec{Addr: srv.Addr, User: "tester", Cred: cred, HostKey: hk}, 3*time.Second)
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		_ = c.Close()
	}
	otherPEM, _ := testKeyPEM(t, "")
	_, err := Dial(context.Background(), Spec{Addr: srv.Addr, User: "tester", Cred: Credential{Kind: AuthKey, PrivateKey: otherPEM}, HostKey: hk}, 3*time.Second)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("未授权的密钥应返回 ErrAuthFailed，得到 %v", err)
	}
}

func TestHostKeyAlgorithmFollowsThePinnedKey(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword, ExtraHostKeys: []ssh.Signer{rsaSigner}})
	// 服务器同时有 ed25519 与 RSA 两把主机密钥：固定哪一把，就必须协商到哪一把，不能因为服务器的偏好而误报「密钥变了」。
	for name, pinned := range map[string]ssh.PublicKey{"ed25519": srv.HostKey(), "rsa": rsaSigner.PublicKey()} {
		spec := passwordSpec(srv, "tester")
		spec.HostKey = NewHostKey(pinned)
		c, err := Dial(context.Background(), spec, 5*time.Second)
		if err != nil {
			t.Fatalf("固定 %s 主机密钥应能连接: %v", name, err)
		}
		_ = c.Close()
	}
}

func TestExecStdinExitCodeStderrAndLimits(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword})
	c, err := Dial(context.Background(), passwordSpec(srv, "tester"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	res, err := c.Exec(ctx, "cat", []byte("hello stdin"), 0)
	if err != nil || string(res.Stdout) != "hello stdin" {
		t.Fatalf("stdin 回显异常: %q %v", res.Stdout, err)
	}
	res, err = c.Exec(ctx, "echo out; echo err 1>&2; exit 7", nil, 0)
	if err != nil || res.Exit != 7 || string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" {
		t.Fatalf("退出码 / 两路输出异常: %+v %v", res, err)
	}
	if _, err := c.Exec(ctx, "head -c 200000 /dev/zero", nil, 1024); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超出上限应返回 ErrTooLarge，得到 %v", err)
	}

	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Exec(short, "sleep 5", nil, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时应返回 DeadlineExceeded，得到 %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("取消应当及时生效")
	}
}

func TestPoolReusesRetriesAndForgets(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword})
	p := &Pool{DialTimeout: 3 * time.Second}
	defer p.Close()
	spec := passwordSpec(srv, "tester")
	ctx := context.Background()
	run := func() string {
		t.Helper()
		res, err := p.Exec(ctx, "h1", spec, "echo hi", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(res.Stdout))
	}
	for i := 0; i < 3; i++ {
		if run() != "hi" {
			t.Fatal("输出不对")
		}
	}
	if n := srv.Connections(); n != 1 {
		t.Fatalf("连接应被复用，实际建立了 %d 条", n)
	}

	srv.DropConnections() // 对端关掉连接：下一次调用应透明地重连
	time.Sleep(100 * time.Millisecond)
	if run() != "hi" || srv.Connections() != 2 {
		t.Fatalf("断线后应重连一次，连接数 %d", srv.Connections())
	}

	srv.DropNextExec(1) // 命令发出后连接被掐断：同一次调用内换新连接重试
	if run() != "hi" {
		t.Fatal("重试后应成功")
	}
	if n := srv.Connections(); n != 3 {
		t.Fatalf("重试应使用新连接，连接数 %d", n)
	}

	p.Forget("h1")
	if run() != "hi" || srv.Connections() != 4 {
		t.Fatalf("Forget 之后应重新建立连接，连接数 %d", srv.Connections())
	}
	if p.Reap(time.Now().Add(time.Hour)) != 1 {
		t.Fatal("空闲连接应被回收")
	}
	if run() != "hi" || srv.Connections() != 5 {
		t.Fatalf("回收之后应重新建立连接，连接数 %d", srv.Connections())
	}
}

func TestPoolLimitsConcurrentSessionsPerHost(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword})
	p := &Pool{DialTimeout: 3 * time.Second, MaxSessions: 2}
	defer p.Close()
	spec := passwordSpec(srv, "tester")
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Exec(context.Background(), "h", spec, "sleep 0.3", nil, 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if d := time.Since(start); d < 800*time.Millisecond {
		t.Fatalf("6 个各 0.3 秒的会话在并发上限 2 下至少需要约 0.9 秒，实际 %v", d)
	}
}

func TestPoolForgetDuringDialLeavesNoConnection(t *testing.T) {
	srv := sshdtest.Start(t, sshdtest.Options{Password: testPassword})
	p := &Pool{DialTimeout: 3 * time.Second}
	defer p.Close()
	spec := passwordSpec(srv, "tester")
	e := p.entry("h")
	e.st.Lock() // 模拟「建立连接期间被 Forget」
	e.closed = true
	e.st.Unlock()
	if _, err := e.get(context.Background(), spec, 3*time.Second); !errors.Is(err, errEntryClosed) {
		t.Fatalf("已关闭的条目不应再建立连接，得到 %v", err)
	}
	// 通过 Exec 走一遍：旧条目被丢弃后会换新条目成功执行。
	p.Forget("h")
	if _, err := p.Exec(context.Background(), "h", spec, "true", nil, 0); err != nil {
		t.Fatal(err)
	}
}
