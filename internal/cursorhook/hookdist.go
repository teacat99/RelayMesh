// Package cursorhook 提供 Cursor hook（追加指令收件箱的第一层发现机制）的安装入口：
// install.sh / install.ps1 与 Go 二进制 relaymesh-hook 的下载路径。
//
// 为什么放在服务端：hook 与服务端之间有约定（inbox_peek 的字段、提醒文案、提醒节奏），会随版本演进；
// 由服务端提供安装脚本和二进制，hook 就永远与服务端版本一致，也不依赖公开仓库有没有同步。
// hook 本体是 cmd/relaymesh-hook（实现在 internal/hookcli）。历史上还有过一份 Python 脚本版，已移除。
//
// 没有 agent-dist 目录的部署（单二进制、go run、stdio 直驱）里 Hub 自己拿不出 hook 二进制，安装脚本会回退到
// GitHub Release 下载；为了让回退时取到与这个 Hub 同版本的 hook，Hub 发放脚本时把自己的版本号写进去
// （RenderInstallSh / RenderInstallPs1）。
package cursorhook

import (
	"bytes"
	_ "embed"

	"github.com/teacat99/RelayMesh/v2/internal/buildinfo"
)

const (
	// InstallShPath 是一键安装脚本路径。
	InstallShPath  = "/hooks/cursor/install.sh"
	InstallPs1Path = "/hooks/cursor/install.ps1"
	HookBinPrefix  = "/hooks/cursor/bin/"
)

//go:embed install.sh
var installSh []byte

//go:embed install.ps1
var installPs1 []byte

// InstallSh 返回 install.sh 副本（原文，没有写入 Hub 版本号）。
func InstallSh() []byte {
	out := make([]byte, len(installSh))
	copy(out, installSh)
	return out
}

// InstallPs1 返回 install.ps1 副本（原文，没有写入 Hub 版本号）。
func InstallPs1() []byte {
	out := make([]byte, len(installPs1))
	copy(out, installPs1)
	return out
}

// GoBinaryBaseName 是不含平台后缀的二进制名前缀。
const GoBinaryBaseName = "relaymesh-hook"

// hubVersionMarker 出现在脚本里版本赋值那一行末尾的注释中；渲染时把含有它的整行换成写入了版本号的赋值。
const hubVersionMarker = "@relaymesh-hub-version@"

// ReleaseTag 把 Hub 的版本号换算成对应的 GitHub Release 标签（vX.Y.Z 或 vX.Y.Z-rc.N）。
// 不是正式 / 预发布版本时返回空串，调用方（安装脚本）会改取最新 Release。
// 判定规则只有一份，放在 buildinfo.ReleaseTag（Web 生成安装命令时用的也是它）；这里保留同名入口给安装脚本的渲染用。
func ReleaseTag(version string) string {
	return buildinfo.ReleaseTag(version)
}

// RenderInstallSh 返回写入了 Hub 版本号的 install.sh；hubVersion 不是正式 / 预发布版本时写入空串。
func RenderInstallSh(hubVersion string) []byte {
	return replaceMarkerLine(installSh, "HUB_VERSION='"+ReleaseTag(hubVersion)+"'")
}

// RenderInstallPs1 返回写入了 Hub 版本号的 install.ps1；hubVersion 不是正式 / 预发布版本时写入空串。
func RenderInstallPs1(hubVersion string) []byte {
	return replaceMarkerLine(installPs1, "$hubVersion = '"+ReleaseTag(hubVersion)+"'")
}

// replaceMarkerLine 把含有版本标记的那一整行换成 replacement，返回新切片；内嵌脚本里一定有标记（测试保证），
// 万一没有就原样返回副本，脚本里的默认值本来就是「留空 = 取最新 Release」。
func replaceMarkerLine(src []byte, replacement string) []byte {
	idx := bytes.Index(src, []byte(hubVersionMarker))
	if idx < 0 {
		out := make([]byte, len(src))
		copy(out, src)
		return out
	}
	start := bytes.LastIndexByte(src[:idx], '\n') + 1
	end := bytes.IndexByte(src[idx:], '\n')
	if end < 0 {
		end = len(src)
	} else {
		end += idx
	}
	out := make([]byte, 0, len(src)+len(replacement))
	out = append(out, src[:start]...)
	out = append(out, replacement...)
	out = append(out, src[end:]...)
	return out
}
