// Package sshexec 是 RelayMesh 的 SSH 执行后端的传输层：严格固定主机密钥的连接、会话执行、按主机复用的连接池，
// 以及在远端主机上启动 / 轮询 / 读取 / 取消「后台任务」的 POSIX 脚本。
//
// 本包不知道 Hub 的运行状态机、闸门和数据库：它只负责「安全地把一小段脚本送到一台已确认身份的主机上并取回结果」。
// 凭据的加密保存、授权与审计都在调用方（internal/terminal）。
package sshexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// 认证方式。
const (
	AuthPassword = "password"
	AuthKey      = "key"
)

// 凭据大小上限：私钥文本通常只有几 KiB，更大的输入一定不是正常的私钥。
const (
	maxPasswordBytes   = 1024
	maxPrivateKeyBytes = 16 << 10
	maxPassphraseBytes = 1024
)

// 稳定的错误值：调用方用 errors.Is 判断，并映射成稳定的错误码。
var (
	ErrHostKeyMismatch = errors.New("主机密钥与已固定的不一致")
	ErrAuthFailed      = errors.New("SSH 认证失败")
	ErrUnreachable     = errors.New("无法连接到 SSH 主机")
	ErrPassphrase      = errors.New("私钥已加密，需要提供口令")
	ErrBadCredential   = errors.New("凭据格式无效")
	ErrBadAddress      = errors.New("地址无效")
	ErrTooLarge        = errors.New("远端返回的数据超过上限")
	ErrUnsupported     = errors.New("目标主机不是受支持的 POSIX 环境")
	ErrNoHostKey       = errors.New("没有固定的主机密钥，拒绝连接")
	ErrProtocol        = errors.New("远端返回了无法解析的内容")
	ErrConn            = errors.New("SSH 连接中断")
	// ErrViaGateway：这份规格属于经网关接入的主机（Spec.Via 非空），只能由网关出站连接，本进程不得直连。
	ErrViaGateway = errors.New("这台 SSH 主机经网关主机接入，不能由本机直连")
)

// Credential 是连接一台主机所需的认证材料。它永远不应该被打印：String / GoString 都做了脱敏。
type Credential struct {
	Kind       string `json:"kind"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// String 脱敏输出，避免凭据经 %v / %+v 误入日志。
func (c Credential) String() string { return "ssh.Credential{kind=" + c.Kind + ", secret=<redacted>}" }

// GoString 脱敏输出（%#v）。
func (c Credential) GoString() string { return c.String() }

// Validate 检查凭据的格式（私钥会被真正解析一次，口令不对 / 缺失会被发现）。
func (c Credential) Validate() error {
	switch c.Kind {
	case AuthPassword:
		switch {
		case c.Password == "":
			return fmt.Errorf("%w：密码为空", ErrBadCredential)
		case len(c.Password) > maxPasswordBytes:
			return fmt.Errorf("%w：密码过长", ErrBadCredential)
		case strings.ContainsRune(c.Password, 0):
			return fmt.Errorf("%w：密码含非法字符", ErrBadCredential)
		case c.PrivateKey != "" || c.Passphrase != "":
			return fmt.Errorf("%w：密码方式不应携带私钥", ErrBadCredential)
		}
		return nil
	case AuthKey:
		if c.Password != "" {
			return fmt.Errorf("%w：私钥方式不应携带密码", ErrBadCredential)
		}
		if len(c.Passphrase) > maxPassphraseBytes || strings.ContainsRune(c.Passphrase, 0) {
			return fmt.Errorf("%w：口令无效", ErrBadCredential)
		}
		_, err := c.signer()
		return err
	}
	return fmt.Errorf("%w：认证方式只能是 password 或 key", ErrBadCredential)
}

// signer 解析私钥。错误信息只描述类别，不回显任何材料。
func (c Credential) signer() (ssh.Signer, error) {
	pem := strings.TrimSpace(c.PrivateKey)
	if pem == "" {
		return nil, fmt.Errorf("%w：私钥为空", ErrBadCredential)
	}
	if len(pem) > maxPrivateKeyBytes {
		return nil, fmt.Errorf("%w：私钥过大", ErrBadCredential)
	}
	var (
		signer ssh.Signer
		err    error
	)
	if c.Passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(pem), []byte(c.Passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(pem))
	}
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, ErrPassphrase
		}
		if c.Passphrase != "" && strings.Contains(err.Error(), "decryption") {
			return nil, fmt.Errorf("%w：口令不正确", ErrBadCredential)
		}
		return nil, fmt.Errorf("%w：无法解析私钥", ErrBadCredential)
	}
	return signer, nil
}

// authMethods 生成 ssh 客户端的认证方法。
func (c Credential) authMethods() ([]ssh.AuthMethod, error) {
	switch c.Kind {
	case AuthPassword:
		if c.Password == "" {
			return nil, fmt.Errorf("%w：密码为空", ErrBadCredential)
		}
		pw := c.Password
		return []ssh.AuthMethod{
			ssh.Password(pw),
			// 不少服务器把「密码」实现成 keyboard-interactive：用同一个密码应答所有提问。
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}),
		}, nil
	case AuthKey:
		s, err := c.signer()
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(s)}, nil
	}
	return nil, fmt.Errorf("%w：认证方式无效", ErrBadCredential)
}

// Marshal 序列化成待密封的字节。
func (c Credential) Marshal() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

// ParseCredential 还原 Marshal 的输出（不做私钥解析，解析发生在真正连接时）。
func ParseCredential(b []byte) (Credential, error) {
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return Credential{}, ErrBadCredential
	}
	if c.Kind != AuthPassword && c.Kind != AuthKey {
		return Credential{}, ErrBadCredential
	}
	return c, nil
}
