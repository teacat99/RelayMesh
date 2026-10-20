// Package docs 把本目录下的 Markdown 文档随二进制一起发布。
//
// 这些文档本来就是写给人看的使用说明（也会同步到公开仓库）；内嵌之后，Hub 还能通过 MCP 工具 usage_guide
// 把它们按需读给接入的智能体——智能体不会用某个功能时，可以直接查手册，而不必凭记忆回答。
// 哪些文件算手册、各自的标题与摘要，登记在 internal/guide（测试保证本目录里的每个 .md 都登记过）。
//
// go:embed 不能引用上级目录，所以内嵌声明只能放在 docs/ 里。
package docs

import "embed"

// FS 是 docs/ 下全部 Markdown 文件（扁平，没有子目录）。
//
//go:embed *.md
var FS embed.FS
