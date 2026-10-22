package main

import (
	"os"

	"github.com/teacat99/RelayMesh/v2/internal/buildinfo"
	"github.com/teacat99/RelayMesh/v2/internal/hookcli"
)

// version 由发布流水线通过 -ldflags "-X main.version=…" 注入；没有注入（go install …@vX.Y.Z）时，
// 改用 Go 工具链记在二进制里的模块版本（是正式 / 预发布标签才采用，否则仍是 dev），见 internal/buildinfo。
var version = "dev"

func init() {
	hookcli.SetBuildVersion(buildinfo.Resolve(version))
}

func main() {
	os.Exit(hookcli.Main(os.Args))
}
