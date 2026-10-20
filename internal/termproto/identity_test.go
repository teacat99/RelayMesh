package termproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

// 金标准向量：由独立实现（Python cryptography + hashlib）计算，而非由被测代码自身生成。
// 它们固化了「私钥种子 → 公钥 → 机器码」与「域前缀 ‖ nonce → 签名」的派生方式；
// 任何不兼容的改动（域前缀、截断位数、字母表、分组）都会让这里失败。
var goldenVectors = []struct {
	seed, pub, code, sig string
}{
	{
		seed: "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		pub:  "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8",
		code: "RM-H3B4-5AW2-4DPP-ZAW6",
		sig:  "cc31f848192a710c891d69a99eba00a05a551ca50343932d8cd2d5828e85fed69d6ca85b8d3a84820abd72aa933e2eb99535aa8cd537edd03229a8c67c641a0b",
	},
	{
		seed: "0000000000000000000000000000000000000000000000000000000000000000",
		pub:  "3b6a27bcceb6a42d62a3a8d02a6f0d73653215771de243a63ac048a18b59da29",
		code: "RM-B4P1-CA5W-GS1M-Y41V",
		sig:  "27c56f496b658f29099a551bcedbbe47f714996e1f0f99b719a637b760b3f70b5e4a6b1ff6abbfc24ed1acdc7e24143d73480323dc8e9ba9f7f9e5f97af83201",
	},
	{
		seed: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		pub:  "76a1592044a6e4f511265bca73a604d90b0529d1df602be30a19a9257660d1f5",
		code: "RM-C0N0-NWVQ-M6Q6-VMY0",
		sig:  "6dd366d142a56c487ec424c6d12ca41fb95b9a509619ad15257e34421a1ec8dc4cb7bc6ff420050205c4552d1167a77f3a50bb9efeef0d3bbcdd12e47ee2eb0c",
	},
}

func goldenNonce() []byte {
	n := make([]byte, NonceSize)
	for i := range n {
		n[i] = byte(32 + i)
	}
	return n
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGoldenVectors(t *testing.T) {
	for _, v := range goldenVectors {
		priv := ed25519.NewKeyFromSeed(mustHex(t, v.seed))
		pub := priv.Public().(ed25519.PublicKey)

		if got := hex.EncodeToString(pub); got != v.pub {
			t.Fatalf("seed %s…: 公钥 = %s，期望 %s", v.seed[:8], got, v.pub)
		}
		if got := MachineCode(pub); got != v.code {
			t.Fatalf("seed %s…: 机器码 = %s，期望 %s", v.seed[:8], got, v.code)
		}
		sig := SignChallenge(priv, goldenNonce())
		if got := hex.EncodeToString(sig); got != v.sig {
			t.Fatalf("seed %s…: 签名 = %s，期望 %s", v.seed[:8], got, v.sig)
		}
		if !VerifyChallenge(pub, goldenNonce(), mustHex(t, v.sig)) {
			t.Fatalf("seed %s…: 金标准签名应通过校验", v.seed[:8])
		}
	}
}

func TestMachineCodeShape(t *testing.T) {
	for _, v := range goldenVectors {
		if len(v.code) != 22 || !strings.HasPrefix(v.code, "RM-") {
			t.Fatalf("机器码形态不符: %q", v.code)
		}
		parts := strings.Split(v.code, "-")
		if len(parts) != 5 {
			t.Fatalf("应为 RM + 四组: %q", v.code)
		}
		for _, p := range parts[1:] {
			if len(p) != 4 {
				t.Fatalf("每组应为 4 字符: %q", v.code)
			}
			for _, c := range p {
				if !strings.ContainsRune(crockford, c) {
					t.Fatalf("含非 Crockford 字符 %q: %q", c, v.code)
				}
			}
		}
	}
}

func TestMachineCodeIsKeyDependent(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		seed := make([]byte, 32)
		seed[0], seed[1] = byte(i), byte(i>>8)
		pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		code := MachineCode(pub)
		if seen[code] {
			t.Fatalf("机器码冲突（80 位空间中 200 个样本不应碰撞）: %s", code)
		}
		seen[code] = true
	}
}

func TestNormalizeMachineCode(t *testing.T) {
	const canonical = "RM-H3B4-5AW2-4DPP-ZAW6"
	ok := []string{
		"RM-H3B4-5AW2-4DPP-ZAW6",
		"rm-h3b4-5aw2-4dpp-zaw6",
		"  RM-H3B4-5AW2-4DPP-ZAW6\n",
		"H3B4-5AW2-4DPP-ZAW6",
		"H3B45AW24DPPZAW6",
		"RMH3B45AW24DPPZAW6",
		"rm h3b4 5aw2 4dpp zaw6",
		"RM_H3B4_5AW2_4DPP_ZAW6",
	}
	for _, in := range ok {
		got, err := NormalizeMachineCode(in)
		if err != nil || got != canonical {
			t.Fatalf("Normalize(%q) = %q, %v，期望 %q", in, got, err, canonical)
		}
	}

	// Crockford 易混字符映射：O→0，I/L→1
	if g, err := NormalizeMachineCode("RM-CONO-NWVQ-M6Q6-VMYO"); err != nil || g != "RM-C0N0-NWVQ-M6Q6-VMY0" {
		t.Fatalf("O 应映射为 0: %q %v", g, err)
	}
	if g, err := NormalizeMachineCode("rm-conO-nwvq-m6q6-vmyo"); err != nil || g != "RM-C0N0-NWVQ-M6Q6-VMY0" {
		t.Fatalf("小写 o 应映射为 0: %q %v", g, err)
	}
	if g, err := NormalizeMachineCode("RM-B4P1-CA5W-GS1M-Y41V"); err != nil || g != "RM-B4P1-CA5W-GS1M-Y41V" {
		t.Fatalf("规范码应保持不变: %q %v", g, err)
	}
	if g, err := NormalizeMachineCode("RM-B4PI-CA5W-GSLM-Y4IV"); err != nil || g != "RM-B4P1-CA5W-GS1M-Y41V" {
		t.Fatalf("I/L 应映射为 1: %q %v", g, err)
	}

	bad := []string{
		"", "RM-", "RM-H3B4", "RM-H3B4-5AW2-4DPP-ZAW", "RM-H3B4-5AW2-4DPP-ZAW66",
		"RM-H3B4-5AW2-4DPP-ZAWU", // U 不在字母表
		"RM-H3B4-5AW2-4DPP-ZAW!", "XX-H3B4-5AW2-4DPP-ZAW6", "RM-H3B4-5AW2-4DPP-ZAW6-EXTRA",
		strings.Repeat("A", 500), "RM-H3B4-5AW2-4DPP-ZAW6\x00",
	}
	for _, in := range bad {
		if _, err := NormalizeMachineCode(in); err == nil {
			t.Fatalf("Normalize(%q) 应失败", in)
		}
	}
}

// 回归：净荷本身以 R、M 开头时，前缀剥离不能误伤净荷。
func TestNormalizeMachineCodePayloadStartingWithRM(t *testing.T) {
	got, err := NormalizeMachineCode("RM-RM12-3456-789A-BCDE")
	if err != nil || got != "RM-RM12-3456-789A-BCDE" {
		t.Fatalf("带前缀输入: %q %v", got, err)
	}
	got, err = NormalizeMachineCode("RM12-3456-789A-BCDE")
	if err != nil || got != "RM-RM12-3456-789A-BCDE" {
		t.Fatalf("省略前缀且净荷以 RM 开头: %q %v", got, err)
	}
}

func TestNormalizeRoundTripsGeneratedCodes(t *testing.T) {
	for i := 0; i < 100; i++ {
		seed := make([]byte, 32)
		seed[5] = byte(i)
		code := MachineCode(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
		got, err := NormalizeMachineCode(strings.ToLower(code))
		if err != nil || got != code {
			t.Fatalf("规整应幂等: %q -> %q %v", code, got, err)
		}
	}
}

func TestVerifyChallengeRejectsTampering(t *testing.T) {
	v := goldenVectors[0]
	priv := ed25519.NewKeyFromSeed(mustHex(t, v.seed))
	pub := priv.Public().(ed25519.PublicKey)
	nonce := goldenNonce()
	sig := SignChallenge(priv, nonce)

	// 重放到另一个 nonce（即另一条连接）必须失败
	other := bytes.Clone(nonce)
	other[0] ^= 1
	if VerifyChallenge(pub, other, sig) {
		t.Fatal("签名不得在不同 nonce 下通过（重放）")
	}
	// 篡改签名
	bad := bytes.Clone(sig)
	bad[10] ^= 0x80
	if VerifyChallenge(pub, nonce, bad) {
		t.Fatal("篡改的签名不得通过")
	}
	// 换公钥（冒用他人机器码）
	otherPub := ed25519.NewKeyFromSeed(mustHex(t, goldenVectors[1].seed)).Public().(ed25519.PublicKey)
	if VerifyChallenge(otherPub, nonce, sig) {
		t.Fatal("他人公钥不得通过")
	}
	// 直接对 nonce（不带域前缀）签名的签名不得被接受，保证域分隔生效
	raw := ed25519.Sign(priv, nonce)
	if VerifyChallenge(pub, nonce, raw) {
		t.Fatal("缺少域前缀的签名不得通过")
	}
	// 长度异常
	if VerifyChallenge(pub[:31], nonce, sig) || VerifyChallenge(pub, nonce[:31], sig) || VerifyChallenge(pub, nonce, sig[:63]) || VerifyChallenge(nil, nil, nil) {
		t.Fatal("长度异常必须直接失败且不得 panic")
	}
}

func TestNewNonceIsRandom(t *testing.T) {
	a, err := NewNonce()
	if err != nil || len(a) != NonceSize {
		t.Fatalf("nonce: %v len=%d", err, len(a))
	}
	b, _ := NewNonce()
	if bytes.Equal(a, b) {
		t.Fatal("两次 nonce 不应相同")
	}
}

func TestKeyEncoding(t *testing.T) {
	v := goldenVectors[0]
	pub := mustHex(t, v.pub)
	enc := EncodeKey(pub)
	if enc != "A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg" {
		t.Fatalf("base64url 编码不符: %s", enc)
	}
	dec, err := DecodeKey(enc, 32)
	if err != nil || !bytes.Equal(dec, pub) {
		t.Fatalf("往返失败: %v", err)
	}
	for _, bad := range []string{"", "!!!", enc[:40], enc + "AA", enc + "="} {
		if _, err := DecodeKey(bad, 32); err == nil {
			t.Fatalf("DecodeKey(%q) 应失败", bad)
		}
	}
}
