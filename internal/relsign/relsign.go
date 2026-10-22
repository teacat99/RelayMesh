// Package relsign 是 Agent 发布产物的独立签名（方案见文档库 design/agent-auto-upgrade.md B.3）。
//
// 它存在的原因：Agent 的「静默自动升级」是无人值守的，只靠 Hub 给出的 SHA-256 不够——Hub 被攻破时，
// 攻击者能同时换掉二进制与它的哈希。所以发布流程用一把只在发布时才用到的私钥给每个 Agent 程序签名，
// Agent 用主机信任的公钥验签：验不过就既不运行也不安装下载到的程序。
//
// 签名绑定的是「版本 + 平台 + 程序内容的 SHA-256」，而不是某个文件名：
//   - 拿旧版本的签名去配新版本号，消息里的版本对不上，验签失败；
//   - 把别的平台的程序当作本平台的，消息里的平台对不上，验签失败；
//   - 程序被改动一个字节，SHA-256 变了，验签失败。
//
// 只用标准库的 ed25519，没有新依赖；这个包只包含验证与签名的原语，密钥文件的保管与读写属于签名工具。
package relsign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	// SignatureScheme 是签名文本的前缀，兼作格式版本：以后要改消息格式就换一个前缀，旧的验证方一律拒绝新格式。
	SignatureScheme = "rmsig1"
	// PublicKeyScheme 是公钥文本的前缀。
	PublicKeyScheme = "rmpub1"
	// MaxSignatureLen 是签名文本的长度上限（实际为 6+1+16+1+86=110 个字符），Hub 与 Agent 都按它限长。
	MaxSignatureLen = 128

	// messageDomain 是签名消息的域分隔前缀：同一把密钥将来用于别的用途时，签名不会被拿来冒充发布签名。
	messageDomain = "relaymesh-agent-release/v1"
	keyIDBytes    = 8
)

var (
	// ErrNoKeys 表示可信公钥名单是空的：此时任何签名都不可能验过。
	ErrNoKeys = errors.New("没有可信的发布公钥")
	// ErrBadFormat 表示签名文本、版本、平台或 SHA-256 的写法不合规。
	ErrBadFormat = errors.New("签名格式不对")
	// ErrUnknownKey 表示签名所用的密钥不在可信名单里。
	ErrUnknownKey = errors.New("签名所用的密钥不在可信名单里")
	// ErrBadSignature 表示签名与（版本、平台、内容）不匹配。
	ErrBadSignature = errors.New("签名与程序不匹配")
)

// 字段写法与 termproto 里升级请求的字段保持一致（relsign_test 里有一致性检查）。
var (
	versionRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	platformRe = regexp.MustCompile(`^[a-z0-9]{2,16}-[a-z0-9]{2,16}$`)
	sha256Re   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keyIDRe    = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// 64 字节的签名按 base64url（无填充）编码恰好是 86 个字符。
	sigBodyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{86}$`)
)

// KeyID 是公钥的短标识（公钥 SHA-256 的前 8 个字节，16 位小写十六进制），写在签名里，
// 让验证方能在信任名单里直接选出对应的公钥——这也是将来轮换密钥（内置主钥与备用钥）时的依据。
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:keyIDBytes])
}

// FormatPublicKey 把公钥写成 rmpub1:<64 位十六进制>。
func FormatPublicKey(pub ed25519.PublicKey) string {
	return PublicKeyScheme + ":" + hex.EncodeToString(pub)
}

// ParsePublicKey 解析公钥文本：rmpub1:<64 位十六进制>，也接受不带前缀的 64 位十六进制。
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw := strings.TrimSpace(s)
	raw = strings.TrimPrefix(raw, PublicKeyScheme+":")
	if len(raw) != 2*ed25519.PublicKeySize {
		return nil, fmt.Errorf("公钥应是 %d 位十六进制（可带 %s: 前缀）", 2*ed25519.PublicKeySize, PublicKeyScheme)
	}
	b, err := hex.DecodeString(raw)
	if err != nil {
		return nil, errors.New("公钥不是合法的十六进制")
	}
	return ed25519.PublicKey(b), nil
}

// Message 是被签名的消息：带域分隔前缀的四行文本。三个字段必须符合各自的写法（不允许换行或空白混进去）。
func Message(version, platform, sha256hex string) ([]byte, error) {
	switch {
	case !versionRe.MatchString(version):
		return nil, fmt.Errorf("%w：版本号写法不合规", ErrBadFormat)
	case !platformRe.MatchString(platform):
		return nil, fmt.Errorf("%w：平台写法不合规", ErrBadFormat)
	case !sha256Re.MatchString(sha256hex):
		return nil, fmt.Errorf("%w：SHA-256 写法不合规", ErrBadFormat)
	}
	var b bytes.Buffer
	b.WriteString(messageDomain)
	b.WriteByte('\n')
	b.WriteString(version)
	b.WriteByte('\n')
	b.WriteString(platform)
	b.WriteByte('\n')
	b.WriteString(sha256hex)
	b.WriteByte('\n')
	return b.Bytes(), nil
}

// Sign 用私钥给（版本、平台、内容的 SHA-256）签名，返回 rmsig1:<密钥标识>:<base64url 签名>。
func Sign(priv ed25519.PrivateKey, version, platform, sha256hex string) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", errors.New("私钥长度不对")
	}
	msg, err := Message(version, platform, sha256hex)
	if err != nil {
		return "", err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return "", errors.New("无法从私钥得到公钥")
	}
	sig := ed25519.Sign(priv, msg)
	return SignatureScheme + ":" + KeyID(pub) + ":" + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseSignature 拆开签名文本，返回密钥标识与原始签名；写法不对时返回 ErrBadFormat。
func parseSignature(text string) (keyID string, sig []byte, err error) {
	if len(text) == 0 || len(text) > MaxSignatureLen {
		return "", nil, fmt.Errorf("%w：长度不合规", ErrBadFormat)
	}
	parts := strings.Split(text, ":")
	if len(parts) != 3 || parts[0] != SignatureScheme {
		return "", nil, fmt.Errorf("%w：不是 %s 格式", ErrBadFormat, SignatureScheme)
	}
	if !keyIDRe.MatchString(parts[1]) {
		return "", nil, fmt.Errorf("%w：密钥标识写法不合规", ErrBadFormat)
	}
	// 先按字符集与长度卡死再解码：base64 解码器会悄悄忽略换行，不先卡住的话「带换行的签名」也能解出来。
	if !sigBodyRe.MatchString(parts[2]) {
		return "", nil, fmt.Errorf("%w：签名内容不合规", ErrBadFormat)
	}
	sig, derr := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if derr != nil || len(sig) != ed25519.SignatureSize {
		return "", nil, fmt.Errorf("%w：签名内容不合规", ErrBadFormat)
	}
	return parts[1], sig, nil
}

// ValidSignatureText 只检查签名文本的写法（不验签）：Hub 用它在转交之前挡掉明显不合规的内容，
// 它没有任何公钥，也不应该有。
func ValidSignatureText(text string) bool {
	_, _, err := parseSignature(text)
	return err == nil
}

// Verify 用可信公钥名单验证签名。名单为空返回 ErrNoKeys；签名文本不合规返回 ErrBadFormat；
// 签名所用密钥不在名单里返回 ErrUnknownKey；与（版本、平台、内容）对不上返回 ErrBadSignature。
// 长度不对的公钥会被忽略（不会让验证崩溃）；它们不计入「名单非空」。
func Verify(trusted []ed25519.PublicKey, signature, version, platform, sha256hex string) error {
	usable := make([]ed25519.PublicKey, 0, len(trusted))
	for _, k := range trusted {
		if len(k) == ed25519.PublicKeySize {
			usable = append(usable, k)
		}
	}
	if len(usable) == 0 {
		return ErrNoKeys
	}
	id, sig, err := parseSignature(signature)
	if err != nil {
		return err
	}
	msg, err := Message(version, platform, sha256hex)
	if err != nil {
		return err
	}
	matched := false
	for _, k := range usable {
		if KeyID(k) != id {
			continue
		}
		matched = true
		if ed25519.Verify(k, msg, sig) {
			return nil
		}
	}
	if !matched {
		return ErrUnknownKey
	}
	return ErrBadSignature
}
