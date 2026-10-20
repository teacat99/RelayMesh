// Package installer 是服务端自带的「Agent 安装源」。
//
// 为什么要自带：安装命令原先指向公开仓库 main 分支上的脚本，而公开仓库只在发版时才同步，
// 新脚本还没发布时用户拿到的是 404 或旧脚本（旧脚本甚至用了 bash 专有语法）。让服务端自己提供脚本与 Agent 二进制，
// 命令就永远和服务端版本一致，也不依赖 GitHub 是否可达。
//
// 本包只含与 HTTP 无关的部分，便于单独测试：
//   - assets/install.sh、assets/install.ps1：安装脚本的唯一正本（公开库根目录的同名文件由同步脚本从这里复制）；
//   - Render：把本次发现窗口的票据（以及内置 HTTPS 的公钥指纹）写进脚本前导；
//   - Dist：Agent 各平台二进制所在的目录，及其 checksums.txt。
//
// 票据的签发、校验与路由在 internal/terminal（它们与发现窗口的生命周期绑定）。
package installer

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"regexp"
)

//go:embed assets/install.sh assets/install.ps1
var assets embed.FS

// 脚本文件名，同时也是下载路径的最后一段。
const (
	ScriptSh  = "install.sh"
	ScriptPS1 = "install.ps1"
)

// preludeMarker 出现在脚本中独占一行的注释里，Render 用前导替换这一整行。
const preludeMarker = "@relaymesh-prelude@"

var (
	ticketRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// curl --pinnedpubkey 的参数格式：sha256// 加 SHA-256 摘要（32 字节）的标准 base64（44 个字符，末尾一个 =）。
	spkiRe = regexp.MustCompile(`^sha256//[A-Za-z0-9+/]{43}=$`)
)

// Prelude 是写进脚本前导、由服务端决定的内容。所有字段在写入前都会严格校验，保证不会出现脚本注入。
type Prelude struct {
	// Ticket 是本次发现窗口的安装票据（32 位小写十六进制）。
	Ticket string
	// SPKIPin 是内置 HTTPS 叶子证书的公钥指纹（curl --pinnedpubkey 格式）；没有内置 HTTPS 时留空。
	SPKIPin string
}

// IsScript 判断 name 是否是可渲染的安装脚本。
func IsScript(name string) bool { return name == ScriptSh || name == ScriptPS1 }

// ContentType 返回安装脚本的响应类型。两种脚本都按纯文本发送：浏览器不会渲染或执行，也避开了 MIME 嗅探。
func ContentType(name string) string {
	if IsScript(name) {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// Render 读取内嵌的脚本并写入前导。
func Render(name string, p Prelude) ([]byte, error) {
	if !IsScript(name) {
		return nil, fmt.Errorf("installer: 未知脚本 %q", name)
	}
	if !ticketRe.MatchString(p.Ticket) {
		return nil, errors.New("installer: 票据格式不正确")
	}
	if p.SPKIPin != "" && !spkiRe.MatchString(p.SPKIPin) {
		return nil, errors.New("installer: 公钥指纹格式不正确")
	}
	src, err := assets.ReadFile("assets/" + name)
	if err != nil {
		return nil, err
	}
	var prelude string
	switch name {
	case ScriptSh:
		prelude = "# --- 以下由 RelayMesh 服务端在发放时写入：票据只在发现窗口开启期间有效 ---\n" +
			"RELAYMESH_HUB_TICKET='" + p.Ticket + "'\n" +
			"RELAYMESH_HUB_SPKI='" + p.SPKIPin + "'\n" +
			"# --- 前导结束 ---"
	case ScriptPS1:
		prelude = "# --- 以下由 RelayMesh 服务端在发放时写入：票据只在发现窗口开启期间有效 ---\n" +
			"$RelayMeshHub.Ticket = '" + p.Ticket + "'\n" +
			"# --- 前导结束 ---"
	}
	return replaceMarkerLine(src, prelude)
}

// replaceMarkerLine 把含有标记的那一整行换成 replacement；找不到标记说明脚本与服务端不匹配，必须报错而不是静默发出无票据的脚本。
func replaceMarkerLine(src []byte, replacement string) ([]byte, error) {
	idx := bytes.Index(src, []byte(preludeMarker))
	if idx < 0 {
		return nil, errors.New("installer: 脚本中没有前导标记")
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
	return out, nil
}

// RawScript 返回内嵌脚本的原文（不含票据），供测试与公开库同步校验使用。
func RawScript(name string) ([]byte, error) {
	if !IsScript(name) {
		return nil, fmt.Errorf("installer: 未知脚本 %q", name)
	}
	return assets.ReadFile("assets/" + name)
}
