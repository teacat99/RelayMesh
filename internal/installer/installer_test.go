package installer

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testTicket = "0123456789abcdef0123456789abcdef"
)

func testSPKI() string {
	sum := sha256.Sum256([]byte("spki"))
	return "sha256//" + base64.StdEncoding.EncodeToString(sum[:])
}

func TestEmbeddedScriptsCarryExactlyOneMarker(t *testing.T) {
	for _, name := range []string{ScriptSh, ScriptPS1} {
		raw, err := RawScript(name)
		if err != nil {
			t.Fatal(err)
		}
		if n := bytes.Count(raw, []byte(preludeMarker)); n != 1 {
			t.Errorf("%s 中的前导标记应恰好出现一次，实际 %d 次", name, n)
		}
	}
}

func TestPowerShellScriptKeepsBOM(t *testing.T) {
	// PowerShell 5.1 依赖 UTF-8 BOM 正确显示中文；内嵌的正本与渲染后的输出都不能丢掉它。
	raw, _ := RawScript(ScriptPS1)
	if !bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("install.ps1 正本必须带 UTF-8 BOM")
	}
	out, err := Render(ScriptPS1, Prelude{Ticket: testTicket})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("渲染后的 install.ps1 丢失了 UTF-8 BOM")
	}
}

func TestRenderShellScript(t *testing.T) {
	raw, _ := RawScript(ScriptSh)
	out, err := Render(ScriptSh, Prelude{Ticket: testTicket, SPKIPin: testSPKI()})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "RELAYMESH_HUB_TICKET='"+testTicket+"'\n") {
		t.Error("前导里应有票据赋值")
	}
	if !strings.Contains(text, "RELAYMESH_HUB_SPKI='"+testSPKI()+"'\n") {
		t.Error("前导里应有公钥指纹赋值")
	}
	if strings.Contains(text, preludeMarker) {
		t.Error("标记行应被前导整行替换")
	}
	// 除了标记那一行，其余内容必须与正本逐字节一致。
	oldLines := strings.Split(string(raw), "\n")
	var kept []string
	for _, l := range oldLines {
		if !strings.Contains(l, preludeMarker) {
			kept = append(kept, l)
		}
	}
	for _, l := range kept {
		if !strings.Contains(text, l) {
			t.Fatalf("渲染结果丢失了正本中的一行: %q", l)
		}
	}

	// 渲染结果必须是合法的 POSIX sh。
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("没有 sh，跳过语法检查")
	}
	cmd := exec.Command(sh, "-n")
	cmd.Stdin = bytes.NewReader(out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n 失败: %v\n%s", err, msg)
	}
}

func TestRenderShellScriptWithoutSPKI(t *testing.T) {
	out, err := Render(ScriptSh, Prelude{Ticket: testTicket})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "RELAYMESH_HUB_SPKI=''\n") {
		t.Error("没有内置 HTTPS 时，指纹应写成空串")
	}
}

func TestRenderPowerShellScript(t *testing.T) {
	out, err := Render(ScriptPS1, Prelude{Ticket: testTicket, SPKIPin: testSPKI()})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "$RelayMeshHub.Ticket = '"+testTicket+"'\n") {
		t.Error("前导里应有票据赋值")
	}
	// PowerShell 用证书哈希（来自 --pin）校验，不需要公钥指纹，也不应把它写进脚本。
	if strings.Contains(text, "sha256//") {
		t.Error("PowerShell 脚本不应带公钥指纹")
	}
	if strings.Contains(text, preludeMarker) {
		t.Error("标记行应被前导整行替换")
	}
}

func TestRenderRejectsAnythingThatCouldInjectScript(t *testing.T) {
	bad := []Prelude{
		{Ticket: ""},
		{Ticket: "ABCDEF0123456789ABCDEF0123456789"},    // 只接受小写
		{Ticket: testTicket[:31]},                       // 太短
		{Ticket: testTicket + "0"},                      // 太长
		{Ticket: testTicket[:30] + "';"},                // 引号与分号
		{Ticket: testTicket[:30] + "\n#"},               // 换行
		{Ticket: testTicket[:30] + "$("},                // 命令替换
		{Ticket: testTicket, SPKIPin: "sha256//short="}, // 指纹长度不对
		{Ticket: testTicket, SPKIPin: testSPKI() + "'"}, // 末尾带引号
		{Ticket: testTicket, SPKIPin: "sha1//" + testSPKI()[8:]},
		{Ticket: testTicket, SPKIPin: testSPKI()[:len(testSPKI())-1] + ";"},
	}
	for i, p := range bad {
		for _, name := range []string{ScriptSh, ScriptPS1} {
			if _, err := Render(name, p); err == nil {
				t.Errorf("用例 %d（%s）应被拒绝: %+v", i, name, p)
			}
		}
	}
	if _, err := Render("evil.sh", Prelude{Ticket: testTicket}); err == nil {
		t.Error("未知脚本名应被拒绝")
	}
}

func TestReplaceMarkerLineEdgeCases(t *testing.T) {
	cases := []struct {
		name, src, want string
		wantErr         bool
	}{
		{"首行", "# @relaymesh-prelude@\nrest\n", "X\nrest\n", false},
		{"中间行", "a\n# @relaymesh-prelude@\nb\n", "a\nX\nb\n", false},
		{"末行无换行", "a\n# @relaymesh-prelude@", "a\nX", false},
		{"没有标记", "a\nb\n", "", true},
	}
	for _, tc := range cases {
		got, err := replaceMarkerLine([]byte(tc.src), "X")
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: 应报错", tc.name)
			}
			continue
		}
		if err != nil || string(got) != tc.want {
			t.Errorf("%s: got %q err=%v, want %q", tc.name, got, err, tc.want)
		}
	}
}

// ---------- 分发目录 ----------

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDistListFiltersToWhitelistedRegularFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "relaymesh-agent-linux-amd64", "linux")
	writeFile(t, dir, "relaymesh-agent-windows-amd64.exe", "windows")
	writeFile(t, dir, "relaymesh-agent-darwin-arm64", "darwin")
	writeFile(t, dir, "relaymesh-agent-freebsd-amd64", "not whitelisted platform")
	writeFile(t, dir, "relaymesh-agent-linux-amd64.sig", "wrong suffix")
	writeFile(t, dir, "notes.txt", "other")
	writeFile(t, dir, "relaymesh-agent-linux-arm64", "") // 空文件不分发
	if err := os.Mkdir(filepath.Join(dir, "relaymesh-agent-linux-386"), 0o755); err != nil {
		t.Fatal(err)
	}

	d := NewDist(dir)
	var names []string
	for _, a := range d.List() {
		names = append(names, a.Name)
	}
	want := []string{"relaymesh-agent-darwin-arm64", "relaymesh-agent-linux-amd64", "relaymesh-agent-windows-amd64.exe"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("List = %v, want %v", names, want)
	}
	if got := strings.Join(d.Platforms(), ","); got != "darwin-arm64,linux-amd64,windows-amd64" {
		t.Errorf("Platforms = %s", got)
	}
	if !d.Available() {
		t.Error("应判定为可用")
	}
}

func TestDistFollowsOperatorSymlinks(t *testing.T) {
	src := t.TempDir()
	dir := t.TempDir()
	target := writeFile(t, src, "built-agent", "real binary")
	if err := os.Symlink(target, filepath.Join(dir, "relaymesh-agent-linux-amd64")); err != nil {
		t.Skip("无法创建符号链接: ", err)
	}
	f, st, err := NewDist(dir).Open("relaymesh-agent-linux-amd64")
	if err != nil {
		t.Fatalf("部署方做的符号链接应可分发: %v", err)
	}
	defer f.Close()
	if st.Size() != int64(len("real binary")) {
		t.Errorf("size = %d", st.Size())
	}
}

func TestDistEmptyOrMissing(t *testing.T) {
	cases := map[string]*Dist{
		"nil":      nil,
		"空路径":      NewDist(""),
		"目录不存在":    NewDist(filepath.Join(t.TempDir(), "nope")),
		"目录里没有可分发": NewDist(t.TempDir()),
	}
	for name, d := range cases {
		if d.Available() {
			t.Errorf("%s: 不应可用", name)
		}
		if _, _, err := d.Open("relaymesh-agent-linux-amd64"); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: Open 应返回 ErrNotFound，实际 %v", name, err)
		}
		if _, err := d.Checksums(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: Checksums 应返回 ErrNotFound，实际 %v", name, err)
		}
	}
}

// VERSION 与 REV 共用同一套「读第一行 + 校验 + 按大小与修改时间缓存」的实现；两者互不串扰，非法内容一律当作没有。
func TestDistVersionAndRevFiles(t *testing.T) {
	dir := t.TempDir()
	d := NewDist(dir)
	if d.Version() != "" || d.Rev() != "" {
		t.Fatalf("没有文件时应为空: %q %q", d.Version(), d.Rev())
	}

	writeFile(t, dir, VersionFileName, "v1.6.3-dev-20261016-c26a452\nignored second line\n")
	if got := d.Version(); got != "v1.6.3-dev-20261016-c26a452" {
		t.Fatalf("只取第一行并去掉空白: %q", got)
	}
	if d.Rev() != "" {
		t.Fatalf("只写了 VERSION 时 Rev 应为空: %q", d.Rev())
	}

	rev := writeFile(t, dir, RevFileName, "  72ae54bd8799 \r\nextra\n")
	if got := d.Rev(); got != "72ae54bd8799" {
		t.Fatalf("REV 应取第一行并去掉空白: %q", got)
	}
	if got := d.Version(); got != "v1.6.3-dev-20261016-c26a452" {
		t.Fatalf("写 REV 不应影响 Version: %q", got)
	}

	// 内容变了（大小不同）：缓存失效，读到新值。
	writeFile(t, dir, RevFileName, "0123456789abcdef0123\n")
	if got := d.Rev(); got != "0123456789abcdef0123" {
		t.Fatalf("文件变化后应读到新值: %q", got)
	}
	// 大小一样但修改时间变了：同样刷新。
	writeFile(t, dir, RevFileName, "abcdefabcdefabcdef01\n")
	if err := os.Chtimes(rev, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := d.Rev(); got != "abcdefabcdefabcdef01" {
		t.Fatalf("修改时间变化后应读到新值: %q", got)
	}

	// 不合规的 REV：大写、过短、过长、带路径或奇怪字符、空文件、超大文件，一律当作没有。
	bad := []struct{ name, content string }{
		{"大写", "72AE54BD8799\n"},
		{"过短", "abc12\n"},
		{"过长", strings.Repeat("a", 41) + "\n"},
		{"带路径", "../../etc/passwd\n"},
		{"带空格", "72ae 54bd8799\n"},
		{"空文件", ""},
		{"只有换行", "\n"},
		{"超大文件", strings.Repeat("a", 2048)},
		{"首行为空", "\n72ae54bd8799\n"},
		{"含控制字符", "72ae54bd\x008799\n"},
	}
	base := time.Now().Add(2 * time.Hour)
	for i, c := range bad {
		writeFile(t, dir, RevFileName, c.content)
		// 缓存按「大小 + 修改时间」判断；逐个用例推进修改时间，保证每个用例读到的都是它自己的内容。
		at := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(rev, at, at); err != nil {
			t.Fatal(err)
		}
		if got := d.Rev(); got != "" {
			t.Errorf("%s：不合规的 REV 应被忽略，得到 %q", c.name, got)
		}
	}
	// 非法内容之后再写回合法内容，应立即恢复（非法结果不会被「永久缓存」）。
	writeFile(t, dir, RevFileName, "72ae54bd8799\n")
	if err := os.Chtimes(rev, base.Add(time.Hour), base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := d.Rev(); got != "72ae54bd8799" {
		t.Errorf("恢复合法内容后应读到新值: %q", got)
	}

	// REV 与 VERSION 都不是可分发的文件。
	writeFile(t, dir, "relaymesh-agent-linux-amd64", "bin")
	for _, name := range []string{RevFileName, VersionFileName} {
		if f, _, err := d.Open(name); err == nil {
			_ = f.Close()
			t.Errorf("%s 不应可被 Open", name)
		}
	}
	var nilDist *Dist
	if nilDist.Version() != "" || nilDist.Rev() != "" {
		t.Fatal("nil Dist 应安全地返回空")
	}
}

func TestDistOpenRejectsTraversalAndStrangers(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "dist")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "relaymesh-agent-linux-amd64", "ok")
	writeFile(t, parent, "secret", "must not leak")
	writeFile(t, dir, "secret.txt", "also not served")

	d := NewDist(dir)
	for _, name := range []string{
		"../secret", "..", ".", "", "/etc/passwd", `..\secret`,
		"relaymesh-agent-linux-amd64/../../secret", "relaymesh-agent-linux-amd64/",
		"relaymesh-agent-linux-amd64\x00.txt", "secret.txt", "RELAYMESH-AGENT-LINUX-AMD64",
		"relaymesh-agent-linux-amd64.exe", "checksums.txt",
	} {
		if f, _, err := d.Open(name); err == nil {
			_ = f.Close()
			t.Errorf("Open(%q) 不应成功", name)
		}
	}
	f, _, err := d.Open("relaymesh-agent-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestDistChecksumsFormatAndRefresh(t *testing.T) {
	dir := t.TempDir()
	linux := writeFile(t, dir, "relaymesh-agent-linux-amd64", "first")
	writeFile(t, dir, "relaymesh-agent-windows-amd64.exe", "win")
	d := NewDist(dir)

	got, err := d.Checksums()
	if err != nil {
		t.Fatal(err)
	}
	h1 := sha256.Sum256([]byte("first"))
	h2 := sha256.Sum256([]byte("win"))
	want := hex.EncodeToString(h1[:]) + "  relaymesh-agent-linux-amd64\n" +
		hex.EncodeToString(h2[:]) + "  relaymesh-agent-windows-amd64.exe\n"
	if string(got) != want {
		t.Fatalf("checksums.txt =\n%s\nwant\n%s", got, want)
	}

	// 部署方替换了二进制：大小或修改时间变化后必须重新计算，不能继续发过期的校验值。
	if err := os.WriteFile(linux, []byte("second-version"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(linux, later, later); err != nil {
		t.Fatal(err)
	}
	got2, err := d.Checksums()
	if err != nil {
		t.Fatal(err)
	}
	h3 := sha256.Sum256([]byte("second-version"))
	if !strings.Contains(string(got2), hex.EncodeToString(h3[:])+"  relaymesh-agent-linux-amd64\n") {
		t.Fatalf("替换后应给出新的校验值:\n%s", got2)
	}
	if strings.Contains(string(got2), hex.EncodeToString(h1[:])) {
		t.Fatal("不应继续给出旧的校验值")
	}
}

func TestResolveDirExplicitWins(t *testing.T) {
	if got := ResolveDir("  /somewhere/else  "); got != "/somewhere/else" {
		t.Errorf("显式配置应原样采用（去掉空白），实际 %q", got)
	}
}

func TestResolveDirFallsBackToWorkingDirectory(t *testing.T) {
	if st, err := os.Stat("/app/agent-dist"); err == nil && st.IsDir() {
		t.Skip("本机存在 /app/agent-dist，优先级更高")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "agent-dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got := ResolveDir(""); got != "agent-dist" {
		t.Errorf("应回退到工作目录下的 agent-dist，实际 %q", got)
	}
	t.Chdir(t.TempDir())
	if got := ResolveDir(""); got != "" {
		t.Errorf("什么都没有时应为空，实际 %q", got)
	}
}
