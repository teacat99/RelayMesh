package termproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	// machineCodeDomain 是机器码派生的域分隔前缀，保证同一公钥在别处的哈希不会与机器码混用。
	machineCodeDomain = "relaymesh-machine-code-v1\x00"
	// authDomain 是挑战签名的域分隔前缀：Agent 只对「前缀 + 服务端给的一次性 nonce」签名，
	// 绝不会被诱导去签别的东西（例如某个 TLS 会话或交易数据）。
	authDomain = "relaymesh-agent-auth-v1\x00"

	// NonceSize 是服务端挑战随机数的字节数。
	NonceSize = 32
	// crockford 是 Crockford Base32 字母表（无 I L O U，避免视觉混淆）。
	crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	// machineCodeChars 是机器码净荷字符数：80 位 / 每字符 5 位 = 16。
	machineCodeChars = 16
	// MachineCodePrefix 是机器码前缀。
	MachineCodePrefix = "RM-"
)

// ErrBadMachineCode 表示字符串不是合法的机器码。
var ErrBadMachineCode = errors.New("invalid machine code")

// MachineCode 由 Ed25519 公钥派生机器码：
// "RM-" + SHA-256("relaymesh-machine-code-v1\0" ‖ 公钥) 前 80 位的 Crockford Base32，4-4-4-4 分组。
//
// 机器码是公钥指纹而非随机数或系统 machine-id：码本身可以公开出现在界面与日志里，
// 而冒用它需要对应私钥，因此「看到机器码」不等于「能冒充该主机」。
func MachineCode(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(append([]byte(machineCodeDomain), pub...))
	// 取前 10 字节（80 位）编码为 16 个 5 位字符。
	var acc uint64
	var bits uint
	out := make([]byte, 0, machineCodeChars+3)
	emitted := 0
	for _, b := range sum[:10] {
		acc = acc<<8 | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, crockford[(acc>>bits)&0x1f])
			emitted++
			if emitted%4 == 0 && emitted < machineCodeChars {
				out = append(out, '-')
			}
		}
	}
	return MachineCodePrefix + string(out)
}

// RandomMachineCode 生成一个随机的机器码（同样的 RM- 格式，因而令牌白名单、校验与界面都能直接复用）。
// 它用于没有 Agent、也就没有密钥对可以派生机器码的主机（SSH 主机）；唯一性由调用方在落库时保证。
func RandomMachineCode() (string, error) {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	var acc uint64
	var bits uint
	out := make([]byte, 0, machineCodeChars+3)
	emitted := 0
	for _, b := range raw {
		acc = acc<<8 | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, crockford[(acc>>bits)&0x1f])
			emitted++
			if emitted%4 == 0 && emitted < machineCodeChars {
				out = append(out, '-')
			}
		}
	}
	return MachineCodePrefix + string(out), nil
}

// ValidMachineCode 判断 s 是否已经是规范形式的机器码（不做任何宽松规整）。
func ValidMachineCode(s string) bool {
	n, err := NormalizeMachineCode(s)
	return err == nil && n == s
}

// NormalizeMachineCode 把用户输入规整为规范形式并校验：
// 大小写不敏感，忽略空白与连字符，按 Crockford 规则把易混字符映射（O→0，I/L→1），前缀 RM 可省略。
func NormalizeMachineCode(input string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(input))
	var payload []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '-', ' ', '\t', '_':
			continue
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if strings.IndexByte(crockford, c) < 0 {
			return "", ErrBadMachineCode
		}
		payload = append(payload, c)
	}
	// 完整输入（含 RM 前缀）清洗后是 18 个字符；省略前缀时是 16 个。
	// 不能先无条件剥掉 "RM"：净荷本身可能恰好以 R、M 开头。
	switch len(payload) {
	case machineCodeChars + 2:
		if payload[0] != 'R' || payload[1] != 'M' {
			return "", ErrBadMachineCode
		}
		payload = payload[2:]
	case machineCodeChars:
	default:
		return "", ErrBadMachineCode
	}
	var b strings.Builder
	b.WriteString(MachineCodePrefix)
	for i, c := range payload {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

// NewNonce 生成服务端挑战用的随机数。
func NewNonce() ([]byte, error) {
	n := make([]byte, NonceSize)
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return n, nil
}

// SignChallenge 用私钥对「域前缀 ‖ nonce」签名。
func SignChallenge(priv ed25519.PrivateKey, nonce []byte) []byte {
	return ed25519.Sign(priv, challengeMessage(nonce))
}

// VerifyChallenge 校验签名。pub 与 sig 的长度必须严格符合 Ed25519，否则直接判定失败（不 panic）。
func VerifyChallenge(pub ed25519.PublicKey, nonce, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || len(nonce) != NonceSize {
		return false
	}
	return ed25519.Verify(pub, challengeMessage(nonce), sig)
}

func challengeMessage(nonce []byte) []byte {
	msg := make([]byte, 0, len(authDomain)+len(nonce))
	msg = append(msg, authDomain...)
	return append(msg, nonce...)
}

// EncodeKey / DecodeKey 用 base64url（无填充）在 JSON 与库中表示公钥、签名与 nonce。
func EncodeKey(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// DecodeKey 解码 base64url；要求 want 字节长度精确匹配，防止截断或超长输入。
func DecodeKey(s string, want int) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != want {
		return nil, errors.New("unexpected key length")
	}
	return b, nil
}
