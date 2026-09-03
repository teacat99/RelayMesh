package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveDataDir(t *testing.T) {
	// Mode serve should always resolve to relative "data"
	if dir := ResolveDataDir("serve"); dir != "data" {
		t.Errorf("expected 'data', got '%s'", dir)
	}
	if dir := ResolveDataDir(""); dir != "data" {
		t.Errorf("expected 'data', got '%s'", dir)
	}

	// Mode stdio should resolve to user data directory
	stdioDir := ResolveDataDir("stdio")
	if stdioDir == "" || stdioDir == "data" {
		home, _ := os.UserHomeDir()
		if home != "" {
			t.Errorf("expected non-empty OS user data dir, got '%s'", stdioDir)
		}
	}

	if runtime.GOOS == "linux" {
		if filepath.Base(stdioDir) != "relaymesh" {
			t.Errorf("expected ending with relaymesh, got '%s'", stdioDir)
		}
	}
}

func TestLoadWithMode(t *testing.T) {
	cfgServe := LoadWithMode("serve")
	if cfgServe.Mode != "serve" {
		t.Errorf("expected mode serve, got %s", cfgServe.Mode)
	}
	if cfgServe.DBPath != "data/relaymesh.db" {
		t.Errorf("expected data/relaymesh.db, got %s", cfgServe.DBPath)
	}

	cfgStdio := LoadWithMode("stdio")
	if cfgStdio.Mode != "stdio" {
		t.Errorf("expected mode stdio, got %s", cfgStdio.Mode)
	}
	if cfgStdio.Host != "127.0.0.1" {
		t.Errorf("expected 127.0.0.1, got %s", cfgStdio.Host)
	}
	if cfgStdio.AutoOpenBrowser != false {
		t.Errorf("expected AutoOpenBrowser false for stdio, got true")
	}
}
