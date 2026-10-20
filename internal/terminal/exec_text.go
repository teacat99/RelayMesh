package terminal

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// ---------- 模型运行的文本处理 ----------
//
// Hub 只保存两类来自命令的文本：命令本身（脱敏副本）与结束时的预览（去控制序列 + 脱敏）。
// 脱敏是尽力而为的兜底，不是安全边界：真正的原则是「完整输出不进 Hub 数据库」。

const redacted = "[REDACTED]"

var (
	reAuthHeader = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|basic|token)\s+)[^\s'"]+`)
	reKeyValue   = regexp.MustCompile(`(?i)((?:pass(?:word|wd)?|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)s?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s'"&;|]+)`)
	reCLISecret  = regexp.MustCompile(`(?i)(--(?:password|passwd|pass|token|secret|api-key|apikey|access-key|auth-token|client-secret)(?:=|\s+))("[^"]*"|'[^']*'|[^\s'"]+)`)
	reURLCred    = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://[^/\s:@]+:)[^@\s/]+(@)`)
	rePrivateKey = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)
	reKnownToken = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_\-]{16,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9\-]{10,}|rm-[0-9a-f]{32,}|glpat-[A-Za-z0-9_\-]{16,}|eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{5,})\b`)
)

// redactSecrets 把常见形式的密钥替换为占位符：Authorization 头、key=value 形式的口令 / 令牌、命令行的 --password 类参数、
// URL 里的口令、PEM 私钥块，以及几类有固定前缀的令牌。
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = rePrivateKey.ReplaceAllString(s, redacted)
	s = reAuthHeader.ReplaceAllString(s, "${1}"+redacted)
	s = reCLISecret.ReplaceAllString(s, "${1}"+redacted)
	s = reKeyValue.ReplaceAllString(s, "${1}"+redacted)
	s = reURLCred.ReplaceAllString(s, "${1}"+redacted+"${2}")
	s = reKnownToken.ReplaceAllString(s, redacted)
	return s
}

var (
	reCSI     = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	reOSC     = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)
	reEscSeq  = regexp.MustCompile(`\x1b[()*+][0-9A-Za-z]|\x1b[@-Z\\-_]|\x1b[=>]`)
	reC1CSI   = regexp.MustCompile("\u009b[0-?]*[ -/]*[@-~]")
	reCRLF    = regexp.MustCompile(`\r\n?`)
	reManyNLs = regexp.MustCompile(`\n{4,}`)
)

// cleanOutput 把命令输出规整为可展示的文本：非法 UTF-8 替换、去掉 ANSI 转义序列、统一换行，
// 并去除其余控制字符与格式字符（含双向控制符，防止用不可见字符伪装内容）。保留换行与制表符。
func cleanOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	s = reOSC.ReplaceAllString(s, "")
	s = reCSI.ReplaceAllString(s, "")
	s = reC1CSI.ReplaceAllString(s, "")
	s = reEscSeq.ReplaceAllString(s, "")
	s = reCRLF.ReplaceAllString(s, "\n")
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			sb.WriteRune(r)
		case r == utf8.RuneError:
			sb.WriteRune(r)
		case unicode.IsGraphic(r):
			sb.WriteRune(r)
		}
	}
	return reManyNLs.ReplaceAllString(sb.String(), "\n\n\n")
}

// previewText 生成可落库的预览：先去掉不完整的首尾字符，再清洗并脱敏。
func previewText(b []byte, head bool) string {
	if head {
		b = trimTrailingPartialRune(b)
	} else {
		b = trimLeadingContinuation(b)
	}
	return redactSecrets(cleanOutput(b))
}

// trimTrailingPartialRune 去掉末尾被截断的多字节字符。
func trimTrailingPartialRune(b []byte) []byte {
	if len(b) == 0 || utf8.Valid(b) {
		return b
	}
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		c := b[len(b)-i]
		if c&0xC0 == 0x80 {
			continue // 续字节，继续往前找起始字节
		}
		if c >= 0xC0 && !utf8.FullRune(b[len(b)-i:]) {
			return b[:len(b)-i]
		}
		break
	}
	return b
}

// trimLeadingContinuation 去掉开头孤立的续字节（读取起点落在多字节字符中间时出现）。
func trimLeadingContinuation(b []byte) []byte {
	i := 0
	for i < len(b) && i < utf8.UTFMax && b[i]&0xC0 == 0x80 {
		i++
	}
	return b[i:]
}

// shellNoCommand 是以这些关键字开头的段：循环头（for x in ...）、分支 / 循环的结束等，本身不含要执行的命令。
var shellNoCommand = map[string]bool{
	"for": true, "select": true, "case": true, "function": true, "in": true,
	"done": true, "fi": true, "esac": true, "{": true, "}": true,
}

// shellLeadWord 是后面紧跟真正命令的关键字（do echo ...、while read ...、time make），提取命令名时跳过它们。
var shellLeadWord = map[string]bool{
	"do": true, "then": true, "else": true, "elif": true, "if": true,
	"while": true, "until": true, "time": true, "!": true,
}

// commandNames 从命令文本里提取用到的命令名（仅用于列表展示）：按 ; & | 与换行拆分（引号内不拆），
// 取每段的第一个词（跳过 VAR=value 前缀），去掉路径，最多 8 个、去重。
func commandNames(cmd string) []string {
	var segs []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			segs = append(segs, t)
		}
		cur.Reset()
	}
	for _, r := range cmd {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == ';' || r == '&' || r == '|' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()

	seen := map[string]struct{}{}
	var names []string
	for _, seg := range segs {
		if len(names) >= 8 {
			break
		}
		for _, w := range strings.Fields(seg) {
			if shellNoCommand[w] {
				break // 循环头、分支结束等：这一段里没有命令
			}
			if shellLeadWord[w] {
				continue // do / then / while 之类：真正的命令在后面
			}
			if eq := strings.IndexByte(w, '='); eq > 0 && !strings.ContainsAny(w[:eq], "/\\") {
				continue // 环境变量前缀
			}
			w = strings.Trim(w, `'"()`)
			if i := strings.LastIndexAny(w, `/\`); i >= 0 {
				w = w[i+1:]
			}
			var ok strings.Builder
			for _, r := range w {
				if r == '.' || r == '_' || r == '-' || r == '+' || unicode.IsLetter(r) || unicode.IsDigit(r) {
					ok.WriteRune(r)
				}
			}
			name := termproto.SanitizeText(ok.String(), 24)
			if name != "" {
				if _, dup := seen[name]; !dup {
					seen[name] = struct{}{}
					names = append(names, name)
				}
			}
			break
		}
	}
	return names
}

// threadKeyFor 计算线程键（短哈希，可直接用在 URL 里）：
//   - 混合令牌且给了 workflow_id：按「工作流 × 目标主机」归并，让终端活动落进同一个会话的时间线；
//   - 其余（含 ExecMesh 档与混合令牌缺省时的「未归属」）：按「凭据 × 目标主机 × 线程标签」归并。
func threadKeyFor(c termexec.Caller, machine, thread, workflow string) string {
	var raw string
	if c.Hybrid() && workflow != "" {
		raw = "w\x00" + workflow + "\x00" + machine
	} else {
		raw = "c\x00" + strconv.FormatUint(uint64(c.CredentialID), 10) + "\x00" + machine + "\x00" + thread
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:24]
}

func commandDigest(cmd string) string {
	sum := sha256.Sum256([]byte(cmd))
	return hex.EncodeToString(sum[:])
}

// label 净化模型自报的单行标签（描述、线程名、工作流、目录），并限长。
func label(s string, maxRunes int) string {
	return termproto.SanitizeText(s, maxRunes)
}

// trimIncompleteTail 返回不以半个字符结尾的文本前缀，以及被去掉的字节数；用于分页读取时让下一页从字符边界开始。
func trimIncompleteTail(b []byte) (kept []byte, dropped int) {
	k := trimTrailingPartialRune(b)
	return k, len(b) - len(k)
}
