package termstate

import (
	"bytes"
	"strconv"
)

// ModeTracker 解析终端输出流，跟踪那些「重新连接后必须恢复」的 DEC 私有模式。
//
// 为什么需要它：浏览器重新附着一个正在运行 vim / tmux / htop 的会话时，新建的 xterm 并不知道
// 进程此前已经切到备用屏、开启了鼠标上报或括号粘贴。若只回放字节，画面与输入行为都会错乱。
// Agent 在输出流上维护这些模式的当前值，附着时先发一段「模式恢复序列」再回放数据。
//
// 解析器是一个极小的状态机，只识别 CSI ? … h/l、ESC = / ESC >、RIS 与 DECSTR，并正确跳过
// OSC / DCS 等字符串序列（窗口标题里的文字不会被误当成模式切换）。它能跨数据块边界工作。
type ModeTracker struct {
	st       parseState
	params   []byte
	private  bool
	bad      bool // 当前 CSI 已被判定为无效（参数过长等），只等终止字节
	inter    byte // 当前 CSI 的中间字节（0 表示没有）；DECSTR 是 '!'
	modes    map[int]bool
	keypad   bool // DECKPAM（应用小键盘）
	alt      bool
	altEnter uint64
}

type parseState uint8

const (
	stGround parseState = iota
	stEsc
	stEscInter // ESC 后的中间字节（如 ESC ( B 字符集指定）
	stCSI
	stString    // OSC / DCS / APC / PM / SOS，直到 BEL 或 ST
	stStringEsc // 字符串内遇到 ESC，等待 '\'
)

// 跟踪的模式及其默认值。未列出的模式不关心。
var trackedModes = []struct {
	n   int
	def bool
}{
	{1, false},    // DECCKM 应用光标键
	{7, true},     // DECAWM 自动换行
	{25, true},    // DECTCEM 光标可见
	{1000, false}, // 鼠标：按下 / 释放
	{1002, false}, // 鼠标：拖动
	{1003, false}, // 鼠标：全部移动
	{1004, false}, // 焦点事件
	{1005, false}, // 鼠标编码：UTF-8
	{1006, false}, // 鼠标编码：SGR
	{1015, false}, // 鼠标编码：urxvt
	{2004, false}, // 括号粘贴
}

const maxModeParamBytes = 64

// NewModeTracker 创建处于初始状态的跟踪器。
func NewModeTracker() *ModeTracker {
	m := &ModeTracker{}
	m.resetModes()
	return m
}

func (m *ModeTracker) resetModes() {
	m.modes = make(map[int]bool, len(trackedModes))
	for _, t := range trackedModes {
		m.modes[t.n] = t.def
	}
	m.keypad = false
}

// Alt 返回当前是否处于备用屏，以及进入备用屏那条序列结束之后的偏移。
func (m *ModeTracker) Alt() (bool, uint64) { return m.alt, m.altEnter }

// Feed 消费一段输出。start 是 data[0] 的累计偏移，用来记录「进入备用屏」的位置。
func (m *ModeTracker) Feed(start uint64, data []byte) {
	for i, c := range data {
		m.step(c, start+uint64(i)+1)
	}
}

// step 处理一个字节；after 是该字节之后的偏移。
func (m *ModeTracker) step(c byte, after uint64) {
	switch m.st {
	case stGround:
		if c == 0x1b {
			m.st = stEsc
		}

	case stEsc:
		switch {
		case c == '[':
			m.beginCSI()
		case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
			m.st = stString
		case c >= 0x20 && c <= 0x2f:
			m.st = stEscInter
		case c == '=':
			m.keypad = true
			m.st = stGround
		case c == '>':
			m.keypad = false
			m.st = stGround
		case c == 'c': // RIS 完全重置
			m.resetModes()
			m.alt = false
			m.st = stGround
		case c == 0x1b:
			// 连续 ESC：仍停留在 Esc 状态
		case c == 0x18 || c == 0x1a:
			m.st = stGround
		default:
			m.st = stGround
		}

	case stEscInter:
		switch {
		case c >= 0x20 && c <= 0x2f:
		case c == 0x1b:
			m.st = stEsc
		default:
			m.st = stGround
		}

	case stCSI:
		switch {
		case c == 0x1b:
			m.st = stEsc
		case c == 0x18 || c == 0x1a:
			m.st = stGround
		case c >= 0x40 && c <= 0x7e:
			m.finishCSI(c, after)
			m.st = stGround
		case c == '?' && len(m.params) == 0 && !m.private:
			m.private = true
		case c >= 0x30 && c <= 0x3f:
			if len(m.params) >= maxModeParamBytes {
				m.bad = true
			} else {
				m.params = append(m.params, c)
			}
		case c >= 0x20 && c <= 0x2f:
			m.inter = c
		default:
			// 其他 C0 控制字符在 CSI 中途出现时按终端惯例被执行但不打断序列，这里忽略即可。
		}

	case stString:
		switch c {
		case 0x07: // BEL 结束 OSC
			m.st = stGround
		case 0x1b:
			m.st = stStringEsc
		case 0x18, 0x1a:
			m.st = stGround
		}

	case stStringEsc:
		switch c {
		case '\\': // ST 结束字符串
			m.st = stGround
		case 0x1b:
			// 仍在等待
		default:
			// 不是 ST：ESC 打断了字符串并开始了新序列，按 ESC 之后的字节重新分派。
			m.st = stEsc
			m.step(c, after)
		}
	}
}

func (m *ModeTracker) beginCSI() {
	m.st = stCSI
	m.params = m.params[:0]
	m.private, m.bad, m.inter = false, false, 0
}

func (m *ModeTracker) finishCSI(final byte, after uint64) {
	switch {
	case final == 'p' && m.inter == '!' && !m.private:
		// DECSTR 软复位：模式回到默认值（不退出备用屏）。
		m.resetModes()
	case (final == 'h' || final == 'l') && m.private && m.inter == 0 && !m.bad:
		set := final == 'h'
		for _, part := range bytes.Split(m.params, []byte{';'}) {
			n, err := strconv.Atoi(string(part))
			if err != nil {
				continue
			}
			m.apply(n, set, after)
		}
	}
}

func (m *ModeTracker) apply(n int, set bool, after uint64) {
	switch n {
	case 47, 1047, 1049:
		if set {
			if !m.alt {
				m.altEnter = after
			}
			m.alt = true
		} else {
			m.alt = false
		}
		return
	}
	if _, ok := m.modes[n]; ok {
		m.modes[n] = set
	}
}

// Prefix 生成「模式恢复序列」：把新终端带到与当前输出流一致的模式状态。
// includeAlt 为 true 时先切换到备用屏（调用方随后应当从进入备用屏之后的偏移开始回放）。
// 只输出与默认值不同的模式，全部为默认时返回 nil。
func (m *ModeTracker) Prefix(includeAlt bool) []byte {
	var b bytes.Buffer
	if includeAlt {
		b.WriteString("\x1b[?1049h")
	}
	for _, t := range trackedModes {
		if cur := m.modes[t.n]; cur != t.def {
			b.WriteString("\x1b[?")
			b.WriteString(strconv.Itoa(t.n))
			if cur {
				b.WriteByte('h')
			} else {
				b.WriteByte('l')
			}
		}
	}
	if m.keypad {
		b.WriteString("\x1b=")
	}
	if b.Len() == 0 {
		return nil
	}
	return b.Bytes()
}
