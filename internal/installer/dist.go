package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/relsign"
)

// ChecksumsName 是校验文件名；格式与 sha256sum 一致（"<十六进制>  <文件名>"），install.sh / install.ps1 都按这个格式读取。
const ChecksumsName = "checksums.txt"

// assetRe 是允许分发的 Agent 二进制文件名。目录里出现的其他文件一律不对外提供，
// 下载路径里的文件名也必须先通过它（或是脚本 / 校验文件名），这样既不会有路径穿越，也不会把目录里的杂物发出去。
var assetRe = regexp.MustCompile(`^relaymesh-agent-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$`)

// ValidAssetName 判断 name 是否是允许分发的 Agent 二进制文件名。
func ValidAssetName(name string) bool { return assetRe.MatchString(name) }

// Asset 描述分发目录里的一个 Agent 二进制。
type Asset struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Platform 返回资产对应的平台标识，例如 "linux-amd64"。
func (a Asset) Platform() string {
	name := strings.TrimSuffix(strings.TrimPrefix(a.Name, "relaymesh-agent-"), ".exe")
	return name
}

type sumEntry struct {
	size  int64
	mtime time.Time
	hex   string
}

// versionEntry 缓存 VERSION 文件的解析结果（按大小与修改时间判断是否过期）。
type versionEntry struct {
	ok    bool
	size  int64
	mtime time.Time
	value string
}

// Dist 是 Agent 二进制的分发目录。目录由部署方提供（Docker 镜像里是 /app/agent-dist），服务端只读不写。
type Dist struct {
	dir string

	mu   sync.Mutex
	sums map[string]sumEntry
	ver  versionEntry
	rev  versionEntry
}

// NewDist 创建分发目录视图；dir 为空表示没有分发目录。目录是否存在、里面有什么，每次访问时才检查
// （部署方替换二进制后无需重启服务端）。
func NewDist(dir string) *Dist {
	return &Dist{dir: dir, sums: make(map[string]sumEntry)}
}

// Dir 返回分发目录路径（可能为空）。
func (d *Dist) Dir() string {
	if d == nil {
		return ""
	}
	return d.dir
}

// ResolveDir 确定分发目录：显式配置（RELAYMESH_AGENT_DIST_DIR）优先，即使它不存在也按配置走（部署方的意图明确）；
// 否则依次尝试可执行文件旁的 agent-dist、容器内的 /app/agent-dist 与工作目录下的 agent-dist，取第一个存在的目录。
func ResolveDir(explicit string) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "agent-dist"))
	}
	candidates = append(candidates, "/app/agent-dist", "agent-dist")
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

// List 列出目录里可分发的 Agent 二进制（按文件名排序）。目录不存在或不可读时返回空列表。
func (d *Dist) List() []Asset {
	if d == nil || d.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil
	}
	var out []Asset
	for _, e := range entries {
		if !assetRe.MatchString(e.Name()) {
			continue
		}
		// os.Stat 会跟随符号链接：部署方把 agent-dist 里的文件做成链接是合法用法，指向目录或设备则不是。
		st, err := os.Stat(filepath.Join(d.dir, e.Name()))
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			continue
		}
		out = append(out, Asset{Name: e.Name(), Size: st.Size(), ModTime: st.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Available 判断是否至少有一个可分发的 Agent 二进制。
func (d *Dist) Available() bool { return len(d.List()) > 0 }

// Platforms 返回可分发的平台标识（如 "linux-amd64"），按名称排序。
func (d *Dist) Platforms() []string {
	assets := d.List()
	out := make([]string, 0, len(assets))
	for _, a := range assets {
		out = append(out, a.Platform())
	}
	return out
}

// VersionFileName 是分发目录里记录「这批 Agent 二进制是哪个版本」的文件（第一行即版本号）。
// 镜像构建（Dockerfile.multistage）与 scripts/build-agent-dist.sh 会写入它；它不是可分发的文件，/i/ 不会发出它。
const VersionFileName = "VERSION"

// versionRe 限定版本号的字符集与长度：它会原样显示在界面里并参与比较，不接受任何奇怪的内容。
var versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// Version 返回分发目录里 Agent 的版本号（VERSION 文件的第一行，去掉首尾空白）。
// 文件不存在、不可读或内容不合规时返回空串——此时服务端无法判断主机的 Agent 是否过旧，也就不会给出升级提醒。
// 结果按文件的大小与修改时间缓存：主机列表会被频繁轮询，部署方替换文件后无需重启服务端。
func (d *Dist) Version() string {
	if d == nil {
		return ""
	}
	return d.firstLine(VersionFileName, &d.ver, versionRe)
}

// RevFileName 是分发目录里记录「这批 Agent 二进制的 Agent 侧代码指纹」的文件（第一行即指纹）。
// 它与 VERSION 同时写入（镜像构建与 scripts/build-agent-dist.sh），同样不是可分发的文件。
// 版本号跟着整个仓库的构建走，服务端每热更一次就变一次；指纹只在 Agent 依赖的代码真的变了才变——
// 服务端据此判断主机「可升级」，而不是拿版本号一比就让所有主机亮灯（计算方式见 internal/agentrev）。
const RevFileName = "REV"

// revRe 与 termproto.ValidAgentRev 的规则一致：小写十六进制，6–40 位。
var revRe = regexp.MustCompile(`^[0-9a-f]{6,40}$`)

// Rev 返回分发目录里这批 Agent 的代码指纹（REV 文件的第一行，去掉首尾空白）。
// 文件不存在（旧镜像 / 没有写 REV 的部署）、不可读或内容不合规时返回空串——此时服务端回退到版本号比较，行为与引入指纹之前一致。
func (d *Dist) Rev() string {
	if d == nil {
		return ""
	}
	return d.firstLine(RevFileName, &d.rev, revRe)
}

// firstLine 读取分发目录里一个小文本文件的第一行并按 valid 校验，结果按文件的大小与修改时间缓存。
func (d *Dist) firstLine(name string, cache *versionEntry, valid *regexp.Regexp) string {
	if d.dir == "" {
		return ""
	}
	path := filepath.Join(d.dir, name)
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > 1024 {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if cache.ok && cache.size == st.Size() && cache.mtime.Equal(st.ModTime()) {
		return cache.value
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimSpace(line)
	if !valid.MatchString(line) {
		line = ""
	}
	*cache = versionEntry{ok: true, size: st.Size(), mtime: st.ModTime(), value: line}
	return line
}

// ErrNotFound 表示请求的文件不在分发目录里（或根本不是允许分发的名字）。
var ErrNotFound = errors.New("installer: not found")

// Open 打开一个 Agent 二进制。name 必须通过 ValidAssetName，调用方拿到的 *os.File 由调用方关闭。
func (d *Dist) Open(name string) (*os.File, fs.FileInfo, error) {
	if d == nil || d.dir == "" || !assetRe.MatchString(name) {
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

// SignatureSuffix 是发布签名文件的后缀：每个 Agent 程序旁边放一个 <程序名>.sig，里面是 relsign 格式的签名文本。
// 签名文件不是可分发的文件（/i/ 不会发出它）：服务端读到后，只在回答静默自动升级的请求时原样转交给 Agent，
// 由 Agent 用自己信任的公钥验证——服务端自己不持有任何公钥，也就无从替 Agent 判断签名的真假。
const SignatureSuffix = ".sig"

// maxSignatureFileBytes 是签名文件大小的上限（合规的签名文本只有 100 来字节，加上换行也远小于它）。
const maxSignatureFileBytes = 512

// Signature 返回 Agent 程序 name 的发布签名文本（<name>.sig 的第一行，去掉首尾空白）。
// name 必须是可分发的程序名；签名文件不存在、不是普通文件、过大、不可读，或写法不合规时返回 false——
// 此时这个版本按「没有签名」处理（静默自动升级不会升级到它）。
func (d *Dist) Signature(name string) (string, bool) {
	if d == nil || d.dir == "" || !assetRe.MatchString(name) {
		return "", false
	}
	path := filepath.Join(d.dir, name+SignatureSuffix)
	// 与 List 一致用 os.Stat：部署方把 agent-dist 里的文件做成符号链接是合法用法；指向目录或设备则不是普通文件，同样拒绝。
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > maxSignatureFileBytes {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimSpace(line)
	if !relsign.ValidSignatureText(line) {
		return "", false
	}
	return line, true
}

// Checksums 生成 checksums.txt 的内容。哈希按「大小 + 修改时间」缓存：文件被替换后会自动重新计算。
// 没有任何可分发文件时返回 ErrNotFound。
func (d *Dist) Checksums() ([]byte, error) {
	assets := d.List()
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
			// 这个文件刚好消失或不可读：不对外声明它的校验值，其余文件照常给出。
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

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
