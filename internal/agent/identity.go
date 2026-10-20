package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	identityFileName = "identity.key"
	identityHeader   = "relaymesh-agent-identity-v1"
)

// ErrNoIdentity 表示持久身份文件不存在。
var ErrNoIdentity = errors.New("identity file does not exist")

// Identity 是 Agent 的身份：一对 Ed25519 密钥。机器码由公钥派生，服务端只保存公钥，
// 因此「知道机器码」不等于「能冒充该主机」——冒充需要私钥。
type Identity struct {
	Priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
	// Kind 是 persistent（存盘，重启后机器码不变）或 temporary（仅内存，每次运行都不同）。
	Kind string
	// Path 是持久身份的文件路径（临时身份为空）。
	Path string
	// Created 表示这次调用新生成了密钥（首次运行）。
	Created bool
}

// MachineCode 返回由公钥派生的机器码。
func (i *Identity) MachineCode() string { return termproto.MachineCode(i.Pub) }

func identityFromSeed(seed []byte) *Identity {
	priv := ed25519.NewKeyFromSeed(seed)
	return &Identity{Priv: priv, Pub: priv.Public().(ed25519.PublicKey)}
}

// NewTemporaryIdentity 生成仅存于内存的临时身份：进程退出即消失，下次运行机器码不同，需要重新启用。
func NewTemporaryIdentity() (*Identity, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	id := identityFromSeed(seed)
	id.Kind = termproto.KindTemporary
	return id, nil
}

// IdentityPath 返回 dir 下持久身份文件的路径。
func IdentityPath(dir string) string { return filepath.Join(dir, identityFileName) }

// RemoveIdentity 删除 dir 下的持久身份文件（文件不存在不算错误）。
func RemoveIdentity(dir string) error {
	if err := os.Remove(IdentityPath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// LoadIdentity 读取 dir 下的持久身份；不存在时返回 ErrNoIdentity。
func LoadIdentity(dir string) (*Identity, error) {
	path := filepath.Join(dir, identityFileName)
	if err := checkKeyFileSecurity(path); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoIdentity
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Fields(string(raw))
	if len(lines) != 2 || lines[0] != identityHeader {
		return nil, fmt.Errorf("身份文件 %s 格式无法识别", path)
	}
	seed, err := termproto.DecodeKey(lines[1], ed25519.SeedSize)
	if err != nil {
		return nil, fmt.Errorf("身份文件 %s 已损坏: %w", path, err)
	}
	id := identityFromSeed(seed)
	id.Kind = termproto.KindPersistent
	id.Path = path
	return id, nil
}

// LoadOrCreateIdentity 读取持久身份，不存在则生成并安全地写盘（首次运行时才生成，绝不随镜像分发）。
func LoadOrCreateIdentity(dir string) (*Identity, error) {
	id, err := LoadIdentity(dir)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrNoIdentity) {
		return nil, err
	}
	return createIdentity(dir)
}

// ResetIdentity 丢弃现有持久身份并重新生成（机器码随之改变，需要在 Web 上重新启用）。
func ResetIdentity(dir string) (*Identity, error) {
	path := filepath.Join(dir, identityFileName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return createIdentity(dir)
}

func createIdentity(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建身份目录失败: %w", err)
	}
	// Windows 上 MkdirAll 的权限位不起作用，新目录继承上级 ACL（ProgramData 下默认对所有本地用户可读）。
	// 写入任何内容之前先收紧；失败不致命——密钥文件本身还会单独收紧，并在每次读取时复核。
	_ = hardenDir(dir)
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, identityFileName)
	content := identityHeader + "\n" + termproto.EncodeKey(seed) + "\n"
	if err := writeFileAtomic(path, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("写入身份文件失败: %w", err)
	}
	id := identityFromSeed(seed)
	id.Kind = termproto.KindPersistent
	id.Path = path
	id.Created = true
	return id, nil
}

// writeFileAtomic 先写同目录的临时文件（权限从创建时就是 perm）再原子替换，崩溃时不会留下半截密钥。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	// 必须在写入内容之前收紧权限：Unix 上是 chmod，Windows 上是只授予 SYSTEM / 管理员 / 当前用户的 ACL。
	if err := restrictFile(tmp, tmpName, perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
