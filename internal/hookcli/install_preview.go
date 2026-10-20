package hookcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// install 的预览与备份（#113）：hooks.json 是用户自己的文件，改之前要让人看得见改什么、出了岔子能还原。

const (
	backupInfix = ".bak-"
	keepBackups = 3
)

// cloneDoc 深拷贝一份 JSON 文档（nil 得到空文档）。
func cloneDoc(doc map[string]any) map[string]any {
	out := map[string]any{}
	if doc != nil {
		b, _ := json.Marshal(doc)
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func sameDoc(a, b map[string]any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// encodeDoc 按 writeJSONAtomic 写文件的格式（两格缩进、不转义 HTML）编码文档。
func encodeDoc(doc map[string]any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
	return buf.String()
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// renderDiff 生成「当前内容 → 安装后内容」的逐行差异。两边都按写文件的格式重新编码后再比，只显示内容上的改动。
func renderDiff(path string, before, after map[string]any) string {
	var a []string
	oldLabel := "（文件不存在）"
	if before != nil {
		a = splitLines(encodeDoc(before))
		oldLabel = path + "（当前）"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s（安装后）\n", oldLabel, path)
	for _, line := range lineDiff(a, splitLines(encodeDoc(after)), 2) {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if before != nil {
		if raw, err := os.ReadFile(path); err == nil && string(raw) != encodeDoc(before) {
			sb.WriteString("注：写入时会统一缩进与键顺序（只影响格式，不影响内容）。\n")
		}
	}
	return sb.String()
}

// lineDiff 按行比较 a、b：改动行以「- 」「+ 」开头，上下文（每处改动前后各 ctx 行）以两个空格开头，不相邻的片段之间用 @@ 隔开。
func lineDiff(a, b []string, ctx int) []string {
	n, m := len(a), len(b)
	if n*m > 4_000_000 {
		return []string{"（文件较大，略去逐行差异）"}
	}
	// lcs[i][j] 是 a[i:] 与 b[j:] 的最长公共子序列长度。
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	type line struct {
		mark byte
		text string
	}
	var all []line
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			all = append(all, line{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			all = append(all, line{'-', a[i]})
			i++
		default:
			all = append(all, line{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		all = append(all, line{'-', a[i]})
	}
	for ; j < m; j++ {
		all = append(all, line{'+', b[j]})
	}
	show := make([]bool, len(all))
	for k, l := range all {
		if l.mark == ' ' {
			continue
		}
		for d := -ctx; d <= ctx; d++ {
			if k+d >= 0 && k+d < len(all) {
				show[k+d] = true
			}
		}
	}
	var out []string
	gap, started := false, false
	for k, l := range all {
		if !show[k] {
			gap = true
			continue
		}
		if gap && started {
			out = append(out, "@@")
		}
		gap, started = false, true
		out = append(out, string(l.mark)+" "+l.text)
	}
	if len(out) == 0 {
		return []string{"（没有差异）"}
	}
	return out
}

// backupFile 把 path 复制成同目录下的「文件名.bak-时间」（同一秒内重名就加序号），并只留最近 keepBackups 份；返回备份路径。
func backupFile(path string, now time.Time) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	stamp := now.Format("20060102-150405")
	dst := path + backupInfix + stamp
	for n := 1; ; n++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		if n > 99 {
			return "", fmt.Errorf("同一秒内的备份太多")
		}
		dst = fmt.Sprintf("%s%s%s-%d", path, backupInfix, stamp, n)
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return "", err
	}
	pruneBackups(path, keepBackups)
	return dst, nil
}

// pruneBackups 删掉 path 的旧备份，只留最近 keep 份（按文件名里的时间排序）。
func pruneBackups(path string, keep int) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := base + backupInfix
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	sort.Strings(names)
	for _, old := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, old))
	}
}
