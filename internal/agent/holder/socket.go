package holder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	socketFileName = "holder.sock"
	// maxSocketPath 是 AF_UNIX 路径的安全长度上限（Linux 108、macOS 104，各留余量）。
	maxSocketPath = 100
	// maxConns 是同时处于握手或服务中的本地连接数上限：控制端 1 个、管理端若干，其余都是异常。
	maxConns = 8
)

var (
	// ErrAlreadyRunning 表示套接字已被另一个守护进程占用。
	ErrAlreadyRunning = errors.New("holder is already running")
	// ErrNotRunning 表示连不上守护进程（没启动、已停止或已崩溃）。
	ErrNotRunning = errors.New("holder is not running")
)

// StateDir 返回守护进程在数据目录下的工作目录（状态文件、记录等都放这里）。
func StateDir(dataDir string) string { return filepath.Join(dataDir, "holder") }

// RecordDir 返回终端记录的根目录。守护进程（写）与 Agent（读）对同一个数据目录总是算出同一个路径。
func RecordDir(dataDir string) string { return filepath.Join(dataDir, "records") }

// SSHCredDir 返回 SSH 网关凭据库的目录（密封保存各 SSH 主机的登录凭据，只存在于这台网关本机）。
func SSHCredDir(dataDir string) string { return filepath.Join(dataDir, "ssh-gateway") }

// SocketPath 返回守护进程本地套接字的路径。AF_UNIX 路径有长度上限，数据目录很深时
// 退回到一个由数据目录哈希得出的短路径（运行时目录或临时目录下，权限 0700）。
// 守护进程与 Agent 对同一个数据目录总是算出同一个路径。
func SocketPath(dataDir string) string {
	p := filepath.Join(StateDir(dataDir), socketFileName)
	if len(p) <= maxSocketPath {
		return p
	}
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	base := os.TempDir()
	if runtime.GOOS == "linux" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
			base = rt
		}
	}
	return filepath.Join(base, fmt.Sprintf("rmh-%d-%s", os.Geteuid(), hex.EncodeToString(sum[:])[:10]), "s")
}

// ensureSocketDir 创建套接字所在目录（0700）并核对归属与权限。
func ensureSocketDir(dir string, harden func(string) error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if harden != nil {
		if err := harden(dir); err != nil {
			return err
		}
	}
	return checkSocketDir(dir)
}

// Listen 在 path 上监听本地套接字。路径上已有套接字文件时：能连通说明守护进程已在运行（拒绝重复启动）；
// 连不通就是上次崩溃留下的残余，删除后重建。
func Listen(path string, harden func(string) error) (net.Listener, error) {
	if err := ensureSocketDir(filepath.Dir(path), harden); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return nil, ErrAlreadyRunning
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// 目录本身是 0700，下面的收紧只是纵深防御；Windows 上 Chmod 只影响只读位，无副作用。
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

// Dial 连接 path 上的守护进程。连接前核对目录的归属，连接后核对对端身份，
// 避免连到别人在共享目录里预先放好的假守护进程（那样会把用户的键入送给别人）。
func Dial(ctx context.Context, path string) (Conn, error) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotRunning
	}
	if err := checkSocketDir(dir); err != nil {
		return nil, err
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isConnRefused(err) {
			return nil, ErrNotRunning
		}
		return nil, err
	}
	if err := verifyPeer(nc); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return NewStreamConn(nc), nil
}
