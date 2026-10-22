package termproto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetaCarriesAutoUpgrade(t *testing.T) {
	for _, mode := range []string{AutoUpgradeOff, AutoUpgradeNotify, AutoUpgradeSilent} {
		raw, err := json.Marshal(Meta{Host: "h", AutoUpgrade: mode})
		if err != nil {
			t.Fatal(err)
		}
		var out Meta
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if got := out.Sanitized().AutoUpgrade; got != mode {
			t.Errorf("模式 %q 经过握手后变成了 %q", mode, got)
		}
	}

	// 不认识的值一律丢弃：对端不能借这个字段塞进别的取值。
	for _, bad := range []string{"on", "yes", "SILENT", "silent ", "notify\n", "auto", " "} {
		if got := (Meta{AutoUpgrade: bad}).Sanitized().AutoUpgrade; got != "" {
			t.Errorf("不认识的取值 %q 应被丢弃，实际 %q", bad, got)
		}
	}

	// 旧版本没有这个字段：保持空，并且序列化时不出现（旧服务端看到的握手不变）。
	old := Meta{Host: "h"}.Sanitized()
	if old.AutoUpgrade != "" {
		t.Errorf("旧版本没有这个字段: %q", old.AutoUpgrade)
	}
	if raw, _ := json.Marshal(old); strings.Contains(string(raw), "auto_upgrade") {
		t.Errorf("空值不应序列化: %s", raw)
	}
}

func TestMetaWithAutoUpgradeStillDecodesForAnOlderHub(t *testing.T) {
	// 「旧 Hub + 新 Agent」：旧 Hub 用的是不认识 auto_upgrade 的结构体，多出来的键必须被忽略而不是报错。
	type oldMeta struct {
		Host       string `json:"host"`
		WebUpgrade string `json:"web_upgrade,omitempty"`
	}
	raw, err := json.Marshal(Meta{Host: "h", WebUpgrade: WebUpgradeOn, AutoUpgrade: AutoUpgradeSilent})
	if err != nil {
		t.Fatal(err)
	}
	var got oldMeta
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("旧结构应当能解出新握手: %v", err)
	}
	if got.Host != "h" || got.WebUpgrade != WebUpgradeOn {
		t.Errorf("既有字段不能受影响: %+v", got)
	}
}

func TestValidAutoUpgradeMode(t *testing.T) {
	for mode, want := range map[string]bool{
		AutoUpgradeNotify: true,
		AutoUpgradeSilent: true,
		AutoUpgradeOff:    false, // 关闭是删除同意，不是一种可以「开启」的模式
		"":                false,
		"on":              false,
		"Notify":          false,
	} {
		if got := ValidAutoUpgradeMode(mode); got != want {
			t.Errorf("ValidAutoUpgradeMode(%q) = %v，期望 %v", mode, got, want)
		}
	}
}

func TestAutoUpgradeCheckValidate(t *testing.T) {
	good := strings.Repeat("a1", 16)
	if err := (AutoUpgradeCheck{T: TAutoUpgradeCheck, ID: good}).Validate(); err != nil {
		t.Errorf("合规的标识被拒绝: %v", err)
	}
	for _, id := range []string{"", "short", strings.Repeat("g", 32), strings.Repeat("A", 32), strings.Repeat("a", 33), "../../etc/passwd"} {
		if err := (AutoUpgradeCheck{T: TAutoUpgradeCheck, ID: id}).Validate(); err == nil {
			t.Errorf("不合规的标识 %q 应被拒绝", id)
		}
	}
}

func TestAutoUpgradeCheckCarriesNothingButAnID(t *testing.T) {
	// 版本与平台以握手时声明的为准：检查帧里不能有可以让 Hub 去查「别的版本 / 别的平台」的字段。
	raw, err := json.Marshal(AutoUpgradeCheck{T: TAutoUpgradeCheck, ID: strings.Repeat("0f", 16)})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["t"] != TAutoUpgradeCheck || m["id"] == nil {
		t.Errorf("检查帧应当只有 t 与 id: %s", raw)
	}
}

func TestAutoUpgradeOfferSanitized(t *testing.T) {
	id := strings.Repeat("c3", 16)
	for _, status := range []string{AutoOfferDisabled, AutoOfferNotAllowed, AutoOfferUpToDate, AutoOfferAvailable, AutoOfferUnavailable, AutoOfferTooSoon} {
		got, ok := AutoUpgradeOffer{ID: id, Status: status, Version: "v1.7.0"}.Sanitized()
		if !ok || got.Status != status || got.Version != "v1.7.0" || got.T != TAutoUpgradeOffer || got.ID != id {
			t.Errorf("状态 %q 应当原样通过: ok=%v %+v", status, ok, got)
		}
	}

	// 标识不合规、状态不认识：整帧丢弃（Agent 不去猜）。
	if _, ok := (AutoUpgradeOffer{ID: "x", Status: AutoOfferAvailable}).Sanitized(); ok {
		t.Error("标识不合规应被丢弃")
	}
	if _, ok := (AutoUpgradeOffer{ID: id, Status: "upgrade_now"}).Sanitized(); ok {
		t.Error("不认识的状态应被丢弃")
	}

	// 版本号不合规：保留帧，但不带版本（不能把任意文本带进日志与界面）。
	for _, v := range []string{"v1 7", "<script>", "../x", strings.Repeat("9", 65), "\x1b[31m"} {
		got, ok := AutoUpgradeOffer{ID: id, Status: AutoOfferAvailable, Version: v}.Sanitized()
		if !ok || got.Version != "" {
			t.Errorf("不合规的版本号 %q 应被去掉: ok=%v %+v", v, ok, got)
		}
	}
}

func TestAutoUpgradeRequestValidate(t *testing.T) {
	good := AutoUpgradeRequest{T: TAutoUpgradeRequest, ID: strings.Repeat("a", 32), Version: "v1.7.0"}
	if err := good.Validate(); err != nil {
		t.Fatalf("合规的请求被拒绝: %v", err)
	}
	for name, bad := range map[string]AutoUpgradeRequest{
		"标识为空":    {ID: "", Version: "v1.7.0"},
		"标识太短":    {ID: "abc", Version: "v1.7.0"},
		"标识含大写":   {ID: strings.Repeat("A", 32), Version: "v1.7.0"},
		"版本为空":    {ID: good.ID, Version: ""},
		"版本含换行":   {ID: good.ID, Version: "v1\n2"},
		"版本含路径符号": {ID: good.ID, Version: "v1/../2"},
	} {
		if bad.Validate() == nil {
			t.Errorf("%s：应当被拒绝", name)
		}
	}
}

func grantedFixture() AutoUpgradeGrant {
	return AutoUpgradeGrant{
		T: TAutoUpgradeGrant, ID: strings.Repeat("a", 32), Status: AutoGrantGranted,
		Version: "v1.7.0", Ticket: strings.Repeat("b", 32), SHA256: strings.Repeat("c", 64),
		Sig: "rmsig1:0123456789abcdef:" + strings.Repeat("A", 86),
	}
}

func TestAutoUpgradeGrantSanitized(t *testing.T) {
	t.Run("granted 带齐合规的凭据", func(t *testing.T) {
		in := grantedFixture()
		out, ok := in.Sanitized()
		if !ok || out != in {
			t.Fatalf("ok=%v out=%+v", ok, out)
		}
	})
	t.Run("granted 缺任何一项或写法不合规都不被采纳", func(t *testing.T) {
		mut := map[string]func(*AutoUpgradeGrant){
			"没有版本":     func(g *AutoUpgradeGrant) { g.Version = "" },
			"版本含换行":    func(g *AutoUpgradeGrant) { g.Version = "v1\n" },
			"没有票据":     func(g *AutoUpgradeGrant) { g.Ticket = "" },
			"票据太短":     func(g *AutoUpgradeGrant) { g.Ticket = "abc" },
			"票据含路径":    func(g *AutoUpgradeGrant) { g.Ticket = strings.Repeat("b", 30) + ".." },
			"没有校验值":    func(g *AutoUpgradeGrant) { g.SHA256 = "" },
			"校验值是大写":   func(g *AutoUpgradeGrant) { g.SHA256 = strings.Repeat("C", 64) },
			"没有签名":     func(g *AutoUpgradeGrant) { g.Sig = "" },
			"签名含空白":    func(g *AutoUpgradeGrant) { g.Sig = "rmsig1:0123456789abcdef:AAAA BBBB" },
			"签名含换行":    func(g *AutoUpgradeGrant) { g.Sig = "rmsig1:0123456789abcdef:AAAA\n" },
			"签名超长":     func(g *AutoUpgradeGrant) { g.Sig = strings.Repeat("A", MaxSignatureTextLen+1) },
			"标识不合规":    func(g *AutoUpgradeGrant) { g.ID = "xyz" },
			"状态不认识":    func(g *AutoUpgradeGrant) { g.Status = "approved" },
			"状态大小写不对":  func(g *AutoUpgradeGrant) { g.Status = "GRANTED" },
			"状态带多余的空格": func(g *AutoUpgradeGrant) { g.Status = "granted " },
		}
		for name, f := range mut {
			g := grantedFixture()
			f(&g)
			if _, ok := g.Sanitized(); ok {
				t.Errorf("%s：不应被采纳", name)
			}
		}
	})
	t.Run("不是 granted 的裁决里夹带的凭据一律丢弃", func(t *testing.T) {
		for _, st := range []string{AutoGrantDisabled, AutoGrantNotAllowed, AutoGrantUpToDate, AutoGrantStale,
			AutoGrantUnavailable, AutoGrantUnsigned, AutoGrantBusy, AutoGrantTooSoon} {
			g := grantedFixture()
			g.Status = st
			out, ok := g.Sanitized()
			if !ok {
				t.Fatalf("%s：应当被认可", st)
			}
			if out.Ticket != "" || out.SHA256 != "" || out.Sig != "" {
				t.Errorf("%s：凭据没有被丢弃: %+v", st, out)
			}
		}
	})
	t.Run("granted 的帧转成 upgrade 帧后过得了 Upgrade.Validate", func(t *testing.T) {
		g, _ := grantedFixture().Sanitized()
		u := g.ToUpgrade()
		if err := u.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if u.T != TUpgrade || u.ID != g.ID || u.Version != g.Version || u.Ticket != g.Ticket || u.SHA256 != g.SHA256 {
			t.Fatalf("字段没有原样带过去: %+v", u)
		}
	})
}

// 授予帧经过 JSON：旧版本不认识的字段会被忽略，所以「新 Hub + 旧 Agent」安全；这里确认字段名没写错。
func TestAutoUpgradeGrantJSONShape(t *testing.T) {
	raw, err := json.Marshal(grantedFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"t":"auto_upgrade_grant"`, `"status":"granted"`, `"version":"v1.7.0"`, `"ticket":`, `"sha256":`, `"sig":"rmsig1:`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("序列化结果里缺少 %s: %s", key, raw)
		}
	}
	var back AutoUpgradeGrant
	if err := json.Unmarshal(raw, &back); err != nil || back != grantedFixture() {
		t.Fatalf("往返不一致: %v %+v", err, back)
	}
}

func TestValidAutoWindow(t *testing.T) {
	for _, ok := range []string{"03:00-05:00", "00:00-23:59", "23:00-02:00", "12:30-12:31", "00:00-00:01"} {
		if !ValidAutoWindow(ok) {
			t.Errorf("%q 应当合规", ok)
		}
	}
	for _, bad := range []string{"", "3:00-5:00", "03:00-05:0", "24:00-05:00", "03:60-05:00", "03:00-25:00", "03:00–05:00",
		"03:00-05:00 ", " 03:00-05:00", "03:00-05:00\n", "03:00-03:00", "00:00-00:00", "03:00", "03:00-05:00-06:00", "ab:cd-ef:gh"} {
		if ValidAutoWindow(bad) {
			t.Errorf("%q 不应合规", bad)
		}
	}
}

func TestMetaCarriesAutoWindowOnlyForSilent(t *testing.T) {
	m := Meta{Host: "h", AutoUpgrade: AutoUpgradeSilent, AutoWindow: "03:00-05:00"}.Sanitized()
	if m.AutoWindow != "03:00-05:00" {
		t.Errorf("静默模式的时间段应当保留: %q", m.AutoWindow)
	}
	for _, mode := range []string{AutoUpgradeOff, AutoUpgradeNotify, ""} {
		if got := (Meta{AutoUpgrade: mode, AutoWindow: "03:00-05:00"}).Sanitized().AutoWindow; got != "" {
			t.Errorf("模式 %q 下不应带时间段: %q", mode, got)
		}
	}
	if got := (Meta{AutoUpgrade: AutoUpgradeSilent, AutoWindow: "whenever"}).Sanitized().AutoWindow; got != "" {
		t.Errorf("不合规的时间段应被丢弃: %q", got)
	}
	// 没有时间段时不序列化：旧 Hub 看到的握手不变。
	raw, _ := json.Marshal(Meta{Host: "h", AutoUpgrade: AutoUpgradeSilent}.Sanitized())
	if strings.Contains(string(raw), "auto_window") {
		t.Errorf("空值不应序列化: %s", raw)
	}
}
