package cli

import (
	"flag"
	"strings"
	"testing"
)

func TestRecordSessionLinesFlag(t *testing.T) {
	env := testEnv(t)

	// 默认不限制：既不进记录配置，也不往服务单元里写多余参数。
	var c common
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if c.sessionLines != 0 || c.recordFlags.options(t.TempDir()).SessionLines != 0 {
		t.Fatalf("默认应不限制: %+v", c.recordFlags)
	}
	if strings.Contains(strings.Join(c.recordFlags.args(), " "), "--record-session-lines") {
		t.Fatalf("没给就不应写进服务单元: %v", c.recordFlags.args())
	}

	// 显式给出：进入记录配置，并还原进服务单元参数（Agent 与守护进程各自启动时都要带上）。
	c = common{server: "https://relay.example.com"}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse([]string{"--record-session-lines", "20000"}); err != nil {
		t.Fatal(err)
	}
	if err := c.recordFlags.validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.recordFlags.options(t.TempDir()).SessionLines; got != 20000 {
		t.Fatalf("记录配置 = %d", got)
	}
	if got := strings.Join(c.recordFlags.args(), " "); !strings.Contains(got, "--record-session-lines 20000") {
		t.Fatalf("应还原进服务单元参数: %s", got)
	}

	// 负数被拒绝。
	c = common{}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse([]string{"--record-session-lines", "-1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.recordFlags.validate(); err == nil || !strings.Contains(err.Error(), "--record-session-lines") {
		t.Fatalf("负数应被拒绝: %v", err)
	}
}
