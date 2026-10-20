package hookcli

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func throttleOK(workflow, url string, interval float64) bool {
	if interval <= 0 {
		return true
	}
	sum := sha1.Sum([]byte(workflow + "\n" + normURL(maskURL(url))))
	key := hex.EncodeToString(sum[:])[:12]
	path := filepath.Join(os.TempDir(), "relaymesh-inbox-"+key+".json")
	now := float64(time.Now().Unix())
	var last float64
	if b, err := os.ReadFile(path); err == nil {
		var st map[string]any
		if json.Unmarshal(b, &st) == nil {
			if v, ok := st["last"].(float64); ok {
				last = v
			}
		}
	}
	if now-last >= 0 && now-last < interval {
		return false
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	_ = os.WriteFile(tmp, []byte(fmt.Sprintf(`{"last": %f}`, now)), 0o644)
	_ = os.Rename(tmp, path)
	return true
}
