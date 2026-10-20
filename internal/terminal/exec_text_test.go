package terminal

import (
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termexec"
)

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name, in string
		gone     []string // 必须消失的敏感片段
		keep     []string // 必须保留的非敏感片段
	}{
		{"auth header", `curl -H "Authorization: Bearer abc123SECRETxyz" https://x`, []string{"abc123SECRETxyz"}, []string{"curl", "https://x"}},
		{"key=value", `export DB_PASSWORD=hunter2 && run`, []string{"hunter2"}, []string{"DB_PASSWORD", "run"}},
		{"quoted value", `API_KEY="s3cr3t value here" go test`, []string{"s3cr3t value here"}, []string{"go test"}},
		{"cli flag", `mysql --password hunter2 -u root`, []string{"hunter2"}, []string{"-u root"}},
		{"cli flag eq", `tool --token=tok_ABCDEFG1234567 run`, []string{"tok_ABCDEFG1234567"}, []string{"run"}},
		{"url credential", `git clone https://bob:pa55w0rd@example.com/r.git`, []string{"pa55w0rd"}, []string{"bob", "example.com/r.git"}},
		{"github token", `echo ghp_abcdefghijklmnopqrstuvwxyz0123456789`, []string{"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}, []string{"echo"}},
		{"aws key", `AKIAABCDEFGHIJKLMNOP is the id`, []string{"AKIAABCDEFGHIJKLMNOP"}, []string{"is the id"}},
		{"own mcp token", `x rm-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef y`, []string{"0123456789abcdef0123456789abcdef"}, []string{"x ", " y"}},
		{"jwt", `t=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.abcdefgh12345`, []string{"eyJhbGciOiJIUzI1NiJ9"}, nil},
		{"pem block", "cat <<EOF\n-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nabc\n-----END RSA PRIVATE KEY-----\nEOF", []string{"MIIEow"}, []string{"cat <<EOF", "EOF"}},
		{"plain", `ls -la /var/log && git status`, nil, []string{"ls -la /var/log && git status"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactSecrets(c.in)
			for _, g := range c.gone {
				if strings.Contains(got, g) {
					t.Errorf("secret %q survived in %q", g, got)
				}
			}
			for _, k := range c.keep {
				if !strings.Contains(got, k) {
					t.Errorf("benign %q lost in %q", k, got)
				}
			}
		})
	}
}

func TestCleanOutput(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ansi color", "\x1b[31mred\x1b[0m ok", "red ok"},
		{"ansi cursor", "a\x1b[2K\x1b[1Gb", "ab"},
		{"osc title", "x\x1b]0;evil title\x07y", "xy"},
		{"osc st", "x\x1b]8;;http://e\x1b\\link\x1b]8;;\x1b\\y", "xlinky"},
		{"crlf", "a\r\nb\rc", "a\nb\nc"},
		{"control", "a\x00b\x07c\x08d", "abcd"},
		{"bidi override", "safe\u202eevil", "safeevil"},
		{"zero width", "a\u200bb", "ab"},
		{"invalid utf8", "ok\xffbad", "ok\uFFFDbad"},
		{"keeps tab newline", "a\tb\nc", "a\tb\nc"},
		{"chinese", "你好，世界", "你好，世界"},
		{"c1 csi", "a\u009b31mb", "ab"},
	}
	for _, c := range cases {
		if got := cleanOutput([]byte(c.in)); got != c.want {
			t.Errorf("%s: cleanOutput(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	if got := cleanOutput([]byte("a\n\n\n\n\n\n\nb")); got != "a\n\n\nb" {
		t.Errorf("blank-line run not collapsed: %q", got)
	}
}

func TestPreviewTextBoundaries(t *testing.T) {
	zh := []byte("你好世界")              // 12 bytes
	head := previewText(zh[:7], true) // 切在第三个字中间
	if head != "你好" {
		t.Fatalf("head with partial rune = %q, want 你好", head)
	}
	tail := previewText(zh[1:], false) // 起点是续字节
	if !strings.HasSuffix(tail, "好世界") || strings.Contains(tail, "\uFFFD") {
		t.Fatalf("tail with leading continuation = %q", tail)
	}
	if got := previewText([]byte("token=abc123 done"), true); strings.Contains(got, "abc123") {
		t.Fatalf("preview must be redacted, got %q", got)
	}
}

func TestTrimIncompleteTail(t *testing.T) {
	b := []byte("ab你")
	kept, dropped := trimIncompleteTail(b[:3]) // 「你」只有第 1 个字节
	if string(kept) != "ab" || dropped != 1 {
		t.Fatalf("kept=%q dropped=%d", kept, dropped)
	}
	kept, dropped = trimIncompleteTail(b[:4]) // 只有前 2 个字节
	if string(kept) != "ab" || dropped != 2 {
		t.Fatalf("kept=%q dropped=%d", kept, dropped)
	}
	kept, dropped = trimIncompleteTail(b)
	if string(kept) != "ab你" || dropped != 0 {
		t.Fatalf("complete input should be untouched, got %q %d", kept, dropped)
	}
}

func TestCommandNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"cd /srv/app && git pull --rebase | tee out.log", []string{"cd", "git", "tee"}},
		{"FOO=1 BAR=2 /usr/bin/python3 -c 'import os; print(1)'", []string{"python3"}},
		{`echo "a; b" ; ls`, []string{"echo", "ls"}},
		{"ls\nls\npwd", []string{"ls", "pwd"}},
		{"", nil},
		{"C:\\Windows\\System32\\whoami.exe /all", []string{"whoami.exe"}},
		// shell 流程控制关键字不是命令：循环头整段跳过，do / then / while 之类跳过关键字取后面的命令。
		{"for i in $(seq 1 40); do echo \"步骤 $i\"; sleep 2; done; echo 完成", []string{"echo", "sleep"}},
		{"if [ -f x ]; then cat x; else echo none; fi", []string{"cat", "echo"}},
		{"while read l; do grep foo \"$l\"; done < list", []string{"read", "grep"}},
		{"time make -j4 && ! false", []string{"make", "false"}},
	}
	for _, c := range cases {
		got := commandNames(c.in)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("commandNames(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	long := strings.Repeat("a;", 30)
	if n := len(commandNames(long)); n != 1 {
		t.Errorf("duplicates must collapse, got %d", n)
	}
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, "cmd"+strings.Repeat("x", i))
	}
	if n := len(commandNames(strings.Join(many, ";"))); n != 8 {
		t.Errorf("name list must be capped at 8, got %d", n)
	}
}

func TestThreadKey(t *testing.T) {
	exec := termexec.Caller{CredentialID: 3, Permissions: model.Permissions{Terminal: true}}
	hybrid := termexec.Caller{CredentialID: 3, Permissions: model.Permissions{Terminal: true, Feedback: true}}

	a := threadKeyFor(exec, "m1", "deploy", "wf-1")
	if a != threadKeyFor(exec, "m1", "deploy", "wf-2") {
		t.Errorf("ExecMesh tier must ignore workflow_id")
	}
	if a == threadKeyFor(exec, "m1", "other", "") || a == threadKeyFor(exec, "m2", "deploy", "") {
		t.Errorf("thread and machine must separate keys")
	}
	if a == threadKeyFor(termexec.Caller{CredentialID: 4, Permissions: exec.Permissions}, "m1", "deploy", "") {
		t.Errorf("different credentials must not share a thread")
	}
	h1 := threadKeyFor(hybrid, "m1", "x", "wf-1")
	if h1 != threadKeyFor(hybrid, "m1", "y", "wf-1") || h1 != threadKeyFor(termexec.Caller{CredentialID: 9, Permissions: hybrid.Permissions}, "m1", "", "wf-1") {
		t.Errorf("hybrid keys must be per workflow x host regardless of credential or thread label")
	}
	if h1 == threadKeyFor(hybrid, "m1", "x", "") {
		t.Errorf("hybrid with and without workflow must differ")
	}
	if len(a) != 24 {
		t.Errorf("key length = %d", len(a))
	}
}

func TestResolveExec(t *testing.T) {
	for _, on := range []string{"on", "ON", " true ", "1", "yes"} {
		if v, w := ResolveExec(on); !v || w != "" {
			t.Errorf("%q should enable, got %v %q", on, v, w)
		}
	}
	for _, off := range []string{"", "off", "false", "0", "no"} {
		if v, w := ResolveExec(off); v || w != "" {
			t.Errorf("%q should disable silently, got %v %q", off, v, w)
		}
	}
	if v, w := ResolveExec("maybe"); v || w == "" {
		t.Errorf("unknown value must fail closed with a warning, got %v %q", v, w)
	}
}
