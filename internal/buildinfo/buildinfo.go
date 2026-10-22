// Package buildinfo 决定程序对外报告的版本号，并把版本号换算成 GitHub Release 标签。
//
// 版本号有两个来源：
//  1. 发布流水线用 -ldflags "-X main.version=…" 写进二进制的值（Release 二进制、Docker 镜像、热更用的开发版都是这样）；
//  2. 没有 ldflags 时（go run / go install github.com/…/cmd/relaymesh@vX.Y.Z）Go 工具链记在二进制构建信息里的模块版本。
//
// 第二个来源让「go run …@v2.0.0-rc.2」起来的 Hub 也知道自己是哪个 Release：Agent / hook 的安装脚本、
// Web 里生成的安装命令都要靠它去 GitHub 取同一个版本，而不是「最新正式版」（那里可能根本没有 Agent 与 hook）。
package buildinfo

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// DevVersion 是没有任何版本信息时的占位值，与各 main 包里 `var version = "dev"` 的默认值一致。
const DevVersion = "dev"

// releaseVersionRe 只认两种版本号：正式版 vX.Y.Z（v 可省略）和预发布版 vX.Y.Z-alpha.N / -beta.N / -rc.N
// （发布流水线只会发这两种标签，见 scripts/release-publish.sh；预发布版同样有自己的 Release 与二进制，
// 预览版的 Hub 回退下载时必须取到它自己的 hook 与 Agent，而不是「最新正式版」那份旧的）。
// 开发版（v1.6.3-dev-…）、伪版本、(devel)、dev 与任何别的写法都对不上 Release。
var releaseVersionRe = regexp.MustCompile(`^v?([0-9]{1,4})\.([0-9]{1,4})\.([0-9]{1,4})(?:-(alpha|beta|rc)\.([0-9]{1,3}))?$`)

// ReleaseTag 把版本号换算成对应的 GitHub Release 标签（vX.Y.Z 或 vX.Y.Z-rc.N）。
// 不是正式 / 预发布版本时返回空串，调用方（安装脚本、Web 安装命令）会改取最新 Release 或明确提示。
// 返回值是用匹配出的数字和固定的 alpha / beta / rc 重新拼出来的，只含数字、点、连字符、小写字母和前缀 v，
// 写进脚本或 shell 命令都不会有注入风险。
func ReleaseTag(version string) string {
	m := releaseVersionRe.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return ""
	}
	tag := "v" + m[1] + "." + m[2] + "." + m[3]
	if m[4] != "" {
		tag += "-" + m[4] + "." + m[5]
	}
	return tag
}

// Resolve 返回程序应当对外报告的版本号。
//
// injected 是 -ldflags 注入的值：空或 "dev" 表示没有注入。注入了真正的版本号就原样使用（不做任何换算，
// 发布流水线与热更脚本写的是什么，程序就报告什么）。
// 没有注入时，看 Go 工具链记下的模块版本：它是正式 / 预发布标签（例如 go run …@v2.0.0-rc.2 得到 v2.0.0-rc.2）就用它；
// 伪版本（v2.0.1-0.2026…-abcdef123456）、(devel)、带 +dirty 的工作区构建都不是 Release，继续报告 "dev"。
func Resolve(injected string) string {
	info, ok := debug.ReadBuildInfo()
	return resolve(injected, info, ok)
}

// resolve 是 Resolve 去掉对全局构建信息的依赖后的部分，单测直接喂构建信息。
func resolve(injected string, info *debug.BuildInfo, ok bool) string {
	injected = strings.TrimSpace(injected)
	if injected != "" && injected != DevVersion {
		return injected
	}
	if ok && info != nil {
		if v := info.Main.Version; ReleaseTag(v) != "" {
			// 返回规范化后的写法（补上 v 前缀、去掉首尾空白），与 ReleaseTag 的输出一致。
			return ReleaseTag(v)
		}
	}
	return DevVersion
}
