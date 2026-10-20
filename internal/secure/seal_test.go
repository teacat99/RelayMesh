package secure

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const testMasterKey = "Qm9ndXMtbWFzdGVyLWtleS1mb3ItdGVzdGluZy1vbmx5LTEyMzQ1Ng"

func mustSealer(t *testing.T, key, purpose string) *Sealer {
	t.Helper()
	s, err := NewSealer(key, purpose)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

func TestSealRoundTrip(t *testing.T) {
	s := mustSealer(t, testMasterKey, "ssh")
	plain := []byte(`{"type":"password","password":"correct horse battery staple"}`)
	sealed, err := s.Seal(plain, "RM-AAAA-BBBB-CCCC-DDDD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "correct horse") || strings.Contains(sealed, "password") {
		t.Fatal("密文里不应出现明文片段")
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("缺少版本前缀: %q", sealed)
	}
	got, err := s.Open(sealed, "RM-AAAA-BBBB-CCCC-DDDD")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("还原结果不一致: %q", got)
	}
}

func TestSealNonceIsRandom(t *testing.T) {
	s := mustSealer(t, testMasterKey, "ssh")
	a, _ := s.Seal([]byte("same"), "ctx")
	b, _ := s.Seal([]byte("same"), "ctx")
	if a == b {
		t.Fatal("相同明文两次密封的结果必须不同（随机 nonce）")
	}
}

func TestSealBindsContextKeyAndPurpose(t *testing.T) {
	s := mustSealer(t, testMasterKey, "ssh")
	sealed, _ := s.Seal([]byte("secret"), "host-A")

	if _, err := s.Open(sealed, "host-B"); !errors.Is(err, ErrSealOpen) {
		t.Fatalf("上下文不一致必须失败，得到 %v", err)
	}
	other := mustSealer(t, testMasterKey+"x", "ssh")
	if _, err := other.Open(sealed, "host-A"); !errors.Is(err, ErrSealOpen) {
		t.Fatalf("主密钥不同必须失败，得到 %v", err)
	}
	otherPurpose := mustSealer(t, testMasterKey, "other")
	if _, err := otherPurpose.Open(sealed, "host-A"); !errors.Is(err, ErrSealOpen) {
		t.Fatalf("用途不同必须失败，得到 %v", err)
	}
}

func TestSealRejectsTamperedAndMalformed(t *testing.T) {
	s := mustSealer(t, testMasterKey, "ssh")
	sealed, _ := s.Seal([]byte("secret"), "ctx")

	tampered := []byte(sealed)
	tampered[len(tampered)-2] ^= 0x01
	cases := map[string]string{
		"篡改":    string(tampered),
		"空":     "",
		"无前缀":   strings.TrimPrefix(sealed, "v1:"),
		"未知版本":  "v9:" + strings.TrimPrefix(sealed, "v1:"),
		"非法编码":  "v1:@@@@",
		"过短":    "v1:AAAA",
		"只有前缀":  "v1:",
		"截断":    sealed[:len(sealed)/2],
		"明文冒充":  "v1:" + "c2VjcmV0",
		"多余的后缀": sealed + "AA",
	}
	for name, in := range cases {
		if _, err := s.Open(in, "ctx"); !errors.Is(err, ErrSealOpen) {
			t.Errorf("%s：应返回 ErrSealOpen，得到 %v", name, err)
		}
	}
}

func TestSealRequiresMasterKey(t *testing.T) {
	if _, err := NewSealer("", "ssh"); !errors.Is(err, ErrSealKey) {
		t.Fatalf("空主密钥必须被拒绝，得到 %v", err)
	}
}

func TestSealEmptyPlaintext(t *testing.T) {
	s := mustSealer(t, testMasterKey, "ssh")
	sealed, err := s.Seal(nil, "ctx")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Open(sealed, "ctx")
	if err != nil || len(got) != 0 {
		t.Fatalf("空明文应能往返: %v %q", err, got)
	}
}
