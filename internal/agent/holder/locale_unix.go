//go:build !windows

package holder

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 会话必须运行在「确实可用的」UTF-8 locale 下。
//
// 浏览器与 xterm.js 发出的永远是 UTF-8 字节。如果主机上的会话运行在 C / POSIX（或者名字写着 UTF-8、
// 但这台机器并没有生成它）的 locale 里，bash 的 readline 会把 UTF-8 的每个高位字节当成 Meta 键：
// 例如「你」的第三个字节 0xA0 变成 M-空格、「好」的第三个字节 0xBD 变成 M-=（列出补全）。
// 现象就是中文输入「没反应 / 乱跳 / 弹出一串补全」。以前只在 LANG 与 LC_ALL 都为空时补一个 C.UTF-8，
// 既没处理 LANG=C，也没确认 C.UTF-8 真的存在（CentOS 7 等老系统没有它）。
//
// 第二层原因（2026-10-08 在两台 Debian 主机上实测）：会话是登录 shell，执行 ~/.profile 时 LANG 又被改回 C
// （Debian 安装器写在 root 的 ~/.profile 里），所以光在启动时把环境设对不够，要把字符类别钉在 LC_CTYPE 上，见 ensureUTF8。

// localeTool 抽象对系统 `locale` 命令的调用，便于测试替换。
type localeTool interface {
	// Charmap 在 LC_ALL=name 下运行 `locale charmap`。命令不存在或无法运行时 ok=false。
	// 对不存在的 locale，glibc 会静默回落到 C，因此返回的是 ANSI_X3.4-1968 而不是错误。
	Charmap(name string) (charmap string, ok bool)
	// Installed 返回 `locale -a` 列出的已安装 locale；无法获取时返回 nil。
	Installed() []string
}

type sysLocaleTool struct{}

const localeCmdTimeout = 3 * time.Second

func localeBin() string {
	if p, err := exec.LookPath("locale"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/bin/locale", "/bin/locale", "/usr/local/bin/locale"} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

func runLocale(env []string, args ...string) (string, bool) {
	bin := localeBin()
	if bin == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), localeCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/local/bin"}, env...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (sysLocaleTool) Charmap(name string) (string, bool) {
	out, ok := runLocale([]string{"LC_ALL=" + name}, "charmap")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func (sysLocaleTool) Installed() []string {
	out, ok := runLocale([]string{"LC_ALL=C"}, "-a")
	if !ok {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

var (
	localeMu    sync.Mutex
	localeTools localeTool = sysLocaleTool{}
	utf8Cache              = map[string]bool{} // locale 名 -> 是否可用（探测结果；整个进程内不会变化）
	pickCache   string                         // 选出的 UTF-8 locale
)

// resetLocaleCache 清空探测缓存（仅供测试）。
func resetLocaleCache() {
	localeMu.Lock()
	defer localeMu.Unlock()
	utf8Cache = map[string]bool{}
	pickCache = ""
}

// isUTF8Name 只看名字：C、POSIX、zh_CN.GBK、en_US（没有字符集后缀）都不是 UTF-8。
func isUTF8Name(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "utf-8") || strings.Contains(n, "utf8")
}

// utf8Usable 判断这个 locale 名在本机上是否真的会得到 UTF-8。
// 找不到 `locale` 命令（Alpine / musl 等）时无法验证，只能相信名字。
func utf8Usable(name string) bool {
	if !isUTF8Name(name) {
		return false
	}
	localeMu.Lock()
	if v, ok := utf8Cache[name]; ok {
		localeMu.Unlock()
		return v
	}
	tool := localeTools
	localeMu.Unlock()

	v := true
	if cm, ok := tool.Charmap(name); ok {
		v = strings.EqualFold(cm, "UTF-8") || strings.EqualFold(cm, "UTF8")
	}
	localeMu.Lock()
	utf8Cache[name] = v
	localeMu.Unlock()
	return v
}

// pickUTF8Locale 选一个本机可用的 UTF-8 locale：优先通用名字，其次任意已安装的 UTF-8 locale。
func pickUTF8Locale() string {
	localeMu.Lock()
	if pickCache != "" {
		p := pickCache
		localeMu.Unlock()
		return p
	}
	tool := localeTools
	localeMu.Unlock()

	prefer := []string{"C.UTF-8", "C.utf8", "en_US.UTF-8", "en_US.utf8"}
	fallback := "C.UTF-8"
	if runtime.GOOS == "darwin" {
		// macOS 没有 C.UTF-8，en_US.UTF-8 一定在。
		prefer = []string{"en_US.UTF-8", "C.UTF-8"}
		fallback = "en_US.UTF-8"
	}
	pick := ""
	for _, n := range prefer {
		if utf8Usable(n) {
			pick = n
			break
		}
	}
	if pick == "" {
		for _, n := range tool.Installed() {
			if isUTF8Name(n) && utf8Usable(n) {
				pick = n
				break
			}
		}
	}
	if pick == "" {
		// 一个 UTF-8 locale 都没有：退回通用名字（尽力而为，比保持 C 好——至少 musl / 最小化环境认得它）。
		pick = fallback
	}
	localeMu.Lock()
	pickCache = pick
	localeMu.Unlock()
	return pick
}

// ensureUTF8 保证 env 描述的会话环境运行在可用的 UTF-8 locale 下，并把字符类别「钉」在 LC_CTYPE 上。
//
// 为什么要钉，而不是只在 LANG 不对时才补：会话是登录 shell，它会在我们设好环境「之后」才执行
// /etc/profile、~/.profile 这些脚本，而其中常有把 LANG 改回 C 的赋值——Debian 安装器给 root 的 ~/.profile
// 就写着 `LANG=C` 与 `LANGUAGE=C`（理由是 zh_CN.UTF-8 在 Linux 文本控制台上显示不了汉字）。只设 LANG 的话，
// 脚本一跑，会话又回到 C locale，readline 又把中文字节当 Meta 键。LC_CTYPE 优先于 LANG，且不会被
// 「LANG=…」这类赋值改动，所以把字符类别钉在它上面；消息语言与排序（LANG、LANGUAGE）仍由用户的脚本说了算。
//
//   - LC_ALL 已设置：它盖过其余一切，只能改它本身（不是可用的 UTF-8 时换掉），不再设 LC_CTYPE。
//     登录脚本自己再改 LC_ALL / LC_CTYPE 的话，环境变量就无能为力了，需要用户去改脚本。
//   - 否则 LC_CTYPE 取：已经设置且可用的 LC_CTYPE > 可用的 LANG（保留用户的语言偏好，例如 zh_CN.UTF-8）> 本机挑出的 UTF-8 locale。
//   - LANG 也没设（systemd / launchd 的精简环境）：顺带补上 LANG，让只看 LANG 的程序也能得到 UTF-8。
func ensureUTF8(env map[string]string) {
	if v := env["LC_ALL"]; v != "" {
		if !utf8Usable(v) {
			env["LC_ALL"] = pickUTF8Locale()
		}
		return
	}
	if v := env["LC_CTYPE"]; v != "" && utf8Usable(v) {
		return
	}
	target := ""
	// LC_CTYPE 被显式设成了不可用的值（POSIX、GBK…）时不采信 LANG：那是用户有意写的，但对浏览器来说是错的，直接换成挑出来的。
	if env["LC_CTYPE"] == "" {
		if lang := env["LANG"]; lang != "" && utf8Usable(lang) {
			target = lang
		}
	}
	if target == "" {
		target = pickUTF8Locale()
	}
	env["LC_CTYPE"] = target
	if env["LANG"] == "" {
		env["LANG"] = target
	}
}
