package cli

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
)

// ---------- 命令行引用 ----------

// parseWindowsArgs 是 CommandLineToArgvW 规则的参考实现，用来验证 windowsQuoteArg 能被原样解析回来。
func parseWindowsArgs(s string) []string {
	var args []string
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			return args
		}
		var cur strings.Builder
		inQuote := false
		for i < len(s) {
			c := s[i]
			if c == '\\' {
				n := 0
				for i < len(s) && s[i] == '\\' {
					n++
					i++
				}
				if i < len(s) && s[i] == '"' {
					cur.WriteString(strings.Repeat(`\`, n/2))
					if n%2 == 1 {
						cur.WriteByte('"')
						i++
					}
					continue
				}
				cur.WriteString(strings.Repeat(`\`, n))
				continue
			}
			if c == '"' {
				inQuote = !inQuote
				i++
				continue
			}
			if !inQuote && (c == ' ' || c == '\t') {
				break
			}
			cur.WriteByte(c)
			i++
		}
		args = append(args, cur.String())
	}
}

func TestWindowsQuoteArgRoundTrips(t *testing.T) {
	cases := [][]string{
		{"run", "--server", "https://relay.example.com:18776"},
		{`C:\Program Files\RelayMesh Agent\relaymesh-agentw.exe`, "run"},
		{`C:\Users\a b\AppData\Local\RelayMesh\Agent\`, "x"}, // 结尾反斜杠
		{`a"b`, `c\"d`, `e\\"f`, ``},
		{`--data-dir`, `D:\dir with space\sub dir\`, `--log-file`, `D:\x y\agent.log`},
		{"tab\there", "new\nline"},
		{`trailing\`, `two\\`},
	}
	for _, args := range cases {
		line := windowsJoinArgs(args)
		got := parseWindowsArgs(line)
		if len(got) != len(args) {
			t.Fatalf("%q → %q → %q", args, line, got)
		}
		for i := range args {
			if got[i] != args[i] {
				t.Errorf("参数 %d 不一致: 原 %q，解析回 %q（整行 %s）", i, args[i], got[i], line)
			}
		}
	}
	if got := windowsQuoteArg("plain"); got != "plain" {
		t.Errorf("无需引用的参数应原样返回: %q", got)
	}
}

// ---------- XML ----------

type taskDoc struct {
	XMLName xml.Name `xml:"Task"`
	Desc    string   `xml:"RegistrationInfo>Description"`
	Logon   []struct {
		User string `xml:"UserId"`
	} `xml:"Triggers>LogonTrigger"`
	Boot []struct {
		Enabled string `xml:"Enabled"`
	} `xml:"Triggers>BootTrigger"`
	Time []struct {
		Interval string `xml:"Repetition>Interval"`
		Start    string `xml:"StartBoundary"`
	} `xml:"Triggers>TimeTrigger"`
	Principal struct {
		User      string `xml:"UserId"`
		LogonType string `xml:"LogonType"`
		RunLevel  string `xml:"RunLevel"`
	} `xml:"Principals>Principal"`
	Settings struct {
		Multiple string `xml:"MultipleInstancesPolicy"`
		Limit    string `xml:"ExecutionTimeLimit"`
		Priority string `xml:"Priority"`
		Restart  string `xml:"RestartOnFailure>Interval"`
		Battery  string `xml:"DisallowStartIfOnBatteries"`
	} `xml:"Settings"`
	Exec struct {
		Command string `xml:"Command"`
		Args    string `xml:"Arguments"`
	} `xml:"Actions>Exec"`
}

func parseTaskXML(t *testing.T, s string) taskDoc {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(s))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil } // 声明是 UTF-16，这里喂的是 Go 字符串
	var doc taskDoc
	if err := d.Decode(&doc); err != nil {
		t.Fatalf("生成的 XML 不合法: %v\n%s", err, s)
	}
	return doc
}

func TestRenderTaskXMLUserScope(t *testing.T) {
	start := time.Date(2026, 10, 1, 8, 30, 15, 0, time.UTC)
	doc := parseTaskXML(t, renderTaskXML(taskSpec{
		Description: "RelayMesh <Agent> & co",
		User:        `WINHOST\alice`,
		Command:     `C:\Users\teaca\AppData\Local\Programs\RelayMesh Agent\relaymesh-agentw.exe`,
		Arguments:   `run --server "https://x?a=1&b=<2>"`,
		Start:       start,
	}))
	if doc.Desc != "RelayMesh <Agent> & co" {
		t.Errorf("描述里的特殊字符应被正确转义: %q", doc.Desc)
	}
	if len(doc.Logon) != 1 || doc.Logon[0].User != `WINHOST\alice` || len(doc.Boot) != 0 {
		t.Errorf("用户级应只有登录触发器: logon=%v boot=%v", doc.Logon, doc.Boot)
	}
	if len(doc.Time) != 1 || doc.Time[0].Interval != "PT5M" || doc.Time[0].Start != "2026-10-01T08:30:15" {
		t.Errorf("应有 5 分钟重复触发器作为看门狗: %+v", doc.Time)
	}
	p := doc.Principal
	if p.User != `WINHOST\alice` || p.LogonType != "InteractiveToken" || p.RunLevel != "LeastPrivilege" {
		t.Errorf("用户级主体应为交互令牌 + 最低权限: %+v", p)
	}
	if doc.Exec.Command != `C:\Users\teaca\AppData\Local\Programs\RelayMesh Agent\relaymesh-agentw.exe` || doc.Exec.Args != `run --server "https://x?a=1&b=<2>"` {
		t.Errorf("动作不对: %+v", doc.Exec)
	}
}

func TestRenderTaskXMLSettingsThatMatterForALongRunningAgent(t *testing.T) {
	doc := parseTaskXML(t, renderTaskXML(taskSpec{System: true, Command: "c", Arguments: "a", Start: time.Now()}))
	s := doc.Settings
	if s.Limit != "PT0S" {
		t.Errorf("必须关闭执行时限，否则 72 小时后会被强行结束: %q", s.Limit)
	}
	if s.Multiple != "IgnoreNew" {
		t.Errorf("重复触发时必须忽略新实例，不能起第二个 Agent: %q", s.Multiple)
	}
	if s.Priority != "5" {
		t.Errorf("优先级应为正常（默认的 7 会让 Shell 变慢）: %q", s.Priority)
	}
	if s.Restart != "" {
		t.Errorf("RestartOnFailure 对被强杀的进程不生效，不应依赖它: %q", s.Restart)
	}
	if s.Battery != "false" {
		t.Errorf("笔记本用电池时也应保持运行: %q", s.Battery)
	}
}

func TestRenderTaskXMLSystemScope(t *testing.T) {
	doc := parseTaskXML(t, renderTaskXML(taskSpec{System: true, User: `X\ignored`, Command: "c", Arguments: "a", Start: time.Now()}))
	if len(doc.Boot) != 1 || len(doc.Logon) != 0 {
		t.Errorf("系统级应只有开机触发器: boot=%d logon=%d", len(doc.Boot), len(doc.Logon))
	}
	if doc.Principal.User != "S-1-5-18" || doc.Principal.RunLevel != "HighestAvailable" {
		t.Errorf("系统级应以 SYSTEM（S-1-5-18）+ 最高权限运行: %+v", doc.Principal)
	}
	if strings.Contains(renderTaskXML(taskSpec{System: true, User: `X\ignored`, Command: "c", Start: time.Now()}), `X\ignored`) {
		t.Error("系统级任务里不应出现用户名")
	}
}

func TestEncodeUTF16LEBOM(t *testing.T) {
	in := "中文 <ok> 😀"
	b := encodeUTF16LEBOM(in)
	if b[0] != 0xFF || b[1] != 0xFE || len(b)%2 != 0 {
		t.Fatalf("应以 FF FE 开头且长度为偶数: % x", b[:4])
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	if got := string(utf16.Decode(u)); got != in {
		t.Fatalf("编码后应能还原: %q", got)
	}
}

func TestSanitizeTaskPart(t *testing.T) {
	if got := sanitizeTaskPart(`WINHOST\al ice@x`); got != "WINHOST_al_ice_x" {
		t.Fatalf("got %q", got)
	}
}

// ---------- 安装 / 卸载流程（注入假的系统） ----------

type fakeWin struct {
	w        winOps
	cmds     []string
	xmls     []string // 每次 /Create 时读到的 XML 内容
	running  map[string]bool
	failOn   map[string]error
	outOn    map[string]string
	procsHit int
}

// newFakeWin 构造一个假的 Windows：所有目录都在临时目录里，schtasks 由内存里的假实现回答。
func newFakeWin(t *testing.T, elevated bool) *fakeWin {
	t.Helper()
	root := t.TempDir()
	exe := filepath.Join(root, "downloads", "relaymesh-agent.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, makePE(0x20b, peSubsystemCUI), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeWin{running: map[string]bool{}, failOn: map[string]error{}, outOn: map[string]string{}}
	installed := map[string]bool{} // 已创建的任务
	taskXML := map[string]string{} // 任务名 → 最近一次 /Create 的定义（/Query /XML 原样返回）
	f.w = winOps{
		elevated:    elevated,
		username:    `HOST\alice`,
		progFiles:   filepath.Join(root, "Program Files"),
		localApp:    filepath.Join(root, "Users", "alice", "AppData", "Local"),
		programData: filepath.Join(root, "ProgramData"),
		exe:         exe,
		tempDir:     filepath.Join(root, "tmp"),
		now:         func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) },
		sleep:       func(time.Duration) {},
	}
	f.w.run = func(name string, args ...string) (string, error) {
		line := strings.TrimSpace(name + " " + strings.Join(args, " "))
		f.cmds = append(f.cmds, line)
		for prefix, err := range f.failOn {
			if strings.HasPrefix(line, prefix) {
				return f.outOn[prefix], err
			}
		}
		if name != "schtasks" || len(args) < 3 {
			return "", nil
		}
		task := args[2]
		switch args[0] {
		case "/Create":
			raw, err := os.ReadFile(args[4])
			if err != nil {
				return "", err
			}
			def := decodeUTF16LE(raw)
			f.xmls = append(f.xmls, def)
			taskXML[task] = def
			installed[task] = true
		case "/Query":
			if !installed[task] {
				return "错误: 系统找不到指定的文件。", errors.New("exit status 1")
			}
			if len(args) > 3 && args[3] == "/XML" {
				return taskXML[task], nil
			}
		case "/Delete":
			delete(installed, task)
			delete(taskXML, task)
		case "/End":
			// 任务感知：结束 Agent 任务只影响 Agent 映像，守护进程有自己的映像和任务。
			holderTask := strings.HasPrefix(task, winHolderTaskName)
			for k := range f.running {
				if strings.HasSuffix(k, "-holderw.exe") == holderTask {
					f.running[k] = false
				}
			}
		case "/Run":
			image := "relaymesh-agentw.exe"
			if strings.HasPrefix(task, winHolderTaskName) {
				image = "relaymesh-agent-holderw.exe"
			}
			for _, p := range []string{
				filepath.Join(f.w.localApp, "Programs", "RelayMesh Agent", image),
				filepath.Join(f.w.progFiles, "RelayMesh Agent", image),
			} {
				if _, err := os.Stat(p); err == nil {
					f.running[p] = true
				}
			}
		}
		return "", nil
	}
	f.w.procs = func(image string) []int {
		f.procsHit++
		if f.running[image] {
			return []int{4242}
		}
		return nil
	}
	return f
}

func decodeUTF16LE(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	return string(utf16.Decode(u))
}

// winCfg 补全测试用的默认参数。基线用例沿用单任务（内嵌）形态；双任务形态的用例显式传 holderMode。
func winCfg(t *testing.T, c common) common {
	t.Helper()
	if c.server == "" {
		c.server = "https://relay.example.com"
	}
	if c.holderMode == "" {
		c.holderMode = "embedded"
	}
	return c
}

func TestInstallWindowsUserScope(t *testing.T) {
	f := newFakeWin(t, false)
	res, err := doInstallWindows(testEnv(t), winCfg(t, common{user: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(f.w.localApp, "Programs", "RelayMesh Agent")
	if res.BinPath != filepath.Join(binDir, "relaymesh-agent.exe") || res.TaskBinPath != filepath.Join(binDir, "relaymesh-agentw.exe") {
		t.Fatalf("可执行文件位置不对: %+v", res)
	}
	con, _ := os.ReadFile(res.BinPath)
	gui, _ := os.ReadFile(res.TaskBinPath)
	if binary.LittleEndian.Uint16(con[0x80+4+20+68:]) != peSubsystemCUI || binary.LittleEndian.Uint16(gui[0x80+4+20+68:]) != peSubsystemGUI {
		t.Fatal("应同时装控制台版与无窗口版")
	}
	if res.TaskName != "RelayMeshAgent-HOST_alice" || !res.User {
		t.Fatalf("用户级任务名应带用户名: %+v", res)
	}
	wantDir := filepath.Join(f.w.localApp, "RelayMesh", "Agent")
	if res.DataDir != wantDir || res.LogPath != filepath.Join(wantDir, "agent.log") {
		t.Fatalf("数据目录 / 日志位置不对: %+v", res)
	}
	if _, err := agent.LoadIdentity(wantDir); err != nil {
		t.Fatalf("应已创建身份: %v", err)
	}
	if !res.Started {
		t.Fatal("/Run 之后应检测到进程")
	}

	// 命令顺序：先停旧实例，再创建，再启动。
	var verbs []string
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "schtasks ") {
			verbs = append(verbs, strings.Fields(c)[1])
		}
	}
	if strings.Join(verbs, ",") != "/End,/Create,/Run" {
		t.Fatalf("schtasks 调用顺序不对: %v", f.cmds)
	}
	doc := parseTaskXML(t, f.xmls[0])
	if doc.Exec.Command != res.TaskBinPath {
		t.Errorf("任务应启动无窗口版: %q", doc.Exec.Command)
	}
	args := parseWindowsArgs(doc.Exec.Args)
	joined := strings.Join(args, "\x00")
	for _, want := range []string{"run", "--server\x00https://relay.example.com", "--data-dir\x00" + wantDir, "--log-file\x00" + res.LogPath} {
		if !strings.Contains(joined, want) {
			t.Errorf("任务参数缺少 %q: %q", strings.ReplaceAll(want, "\x00", " "), args)
		}
	}
	if doc.Principal.LogonType != "InteractiveToken" || len(doc.Logon) != 1 {
		t.Errorf("用户级任务应为登录触发 + 交互令牌: %+v", doc)
	}
}

func TestInstallWindowsSystemScope(t *testing.T) {
	f := newFakeWin(t, true)
	res, err := doInstallWindows(testEnv(t), winCfg(t, common{}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	if res.User || res.TaskName != "RelayMeshAgent" {
		t.Fatalf("管理员未指定 --user 时应装为系统级: %+v", res)
	}
	if !strings.HasPrefix(res.BinPath, filepath.Join(f.w.progFiles, "RelayMesh Agent")) {
		t.Fatalf("系统级可执行文件必须放在 Program Files（普通用户不可写），否则 SYSTEM 任务会成为提权通道: %s", res.BinPath)
	}
	if res.DataDir != filepath.Join(f.w.programData, "RelayMesh", "Agent") {
		t.Fatalf("系统级数据目录应在 ProgramData: %s", res.DataDir)
	}
	doc := parseTaskXML(t, f.xmls[0])
	if doc.Principal.User != "S-1-5-18" || len(doc.Boot) != 1 {
		t.Errorf("系统级任务应为 SYSTEM + 开机触发: %+v", doc)
	}
}

func TestInstallWindowsNeedsAdminOrUserFlag(t *testing.T) {
	f := newFakeWin(t, false)
	_, err := doInstallWindows(testEnv(t), winCfg(t, common{}), f.w)
	if !errors.Is(err, errWindowsNeedsAdmin) || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("应提示需要管理员或 --user: %v", err)
	}
	if len(f.cmds) != 0 {
		t.Fatalf("失败时不应有任何副作用: %v", f.cmds)
	}
	if _, statErr := os.Stat(filepath.Join(f.w.localApp, "RelayMesh")); statErr == nil {
		t.Fatal("失败时不应创建身份目录")
	}
}

func TestInstallWindowsElevatedUserFlagUsesUserDirectories(t *testing.T) {
	// 管理员为自己装用户级任务：任务以非提升令牌运行，两边各自推导的默认目录不同，
	// 所以必须显式把数据目录写进任务参数，保证任务用的身份就是安装时打印的那个。
	f := newFakeWin(t, true)
	res, err := doInstallWindows(testEnv(t), winCfg(t, common{user: true}), f.w)
	if err != nil {
		t.Fatal(err)
	}
	if !res.User || !strings.HasPrefix(res.DataDir, f.w.localApp) {
		t.Fatalf("应使用用户目录: %+v", res)
	}
	doc := parseTaskXML(t, f.xmls[0])
	if !strings.Contains(doc.Exec.Args, "--data-dir") {
		t.Fatalf("必须显式写入 --data-dir: %s", doc.Exec.Args)
	}
}

func TestInstallWindowsRejectsTemporaryIdentity(t *testing.T) {
	f := newFakeWin(t, true)
	if _, err := doInstallWindows(testEnv(t), winCfg(t, common{temp: true}), f.w); err == nil {
		t.Fatal("临时身份不能安装为常驻任务")
	}
}

func TestInstallWindowsReportsCreateFailureWithOutput(t *testing.T) {
	f := newFakeWin(t, false)
	f.failOn["schtasks /Create"] = errors.New("exit status 1")
	f.outOn["schtasks /Create"] = "错误: 拒绝访问。"
	_, err := doInstallWindows(testEnv(t), winCfg(t, common{user: true}), f.w)
	if err == nil || !strings.Contains(err.Error(), "拒绝访问") || !strings.Contains(err.Error(), "创建计划任务失败") {
		t.Fatalf("应带上 schtasks 的原始输出: %v", err)
	}
}

func TestInstallWindowsReinstallStopsOldInstanceFirst(t *testing.T) {
	f := newFakeWin(t, false)
	c := winCfg(t, common{user: true})
	if _, err := doInstallWindows(testEnv(t), c, f.w); err != nil {
		t.Fatal(err)
	}
	f.cmds, f.xmls = nil, nil
	// 新版本可执行文件；旧实例仍在运行，/End 之后才退出。
	if err := os.WriteFile(f.w.exe, append(makePE(0x20b, peSubsystemCUI), []byte("v2")...), 0o755); err != nil {
		t.Fatal(err)
	}
	c.server = "https://other.example.com:8443"
	res, err := doInstallWindows(testEnv(t), c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.cmds[0], "schtasks /End") {
		t.Fatalf("重装必须先停掉旧实例（否则无法替换正在运行的可执行文件）: %v", f.cmds)
	}
	got, _ := os.ReadFile(res.BinPath)
	if !strings.HasSuffix(string(got), "v2") {
		t.Fatal("重装应更新可执行文件")
	}
	if !strings.Contains(f.xmls[0], "other.example.com") {
		t.Fatal("重装应更新任务定义")
	}
}

func TestInstallWindowsFailsIfOldInstanceWontExit(t *testing.T) {
	f := newFakeWin(t, false)
	c := winCfg(t, common{user: true})
	if _, err := doInstallWindows(testEnv(t), c, f.w); err != nil {
		t.Fatal(err)
	}
	gui := filepath.Join(f.w.localApp, "Programs", "RelayMesh Agent", "relaymesh-agentw.exe")
	f.w.procs = func(image string) []int {
		if image == gui {
			return []int{99} // /End 之后进程仍然存在
		}
		return nil
	}
	f.cmds = nil
	if _, err := doInstallWindows(testEnv(t), c, f.w); err == nil || !strings.Contains(err.Error(), "没有退出") {
		t.Fatalf("旧进程不退出时应明确报错而不是覆盖失败: %v", err)
	}
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "schtasks /Create") {
			t.Fatal("旧进程没退出时不应继续创建任务")
		}
	}
}

func TestUninstallWindows(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	c := winCfg(t, common{user: true})
	res, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	f.cmds = nil
	ur, err := doUninstallWindows(env, c, false, resolved{}, f.w)
	if err != nil {
		t.Fatal(err)
	}
	if !ur.Found {
		t.Fatal("应发现已安装的任务")
	}
	var seq []string
	for _, cmd := range f.cmds {
		seq = append(seq, strings.Fields(cmd)[1])
	}
	// Agent 任务：查询 → 停止 → 删除；随后还要查一次守护进程任务（单任务形态下它不存在，什么都不做），
	// 以及远程卸载留下的一次性任务（手动卸载时同样不存在，只查询）。
	if strings.Join(seq, ",") != "/Query,/End,/Delete,/Query,/Query" {
		t.Fatalf("卸载应先查询、停止、再删除: %v", f.cmds)
	}
	for _, p := range []string{res.BinPath, res.TaskBinPath} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("应已删除 %s", p)
		}
	}
	if _, err := agent.LoadIdentity(res.DataDir); err != nil {
		t.Fatalf("不带 --purge 应保留身份: %v", err)
	}

	// 再装再卸，这次 --purge。
	if _, err := doInstallWindows(env, c, f.w); err != nil {
		t.Fatal(err)
	}
	if _, err := doUninstallWindows(env, c, true, resolved{}, f.w); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadIdentity(res.DataDir); !errors.Is(err, agent.ErrNoIdentity) {
		t.Fatalf("--purge 应删除身份: %v", err)
	}
	if _, err := os.Stat(res.DataDir); err == nil {
		t.Error("--purge 之后空的数据目录应被清理")
	}
}

func TestUninstallWindowsSchedulesCleanupForLockedBinaries(t *testing.T) {
	// 直接运行已安装目录里的 exe 来卸载自己：正在运行的文件只能改名，必须安排退出后清理，而不是留下 .old 垃圾。
	f := newFakeWin(t, false)
	env := testEnv(t)
	c := winCfg(t, common{user: true})
	res, err := doInstallWindows(env, c, f.w)
	if err != nil {
		t.Fatal(err)
	}
	var gotPaths, gotDirs []string
	f.w.deferCleanup = func(paths, dirs []string) error {
		gotPaths, gotDirs = paths, dirs
		return nil
	}
	f.w.removeFile = func(path string) (bool, bool) {
		if path == res.BinPath { // 控制台版正被自己占用
			return true, true
		}
		return removeOrMoveAside(path)
	}
	ur, err := doUninstallWindows(env, c, false, resolved{}, f.w)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotPaths) != 1 || gotPaths[0] != res.BinPath+".old" || len(gotDirs) != 1 || gotDirs[0] != filepath.Dir(res.BinPath) {
		t.Fatalf("应把被占用文件的 .old 与程序目录交给退出后清理: paths=%v dirs=%v", gotPaths, gotDirs)
	}
	if !ur.Deferred || !strings.Contains(strings.Join(ur.Removed, "\n"), "自动清理") {
		t.Fatalf("结果应说明稍后自动清理: %+v", ur)
	}

	// 安排失败（或不支持）时，退回到「下次安装 / 卸载时清理」的提示，并且不声称已安排。
	f2 := newFakeWin(t, false)
	res2, err := doInstallWindows(env, c, f2.w)
	if err != nil {
		t.Fatal(err)
	}
	f2.w.deferCleanup = func(_, _ []string) error { return errors.New("boom") }
	f2.w.removeFile = func(path string) (bool, bool) {
		if path == res2.BinPath {
			return true, true
		}
		return removeOrMoveAside(path)
	}
	ur2, err := doUninstallWindows(env, c, false, resolved{}, f2.w)
	if err != nil {
		t.Fatal(err)
	}
	if ur2.Deferred || !strings.Contains(strings.Join(ur2.Removed, "\n"), "下次安装或卸载时清理") {
		t.Fatalf("安排失败时应给出兜底提示: %+v", ur2)
	}
}

func TestUninstallWindowsNothingInstalled(t *testing.T) {
	f := newFakeWin(t, false)
	res, err := doUninstallWindows(testEnv(t), winCfg(t, common{user: true}), false, resolved{}, f.w)
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || len(res.Removed) != 0 {
		t.Fatalf("什么都没装时不应报告删除了东西: %+v", res)
	}
}

func TestUninstallWindowsWithoutAdminTargetsUserScope(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	if _, err := doInstallWindows(env, winCfg(t, common{user: true}), f.w); err != nil {
		t.Fatal(err)
	}
	ur, err := doUninstallWindows(env, common{}, false, resolved{}, f.w) // 没写 --user，也没有管理员权限
	if err != nil || !ur.Found {
		t.Fatalf("非管理员卸载应默认处理用户级: found=%v err=%v", ur.Found, err)
	}
}

func TestRemoveOrMoveAside(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.exe")
	if removed, moved := removeOrMoveAside(p); removed || moved {
		t.Fatal("不存在的文件不应报告已删除")
	}
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if removed, moved := removeOrMoveAside(p); !removed || moved {
		t.Fatalf("普通文件应被直接删除: removed=%v moved=%v", removed, moved)
	}
}

func TestReportWindows(t *testing.T) {
	f := newFakeWin(t, false)
	env := testEnv(t)
	reportWindows(env, f.w)
	if out := env.Stdout.(*syncBuffer).String(); !strings.Contains(out, "未安装") {
		t.Fatalf("没有任务时应提示未安装: %q", out)
	}
	if _, err := doInstallWindows(env, winCfg(t, common{user: true}), f.w); err != nil {
		t.Fatal(err)
	}
	env2 := testEnv(t)
	reportWindows(env2, f.w)
	out := env2.Stdout.(*syncBuffer).String()
	if !strings.Contains(out, "用户级任务") || !strings.Contains(out, "RelayMeshAgent-HOST_alice") || !strings.Contains(out, "PID 4242") {
		t.Fatalf("应报告用户级任务与运行中的 PID: %q", out)
	}
}
