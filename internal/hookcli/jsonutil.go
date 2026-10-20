package hookcli

import (
	"encoding/json"
	"fmt"
	"os"
)

func loadJSON(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var data map[string]any
	if json.Unmarshal(b, &data) != nil {
		return map[string]any{}
	}
	if data == nil {
		return map[string]any{}
	}
	return data
}

func writeJSONAtomic(path string, data map[string]any) error {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func readJSONStrict(path string) (map[string]any, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("无法解析 %s：%w\n请先修好这个文件再重试（不会覆盖它）。", path, err)
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("无法解析 %s：%v\n请先修好这个文件再重试（不会覆盖它）。", path, err)
	}
	if data == nil {
		return nil, fmt.Errorf("%s 的顶层不是 JSON 对象，请先检查。", path)
	}
	return data, nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
