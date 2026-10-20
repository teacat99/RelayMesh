package sshcred

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"encoding/pem"

	"golang.org/x/crypto/ssh"

	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const secretPW = "pw-DO-NOT-LEAK-91ac"

func code(t testing.TB) string {
	t.Helper()
	c, err := termproto.RandomMachineCode()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func pwCred() sshexec.Credential {
	return sshexec.Credential{Kind: sshexec.AuthPassword, Password: secretPW}
}

func keyCred(t testing.TB, passphrase string) sshexec.Credential {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	return sshexec.Credential{Kind: sshexec.AuthKey, PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: passphrase}
}

func open(t testing.TB, dir, key string) *Store {
	t.Helper()
	s, err := Open(Options{Dir: dir, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutGetRoundTripAndPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ssh-gateway")
	s := open(t, dir, "")
	a, b := code(t), code(t)
	kc := keyCred(t, "phrase-1")
	if err := s.Put(a, pwCred()); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(b, kc); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(a)
	if err != nil || got.Kind != sshexec.AuthPassword || got.Password != secretPW {
		t.Fatalf("Get(a) = %+v %v", got, err)
	}
	gotKey, err := s.Get(b)
	if err != nil || gotKey.PrivateKey != kc.PrivateKey || gotKey.Passphrase != "phrase-1" {
		t.Fatalf("Get(b) 不一致: %v", err)
	}
	if _, err := s.Get(code(t)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应返回 ErrNotFound: %v", err)
	}

	// 重新打开（模拟 Agent / 守护进程重启）：凭据与密钥文件都在。
	s2 := open(t, dir, "")
	if s2.Len() != 2 {
		t.Fatalf("重开后应有 2 条: %d", s2.Len())
	}
	if got, err := s2.Get(a); err != nil || got.Password != secretPW {
		t.Fatalf("重开后取不到: %+v %v", got, err)
	}
}

// 落盘的文件里绝不能出现明文凭据；密钥文件权限只有所有者可读写（Unix）。
func TestFilesNeverContainPlaintextAndAreOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, "")
	kc := keyCred(t, "")
	if err := s.Put(code(t), pwCred()); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(code(t), kc); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{secretPW, "BEGIN OPENSSH PRIVATE KEY", kc.PrivateKey[40:80]} {
			if strings.Contains(string(raw), bad) {
				t.Fatalf("%s 含明文 %q", e.Name(), bad)
			}
		}
		if runtime.GOOS != "windows" {
			info, _ := e.Info()
			if perm := info.Mode().Perm(); perm&0o077 != 0 {
				t.Fatalf("%s 权限过宽: %o", e.Name(), perm)
			}
		}
	}
	// 没有遗留的临时文件。
	for _, e := range entries {
		if strings.Contains(e.Name(), ".json.") {
			t.Fatalf("遗留了临时文件 %s", e.Name())
		}
	}
}

// 密文被搬到别的主机名下（改 JSON）必须解不开：AAD 绑定机器码。
func TestSealedBlobCannotBeMovedToAnotherHost(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, "")
	a, b := code(t), code(t)
	if err := s.Put(a, pwCred()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc fileDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc.Items = map[string]string{b: doc.Items[a]}
	moved, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, fileName), moved, 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := open(t, dir, "")
	if _, err := s2.Get(b); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("搬家后的密文应解不开: %v", err)
	}
	if s2.Has(b) {
		t.Fatal("Has 应为 false")
	}
}

func TestExternalKeyReplacesKeyFile(t *testing.T) {
	dir := t.TempDir()
	const ext = "external-key-0123456789-abcdef"
	s := open(t, dir, ext)
	id := code(t)
	if err := s.Put(id, pwCred()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, keyName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("外置密钥时不应生成 key 文件: %v", err)
	}
	// 错误的外置密钥打开得了，但取不出凭据。
	wrong := open(t, dir, "another-external-key-9876543210")
	if _, err := wrong.Get(id); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("密钥不对应解不开: %v", err)
	}
	// 正确的密钥可以。
	if got, err := open(t, dir, ext).Get(id); err != nil || got.Password != secretPW {
		t.Fatalf("正确密钥应可用: %+v %v", got, err)
	}
	if _, err := Open(Options{Dir: t.TempDir(), Key: "short"}); err == nil {
		t.Fatal("过短的外置密钥应被拒绝")
	}
}

// 密钥文件被清空 / 损坏时不能偷偷重新生成：那会让已有凭据永远解不开。
func TestInvalidKeyFileIsNotRegenerated(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, "")
	if err := s.Put(code(t), pwCred()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, keyName), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Dir: dir}); err == nil {
		t.Fatal("无效的密钥文件应报错，而不是重新生成")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, keyName)); strings.TrimSpace(string(data)) != "" {
		t.Fatal("无效的密钥文件不应被改写")
	}
}

// 损坏的凭据库文件不会被覆盖。
func TestCorruptFileIsPreserved(t *testing.T) {
	dir := t.TempDir()
	open(t, dir, "") // 生成 key 文件
	garbage := []byte("{not json")
	if err := os.WriteFile(filepath.Join(dir, fileName), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Dir: dir}); err == nil {
		t.Fatal("损坏的文件应报错")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, fileName)); string(got) != string(garbage) {
		t.Fatalf("损坏的文件被改动了: %q", got)
	}
	// 版本不认识同样不覆盖。
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(`{"v":99,"items":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Dir: dir}); err == nil {
		t.Fatal("未知版本应报错")
	}
}

func TestPutValidatesAndRejectsBadIDs(t *testing.T) {
	s := open(t, t.TempDir(), "")
	if err := s.Put("not-a-code", pwCred()); !errors.Is(err, ErrBadID) {
		t.Fatalf("非法主机码: %v", err)
	}
	if err := s.Put(code(t), sshexec.Credential{Kind: sshexec.AuthPassword}); err == nil {
		t.Fatal("空密码应被拒绝")
	}
	if err := s.Put(code(t), sshexec.Credential{Kind: sshexec.AuthKey, PrivateKey: "garbage"}); err == nil {
		t.Fatal("无法解析的私钥应被拒绝")
	}
	if s.Len() != 0 {
		t.Fatalf("失败的 Put 不应留下条目: %d", s.Len())
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, "")
	id := code(t)
	if err := s.Put(id, pwCred()); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Delete(id); err != nil || !ok {
		t.Fatalf("Delete = %v %v", ok, err)
	}
	if ok, _ := s.Delete(id); ok {
		t.Fatal("重复删除应返回 false")
	}
	if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应不存在: %v", err)
	}
	if open(t, dir, "").Len() != 0 {
		t.Fatal("删除应落盘")
	}
}

func TestRetainDropsOthersAndReportsHeld(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, "")
	keep1, keep2, drop, missing := code(t), code(t), code(t), code(t)
	for _, id := range []string{keep1, keep2, drop} {
		if err := s.Put(id, pwCred()); err != nil {
			t.Fatal(err)
		}
	}
	have, dropped, err := s.Retain([]string{keep1, keep2, missing, "garbage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != drop || len(have) != 2 {
		t.Fatalf("have=%v dropped=%v", have, dropped)
	}
	for _, h := range have {
		if h == missing || h == drop {
			t.Fatalf("回报了不该有的主机: %s", h)
		}
	}
	if s.Has(drop) || !s.Has(keep1) {
		t.Fatal("回收结果不对")
	}
	if open(t, dir, "").Len() != 2 {
		t.Fatal("回收应落盘")
	}
	// 空名单 = 全部回收。
	if _, gone, err := s.Retain(nil); err != nil || len(gone) != 2 || s.Len() != 0 {
		t.Fatalf("空名单应清空: gone=%v err=%v len=%d", gone, err, s.Len())
	}
}

// 名单内但解不开的凭据不能被回收（可能只是密钥配置错了），也不算「仍持有」。
func TestRetainKeepsUnreadableButDoesNotReportIt(t *testing.T) {
	dir := t.TempDir()
	const k1, k2 = "external-key-0123456789-abcdef", "another-external-key-9876543210"
	id := code(t)
	if err := open(t, dir, k1).Put(id, pwCred()); err != nil {
		t.Fatal(err)
	}
	wrong := open(t, dir, k2)
	have, dropped, err := wrong.Retain([]string{id})
	if err != nil || len(dropped) != 0 || len(have) != 0 {
		t.Fatalf("have=%v dropped=%v err=%v", have, dropped, err)
	}
	if wrong.Len() != 1 {
		t.Fatal("解不开的凭据不应被删除")
	}
	if got, err := open(t, dir, k1).Get(id); err != nil || got.Password != secretPW {
		t.Fatalf("纠正密钥后应仍可用: %+v %v", got, err)
	}
}

func TestStoreIsFull(t *testing.T) {
	s := open(t, t.TempDir(), "")
	first := ""
	for i := 0; i < MaxItems; i++ {
		id := code(t)
		if first == "" {
			first = id
		}
		s.mu.Lock()
		s.items[id] = "v1:placeholder"
		s.mu.Unlock()
	}
	if err := s.Put(code(t), pwCred()); !errors.Is(err, ErrFull) {
		t.Fatalf("满库应拒绝新条目: %v", err)
	}
	// 覆盖已有条目不受上限影响。
	if err := s.Put(first, pwCred()); err != nil {
		t.Fatalf("覆盖已有条目应允许: %v", err)
	}
}

func TestConcurrentPuts(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, "")
	ids := make([]string, 16)
	for i := range ids {
		ids[i] = code(t)
	}
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cred := sshexec.Credential{Kind: sshexec.AuthPassword, Password: fmt.Sprintf("pw-%d", i)}
			if err := s.Put(id, cred); err != nil {
				t.Errorf("Put: %v", err)
			}
			_, _ = s.Get(id)
		}()
	}
	wg.Wait()
	re := open(t, dir, "")
	if re.Len() != len(ids) {
		t.Fatalf("并发写入后应有 %d 条: %d", len(ids), re.Len())
	}
	for i, id := range ids {
		if got, err := re.Get(id); err != nil || got.Password != fmt.Sprintf("pw-%d", i) {
			t.Fatalf("条目 %d 不一致: %+v %v", i, got, err)
		}
	}
}

func TestOpenRejectsEmptyDir(t *testing.T) {
	if _, err := Open(Options{}); err == nil {
		t.Fatal("没有目录应报错")
	}
}
