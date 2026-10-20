package termproto

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	okID     = "0123456789abcdef0123456789abcdef"
	okTicket = "fedcba9876543210fedcba9876543210"
	okSHA    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestUpgradeValidate(t *testing.T) {
	good := Upgrade{T: TUpgrade, ID: okID, Version: "v1.6.3-dev-20261008-1649b9e", Ticket: okTicket, SHA256: okSHA}
	if err := good.Validate(); err != nil {
		t.Fatalf("合规的请求被拒绝: %v", err)
	}
	cases := []struct {
		name string
		mut  func(u *Upgrade)
	}{
		{"id 太短", func(u *Upgrade) { u.ID = "abc" }},
		{"id 含大写", func(u *Upgrade) { u.ID = strings.ToUpper(okID) }},
		{"版本为空", func(u *Upgrade) { u.Version = "" }},
		{"版本含空格", func(u *Upgrade) { u.Version = "v1 2" }},
		{"版本含路径符", func(u *Upgrade) { u.Version = "../v1" }},
		{"版本过长", func(u *Upgrade) { u.Version = "v" + strings.Repeat("1", 80) }},
		{"票据不是 32 位十六进制", func(u *Upgrade) { u.Ticket = "short" }},
		{"票据含非十六进制", func(u *Upgrade) { u.Ticket = strings.Repeat("g", 32) }},
		{"sha 长度不对", func(u *Upgrade) { u.SHA256 = okSHA[:60] }},
		{"sha 含大写", func(u *Upgrade) { u.SHA256 = strings.ToUpper(okSHA) }},
	}
	for _, c := range cases {
		u := good
		c.mut(&u)
		if err := u.Validate(); err == nil {
			t.Errorf("%s：应当被拒绝", c.name)
		}
	}
}

func TestUpgradeFrameHasNoURLField(t *testing.T) {
	// 安全约束：帧里不能有 URL / 文件名 / 路径，Agent 的下载地址只能由它自己推出（见 Upgrade 的注释）。
	raw, err := json.Marshal(Upgrade{T: TUpgrade, ID: okID, Version: "v1.0.0", Ticket: okTicket, SHA256: okSHA})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for k := range m {
		switch k {
		case "t", "id", "version", "ticket", "sha256":
		default:
			t.Errorf("升级帧里出现了意料之外的字段 %q", k)
		}
	}
}

func TestUpgradeStatusSanitized(t *testing.T) {
	s, ok := UpgradeStatus{ID: okID, Stage: UpgradeStageFailed, Code: UpgradeCodeDownload, Detail: "下载失败\x1b[31m: " + strings.Repeat("长", 400)}.Sanitized()
	if !ok {
		t.Fatal("合规的状态被拒绝")
	}
	if strings.ContainsRune(s.Detail, 0x1b) {
		t.Errorf("说明里的控制字符没有去除: %q", s.Detail)
	}
	if n := len([]rune(s.Detail)); n > MaxUpgradeDetailRunes {
		t.Errorf("说明没有限长: %d", n)
	}
	if s.T != TUpgradeStatus {
		t.Errorf("T = %q", s.T)
	}
	for _, bad := range []UpgradeStatus{
		{ID: "x", Stage: UpgradeStageFailed},
		{ID: okID, Stage: "unknown"},
		{ID: okID},
	} {
		if _, ok := bad.Sanitized(); ok {
			t.Errorf("%+v 应当被丢弃", bad)
		}
	}
}

func TestMetaCarriesWebUpgrade(t *testing.T) {
	in := Meta{Host: "h", OS: "linux", Arch: "amd64", Ver: "v1", Priv: PrivRoot, Kind: KindPersistent, WebUpgrade: WebUpgradeOn,
		LastUpgrade: &UpgradeNote{Outcome: UpgradeOutcomeRolledBack, From: "v1", To: "v2", At: 1700000000, Detail: "没有在 90 秒内连上服务端"}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Meta
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	out = out.Sanitized()
	if out.WebUpgrade != WebUpgradeOn {
		t.Errorf("WebUpgrade = %q", out.WebUpgrade)
	}
	if out.LastUpgrade == nil || out.LastUpgrade.Outcome != UpgradeOutcomeRolledBack || out.LastUpgrade.To != "v2" || out.LastUpgrade.At != 1700000000 {
		t.Errorf("LastUpgrade = %+v", out.LastUpgrade)
	}

	// 不认识的值一律丢弃（不能让对端塞进别的取值），旧版本没有这些字段时保持空。
	bad := Meta{WebUpgrade: "yes", LastUpgrade: &UpgradeNote{Outcome: "whatever"}}.Sanitized()
	if bad.WebUpgrade != "" || bad.LastUpgrade != nil {
		t.Errorf("不认识的取值应被丢弃: %+v", bad)
	}
	old := Meta{Host: "h"}.Sanitized()
	if old.WebUpgrade != "" || old.LastUpgrade != nil {
		t.Errorf("旧版本没有这些字段: %+v", old)
	}
	if raw, _ := json.Marshal(old); strings.Contains(string(raw), "web_upgrade") || strings.Contains(string(raw), "last_upgrade") {
		t.Errorf("空值不应出现在 JSON 里（保持与旧版本的握手一致）: %s", raw)
	}
}

// 平台标识（Meta.Plat）只用来在分发目录里找对应的二进制：只接受两段小写字母数字，其余一律当作没声明。
func TestMetaPlatformIsSanitized(t *testing.T) {
	for _, ok := range []string{"linux-amd64", "linux-arm64", "darwin-arm64", "windows-amd64"} {
		if got := (Meta{Plat: ok}).Sanitized().Plat; got != ok {
			t.Errorf("%q 应被保留，得到 %q", ok, got)
		}
	}
	for _, bad := range []string{
		"", "linux", "Linux-AMD64", "linux_amd64", "linux-amd64-v2", "../../etc/passwd", "linux-amd64\n", " linux-amd64",
		"linux-", "-amd64", "l-a", strings.Repeat("a", 17) + "-amd64", "linux-amd64/../x", "linux-amd64;rm",
	} {
		if got := (Meta{Plat: bad}).Sanitized().Plat; got != "" {
			t.Errorf("%q 应被丢弃，得到 %q", bad, got)
		}
	}
	// 旧版本没有这个字段：JSON 里不出现，握手与旧服务端一致。
	if raw, _ := json.Marshal(Meta{Host: "h"}.Sanitized()); strings.Contains(string(raw), "plat") {
		t.Errorf("空值不应出现在 JSON 里: %s", raw)
	}
}
