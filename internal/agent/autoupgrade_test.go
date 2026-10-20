package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 自动升级的主机本地同意：数据目录里的 auto-upgrade.json。读不清楚的时候必须落在「关闭」的一侧。

func TestAutoUpgradeDefaultsToOff(t *testing.T) {
	dir := t.TempDir()
	if cfg, ok := ReadAutoUpgrade(dir); ok || cfg.Mode != "" {
		t.Fatalf("没有同意文件时应是关闭: %+v %v", cfg, ok)
	}
	if got := AutoUpgradeMode(dir); got != termproto.AutoUpgradeOff {
		t.Fatalf("没有同意文件时声明 off，得到 %q", got)
	}
	if _, ok := ReadAutoUpgrade(""); ok {
		t.Fatal("数据目录为空时不能读到同意")
	}
	if got := AutoUpgradeMode(""); got != termproto.AutoUpgradeOff {
		t.Fatalf("数据目录为空时声明 off，得到 %q", got)
	}
}

func TestAutoUpgradeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAutoUpgrade(dir, AutoUpgradeConfig{Mode: termproto.AutoUpgradeNotify}); err != nil {
		t.Fatal(err)
	}
	cfg, ok := ReadAutoUpgrade(dir)
	if !ok || cfg.Mode != termproto.AutoUpgradeNotify || cfg.At <= 0 {
		t.Fatalf("写入后应读到 notify 与写入时刻: %+v %v", cfg, ok)
	}
	if got := AutoUpgradeMode(dir); got != termproto.AutoUpgradeNotify {
		t.Fatalf("AutoUpgradeMode = %q", got)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(AutoUpgradePath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("同意文件应只有所有者可读写，得到 %v", st.Mode().Perm())
		}
	}

	// 改模式是覆盖写入，不会留下临时文件。
	if err := WriteAutoUpgrade(dir, AutoUpgradeConfig{Mode: termproto.AutoUpgradeSilent, At: 1_800_000_000}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := ReadAutoUpgrade(dir); cfg.Mode != termproto.AutoUpgradeSilent || cfg.At != 1_800_000_000 {
		t.Fatalf("覆盖写入后: %+v", cfg)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != autoUpgradeFile {
		t.Fatalf("数据目录里应只有同意文件，得到 %v %v", entries, err)
	}

	if err := RemoveAutoUpgrade(dir); err != nil {
		t.Fatal(err)
	}
	if got := AutoUpgradeMode(dir); got != termproto.AutoUpgradeOff {
		t.Fatalf("收回后应是 off，得到 %q", got)
	}
	if err := RemoveAutoUpgrade(dir); err != nil {
		t.Fatalf("重复收回不应报错: %v", err)
	}
}

func TestAutoUpgradeCreatesTheDataDirPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "agent")
	if err := WriteAutoUpgrade(dir, AutoUpgradeConfig{Mode: termproto.AutoUpgradeNotify}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("新建的数据目录应只有所有者可进入，得到 %v", st.Mode().Perm())
	}
}

func TestAutoUpgradeRefusesToWriteAnInvalidMode(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{"", termproto.AutoUpgradeOff, "on", "Notify", "silent "} {
		if err := WriteAutoUpgrade(dir, AutoUpgradeConfig{Mode: mode}); err == nil {
			t.Errorf("模式 %q 不能被写成同意", mode)
		}
	}
	if err := WriteAutoUpgrade("", AutoUpgradeConfig{Mode: termproto.AutoUpgradeNotify}); err == nil {
		t.Error("数据目录为空应报错")
	}
	if err := RemoveAutoUpgrade(""); err == nil {
		t.Error("数据目录为空应报错")
	}
	if _, err := os.Stat(AutoUpgradePath(dir)); err == nil {
		t.Fatal("被拒绝的写入不应留下文件")
	}
}

func TestAutoUpgradeReadFailsClosed(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(AutoUpgradePath(dir), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for name, content := range map[string]string{
		"空文件":       "",
		"不是 JSON":   "notify",
		"损坏的 JSON":  `{"mode":"notify"`,
		"模式是 off":   `{"mode":"off"}`,
		"模式不认识":     `{"mode":"on"}`,
		"模式大小写不对":   `{"mode":"NOTIFY"}`,
		"模式带空白":     `{"mode":"notify "}`,
		"没有模式":      `{"at":1}`,
		"模式不是字符串":   `{"mode":true}`,
		"整个文件是个数组":  `["notify"]`,
		"超过大小上限的内容": `{"mode":"notify","pad":"` + strings.Repeat("x", maxAutoUpgradeFileBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := write(t, content)
			if cfg, ok := ReadAutoUpgrade(dir); ok {
				t.Fatalf("读不清楚时必须视为没有同意: %+v", cfg)
			}
			if got := AutoUpgradeMode(dir); got != termproto.AutoUpgradeOff {
				t.Fatalf("声明应是 off，得到 %q", got)
			}
		})
	}

	t.Run("同意文件是个目录", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(AutoUpgradePath(dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadAutoUpgrade(dir); ok {
			t.Fatal("目录不是同意")
		}
	})

	t.Run("同意文件是符号链接", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		if err := os.WriteFile(target, []byte(`{"mode":"notify"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, AutoUpgradePath(dir)); err != nil {
			t.Skipf("不能创建符号链接: %v", err)
		}
		if _, ok := ReadAutoUpgrade(dir); ok {
			t.Fatal("符号链接不能当作同意（与 web-upgrade 的开关文件同样的立场）")
		}
	})

	t.Run("不认识的多余字段不影响读取", func(t *testing.T) {
		// 以后的版本可能往同意文件里加字段（时间段、公钥……）；旧程序读到新文件时只认自己懂的部分。
		dir := write(t, `{"mode":"notify","at":1800000000,"window":"03:00-05:00","future":{"x":1}}`)
		cfg, ok := ReadAutoUpgrade(dir)
		if !ok || cfg.Mode != termproto.AutoUpgradeNotify || cfg.At != 1_800_000_000 {
			t.Fatalf("多余字段应被忽略: %+v %v", cfg, ok)
		}
	})
}

func TestSelfUpgradeAutoState(t *testing.T) {
	var nilCfg *SelfUpgrade
	if got := nilCfg.autoState(); got != "" {
		t.Errorf("没有升级能力时不声明: %q", got)
	}
	if got := (&SelfUpgrade{}).autoState(); got != "" {
		t.Errorf("没有提供自动升级时不声明: %q", got)
	}
	mode := func(m string) *SelfUpgrade { return &SelfUpgrade{AutoMode: func() string { return m }} }
	for in, want := range map[string]string{
		termproto.AutoUpgradeOff:    termproto.AutoUpgradeOff,
		termproto.AutoUpgradeNotify: termproto.AutoUpgradeNotify,
		termproto.AutoUpgradeSilent: termproto.AutoUpgradeSilent,
		"":                          termproto.AutoUpgradeOff,
		"garbage":                   termproto.AutoUpgradeOff,
	} {
		if got := mode(in).autoState(); got != want {
			t.Errorf("AutoMode()=%q 应声明 %q，得到 %q", in, want, got)
		}
	}
	// 两个开关互不影响：只改其中一个，gates 的另一半不变。
	web := &SelfUpgrade{Allowed: func() bool { return true }, AutoMode: func() string { return termproto.AutoUpgradeNotify }}
	if g := web.gates(); g.web != termproto.WebUpgradeOn || g.auto != termproto.AutoUpgradeNotify {
		t.Errorf("gates = %+v", g)
	}
}

func TestHelloAdvertisesAutoUpgrade(t *testing.T) {
	mode := func(m string) func() string { return func() string { return m } }
	cases := []struct {
		name      string
		upg       *SelfUpgrade
		temporary bool
		want      string
	}{
		{"没有提供升级能力（前台运行 / 不支持的系统）", nil, false, ""},
		{"提供了 Web 升级但没有自动升级（旧版安装器构造的配置）", &SelfUpgrade{Allowed: func() bool { return true }}, false, ""},
		{"所有者没有开启", &SelfUpgrade{AutoMode: mode(termproto.AutoUpgradeOff)}, false, termproto.AutoUpgradeOff},
		{"所有者选了提示模式", &SelfUpgrade{AutoMode: mode(termproto.AutoUpgradeNotify)}, false, termproto.AutoUpgradeNotify},
		{"所有者选了静默模式", &SelfUpgrade{AutoMode: mode(termproto.AutoUpgradeSilent)}, false, termproto.AutoUpgradeSilent},
		{"读到不认识的值时按关闭声明", &SelfUpgrade{AutoMode: mode("garbage")}, false, termproto.AutoUpgradeOff},
		{"临时身份即使提供了也不声明（没有安装过任何东西）", &SelfUpgrade{AutoMode: mode(termproto.AutoUpgradeSilent)}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu  sync.Mutex
				got termproto.Hello
			)
			target := stubHub(t, func(ctx context.Context, ws *websocket.Conn, _ *http.Request) {
				ch, _ := challenge(t, termproto.ProtocolVersion)
				sendJSON(t, ctx, ws, ch)
				_, data, err := ws.Read(ctx)
				if err != nil {
					return
				}
				var hello termproto.Hello
				if json.Unmarshal(data, &hello) != nil {
					return
				}
				mu.Lock()
				got = hello
				mu.Unlock()
				pub, _ := termproto.DecodeKey(hello.Pub, 32)
				sendJSON(t, ctx, ws, termproto.Welcome{T: termproto.TWelcome, Status: termproto.WelcomePending, MachineCode: termproto.MachineCode(pub)})
				_ = ws.Close(websocket.StatusCode(termproto.CloseUnknownMachine), "bye")
			})
			a := newAgentFor(t, target, func(o *Options) {
				o.SelfUpgrade = tc.upg
				if !tc.temporary {
					o.Identity = persistentIdentity(t)
				}
			})
			connectWith(t, a)
			mu.Lock()
			defer mu.Unlock()
			if got.Meta.AutoUpgrade != tc.want {
				t.Fatalf("hello.meta.auto_upgrade = %q，期望 %q", got.Meta.AutoUpgrade, tc.want)
			}
		})
	}
}

func TestAgentReconnectsWhenTheAutoUpgradeModeFlips(t *testing.T) {
	old := upgradeGateInterval
	upgradeGateInterval = 20 * time.Millisecond
	t.Cleanup(func() { upgradeGateInterval = old })

	var mode atomic.Value
	mode.Store(termproto.AutoUpgradeOff)
	h := newUpgradeHarness(t, []byte("payload"), func(c *SelfUpgrade) {
		c.AutoMode = func() string { return mode.Load().(string) }
	})
	h.allowed.Store(false)
	hellos := make(chan termproto.Hello, 8)
	h.helloCapturingHub(t, func(hello termproto.Hello) { hellos <- hello }, nil)

	a := newAgentFor(t, h.target, func(o *Options) {
		o.Identity = persistentIdentity(t)
		o.Version = "1.0.0"
		o.SelfUpgrade = h.cfg
		o.MinBackoff = 10 * time.Millisecond
		o.MaxBackoff = 20 * time.Millisecond
		o.SlowRetry = time.Minute // 开关变化引起的重连若被当成故障，会落到这个慢节奏上，测试就会超时
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run 未在 ctx 取消后及时返回")
		}
	}()

	wait := func() termproto.Hello {
		select {
		case hello := <-hellos:
			return hello
		case <-time.After(10 * time.Second):
			t.Fatal("没有等到 Agent 的 hello")
			return termproto.Hello{}
		}
	}
	first := wait()
	if first.Meta.AutoUpgrade != termproto.AutoUpgradeOff || first.Meta.WebUpgrade != termproto.WebUpgradeOff {
		t.Fatalf("起初两个开关都应声明 off: auto=%q web=%q", first.Meta.AutoUpgrade, first.Meta.WebUpgrade)
	}
	mode.Store(termproto.AutoUpgradeNotify) // 主机所有者执行了 `auto-upgrade enable --mode notify`
	second := wait()
	if second.Meta.AutoUpgrade != termproto.AutoUpgradeNotify {
		t.Fatalf("所有者开启后，Agent 应自己重新握手并声明 notify（不必重启服务）: %q", second.Meta.AutoUpgrade)
	}
	if second.Meta.WebUpgrade != termproto.WebUpgradeOff {
		t.Fatalf("自动升级的变化不应改变 Web 升级的声明: %q", second.Meta.WebUpgrade)
	}
	mode.Store(termproto.AutoUpgradeOff) // 又执行了 `auto-upgrade disable`
	if third := wait(); third.Meta.AutoUpgrade != termproto.AutoUpgradeOff {
		t.Fatalf("所有者收回后，Agent 应重新握手并声明 off: %q", third.Meta.AutoUpgrade)
	}
}
