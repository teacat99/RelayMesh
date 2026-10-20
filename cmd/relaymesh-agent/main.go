// relaymesh-agent 是 RelayMesh 终端 Agent 的独立精简二进制：只包含 Agent，不含服务端与 Web 前端。
package main

import (
	"context"
	"os"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/agent/cli"
)

// version 由发布流水线通过 -ldflags "-X main.version=…" 注入。
var version = "dev"

// rev 是「Agent 侧代码指纹」，由构建通过 -ldflags "-X main.rev=$(go run ./cmd/agentrev)" 注入（见 internal/agentrev）。
// 没有注入（本地 go build、测试）时为空，服务端回退到版本号比较。
var rev = ""

func main() {
	agent.Version = version
	agent.Rev = rev
	os.Exit(cli.Run(context.Background(), os.Args[1:], cli.Env{
		Version: version,
		Rev:     rev,
		Prog:    "relaymesh-agent",
	}))
}
