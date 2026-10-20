package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// syncBuffer 是并发安全的输出缓冲，供 Run 在后台写、测试在前台读。
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var machineCodeRE = regexp.MustCompile(`^RM-[0-9A-Z]{4}(-[0-9A-Z]{4}){3}$`)

func out(e Env) string { return e.Stdout.(*syncBuffer).String() }
func errOut(e Env) string {
	return e.Stderr.(*syncBuffer).String()
}

func runCLI(t *testing.T, args []string, mod func(*Env)) (int, Env) {
	t.Helper()
	env := testEnv(t)
	if mod != nil {
		mod(&env)
	}
	code := Run(context.Background(), args, env)
	return code, env
}

func TestRunDispatch(t *testing.T) {
	t.Run("无参数且没有环境变量：打印用法，退出码 2", func(t *testing.T) {
		code, env := runCLI(t, nil, nil)
		if code != 2 || !strings.Contains(out(env), "用法") {
			t.Fatalf("code=%d out=%q", code, out(env))
		}
	})
	t.Run("help", func(t *testing.T) {
		code, env := runCLI(t, []string{"help"}, nil)
		if code != 0 || !strings.Contains(out(env), "install") || !strings.Contains(out(env), "--pin") {
			t.Fatalf("code=%d", code)
		}
	})
	t.Run("version", func(t *testing.T) {
		code, env := runCLI(t, []string{"version"}, nil)
		if code != 0 || !strings.Contains(out(env), "1.7.0-test") {
			t.Fatalf("code=%d out=%q", code, out(env))
		}
	})
	t.Run("未知命令", func(t *testing.T) {
		code, env := runCLI(t, []string{"frobnicate"}, nil)
		if code != 2 || !strings.Contains(errOut(env), "未知命令") {
			t.Fatalf("code=%d err=%q", code, errOut(env))
		}
	})
	t.Run("未知选项", func(t *testing.T) {
		code, _ := runCLI(t, []string{"run", "--no-such-flag"}, nil)
		if code != 2 {
			t.Fatalf("code=%d", code)
		}
	})
	t.Run("多余的位置参数", func(t *testing.T) {
		code, env := runCLI(t, []string{"id", "stray"}, nil)
		if code != 2 || !strings.Contains(errOut(env), "多余") {
			t.Fatalf("code=%d err=%q", code, errOut(env))
		}
	})
}

func TestRunConfigErrors(t *testing.T) {
	t.Run("缺少 --server", func(t *testing.T) {
		code, env := runCLI(t, []string{"run", "--data-dir", t.TempDir()}, nil)
		if code != 2 || !strings.Contains(errOut(env), "--server") || !strings.Contains(errOut(env), envServer) {
			t.Fatalf("应提示如何指定服务端: code=%d err=%q", code, errOut(env))
		}
	})
	t.Run("明文地址没有 --insecure", func(t *testing.T) {
		code, env := runCLI(t, []string{"run", "--server", "http://10.0.0.5:18775", "--data-dir", t.TempDir()}, nil)
		if code != 2 || !strings.Contains(errOut(env), "明文") {
			t.Fatalf("code=%d err=%q", code, errOut(env))
		}
	})
	t.Run("非法指纹", func(t *testing.T) {
		code, env := runCLI(t, []string{"run", "--server", "https://x.example.com", "--pin", "zzz", "--data-dir", t.TempDir()}, nil)
		if code != 2 || !strings.Contains(errOut(env), "--pin") {
			t.Fatalf("code=%d err=%q", code, errOut(env))
		}
	})
	t.Run("负的会话上限", func(t *testing.T) {
		code, _ := runCLI(t, []string{"run", "--server", "https://x.example.com", "--max-sessions", "-3", "--data-dir", t.TempDir()}, nil)
		if code != 2 {
			t.Fatalf("code=%d", code)
		}
	})
	t.Run("只配环境变量也能运行（此处因明文被拒以验证确实走到了 run）", func(t *testing.T) {
		code, env := runCLI(t, nil, func(e *Env) {
			e.Getenv = func(k string) string {
				if k == envServer {
					return "http://10.0.0.5"
				}
				return ""
			}
		})
		if code != 2 || !strings.Contains(errOut(env), "明文") {
			t.Fatalf("code=%d err=%q out=%q", code, errOut(env), out(env))
		}
	})
}

func TestIDCommand(t *testing.T) {
	dir := t.TempDir()
	code, env := runCLI(t, []string{"id", "--data-dir", dir}, nil)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut(env))
	}
	first := strings.TrimSpace(out(env))
	if !machineCodeRE.MatchString(first) {
		t.Fatalf("机器码格式不对: %q", first)
	}
	// 只有机器码一行，便于脚本捕获。
	if strings.Contains(out(env), "\n") && strings.Count(out(env), "\n") != 1 {
		t.Fatalf("应只输出一行: %q", out(env))
	}
	_, env2 := runCLI(t, []string{"id", "--data-dir", dir}, nil)
	if strings.TrimSpace(out(env2)) != first {
		t.Fatal("同一目录下机器码必须稳定")
	}
	// 环境变量指定目录等价于 --data-dir。
	_, env3 := runCLI(t, []string{"id"}, func(e *Env) {
		e.Getenv = func(k string) string {
			if k == envDataDir {
				return dir
			}
			return ""
		}
	})
	if strings.TrimSpace(out(env3)) != first {
		t.Fatal("环境变量指定的目录应与 --data-dir 等价")
	}
}

func TestResetIDCommand(t *testing.T) {
	dir := t.TempDir()
	_, env := runCLI(t, []string{"id", "--data-dir", dir}, nil)
	old := strings.TrimSpace(out(env))

	// 非交互环境没有 --yes：拒绝，机器码不变。
	code, env := runCLI(t, []string{"reset-id", "--data-dir", dir}, nil)
	if code != 1 || !strings.Contains(errOut(env), "--yes") {
		t.Fatalf("非交互且无 --yes 应拒绝: code=%d err=%q", code, errOut(env))
	}
	_, env = runCLI(t, []string{"id", "--data-dir", dir}, nil)
	if strings.TrimSpace(out(env)) != old {
		t.Fatal("被拒绝的重置不应改变机器码")
	}

	code, env = runCLI(t, []string{"reset-id", "--yes", "--data-dir", dir}, nil)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut(env))
	}
	if !strings.Contains(out(env), old) {
		t.Errorf("应展示旧机器码以便管理员去 Web 清理: %q", out(env))
	}
	_, env = runCLI(t, []string{"id", "--data-dir", dir}, nil)
	if strings.TrimSpace(out(env)) == old {
		t.Fatal("重置后机器码应改变")
	}
}

func TestResolveTOFUPolicy(t *testing.T) {
	env := testEnv(t)
	base := common{server: "https://relay.example.com", dataDir: t.TempDir(), configDir: t.TempDir()}

	r, err := base.resolve(env, true)
	if err != nil || r.trust.Prompt == nil || r.trust.KnownServersPath == "" {
		t.Fatalf("交互式前台、wss、没有 pin：应启用 TOFU: %v", err)
	}
	if want := filepath.Join(base.configDir, "known_servers"); r.trust.KnownServersPath != want {
		t.Fatalf("KnownServersPath = %s", r.trust.KnownServersPath)
	}

	r, _ = base.resolve(env, false)
	if r.trust.Prompt != nil {
		t.Fatal("非交互（服务）场景不能 TOFU：没有人可以确认")
	}
	noTOFU := base
	noTOFU.noTOFU = true
	if r, _ = noTOFU.resolve(env, true); r.trust.Prompt != nil {
		t.Fatal("--no-tofu 应关闭 TOFU")
	}
	pinned := base
	pinned.pins = stringList{"sha256:" + strings.Repeat("ab", 32)}
	if r, _ = pinned.resolve(env, true); r.trust.Prompt != nil || len(r.trust.Pins) != 1 {
		t.Fatal("指定了指纹时指纹取代 TOFU")
	}
	plain := common{server: "http://10.0.0.5:18775", insecure: true, dataDir: base.dataDir, configDir: base.configDir}
	if r, err = plain.resolve(env, true); err != nil || r.trust.Prompt != nil {
		t.Fatalf("明文连接没有证书可 TOFU: %v", err)
	}
}

func TestPromptRefusesWithoutInteractiveInput(t *testing.T) {
	// Prompt 在 stdin 不是终端时必须返回 false（安全默认），而不是读到空行就放行。
	env := testEnv(t)
	c := common{server: "https://relay.example.com", dataDir: t.TempDir(), configDir: t.TempDir()}
	r, err := c.resolve(env, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.trust.Prompt("relay.example.com:443", "sha256:aa") {
		t.Fatal("非交互输入不应信任未知证书")
	}
	if !strings.Contains(out(env), "sha256:aa") {
		t.Error("提示里应展示指纹供人核对")
	}
}

func TestServiceArgsRoundTrip(t *testing.T) {
	c := common{
		server: "https://relay.example.com", pins: stringList{"sha256:aa", "sha256:bb"},
		insecure: true, dataDir: "/var/lib/x", configDir: "/etc/x", maxSessions: 4,
	}
	got := strings.Join(c.serviceArgs(), " ")
	want := "run --server https://relay.example.com --pin sha256:aa --pin sha256:bb --insecure --data-dir /var/lib/x --config-dir /etc/x --max-sessions 4"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	minimal := common{server: "https://relay.example.com"}
	if got := strings.Join(minimal.serviceArgs(), " "); got != "run --server https://relay.example.com" {
		t.Fatalf("最小参数: %s", got)
	}
}

func TestEnvDefaultsForPinsAndFlags(t *testing.T) {
	env := testEnv(t)
	env.Getenv = func(k string) string {
		return map[string]string{
			envServer:   "https://relay.example.com",
			envPin:      "sha256:aa, sha256:bb ,",
			envInsecure: "true",
			envTemp:     "1",
		}[k]
	}
	var c common
	fs := newFlagSet("run", env)
	c.bind(fs, env)
	if err := parse(fs, []string{"--pin", "sha256:cc"}); err != nil {
		t.Fatal(err)
	}
	if c.server != "https://relay.example.com" || !c.insecure || !c.temp {
		t.Fatalf("环境变量默认值未生效: %+v", c)
	}
	if strings.Join(c.pins, ",") != "sha256:aa,sha256:bb,sha256:cc" {
		t.Fatalf("环境变量与命令行的指纹应合并: %v", c.pins)
	}
	// 命令行显式值优先于环境变量。
	var c2 common
	fs2 := newFlagSet("run", env)
	c2.bind(fs2, env)
	if err := parse(fs2, []string{"--server", "https://override.example.com"}); err != nil || c2.server != "https://override.example.com" {
		t.Fatalf("命令行应覆盖环境变量: %v %s", err, c2.server)
	}
}

// 用一个脚本化的假 Hub 验证 `run` 的完整路径：身份创建 → 横幅 → 握手 → 状态日志 → 取消后干净退出。
func TestRunCommandEndToEnd(t *testing.T) {
	var (
		mu      sync.Mutex
		gotCode string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{termproto.SubprotocolAgent}, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx := r.Context()
		nonce, _ := termproto.NewNonce()
		b, _ := json.Marshal(termproto.Challenge{T: termproto.TChallenge, V: termproto.ProtocolVersion, Nonce: termproto.EncodeKey(nonce)})
		if ws.Write(ctx, websocket.MessageText, b) != nil {
			return
		}
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var h termproto.Hello
		_ = json.Unmarshal(data, &h)
		pub, _ := termproto.DecodeKey(h.Pub, 32)
		code := termproto.MachineCode(pub)
		mu.Lock()
		gotCode = code
		mu.Unlock()
		w2, _ := json.Marshal(termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomePending, MachineCode: code})
		_ = ws.Write(ctx, websocket.MessageText, w2)
		// 保持连接直到 Agent 退出。
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	env := testEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"run", "--server", srv.URL, "--insecure", "--data-dir", dir, "--config-dir", t.TempDir()}, env)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(errOut(env), "等待管理员") {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(errOut(env), "等待管理员") {
		t.Fatalf("应记录「等待管理员启用」; stderr=%q", errOut(env))
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("取消后应以 0 退出，得到 %d; stderr=%q", code, errOut(env))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run 未在取消后退出")
	}

	id, err := agent.LoadIdentity(dir)
	if err != nil {
		t.Fatalf("run 应创建持久身份: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotCode != id.MachineCode() {
		t.Fatalf("握手使用的机器码与落盘身份不一致: %s vs %s", gotCode, id.MachineCode())
	}
	banner := out(env)
	for _, want := range []string{"RelayMesh Agent 1.7.0-test", id.MachineCode(), "明文", dir} {
		if !strings.Contains(banner, want) {
			t.Errorf("横幅缺少 %q:\n%s", want, banner)
		}
	}
	if !strings.Contains(errOut(env), "已终止") {
		t.Errorf("退出时应说明会话已终止: %q", errOut(env))
	}
	st, err := os.Stat(id.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 没有 Unix 权限位，身份文件的 ACL 由 agent 包的 Windows 测试覆盖。
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		t.Errorf("身份文件权限过宽: %o", st.Mode().Perm())
	}
}

func TestRunWithTemporaryIdentityWritesNothing(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) }))
	defer srv.Close()
	env := testEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	code := Run(ctx, []string{"run", "--temp", "--server", srv.URL, "--insecure", "--data-dir", dir}, env)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut(env))
	}
	if !strings.Contains(out(env), "临时") {
		t.Errorf("横幅应说明临时身份: %q", out(env))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("临时身份不应写任何文件: %v", entries)
	}
}
