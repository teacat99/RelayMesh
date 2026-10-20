package cli

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdQuote(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "/usr/local/bin/relaymesh-agent", want: "/usr/local/bin/relaymesh-agent"},
		{in: "https://relay.example.com", want: "https://relay.example.com"},
		{in: "sha256:abcdef0123", want: "sha256:abcdef0123"},
		{in: "--pin", want: "--pin"},
		{in: "", want: `""`},
		{in: "/opt/my dir/agent", want: `"/opt/my dir/agent"`},
		{in: `say "hi"`, want: `"say \"hi\""`},
		{in: `back\slash`, want: `"back\\slash"`},
		{in: "100%done", want: `"100%%done"`},   // systemd 说明符
		{in: "cost$HOME", want: `"cost$$HOME"`}, // systemd 变量展开
		{in: "a;b", want: `"a;b"`},              // 单独的分号是命令分隔符，必须被引用
		{in: "https://x/ws?a=1&b=2", want: `"https://x/ws?a=1&b=2"`},
		{in: "line\nbreak", wantErr: true},
		{in: "tab\there", wantErr: true},
		{in: "nul\x00byte", wantErr: true},
	}
	for _, c := range cases {
		got, err := systemdQuote(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("systemdQuote(%q) 应报错，得到 %q", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("systemdQuote(%q) = %q, %v，期望 %q", c.in, got, err, c.want)
		}
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	argv := []string{"/usr/local/bin/relaymesh-agent", "run", "--server", "https://relay.example.com", "--pin", "sha256:" + strings.Repeat("ab", 32)}
	unit, err := renderSystemdUnit(argv, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[Unit]", "[Service]", "[Install]",
		"ExecStart=/usr/local/bin/relaymesh-agent run --server https://relay.example.com --pin sha256:",
		"Restart=always", "WantedBy=multi-user.target", "network-online.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("单元缺少 %q:\n%s", want, unit)
		}
	}
	// 沙箱类指令会让终端里的命令莫名失败；KillMode 必须保持默认，服务停止时连同 shell 一并终止。
	for _, bad := range []string{"ProtectSystem", "ProtectHome", "PrivateTmp", "NoNewPrivileges", "ReadOnlyPaths", "CapabilityBoundingSet", "KillMode", "User="} {
		if strings.Contains(unit, bad) {
			t.Errorf("单元不应包含 %q", bad)
		}
	}

	user, err := renderSystemdUnit(argv, true)
	if err != nil || !strings.Contains(user, "WantedBy=default.target") || strings.Contains(user, "multi-user.target") {
		t.Errorf("用户级单元应挂在 default.target 下: %v\n%s", err, user)
	}

	if _, err := renderSystemdUnit([]string{"relaymesh-agent", "run"}, false); err == nil {
		t.Error("相对路径的可执行文件必须被拒绝")
	}
	if _, err := renderSystemdUnit([]string{"/bin/x", "bad\narg"}, false); err == nil {
		t.Error("含换行的参数可能注入额外的单元指令，必须被拒绝")
	}
	if _, err := renderSystemdUnit(nil, false); err == nil {
		t.Error("空 argv 必须被拒绝")
	}
}

func TestRenderSystemdUnitInjectionAttempt(t *testing.T) {
	// 试图经由 --server 注入 [Service] 指令：换行被拒绝，引号 / 百分号 / 美元符被转义，不会形成新的单元行。
	for _, evil := range []string{
		"https://x.example.com\nExecStartPost=/bin/sh -c id",
		"https://x.example.com\rExecStartPost=/bin/sh",
	} {
		if _, err := renderSystemdUnit([]string{"/bin/agent", "run", "--server", evil}, false); err == nil {
			t.Errorf("应拒绝含换行的参数: %q", evil)
		}
	}
	unit, err := renderSystemdUnit([]string{"/bin/agent", "run", "--server", `https://x" ExecStartPost=/bin/sh "$(id)%n`}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(unit, "\nExecStart") != 1 {
		t.Errorf("不应出现第二条 ExecStart*: \n%s", unit)
	}
	if !strings.Contains(unit, `\"`) || !strings.Contains(unit, "$$(id)") || !strings.Contains(unit, "%%n") {
		t.Errorf("引号 / 美元符 / 百分号应被转义:\n%s", unit)
	}
}

// 用真实的 systemd-analyze 校验生成的单元语法（ExecStart 指向 /bin/true 以避免「文件不存在」的无关告警）。
func TestRenderedUnitPassesSystemdAnalyze(t *testing.T) {
	bin, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("没有 systemd-analyze")
	}
	unit, err := renderSystemdUnit([]string{"/bin/true", "run", "--server", "https://relay.example.com", "--pin", "sha256:" + strings.Repeat("ab", 32), "--data-dir", "/var/lib/my agent"}, false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "relaymesh-agent.service")
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "verify", path).CombinedOutput()
	text := string(out)
	if strings.Contains(text, "Failed to") && strings.Contains(text, "connect") {
		t.Skipf("systemd-analyze 在此环境不可用: %s", text)
	}
	// 只关心语法类问题；network-online.target 等依赖在 verify 环境里可能提示，不属于格式错误。
	for _, line := range strings.Split(text, "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "unknown key") || strings.Contains(l, "invalid") || strings.Contains(l, "unbalanced") || strings.Contains(l, "failed to parse") || strings.Contains(l, "missing") && strings.Contains(l, "=") {
			t.Fatalf("systemd-analyze 报告单元有问题: %s\n--- 单元 ---\n%s", line, unit)
		}
	}
	if err != nil && strings.Contains(text, relaymeshUnitFile) {
		t.Logf("systemd-analyze 提示（非语法问题）: %s", text)
	}
}

const relaymeshUnitFile = "relaymesh-agent.service"

func TestRenderLaunchdPlist(t *testing.T) {
	argv := []string{"/usr/local/bin/relaymesh", "agent", "run", "--server", "https://relay.example.com?a=1&b=<2>"}
	plist, err := renderLaunchdPlist(argv, "/var/log/relaymesh-agent.log")
	if err != nil {
		t.Fatal(err)
	}
	// 必须是良构的 XML，且特殊字符被转义（否则 launchd 会拒绝加载，或解析成别的参数）。
	dec := xml.NewDecoder(strings.NewReader(plist))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity
	var strs []string
	inString := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("plist 不是良构的 XML: %v\n%s", err, plist)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			inString = v.Name.Local == "string"
		case xml.EndElement:
			inString = false
		case xml.CharData:
			if inString {
				strs = append(strs, string(v))
			}
		}
	}
	joined := strings.Join(strs, "|")
	if !strings.Contains(joined, "https://relay.example.com?a=1&b=<2>") {
		t.Errorf("转义后再解析应还原原始参数: %s", joined)
	}
	for _, want := range []string{launchdLabel, "<key>KeepAlive</key>", "<key>RunAtLoad</key>", "/var/log/relaymesh-agent.log"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist 缺少 %q", want)
		}
	}

	if _, err := renderLaunchdPlist([]string{"relative"}, ""); err == nil {
		t.Error("相对路径必须被拒绝")
	}
	if _, err := renderLaunchdPlist([]string{"/bin/x", "a\x01b"}, ""); err == nil {
		t.Error("控制字符必须被拒绝")
	}
	noLog, err := renderLaunchdPlist([]string{"/bin/x"}, "")
	if err != nil || strings.Contains(noLog, "StandardOutPath") {
		t.Errorf("未指定日志路径时不应写入 StandardOutPath: %v", err)
	}
}
