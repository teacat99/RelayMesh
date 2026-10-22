package hookcli

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func scanProjectNoSecrets(t *testing.T, root string, secrets ...string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := path + "\n" + string(b)
		assertNoSecret(t, text, "项目文件", secrets...)
		return nil
	})
}

func TestTokenMatrix_UserHomeMcpJSON_Bearer(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "interval_seconds": 0})
	s.mcpJSON(map[string]any{"relaymesh": map[string]any{
		"url": f.url(), "headers": map[string]any{"Authorization": "Bearer matrix-bearer-token-xyz"},
	}})
	r := s.run(s.project, toolPayload(s.project), nil, "post-tool-use")
	if r.json(t)["additional_context"] != remindMsg {
		t.Fatal("Bearer 头应连通")
	}
	if h := f.hits(); len(h) != 1 || h[0].auth != "Bearer matrix-bearer-token-xyz" {
		t.Fatalf("auth: %+v", h)
	}
	assertNoSecret(t, r.raw, "stdout", "matrix-bearer-token-xyz")
	scanProjectNoSecrets(t, s.project, "matrix-bearer-token-xyz")
}

func TestTokenMatrix_ProjectMcpJSONNotUsed(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	s.writeJSON(filepath.Join(s.project, ".cursor", "mcp.json"), map[string]any{
		"mcpServers": map[string]any{"relaymesh": map[string]any{
			"url": f.url(), "headers": map[string]any{"Authorization": "Bearer only-in-project-mcp"},
		}},
	})
	out := s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t)
	if len(out) != 0 || len(f.hits()) != 0 {
		t.Fatal("项目级 mcp.json 不被读取，应静默 {}")
	}
}

func TestTokenMatrix_EnvExpansionBareName(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": false})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_server": "relaymesh", "interval_seconds": 0})
	s.mcpJSON(map[string]any{"relaymesh": map[string]any{
		"url": f.url(), "headers": map[string]any{"Authorization": "Bearer ${RM_PLAIN}"},
	}})
	s.run(s.project, toolPayload(s.project), []string{"RM_PLAIN=plain-expanded"}, "post-tool-use")
	if h := f.hits(); len(h) != 1 || h[0].auth != "Bearer plain-expanded" {
		t.Fatalf("${NAME} 展开: %+v", h)
	}
}

func TestTokenMatrix_MultiServerMatchByInstallHub(t *testing.T) {
	s := newGoSandbox(t)
	hub := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	other := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": hub.url(), "interval_seconds": 0})
	s.mcpJSON(map[string]any{
		"relaymesh": map[string]any{"url": hub.url(), "headers": map[string]any{"Authorization": "Bearer hub-correct"}},
		"other":     map[string]any{"url": other.url(), "headers": map[string]any{"Authorization": "Bearer other-wrong"}},
	})
	s.run(s.project, toolPayload(s.project), nil, "post-tool-use")
	if len(hub.hits()) != 1 || len(other.hits()) != 0 {
		t.Fatalf("只应请求 install 登记的 Hub: hub=%d other=%d", len(hub.hits()), len(other.hits()))
	}
	if hub.hits()[0].auth != "Bearer hub-correct" {
		t.Fatal("应用 relaymesh 条目令牌")
	}
}

func TestTokenMatrix_URLFormsBlackBox(t *testing.T) {
	cases := []struct {
		name   string
		suffix string
		secret string
	}{
		{"query", "?token=" + urlSecret, urlSecret},
		{"path", "/" + urlSecret, urlSecret},
		{"capability", "/c/" + capSecret, capSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newGoSandbox(t)
			f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
			s.config(map[string]any{"workflow_id": "wf-1", "interval_seconds": 0})
			s.mcpJSON(map[string]any{"relaymesh": map[string]any{"url": f.url() + tc.suffix}})
			r := s.run(s.project, toolPayload(s.project), nil, "post-tool-use")
			if r.json(t)["additional_context"] != remindMsg {
				t.Fatal("应找到 URL 内令牌")
			}
			if f.hits()[0].auth != "" {
				t.Fatal("URL 令牌形态不应加 Authorization")
			}
			assertNoSecret(t, r.raw, "输出", tc.secret)
			scanProjectNoSecrets(t, s.project, tc.secret)
		})
	}
}

func TestTokenMatrix_WrongNamedServerNoLeak(t *testing.T) {
	s := newGoSandbox(t)
	mine := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	leak := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": mine.url(), "mcp_server": "wrong-name", "interval_seconds": 0})
	s.mcpJSON(map[string]any{"wrong-name": map[string]any{"url": leak.url() + "?token=" + urlSecret}})
	out := s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t)
	if len(out) != 0 || len(mine.hits()) != 0 || len(leak.hits()) != 0 {
		t.Fatal("条目名与配置不一致时不应向条目主机发令牌")
	}
}
