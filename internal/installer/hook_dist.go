package installer

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var hookAssetRe = regexp.MustCompile(`^relaymesh-hook-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$`)

// ValidHookAssetName 判断 name 是否是允许分发的 hook 二进制文件名。
func ValidHookAssetName(name string) bool { return hookAssetRe.MatchString(name) }

// ListHooks 列出目录里可分发的 hook 二进制。
func (d *Dist) ListHooks() []Asset {
	if d == nil || d.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil
	}
	var out []Asset
	for _, e := range entries {
		if !hookAssetRe.MatchString(e.Name()) {
			continue
		}
		st, err := os.Stat(filepath.Join(d.dir, e.Name()))
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			continue
		}
		out = append(out, Asset{Name: e.Name(), Size: st.Size(), ModTime: st.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HooksAvailable 判断是否有 hook 二进制可分发。
func (d *Dist) HooksAvailable() bool { return len(d.ListHooks()) > 0 }

// OpenHook 打开 hook 二进制。
func (d *Dist) OpenHook(name string) (*os.File, fs.FileInfo, error) {
	if d == nil || d.dir == "" || !hookAssetRe.MatchString(name) {
		return nil, nil, ErrNotFound
	}
	f, err := os.Open(filepath.Join(d.dir, name))
	if err != nil {
		return nil, nil, ErrNotFound
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		_ = f.Close()
		return nil, nil, ErrNotFound
	}
	return f, st, nil
}

// HookChecksums 只包含 hook 二进制的 checksums.txt。
func (d *Dist) HookChecksums() ([]byte, error) {
	assets := d.ListHooks()
	if len(assets) == 0 {
		return nil, ErrNotFound
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var b strings.Builder
	for _, a := range assets {
		if cached, ok := d.sums[a.Name]; ok && cached.size == a.Size && cached.mtime.Equal(a.ModTime) {
			b.WriteString(cached.hex + "  " + a.Name + "\n")
			continue
		}
		sum, err := hashFile(filepath.Join(d.dir, a.Name))
		if err != nil {
			delete(d.sums, a.Name)
			continue
		}
		d.sums[a.Name] = sumEntry{size: a.Size, mtime: a.ModTime, hex: sum}
		b.WriteString(sum + "  " + a.Name + "\n")
	}
	if b.Len() == 0 {
		return nil, ErrNotFound
	}
	return []byte(b.String()), nil
}
