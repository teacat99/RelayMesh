package cli

import (
	"strings"
	"testing"
)

func testEnv(t *testing.T) Env {
	t.Helper()
	e := Env{Version: "1.7.0-test", Prog: "relaymesh-agent", Stdout: &syncBuffer{}, Stderr: &syncBuffer{}, Stdin: strings.NewReader("")}
	e.normalize()
	return e
}

func testConfig(t *testing.T, c common) (common, resolved) {
	t.Helper()
	if c.server == "" {
		c.server = "https://relay.example.com"
	}
	c.dataDir = t.TempDir()
	c.configDir = t.TempDir()
	if c.holderMode == "" {
		// 沿用单服务（内嵌）形态做基线；双服务形态由 holder_install_test.go 专门覆盖。
		c.holderMode = "embedded"
	}
	cfg, err := c.resolve(testEnv(t), false)
	if err != nil {
		t.Fatal(err)
	}
	return c, cfg
}
