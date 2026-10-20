package termproto

import "testing"

func TestParseAgentVersion(t *testing.T) {
	cases := []struct {
		in      string
		ok      bool
		maj, mi int
		pa, dt  int
		sha     string
	}{
		{"v1.7.0", true, 1, 7, 0, 0, ""},
		{"1.7.0", true, 1, 7, 0, 0, ""},
		{"v1.6.3-dev-20261003-24bd66b", true, 1, 6, 3, 20261003, "24bd66b"},
		{"v10.20.30", true, 10, 20, 30, 0, ""},
		{"", false, 0, 0, 0, 0, ""},
		{"dev", false, 0, 0, 0, 0, ""},
		{"5c23d7b", false, 0, 0, 0, 0, ""},
		{"dev-5c23d7b", false, 0, 0, 0, 0, ""},
		{"v1.7", false, 0, 0, 0, 0, ""},
		{"v1.7.0-rc1", false, 0, 0, 0, 0, ""},
		{"v1.7.0-dev-2026-abc", false, 0, 0, 0, 0, ""},
		{"v2.0.0-pre.1", false, 0, 0, 0, 0, ""},
		{"v2.0.0-RC.1", false, 0, 0, 0, 0, ""},
		{"v2.0.0-rc.", false, 0, 0, 0, 0, ""},
		{"v2.0.0-rc.1.2", false, 0, 0, 0, 0, ""},
	}
	for _, c := range cases {
		got := parseAgentVersion(c.in)
		if got.ok != c.ok {
			t.Errorf("%q: ok=%v，想要 %v", c.in, got.ok, c.ok)
			continue
		}
		if c.ok && (got.major != c.maj || got.minor != c.mi || got.patch != c.pa || got.date != c.dt || got.sha != c.sha) {
			t.Errorf("%q: 解析结果 %+v", c.in, got)
		}
	}
}

// 预发布版本（v2.0.0-rc.1）能解析：排名与序号单独存放；正式版本的排名在所有预发布之后；基线是预发布 tag 的 dev 构建也能解析。
func TestParseAgentVersionPrerelease(t *testing.T) {
	cases := []struct {
		in         string
		pre, preN  int
		date       int
		sha        string
		maj, mi, p int
	}{
		{"v2.0.0-rc.1", 3, 1, 0, "", 2, 0, 0},
		{"2.0.0-beta.12", 2, 12, 0, "", 2, 0, 0},
		{"v2.0.0-alpha.3", 1, 3, 0, "", 2, 0, 0},
		{"v2.0.0", preRankRelease, 0, 0, "", 2, 0, 0},
		{"v2.0.0-rc.1-dev-20261101-abcdef1", 3, 1, 20261101, "abcdef1", 2, 0, 0},
	}
	for _, c := range cases {
		got := parseAgentVersion(c.in)
		if !got.ok || got.pre != c.pre || got.preN != c.preN || got.date != c.date || got.sha != c.sha ||
			got.major != c.maj || got.minor != c.mi || got.patch != c.p {
			t.Errorf("%q: 解析结果 %+v", c.in, got)
		}
	}
}

func TestRelateAgentVersion(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        VersionRelation
		why         string
	}{
		{"", "v1.7.0", VersionCurrent, "当前版本未知：不提醒"},
		{"v1.7.0", "", VersionCurrent, "没有目标版本信息：不提醒"},
		{"v1.7.0", "v1.7.0", VersionCurrent, "完全一致"},
		{"v1.6.9", "v1.7.0", VersionOlder, "次版本更旧"},
		{"v1.7.0", "v1.7.1", VersionOlder, "补丁更旧"},
		{"v1.9.9", "v2.0.0", VersionOlder, "主版本更旧"},
		{"v1.7.1", "v1.7.0", VersionCurrent, "当前更新（服务端回退过）：不提醒"},
		{"v1.6.3", "v1.6.3-dev-20261003-24bd66b", VersionOlder, "同基线下 dev 构建比正式版新"},
		{"v1.6.3-dev-20261003-24bd66b", "v1.6.3", VersionCurrent, "当前是 dev 构建、目标是同基线正式版：当前更新"},
		{"v1.6.3-dev-20261001-aaaaaaa", "v1.6.3-dev-20261003-24bd66b", VersionOlder, "dev 构建日期更早"},
		{"v1.6.3-dev-20261003-24bd66b", "v1.6.3-dev-20261001-aaaaaaa", VersionCurrent, "dev 构建日期更晚"},
		{"v1.6.3-dev-20261003-aaaaaaa", "v1.6.3-dev-20261003-24bd66b", VersionDiffers, "同一天不同提交：无法判断先后"},
		{"e14958f", "v1.6.3-dev-20261003-24bd66b", VersionDiffers, "短哈希：不同但无法比较"},
		{"v1.6.3-dev-20261003-24bd66b", "5c23d7b", VersionDiffers, "目标是短哈希：不同但无法比较"},
		{"dev", "dev-5c23d7b", VersionDiffers, "本地构建标记：不同但无法比较"},
		{"e14958f", "e14958f", VersionCurrent, "短哈希相同"},
		// 预发布（预览版）：alpha < beta < rc < 正式版；序号按数值比；基线是预发布 tag 的 dev 构建比该 tag 新、比下一个预发布与正式版旧
		{"v2.0.0-rc.1", "v2.0.0-rc.1", VersionCurrent, "预发布完全一致"},
		{"v2.0.0-rc.1", "v2.0.0-rc.2", VersionOlder, "rc.1 比 rc.2 旧"},
		{"v2.0.0-rc.2", "v2.0.0-rc.1", VersionCurrent, "当前 rc.2 比目标 rc.1 新：不提醒"},
		{"v2.0.0-rc.9", "v2.0.0-rc.10", VersionOlder, "序号按数值比较（9 < 10），不是按字符串"},
		{"v2.0.0-rc.1", "v2.0.0", VersionOlder, "预发布比同版本正式版旧（预览版 Agent 能升到正式版）"},
		{"v2.0.0", "v2.0.0-rc.1", VersionCurrent, "当前是正式版、目标是它的预发布（服务端回退到预览）：不提醒"},
		{"v2.0.0-alpha.9", "v2.0.0-beta.1", VersionOlder, "alpha 比 beta 旧"},
		{"v2.0.0-beta.3", "v2.0.0-rc.1", VersionOlder, "beta 比 rc 旧"},
		{"v1.9.9", "v2.0.0-rc.1", VersionOlder, "旧主版本的正式版比新主版本的预发布旧"},
		{"v2.0.0-rc.1", "v1.9.9", VersionCurrent, "预览版比上一个主版本的正式版新"},
		{"v2.0.0-rc.1", "v2.0.0-rc.1-dev-20261101-abcdef1", VersionOlder, "同一预发布基线下 dev 构建比 tag 本身新"},
		{"v2.0.0-rc.1-dev-20261101-abcdef1", "v2.0.0-rc.2", VersionOlder, "rc.1 的 dev 构建比 rc.2 旧"},
		{"v2.0.0-rc.1-dev-20261101-abcdef1", "v2.0.0", VersionOlder, "rc.1 的 dev 构建比正式版旧"},
		{"v2.0.0-rc.1-dev-20261101-aaaaaaa", "v2.0.0-rc.1-dev-20261101-bbbbbbb", VersionDiffers, "同一天不同提交：无法判断先后"},
		{"v2.0.0-rc1", "v2.0.0", VersionDiffers, "写法不对（缺点号）的预发布：不同但无法比较"},
	}
	for _, c := range cases {
		if got := RelateAgentVersion(c.cur, c.latest); got != c.want {
			t.Errorf("%q vs %q：%s——得到 %d，想要 %d", c.cur, c.latest, c.why, got, c.want)
		}
	}
}
