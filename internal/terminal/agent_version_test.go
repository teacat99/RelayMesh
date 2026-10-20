package terminal

import "testing"

// 版本解析与比较本身的用例在 termproto/agentversion_test.go；这里只确认服务端沿用的名字与它保持一致。
func TestRelateAgentVersion(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        versionRelation
		why         string
	}{
		{"", "v1.7.0", versionCurrent, "主机版本未知：不提醒"},
		{"v1.7.0", "", versionCurrent, "服务端没有版本信息：不提醒"},
		{"v1.7.0", "v1.7.0", versionCurrent, "完全一致"},
		{"v1.6.9", "v1.7.0", versionOlder, "次版本更旧"},
		{"v1.7.1", "v1.7.0", versionCurrent, "主机更新（服务端回退过）：不提醒"},
		{"v1.6.3", "v1.6.3-dev-20261003-24bd66b", versionOlder, "同基线下 dev 构建比正式版新"},
		{"v1.6.3-dev-20261003-aaaaaaa", "v1.6.3-dev-20261003-24bd66b", versionDiffers, "同一天不同提交：无法判断先后"},
		{"e14958f", "v1.6.3-dev-20261003-24bd66b", versionDiffers, "短哈希：不同但无法比较"},
		{"e14958f", "e14958f", versionCurrent, "短哈希相同"},
	}
	for _, c := range cases {
		if got := relateAgentVersion(c.cur, c.latest); got != c.want {
			t.Errorf("%q vs %q：%s——得到 %d，想要 %d", c.cur, c.latest, c.why, got, c.want)
		}
	}
}
