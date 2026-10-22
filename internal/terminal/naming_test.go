package terminal

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 主机的「名字」只有一个口径：显示名 = 备注，没有备注才是主机名。
// 界面标题、「同名」提示、模型在 terminal_targets 里看到的 name、target 的解析都以它为准。

func codesOf(hosts []model.TerminalHost, idx []int) []string {
	var out []string
	for _, i := range idx {
		out = append(out, hosts[i].MachineCode)
	}
	return out
}

func TestMatchHostsPrefersDisplayNameOverHostnameAlias(t *testing.T) {
	const a, b, c = "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"
	hosts := []model.TerminalHost{
		{MachineCode: a, Hostname: "web", Remark: "db"},   // 显示名 db，自报主机名 web
		{MachineCode: b, Hostname: "srv2", Remark: "Web"}, // 显示名 Web
		{MachineCode: c, Hostname: "cache"},               // 没有备注：显示名就是 cache
	}
	cases := []struct {
		want  string
		codes []string
		why   string
	}{
		{"web", []string{b}, "显示名命中优先：a 自报的主机名 web 只是别名，不能抢走 b 的备注"},
		{"WEB", []string{b}, "大小写不敏感"},
		{"db", []string{a}, "备注"},
		{"cache", []string{c}, "没有备注时用主机名"},
		{"srv2", []string{b}, "没有任何显示名等于 srv2，才退回主机名别名"},
		{"AAAAAAAAAAAAAAAA", []string{a}, "机器码不区分大小写"},
		{"nope", nil, "不存在"},
	}
	for _, tc := range cases {
		if got := codesOf(hosts, matchHosts(hosts, tc.want)); !reflect.DeepEqual(got, tc.codes) {
			t.Errorf("target=%q: got %v, want %v（%s）", tc.want, got, tc.codes, tc.why)
		}
	}
}

func TestResolveTargetUsesDisplayName(t *testing.T) {
	const a, b, c = "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"
	hosts := []model.TerminalHost{
		{MachineCode: a, Hostname: "web", Remark: "db"},
		{MachineCode: b, Hostname: "srv2", Remark: "Web"},
		{MachineCode: c, Hostname: "cache"},
	}
	for want, code := range map[string]string{"web": b, "db": a, "cache": c, "srv2": b} {
		h, terr := resolveTarget(hosts, want)
		if terr != nil || h.MachineCode != code {
			t.Errorf("resolve(%q) = %v / %v, want %s", want, h, terr, code)
		}
	}

	// 显示名相同（一台靠备注，一台靠主机名）：必须报歧义并列出机器码，而不是挑一台。
	dup := []model.TerminalHost{
		{MachineCode: a, Hostname: "web"},
		{MachineCode: b, Hostname: "srv2", Remark: "WEB"},
	}
	_, terr := resolveTarget(dup, "web")
	if terr == nil || terr.Code != termexec.CodeHostNotFound || !strings.Contains(terr.Message, a) || !strings.Contains(terr.Message, b) {
		t.Fatalf("显示名相同应报歧义并列出两个机器码: %+v", terr)
	}
	// 机器码永远能精确指定。
	if h, terr := resolveTarget(dup, b); terr != nil || h.MachineCode != b {
		t.Fatalf("机器码应能消除歧义: %v %v", h, terr)
	}
	// 两台都没有备注、自报同一个主机名：同样是歧义。
	same := []model.TerminalHost{{MachineCode: a, Hostname: "ubuntu"}, {MachineCode: b, Hostname: "ubuntu"}}
	if _, terr := resolveTarget(same, "ubuntu"); terr == nil || !strings.Contains(terr.Message, "歧义") {
		t.Fatalf("自报同名应是歧义: %+v", terr)
	}
	// 其中一台写了备注之后，歧义消失：备注就是区分它们的办法。
	same[1].Remark = "ubuntu-gpu"
	if h, terr := resolveTarget(same, "ubuntu-gpu"); terr != nil || h.MachineCode != b {
		t.Fatalf("写了备注的主机应能用备注指定: %v %v", h, terr)
	}
	if h, terr := resolveTarget(same, "ubuntu"); terr != nil || h.MachineCode != a {
		t.Fatalf("另一台仍能用主机名指定（它的显示名就是主机名；写了备注那台的主机名只是别名）: %v %v", h, terr)
	}
}

func TestMarkDuplicateNamesFollowsDisplayName(t *testing.T) {
	enabled, disabled := termproto.HostStatusEnabled, termproto.HostStatusDisabled
	hosts := []HostView{
		{MachineCode: "a", Hostname: "web", Remark: "db", Status: enabled},      // 主机名相同，但备注不同 → 名字不同
		{MachineCode: "b", Hostname: "web", Remark: "cache", Status: enabled},   // 同上
		{MachineCode: "c", Hostname: "x", Remark: "Same", Status: enabled},      // 备注相同（大小写不敏感）
		{MachineCode: "d", Hostname: "y", Remark: "same", Status: enabled},      // 同上
		{MachineCode: "e", Hostname: "z", Remark: "same", Status: disabled},     // 停用的不参与，也不被标记
		{MachineCode: "f", Hostname: "plain", Status: enabled},                  // 没有备注：显示名 plain
		{MachineCode: "g", Hostname: "PLAIN", Remark: "other", Status: enabled}, // 自报主机名撞上 f 的显示名，但它的显示名是 other
	}
	pend := []PendingView{{MachineCode: "p1", Hostname: "plain"}, {MachineCode: "p2", Hostname: "db"}, {MachineCode: "p3", Hostname: "fresh"}}
	markDuplicateNames(hosts, pend)

	wantHosts := map[string]bool{"a": true, "b": false, "c": true, "d": true, "e": false, "f": true, "g": false}
	for _, h := range hosts {
		if h.DuplicateName != wantHosts[h.MachineCode] {
			t.Errorf("主机 %s: DuplicateName=%v, want %v", h.MachineCode, h.DuplicateName, wantHosts[h.MachineCode])
		}
	}
	// 待启用项没有备注，显示名就是自报主机名；它与已启用主机的显示名相同时双方都要提示。
	wantPend := map[string]bool{"p1": true, "p2": true, "p3": false}
	for _, p := range pend {
		if p.DuplicateName != wantPend[p.MachineCode] {
			t.Errorf("待启用 %s: DuplicateName=%v, want %v", p.MachineCode, p.DuplicateName, wantPend[p.MachineCode])
		}
	}
}

// 模型在 terminal_targets 里看到的名字、「同名」标记和按名字过滤，全部来自同一个口径。
func TestTargetsNameAndDuplicateFlagFollowDisplayName(t *testing.T) {
	f := newExecFixture(t)                                        // 主机 exec-host，备注 "build box"
	second := f.env.enrollAgent(testMeta("exec-host"), "")        // 没有备注：显示名 exec-host
	third := f.env.enrollAgent(testMeta("db-01"), "exec-host")    // 备注与上一台的主机名相同 → 同名
	fourth := f.env.enrollAgent(testMeta("build box"), "special") // 主机名撞上第一台的备注，但显示名是 special

	codes := []string{f.a.code, second.code, third.code, fourth.code}
	caller := f.caller(func(c *termexec.Caller) { c.Credential.TerminalHosts = model.StringList(codes) })
	out, err := f.svc.Targets(context.Background(), caller, "")
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]termexec.Target{}
	for _, tg := range out.(*termexec.TargetsResult).Targets {
		byCode[tg.MachineCode] = tg
	}
	if len(byCode) != 4 {
		t.Fatalf("targets = %+v", byCode)
	}
	want := map[string]struct {
		name string
		dup  bool
	}{
		f.a.code:    {"build box", false},
		second.code: {"exec-host", true},
		third.code:  {"exec-host", true},
		fourth.code: {"special", false},
	}
	for code, w := range want {
		if tg := byCode[code]; tg.Name != w.name || tg.DuplicateName != w.dup {
			t.Errorf("%s: name=%q dup=%v, want name=%q dup=%v", code, tg.Name, tg.DuplicateName, w.name, w.dup)
		}
	}

	// 按名字过滤与解析一致：第四台的主机名 "build box" 只是别名，第一台的备注 "build box" 是显示名，应只命中第一台。
	got := func(filter string) []string {
		o, err := f.svc.Targets(context.Background(), caller, filter)
		if err != nil {
			t.Fatal(err)
		}
		var cs []string
		for _, tg := range o.(*termexec.TargetsResult).Targets {
			cs = append(cs, tg.MachineCode)
		}
		return cs
	}
	if cs := got("BUILD BOX"); len(cs) != 1 || cs[0] != f.a.code {
		t.Fatalf("按备注过滤应只命中备注为 build box 的主机: %v", cs)
	}
	if cs := got("special"); len(cs) != 1 || cs[0] != fourth.code {
		t.Fatalf("按备注过滤: %v", cs)
	}
	if cs := got("exec-host"); len(cs) != 2 {
		t.Fatalf("同名的两台都应出现在过滤结果里，由模型改用机器码: %v", cs)
	}

	// 同名时直接用名字执行必须被拒绝，并提示改用机器码；没有同名时可以用名字。
	_, err = f.exec(t, caller, withTarget(waitSet(3), "exec-host", "true"))
	if te, ok := err.(*termexec.Error); !ok || te.Code != termexec.CodeHostNotFound || !strings.Contains(te.Message, "机器码") {
		t.Fatalf("同名目标应被拒绝: %v", err)
	}
}
