package hookcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var httpClient = &http.Client{Timeout: httpTimeout}

func parseRPC(raw []byte) (map[string]any, error) {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "event:") || strings.HasPrefix(text, "data:") {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "data:") {
				text = strings.TrimSpace(line[5:])
				break
			}
		}
	}
	var rpc map[string]any
	if err := json.Unmarshal([]byte(text), &rpc); err != nil {
		return nil, err
	}
	return rpc, nil
}

func peek(ctx context.Context, ep endpoint, workflow string, reminder bool, source string, report map[string]any) (map[string]any, error) {
	args := map[string]any{
		"action":      "inbox_peek",
		"workflow_id": workflow,
		"source":      source,
	}
	if reminder {
		args["reminder"] = true
	}
	for k, v := range report {
		args[k] = v
	}
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "workflow_context",
			"arguments": args,
		},
	}
	rawBody, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if ep.Auth != "" {
		req.Header.Set("Authorization", ep.Auth)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	rpc, err := parseRPC(b)
	if err != nil {
		return nil, err
	}
	result, _ := rpc["result"].(map[string]any)
	if result == nil {
		return nil, fmt.Errorf("no result")
	}
	if result["isError"] == true {
		return nil, fmt.Errorf("tool error")
	}
	content, _ := result["content"].([]any)
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok || m["type"] != "text" {
			continue
		}
		text, _ := m["text"].(string)
		var data map[string]any
		if json.Unmarshal([]byte(text), &data) == nil && data != nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("no text")
}
