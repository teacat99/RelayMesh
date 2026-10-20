package termproto

import (
	"regexp"
	"strconv"
)

// Agent 版本的比较：服务端用它给主机卡片打「可升级」徽标，Agent 用它拒绝「升级」到不比自己新的版本。
// 放在协议包里，是为了两边用的是同一份判断，不会各写一套而慢慢走样。
//
// 版本号的来源很杂：正式版 v1.7.0、预发布版 v2.0.0-rc.1（-alpha.N / -beta.N / -rc.N）、
// 开发版 v1.6.3-dev-20261003-24bd66b（基线 tag + 构建日期 + 提交；基线是预发布 tag 时形如 v2.0.0-rc.1-dev-20261101-abcdef1）、
// 只有提交短哈希的手工构建（5c23d7b）、本地构建的 dev-5c23d7b / dev。
// 能解析成「主.次.补丁[-预发布.N][-dev-日期-提交]」的才能判断新旧；
// 其余只能说「与对方的不同」——宁可如实说不知道谁新谁旧，也不要把一个无法比较的版本说成「过旧」。
//
// 先后顺序（语义化版本）：主.次.补丁 → 同一个主.次.补丁里 alpha < beta < rc < 正式版 → 同一个预发布序号内按 N 的数值 →
// 同一基线下的 dev 构建比基线 tag 本身新（它是在 tag「之后」构建的），dev 之间比构建日期。

var agentVerRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-(alpha|beta|rc)\.(\d+))?(?:-dev-(\d{8})-([0-9a-f]{4,40}))?$`)

// 预发布的排名：数字越大越新；正式版本排在所有预发布之后。
const preRankRelease = 4

var preRank = map[string]int{"alpha": 1, "beta": 2, "rc": 3}

// agentVersion 是解析后的版本。
type agentVersion struct {
	major, minor, patch int
	// pre 是预发布的排名（alpha=1、beta=2、rc=3，正式版本=preRankRelease）；preN 是 -rc.N 里的 N，正式版本为 0。
	pre, preN int
	// date 是开发版的构建日期（YYYYMMDD）；正式版本为 0。开发版是在基线 tag「之后」构建的，所以同一基线下 dev 比正式版新。
	date int
	sha  string
	ok   bool
}

func parseAgentVersion(s string) agentVersion {
	m := agentVerRe.FindStringSubmatch(s)
	if m == nil {
		return agentVersion{}
	}
	num := func(v string) int {
		n, _ := strconv.Atoi(v)
		return n
	}
	pre, preN := preRankRelease, 0
	if m[4] != "" {
		pre, preN = preRank[m[4]], num(m[5])
	}
	return agentVersion{major: num(m[1]), minor: num(m[2]), patch: num(m[3]), pre: pre, preN: preN, date: num(m[6]), sha: m[7], ok: true}
}

// compare 比较两个已解析的版本：-1 / 0 / 1；第二个返回值为 false 表示无法确定先后（同基线、同一天、不同提交）。
func (v agentVersion) compare(o agentVersion) (int, bool) {
	for _, p := range [][2]int{{v.major, o.major}, {v.minor, o.minor}, {v.patch, o.patch}, {v.pre, o.pre}, {v.preN, o.preN}, {v.date, o.date}} {
		switch {
		case p[0] < p[1]:
			return -1, true
		case p[0] > p[1]:
			return 1, true
		}
	}
	if v.sha == o.sha {
		return 0, true
	}
	return 0, false
}

// agentRevRe 限定「Agent 侧代码指纹」的写法：小写十六进制，6–40 位。它来自对端、会原样参与比较与显示，不接受别的内容。
var agentRevRe = regexp.MustCompile(`^[0-9a-f]{6,40}$`)

// ValidAgentRev 判断 s 是否是合法的 Agent 侧代码指纹（计算方式见 internal/agentrev）。
//
// 版本号跟着整个仓库的构建走，服务端每热更一次就变一次；指纹只在 Agent 二进制依赖的代码真的变了时才变。
// 所以「要不要升级」「守护进程是否落后」先比指纹：指纹一致，两边跑的就是同一份 Agent 代码，版本号不同只是构建号不同。
func ValidAgentRev(s string) bool { return agentRevRe.MatchString(s) }

// SanitizeAgentRev 返回合法的指纹，否则返回空串。空串表示对端没有声明（旧版本 / 非发布构建），调用方回退到版本号比较。
func SanitizeAgentRev(s string) string {
	if ValidAgentRev(s) {
		return s
	}
	return ""
}

// CompareAgentRev 比较两个指纹。known 为 false 表示至少一边没有声明或不合法，调用方必须回退到版本号比较；
// known 为 true 时 same 说明两边的 Agent 代码是否一致。
func CompareAgentRev(a, b string) (same, known bool) {
	if !ValidAgentRev(a) || !ValidAgentRev(b) {
		return false, false
	}
	return a == b, true
}

// VersionRelation 描述当前版本相对目标版本的关系。
type VersionRelation int

const (
	// VersionCurrent：一致，或当前版本更新（例如服务端回退过）——都不需要升级。
	VersionCurrent VersionRelation = iota
	// VersionOlder：能确定当前版本更旧。
	VersionOlder
	// VersionDiffers：与目标版本不同，但无法比较新旧（短哈希之类）。
	VersionDiffers
)

// RelateAgentVersion 比较当前版本 cur 与目标版本 latest。任何一边为空都按「无需升级」处理。
func RelateAgentVersion(cur, latest string) VersionRelation {
	if cur == "" || latest == "" || cur == latest {
		return VersionCurrent
	}
	a, b := parseAgentVersion(cur), parseAgentVersion(latest)
	if !a.ok || !b.ok {
		return VersionDiffers
	}
	switch c, known := a.compare(b); {
	case !known:
		return VersionDiffers
	case c < 0:
		return VersionOlder
	default:
		return VersionCurrent
	}
}
