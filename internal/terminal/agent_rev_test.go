package terminal

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Agent 侧代码指纹（#143）：
//   - 版本号跟着整个仓库的构建走，服务端每热更一次就变；以前拿它与主机一比，所有主机都亮「可升级」；
//   - 现在两边都有指纹时只比指纹：指纹一致 = Agent 代码一致，不提醒；指纹不同才提醒；
//   - 任何一边没有指纹（旧版本 Agent、没有 REV 文件的部署）时回退到比版本号，行为与引入指纹之前一致。

const (
	revA = "72ae54bd8799"
	revB = "0123456789ab"
)

func TestJudgeUpgrade(t *testing.T) {
	const (
		latest = "v1.6.3-dev-20261016-c26a452"
		older  = "v1.6.3-dev-20261015-95b0679"
		newer  = "v1.6.3-dev-20261017-aaaaaaa"
		sameDy = "v1.6.3-dev-20261016-aaaaaaa" // 同一天不同提交：无法判断先后
	)
	cases := []struct {
		name                    string
		curVer, curRev          string
		latestVer, latestRev    string
		wantAvail, wantSameCode bool
		wantKind                string
	}{
		// —— 两边都有指纹：只看指纹 ——
		{"指纹一致、版本号更旧：这就是 #124 的现场，不该提醒", older, revA, latest, revA, false, true, ""},
		{"指纹一致、版本号无法比较：同样不提醒", sameDy, revA, latest, revA, false, true, ""},
		{"指纹一致、版本号也一致：不提醒，也不必解释", latest, revA, latest, revA, false, false, ""},
		{"指纹不同、版本号更旧：确实该升级", older, revA, latest, revB, true, false, upgradeKindOlder},
		{"指纹不同、版本号无法比较（同一天两次构建）：代码确实变了", sameDy, revA, latest, revB, true, false, upgradeKindChanged},
		{"指纹不同、但主机版本号更新（服务端回退过）：不拿较旧的一批去降级主机", newer, revA, latest, revB, false, false, ""},
		{"指纹不同、版本号一致（同版本号的本地构建）：不提醒", latest, revA, latest, revB, false, false, ""},
		// —— 任一边没有指纹：回退到版本号，与引入指纹之前一致 ——
		{"主机没有指纹（旧版本 Agent）、版本号更旧", older, "", latest, revA, true, false, upgradeKindOlder},
		{"主机没有指纹、版本号无法比较", sameDy, "", latest, revA, true, false, upgradeKindDiffers},
		{"服务端没有 REV 文件、主机有指纹、版本号更旧", older, revA, latest, "", true, false, upgradeKindOlder},
		{"两边都没有指纹、短哈希版本", "5c23d7b", "", latest, "", true, false, upgradeKindDiffers},
		{"两边都没有指纹、版本号一致", latest, "", latest, "", false, false, ""},
		{"两边都没有指纹、主机更新", newer, "", latest, "", false, false, ""},
		// —— 不合规的指纹等同于没有（正常路径里握手时就已清洗，这里确认判断函数本身也不被带偏）——
		{"主机指纹不合规", older, "NOT-HEX", latest, revA, true, false, upgradeKindOlder},
		{"服务端指纹不合规", older, revA, latest, "xyz", true, false, upgradeKindOlder},
	}
	for _, c := range cases {
		got := judgeUpgrade(c.curVer, c.curRev, c.latestVer, c.latestRev)
		if got.Available != c.wantAvail || got.Kind != c.wantKind || got.SameCode != c.wantSameCode {
			t.Errorf("%s：得到 %+v，想要 available=%v kind=%q sameCode=%v", c.name, got, c.wantAvail, c.wantKind, c.wantSameCode)
		}
	}
}

func writeDistRev(t *testing.T, dir, rev string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "REV"), []byte(rev+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 主机视图：指纹一致的主机不再亮「可升级」，指纹不同的亮；旧版本 Agent（没有指纹）仍按版本号。
func TestHostViewUsesAgentRevWhenBothSidesHaveIt(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	writeDistRev(t, dir, revA)
	e := newTestEnv(t, withAgentDist(dir))

	enroll := func(host, ver, rev, remark string) string {
		m := testMeta(host)
		m.Ver, m.Rev = ver, rev
		return e.enrollAgent(m, remark).code
	}

	// 版本号旧、代码一致：热更之后最常见的情形。
	same := hostViewOf(t, e, enroll("same-code", "v1.6.0", revA, "代码一致"))
	if _, has := same["upgrade_available"]; has {
		t.Fatalf("指纹一致不应提醒升级: %v", same)
	}
	if same["up_to_date_by_rev"] != true || same["agent_rev"] != revA || same["latest_agent_rev"] != revA || same["latest_agent_version"] != "v1.7.0" {
		t.Fatalf("应说明「代码一致」并带出两边的指纹: %v", same)
	}

	// 代码确实变了。
	changed := hostViewOf(t, e, enroll("changed-code", "v1.6.0", revB, "代码有变化"))
	if changed["upgrade_available"] != true || changed["upgrade_kind"] != "older" || changed["agent_rev"] != revB {
		t.Fatalf("指纹不同应提醒升级: %v", changed)
	}
	if _, has := changed["up_to_date_by_rev"]; has {
		t.Fatalf("有可升级的更新时不应同时说「代码一致」: %v", changed)
	}

	// 版本号无法比较（短哈希），指纹不同：提示「代码有更新」而不是「版本不同」。
	hash := hostViewOf(t, e, enroll("hash-box", "5c23d7b", revB, "短哈希"))
	if hash["upgrade_available"] != true || hash["upgrade_kind"] != "changed" {
		t.Fatalf("指纹不同但版本号无法比较应为 changed: %v", hash)
	}

	// 旧版本 Agent 没有指纹：回退到比版本号，与引入指纹之前一致。
	legacy := hostViewOf(t, e, enroll("legacy-box", "v1.6.0", "", "旧版本"))
	if legacy["upgrade_available"] != true || legacy["upgrade_kind"] != "older" {
		t.Fatalf("没有指纹应回退到比版本号: %v", legacy)
	}
	if _, has := legacy["agent_rev"]; has {
		t.Fatalf("没有指纹时不应给出 agent_rev: %v", legacy)
	}

	// 不合规的指纹在握手时被清洗成空，等同于没有。
	junk := hostViewOf(t, e, enroll("junk-box", "v1.6.0", "NOT-A-HEX-FINGERPRINT", "乱写的指纹"))
	if _, has := junk["agent_rev"]; has || junk["upgrade_kind"] != "older" {
		t.Fatalf("不合规的指纹应被丢弃并回退到比版本号: %v", junk)
	}
}

// 服务端没有 REV 文件（旧镜像 / 没有写 REV 的部署）：一切按版本号，行为与引入指纹之前一致。
func TestHostViewWithoutRevFileFallsBackToVersion(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	e := newTestEnv(t, withAgentDist(dir))
	m := testMeta("has-rev-box")
	m.Ver, m.Rev = "v1.6.0", revA
	v := hostViewOf(t, e, e.enrollAgent(m, "").code)
	if v["upgrade_available"] != true || v["upgrade_kind"] != "older" {
		t.Fatalf("服务端没有指纹时应回退到比版本号: %v", v)
	}
	if _, has := v["latest_agent_rev"]; has {
		t.Fatalf("没有 REV 文件时不应给出 latest_agent_rev: %v", v)
	}
	if v["agent_rev"] != revA {
		t.Fatalf("主机自报的指纹照常带出: %v", v)
	}
}

// REV 与 VERSION 一样只是元数据，不是可分发的文件；格式不合规的 REV 等同于没有。
func TestRevFileIsNeverServedAndMustBeWellFormed(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	writeDistRev(t, dir, revA)
	e := newTestEnv(t, withAgentDist(dir))
	ticket := e.openWindow()
	if st, _, _ := e.callRaw("GET", "/i/"+ticket+"/REV"); st != http.StatusNotFound {
		t.Fatalf("REV 不应可下载: %d", st)
	}

	writeDistRev(t, dir, "NOT-HEX-AT-ALL") // 长度与原来不同，缓存会失效
	m := testMeta("box")
	m.Ver, m.Rev = "v1.6.0", revA
	v := hostViewOf(t, e, e.enrollAgent(m, "").code)
	if _, has := v["latest_agent_rev"]; has {
		t.Fatalf("不合规的 REV 应被忽略: %v", v)
	}
	if v["upgrade_available"] != true {
		t.Fatalf("不合规的 REV 应回退到比版本号: %v", v)
	}
}

// 主机的指纹落库：离线时「可升级」的判断也照常（不靠在线连接里的内存状态），重新上线后跟着新值刷新。
func TestAgentRevPersistsForOfflineHostsAndRefreshesOnReconnect(t *testing.T) {
	dir, _, _ := newDistDir(t)
	writeDistVersion(t, dir, "v1.7.0")
	writeDistRev(t, dir, revA)
	e := newTestEnv(t, withAgentDist(dir))
	m := testMeta("persist-box")
	m.Ver, m.Rev = "v1.6.0", revB
	a := e.enrollAgent(m, "")

	a.close()
	eventually(t, 3*time.Second, "主机离线", func() bool { return e.hub.onlineAgent(a.code) == nil })
	off := hostViewOf(t, e, a.code)
	if off["online"] != false || off["agent_rev"] != revB || off["upgrade_available"] != true {
		t.Fatalf("离线主机应带着落库的指纹给出升级判断: %v", off)
	}

	// 带着新代码（指纹与服务端自带的一致）重新上线：落库的指纹刷新，不再提醒。
	m.Ver, m.Rev = "v1.7.0", revA
	b := e.connectEnabled(a.priv, m)
	b.close()
	eventually(t, 3*time.Second, "主机再次离线", func() bool { return e.hub.onlineAgent(a.code) == nil })
	back := hostViewOf(t, e, a.code)
	if back["agent_rev"] != revA {
		t.Fatalf("重新上线后应刷新落库的指纹: %v", back)
	}
	if _, has := back["upgrade_available"]; has {
		t.Fatalf("指纹一致后不应再提醒: %v", back)
	}
}

// 守护进程的指纹原样带到视图里（界面用它与主机 Agent 的指纹比，判断守护进程跑的是不是旧代码）；不合规的被丢弃。
func TestHolderRevIsExposedAndSanitized(t *testing.T) {
	e := newTestEnv(t)
	m := testMeta("holder-rev-box")
	m.Rev = revA
	a := e.enrollAgent(m, "")
	a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test", Rev: revA})
	eventually(t, 3*time.Second, "Hub 收到守护进程的指纹", func() bool {
		hv := e.hub.onlineAgent(a.code).holderView()
		return hv != nil && hv.Rev == revA
	})
	h, _ := hostViewOf(t, e, a.code)["holder"].(map[string]any)
	if h["rev"] != revA {
		t.Fatalf("主机视图的 holder 应带 rev: %v", h)
	}

	a.send(termproto.Holder{T: termproto.THolder, State: termproto.HolderReady, Persistent: true, Version: "test", Rev: "../../etc/passwd"})
	eventually(t, 3*time.Second, "不合规的指纹被清洗", func() bool {
		hv := e.hub.onlineAgent(a.code).holderView()
		return hv != nil && hv.Rev == ""
	})
}

// 一键升级：指纹一致就说明没有可升的东西（说清楚原因），指纹不同才发请求。
func TestWebUpgradeJudgesByRev(t *testing.T) {
	f := newWebUpgradeFixture(t, func(m *termproto.Meta) { m.Rev = revA })
	dist := f.env.hub.dist.Dir()
	writeDistRev(t, dist, revA)

	res := f.post(nil)
	if res.Status != http.StatusConflict || res.str("code") != "up_to_date" {
		t.Fatalf("代码一致应以 up_to_date 拒绝: %d %s", res.Status, res.Raw)
	}
	if !strings.Contains(res.str("error"), "代码完全一致") {
		t.Fatalf("应说明是代码一致（版本号不同只是构建号不同）: %s", res.Raw)
	}
	f.noRequestSent(t)

	// 服务端换了一批代码不同的 Agent：现在可以升级了（长度不同，避开 Dist 对 REV 的缓存）。
	writeDistRev(t, dist, revB+"0")
	f.request(t)
}
