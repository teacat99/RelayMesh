package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// 密封（可逆加密）与票据 / 解锁令牌不同：票据只需要校验，所以只存摘要；
// 而 SSH 主机凭据必须在每次连接时还原成明文，所以只能加密保存。密封用于把这类「必须可还原」的敏感值
// 写进数据库时，保证拿到数据库文件的人无法直接读出它们。
//
// 设计要点：
//   - 主密钥由运维通过环境变量单独提供（与 JWT 密钥彼此独立：JWT 密钥泄露不应连带暴露主机凭据，反之亦然）；
//   - 用 HKDF-SHA256 按「用途」派生子密钥，不同用途之间互不通用；
//   - AES-256-GCM，随机 96 位 nonce，每次密封都不同；
//   - AAD 绑定调用方给定的上下文（例如机器码），密文被搬到别的行就无法解开；
//   - 密文自带版本前缀，将来更换算法时可以平滑迁移。

// sealVersion 是密文的版本前缀。
const sealVersion = "v1:"

// ErrSealOpen 表示密文无法解开：密钥不对、被篡改、上下文不一致或格式不合法。
// 故意不区分具体原因，避免给攻击者提供区分信息。
var ErrSealOpen = errors.New("secure: 无法解开密文")

// ErrSealKey 表示主密钥不可用（为空）。
var ErrSealKey = errors.New("secure: 缺少密封主密钥")

// Sealer 用主密钥派生出的子密钥对敏感值做 AEAD 加解密，可并发使用。
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer 用主密钥与用途派生子密钥。主密钥强度由调用方在配置层校验。
func NewSealer(masterKey, purpose string) (*Sealer, error) {
	if masterKey == "" {
		return nil, ErrSealKey
	}
	key, err := hkdf.Key(sha256.New, []byte(masterKey), []byte("relaymesh/seal/salt/v1"), "relaymesh/seal/"+purpose, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal 加密 plaintext，aad 为绑定的上下文（解密时必须一致）。返回可直接存入文本列的字符串。
func (s *Sealer) Seal(plaintext []byte, aad string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, plaintext, []byte(aad))
	return sealVersion + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open 解开 Seal 的输出。任何失败都返回 ErrSealOpen。
func (s *Sealer) Open(sealed, aad string) ([]byte, error) {
	if !strings.HasPrefix(sealed, sealVersion) {
		return nil, ErrSealOpen
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, sealVersion))
	if err != nil || len(raw) < s.aead.NonceSize()+s.aead.Overhead() {
		return nil, ErrSealOpen
	}
	nonce, ct := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	pt, err := s.aead.Open(nil, nonce, ct, []byte(aad))
	if err != nil {
		return nil, ErrSealOpen
	}
	return pt, nil
}
