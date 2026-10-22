package sshexec

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// 连接与执行的默认参数。
const (
	DefaultDialTimeout = 10 * time.Second
	// DefaultMaxOutput 是一次会话里每一路输出在内存中的上限：本包只运行短脚本，远端不应该返回大量数据。
	DefaultMaxOutput = 2 << 20
	clientVersion    = "SSH-2.0-RelayMesh"
)

// NormalizeAddr 把管理员输入的地址规整成 host:port（默认 22 端口）。
// 拒绝 user@host、URL、空白与控制字符：用户名单独配置，目标 ID 之外的地址永远不会来自模型。
func NormalizeAddr(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 255 {
		return "", fmt.Errorf("%w：为空或过长", ErrBadAddress)
	}
	if strings.ContainsAny(s, " \t\r\n\x00/@\\?#%") {
		return "", fmt.Errorf("%w：只能是 主机名 / IP 或 主机:端口", ErrBadAddress)
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		host, port = s, "22"
		if strings.Contains(s, ":") && net.ParseIP(s) == nil { // 既不是 host:port，也不是不带方括号的 IPv6
			return "", fmt.Errorf("%w：格式不正确", ErrBadAddress)
		}
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "", fmt.Errorf("%w：缺少主机", ErrBadAddress)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%w：端口必须在 1~65535", ErrBadAddress)
	}
	if net.ParseIP(host) == nil && !validHostname(host) {
		return "", fmt.Errorf("%w：主机名含非法字符", ErrBadAddress)
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

func validHostname(h string) bool {
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return false
			}
		}
	}
	return true
}

// ValidUser 检查 SSH 用户名：只允许常见的登录名字符。
func ValidUser(u string) bool {
	if u == "" || len(u) > 64 {
		return false
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '$':
		default:
			return false
		}
	}
	return true
}

// HostKey 是被探测到 / 被固定的主机公钥。
type HostKey struct {
	Type        string        // 例如 ssh-ed25519
	Key         ssh.PublicKey // 公钥本体
	Fingerprint string        // SHA256:xxxx（与 ssh-keygen -l 的输出一致）
}

// NewHostKey 由公钥生成描述。
func NewHostKey(k ssh.PublicKey) *HostKey {
	return &HostKey{Type: k.Type(), Key: k, Fingerprint: ssh.FingerprintSHA256(k)}
}

// Encode 把公钥编码成可存库的文本（标准 base64）。
func (h *HostKey) Encode() string { return base64.StdEncoding.EncodeToString(h.Key.Marshal()) }

// DecodeHostKey 还原 Encode 的输出。
func DecodeHostKey(s string) (*HostKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, ErrNoHostKey
	}
	k, err := ssh.ParsePublicKey(raw)
	if err != nil {
		return nil, ErrNoHostKey
	}
	return NewHostKey(k), nil
}

// 探测时按偏好顺序提供的主机密钥算法：不含 SHA-1 的 ssh-rsa 与 DSA。
var probeAlgos = []string{
	ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

// algorithmsFor 返回固定了某把主机密钥之后允许协商的算法：只允许这把密钥的类型。
// 否则服务器同时有多把密钥时，可能因为协商到另一把而被误判为「主机密钥变了」。
func algorithmsFor(k ssh.PublicKey) []string {
	if k.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{k.Type()}
}

var errProbeDone = errors.New("probe done")

func dialTCP(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w：%s", ErrUnreachable, scrubNetErr(err))
	}
	return conn, nil
}

// scrubNetErr 只保留网络错误里对排错有用的部分。
func scrubNetErr(err error) string {
	var ne *net.OpError
	if errors.As(err, &ne) && ne.Err != nil {
		return ne.Op + ": " + ne.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时"
	}
	return err.Error()
}

// Probe 只做密钥交换，取得服务器的主机公钥，不发送任何认证信息。
func Probe(ctx context.Context, addr string, timeout time.Duration) (*HostKey, error) {
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User:              "relaymesh-probe",
		HostKeyAlgorithms: probeAlgos,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errProbeDone
		},
		Timeout:       timeout,
		ClientVersion: clientVersion,
	}
	conn, err := dialTCP(ctx, addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	_, _, _, herr := ssh.NewClientConn(conn, addr, cfg)
	if got == nil {
		if herr == nil {
			herr = errors.New("握手异常结束")
		}
		return nil, fmt.Errorf("%w：对端没有给出 SSH 主机密钥（%s）", ErrUnreachable, scrubNetErr(herr))
	}
	return NewHostKey(got), nil
}

// Spec 描述如何连接一台主机。HostKey 必须有值：没有固定的主机密钥时拒绝连接。
type Spec struct {
	Addr    string
	User    string
	Cred    Credential
	HostKey *HostKey
	// Via 是承载网关的机器码：非空表示这台主机只能经那台网关出站连接，凭据也只保存在网关上。
	// 本包自身从不使用它，但 Dial 会拒绝 Via 非空的规格（ErrViaGateway）——「Hub 不得绕过网关自己去连」
	// 因此是底层保证，而不只是调用方的约定。
	Via string
}

// String 不含凭据。
func (s Spec) String() string { return s.User + "@" + s.Addr }

// Client 是一条已认证的 SSH 连接。
type Client struct {
	c *ssh.Client
}

// Dial 建立到 sp 的连接：主机密钥必须与固定值逐字节一致，否则返回 ErrHostKeyMismatch。
func Dial(ctx context.Context, sp Spec, timeout time.Duration) (*Client, error) {
	if sp.Via != "" {
		return nil, ErrViaGateway
	}
	if sp.HostKey == nil || sp.HostKey.Key == nil {
		return nil, ErrNoHostKey
	}
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	methods, err := sp.Cred.authMethods()
	if err != nil {
		return nil, err
	}
	want := sp.HostKey.Key.Marshal()
	mismatch := false
	cfg := &ssh.ClientConfig{
		User:              sp.User,
		Auth:              methods,
		HostKeyAlgorithms: algorithmsFor(sp.HostKey.Key),
		HostKeyCallback: func(_ string, _ net.Addr, got ssh.PublicKey) error {
			if subtle.ConstantTimeCompare(want, got.Marshal()) == 1 {
				return nil
			}
			mismatch = true
			return ErrHostKeyMismatch
		},
		Timeout:       timeout,
		ClientVersion: clientVersion,
	}
	conn, err := dialTCP(ctx, sp.Addr, timeout)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(timeout))
	sc, chans, reqs, err := ssh.NewClientConn(conn, sp.Addr, cfg)
	stopped := stop()
	if err != nil {
		_ = conn.Close()
		switch {
		case mismatch:
			return nil, ErrHostKeyMismatch
		case !stopped || ctx.Err() != nil:
			return nil, fmt.Errorf("%w：已取消", ErrUnreachable)
		case isAuthRejected(err):
			return nil, ErrAuthFailed
		}
		return nil, fmt.Errorf("%w：%s", ErrUnreachable, scrubNetErr(err))
	}
	_ = conn.SetDeadline(time.Time{})
	return &Client{c: ssh.NewClient(sc, chans, reqs)}, nil
}

// isAuthRejected 判断握手失败是不是「服务器拒绝了我们提交的凭据」。
//
// 所有认证方法都失败时，x/crypto 返回 "unable to authenticate"。但如果最后一个被尝试的方法是
// keyboard-interactive，而服务器（例如没有启用 PAM 的 OpenSSH）声明支持它、却不发任何提问就直接回
// USERAUTH_FAILURE（消息类型 51），x/crypto 会把这个协议层的意外当作最终错误，报
// "unexpected message type 51 (expected 60)"。两种都是认证失败，不能当作网络不可达来提示用户。
func isAuthRejected(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "unexpected message type 51 ")
}

// Close 关闭连接。
func (c *Client) Close() error { return c.c.Close() }

// Wait 在连接结束时返回，用于连接池及时丢弃已断开的连接。
func (c *Client) wait() error { return c.c.Wait() }

// Result 是一次会话的结果。
type Result struct {
	Stdout []byte
	Stderr []byte
	Exit   int
}

type limitedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) <= room {
			b.buf.Write(p)
		} else {
			b.buf.Write(p[:room])
			b.over = true
		}
	} else if len(p) > 0 {
		b.over = true
	}
	return len(p), nil // 超出上限的部分直接丢弃，不阻塞远端
}

// Exec 在新会话里运行 cmd，stdin 作为标准输入。非 0 退出码是正常结果（Result.Exit），不是错误。
func (c *Client) Exec(ctx context.Context, cmd string, stdin []byte, maxOut int) (Result, error) {
	if maxOut <= 0 {
		maxOut = DefaultMaxOutput
	}
	sess, err := c.c.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("%w：%v", ErrConn, err)
	}
	defer sess.Close()
	out, errb := &limitedBuffer{max: maxOut}, &limitedBuffer{max: maxOut}
	sess.Stdout, sess.Stderr = out, errb
	if len(stdin) > 0 {
		sess.Stdin = bytes.NewReader(stdin)
	}
	if err := sess.Start(cmd); err != nil {
		return Result{}, fmt.Errorf("%w：%v", ErrConn, err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-ctx.Done():
		_ = sess.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return Result{}, ctx.Err()
	}
	res := Result{Stdout: out.buf.Bytes(), Stderr: errb.buf.Bytes()}
	if out.over || errb.over {
		return res, ErrTooLarge
	}
	var ee *ssh.ExitError
	var em *ssh.ExitMissingError
	switch {
	case werr == nil:
		return res, nil
	case errors.As(werr, &ee):
		res.Exit = ee.ExitStatus()
		return res, nil
	case errors.As(werr, &em):
		return res, fmt.Errorf("%w：会话结束但没有退出状态", ErrConn)
	}
	return res, fmt.Errorf("%w：%v", ErrConn, werr)
}
