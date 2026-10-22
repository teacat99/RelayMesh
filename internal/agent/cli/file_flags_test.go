package cli

import (
	"flag"
	"strings"
	"testing"
)

func TestFileURLAllowPrivateFlag(t *testing.T) {
	env := testEnv(t)

	// 默认：不放开，服务单元里不写任何多余参数。
	var c common
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if c.fileFlags.options().AllowPrivateURLs || len(c.fileFlags.args()) != 0 {
		t.Fatalf("默认不应放开私网网址: %+v", c.fileFlags)
	}

	// 命令行显式给出：只还原进 Agent 的服务单元；会话守护进程用不到它，不应出现。
	c = common{server: "https://relay.example.com"}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse([]string{"--file-url-allow-private"}); err != nil {
		t.Fatal(err)
	}
	if !c.fileFlags.options().AllowPrivateURLs {
		t.Fatal("命令行标志未生效")
	}
	if got := strings.Join(c.agentServiceArgs(true), " "); !strings.Contains(got, "--file-url-allow-private") {
		t.Errorf("Agent 服务单元应带上该选项: %s", got)
	}
	if got := strings.Join(c.holderServiceArgs(), " "); strings.Contains(got, "--file-url-allow-private") {
		t.Errorf("守护进程服务单元不应带该选项: %s", got)
	}

	// 环境变量。
	env.Getenv = func(k string) string {
		if k == envFileURLAllowPrivate {
			return "1"
		}
		return ""
	}
	c = common{}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if !c.fileFlags.options().AllowPrivateURLs {
		t.Fatal("环境变量未生效")
	}

	// resolve 把它带进连接配置，并与一票否决互不干扰。
	c = common{server: "https://relay.example.com", dataDir: t.TempDir(), configDir: t.TempDir()}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse([]string{"--server", "https://relay.example.com", "--deny-model-exec"}); err != nil {
		t.Fatal(err)
	}
	r, err := c.resolve(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if !r.files.AllowPrivateURLs || !r.exec.DenyExec {
		t.Fatalf("resolve 结果不符: files=%+v exec=%+v", r.files, r.exec)
	}
}
