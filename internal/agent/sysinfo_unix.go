//go:build !windows

package agent

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// IsElevated 报告是否以 root 身份运行。
func IsElevated() bool { return os.Geteuid() == 0 }

// PrivLevel 返回自报的特权等级：root、admin（属于管理员组，可提权）或 user。
func PrivLevel() string {
	if os.Geteuid() == 0 {
		return termproto.PrivRoot
	}
	groups, err := os.Getgroups()
	if err != nil {
		return termproto.PrivUser
	}
	adminGIDs := map[int]bool{}
	if runtime.GOOS == "darwin" {
		adminGIDs[80] = true // macOS 的 admin 组固定为 80
	}
	for _, name := range []string{"sudo", "wheel", "admin"} {
		if gid, ok := groupID(name); ok {
			adminGIDs[gid] = true
		}
	}
	for _, g := range groups {
		if adminGIDs[g] {
			return termproto.PrivAdmin
		}
	}
	return termproto.PrivUser
}

// groupID 在 /etc/group 里按名称查组号。
func groupID(name string) (int, bool) {
	f, err := os.Open("/etc/group")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) >= 3 && fields[0] == name {
			if gid, err := strconv.Atoi(fields[2]); err == nil {
				return gid, true
			}
		}
	}
	return 0, false
}

// darwinVersion 返回 "macOS 14.5" 形式的描述；取不到时回落到 "macOS"。
func darwinVersion() string {
	out, err := runWithTimeout(2*time.Second, "sw_vers", "-productVersion")
	if err != nil || strings.TrimSpace(out) == "" {
		return "macOS"
	}
	return "macOS " + strings.TrimSpace(out)
}

func runWithTimeout(d time.Duration, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
		return string(out), err
	case <-time.After(d):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return "", os.ErrDeadlineExceeded
	}
}
