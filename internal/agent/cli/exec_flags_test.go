package cli

import (
	"flag"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

func TestDenyModelExecFlagEnvAndServiceArgs(t *testing.T) {
	env := testEnv(t)

	// 默认：不否决，服务单元里不写任何多余参数。
	var c common
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if c.denyModelExec || len(c.execFlags.args()) != 0 {
		t.Fatalf("默认不应否决: %+v", c.execFlags)
	}

	// 命令行显式给出：会还原进 Agent 与守护进程两个服务单元（否决必须在两处都生效）。
	c = common{server: "https://relay.example.com"}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse([]string{"--deny-model-exec"}); err != nil {
		t.Fatal(err)
	}
	if !c.denyModelExec {
		t.Fatal("命令行标志未生效")
	}
	if got := strings.Join(c.agentServiceArgs(true), " "); !strings.Contains(got, "--deny-model-exec") {
		t.Errorf("Agent 服务单元应带上否决: %s", got)
	}
	if got := strings.Join(c.holderServiceArgs(), " "); !strings.Contains(got, "--deny-model-exec") {
		t.Errorf("守护进程服务单元应带上否决: %s", got)
	}

	// 环境变量。
	env.Getenv = func(k string) string {
		if k == envDenyModelExec {
			return "true"
		}
		return ""
	}
	c = common{}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	c.bind(fs, env)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if !c.denyModelExec {
		t.Fatal("环境变量未生效")
	}
}

func TestExecOptionsFollowIdentityKind(t *testing.T) {
	dir := t.TempDir()
	ef := execFlags{denyModelExec: true}

	persistent := ef.options(dir, true)
	if persistent.Dir != filepath.Join(dir, "runs") || !persistent.DenyExec {
		t.Fatalf("持久身份应有输出目录并带上否决: %+v", persistent)
	}
	if holder.RunsDir(dir) != persistent.Dir {
		t.Fatal("输出目录应与 holder.RunsDir 一致")
	}

	temp := ef.options(dir, false)
	if temp.Dir != "" || !temp.DenyExec {
		t.Fatalf("临时身份不支持模型执行（无目录），但否决仍要保留: %+v", temp)
	}
	if got := (&execFlags{}).options(dir, true); got.DenyExec {
		t.Fatalf("没有设置就不应否决: %+v", got)
	}
}
