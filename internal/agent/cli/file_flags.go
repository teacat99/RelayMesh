package cli

import (
	"flag"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
)

const envFileURLAllowPrivate = "RELAYMESH_AGENT_FILE_URL_ALLOW_PRIVATE"

// fileFlags 是文件能力（模型经 MCP 查看 / 拉取 / 写入这台主机上的文件）的本机选项。
//
// 文件能力本身没有单独的本机开关：它和模型执行共用同一个一票否决（--deny-model-exec）。
// 这里只有一个与网络位置相关的旋钮：push_url 默认只能访问公网地址，防止模型借这台主机去探测内网、
// 访问云厂商元数据服务或本机上的管理接口；确实需要从内网取文件时，由这台主机的拥有者在本机显式打开。
// 服务端无法设置它。
type fileFlags struct {
	urlAllowPrivate bool
}

func (ff *fileFlags) bind(fs *flag.FlagSet, env Env) {
	fs.BoolVar(&ff.urlAllowPrivate, "file-url-allow-private", truthy(env.Getenv(envFileURLAllowPrivate)),
		"允许 push_url 访问回环 / 私网 / 链路本地地址（默认只允许公网；服务端无法覆盖）")
}

func (ff *fileFlags) options() agent.FileOptions {
	return agent.FileOptions{AllowPrivateURLs: ff.urlAllowPrivate}
}

// args 还原成服务单元里的参数（只写用户显式给出的值）。只属于 Agent，会话守护进程用不到。
func (ff *fileFlags) args() []string {
	if ff.urlAllowPrivate {
		return []string{"--file-url-allow-private"}
	}
	return nil
}
