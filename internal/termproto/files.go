package termproto

import (
	"encoding/binary"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 文件能力（P2）协议：Hub 经 Agent 在主机上做 stat / hash / 拉取 / 经 URL 推送。
//
// 授权分两层：Hub 判定令牌能不能碰这台主机（权限位、主机白名单、闸门、模式），随请求下发令牌允许的目录（roots）；
// Agent 在本机做权威检查——路径必须落在 roots 之内，符号链接也不能逃出去（os.Root），推送的网址不得指向回环 / 私网 / 链路本地。
// 文件内容不经过 Hub 的数据库与审计：拉取时 Hub 只做内存里的中继，推送由 Agent 直接下载。
const (
	TFileReq = "file_req" // Hub→Agent：文件请求
	TFileRes = "file_res" // Agent→Hub：文件应答（成功与失败；read 成功时用 FrameFileData）
)

// FrameFileData 是 read 请求的应答：[0x07][req u32][标志 u8][起点 u64][文件总大小 u64][净荷]（Agent→Hub）。
const FrameFileData byte = 0x07

// 文件请求的操作。
const (
	FileOpStat    = "stat"     // 取元数据（不存在不算错误）
	FileOpHash    = "hash"     // 计算 sha256
	FileOpOpen    = "open"     // 打开一个文件准备分块读取（拉取用），返回句柄
	FileOpRead    = "read"     // 读取句柄的一个区间
	FileOpClose   = "close"    // 释放句柄
	FileOpPushURL = "push_url" // 由 Agent 下载网址内容并原子写入目标路径
)

// ValidFileOp 判断操作名是否为已知值。
func ValidFileOp(op string) bool {
	switch op {
	case FileOpStat, FileOpHash, FileOpOpen, FileOpRead, FileOpClose, FileOpPushURL:
		return true
	}
	return false
}

// FrameFileData 的标志位。
const (
	FileFlagEOF  byte = 1 << 0 // 已读到文件末尾
	FileFlagLast byte = 1 << 1 // 本次 read 请求的最后一帧
)

// 文件操作的稳定错误码（FileRes.Code）。
const (
	FileErrBadRequest   = "bad_request"      // 参数无效
	FileErrBadPath      = "bad_path"         // 路径不是绝对路径 / 含非法字符 / 格式不支持
	FileErrNotAllowed   = "path_not_allowed" // 路径不在允许的目录内（含通过符号链接逃出）
	FileErrNotFound     = "not_found"        // 文件或父目录不存在
	FileErrNotFile      = "not_a_file"       // 目标不是普通文件
	FileErrIsDir        = "is_directory"     // 目标是目录
	FileErrIsSymlink    = "is_symlink"       // 目标是符号链接（拒绝覆盖）
	FileErrExists       = "exists"           // 目标已存在且没有允许覆盖
	FileErrTooLarge     = "too_large"        // 超过大小上限
	FileErrPermission   = "permission_denied"
	FileErrIO           = "io_error"
	FileErrURLBlocked   = "url_blocked"     // 网址不被允许（协议、凭据、指向非公网地址等）
	FileErrDownload     = "download_failed" // 下载失败（连接 / 状态码 / 中断）
	FileErrHashMismatch = "hash_mismatch"   // 下载内容的 sha256 与给定值不一致
	FileErrTimeout      = "timeout"
	FileErrBusy         = "busy"           // 本机同时进行的文件操作过多
	FileErrDenied       = "denied_by_host" // 本机一票否决（--deny-model-exec）
	FileErrBadHandle    = "bad_handle"     // 句柄不存在或已过期
)

var fileErrCodes = map[string]struct{}{
	FileErrBadRequest: {}, FileErrBadPath: {}, FileErrNotAllowed: {}, FileErrNotFound: {}, FileErrNotFile: {},
	FileErrIsDir: {}, FileErrIsSymlink: {}, FileErrExists: {}, FileErrTooLarge: {}, FileErrPermission: {},
	FileErrIO: {}, FileErrURLBlocked: {}, FileErrDownload: {}, FileErrHashMismatch: {}, FileErrTimeout: {},
	FileErrBusy: {}, FileErrDenied: {}, FileErrBadHandle: {},
}

// 文件相关的尺寸限制。
const (
	MaxFileRoots     = 20      // 一次请求最多带多少个允许目录
	MaxFilePathBytes = 1024    // 单个路径（含允许目录）的字节上限
	MaxFileURLBytes  = 2048    // 网址的字节上限
	MaxFileRead      = 1 << 20 // 一次 read 请求最多返回的字节数
	// MaxFileDataFrame 是单个 FrameFileData 的净荷上限（与运行输出帧一致）。
	MaxFileDataFrame = 32 << 10
	MaxFilePull      = 1 << 30   // 单次拉取的大小上限
	MaxFilePush      = 256 << 20 // 单次 URL 推送的大小上限
	MaxFileHash      = 4 << 30   // 单次 hash 处理的大小上限
)

var fileHandlePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidFileHandle 判断句柄是否为 128 位小写十六进制。
func ValidFileHandle(s string) bool { return fileHandlePattern.MatchString(s) }

// FileRootAll 是 FileReq.Roots 里的哨兵条目：令牌被明确授予「整个文件系统」（model.TerminalLimits.AllowWholeFS）。
// Agent 见到它就不再把路径限制在某个目录里（路径仍须是规范的绝对路径；Windows 上是任一盘符）。
// 它不是合法的目录写法（RootProblem 拒绝），所以没有任何人工输入的允许目录能构造出它——只有 Hub 在令牌打开了开关时才会放进去。
// 旧版 Agent 不认识它，会把它当作无法解析的条目丢弃，于是一切路径都被拒绝（失败时更保守，而不是更宽松）。
const FileRootAll = "*"

// FileReq 是 Hub→Agent 的文件请求。Roots 是令牌允许的目录；权威检查在 Agent 上完成。
type FileReq struct {
	T     string   `json:"t"`
	Req   uint32   `json:"req"`
	Op    string   `json:"op"`
	Roots []string `json:"roots,omitempty"`
	Path  string   `json:"path,omitempty"`
	// Max 是 hash / open / push_url 允许处理的最大字节数；0 表示取协议上限。
	Max int64 `json:"max,omitempty"`

	// read / close
	Handle string `json:"h,omitempty"`
	Off    uint64 `json:"off,omitempty"`
	Len    int    `json:"len,omitempty"`

	// push_url
	URL       string `json:"url,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
	SHA256    string `json:"sha256,omitempty"` // 期望的内容摘要（小写十六进制），可选
}

// Problem 检查请求的结构是否合法，返回问题描述；合法返回空串。它只管形状（长度、字符、数量），不做路径授权。
func (r FileReq) Problem() string {
	if !ValidFileOp(r.Op) {
		return "unknown file operation"
	}
	if len(r.Roots) > MaxFileRoots {
		return "too many roots"
	}
	for _, root := range r.Roots {
		if bad := badFileText(root, MaxFilePathBytes); bad != "" {
			return "root: " + bad
		}
	}
	if r.Max < 0 || r.Len < 0 {
		return "negative size"
	}
	switch r.Op {
	case FileOpStat, FileOpHash, FileOpOpen:
		if r.Path == "" {
			return "missing path"
		}
	case FileOpRead:
		if !ValidFileHandle(r.Handle) {
			return "bad handle"
		}
	case FileOpClose:
		if !ValidFileHandle(r.Handle) {
			return "bad handle"
		}
	case FileOpPushURL:
		if r.Path == "" || r.URL == "" {
			return "missing path or url"
		}
		if bad := badFileText(r.URL, MaxFileURLBytes); bad != "" {
			return "url: " + bad
		}
		if r.SHA256 != "" && !ValidSHA256Hex(r.SHA256) {
			return "bad sha256"
		}
	}
	if r.Path != "" {
		if bad := badFileText(r.Path, MaxFilePathBytes); bad != "" {
			return "path: " + bad
		}
	}
	return ""
}

func badFileText(s string, limit int) string {
	switch {
	case len(s) > limit:
		return "too long"
	case !utf8.ValidString(s):
		return "invalid utf-8"
	case strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "control character"
	}
	return ""
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidSHA256Hex 判断是否为 64 位小写十六进制摘要。
func ValidSHA256Hex(s string) bool { return sha256Pattern.MatchString(s) }

// FileStat 是 stat 的结果。
type FileStat struct {
	Exists bool   `json:"exists"`
	Type   string `json:"type,omitempty"` // file | dir | symlink | other
	Size   int64  `json:"size,omitempty"`
	Mode   string `json:"mode,omitempty"`  // 八进制权限位（Windows 为空）
	MTime  int64  `json:"mtime,omitempty"` // Unix 秒
}

// 文件类型取值（FileStat.Type）。
const (
	FileTypeFile    = "file"
	FileTypeDir     = "dir"
	FileTypeSymlink = "symlink"
	FileTypeOther   = "other"
)

// FileRes 是 Agent→Hub 的文件应答。OK=false 时只有 Code / Msg 有意义。
type FileRes struct {
	T    string `json:"t"`
	Req  uint32 `json:"req"`
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`

	Stat   *FileStat `json:"stat,omitempty"`   // stat
	SHA256 string    `json:"sha256,omitempty"` // hash / push_url
	Size   int64     `json:"size,omitempty"`   // hash / open / push_url
	Handle string    `json:"h,omitempty"`      // open

	Status    int    `json:"status,omitempty"`    // push_url：最终响应的 HTTP 状态码
	Source    string `json:"source,omitempty"`    // push_url：最终来源（协议://主机[:端口]，不含路径与查询串）
	Redirects int    `json:"redirects,omitempty"` // push_url：经过的重定向次数
}

// Sanitized 返回净化后的副本：应答来自 Agent，全部字段都不可信。
func (r FileRes) Sanitized() FileRes {
	out := FileRes{T: TFileRes, Req: r.Req, OK: r.OK}
	if !r.OK {
		out.Code = FileErrIO
		if _, known := fileErrCodes[r.Code]; known {
			out.Code = r.Code
		}
		out.Msg = SanitizeText(r.Msg, 200)
		return out
	}
	if r.Stat != nil {
		st := FileStat{Exists: r.Stat.Exists}
		if st.Exists {
			switch r.Stat.Type {
			case FileTypeFile, FileTypeDir, FileTypeSymlink:
				st.Type = r.Stat.Type
			default:
				st.Type = FileTypeOther
			}
			if r.Stat.Size > 0 {
				st.Size = r.Stat.Size
			}
			if len(r.Stat.Mode) <= 8 && strings.Trim(r.Stat.Mode, "01234567") == "" {
				st.Mode = r.Stat.Mode
			}
			if r.Stat.MTime > 0 {
				st.MTime = r.Stat.MTime
			}
		}
		out.Stat = &st
	}
	if ValidSHA256Hex(r.SHA256) {
		out.SHA256 = r.SHA256
	}
	if r.Size > 0 {
		out.Size = r.Size
	}
	if ValidFileHandle(r.Handle) {
		out.Handle = r.Handle
	}
	if r.Status >= 100 && r.Status <= 599 {
		out.Status = r.Status
	}
	out.Source = SanitizeText(r.Source, 300)
	if r.Redirects > 0 && r.Redirects <= 32 {
		out.Redirects = r.Redirects
	}
	return out
}

// ---- 二进制帧 ----

const fileDataHdrLen = 1 + 4 + 1 + 8 + 8

// FileChunk 是 FrameFileData 解码后的内容。
type FileChunk struct {
	Req   uint32
	Flags byte
	Off   uint64 // 本帧第一个字节在文件中的位置
	Total uint64 // 打开时（或读取时）观察到的文件总大小
	Data  []byte
}

// EncodeFileData 编码 read 的应答帧。
func EncodeFileData(c FileChunk) []byte {
	buf := make([]byte, fileDataHdrLen+len(c.Data))
	buf[0] = FrameFileData
	binary.BigEndian.PutUint32(buf[1:5], c.Req)
	buf[5] = c.Flags
	binary.BigEndian.PutUint64(buf[6:14], c.Off)
	binary.BigEndian.PutUint64(buf[14:22], c.Total)
	copy(buf[fileDataHdrLen:], c.Data)
	return buf
}

// DecodeFileData 解码 read 的应答帧；返回的 Data 与 frame 共享底层数组。
func DecodeFileData(frame []byte) (FileChunk, error) {
	if len(frame) < fileDataHdrLen {
		return FileChunk{}, ErrShortFrame
	}
	if frame[0] != FrameFileData {
		return FileChunk{}, ErrWrongFrame
	}
	data := frame[fileDataHdrLen:]
	if len(data) > MaxFileDataFrame {
		return FileChunk{}, ErrPayloadTooBig
	}
	return FileChunk{
		Req:   binary.BigEndian.Uint32(frame[1:5]),
		Flags: frame[5],
		Off:   binary.BigEndian.Uint64(frame[6:14]),
		Total: binary.BigEndian.Uint64(frame[14:22]),
		Data:  data,
	}, nil
}

// RootProblem 检查令牌「允许目录」条目的写法，返回问题描述；空串表示合法。只看字符串，不访问文件系统。
//
// 合法写法：`~` 或 `~/…`（Agent 所在账户的家目录）、`/…` 绝对路径、`C:\…` 或 `C:/…` 盘符路径。
// 文件系统根（`/`、`C:\`）、UNC / 设备路径、相对路径、`~用户名`、含 `..` 片段、含控制字符的写法一律不接受：
// 把根目录当作允许目录等于不设防，`..` 则会让条目的真实含义与写法对不上。
func RootProblem(p string) string {
	switch {
	case p == "":
		return "不能为空"
	case len(p) > MaxFilePathBytes:
		return "过长"
	case !utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl):
		return "含有非法字符"
	}
	isSep := func(r rune) bool { return r == '/' || r == '\\' }
	segs := strings.FieldsFunc(p, isSep)
	meaningful := 0
	for _, seg := range segs {
		if seg == ".." {
			return "路径里不能出现 .. 片段"
		}
		if seg != "." {
			meaningful++
		}
	}
	switch {
	case p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		return ""
	case strings.HasPrefix(p, "~"):
		return "只支持 ~ 与 ~/…，不支持 ~用户名"
	case strings.HasPrefix(p, "//") || strings.HasPrefix(p, `\\`):
		return "不支持 UNC 与设备路径"
	case strings.HasPrefix(p, "/"):
		if meaningful == 0 {
			return "不能把文件系统根目录作为允许目录；确实需要访问整个文件系统时，请使用「允许整个文件系统」开关"
		}
		return ""
	case len(p) >= 3 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':' && isSep(rune(p[2])):
		if strings.Contains(p[2:], ":") {
			return "路径里不能出现冒号"
		}
		for _, seg := range segs[1:] { // segs[0] 是盘符 "C:"
			if seg != "." && (strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ")) {
				return "路径片段不能以点或空格结尾"
			}
		}
		if meaningful <= 1 { // 只有盘符本身
			return "不能把盘符根目录作为允许目录；确实需要访问整个文件系统时，请使用「允许整个文件系统」开关"
		}
		return ""
	}
	return `必须是绝对路径：/…、~/… 或 C:\…`
}
