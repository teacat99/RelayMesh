package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// ResolveDataDir resolves the base directory for database and certificates based on the runtime mode.
// For "stdio" mode, it defaults to the OS-standard user data directory to avoid polluting the workspace.
// For "serve" mode (or empty), it defaults to the relative "data" directory to preserve backward compatibility.
func ResolveDataDir(mode string) string {
	if mode != "stdio" {
		return "data"
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "data"
	}

	switch runtime.GOOS {
	case "windows":
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "RelayMesh")
		}
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "RelayMesh")
		}
		return filepath.Join(home, "AppData", "Local", "RelayMesh")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "RelayMesh")
	default: // linux, freebsd, openbsd, etc.
		if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
			return filepath.Join(xdgData, "relaymesh")
		}
		return filepath.Join(home, ".local", "share", "relaymesh")
	}
}

// EnsureDataDir creates the data directory and its certs subdirectory with secure permissions (0700).
func EnsureDataDir(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	certsDir := filepath.Join(dataDir, "certs")
	return os.MkdirAll(certsDir, 0700)
}
