package ntfy

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func intp(v int) *int { return &v }

// okRun 是一条「跑了 5 分钟、成功结束」的运行，各测试在它的基础上改。
func okRun() RunInfo {
	return RunInfo{
		RunID: "r1", State: model.RunSucceeded, Machine: "RM-AAAA-BBBB",
		Description: "跑全量回归测试", Command: "go test ./... -count=1",
		ExitCode: intp(0), Duration: 5 * time.Minute,
		WorkflowID: "wf", ProjectDirectory: "/home/dev/RelayMesh",
	}
}

func TestBuildRunMessage_OutcomeTitlesTagsAndPriority(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(*RunInfo)
		wantTitle    string
		wantTag      string
		wantPriority int // 配置的优先级是 5
	}{
		{"成功：不吵，优先级压到默认", func(r *RunInfo) {}, "模型命令已完成", "white_check_mark", 3},
		{"失败带退出码", func(r *RunInfo) { r.State, r.ExitCode = model.RunFailed, intp(3) }, "模型命令失败（退出码 3）", "x", 5},
		{"失败带信号", func(r *RunInfo) { r.State, r.ExitCode, r.Signal = model.RunFailed, nil, "SIGKILL" }, "模型命令被信号终止（SIGKILL）", "x", 5},
		{"失败什么都没有", func(r *RunInfo) { r.State, r.ExitCode = model.RunFailed, nil }, "模型命令失败", "x", 5},
		{"超时", func(r *RunInfo) { r.State, r.ExitCode = model.RunTimedOut, nil }, "模型命令超时，已被终止", "hourglass_flowing_sand", 5},
		{"结果丢失", func(r *RunInfo) { r.State, r.ExitCode = model.RunLost, nil }, "模型命令的结果已丢失", "warning", 5},
		{"被主机侧中断", func(r *RunInfo) { r.State, r.ExitCode = model.RunCancelled, nil }, "模型命令被中断", "warning", 5},
	}
	cfg := Config{Priority: 5, ClickURL: "https://rm.example.com/"}.Normalize()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := okRun()
			tc.mutate(&in)
			m := BuildRunMessage(cfg, in, "", 0)
			if m.Title != tc.wantTitle {
				t.Errorf("标题不对：%q，想要 %q", m.Title, tc.wantTitle)
			}
			if len(m.Tags) != 1 || m.Tags[0] != tc.wantTag {
				t.Errorf("标签不对：%v，想要 %q", m.Tags, tc.wantTag)
			}
			if m.Priority != tc.wantPriority {
				t.Errorf("优先级不对：%d，想要 %d", m.Priority, tc.wantPriority)
			}
			if m.Click != "https://rm.example.com/" {
				t.Errorf("点击地址不对：%q", m.Click)
			}
		})
	}

	// 成功的优先级只往下压，不往上抬：配置成 2（低）就还是 2。
	low := Config{Priority: 2}.Normalize()
	if m := BuildRunMessage(low, okRun(), "", 0); m.Priority != 2 {
		t.Errorf("配置的优先级低于默认时，成功推送不应被抬高：%d", m.Priority)
	}
}

func TestBuildRunMessage_BodyHasWhatYouNeedAndNothingMore(t *testing.T) {
	in := okRun()
	in.Command = "echo hunter2-SECRET && make deploy\nsecond line"
	in.Why = "主机重启，进程已丢失"
	in.State, in.ExitCode = model.RunLost, nil
	in.Duration = 3*time.Hour + 5*time.Minute + 20*time.Second

	m := BuildRunMessage(Config{}.Normalize(), in, "build-box", 0)
	for _, want := range []string{
		"跑全量回归测试",
		"主机：build-box",
		"项目：RelayMesh",
		"用时 3 小时 5 分",
		"原因：主机重启，进程已丢失",
	} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("正文应含 %q：\n%s", want, m.Body)
		}
	}
	// 默认不带命令：命令里可能有内部地址和口令的蛛丝马迹，不该默认经过第三方服务。
	for _, banned := range []string{"hunter2", "make deploy", "go test", "命令："} {
		if strings.Contains(m.Body, banned) {
			t.Errorf("默认不应带出命令内容 %q：\n%s", banned, m.Body)
		}
	}
	// 机器码只是内部标识，没有解析出主机名时也不往推送里塞。
	if strings.Contains(m.Body, "RM-AAAA-BBBB") {
		t.Errorf("推送里不应出现机器码：\n%s", m.Body)
	}
}

func TestBuildRunMessage_NoHostNameMeansNoHostLine(t *testing.T) {
	m := BuildRunMessage(Config{}.Normalize(), okRun(), "", 0)
	if strings.Contains(m.Body, "主机：") {
		t.Errorf("没有主机名就不要写「主机：」：\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "项目：RelayMesh") {
		t.Errorf("项目仍应出现：\n%s", m.Body)
	}
}

func TestBuildRunMessage_IncludeSummaryAddsFirstCommandLineOnly(t *testing.T) {
	in := okRun()
	in.Command = "\n  make   build \nrm -rf /tmp/x"
	m := BuildRunMessage(Config{IncludeSummary: true}.Normalize(), in, "h", 0)
	if !strings.Contains(m.Body, "命令：make build") {
		t.Errorf("打开「附带摘要」后应带命令的第一行（折叠空白）：\n%s", m.Body)
	}
	if strings.Contains(m.Body, "rm -rf") {
		t.Errorf("只应带第一行：\n%s", m.Body)
	}

	// 没写说明时，命令就是第一行内容；既没说明也没打开摘要时，正文仍有「用时」，不会是空的。
	in.Description = ""
	m = BuildRunMessage(Config{IncludeSummary: true}.Normalize(), in, "h", 0)
	if !strings.HasPrefix(m.Body, "命令：make build") {
		t.Errorf("没有说明时命令应排在最前：\n%s", m.Body)
	}
	m = BuildRunMessage(Config{}.Normalize(), in, "", 0)
	if strings.TrimSpace(m.Body) == "" || !strings.Contains(m.Body, "用时") {
		t.Errorf("什么都不带时正文也不能是空的：%q", m.Body)
	}
}

func TestBuildRunMessage_TruncatesAndFoldsByRunes(t *testing.T) {
	in := okRun()
	in.Description = "第一行\n第二行 " + strings.Repeat("汉", 300)
	in.Command = strings.Repeat("字", 400)
	m := BuildRunMessage(Config{IncludeSummary: true}.Normalize(), in, strings.Repeat("机", 100), 0)
	if !utf8.ValidString(m.Body) {
		t.Fatalf("截断不应劈开汉字：%q", m.Body)
	}
	lines := strings.Split(m.Body, "\n")
	if !strings.HasPrefix(lines[0], "第一行 第二行 ") {
		t.Errorf("说明里的换行应折成空格：%q", lines[0])
	}
	if r := []rune(lines[0]); len(r) != titleLineMaxLen+1 || r[len(r)-1] != '…' {
		t.Errorf("说明应截成 %d 个字符加省略号，实际 %d", titleLineMaxLen, len(r))
	}
	if r := []rune(lines[1]); len(r) != len([]rune("命令："))+commandExcerptLen+1 {
		t.Errorf("命令应截成 %d 个字符加省略号，实际整行 %d", commandExcerptLen, len(r))
	}
	// 主机和项目在同一行：「主机：…　项目：…」。主机名给了 100 个字，应只留 40 个加省略号。
	var hostSeen bool
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l, "主机："); ok {
			hostSeen = true
			host, _, _ := strings.Cut(rest, "　项目：")
			if r := []rune(host); len(r) != hostNameMaxLen+1 || r[len(r)-1] != '…' {
				t.Errorf("主机名应截成 %d 个字符加省略号，实际 %d 个：%q", hostNameMaxLen, len(r), host)
			}
		}
	}
	if !hostSeen {
		t.Errorf("应有主机行：\n%s", m.Body)
	}
}

func TestBuildRunMessage_MergedNote(t *testing.T) {
	m := BuildRunMessage(Config{}.Normalize(), okRun(), "h", 4)
	if !strings.Contains(m.Body, "另有 4 条通知因推送过于频繁被合并") {
		t.Errorf("应说明被合并的条数：%q", m.Body)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "不到 1 秒"},
		{400 * time.Millisecond, "不到 1 秒"},
		{time.Second, "1 秒"},
		{45 * time.Second, "45 秒"},
		{time.Minute, "1 分钟"},
		{90 * time.Second, "1 分 30 秒"},
		{59*time.Minute + 59*time.Second, "59 分 59 秒"},
		{time.Hour, "1 小时"},
		{time.Hour + 2*time.Minute + 5*time.Second, "1 小时 2 分"}, // 到小时这一级就不再报秒
		{26 * time.Hour, "1 天 2 小时"},
		{24 * time.Hour, "1 天"},
		{7 * 24 * time.Hour, "7 天"},
		{-5 * time.Second, "不到 1 秒"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%s) = %q，想要 %q", tc.d, got, tc.want)
		}
	}
}

func TestRunInfo_WorthNotifying(t *testing.T) {
	on := Config{RunNotify: true, RunMinSeconds: 60}.Normalize()
	with := func(f func(*RunInfo)) RunInfo { r := okRun(); f(&r); return r }

	cases := []struct {
		name string
		cfg  Config
		in   RunInfo
		want bool
	}{
		{"成功、够久", on, okRun(), true},
		{"开关关着", Config{RunNotify: false}.Normalize(), okRun(), false},
		{"失败", on, with(func(r *RunInfo) { r.State = model.RunFailed }), true},
		{"超时", on, with(func(r *RunInfo) { r.State = model.RunTimedOut }), true},
		{"结果丢失", on, with(func(r *RunInfo) { r.State = model.RunLost }), true},
		{"没人要求取消、被主机侧中断", on, with(func(r *RunInfo) { r.State = model.RunCancelled }), true},
		{"你自己取消的", on, with(func(r *RunInfo) { r.State, r.CancelBy = model.RunCancelled, "user" }), false},
		{"暂停开关取消的", on, with(func(r *RunInfo) { r.State, r.CancelBy = model.RunCancelled, "pause" }), false},
		{"收回授权取消的", on, with(func(r *RunInfo) { r.State, r.CancelBy = model.RunCancelled, "revoked" }), false},
		{"系统（停用主机 / 令牌变更）取消的", on, with(func(r *RunInfo) { r.State, r.CancelBy = model.RunCancelled, "system" }), false},
		{"模型自己取消的", on, with(func(r *RunInfo) { r.State, r.CancelBy = model.RunCancelled, "model" }), false},
		{"被拒绝：根本没运行", on, with(func(r *RunInfo) { r.State = model.RunRejected }), false},
		{"被闸门拒绝：根本没运行", on, with(func(r *RunInfo) { r.State = model.RunDenied }), false},
		{"还在运行", on, with(func(r *RunInfo) { r.State = model.RunRunning }), false},
		{"还在等批准", on, with(func(r *RunInfo) { r.State = model.RunAwaitingApproval }), false},
		{"空状态", on, with(func(r *RunInfo) { r.State = "" }), false},
		{"差一点没到门槛", on, with(func(r *RunInfo) { r.Duration = 60*time.Second - time.Millisecond }), false},
		{"恰好到门槛", on, with(func(r *RunInfo) { r.Duration = 60 * time.Second }), true},
		{"门槛调到 1 秒", Config{RunNotify: true, RunMinSeconds: 1}.Normalize(), with(func(r *RunInfo) { r.Duration = time.Second }), true},
		{"没跑起来的失败（时长为 0）", on, with(func(r *RunInfo) { r.State, r.Duration = model.RunFailed, 0 }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.WorthNotifying(tc.cfg); got != tc.want {
				t.Errorf("WorthNotifying = %v，想要 %v（%+v）", got, tc.want, tc.in)
			}
		})
	}
}
