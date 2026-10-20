package hookcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func readPayload(stdin []byte) map[string]any {
	if len(strings.TrimSpace(string(stdin))) == 0 {
		return map[string]any{}
	}
	var data map[string]any
	if json.Unmarshal(stdin, &data) != nil {
		return map[string]any{}
	}
	if data == nil {
		return map[string]any{}
	}
	return data
}

// findConfig 从 Cursor 传来的工作区根目录（以及工具的当前目录）往上找 .cursor/relaymesh.json。
func findConfig(payload map[string]any, start string) string {
	var dirs []string
	if roots, ok := payload["workspace_roots"].([]any); ok {
		for _, d := range roots {
			if s, ok := d.(string); ok && s != "" {
				dirs = append(dirs, s)
			}
		}
	}
	if cwd, ok := payload["cwd"].(string); ok && cwd != "" {
		dirs = append(dirs, cwd)
	}
	if start != "" {
		dirs = append(dirs, start)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	home, _ = filepath.Abs(home)
	for _, d := range dirs {
		cur, err := filepath.Abs(d)
		if err != nil || cur == "" {
			continue
		}
		for range 8 {
			if cur == home {
				break
			}
			path := filepath.Join(cur, configRel)
			if st, err := os.Stat(path); err == nil && !st.IsDir() {
				return path
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return ""
}
