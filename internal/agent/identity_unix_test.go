//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(id.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("身份文件权限应为 0600，得到 %o", perm)
	}
	dst, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dst.Mode().Perm(); perm != 0o700 {
		t.Fatalf("身份目录权限应为 0700，得到 %o", perm)
	}
}

func TestLoadIdentityTightensLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(id.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(dir); err != nil {
		t.Fatalf("属主是自己时应自行收紧而不是失败: %v", err)
	}
	st, _ := os.Stat(id.Path)
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("权限应被收紧，得到 %o", perm)
	}
}

func TestLoadIdentityRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreateIdentity(t.TempDir()) // 「别人」持有的身份文件
	if err != nil {
		t.Fatal(err)
	}
	// 有人在身份目录里预先放了一个指向他自己文件的符号链接：必须拒绝跟随。
	link := filepath.Join(dir, identityFileName)
	if err := os.Symlink(id.Path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(dir); err == nil {
		t.Fatal("符号链接形式的身份文件应被拒绝")
	}
}

func TestLoadIdentityRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, identityFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(dir); err == nil {
		t.Fatal("目录不能当作身份文件")
	}
}
