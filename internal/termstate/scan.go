package termstate

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 托管终端的屏幕扫描器（#113 / M3.1，设计稿 04 §2.6）。
//
// 它只看 PTY 输出的字节流，不是终端模拟器：去掉转义序列后取末尾一小段文字，用几条正则认出 cursor-agent TUI 的
// 「状态行」（⠞ Thinking 4 tokens）、「等待审批」和空闲页脚的上下文占用百分比，再维护一个按秒分桶的字节速率。
// 样本来自实验台（`.cursor/design/async-collab-lab/README.md`，CLI 2026.10.12-d47ca18，交互 TUI）。
//
// 设计要点：
//   - 纯函数式的数据结构：时间由调用方传入，没有 goroutine、没有 PTY、没有网络，所以能拿屏幕帧做回归夹具；
//   - 版本闸门不在这里：扫描器永远照常扫描，是否「相信」它由使用方按 cursor 版本决定（见 TUIVersionVerified）；
//   - 认不出就说认不出（Recognized=false），绝不猜——未知画面不产生任何告警。

const (
	// scanTailBytes 是保留的原始字节尾部长度。状态行与审批提示都在屏幕底部，16 KB 足够覆盖最近几次重绘，
	// 又不至于让每次去转义的成本变得可感知（设计稿写的是「约 4 KB」文字，这里留余量给转义序列本身）。
	scanTailBytes = 16 << 10
	// scanRateBuckets 是字节速率的滑窗秒数。
	scanRateBuckets = 5
)

// ScreenInfo 是一次扫描的结果。零值表示「没有认出任何东西」。
type ScreenInfo struct {
	// Recognized 为 true 说明至少认出了状态行、审批提示或页脚之一。
	Recognized bool `json:"recognized"`
	// Word 是状态行的动词（Thinking / Working / Globbing / Running ...），没有状态行时为空。
	Word string `json:"word,omitempty"`
	// Tokens 是状态行里的实时 token 数；-1 表示没读到（状态行不带计数，或写法没见过）。
	Tokens int `json:"tokens"`
	// CtxPct 是页脚里的上下文占用百分比（0~100，保留一位小数）；-1 表示没读到。
	CtxPct float64 `json:"ctx_pct"`
	// Approval 为 true 表示屏幕正停在「Waiting for approval...」上。
	Approval bool `json:"approval,omitempty"`
	// Cancelled 为 true 表示最近一次画面是「Cancelled • 15s」（被人中断）。
	Cancelled bool `json:"cancelled,omitempty"`
	// Animating 为 true 表示最近 2 秒还有输出字节（旋转动画仍在动）。
	Animating bool `json:"animating,omitempty"`
	// BytesPerSec 是最近 scanRateBuckets 秒内的平均字节速率。
	BytesPerSec float64 `json:"bytes_per_sec"`
	// QuietFor 是距离最后一次输出过去了多久；从未有过输出为 -1。
	QuietFor time.Duration `json:"quiet_for"`
}

// Scanner 累积输出并按需给出 ScreenInfo。非并发安全：调用方自己加锁（Feed 与 Info 通常在同一把会话锁下）。
type Scanner struct {
	tail    []byte
	buckets [scanRateBuckets]uint64
	// bucketSec 是 buckets 最新一格对应的 Unix 秒；lastOut 是最后一次输出的时间。
	bucketSec int64
	head      int
	lastOut   time.Time
}

// NewScanner 返回一个空的扫描器。
func NewScanner() *Scanner { return &Scanner{} }

// Feed 登记一段输出。p 不会被保留（内部拷贝尾部）。
func (s *Scanner) Feed(now time.Time, p []byte) {
	if len(p) == 0 {
		return
	}
	s.advance(now)
	s.buckets[s.head] += uint64(len(p))
	s.lastOut = now
	if len(p) >= scanTailBytes {
		s.tail = append(s.tail[:0], p[len(p)-scanTailBytes:]...)
		return
	}
	s.tail = append(s.tail, p...)
	if over := len(s.tail) - scanTailBytes; over > 0 {
		s.tail = append(s.tail[:0], s.tail[over:]...)
	}
}

// advance 把滑窗推进到 now 所在的秒；跨过的空秒清零。时钟回拨时只把游标对齐，不动已有计数。
func (s *Scanner) advance(now time.Time) {
	sec := now.Unix()
	if s.bucketSec == 0 {
		s.bucketSec = sec
		return
	}
	if sec <= s.bucketSec {
		return
	}
	gap := sec - s.bucketSec
	if gap > scanRateBuckets {
		gap = scanRateBuckets
	}
	for i := int64(0); i < gap; i++ {
		s.head = (s.head + 1) % scanRateBuckets
		s.buckets[s.head] = 0
	}
	s.bucketSec = sec
}

// Info 扫描当前尾部并返回结果。
func (s *Scanner) Info(now time.Time) ScreenInfo {
	s.advance(now)
	var total uint64
	for _, b := range s.buckets {
		total += b
	}
	info := ScreenInfo{
		Tokens:      -1,
		CtxPct:      -1,
		BytesPerSec: float64(total) / scanRateBuckets,
		QuietFor:    -1,
	}
	if !s.lastOut.IsZero() {
		q := now.Sub(s.lastOut)
		if q < 0 {
			q = 0
		}
		info.QuietFor = q
		info.Animating = q <= 2*time.Second
	}
	if len(s.tail) == 0 {
		return info
	}
	scanText(StripANSI(s.tail), &info)
	// 没有终端模拟器，认不出「状态行已经被擦掉」：空闲或等审批时尾部仍留着最后一条状态行。
	// 旋转动画一直在出字节，所以只有最近 2 秒还有输出时才把动词与 token 当作「现在」的值。
	if !info.Animating {
		info.Word = ""
		info.Tokens = -1
	}
	return info
}

var (
	// 状态行：盲文旋转字符 + 动词 + 可选的「<N>[k] tokens」。动词只取首字母大写的英文单词，
	// 这样「Globbing / Reading / Editing / Exploring / Running」以及将来新增的工具动词都能认出来。
	reStatusLine = regexp.MustCompile(`[\x{2800}-\x{28FF}]{1,3}\s+([A-Z][a-z]+)(?:\s+([0-9][0-9.,]*)(k|K|m|M)?\s+tokens)?`)
	// 页脚：「Claude Sonnet 5.5 300K Max · 4.4%」，取 `· <百分比>%`。
	reFooterPct = regexp.MustCompile(`·\s*([0-9]+(?:\.[0-9]+)?)\s*%`)
	reApproval  = "Waiting for approval"
	reCancelled = regexp.MustCompile(`Cancelled\s*•\s*[0-9]+\s*[smh]`)
)

// scanText 在去过转义的文字上认状态行 / 审批 / 页脚，结果写进 info。
func scanText(text string, info *ScreenInfo) {
	// 状态行取「最后一条」：画面是不断重绘的，后出现的才是当前的。
	if all := reStatusLine.FindAllStringSubmatchIndex(text, -1); len(all) > 0 {
		m := all[len(all)-1]
		info.Word = text[m[2]:m[3]]
		info.Recognized = true
		if m[4] >= 0 {
			info.Tokens = parseTokenCount(text[m[4]:m[5]], textOrEmpty(text, m[6], m[7]))
		}
	}
	statusEnd := -1
	if all := reStatusLine.FindAllStringIndex(text, -1); len(all) > 0 {
		statusEnd = all[len(all)-1][1]
	}
	if i := strings.LastIndex(text, reApproval); i >= 0 {
		// 审批提示之后又出现了状态行，说明已经批过了（或被跳过了），画面继续走。
		if i > statusEnd {
			info.Approval = true
		}
		info.Recognized = true
	}
	if all := reCancelled.FindAllStringIndex(text, -1); len(all) > 0 {
		last := all[len(all)-1]
		if last[0] > statusEnd {
			info.Cancelled = true
			// 被中断之后，之前的审批画面不再算数。
			if i := strings.LastIndex(text, reApproval); i >= 0 && last[0] > i {
				info.Approval = false
			}
		}
		info.Recognized = true
	}
	// 页脚从后往前找第一个合法的百分比（0~100），不合法的写法跳过而不是整体放弃。
	pcts := reFooterPct.FindAllStringSubmatch(text, -1)
	for i := len(pcts) - 1; i >= 0; i-- {
		if v, err := strconv.ParseFloat(pcts[i][1], 64); err == nil && v >= 0 && v <= 100 {
			info.CtxPct = v
			info.Recognized = true
			break
		}
	}
}

func textOrEmpty(s string, a, b int) string {
	if a < 0 || b < 0 {
		return ""
	}
	return s[a:b]
}

// parseTokenCount 把「205」「1.2k」「1,234」换成整数；写法不认识返回 -1（降级为未知，不猜）。
func parseTokenCount(num, unit string) int {
	n := strings.ReplaceAll(num, ",", "")
	f, err := strconv.ParseFloat(n, 64)
	if err != nil || f < 0 {
		return -1
	}
	switch strings.ToLower(unit) {
	case "k":
		f *= 1000
	case "m":
		f *= 1000000
	}
	if f > 1e9 {
		return -1
	}
	return int(f + 0.5)
}

// StripANSI 去掉 CSI / OSC / 其它两字节转义序列和控制字符（保留换行与制表），返回 UTF-8 文字。
// 输入开头可能是被截断的转义序列的残片，这里宽容处理：认不出的 ESC 直接丢掉它和后面一个字节。
func StripANSI(b []byte) string {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == 0x1b:
			if i+1 >= len(b) {
				return string(out)
			}
			switch b[i+1] {
			case '[': // CSI：参数字节 0x30-0x3f、中间字节 0x20-0x2f、终止字节 0x40-0x7e
				j := i + 2
				for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
					j++
				}
				i = j
			case ']': // OSC：到 BEL 或 ESC \ 结束
				j := i + 2
				for j < len(b) && b[j] != 0x07 && !(b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\') {
					j++
				}
				if j < len(b) && b[j] == 0x1b {
					j++
				}
				i = j
			default: // ESC 7 / ESC = / ESC ( B 之类：丢掉 ESC 和下一个字节
				i++
				if b[i] == '(' || b[i] == ')' || b[i] == '#' {
					i++
				}
			}
		case c == '\r':
			out = append(out, '\n')
		case c < 0x20 && c != '\n' && c != '\t':
			// 其它控制字符丢弃
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// TUIVersionVerified 判断一个 cursor 版本号是否在「已用实验验证过屏幕写法」的清单里。
// 未知版本返回 false：使用方应把该会话标为 tui:unrecognized 并降级为纯 hook，不产生任何告警（设计稿 04 §2.6）。
func TUIVersionVerified(cursorVersion string) bool {
	v := strings.TrimSpace(cursorVersion)
	if v == "" {
		return false
	}
	for _, ok := range verifiedTUIVersions {
		if v == ok {
			return true
		}
	}
	return false
}

// verifiedTUIVersions 是实验验证过的 CLI 版本（交互 TUI）。新版本经过抓屏回归后追加。
var verifiedTUIVersions = []string{
	"2026.10.12-d47ca18",
}

// Wire 把当前扫描结果换成线上格式；认不出任何东西（没有状态行 / 审批 / 页脚）时返回 nil。
func (s *Scanner) Wire(now time.Time) *termproto.Screen {
	info := s.Info(now)
	if !info.Recognized {
		return nil
	}
	w := &termproto.Screen{
		Word: info.Word, Tokens: info.Tokens, CtxPct: info.CtxPct,
		Approval: info.Approval, Cancelled: info.Cancelled, Animating: info.Animating,
		BPS: info.BytesPerSec, Quiet: -1,
	}
	if info.QuietFor >= 0 {
		w.Quiet = int(info.QuietFor / time.Second)
	}
	return w
}
