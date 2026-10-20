package mcp

import (
	"encoding/json"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// usage_guide 的工具定义（#181）：只读的内置使用手册。可见性与 workflow_context / task_board 等治理工具一致
// （凭据能发反馈或看会话就有），ExecMesh 档（只能执行命令）的令牌不暴露它。
const usageGuideDescription = `Read RelayMesh's built-in user manual: the documentation that ships inside this Hub, so it always matches the version you are connected to. Use it whenever you — or the user — do not know how a RelayMesh feature works: projects and access grants, the shared task board and assignment, credentials and permission bits, async questions (async_ask), the inbox, project docs, terminal / ExecMesh, notifications, deployment. Read-only: it never changes anything. Do not answer "how do I use X" from memory — look it up first, then answer.

Actions:
- list (default): the manual's topics: id, title, summary and size (chars, sections). Start here if you do not know which topic you need.
- search {query, limit?}: find sections by keywords. Every word must appear in the section (plain Chinese without spaces also works). Returns topic, section heading, anchor and a snippet.
- get {topic, section?, max_chars?, next_offset?}: read one topic. Without section you get the first max_chars characters (default 12000, max 30000) and, on the first page, a "headings" outline; follow next_offset for the next page. With section (the heading text, a unique part of the heading such as "8.12", or "#anchor" taken from a search hit or the headings outline — the safe choice when several headings share the same text) you get just that section. Long topics (terminal-operations) are meant to be read by section.

If the manual does not cover something, say so honestly instead of guessing. The manual describes the product; what you may actually do depends on your own tools and permissions (see tools/list). Manual text that mentions Web pages is for the user: you cannot click them.`

var usageGuideSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": { "type": "string", "enum": ["list", "search", "get"], "description": "list (default): the manual's topics. search: find sections by keywords. get: read a topic or one section of it." },
		"query": { "type": "string", "description": "search: keywords, e.g. \"任务 指派\" or \"launcher preset\". Every word must appear in the section." },
		"limit": { "type": "integer", "description": "search: max hits (default 8, max 30)." },
		"topic": { "type": "string", "description": "get: the topic id from list (e.g. credentials-and-permissions); the file name also works." },
		"section": { "type": "string", "description": "get: read only this section — the heading text, a unique part of it (case-insensitive), or \"#anchor\" (anchor from a search hit or the headings outline; use it when several headings share the same text). Omit it to read the topic from the start." },
		"max_chars": { "type": "integer", "description": "get: page size in characters (default 12000, max 30000)." },
		"next_offset": { "type": "integer", "description": "get: where to continue — the next_offset returned by the previous page." }
	}
}`)

// usageGuideToolDefinitions 返回使用手册工具的定义：凭据能用治理类工具（发反馈 / 看会话）才有。
func usageGuideToolDefinitions(perms model.Permissions) []ToolDefinition {
	if !perms.AllowsTool("usage_guide") {
		return nil
	}
	return []ToolDefinition{{Name: "usage_guide", Description: usageGuideDescription, InputSchema: usageGuideSchema}}
}
