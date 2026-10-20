//go:build !windows

package cli

import "errors"

func relaunchElevatedImpl(Env, []string) (int, error) {
	return 0, errors.New("仅 Windows 支持")
}

func ownsConsoleImpl() bool { return false }
