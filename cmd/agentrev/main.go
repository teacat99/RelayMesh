// agentrev 打印当前源码树的「Agent 侧代码指纹」（见 internal/agentrev）。
//
// 构建脚本、Dockerfile 与发布流水线用它给 Agent 注入 -X main.rev，并写入分发目录的 REV 文件：
//
//	REV=$(go run ./cmd/agentrev)
//	go build -ldflags "-X main.version=… -X main.rev=${REV}" ./cmd/relaymesh-agent
//
// 默认以当前目录为模块根；-C 指定其他目录。只输出指纹本身（一行），出错时以非零状态退出。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/teacat99/RelayMesh/v2/internal/agentrev"
)

func main() {
	root := flag.String("C", ".", "模块根目录（包含 go.mod）")
	flag.Parse()
	rev, err := agentrev.Compute(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(rev)
}
