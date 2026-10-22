package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func TestIdentityCreateAndReload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "agent") // 目录尚不存在
	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Created || id.Kind != termproto.KindPersistent || id.Path == "" {
		t.Fatalf("首次应创建持久身份: %+v", id)
	}
	code := id.MachineCode()
	if !strings.HasPrefix(code, "RM-") {
		t.Fatalf("机器码格式不对: %s", code)
	}

	again, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created {
		t.Fatal("第二次应读取而非重新生成")
	}
	if again.MachineCode() != code {
		t.Fatalf("持久身份的机器码必须稳定: %s != %s", again.MachineCode(), code)
	}

	// 私钥能对挑战签名，且只能被对应公钥验证。
	nonce, err := termproto.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	sig := termproto.SignChallenge(again.Priv, nonce)
	if !termproto.VerifyChallenge(id.Pub, nonce, sig) {
		t.Fatal("签名应能被公钥验证")
	}
	other, _ := NewTemporaryIdentity()
	if termproto.VerifyChallenge(other.Pub, nonce, sig) {
		t.Fatal("他人的公钥不应验证通过")
	}
}

func TestTemporaryIdentityIsUniqueAndNotPersisted(t *testing.T) {
	a, err := NewTemporaryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewTemporaryIdentity()
	if a.MachineCode() == b.MachineCode() {
		t.Fatal("临时身份每次都应不同")
	}
	if a.Kind != termproto.KindTemporary || a.Path != "" {
		t.Fatalf("临时身份不应有文件路径: %+v", a)
	}
}

func TestResetIdentityChangesMachineCode(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResetIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.MachineCode() == second.MachineCode() {
		t.Fatal("重置后机器码必须改变")
	}
	reloaded, err := LoadIdentity(dir)
	if err != nil || reloaded.MachineCode() != second.MachineCode() {
		t.Fatalf("重置后的身份应已落盘: %v", err)
	}
}

func TestLoadIdentityMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadIdentity(dir); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("文件不存在应返回 ErrNoIdentity，得到 %v", err)
	}

	path := filepath.Join(dir, identityFileName)
	cases := map[string]string{
		"空文件":    "",
		"错误的头":   "something-else\nAAAA\n",
		"缺少种子":   identityHeader + "\n",
		"种子长度不对": identityHeader + "\n" + termproto.EncodeKey([]byte("short")) + "\n",
		"种子不是编码": identityHeader + "\n!!!not-base64!!!\n",
	}
	for name, content := range cases {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadIdentity(dir); err == nil || errors.Is(err, ErrNoIdentity) {
			t.Errorf("%s: 应报「已损坏 / 无法识别」，得到 %v", name, err)
		}
		// 损坏的文件不能被静默覆盖：那会悄悄改变机器码。
		if _, err := LoadOrCreateIdentity(dir); err == nil {
			t.Errorf("%s: LoadOrCreate 不应覆盖损坏的身份文件", name)
		}
	}
}
