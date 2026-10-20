package relsign

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	testVersion  = "v1.7.0"
	testPlatform = "linux-amd64"
)

var testSum = strings.Repeat("ab", 32)

// fixedKey 返回由固定种子生成的密钥对：ed25519 的签名是确定性的，测试结果因此可以逐字节比对。
func fixedKey(seedByte byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seedByte}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

func mustSign(t *testing.T, priv ed25519.PrivateKey, version, platform, sum string) string {
	t.Helper()
	sig, err := Sign(priv, version, platform, sum)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return sig
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	pub, priv := fixedKey(1)
	sig := mustSign(t, priv, testVersion, testPlatform, testSum)
	if !strings.HasPrefix(sig, "rmsig1:"+KeyID(pub)+":") {
		t.Fatalf("签名文本的前缀不对: %s", sig)
	}
	if len(sig) > MaxSignatureLen {
		t.Fatalf("签名文本 %d 个字符，超过上限 %d", len(sig), MaxSignatureLen)
	}
	if err := Verify([]ed25519.PublicKey{pub}, sig, testVersion, testPlatform, testSum); err != nil {
		t.Fatalf("正确的签名应当验过: %v", err)
	}
	if !ValidSignatureText(sig) {
		t.Fatal("ValidSignatureText 应当认可它")
	}
}

// 线上格式一旦发出就不能悄悄改：已经编进 Agent 的验证代码与签名工具靠它对得上。
// 固定种子的 ed25519 签名是确定性的，所以这条用例同时锁住「消息的构成」与「文本的编码」。
func TestSignatureWireFormatIsStable(t *testing.T) {
	pub, priv := fixedKey(7)
	if got, want := KeyID(pub), goldenKeyID; got != want {
		t.Fatalf("KeyID = %s，期望 %s（公钥标识的算法变了？）", got, want)
	}
	if got, want := mustSign(t, priv, "v1.2.3", "linux-arm64", strings.Repeat("0123456789abcdef", 4)), goldenSignature; got != want {
		t.Fatalf("签名文本变了（消息或编码被改动）:\n got  %s\n want %s", got, want)
	}
	msg, err := Message("v1.2.3", "linux-arm64", strings.Repeat("0123456789abcdef", 4))
	if err != nil {
		t.Fatal(err)
	}
	wantMsg := "relaymesh-agent-release/v1\nv1.2.3\nlinux-arm64\n" + strings.Repeat("0123456789abcdef", 4) + "\n"
	if string(msg) != wantMsg {
		t.Fatalf("消息格式变了:\n got  %q\n want %q", msg, wantMsg)
	}
	// 独立验证：不经过本包的 Verify，直接用标准库按文档里的消息格式验这条固定签名。
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(goldenSignature, ":")[2])
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, []byte(wantMsg), raw) {
		t.Fatal("固定签名在标准库里验不过：签名的并不是文档里写明的消息")
	}
}

func TestKeyIDIsHashPrefixOfPublicKey(t *testing.T) {
	pub, _ := fixedKey(3)
	sum := sha256.Sum256(pub)
	if got, want := KeyID(pub), hex.EncodeToString(sum[:8]); got != want {
		t.Fatalf("KeyID = %s，期望 %s", got, want)
	}
	if len(KeyID(pub)) != 16 {
		t.Fatalf("KeyID 应当是 16 个字符: %s", KeyID(pub))
	}
}

// 签名只在（版本、平台、内容）与签发时完全一致时才有效：改任何一项都必须验签失败，并且是「签名不匹配」而不是别的错误。
func TestVerifyRejectsAnyChangedField(t *testing.T) {
	pub, priv := fixedKey(1)
	sig := mustSign(t, priv, testVersion, testPlatform, testSum)
	trusted := []ed25519.PublicKey{pub}

	cases := []struct {
		name                    string
		version, platform, hash string
	}{
		{"换成别的版本号（拿旧签名配新版本）", "v1.7.1", testPlatform, testSum},
		{"换成别的平台", testVersion, "linux-arm64", testSum},
		{"程序内容变了一个字节", testVersion, testPlatform, strings.Repeat("ab", 31) + "ac"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Verify(trusted, sig, c.version, c.platform, c.hash)
			if !errors.Is(err, ErrBadSignature) {
				t.Fatalf("err = %v，期望 ErrBadSignature", err)
			}
		})
	}
}

func TestVerifyRejectsTamperedSignatureBytes(t *testing.T) {
	pub, priv := fixedKey(1)
	sig := mustSign(t, priv, testVersion, testPlatform, testSum)
	parts := strings.Split(sig, ":")
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0x01
	forged := parts[0] + ":" + parts[1] + ":" + base64.RawURLEncoding.EncodeToString(raw)
	if err := Verify([]ed25519.PublicKey{pub}, forged, testVersion, testPlatform, testSum); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v，期望 ErrBadSignature", err)
	}
}

func TestVerifyKeySelection(t *testing.T) {
	pubA, privA := fixedKey(1)
	pubB, privB := fixedKey(2)
	sigA := mustSign(t, privA, testVersion, testPlatform, testSum)
	sigB := mustSign(t, privB, testVersion, testPlatform, testSum)

	t.Run("名单里有多把公钥时按密钥标识选", func(t *testing.T) {
		both := []ed25519.PublicKey{pubA, pubB}
		for _, sig := range []string{sigA, sigB} {
			if err := Verify(both, sig, testVersion, testPlatform, testSum); err != nil {
				t.Fatalf("两把公钥都在名单里，应当验过: %v", err)
			}
		}
	})
	t.Run("签名所用的密钥不在名单里", func(t *testing.T) {
		if err := Verify([]ed25519.PublicKey{pubA}, sigB, testVersion, testPlatform, testSum); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("err = %v，期望 ErrUnknownKey", err)
		}
	})
	t.Run("冒用别人的密钥标识也不行", func(t *testing.T) {
		// 用 B 的私钥签，但把密钥标识改成 A 的：标识选中了 A，A 验不过 → 签名不匹配。
		parts := strings.Split(sigB, ":")
		forged := parts[0] + ":" + KeyID(pubA) + ":" + parts[2]
		if err := Verify([]ed25519.PublicKey{pubA}, forged, testVersion, testPlatform, testSum); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("err = %v，期望 ErrBadSignature", err)
		}
	})
}

func TestVerifyWithoutUsableKeysFailsClosed(t *testing.T) {
	_, priv := fixedKey(1)
	sig := mustSign(t, priv, testVersion, testPlatform, testSum)
	for name, keys := range map[string][]ed25519.PublicKey{
		"nil 名单":    nil,
		"空名单":       {},
		"只有长度不对的公钥": {ed25519.PublicKey("short"), nil},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Verify(keys, sig, testVersion, testPlatform, testSum); !errors.Is(err, ErrNoKeys) {
				t.Fatalf("err = %v，期望 ErrNoKeys", err)
			}
		})
	}
	t.Run("长度不对的公钥被忽略，不影响好的公钥", func(t *testing.T) {
		pub, _ := fixedKey(1)
		if err := Verify([]ed25519.PublicKey{ed25519.PublicKey("short"), pub}, sig, testVersion, testPlatform, testSum); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestVerifyRejectsMalformedSignatureText(t *testing.T) {
	pub, priv := fixedKey(1)
	good := mustSign(t, priv, testVersion, testPlatform, testSum)
	parts := strings.Split(good, ":")
	trusted := []ed25519.PublicKey{pub}

	cases := map[string]string{
		"空":           "",
		"别的前缀":        "rmsig2:" + parts[1] + ":" + parts[2],
		"缺一段":         parts[0] + ":" + parts[2],
		"多一段":         good + ":extra",
		"密钥标识太短":      parts[0] + ":abcd:" + parts[2],
		"密钥标识不是十六进制":  parts[0] + ":zzzzzzzzzzzzzzzz:" + parts[2],
		"密钥标识是大写":     parts[0] + ":" + strings.ToUpper(parts[1]) + ":" + parts[2],
		"签名不是 base64": parts[0] + ":" + parts[1] + ":!!!!",
		"签名太短":        parts[0] + ":" + parts[1] + ":" + parts[2][:40],
		"签名带填充符":      parts[0] + ":" + parts[1] + ":" + parts[2] + "==",
		"超长":          good + strings.Repeat("A", 200),
		"含换行":         good + "\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			err := Verify(trusted, text, testVersion, testPlatform, testSum)
			if !errors.Is(err, ErrBadFormat) {
				t.Fatalf("err = %v，期望 ErrBadFormat", err)
			}
			if ValidSignatureText(text) {
				t.Fatal("ValidSignatureText 不应认可它")
			}
		})
	}
}

func TestMessageRejectsBadFields(t *testing.T) {
	cases := map[string][3]string{
		"版本号为空":       {"", testPlatform, testSum},
		"版本号含换行":      {"v1\n2", testPlatform, testSum},
		"版本号含空格":      {"v1 2", testPlatform, testSum},
		"版本号过长":       {"v" + strings.Repeat("1", 64), testPlatform, testSum},
		"平台为空":        {testVersion, "", testSum},
		"平台含路径":       {testVersion, "../etc-passwd", testSum},
		"平台含大写":       {testVersion, "Linux-amd64", testSum},
		"SHA-256 太短":  {testVersion, testPlatform, "abcd"},
		"SHA-256 含大写": {testVersion, testPlatform, strings.Repeat("AB", 32)},
		"SHA-256 含换行": {testVersion, testPlatform, strings.Repeat("ab", 31) + "a\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Message(c[0], c[1], c[2]); !errors.Is(err, ErrBadFormat) {
				t.Fatalf("err = %v，期望 ErrBadFormat", err)
			}
			_, priv := fixedKey(1)
			if _, err := Sign(priv, c[0], c[1], c[2]); !errors.Is(err, ErrBadFormat) {
				t.Fatalf("Sign err = %v，期望 ErrBadFormat", err)
			}
		})
	}
}

func TestSignRejectsBadPrivateKey(t *testing.T) {
	if _, err := Sign(ed25519.PrivateKey("short"), testVersion, testPlatform, testSum); err == nil {
		t.Fatal("长度不对的私钥应当被拒绝，而不是 panic")
	}
}

func TestPublicKeyTextRoundTrip(t *testing.T) {
	pub, _ := fixedKey(5)
	text := FormatPublicKey(pub)
	if !strings.HasPrefix(text, "rmpub1:") || len(text) != len("rmpub1:")+64 {
		t.Fatalf("公钥文本不对: %s", text)
	}
	for _, in := range []string{text, strings.TrimPrefix(text, "rmpub1:"), "  " + text + "\n"} {
		got, err := ParsePublicKey(in)
		if err != nil {
			t.Fatalf("ParsePublicKey(%q): %v", in, err)
		}
		if !bytes.Equal(got, pub) {
			t.Fatalf("解析结果不一致: %q", in)
		}
	}
	for name, bad := range map[string]string{
		"空":      "",
		"太短":     "rmpub1:abcd",
		"太长":     text + "00",
		"不是十六进制": "rmpub1:" + strings.Repeat("zz", 32),
		"私钥长度":   hex.EncodeToString(bytes.Repeat([]byte{1}, 64)),
	} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Fatalf("%s: 应当被拒绝", name)
		}
	}
}

// 字段写法要和 termproto 里升级请求的字段一致：否则 Hub 放行的值签名工具拒绝（或相反），升级会莫名其妙地失败。
func TestFieldRulesMatchTermproto(t *testing.T) {
	versions := []string{"v1.7.0", "1.7.0", "v1.6.3-dev-20261018-81cbce4", "", "-v1", "v 1", "v1\n", strings.Repeat("a", 64), strings.Repeat("a", 65), "v1/2"}
	for _, v := range versions {
		if got, want := versionRe.MatchString(v), termproto.ValidAgentVersion(v); got != want {
			t.Errorf("版本号 %q：relsign=%v termproto=%v", v, got, want)
		}
	}
	platforms := []string{"linux-amd64", "windows-arm64", "darwin-amd64", "", "linux", "Linux-amd64", "linux-amd64-x", "../x-y", "a-b"}
	for _, p := range platforms {
		if got, want := platformRe.MatchString(p), termproto.SanitizePlatform(p) != ""; got != want {
			t.Errorf("平台 %q：relsign=%v termproto=%v", p, got, want)
		}
	}
	// SHA-256 与升级请求里的写法一致：用一个字段合规的请求验证两边都认同。
	u := termproto.Upgrade{T: termproto.TUpgrade, ID: strings.Repeat("a", 32), Version: testVersion, Ticket: strings.Repeat("b", 32), SHA256: testSum}
	if err := u.Validate(); err != nil {
		t.Fatalf("termproto 不认可测试用的 SHA-256: %v", err)
	}
	if !sha256Re.MatchString(testSum) {
		t.Fatal("relsign 不认可测试用的 SHA-256")
	}
	u.SHA256 = strings.ToUpper(testSum)
	if u.Validate() == nil || sha256Re.MatchString(u.SHA256) {
		t.Fatal("大写的 SHA-256 两边都应当拒绝")
	}
}

// 固定种子 7 对（v1.2.3, linux-arm64, 0123456789abcdef×4）的签名；由 TestSignatureWireFormatIsStable 逐字比对，
// 并且在该用例里用标准库按文档里写明的消息格式独立验过一遍，不是只和本包的实现自洽。
const (
	goldenKeyID     = "fe812c12f3ab4ce6"
	goldenSignature = "rmsig1:fe812c12f3ab4ce6:PpxNN5m9VMBstrHX5YZGDXgtDvHYdL_VZr6TH20Cr5oJTMXwsW6zoS-I9WEqo-aZdFsRq0VGRAVObcJQ3f5bDQ"
)
