package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/guide"
	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// usage_guide（#181）：内置使用手册，只读，按凭据权限可见。

func guideCall(t *testing.T, srv *Server, token string, args map[string]any) (map[string]any, string, bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := callTool(t, srv, token, "usage_guide", string(raw))
	if isErr {
		return nil, text, true
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("usage_guide 的结果不是 JSON 对象: %v\n%.300s", err, text)
	}
	return out, text, false
}

func mustGuide(t *testing.T, srv *Server, token string, args map[string]any) map[string]any {
	t.Helper()
	out, text, isErr := guideCall(t, srv, token, args)
	if isErr {
		t.Fatalf("usage_guide %v 失败: %.400s", args, text)
	}
	return out
}

func TestUsageGuideVisibleByPermission(t *testing.T) {
	srv := setupTestMCPServer(t)
	mk := func(name string, p model.Permissions) string { return newTerminalCredential(t, srv, name, p).Token }

	visible := map[string]string{
		"反馈 + 会话": mk("ug-both", model.Permissions{Feedback: true, Sessions: true}),
		"只有反馈":    mk("ug-feedback", model.Permissions{Feedback: true}),
		"只有会话":    mk("ug-sessions", model.Permissions{Sessions: true}),
		"混合档":     mk("ug-hybrid", model.Permissions{Feedback: true, Terminal: true}),
	}
	for name, token := range visible {
		if toolNames(t, srv, token)["usage_guide"] == nil {
			t.Errorf("%s 的令牌应该看到 usage_guide", name)
		}
	}

	hidden := map[string]string{
		"只有系统信息":      mk("ug-sysinfo", model.Permissions{SystemInfo: true}),
		"只有任务指挥":      mk("ug-configure", model.Permissions{Configure: true}),
		"ExecMesh 档":  mk("ug-exec", model.Permissions{Terminal: true}),
		"ExecMesh 兼容": mk("ug-compat", model.Permissions{Terminal: true, TerminalCompat: true}),
		"只有文件":        newFilesCredential(t, srv, "ug-files", model.Permissions{TerminalFiles: true}).Token,
	}
	for name, token := range hidden {
		if toolNames(t, srv, token)["usage_guide"] != nil {
			t.Errorf("%s 的令牌不应该看到 usage_guide", name)
		}
		// tools/list 看不到的工具，tools/call 也不能调用（两处由同一个判定）。
		body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"usage_guide","arguments":{"action":"list"}}}`
		_, resp := rpcFrom(t, srv, token, "198.51.100.9:4040", body)
		res, _ := resp.Result.(map[string]any)
		if resp.Error == nil && res["isError"] != true {
			t.Errorf("%s 的令牌不应该能调用 usage_guide：%#v", name, resp.Result)
		}
	}
}

func TestUsageGuideListIsTheWholeManual(t *testing.T) {
	srv := setupTestMCPServer(t)
	token := newTerminalCredential(t, srv, "ug-list", model.Permissions{Feedback: true}).Token

	// 没有 workflow_id、不带任何参数也能调用；缺省动作就是 list。
	for _, args := range []map[string]any{{}, {"action": "list"}, {"action": " LIST "}} {
		out := mustGuide(t, srv, token, args)
		topics, _ := out["topics"].([]any)
		if len(topics) != len(guide.Default.Topics()) || int(out["total"].(float64)) != len(topics) {
			t.Fatalf("list %v 应列出全部 %d 个主题，得到 %d", args, len(guide.Default.Topics()), len(topics))
		}
		for _, it := range topics {
			m := it.(map[string]any)
			for _, k := range []string{"id", "title", "summary", "chars", "sections"} {
				if v, ok := m[k]; !ok || v == "" || v == nil {
					t.Fatalf("主题条目缺 %s：%v", k, m)
				}
			}
		}
		if _, ok := out["hub_version"]; !ok {
			t.Fatal("list 应带 hub_version")
		}
		if hint, _ := out["hint"].(string); !strings.Contains(hint, "search") || !strings.Contains(hint, "get") {
			t.Fatalf("list 的提示应说明 search / get：%v", out["hint"])
		}
	}
}

func TestUsageGuideSearch(t *testing.T) {
	srv := setupTestMCPServer(t)
	token := newTerminalCredential(t, srv, "ug-search", model.Permissions{Sessions: true}).Token

	out := mustGuide(t, srv, token, map[string]any{"action": "search", "query": "preset_run"})
	hits, _ := out["hits"].([]any)
	if len(hits) == 0 || int(out["total"].(float64)) != len(hits) {
		t.Fatalf("preset_run 应该有命中：%v", out)
	}
	first := hits[0].(map[string]any)
	for _, k := range []string{"topic", "title", "snippet"} {
		if s, _ := first[k].(string); strings.TrimSpace(s) == "" {
			t.Fatalf("命中缺 %s：%v", k, first)
		}
	}
	if hint, _ := out["hint"].(string); !strings.Contains(hint, "#") {
		t.Fatalf("有命中时的提示应教会用 #anchor 取小节：%v", out["hint"])
	}

	// limit 生效。
	out = mustGuide(t, srv, token, map[string]any{"action": "search", "query": "工作流", "limit": 2})
	if hits, _ := out["hits"].([]any); len(hits) != 2 {
		t.Fatalf("limit=2 应只返回 2 条：%d", len(hits))
	}

	// 没有命中：给空数组（不是 null）与「如实说没有」的提示。
	out = mustGuide(t, srv, token, map[string]any{"action": "search", "query": "zzqq绝对不会出现的词"})
	if hits, ok := out["hits"].([]any); !ok || len(hits) != 0 {
		t.Fatalf("无命中时 hits 应是空数组：%v", out["hits"])
	}
	if hint, _ := out["hint"].(string); !strings.Contains(hint, "手册里没有") {
		t.Fatalf("无命中的提示应让智能体如实说没有：%v", out["hint"])
	}

	// 缺少 query 是调用错误。
	if _, text, isErr := guideCall(t, srv, token, map[string]any{"action": "search"}); !isErr || !strings.Contains(text, "query") {
		t.Fatalf("search 缺 query 应报错并点名 query：%v %q", isErr, text)
	}
}

func TestUsageGuideGetPagesReassembleExactly(t *testing.T) {
	srv := setupTestMCPServer(t)
	token := newTerminalCredential(t, srv, "ug-get", model.Permissions{Feedback: true}).Token

	for _, tc := range []struct {
		topic    string
		maxChars int
	}{
		{"faq", 700},
		{"terminal-operations", 30000}, // 最长的一篇：上限 30000 字符一页
		{"overview", 0},                // 缺省页长
	} {
		want, err := guide.Default.Body(mustGuideTopic(t, tc.topic))
		if err != nil {
			t.Fatal(err)
		}
		limit := tc.maxChars
		if limit == 0 {
			limit = usageGuideDefaultChars
		}
		total := len([]rune(want))
		minPages := (total + limit - 1) / limit // 每页不超过 limit，所以页数有下限

		var got strings.Builder
		offset, pages := 0, 0
		for {
			args := map[string]any{"action": "get", "topic": tc.topic, "next_offset": offset}
			if tc.maxChars > 0 {
				args["max_chars"] = tc.maxChars
			}
			out := mustGuide(t, srv, token, args)
			body, _ := out["body"].(string)
			if n := len([]rune(body)); n > limit {
				t.Fatalf("%s 第 %d 页 %d 字符，超过页长 %d", tc.topic, pages+1, n, limit)
			}
			got.WriteString(body)
			pages++
			if pages == 1 {
				heads, _ := out["headings"].([]any)
				if len(heads) == 0 {
					t.Fatalf("%s 第一页应带 headings 大纲", tc.topic)
				}
				h0 := heads[0].(map[string]any)
				if h0["level"] == nil || h0["text"] == "" || h0["anchor"] == "" {
					t.Fatalf("headings 条目不完整：%v", h0)
				}
			} else if _, has := out["headings"]; has {
				t.Fatalf("%s 只有第一页带 headings", tc.topic)
			}
			if int(out["total_chars"].(float64)) != len([]rune(want)) {
				t.Fatalf("%s total_chars=%v，全文 %d", tc.topic, out["total_chars"], len([]rune(want)))
			}
			next, more := out["next_offset"].(float64)
			if !more {
				if hint, _ := out["hint"].(string); strings.Contains(hint, "还没读完") {
					t.Fatalf("最后一页不该提示还没读完")
				}
				break
			}
			if hint, _ := out["hint"].(string); !strings.Contains(hint, fmt.Sprintf("next_offset: %d", int(next))) {
				t.Fatalf("翻页提示应写明下一个 next_offset：%v", out["hint"])
			}
			if int(next) <= offset {
				t.Fatalf("next_offset 必须向前推进：%d → %d", offset, int(next))
			}
			offset = int(next)
			if pages > 500 {
				t.Fatal("翻页没有终点")
			}
		}
		if got.String() != want {
			t.Fatalf("%s 分页拼回来的内容与全文不一致（%d 页，拼出 %d 字符，全文 %d）", tc.topic, pages, len([]rune(got.String())), len([]rune(want)))
		}
		if pages < minPages {
			t.Fatalf("%s 只读了 %d 页，预期至少 %d 页", tc.topic, pages, minPages)
		}
	}
}

func mustGuideTopic(t *testing.T, id string) guide.Topic {
	t.Helper()
	tp, ok := guide.Default.Lookup(id)
	if !ok {
		t.Fatalf("手册里没有主题 %s", id)
	}
	return tp
}

func TestUsageGuideGetSection(t *testing.T) {
	srv := setupTestMCPServer(t)
	token := newTerminalCredential(t, srv, "ug-section", model.Permissions{Feedback: true}).Token

	// 标题的唯一前缀就够用。
	out := mustGuide(t, srv, token, map[string]any{"action": "get", "topic": "credentials-and-permissions", "section": "运行预设"})
	body, _ := out["body"].(string)
	if !strings.Contains(body, "preset_run") || !strings.Contains(body, "`launcher`") {
		t.Fatalf("运行预设一节应讲 launcher 与 preset_run：%.200s", body)
	}
	if sec, _ := out["section"].(string); !strings.HasPrefix(sec, "运行预设") {
		t.Fatalf("结果应回显取到的小节标题：%v", out["section"])
	}
	if _, has := out["headings"]; has {
		t.Fatal("取单节时不带 headings 大纲")
	}
	if strings.Contains(body, "## 看不到某个工具时怎么排查") {
		t.Fatal("一节要在下一个同级标题处结束")
	}

	// 搜索命中里的 anchor 加 # 就能取到。
	hits, _ := mustGuide(t, srv, token, map[string]any{"action": "search", "query": "claim accept_delivery"})["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("claim accept_delivery 应有命中")
	}
	h := hits[0].(map[string]any)
	got := mustGuide(t, srv, token, map[string]any{"action": "get", "topic": h["topic"], "section": "#" + h["anchor"].(string)})
	if b, _ := got["body"].(string); !strings.Contains(b, "accept_delivery") {
		t.Fatalf("按命中的 #anchor 取小节应拿到命中的内容：%.200s", b)
	}

	// 找不到小节：报错里列出可用标题，方便下一次改对。
	_, text, isErr := guideCall(t, srv, token, map[string]any{"action": "get", "topic": "faq", "section": "完全不存在的小节"})
	if !isErr || !strings.Contains(text, "没有找到标题") || !strings.Contains(text, "连接与访问") {
		t.Fatalf("找不到小节时应列出可用标题：%v %q", isErr, text)
	}
	// 有歧义：说明匹配到多个。
	_, text, isErr = guideCall(t, srv, token, map[string]any{"action": "get", "topic": "faq", "section": "为什么"})
	if isErr && !strings.Contains(text, "多个小节") && !strings.Contains(text, "没有找到标题") {
		t.Fatalf("歧义或找不到都要说明原因：%q", text)
	}
}

func TestUsageGuideRejectsBadCalls(t *testing.T) {
	srv := setupTestMCPServer(t)
	token := newTerminalCredential(t, srv, "ug-bad", model.Permissions{Feedback: true}).Token

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"未知动作", map[string]any{"action": "delete"}, "list, search or get"},
		{"get 缺 topic", map[string]any{"action": "get"}, "topic"},
		{"未知主题", map[string]any{"action": "get", "topic": "no-such-topic"}, "overview"}, // 报错里列出可用主题
		{"负的 next_offset", map[string]any{"action": "get", "topic": "faq", "next_offset": -1}, "负数"},
		{"负的 max_chars", map[string]any{"action": "get", "topic": "faq", "max_chars": -5}, "负数"},
		{"offset 超出全文", map[string]any{"action": "get", "topic": "faq", "next_offset": 99999999}, "超出"},
	}
	for _, c := range cases {
		_, text, isErr := guideCall(t, srv, token, c.args)
		if !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%s：应报错并包含 %q，得到 isErr=%v %q", c.name, c.want, isErr, text)
		}
	}

	// 参数类型不对。
	text, isErr := callTool(t, srv, token, "usage_guide", `{"limit":"八"}`)
	if !isErr || !strings.Contains(text, "invalid arguments") {
		t.Errorf("参数类型错误应报 invalid arguments：%v %q", isErr, text)
	}

	// max_chars 超过上限时被收敛到 30000，不报错。
	out := mustGuide(t, srv, token, map[string]any{"action": "get", "topic": "terminal-operations", "max_chars": 10_000_000})
	if n := int(out["returned_chars"].(float64)); n > usageGuideMaxChars {
		t.Errorf("max_chars 应收敛到 %d，实际返回 %d 字符", usageGuideMaxChars, n)
	}
}

func TestUsageGuideTopicRefAcceptsFileName(t *testing.T) {
	srv := setupTestMCPServer(t)
	token := newTerminalCredential(t, srv, "ug-file", model.Permissions{Feedback: true}).Token
	for _, ref := range []string{"faq", "FAQ", "faq.md"} {
		out := mustGuide(t, srv, token, map[string]any{"action": "get", "topic": ref, "max_chars": 300})
		if out["topic"] != "faq" {
			t.Errorf("topic=%q 应解析到 faq：%v", ref, out["topic"])
		}
	}
}

func TestUsageGuideInstructionsSection(t *testing.T) {
	srv := setupTestMCPServer(t)
	mk := func(name string, p model.Permissions) string { return newTerminalCredential(t, srv, name, p).Token }

	for name, token := range map[string]string{
		"治理":   mk("ug-i-gov", model.Permissions{Feedback: true}),
		"只读会话": mk("ug-i-sess", model.Permissions{Sessions: true}),
		"混合":   mk("ug-i-hyb", model.Permissions{Feedback: true, Terminal: true}),
	} {
		text := initializeInstructions(t, srv, token)
		if !strings.Contains(text, "12b. 使用手册") || !strings.Contains(text, "usage_guide") {
			t.Errorf("%s 令牌的指引应带使用手册一节", name)
		}
		// 编号顺序：引用链接（12）→ 使用手册（12b）→ 等待流程（13，最末）。
		i12, i12b, i13 := strings.Index(text, "12. "), strings.Index(text, "12b. "), strings.Index(text, "13. 等待用户回复")
		if i12b < 0 || (i12 >= 0 && i12 > i12b) || (i13 >= 0 && i13 < i12b) {
			t.Errorf("%s：使用手册一节应排在引用链接之后、等待流程之前（12=%d 12b=%d 13=%d）", name, i12, i12b, i13)
		}
	}
	// 没有 interactive_feedback 的令牌没有等待流程，使用手册一节照样在（只要工具可见）。
	if text := initializeInstructions(t, srv, mk("ug-i-sess2", model.Permissions{Sessions: true})); strings.Contains(text, "13. 等待用户回复") {
		t.Error("只读会话令牌不该有等待流程一节")
	}

	for name, token := range map[string]string{
		"ExecMesh":    mk("ug-i-exec", model.Permissions{Terminal: true}),
		"只有系统信息":      mk("ug-i-sys", model.Permissions{SystemInfo: true}),
		"ExecMesh 文件": newFilesCredential(t, srv, "ug-i-files", model.Permissions{TerminalFiles: true}).Token,
	} {
		if text := initializeInstructions(t, srv, token); strings.Contains(text, "usage_guide") || strings.Contains(text, "使用手册") {
			t.Errorf("%s 令牌看不到 usage_guide，指引里也不该提它", name)
		}
	}

	// 指引每次 initialize 都会下发，这一节要保持精炼。
	if n := len([]rune(usageGuideInstructionsSection)); n > 600 {
		t.Errorf("使用手册一节过长：%d 字，上限 600", n)
	}
}

// ---- 质量门：手册要跟得上工具与权限位 ----

// manualText 返回某个主题的全文。
func manualText(t *testing.T, id string) string {
	t.Helper()
	body, err := guide.Default.Body(mustGuideTopic(t, id))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// 每个 MCP 工具（含 ExecMesh 兼容命名）都要出现在功能地图里：新增工具而忘了写手册会在这里失败。
func TestManualMapsEveryMCPTool(t *testing.T) {
	all := model.AllPermissions()
	all.Terminal, all.TerminalFiles, all.Launcher = true, true, true
	compat := model.Permissions{Feedback: true, Terminal: true, TerminalCompat: true}

	names := map[string]bool{}
	for _, p := range []model.Permissions{all, compat} {
		for _, d := range GetToolDefinitionsForPermissions(p) {
			names[d.Name] = true
		}
	}
	if len(names) < 20 {
		t.Fatalf("工具清单异常少：%d 个", len(names))
	}
	overview := manualText(t, "overview")
	var missing []string
	for name := range names {
		if !strings.Contains(overview, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("功能地图（docs/overview.md）漏了这些 MCP 工具，请补上：%v", missing)
	}
}

// 每个权限位（Permissions 的 JSON 键）都要在「凭据与权限位」里有说明。
func TestManualDocumentsEveryPermissionBit(t *testing.T) {
	raw, err := json.Marshal(model.Permissions{})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) < 10 {
		t.Fatalf("权限位异常少：%d 个", len(keys))
	}
	page := manualText(t, "credentials-and-permissions")
	var missing []string
	for k := range keys {
		if !strings.Contains(page, "`"+k+"`") {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("docs/credentials-and-permissions.md 漏了这些权限位，请补上：%v", missing)
	}
}

// 任务板与运行预设的动作枚举也要在手册里讲到（枚举里新增了取值而手册没跟上，这里失败）。
func TestManualCoversToolActionEnums(t *testing.T) {
	all := model.AllPermissions()
	all.Launcher = true
	defs := map[string]ToolDefinition{}
	for _, d := range GetToolDefinitionsForPermissions(all) {
		defs[d.Name] = d
	}

	check := func(tool, prop, topic string) {
		t.Helper()
		def, ok := defs[tool]
		if !ok {
			t.Fatalf("没有工具 %s", tool)
		}
		var schema any
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		vals := findEnum(schema, prop)
		if len(vals) == 0 {
			t.Fatalf("在 %s 的参数里没找到 %s 的枚举", tool, prop)
		}
		page := manualText(t, topic)
		for _, v := range vals {
			if v == "" {
				continue
			}
			if !strings.Contains(page, v) {
				t.Errorf("%s.%s 的取值 %q 没有出现在 docs/%s.md 里", tool, prop, v, topic)
			}
		}
	}
	check("task_board", "action", "shared-task-board")
	check("task_board", "claim", "shared-task-board")
	check("task_board", "kind", "shared-task-board")
	check("task_board", "stage", "shared-task-board")
	check("task_board", "states", "shared-task-board")
	check("preset_run", "action", "credentials-and-permissions")
	check("usage_guide", "action", "overview")
}

// findEnum 在 JSON Schema 里深度优先找名为 prop 的属性，返回它（或它 items）上的 enum 取值。
func findEnum(node any, prop string) []string {
	switch n := node.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			if p, ok := props[prop].(map[string]any); ok {
				if vals := enumOf(p); len(vals) > 0 {
					return vals
				}
			}
		}
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if vals := findEnum(n[k], prop); len(vals) > 0 {
				return vals
			}
		}
	case []any:
		for _, it := range n {
			if vals := findEnum(it, prop); len(vals) > 0 {
				return vals
			}
		}
	}
	return nil
}

func enumOf(p map[string]any) []string {
	raw, _ := p["enum"].([]any)
	if len(raw) == 0 {
		if items, ok := p["items"].(map[string]any); ok {
			raw, _ = items["enum"].([]any)
		}
	}
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
