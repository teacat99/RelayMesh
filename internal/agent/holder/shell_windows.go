//go:build windows

package holder

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/teacat99/RelayMesh/v2/internal/agent/ptyhost"
)

// DefaultShell 选择 Windows 上的 shell：优先 PowerShell 7（pwsh），其次 Windows PowerShell，最后 cmd。
func DefaultShell(rows, cols uint16) (ptyhost.Spec, error) {
	var shell string
	var args []string
	for _, cand := range []struct {
		name string
		args []string
	}{
		{"pwsh.exe", []string{"-NoLogo"}},
		{"powershell.exe", []string{"-NoLogo"}},
		{"cmd.exe", nil},
	} {
		if p, err := exec.LookPath(cand.name); err == nil {
			shell, args = p, cand.args
			break
		}
	}
	if shell == "" {
		return ptyhost.Spec{}, fmt.Errorf("找不到 pwsh / powershell / cmd")
	}
	dir := os.Getenv("USERPROFILE")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir = ""
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "RELAYMESH_") {
			env = append(env, kv)
		}
	}
	return ptyhost.Spec{Path: shell, Args: args, Env: env, Dir: dir, Rows: rows, Cols: cols}, nil
}
