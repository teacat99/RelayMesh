package cli

import (
	"flag"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/holder"
)

const envDenyModelExec = "RELAYMESH_AGENT_DENY_MODEL_EXEC"

// execFlags 是模型执行（模型通过 MCP 在本机运行命令）的本机选项。
//
// 本机只有一个开关：一票否决。服务端的授权（总闸、主机开关、令牌权限）再宽，只要这里否决，命令就不会运行；
// 反过来，这里没有否决也不意味着放行——是否允许由服务端的几道闸决定。除此之外本机不设任何默认限制
// （并发、超时、目录白名单都由服务端令牌的限额来定），避免出现「令牌里写着允许、实际却被本机默默拒绝」的隐形规则。
type execFlags struct {
	denyModelExec bool
}

func (ef *execFlags) bind(fs *flag.FlagSet, env Env) {
	fs.BoolVar(&ef.denyModelExec, "deny-model-exec", truthy(env.Getenv(envDenyModelExec)),
		"本机一票否决：禁止模型在本机执行命令（服务端无法覆盖；人工终端不受影响）")
}

// options 把选项折算成模型执行的配置。enabled=false（临时身份）时没有输出目录，模型执行不可用，
// 但一票否决仍然如实带上。
func (ef *execFlags) options(dataDir string, enabled bool) holder.ExecOptions {
	o := holder.ExecOptions{DenyExec: ef.denyModelExec, Harden: agent.HardenDir}
	if enabled {
		o.Dir = holder.RunsDir(dataDir)
	}
	return o
}

// args 还原成服务单元里的参数（只写用户显式给出的值）。
func (ef *execFlags) args() []string {
	if ef.denyModelExec {
		return []string{"--deny-model-exec"}
	}
	return nil
}
