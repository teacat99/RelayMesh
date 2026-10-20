package agent

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Agent 必须验证服务端：服务端能在这台主机上开 shell，所以中间人攻击等同于把主机交给攻击者。
// 因此校验失败一律「失败即关闭」——不降级、不重试为明文。
//
// 三种信任方式：
//  1. 默认：wss:// + 系统 CA；
//  2. --pin sha256:<指纹>：固定服务端叶子证书（自签名证书场景，Web 发现面板生成的命令已带上）；
//  3. TOFU（仅交互式前台）：首次连接时让人确认指纹并记录，之后指纹变化一律拒绝。
//
// 明文 ws:// 只有显式 --insecure 才允许。

// TrustOptions 是连接服务端的信任配置。
type TrustOptions struct {
	// Pins 是允许的服务端叶子证书 SHA-256 指纹（"sha256:<hex>"），可配置多个以便证书轮换。
	Pins []string
	// Insecure 允许明文 ws://。
	Insecure bool
	// KnownServersPath 非空时启用 TOFU 记录；与 Prompt 配合使用。
	KnownServersPath string
	// Prompt 在 TOFU 首次遇到未知指纹时询问人类是否信任；为 nil 则不做 TOFU。
	Prompt func(host, fingerprint string) bool
}

// Fingerprint 返回证书（DER）的 SHA-256 指纹，格式 "sha256:<小写十六进制>"。
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NormalizePin 接受 "sha256:AB:CD…" / "AB:CD…" / 纯十六进制，统一成 "sha256:<小写十六进制>"。
func NormalizePin(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.ToLower(s), "sha256:")
	s = strings.NewReplacer(":", "", " ", "", "-", "").Replace(s)
	if len(s) != sha256.Size*2 {
		return "", fmt.Errorf("指纹长度不对：应为 64 个十六进制字符（SHA-256）")
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("指纹包含非十六进制字符")
	}
	return "sha256:" + s, nil
}

// Validate 检查配置与目标是否相容（明文地址必须显式 --insecure；指纹必须合法）。
func (o TrustOptions) Validate(t ServerTarget) error {
	if !t.Secure && !o.Insecure {
		return errors.New("拒绝使用明文 ws://：它会把主机的 root shell 暴露给网络上的任何人。请改用 https，或在受控网络内显式加 --insecure")
	}
	for _, p := range o.Pins {
		if _, err := NormalizePin(p); err != nil {
			return fmt.Errorf("--pin %q 无效: %w", p, err)
		}
	}
	return nil
}

// HTTPClient 构造用于 WebSocket 升级请求的 HTTP 客户端（HTTP/1.1，支持代理环境变量）。
func (o TrustOptions) HTTPClient(t ServerTarget) (*http.Client, error) {
	if err := o.Validate(t); err != nil {
		return nil, err
	}
	var cfg *tls.Config
	if t.Secure {
		var err error
		if cfg, err = o.tlsConfig(t); err != nil {
			return nil, err
		}
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSClientConfig:     cfg,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false, // WebSocket 升级必须走 HTTP/1.1
		DisableKeepAlives:   true,
	}}, nil
}

func (o TrustOptions) tlsConfig(t ServerTarget) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if len(o.Pins) > 0 {
		pins := make(map[string]struct{}, len(o.Pins))
		for _, p := range o.Pins {
			n, err := NormalizePin(p)
			if err != nil {
				return nil, err
			}
			pins[n] = struct{}{}
		}
		// 指纹固定取代 CA 与主机名校验；InsecureSkipVerify 只是关闭内置校验，随后由 VerifyConnection 强制校验指纹。
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("服务端没有出示证书")
			}
			fp := Fingerprint(cs.PeerCertificates[0].Raw)
			if _, ok := pins[fp]; !ok {
				return fmt.Errorf("服务端证书指纹 %s 与 --pin 不匹配，已拒绝连接（可能遭遇中间人攻击，或服务端证书已更换）", fp)
			}
			return nil
		}
		return cfg, nil
	}

	if o.KnownServersPath != "" && o.Prompt != nil {
		known := &KnownServers{Path: o.KnownServersPath}
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			return o.verifyTOFU(t, known, cs)
		}
	}
	return cfg, nil
}

// verifyTOFU：系统 CA 能验证则放行；否则按 TOFU 记录比对，首次出现才询问人类。
func (o TrustOptions) verifyTOFU(t ServerTarget, known *KnownServers, cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("服务端没有出示证书")
	}
	leaf := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: cs.ServerName, Intermediates: inter}); err == nil {
		return nil
	}

	fp := Fingerprint(leaf.Raw)
	recorded, found, err := known.Lookup(t.Host)
	if err != nil {
		return fmt.Errorf("读取已信任服务器记录失败: %w", err)
	}
	switch {
	case found && recorded == fp:
		return nil
	case found:
		return fmt.Errorf("服务端证书指纹 %s 与此前信任的记录 %s 不一致，已拒绝连接（可能遭遇中间人攻击；若服务端确实更换了证书，请先删除 %s 中对应的记录）", fp, recorded, known.Path)
	}
	if !o.Prompt(t.Host, fp) {
		return errors.New("未获得对该服务端证书指纹的信任，已取消连接")
	}
	if err := known.Add(t.Host, fp); err != nil {
		return fmt.Errorf("记录已信任的服务器指纹失败: %w", err)
	}
	return nil
}

// KnownServers 是 TOFU 的指纹记录：每行 "host:port sha256:<hex>"。
type KnownServers struct {
	Path string
	mu   sync.Mutex
}

// Lookup 查询主机的已信任指纹。
func (k *KnownServers) Lookup(host string) (fp string, found bool, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	f, err := os.Open(k.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[0] == host {
			fp, found = fields[1], true // 后出现的记录覆盖先出现的
		}
	}
	return fp, found, sc.Err()
}

// Add 追加一条记录（原子写入，文件权限 0600）。
func (k *KnownServers) Add(host, fp string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if strings.ContainsAny(host, " \t\r\n") {
		return errors.New("非法的主机名")
	}
	if err := os.MkdirAll(filepath.Dir(k.Path), 0o700); err != nil {
		return err
	}
	existing, err := os.ReadFile(k.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := string(existing)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += host + " " + fp + "\n"
	return writeFileAtomic(k.Path, []byte(content), 0o600)
}
