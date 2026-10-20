package hookcli

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGoTokenFromMcpJSON_ByNameAndEnvExpansion(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": false})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_server": "rm", "interval_seconds": 0})
	s.mcpJSON(map[string]any{"rm": map[string]any{
		"url":     "${env:RM_HUB}",
		"headers": map[string]any{"Authorization": "Bearer ${env:RM_SECRET}"},
	}})
	s.run(s.project, toolPayload(s.project), []string{"RM_HUB=" + f.url(), "RM_SECRET=expanded-secret"}, "post-tool-use")
	hits := f.hits()
	if len(hits) != 1 || hits[0].auth != "Bearer expanded-secret" {
		t.Fatalf("应按条目名展开 env: %+v", hits)
	}
}

func TestGoCustomTokenEnvName(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": false})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0, "token_env": "MY_HUB_TOKEN"})
	s.run(s.project, toolPayload(s.project), []string{"RELAYMESH_TOKEN=ignored", "MY_HUB_TOKEN=custom-one"}, "post-tool-use")
	if h := f.hits(); len(h) != 1 || h[0].auth != "Bearer custom-one" {
		t.Fatalf("token_env: %+v", h)
	}
}

func TestGoTokenInMcpJSONURL(t *testing.T) {
	for _, tc := range []struct {
		name, suffix, wantPath, wantQuery string
	}{
		{"查询参数", "?token=" + urlSecret, "/mcp", "token=" + urlSecret},
		{"路径", "/" + urlSecret, "/mcp/" + urlSecret, ""},
		{"令牌路径", "/token/" + urlSecret, "/mcp/token/" + urlSecret, ""},
		{"能力网址", "/c/" + capSecret, "/mcp/c/" + capSecret, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGoSandbox(t)
			f := newFakeMCP(t, map[string]any{"remind": true, "unread": 2, "message": remindMsg, "stop_message": stopMsg})
			s.config(map[string]any{"workflow_id": "wf-1", "interval_seconds": 0})
			s.mcpJSON(map[string]any{"relaymesh": map[string]any{"url": f.url() + tc.suffix}})

			post := s.run(s.project, toolPayload(s.project), nil, "post-tool-use")
			if got := post.json(t); got["additional_context"] != remindMsg {
				t.Fatalf("post-tool-use: %v", got)
			}
			stop := s.run(s.project, stopPayload(s.project, "completed", 0), nil, "stop")
			if got := stop.json(t); got["followup_message"] != stopMsg {
				t.Fatalf("stop: %v", got)
			}
			hits := f.hits()
			if len(hits) != 2 {
				t.Fatalf("want 2 hits, got %d", len(hits))
			}
			for _, h := range hits {
				if h.path != tc.wantPath || h.query != tc.wantQuery || h.auth != "" {
					t.Fatalf("请求应原样带令牌地址、无 Authorization: %+v", h)
				}
			}
			assertNoSecret(t, post.raw+stop.raw, "hook 输出", urlSecret, capSecret)
		})
	}
}

func TestGoTokenInMcpJSONURL_WithBareMcpURLInConfig(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "mcp_server": "rm", "interval_seconds": 0})
	s.mcpJSON(map[string]any{"rm": map[string]any{"url": f.url() + "?token=" + urlSecret}})
	if got := s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t); got["additional_context"] != remindMsg {
		t.Fatalf("同一服务应用条目地址令牌: %v", got)
	}
	if h := f.hits(); len(h) != 1 || h[0].query != "token="+urlSecret {
		t.Fatalf("query token: %+v", h)
	}
}

func TestGoTokenInMcpJSONURL_MatchedByBareMcpURL(t *testing.T) {
	s := newGoSandbox(t)
	f := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	decoy := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": f.url(), "interval_seconds": 0})
	s.mcpJSON(map[string]any{
		"decoy": map[string]any{"url": decoy.url() + "?token=rm-decoy-token-999999"},
		"hub":   map[string]any{"url": f.url() + "/" + urlSecret},
	})
	if got := s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t); got["additional_context"] != remindMsg {
		t.Fatalf("只差令牌匹配: %v", got)
	}
	if h := f.hits(); len(h) != 1 || h[0].path != "/mcp/"+urlSecret {
		t.Fatalf("带令牌路径: %+v", h)
	}
	if len(decoy.hits()) != 0 {
		t.Fatal("decoy 不应收到请求")
	}
}

func TestGoTokenInMcpJSONURL_NeverSentToAnotherServer(t *testing.T) {
	s := newGoSandbox(t)
	mine := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	other := newFakeMCP(t, map[string]any{"remind": true, "message": remindMsg})
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": mine.url(), "mcp_server": "elsewhere", "interval_seconds": 0})
	s.mcpJSON(map[string]any{"elsewhere": map[string]any{"url": other.url() + "?token=" + urlSecret}})
	out := s.run(s.project, toolPayload(s.project), nil, "post-tool-use").json(t)
	if len(out) != 0 || len(mine.hits()) != 0 || len(other.hits()) != 0 {
		t.Fatalf("不同服务不发请求: out=%v mine=%d other=%d", out, len(mine.hits()), len(other.hits()))
	}
}

func TestGoStop_ReportFailsOpenWhenServerIsDown(t *testing.T) {
	s := newGoSandbox(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL + "/mcp"
	closed.Close()
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": closedURL})
	for _, status := range []string{"error", "aborted", "completed"} {
		if len(s.run(s.project, stopPayload(s.project, status, 0), tokenEnv(), "stop").json(t)) != 0 {
			t.Fatalf("%s 应静默 {}", status)
		}
	}
	slow := newFakeMCP(t, map[string]any{"unread": 2, "stop_message": stopMsg})
	slow.delay = 4 * time.Second
	s.config(map[string]any{"workflow_id": "wf-1", "mcp_url": slow.url()})
	r := s.run(s.project, stopPayload(s.project, "error", 0), tokenEnv(), "stop")
	if len(r.json(t)) != 0 || r.took > 3500*time.Millisecond {
		t.Fatalf("慢服务端 stop 应快速放行: %s", r.took)
	}
}
