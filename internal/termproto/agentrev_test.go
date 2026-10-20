package termproto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentRevValidation(t *testing.T) {
	good := []string{"72ae54bd8799", "abcdef", "0123456789abcdef0123456789abcdef01234567"}
	for _, s := range good {
		if !ValidAgentRev(s) || SanitizeAgentRev(s) != s {
			t.Errorf("%q 应是合法指纹", s)
		}
	}
	bad := []string{
		"", "abc", "ABCDEF123456", "72ae54bd879g", " 72ae54bd8799", "72ae54bd8799\n",
		"0123456789abcdef0123456789abcdef012345678", // 41 位
		"72ae54bd8799\x1b[31m", "v1.6.3-dev-20261016-501c1ff",
	}
	for _, s := range bad {
		if ValidAgentRev(s) || SanitizeAgentRev(s) != "" {
			t.Errorf("%q 不应被接受为指纹", s)
		}
	}
}

func TestCompareAgentRev(t *testing.T) {
	const a, b = "72ae54bd8799", "5c23d7b00112"
	cases := []struct {
		name        string
		x, y        string
		same, known bool
	}{
		{"相同", a, a, true, true},
		{"不同", a, b, false, true},
		{"左边没有声明", "", a, false, false},
		{"右边没有声明", a, "", false, false},
		{"两边都没有", "", "", false, false},
		{"一边不合法按没有处理", "NOT-A-REV", a, false, false},
	}
	for _, c := range cases {
		same, known := CompareAgentRev(c.x, c.y)
		if same != c.same || known != c.known {
			t.Errorf("%s：CompareAgentRev(%q, %q) = (%v, %v)，期望 (%v, %v)", c.name, c.x, c.y, same, known, c.same, c.known)
		}
	}
}

// Meta / Holder 里的 Rev 来自对端：合法才保留，其余一律清掉；没有时不出现在 JSON 里（旧版本互通）。
func TestRevSanitizedInMetaAndHolder(t *testing.T) {
	if got := (Meta{Host: "h", Rev: "72ae54bd8799"}).Sanitized().Rev; got != "72ae54bd8799" {
		t.Fatalf("合法指纹应保留: %q", got)
	}
	if got := (Meta{Host: "h", Rev: "<script>alert(1)</script>"}).Sanitized().Rev; got != "" {
		t.Fatalf("不合法的指纹应被清掉: %q", got)
	}
	if got := (Holder{State: HolderReady, Rev: "72ae54bd8799"}).Sanitized().Rev; got != "72ae54bd8799" {
		t.Fatalf("守护进程的合法指纹应保留: %q", got)
	}
	if got := (Holder{State: HolderReady, Rev: "NOPE"}).Sanitized().Rev; got != "" {
		t.Fatalf("守护进程的不合法指纹应被清掉: %q", got)
	}

	raw, _ := json.Marshal(Meta{Host: "h"}.Sanitized())
	if strings.Contains(string(raw), `"rev"`) {
		t.Fatalf("没有指纹时不应出现 rev 字段: %s", raw)
	}
	raw, _ = json.Marshal(Holder{State: HolderReady}.Sanitized())
	if strings.Contains(string(raw), `"rev"`) {
		t.Fatalf("守护进程没有指纹时不应出现 rev 字段: %s", raw)
	}

	// 旧版本的帧没有 rev：照常解析，指纹为空。
	var old Meta
	if err := json.Unmarshal([]byte(`{"host":"h","os":"linux","arch":"amd64","ver":"v1.6.0","priv":"root","kind":"persistent"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Sanitized().Rev != "" {
		t.Fatal("旧版本的 Meta 不应有指纹")
	}
}
