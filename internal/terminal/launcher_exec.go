package terminal

import "github.com/teacat99/RelayMesh/v2/internal/store"

// ClassifyLauncherExec 见 store.ClassifyLauncherExec（前后端表驱动用例一致）。
func ClassifyLauncherExec(command string) (string, error) {
	return store.ClassifyLauncherExec(command)
}
