package files

import (
	"errors"
	"io/fs"
	"path"
	"strings"
	"syscall"
	"unicode"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Error 是带稳定错误码（termproto.FileErr*）的文件操作错误。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func fail(code, msg string) *Error { return &Error{Code: code, Msg: msg} }

// pathRules 描述一个平台的路径语法。windows 做成字段而不是读 runtime.GOOS，
// 是为了让两种语法都能在任何系统上单元测试——路径授权是这个包最关键的安全边界。
type pathRules struct {
	windows bool
	home    string
}

// absolute 把请求里的路径规范成该平台上干净的绝对路径（~ 展开为家目录），不访问文件系统。
// 相对路径、~user、UNC / 设备路径、盘符相对路径、备用数据流一律拒绝。
func (r pathRules) absolute(p string) (string, *Error) {
	if p == "" {
		return "", fail(termproto.FileErrBadPath, "路径为空")
	}
	if p == "~" || strings.HasPrefix(p, "~/") || (r.windows && strings.HasPrefix(p, `~\`)) {
		if r.home == "" {
			return "", fail(termproto.FileErrBadPath, "无法确定这台主机上账户的家目录，请改用绝对路径")
		}
		p = r.home + p[1:]
	} else if strings.HasPrefix(p, "~") {
		return "", fail(termproto.FileErrBadPath, "只支持 ~ 与 ~/…（不支持 ~用户名）")
	}
	if r.windows {
		return r.absoluteWindows(p)
	}
	if !strings.HasPrefix(p, "/") {
		return "", fail(termproto.FileErrBadPath, "必须是绝对路径")
	}
	return path.Clean(p), nil
}

func (r pathRules) absoluteWindows(p string) (string, *Error) {
	p = strings.ReplaceAll(p, "/", `\`)
	if strings.HasPrefix(p, `\\`) {
		return "", fail(termproto.FileErrBadPath, "不支持 UNC 与设备路径（\\\\…）")
	}
	if len(p) < 3 || p[1] != ':' || p[2] != '\\' || !isASCIILetter(p[0]) {
		return "", fail(termproto.FileErrBadPath, `必须是带盘符的绝对路径，例如 C:\data`)
	}
	rest := p[2:]
	if strings.Contains(rest, ":") {
		return "", fail(termproto.FileErrBadPath, "路径里不能出现冒号（备用数据流）")
	}
	for _, seg := range strings.Split(rest, `\`) {
		// 末尾的点或空格会被 Windows 悄悄去掉，同一个文件有多种写法，授权判断无法可靠比较：直接拒绝。
		if seg != "" && seg != "." && seg != ".." && (strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ")) {
			return "", fail(termproto.FileErrBadPath, "路径片段不能以点或空格结尾")
		}
	}
	clean := path.Clean(strings.ReplaceAll(rest, `\`, "/"))
	clean = strings.ReplaceAll(clean, "/", `\`)
	return strings.ToUpper(p[:1]) + ":" + clean, nil
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isFSRoot 判断规范化后的路径是否是文件系统根（/ 或 C:\）。把根当作允许目录等于不设防，一律不接受。
func (r pathRules) isFSRoot(abs string) bool {
	if r.windows {
		return len(abs) == 3 && abs[1] == ':' && abs[2] == '\\'
	}
	return abs == "/"
}

func (r pathRules) sep() string {
	if r.windows {
		return `\`
	}
	return "/"
}

// within 判断 p 是否在 root 之内（含 root 本身），返回相对 root 的路径（root 本身为 "."）。
// 两者都必须是 absolute 的结果。比较按路径片段进行：/srv/app 不包含 /srv/app2；Windows 上不区分大小写。
func (r pathRules) within(root, p string) (string, bool) {
	cr, cp := root, p
	if r.windows {
		cr, cp = strings.ToLower(root), strings.ToLower(p)
	}
	if cp == cr {
		return ".", true
	}
	prefix := cr
	if !strings.HasSuffix(prefix, r.sep()) {
		prefix += r.sep()
	}
	if !strings.HasPrefix(cp, prefix) {
		return "", false
	}
	if r.windows {
		// 取原始大小写的余下部分（ToLower 可能改变字节长度，所以按前缀的片段数重新切分）。
		return cutPrefixSegments(p, root, r.sep()), true
	}
	return p[len(prefix):], true
}

// cutPrefixSegments 去掉 p 开头与 root 相同数量的路径片段（用于大小写不敏感比较后保留原大小写）。
func cutPrefixSegments(p, root, sep string) string {
	n := len(strings.Split(strings.TrimRight(root, sep), sep))
	parts := strings.Split(p, sep)
	if len(parts) <= n {
		return "."
	}
	return strings.Join(parts[n:], sep)
}

// resolved 是路径授权的结果：在哪个允许目录之内、相对它的路径。
type resolved struct {
	Root string // 允许目录（规范化的绝对路径），用于 os.OpenRoot；Whole 时是文件系统根（/ 或 C:\）
	Rel  string // 相对 Root 的路径，Root 本身为 "."
	// Whole 表示令牌被明确授予「整个文件系统」：Root 只是路径的起点，不是边界——符号链接不再被限制在某个目录里。
	Whole bool
}

// wholeFS 把任意规范化的绝对路径解析为「文件系统根（Windows 上是该路径所在盘符的根）+ 相对路径」。
// 只在令牌被明确授予「整个文件系统」（Roots 里有 termproto.FileRootAll）时使用。
func (r pathRules) wholeFS(abs string) resolved {
	if r.windows {
		return resolved{Root: abs[:3], Rel: relOrDot(abs[3:]), Whole: true} // absolute 保证了 `X:\` 前缀
	}
	return resolved{Root: "/", Rel: relOrDot(abs[1:]), Whole: true}
}

func relOrDot(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

// resolve 把请求路径与令牌允许的目录对照：路径必须落在某个允许目录之内。
// 多个允许目录都包含它时取最短（最宽）的一个：它们都是管理员允许的范围，取最宽的可以让合法的跨目录符号链接成立。
// 允许目录里无法解析的条目与文件系统根被忽略（宁可拒绝也不放宽）；唯一的例外是 Hub 明确放进来的哨兵
// termproto.FileRootAll（令牌的「允许整个文件系统」开关）：它让任意规范的绝对路径通过。
func (r pathRules) resolve(roots []string, p string) (resolved, *Error) {
	abs, err := r.absolute(p)
	if err != nil {
		return resolved{}, err
	}
	for _, raw := range roots {
		if raw == termproto.FileRootAll {
			return r.wholeFS(abs), nil
		}
	}
	var best resolved
	found := false
	for _, raw := range roots {
		root, rerr := r.absolute(raw)
		if rerr != nil || r.isFSRoot(root) {
			continue
		}
		rel, ok := r.within(root, abs)
		if !ok {
			continue
		}
		if !found || len(root) < len(best.Root) {
			best, found = resolved{Root: root, Rel: rel}, true
		}
	}
	if !found {
		return resolved{}, fail(termproto.FileErrNotAllowed, "路径不在此令牌允许的目录范围内")
	}
	return best, nil
}

// classify 把 os.Root / 文件系统的错误归入稳定错误码。符号链接逃出允许目录属于授权失败，不是 I/O 错误。
func classify(err error) *Error {
	var pe *fs.PathError
	text := err.Error()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fail(termproto.FileErrNotFound, "文件或目录不存在")
	case errors.Is(err, fs.ErrPermission):
		return fail(termproto.FileErrPermission, "没有权限")
	case errors.Is(err, fs.ErrExist):
		return fail(termproto.FileErrExists, "目标已存在")
	case strings.Contains(text, "path escapes from parent"), strings.Contains(text, "symbolic link"), errors.Is(err, syscall.ELOOP):
		return fail(termproto.FileErrNotAllowed, "路径经符号链接指向允许目录之外（或链接层级过多）")
	case errors.Is(err, syscall.ENOTDIR):
		return fail(termproto.FileErrNotFound, "路径中的某一段不是目录")
	}
	if errors.As(err, &pe) {
		text = pe.Err.Error()
	}
	return fail(termproto.FileErrIO, "读写失败："+cleanText(text, 120))
}

// cleanText 把错误文本压成单行可显示的短文本（去掉控制字符与路径）。
func cleanText(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		if n++; n >= limit {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
