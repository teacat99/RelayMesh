package guide

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/docs"
	idocs "github.com/teacat99/RelayMesh/v2/internal/docs"
)

// 这一组测试守住「真实手册」本身的质量：登记表与文件一一对应、链接不悬空、没有私有信息、
// 关键问题能搜得到。内容对不对（比如每个 MCP 工具、每个权限位都有说明）由 internal/mcp 里的测试把关。

var topicIDRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func TestRegistryIsConsistent(t *testing.T) {
	seenID := map[string]bool{}
	seenFile := map[string]bool{}
	for _, tp := range Default.Topics() {
		if !topicIDRe.MatchString(tp.ID) {
			t.Errorf("topic id %q must be lowercase letters, digits and hyphens", tp.ID)
		}
		if seenID[tp.ID] {
			t.Errorf("duplicate topic id %q", tp.ID)
		}
		seenID[tp.ID] = true
		if seenFile[tp.File] {
			t.Errorf("duplicate topic file %q", tp.File)
		}
		seenFile[tp.File] = true
		if strings.ContainsAny(tp.File, "/\\") || !strings.HasSuffix(tp.File, ".md") {
			t.Errorf("topic %s: file %q must be a flat .md name", tp.ID, tp.File)
		}
		if strings.TrimSpace(tp.Title) == "" || strings.TrimSpace(tp.Summary) == "" {
			t.Errorf("topic %s needs a title and a summary", tp.ID)
		}
		if len(tp.Keywords) == 0 {
			t.Errorf("topic %s should list search keywords", tp.ID)
		}
		info, err := Default.Info(tp)
		if err != nil {
			t.Errorf("topic %s: %v", tp.ID, err)
			continue
		}
		if info.Chars == 0 {
			t.Errorf("topic %s is empty", tp.ID)
		}
	}
}

func TestEveryEmbeddedDocIsRegistered(t *testing.T) {
	entries, err := fs.ReadDir(docs.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			onDisk = append(onDisk, e.Name())
		}
	}
	var registered []string
	for _, tp := range Default.Topics() {
		registered = append(registered, tp.File)
	}
	sort.Strings(onDisk)
	sort.Strings(registered)
	if strings.Join(onDisk, ",") != strings.Join(registered, ",") {
		t.Fatalf("docs/*.md and the registry in topics.go must list the same files\n  on disk:    %v\n  registered: %v", onDisk, registered)
	}
}

func TestManualPagesAreWellFormed(t *testing.T) {
	for _, tp := range Default.Topics() {
		body, err := Default.Body(tp)
		if err != nil {
			t.Errorf("%s: %v", tp.ID, err)
			continue
		}
		hs := idocs.ParseHeadings(body)
		if len(hs) == 0 || hs[0].Level != 1 {
			t.Errorf("%s: the first heading must be a level-1 title", tp.ID)
		}
		h1 := 0
		for _, h := range hs {
			if h.Level == 1 {
				h1++
			}
		}
		if h1 != 1 {
			t.Errorf("%s: want exactly one level-1 heading, got %d", tp.ID, h1)
		}
		if strings.Contains(body, "\r") {
			t.Errorf("%s: use LF line endings", tp.ID)
		}
		// 只数以三个反引号开头的行（标题或正文里行内提到 ``` 不算围栏）。
		fences := 0
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fences++
			}
		}
		if fences%2 != 0 {
			t.Errorf("%s: unbalanced code fence", tp.ID)
		}
	}
}

// 手册里指向别的手册页的链接（写成 name.md 或 name.md#锚点）必须能解析到已登记的主题；
// 带锚点的，锚点还要真的存在。指向 ../README.md 之类手册外文件的链接不在检查范围内。
var mdLinkRe = regexp.MustCompile(`\]\(([^)\s#]+\.md)(#[^)\s]*)?\)`)

func TestManualCrossLinksResolve(t *testing.T) {
	for _, tp := range Default.Topics() {
		body, _ := Default.Body(tp)
		for _, m := range mdLinkRe.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.Contains(target, "/") || strings.HasPrefix(target, ".") {
				continue
			}
			dst, ok := Default.Lookup(target)
			if !ok {
				t.Errorf("%s links to %s, which is not a manual page", tp.ID, target)
				continue
			}
			anchor := strings.TrimPrefix(m[2], "#")
			if anchor == "" {
				continue
			}
			dstBody, _ := Default.Body(dst)
			found := false
			for _, h := range idocs.ParseHeadings(dstBody) {
				if h.Anchor == anchor {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s links to %s#%s, but that page has no such heading", tp.ID, target, anchor)
			}
		}
	}
}

// privateTerm 登记一个私有开发环境的标识（云主机别名、私有仓库名、机密文件名、个人域名）。这些名字本身不写进公开代码，
// 只存「转小写后的 SHA-256」与字节数；扫描时在手册正文（同样转小写）上按这个长度滑窗取摘要比对，
// 效果等同于「正文里不许出现这些字串」（不分大小写），报错也只说类别与位置、不回显原词。
// 要加一项：`printf %s '小写的词' | sha256sum` 取摘要，连同字节数登记在 privateTerms 里。
type privateTerm struct {
	sum  string // 小写后的 SHA-256（十六进制）
	size int    // 小写后的字节数
	what string // 只用在报错里，说明是哪一类
}

var privateTerms = []privateTerm{
	{"5a5c07672a4efaf609e62d921a60bf7cc0e9722a8b9608520d694520c0c41407", 6, "云主机别名"},
	{"7216e73c71c45d39ea177e2411483f99df737fc00578725e09c099a38800232c", 17, "私有仓库名"},
	{"84a5ad541ff3fa8b124c35b8b529ab341fecece08dd30598e7792c3ea67e98f6", 11, "机密文件名"},
	{"1d01aff47b91f89976bb9690f0cb2b5c516ba81d8ef3b5d4ce0449dc5733cc36", 12, "个人域名"},
}

// findPrivateTerms 返回 body 里命中的私有标识（类别@字节位置），不返回词本身。
func findPrivateTerms(body string, terms []privateTerm) []string {
	low := []byte(strings.ToLower(body))
	var hits []string
	for _, tm := range terms {
		want, err := hex.DecodeString(tm.sum)
		if err != nil || len(want) != sha256.Size || tm.size <= 0 {
			hits = append(hits, fmt.Sprintf("%s：登记项不合法", tm.what))
			continue
		}
		for i := 0; i+tm.size <= len(low); i++ {
			sum := sha256.Sum256(low[i : i+tm.size])
			if bytes.Equal(sum[:], want) {
				hits = append(hits, fmt.Sprintf("%s@%d", tm.what, i))
			}
		}
	}
	return hits
}

// 扫描器自己要靠得住：用一个只在测试里造出来的词验证「不分大小写、整串才算命中、不回显原词」，并确认登记表本身合法
// （登记项写坏了会让守护形同虚设，所以也要在这里拦住）。
func TestFindPrivateTerms(t *testing.T) {
	const word = "demo-private-word"
	sum := sha256.Sum256([]byte(word))
	terms := []privateTerm{{hex.EncodeToString(sum[:]), len(word), "演示词"}}
	hits := findPrivateTerms("前文 Demo-Private-WORD 后文，又一次 demo-private-word。", terms)
	if len(hits) != 2 {
		t.Fatalf("应命中 2 处（不分大小写），实际 %v", hits)
	}
	for _, h := range hits {
		if strings.Contains(strings.ToLower(h), word) {
			t.Errorf("报告里不该回显原词: %q", h)
		}
	}
	if hits := findPrivateTerms("前文 demo-private 后文", terms); len(hits) != 0 {
		t.Errorf("只含前半截不该命中，实际 %v", hits)
	}
	if hits := findPrivateTerms("x", []privateTerm{{"zz", 1, "坏登记项"}}); len(hits) != 1 {
		t.Errorf("摘要不是合法十六进制应被报出来，实际 %v", hits)
	}
	for _, tm := range privateTerms {
		if raw, err := hex.DecodeString(tm.sum); err != nil || len(raw) != sha256.Size || tm.size <= 0 || tm.what == "" {
			t.Errorf("privateTerms 里的登记项写坏了: %+v", tm)
		}
	}
}

// 手册随二进制发布（也会同步到公开仓库），不能带私有开发环境的信息，更不能带真实的密钥。
// 注意 `.cursor/mcp.json`、`.cursor/hooks.json` 是用户机器上 Cursor 客户端的配置路径，手册要讲，不在禁止之列；
// 禁止的是开发过程的私有记录目录，以及 privateTerms 登记的私有环境标识。
func TestManualHasNoPrivateOrSecretContent(t *testing.T) {
	forbidden := []string{
		".cursor/decisions", ".cursor/sessions", ".cursor/design", ".cursor/planning", ".cursor/tmp", ".cursor/agent-transcripts",
	}
	secretRes := map[string]*regexp.Regexp{
		"vault token":      regexp.MustCompile(`\brm-[0-9a-f]{32,}\b`),
		"capability token": regexp.MustCompile(`\brmc-[0-9A-Za-z_-]{20,}`),
		"bearer value":     regexp.MustCompile(`Bearer\s+[A-Za-z0-9._-]{24,}`),
		"private key":      regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	}
	for _, tp := range Default.Topics() {
		body, _ := Default.Body(tp)
		for _, f := range forbidden {
			if strings.Contains(body, f) {
				t.Errorf("%s mentions %q, which does not belong in the shipped manual", tp.ID, f)
			}
		}
		for _, hit := range findPrivateTerms(body, privateTerms) {
			t.Errorf("%s mentions a private-environment identifier (%s), which does not belong in the shipped manual", tp.ID, hit)
		}
		for name, re := range secretRes {
			if loc := re.FindStringIndex(body); loc != nil {
				t.Errorf("%s contains something that looks like a %s at byte %d", tp.ID, name, loc[0])
			}
		}
	}
}

// 几个最常见的问法必须搜得到，并且落在该落的主题上；搜索规则改了而这些问法搜不到了，就是回退。
func TestSearchFindsCommonQuestions(t *testing.T) {
	cases := []struct {
		query string
		topic string
	}{
		{"preset_run", "credentials-and-permissions"},
		{"async_ask", "confirm-items"},
		{"能力 URL", "execmesh-migration"},
		{"项目管理 Agent", "projects-and-access"},
		{"enforce", "projects-and-access"},
		{"terminal_files", "credentials-and-permissions"},
		{"追加指令", "inbox"},
		{"异步对话", "async-mode"},
		{"ntfy", "notifications"},
		{"rebind-workflow", "project-migration"},
		{"claim accept_delivery", "shared-task-board"},
		{"reset-auth", "faq"},
		{"usage_guide", "overview"},
		// 同义问法：手册正文写的是别的词，靠登记表里的 Keywords 对上（任务板写「派任务 / 接取」，用户常问「指派 / 认领 / 分配」）。
		{"任务 指派", "shared-task-board"},
		{"任务 认领", "shared-task-board"},
		{"任务 分配", "shared-task-board"},
		{"认领", "shared-task-board"},
		{"assign task", "shared-task-board"},
		{"凭据 权限", "credentials-and-permissions"},
		{"项目 角色", "projects-and-access"},
		{"确认 提问", "confirm-items"},
	}
	for _, c := range cases {
		hits := Default.Search(c.query, 8)
		found := false
		for _, h := range hits {
			if h.Topic == c.topic {
				found = true
				break
			}
		}
		if !found {
			var got []string
			for _, h := range hits {
				got = append(got, h.Topic+"#"+h.Section)
			}
			t.Errorf("search %q should reach topic %s, got %v", c.query, c.topic, got)
		}
	}
}

func TestSectionLookupOnRealManual(t *testing.T) {
	tp, ok := Default.Lookup("terminal-operations")
	if !ok {
		t.Fatal("terminal-operations missing")
	}
	// 智能体常常只记得小节编号的一部分：8.12 这一节讲运行预设。
	hits := Default.Search("运行预设 preset_run", 8)
	var anchor string
	for _, h := range hits {
		if h.Topic == tp.ID && h.Anchor != "" {
			anchor = h.Anchor
			break
		}
	}
	if anchor == "" {
		t.Fatalf("expected a terminal-operations hit for preset_run, got %+v", hits)
	}
	text, _, err := Default.Section(tp, "#"+anchor)
	if err != nil || !strings.Contains(text, "preset_run") {
		t.Fatalf("section by #anchor %q failed: err=%v", anchor, err)
	}
}
