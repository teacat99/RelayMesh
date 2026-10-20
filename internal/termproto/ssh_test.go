package termproto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	secretPassword = "p@ss-DO-NOT-LEAK-7f3a"
	secretKeyBody  = "-----BEGIN OPENSSH PRIVATE KEY-----\nDO-NOT-LEAK-KEY-BODY\n-----END OPENSSH PRIVATE KEY-----"
	secretPhrase   = "phrase-DO-NOT-LEAK"
)

func secretTarget() SSHTarget {
	return SSHTarget{
		ID: "RM-ABCD-EFGH-JKMN-PQRS", Addr: "10.0.0.5:22", User: "ops", Auth: "password",
		Password: secretPassword, PrivateKey: secretKeyBody, Passphrase: secretPhrase, HostKey: "AAAAC3NzaC1lZDI1NTE5",
	}
}

// 凭据不能经任何常见的格式化动词出现在文本里（日志、错误、调试输出）。
func TestSSHTargetNeverPrintsCredentials(t *testing.T) {
	tg := secretTarget()
	op := SSHOp{T: TSSHOp, Req: "r1", Op: SSHOpCheck, Target: tg}
	open := Open{T: TOpen, Req: "r2", SSH: &tg}

	var outs []string
	for _, v := range []any{tg, &tg, op, &op} {
		outs = append(outs,
			fmt.Sprint(v), fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%s", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%q", v))
	}
	// 含指针字段的 open 帧：打印指针本身不应展开内容。
	outs = append(outs, fmt.Sprintf("%v", open.SSH), fmt.Sprintf("%+v", open.SSH), fmt.Sprintf("%#v", open.SSH))

	for _, s := range outs {
		for _, bad := range []string{secretPassword, "DO-NOT-LEAK", secretPhrase} {
			if strings.Contains(s, bad) {
				t.Fatalf("格式化输出泄露了凭据 %q：%s", bad, s)
			}
		}
	}
	if got := tg.String(); got != "ssh://ops@10.0.0.5:22" {
		t.Fatalf("String = %q", got)
	}
}

// 线上传输的 JSON 必须带齐凭据（这是它存在的目的），但字段名要稳定。
func TestOpenWithSSHRoundTrip(t *testing.T) {
	tg := secretTarget()
	data, err := Marshal(Open{T: TOpen, Req: "abc", Name: "prod", Rows: 30, Cols: 100, SSH: &tg})
	if err != nil {
		t.Fatal(err)
	}
	var back Open
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.SSH == nil || *back.SSH != tg {
		t.Fatalf("往返后目标不一致: %+v", back.SSH)
	}
	for _, key := range []string{`"ssh":{`, `"addr":"10.0.0.5:22"`, `"user":"ops"`, `"host_key":`, `"private_key":`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("线格式缺少 %s: %s", key, data)
		}
	}
	// 普通会话不带 ssh 字段。
	plain, _ := Marshal(Open{T: TOpen, Req: "abc"})
	if strings.Contains(string(plain), `"ssh"`) {
		t.Fatalf("普通 open 帧不应出现 ssh 字段: %s", plain)
	}
}

func TestOpenFailedCarriesCode(t *testing.T) {
	data, _ := Marshal(OpenFailed{T: TOpenFailed, Req: "r", Reason: "x", Code: SSHCodeHostKey})
	var back OpenFailed
	if err := json.Unmarshal(data, &back); err != nil || back.Code != SSHCodeHostKey {
		t.Fatalf("code 往返失败: %v %+v", err, back)
	}
	plain, _ := Marshal(OpenFailed{T: TOpenFailed, Req: "r", Reason: "x"})
	if strings.Contains(string(plain), `"code"`) {
		t.Fatalf("旧帧不应多出 code: %s", plain)
	}
}

func TestSSHResultSanitized(t *testing.T) {
	t.Run("失败只保留稳定错误码与净化后的说明", func(t *testing.T) {
		got := SSHResult{T: "evil", Req: "r\x00q", OK: false, Code: "<script>", Msg: "boom\x1b[31m\u202e" + strings.Repeat("x", 500),
			HostKey: "AAAA", Fingerprint: "SHA256:zzz"}.Sanitized()
		if got.T != TSSHResult || got.OK || got.Code != SSHCodeInternal {
			t.Fatalf("unexpected: %+v", got)
		}
		if got.HostKey != "" || got.Fingerprint != "" {
			t.Fatalf("失败结果不应携带探测数据: %+v", got)
		}
		if strings.ContainsAny(got.Msg, "\x1b\u202e") || len([]rune(got.Msg)) > maxSSHMsgRunes || strings.ContainsAny(got.Req, "\x00") {
			t.Fatalf("文本未净化: %q %q", got.Msg, got.Req)
		}
	})
	t.Run("已知错误码原样保留", func(t *testing.T) {
		for _, c := range []string{SSHCodeDisabled, SSHCodeDenied, SSHCodeBadRequest, SSHCodeUnreachable, SSHCodeAuth, SSHCodeHostKey,
			SSHCodeNoHostKey, SSHCodePassphrase, SSHCodeUnsupported, SSHCodeBusy, SSHCodeNoCred, SSHCodeExecDenied, SSHCodeStore,
			SSHCodeInternal} {
			if got := (SSHResult{Req: "r", Code: c}).Sanitized().Code; got != c {
				t.Fatalf("%s 被改成 %s", c, got)
			}
		}
	})
	t.Run("成功结果净化各字段，非法公钥被丢弃", func(t *testing.T) {
		ok := SSHResult{Req: "r", OK: true, Addr: "10.0.0.5:22", KeyType: "ssh-ed25519", HostKey: "AAAAC3Nz+/==", Fingerprint: "SHA256:abc",
			Host: "web\x00-1", OS: "Linux", Arch: "x86_64", User: "ops", Home: "/home/ops", LatencyMS: 42}.Sanitized()
		if !ok.OK || ok.HostKey != "AAAAC3Nz+/==" || ok.Host != "web-1" || ok.LatencyMS != 42 {
			t.Fatalf("unexpected: %+v", ok)
		}
		bad := SSHResult{Req: "r", OK: true, HostKey: "not base64 \x00 !!"}.Sanitized()
		if bad.HostKey != "" {
			t.Fatalf("非法公钥应被丢弃: %q", bad.HostKey)
		}
		huge := SSHResult{Req: "r", OK: true, HostKey: strings.Repeat("A", maxSSHKeyRunes+1)}.Sanitized()
		if huge.HostKey != "" {
			t.Fatal("超长公钥应被丢弃")
		}
	})
}

// 凭据留在网关：open / exec 用的目标只带 stored 标记，线格式里没有任何凭据字段。
func TestStoredTargetCarriesNoCredentials(t *testing.T) {
	tg := SSHTarget{ID: "RM-ABCD-EFGH-JKMN-PQRS", Addr: "10.0.0.5:22", User: "ops", Auth: "key", Stored: true, HostKey: "AAAAC3NzaC1lZDI1NTE5"}
	data, err := Marshal(SSHOp{T: TSSHOp, Req: "r", Op: SSHOpExec, Target: tg, Cmd: "sh", Stdin: []byte("echo hi")})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"stored":true`, `"cmd":"sh"`, `"stdin":`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("线格式缺少 %s: %s", key, data)
		}
	}
	for _, bad := range []string{`"password"`, `"private_key"`, `"passphrase"`, `"persist"`} {
		if strings.Contains(string(data), bad) {
			t.Fatalf("stored 目标不应出现 %s: %s", bad, data)
		}
	}
	// 没有 stored 的目标（登记时的 check）不多出这个字段。
	plain, _ := Marshal(secretTarget())
	if strings.Contains(string(plain), `"stored"`) {
		t.Fatalf("普通目标不应出现 stored: %s", plain)
	}
}

// 命令、标准输入与名单都不会经格式化动词外泄（标准输入里是 Hub 生成的任务脚本，含 base64 的命令文本）。
func TestSSHOpExecPayloadNeverPrinted(t *testing.T) {
	op := SSHOp{T: TSSHOp, Req: "r", Op: SSHOpExec, Target: SSHTarget{ID: "RM-ABCD-EFGH-JKMN-PQRS", Addr: "h:22", User: "ops", Stored: true},
		Cmd: "sh -c DO-NOT-LEAK-CMD", Stdin: []byte("DO-NOT-LEAK-STDIN"), Keep: []string{"DO-NOT-LEAK-KEEP"}}
	for _, v := range []any{op, &op} {
		for _, s := range []string{fmt.Sprint(v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%q", v)} {
			if strings.Contains(s, "DO-NOT-LEAK") {
				t.Fatalf("格式化输出泄露了载荷: %s", s)
			}
		}
	}
}

func TestSSHResultExecAndSyncFields(t *testing.T) {
	t.Run("exec 输出与退出码原样保留，字节不被改写", func(t *testing.T) {
		out := []byte("RM_OK\n\x00\xff binary-ish")
		got := SSHResult{Req: "r", OK: true, Stdout: out, Stderr: []byte("warn"), Exit: 3, Posix: true}.Sanitized()
		if !got.OK || string(got.Stdout) != string(out) || string(got.Stderr) != "warn" || got.Exit != 3 || !got.Posix {
			t.Fatalf("unexpected: %+v", got)
		}
	})
	t.Run("输出（两路合计）超过协议上限整个结果按失败处理，不截断", func(t *testing.T) {
		for _, r := range []SSHResult{
			{Req: "r", OK: true, Stdout: make([]byte, MaxSSHExecOut+1)},
			{Req: "r", OK: true, Stderr: make([]byte, MaxSSHExecOut+1)},
			{Req: "r", OK: true, Stdout: make([]byte, MaxSSHExecOut/2+1), Stderr: make([]byte, MaxSSHExecOut/2)},
		} {
			got := r.Sanitized()
			if got.OK || got.Code != SSHCodeInternal || len(got.Stdout) != 0 || len(got.Stderr) != 0 {
				t.Fatalf("超限应失败: %+v", got)
			}
		}
		if got := (SSHResult{Req: "r", OK: true, Stdout: make([]byte, MaxSSHExecOut)}).Sanitized(); !got.OK {
			t.Fatalf("恰好等于上限应放行: %+v", got)
		}
		if got := (SSHResult{Req: "r", OK: true, Stdout: make([]byte, MaxSSHExecOut/2), Stderr: make([]byte, MaxSSHExecOut/2)}).Sanitized(); !got.OK {
			t.Fatalf("两路合计恰好等于上限应放行: %+v", got)
		}
	})
	t.Run("满额输出的应答帧也放得进控制帧", func(t *testing.T) {
		// 最坏情况：合计恰好 MaxSSHExecOut、字节全是需要转义的值也不影响（[]byte 走 base64，膨胀恒为 4/3）。
		full := SSHResult{T: TSSHResult, Req: "0123456789abcdef", OK: true, Stdout: make([]byte, MaxSSHExecOut/2), Stderr: make([]byte, MaxSSHExecOut/2), Exit: 255}
		raw, err := json.Marshal(full)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) >= MaxControlFrame-4<<10 {
			t.Fatalf("满额应答 %d 字节，必须留出足够余量放进 %d 的控制帧", len(raw), MaxControlFrame)
		}
	})
	t.Run("cred_sync 回报只收合法且不重复的机器码", func(t *testing.T) {
		a, b := "RM-ABCD-EFGH-JKMN-PQRS", "RM-2345-6789-ABCD-EFGH"
		got := SSHResult{Req: "r", OK: true, Dropped: 2, Have: []string{a, "garbage", b, a, "<b>x</b>", ""}}.Sanitized()
		if len(got.Have) != 2 || got.Have[0] != a || got.Have[1] != b || got.Dropped != 2 {
			t.Fatalf("unexpected: %+v", got)
		}
		many := make([]string, 0, MaxSSHKeep+50)
		for i := 0; i < MaxSSHKeep+50; i++ {
			c, err := RandomMachineCode()
			if err != nil {
				t.Fatal(err)
			}
			many = append(many, c)
		}
		if got := (SSHResult{Req: "r", OK: true, Have: many}).Sanitized(); len(got.Have) > MaxSSHKeep {
			t.Fatalf("回报条数应有上限: %d", len(got.Have))
		}
	})
}

func TestSessionInfoTargetMustBeMachineCode(t *testing.T) {
	sid := strings.Repeat("a", 32)
	good, ok := SessionInfo{SID: sid, State: SessionRunning, Target: "RM-ABCD-EFGH-JKMN-PQRS"}.Sanitized()
	if !ok || good.Target != "RM-ABCD-EFGH-JKMN-PQRS" {
		t.Fatalf("合法目标被丢弃: %+v", good)
	}
	for _, bad := range []string{"rm-abcd-efgh-jkmn-pqrs", "ssh://root@host", "<b>x</b>", "RM-ABCD"} {
		got, ok := SessionInfo{SID: sid, State: SessionRunning, Target: bad}.Sanitized()
		if !ok || got.Target != "" {
			t.Fatalf("非法目标 %q 应被清空: %+v", bad, got)
		}
	}
}
