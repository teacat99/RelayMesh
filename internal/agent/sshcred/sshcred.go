// Package sshcred 是 SSH 网关在本机保存「它代为连接的 SSH 主机的登录凭据」的密封库。
//
// 为什么在网关而不是 Hub：经网关接入的 SSH 主机，凭据只应该存在于真正去连接它的那台机器上。
// Hub 的数据库、备份与日志里因此没有这些凭据；网关换了数据卷或重建容器，凭据随之消失，
// Hub 在网关上线时会发现并提示管理员重新填写（见 terminal 包的凭据同步）。
//
// 存储方式：
//   - 目录下的 credentials.json 保存 {"v":1,"items":{<SSH 主机机器码>:<密封后的凭据>}}，逐条 AES-256-GCM，
//     附加数据 = 机器码——把 A 的密文换到 B 的名下无法解开；
//   - 密封密钥默认是同目录下自动生成的 key 文件（0600）；也可以用环境变量外置（Options.Key），
//     外置后不再生成 key 文件，凭据库单独被拷走就无法解开；
//   - 写入是「临时文件 + 重命名」，崩溃不会留下半截文件；损坏的文件不会被覆盖，库直接报错，留给人处理。
package sshcred

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/teacat99/RelayMesh/v2/internal/secure"
	"github.com/teacat99/RelayMesh/v2/internal/sshexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

const (
	fileName = "credentials.json"
	keyName  = "key"
	version  = 1
	purpose  = "ssh-gateway-credentials"

	// MaxItems 是库里最多保存的凭据数（远多于 Hub 允许登记的 SSH 主机数量）。
	MaxItems = termproto.MaxSSHKeep
	// minExternalKey 是外置密钥的最短长度：它是人配置的，太短等于没有。
	minExternalKey = 16
)

var (
	// ErrNotFound 表示库里没有这台 SSH 主机的凭据。
	ErrNotFound = errors.New("sshcred: 没有这台主机的凭据")
	// ErrUnreadable 表示凭据存在但解不开（密钥换过、文件被改动，或密文被搬到了别的主机名下）。
	ErrUnreadable = errors.New("sshcred: 凭据无法解开")
	// ErrFull 表示库已满。
	ErrFull = errors.New("sshcred: 凭据库已满")
	// ErrBadID 表示键不是合法的机器码。
	ErrBadID = errors.New("sshcred: 无效的主机码")
)

// Options 是打开凭据库的参数。
type Options struct {
	// Dir 是凭据库目录；不存在则创建（0700，Windows 上再用 Harden 收紧）。
	Dir string
	// Key 是外置的密封密钥（通常来自环境变量）；为空则使用 Dir 下自动生成的 key 文件。
	Key string
	// Harden 收紧目录访问权限（Windows 设置 ACL）；Unix 为 nil。
	Harden func(string) error
}

// Store 是凭据库，可并发使用。
type Store struct {
	mu     sync.Mutex
	dir    string
	sealer *secure.Sealer
	items  map[string]string
}

type fileDoc struct {
	V     int               `json:"v"`
	Items map[string]string `json:"items"`
}

// Open 打开（必要时创建）凭据库。文件损坏、密钥文件不可读写时返回错误，不会覆盖任何已有内容。
func Open(o Options) (*Store, error) {
	if strings.TrimSpace(o.Dir) == "" {
		return nil, errors.New("sshcred: 没有指定凭据库目录")
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("sshcred: 创建凭据库目录失败: %w", err)
	}
	// 先收紧目录再写任何内容：Windows 上 MkdirAll 的权限位不起作用，新目录继承上级的 ACL。
	if o.Harden != nil {
		if err := o.Harden(o.Dir); err != nil {
			return nil, fmt.Errorf("sshcred: 收紧凭据库目录权限失败: %w", err)
		}
	}
	key := o.Key
	if key != "" {
		if len(key) < minExternalKey {
			return nil, fmt.Errorf("sshcred: 外置密钥太短（至少 %d 个字符）", minExternalKey)
		}
	} else {
		k, err := loadOrCreateKey(filepath.Join(o.Dir, keyName))
		if err != nil {
			return nil, err
		}
		key = k
	}
	sealer, err := secure.NewSealer(key, purpose)
	if err != nil {
		return nil, fmt.Errorf("sshcred: 无法初始化密封: %w", err)
	}
	s := &Store{dir: o.Dir, sealer: sealer, items: map[string]string{}}
	data, err := os.ReadFile(s.path())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("sshcred: 读取凭据库失败: %w", err)
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil || doc.V != version {
		return nil, fmt.Errorf("sshcred: 凭据库文件格式不正确（%s）：为避免丢失数据没有覆盖它，请确认后移走该文件再重试", s.path())
	}
	for id, sealed := range doc.Items {
		if termproto.ValidMachineCode(id) && sealed != "" {
			s.items[id] = sealed
		}
	}
	return s, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, fileName) }

// loadOrCreateKey 读取密钥文件，不存在则生成。文件存在但内容无效时报错而不是重新生成——
// 重新生成会让库里已有的凭据永远解不开。
func loadOrCreateKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		k := strings.TrimSpace(string(data))
		if len(k) < minExternalKey {
			return "", fmt.Errorf("sshcred: 密钥文件内容无效（%s）：为避免凭据永久解不开，没有重新生成", path)
		}
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("sshcred: 读取密钥文件失败: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	k := base64.RawURLEncoding.EncodeToString(raw)
	// O_EXCL：两个进程同时首次启动时，只有一个能创建；另一个读到的是同一把密钥。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateKey(path)
		}
		return "", fmt.Errorf("sshcred: 创建密钥文件失败: %w", err)
	}
	if _, err := f.WriteString(k + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sshcred: 写入密钥文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return k, nil
}

// Put 保存（或覆盖）一台 SSH 主机的凭据。凭据会先被校验（私钥会真正解析一次）。
func (s *Store) Put(id string, c sshexec.Credential) error {
	if !termproto.ValidMachineCode(id) {
		return ErrBadID
	}
	raw, err := c.Marshal()
	if err != nil {
		return err
	}
	sealed, err := s.sealer.Seal(raw, id)
	if err != nil {
		return fmt.Errorf("sshcred: 加密失败: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, had := s.items[id]
	if !had && len(s.items) >= MaxItems {
		return ErrFull
	}
	s.items[id] = sealed
	if err := s.saveLocked(); err != nil {
		if had {
			s.items[id] = old
		} else {
			delete(s.items, id)
		}
		return err
	}
	return nil
}

// Get 取出一台 SSH 主机的凭据。
func (s *Store) Get(id string) (sshexec.Credential, error) {
	s.mu.Lock()
	sealed, ok := s.items[id]
	s.mu.Unlock()
	if !ok {
		return sshexec.Credential{}, ErrNotFound
	}
	raw, err := s.sealer.Open(sealed, id)
	if err != nil {
		return sshexec.Credential{}, ErrUnreadable
	}
	c, err := sshexec.ParseCredential(raw)
	if err != nil {
		return sshexec.Credential{}, ErrUnreadable
	}
	return c, nil
}

// Has 报告库里是否有这台主机的凭据且能解开。
func (s *Store) Has(id string) bool {
	_, err := s.Get(id)
	return err == nil
}

// Delete 删除一台主机的凭据；返回此前是否存在。
func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.items[id]
	if !ok {
		return false, nil
	}
	delete(s.items, id)
	if err := s.saveLocked(); err != nil {
		s.items[id] = old
		return false, err
	}
	return true, nil
}

// Retain 只保留 keep 名单内的凭据，删除其余的；返回名单内仍然持有（且能解开）的主机码，以及被删除的主机码。
// 名单内但解不开的凭据不会被删除（可能只是密钥配置错了，纠正之后还能用），也不计入返回的持有名单。
func (s *Store) Retain(keep []string) (have, dropped []string, err error) {
	want := make(map[string]struct{}, len(keep))
	for _, id := range keep {
		if termproto.ValidMachineCode(id) {
			want[id] = struct{}{}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := map[string]string{}
	for id, sealed := range s.items {
		if _, ok := want[id]; !ok {
			removed[id] = sealed
			delete(s.items, id)
		}
	}
	if len(removed) > 0 {
		if err := s.saveLocked(); err != nil {
			for id, sealed := range removed {
				s.items[id] = sealed
			}
			return nil, nil, err
		}
	}
	for id, sealed := range s.items {
		if raw, oerr := s.sealer.Open(sealed, id); oerr == nil {
			if _, perr := sshexec.ParseCredential(raw); perr == nil {
				have = append(have, id)
			}
		}
	}
	for id := range removed {
		dropped = append(dropped, id)
	}
	sort.Strings(have)
	sort.Strings(dropped)
	return have, dropped, nil
}

// Len 返回库里的凭据条数。
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// saveLocked 原子地写出整个库（调用方持有 s.mu）。
func (s *Store) saveLocked() error {
	doc := fileDoc{V: version, Items: s.items}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, fileName+".*")
	if err != nil {
		return fmt.Errorf("sshcred: 写入凭据库失败: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // 成功重命名之后这是个无害的失败
	// 先收紧权限再写内容（Windows 上由目录的继承 ACL 保护，Chmod 只是无害的补充）。
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sshcred: 收紧凭据库权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sshcred: 写入凭据库失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path()); err != nil {
		return fmt.Errorf("sshcred: 保存凭据库失败: %w", err)
	}
	return nil
}
