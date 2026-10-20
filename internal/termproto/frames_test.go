package termproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestOutputFrameGoldenBytes(t *testing.T) {
	// [0x01][ch=0x00000102][offset=0x0000000000000304][payload "hi"]
	got := EncodeOutput(0x102, 0x304, []byte("hi"))
	want := []byte{0x01, 0, 0, 1, 2, 0, 0, 0, 0, 0, 0, 3, 4, 'h', 'i'}
	if !bytes.Equal(got, want) {
		t.Fatalf("输出帧字节不符:\n got %v\nwant %v", got, want)
	}
	ch, off, payload, err := DecodeOutput(got)
	if err != nil || ch != 0x102 || off != 0x304 || string(payload) != "hi" {
		t.Fatalf("解码失败: %d %d %q %v", ch, off, payload, err)
	}
}

func TestInputAndAckFrameGoldenBytes(t *testing.T) {
	in := EncodeInput(7, []byte{0x1b, '['})
	if !bytes.Equal(in, []byte{0x02, 0, 0, 0, 7, 0x1b, '['}) {
		t.Fatalf("输入帧字节不符: %v", in)
	}
	ch, payload, err := DecodeInput(in)
	if err != nil || ch != 7 || !bytes.Equal(payload, []byte{0x1b, '['}) {
		t.Fatalf("输入帧解码失败: %v", err)
	}

	ack := EncodeAck(9, 1<<40)
	if !bytes.Equal(ack, []byte{0x03, 0, 0, 0, 9, 0, 0, 1, 0, 0, 0, 0, 0}) {
		t.Fatalf("确认帧字节不符: %v", ack)
	}
	ch, off, err := DecodeAck(ack)
	if err != nil || ch != 9 || off != 1<<40 {
		t.Fatalf("确认帧解码失败: %v", err)
	}
}

func TestBrowserFrames(t *testing.T) {
	out := EncodeBrowserOutput(0x0102030405060708, []byte("x"))
	if !bytes.Equal(out, []byte{0x01, 1, 2, 3, 4, 5, 6, 7, 8, 'x'}) {
		t.Fatalf("浏览器输出帧字节不符: %v", out)
	}
	off, payload, err := DecodeBrowserOutput(out)
	if err != nil || off != 0x0102030405060708 || string(payload) != "x" {
		t.Fatalf("解码失败: %v", err)
	}
	in := EncodeBrowserInput([]byte("ls\r"))
	got, err := DecodeBrowserInput(in)
	if err != nil || string(got) != "ls\r" {
		t.Fatalf("输入帧往返失败: %q %v", got, err)
	}
}

func TestBinaryDecodersRejectMalformed(t *testing.T) {
	if _, _, _, err := DecodeOutput(nil); !errors.Is(err, ErrShortFrame) {
		t.Fatal("空帧应为 ErrShortFrame")
	}
	if _, _, _, err := DecodeOutput(make([]byte, 12)); !errors.Is(err, ErrShortFrame) {
		t.Fatal("头部不全应为 ErrShortFrame")
	}
	wrong := EncodeInput(1, nil)
	if _, _, _, err := DecodeOutput(wrong); !errors.Is(err, ErrShortFrame) && !errors.Is(err, ErrWrongFrame) {
		t.Fatal("类型不符应失败")
	}
	bad := EncodeOutput(1, 0, nil)
	bad[0] = 0x7f
	if _, _, _, err := DecodeOutput(bad); !errors.Is(err, ErrWrongFrame) {
		t.Fatal("类型字节不符应为 ErrWrongFrame")
	}
	if _, _, _, err := DecodeOutput(EncodeOutput(1, 0, make([]byte, MaxDataPayload+1))); !errors.Is(err, ErrPayloadTooBig) {
		t.Fatal("净荷超限应为 ErrPayloadTooBig")
	}
	if _, _, err := DecodeInput(EncodeInput(1, make([]byte, MaxInputFrame+1))); !errors.Is(err, ErrPayloadTooBig) {
		t.Fatal("输入超限应为 ErrPayloadTooBig")
	}
	if _, err := DecodeBrowserInput(EncodeBrowserInput(make([]byte, MaxInputFrame+1))); !errors.Is(err, ErrPayloadTooBig) {
		t.Fatal("浏览器输入超限应为 ErrPayloadTooBig")
	}
	if _, _, err := DecodeAck(append(EncodeAck(1, 1), 0)); err == nil {
		t.Fatal("确认帧长度必须精确")
	}
	if _, err := FrameType(nil); err == nil {
		t.Fatal("空帧无类型")
	}
}

func TestParseType(t *testing.T) {
	if typ, err := ParseType([]byte(`{"t":"hello","v":1}`)); err != nil || typ != THello {
		t.Fatalf("got %q %v", typ, err)
	}
	for _, bad := range []string{``, `{}`, `{"t":""}`, `[]`, `not json`, `{"t":1}`} {
		if _, err := ParseType([]byte(bad)); err == nil {
			t.Fatalf("ParseType(%q) 应失败", bad)
		}
	}
}

func goldenHello(t *testing.T) Hello {
	t.Helper()
	v := goldenVectors[0]
	priv := ed25519.NewKeyFromSeed(mustHex(t, v.seed))
	return Hello{
		T:    THello,
		V:    ProtocolVersion,
		Pub:  EncodeKey(priv.Public().(ed25519.PublicKey)),
		Sig:  EncodeKey(SignChallenge(priv, goldenNonce())),
		Meta: Meta{Host: "box-1", OS: "linux", Arch: "amd64", Ver: "1.7.0", Priv: "root", Kind: "persistent"},
		Sessions: []SessionInfo{
			{SID: strings.Repeat("a", 32), Name: "main", Rows: 24, Cols: 80, End: 100, State: SessionRunning},
		},
	}
}

func TestHelloDecode(t *testing.T) {
	h := goldenHello(t)
	pub, sig, err := h.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if MachineCode(pub) != goldenVectors[0].code {
		t.Fatal("机器码应与金标准一致")
	}
	if !VerifyChallenge(pub, goldenNonce(), sig) {
		t.Fatal("hello 中的签名应可验证")
	}

	mutate := map[string]func(*Hello){
		"版本不符":    func(h *Hello) { h.V = 2 },
		"版本为 0":   func(h *Hello) { h.V = 0 },
		"公钥太短":    func(h *Hello) { h.Pub = h.Pub[:20] },
		"公钥非法字符":  func(h *Hello) { h.Pub = strings.Repeat("!", 43) },
		"公钥为空":    func(h *Hello) { h.Pub = "" },
		"签名太长":    func(h *Hello) { h.Sig += "AAAA" },
		"签名非法字符":  func(h *Hello) { h.Sig = "***" },
		"公钥带填充符号": func(h *Hello) { h.Pub += "=" },
	}
	for name, m := range mutate {
		bad := goldenHello(t)
		m(&bad)
		if _, _, err := bad.Decode(); err == nil {
			t.Fatalf("%s: Decode 应失败", name)
		}
	}
}

func TestHelloJSONRoundTrip(t *testing.T) {
	h := goldenHello(t)
	data, err := Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if typ, err := ParseType(data); err != nil || typ != THello {
		t.Fatalf("类型嗅探失败: %q %v", typ, err)
	}
	var back Hello
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Pub != h.Pub || back.Sig != h.Sig || back.Meta != h.Meta || len(back.Sessions) != 1 || back.Sessions[0].SID != h.Sessions[0].SID {
		t.Fatalf("往返不一致: %+v", back)
	}
}

func TestMetaSanitized(t *testing.T) {
	m := Meta{
		Host: "  evil\x1b[31m\nhost\u202eevil  ",
		OS:   strings.Repeat("长", 500),
		Arch: "amd64\x00",
		Ver:  "1.7.0\r\n<script>",
		Priv: "SUPERUSER",
		Kind: "whatever",
	}
	got := m.Sanitized()
	if got.Host != "evil[31mhostevil" {
		t.Fatalf("Host 净化不符: %q", got.Host)
	}
	if n := len([]rune(got.OS)); n != MaxOSRunes {
		t.Fatalf("OS 应截断到 %d 个字符，实际 %d", MaxOSRunes, n)
	}
	if got.Arch != "amd64" {
		t.Fatalf("Arch: %q", got.Arch)
	}
	if got.Ver != "1.7.0<script>" {
		t.Fatalf("Ver 只做字符净化，HTML 由界面按纯文本渲染: %q", got.Ver)
	}
	if got.Priv != PrivUser || got.Kind != KindPersistent {
		t.Fatalf("未知枚举应回落到安全默认值: %+v", got)
	}

	ok := Meta{Priv: "root", Kind: "temporary"}.Sanitized()
	if ok.Priv != PrivRoot || ok.Kind != KindTemporary {
		t.Fatalf("合法枚举应保留: %+v", ok)
	}
	if (Meta{Priv: "admin"}).Sanitized().Priv != PrivAdmin {
		t.Fatal("admin 应保留")
	}
	if !(Meta{Files: true}).Sanitized().Files || (Meta{}).Sanitized().Files {
		t.Fatal("Files 能力位应原样保留")
	}
	if !(Meta{Uninstall: true}).Sanitized().Uninstall || (Meta{}).Sanitized().Uninstall {
		t.Fatal("Uninstall 能力位应原样保留，且缺省为 false（旧版本 Agent 没有这一项）")
	}
}

func TestSessionInfoSanitized(t *testing.T) {
	good := SessionInfo{SID: strings.Repeat("0f", 16), Name: "a\nb", Rows: 0, Cols: 5000, State: "weird", Base: 50, End: 10, Attached: -3}
	got, ok := good.Sanitized()
	if !ok {
		t.Fatal("合法 SID 应保留")
	}
	if got.Name != "ab" || got.Rows != 24 || got.Cols != 1000 || got.State != SessionExited || got.Base != 10 || got.Attached != 0 {
		t.Fatalf("净化结果不符: %+v", got)
	}
	for _, sid := range []string{"", "xyz", strings.Repeat("A", 32), strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("g", 32), "../../etc/passwd"} {
		if _, ok := (SessionInfo{SID: sid}).Sanitized(); ok {
			t.Fatalf("非法 SID 应丢弃: %q", sid)
		}
	}
}

func TestHelloSanitizedSessionsCapsAndFilters(t *testing.T) {
	h := Hello{}
	for i := 0; i < MaxSessionsPerHost+10; i++ {
		sid := []byte(strings.Repeat("a", 32))
		sid[0] = "0123456789abcdef"[i%16]
		sid[1] = "0123456789abcdef"[i/16]
		h.Sessions = append(h.Sessions, SessionInfo{SID: string(sid), State: SessionRunning})
	}
	h.Sessions = append(h.Sessions, SessionInfo{SID: "bad"})
	if got := h.SanitizedSessions(); len(got) != MaxSessionsPerHost {
		t.Fatalf("会话清单应被限制为 %d 条，实际 %d", MaxSessionsPerHost, len(got))
	}
}

func TestClampSize(t *testing.T) {
	cases := []struct{ r, c, wr, wc uint16 }{
		{0, 0, 24, 80}, {1, 1, 1, 1}, {1000, 1000, 1000, 1000}, {1001, 65535, 1000, 1000}, {40, 0, 40, 80},
	}
	for _, c := range cases {
		r, cc := ClampSize(c.r, c.c)
		if r != c.wr || cc != c.wc {
			t.Fatalf("ClampSize(%d,%d) = %d,%d，期望 %d,%d", c.r, c.c, r, cc, c.wr, c.wc)
		}
	}
}

// 模糊测试：任何字节序列都不得让解码器 panic，且通过校验的帧必须能无损再编码。
func FuzzDecodeOutput(f *testing.F) {
	f.Add(EncodeOutput(1, 2, []byte("abc")))
	f.Add([]byte{})
	f.Add([]byte{0x01})
	f.Add(make([]byte, 13))
	f.Fuzz(func(t *testing.T, data []byte) {
		ch, off, payload, err := DecodeOutput(data)
		if err != nil {
			return
		}
		if !bytes.Equal(EncodeOutput(ch, off, payload), data) {
			t.Fatalf("通过解码的帧必须可无损还原: %x", data)
		}
	})
}

func FuzzDecodeInputAck(f *testing.F) {
	f.Add(EncodeInput(1, []byte("x")))
	f.Add(EncodeAck(1, 2))
	f.Add([]byte{0x03})
	f.Fuzz(func(t *testing.T, data []byte) {
		if ch, payload, err := DecodeInput(data); err == nil {
			if !bytes.Equal(EncodeInput(ch, payload), data) {
				t.Fatalf("输入帧无法还原: %x", data)
			}
		}
		if ch, off, err := DecodeAck(data); err == nil {
			if !bytes.Equal(EncodeAck(ch, off), data) {
				t.Fatalf("确认帧无法还原: %x", data)
			}
		}
		_, _ = FrameType(data)
		_, _, _ = DecodeBrowserOutput(data)
		_, _ = DecodeBrowserInput(data)
	})
}

// hello 是唯一会被未认证方解析的复杂结构，必须对任意输入稳健。
func FuzzHelloParsing(f *testing.F) {
	good, _ := Marshal(Hello{T: THello, V: 1, Pub: "x", Sig: "y"})
	f.Add(good)
	f.Add([]byte(`{"t":"hello","v":1,"pub":"","sig":"","meta":null,"sessions":[{"sid":"zz"}]}`))
	f.Add([]byte(`{"t":"hello","sessions":[null,{},{"rows":-1}]}`))
	f.Add([]byte(`{"t":"hello","meta":{"host":"\u202e\u0000"}}`))
	f.Add([]byte(`[`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxPreAuthFrame {
			data = data[:MaxPreAuthFrame]
		}
		var h Hello
		if err := json.Unmarshal(data, &h); err != nil {
			return
		}
		_, _, _ = h.Decode()
		_ = h.Meta.Sanitized()
		for _, s := range h.SanitizedSessions() {
			if !ValidSessionID(s.SID) {
				t.Fatalf("净化后的会话 SID 必须合法: %q", s.SID)
			}
			if s.Rows == 0 || s.Cols == 0 || s.Rows > 1000 || s.Cols > 1000 {
				t.Fatalf("尺寸越界: %+v", s)
			}
		}
	})
}
