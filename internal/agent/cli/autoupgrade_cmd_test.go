//go:build !windows

package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/relsign"
	"github.com/teacat99/RelayMesh/v2/internal/relsign/trust"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// auto-upgrade enable|disable|status：主机所有者在本机决定这台主机的 Agent 要不要自己留意新版本。
// 同意只是数据目录里的一个文件；Hub 改不了。

func TestAutoUpgradeEnableDisableRoundTrip(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()

	res, err := doAutoUpgrade(h.sys, "status", "", dataDir, false)
	if err != nil || res.Mode != "" || !res.Capable {
		t.Fatalf("默认没有同意，但这台主机具备前提: %+v %v", res, err)
	}

	res, err = doAutoUpgrade(h.sys, "enable", termproto.AutoUpgradeNotify, dataDir, false)
	if err != nil || res.Mode != termproto.AutoUpgradeNotify || res.FilePath != agent.AutoUpgradePath(dataDir) || res.Since <= 0 {
		t.Fatalf("开启后应是 notify: %+v %v", res, err)
	}
	if got := agent.AutoUpgradeMode(dataDir); got != termproto.AutoUpgradeNotify {
		t.Fatalf("Agent 读到的同意应与命令一致，得到 %q", got)
	}
	if res, err = doAutoUpgrade(h.sys, "status", "", dataDir, false); err != nil || res.Mode != termproto.AutoUpgradeNotify {
		t.Fatalf("status 应反映同意: %+v %v", res, err)
	}

	res, err = doAutoUpgrade(h.sys, "disable", "", dataDir, false)
	if err != nil || res.Mode != "" || agent.AutoUpgradeMode(dataDir) != termproto.AutoUpgradeOff {
		t.Fatalf("收回后应没有同意: %+v %v", res, err)
	}
	if _, err := doAutoUpgrade(h.sys, "disable", "", dataDir, false); err != nil {
		t.Fatalf("重复收回不应报错: %v", err)
	}
}

func TestAutoUpgradeEnableRefusesWhereTheAgentCouldNotUpgradeItself(t *testing.T) {
	t.Run("没有装常驻服务", func(t *testing.T) {
		h := newFakeHost(t, "linux", 0)
		dataDir := t.TempDir()
		_, err := doAutoUpgrade(h.sys, "enable", termproto.AutoUpgradeNotify, dataDir, false)
		if err == nil || !strings.Contains(err.Error(), "没有检测到已安装的 RelayMesh Agent 常驻服务") {
			t.Fatalf("应说明只有常驻服务才能使用自动升级: %v", err)
		}
		if _, statErr := os.Stat(agent.AutoUpgradePath(dataDir)); statErr == nil {
			t.Fatal("拒绝时不应写同意文件（否则会造成「以为开了」的错觉）")
		}
	})
	t.Run("普通用户面对系统级安装", func(t *testing.T) {
		h, _ := installedHost(t, "linux", 0, common{})
		h.sys.euid = 1000
		dataDir := t.TempDir()
		if _, err := doAutoUpgrade(h.sys, "enable", termproto.AutoUpgradeNotify, dataDir, false); !errors.Is(err, errUpgradeNeedRoot) {
			t.Fatalf("应提示需要管理员权限: %v", err)
		}
		if _, statErr := os.Stat(agent.AutoUpgradePath(dataDir)); statErr == nil {
			t.Fatal("拒绝时不应写同意文件")
		}
	})
	for _, goos := range []string{"darwin", "freebsd"} {
		t.Run("暂不支持的系统 "+goos, func(t *testing.T) {
			h := newFakeHost(t, goos, 0)
			dataDir := t.TempDir()
			_, err := doAutoUpgrade(h.sys, "enable", termproto.AutoUpgradeNotify, dataDir, false)
			if err == nil || !strings.Contains(err.Error(), "暂不支持自动升级") || !strings.Contains(err.Error(), "relaymesh-agent upgrade") {
				t.Fatalf("应明确说明暂不支持，并指向手动路径: %v", err)
			}
			if _, statErr := os.Stat(agent.AutoUpgradePath(dataDir)); statErr == nil {
				t.Fatal("拒绝时不应写同意文件")
			}
		})
	}
}

func TestAutoUpgradeEnableRefusesBadModes(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	for _, mode := range []string{"", "off", "on", "Notify", "everything"} {
		_, err := doAutoUpgrade(h.sys, "enable", mode, dataDir, false)
		if err == nil || !strings.Contains(err.Error(), "--mode") {
			t.Errorf("模式 %q 应被拒绝并提示 --mode: %v", mode, err)
		}
	}
	if _, statErr := os.Stat(agent.AutoUpgradePath(dataDir)); statErr == nil {
		t.Fatal("被拒绝的模式不应写同意文件")
	}
}

// testReleaseKey 是测试用的发布公钥（固定种子，所以每次一样）。
func testReleaseKey(seed byte) string {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	return relsign.FormatPublicKey(priv.Public().(ed25519.PublicKey))
}

// 静默模式没有独立于 Hub 的验签就是个陷阱：Hub 被攻破就能让主机运行任意程序。
// 没有任何可信公钥时必须拒绝，并且明确指出怎么办（--trust-key）与可用的替代（notify）。
func TestAutoUpgradeSilentModeNeedsATrustedKey(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	_, err := doAutoUpgrade(h.sys, "enable", termproto.AutoUpgradeSilent, dataDir, false)
	if err == nil || !strings.Contains(err.Error(), "--trust-key") || !strings.Contains(err.Error(), "notify") {
		t.Fatalf("应说明需要可信的发布公钥并指向 --trust-key / notify: %v", err)
	}
	if agent.AutoUpgradeMode(dataDir) != termproto.AutoUpgradeOff {
		t.Fatal("被拒绝时不应写同意文件")
	}
}

func TestAutoUpgradeSilentEnableWritesWindowAndPinnedKeys(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	keyA, keyB := testReleaseKey(1), testReleaseKey(2)
	// 公钥可以写成不带前缀的十六进制；同一把写两遍只保留一份。
	res, err := doAutoUpgradeWith(h.sys, "enable", autoUpgradeOpts{
		Mode: termproto.AutoUpgradeSilent, Window: "03:00-05:00",
		TrustKeys: []string{keyA, strings.TrimPrefix(keyB, "rmpub1:"), keyA},
	}, dataDir, false)
	if err != nil || res.Mode != termproto.AutoUpgradeSilent || res.Window != "03:00-05:00" || len(res.PinnedKeyIDs) != 2 {
		t.Fatalf("开启静默模式: %+v %v", res, err)
	}
	set, ok := agent.ReadAutoSettings(dataDir)
	if !ok || set.Mode != termproto.AutoUpgradeSilent || set.WindowText != "03:00-05:00" || set.Problem != "" || len(set.Keys()) != 2 {
		t.Fatalf("Agent 读到的设置应与命令一致: %+v ok=%v", set, ok)
	}
	// 文件里保存的是统一的带前缀写法。
	cfg, _ := agent.ReadAutoUpgrade(dataDir)
	for _, k := range cfg.TrustKeys {
		if !strings.HasPrefix(k, "rmpub1:") {
			t.Errorf("保存的公钥应带前缀: %s", k)
		}
	}
}

// 发布构建里编进了主钥与备用钥：所有者开启静默模式不必再用 --trust-key 钉公钥；
// status 与 enable 的输出列出编进程序的公钥标识（不是完整公钥），供所有者与发布方公布的标识核对。
func TestAutoUpgradeSilentEnableUsesEmbeddedKeysWithoutTrustKey(t *testing.T) {
	embedded := []string{testReleaseKey(5), testReleaseKey(6)}
	restore := trust.SetTextsForTest(embedded)
	defer restore()
	var ids []string
	for _, text := range embedded {
		pub, _ := relsign.ParsePublicKey(text)
		ids = append(ids, relsign.KeyID(pub))
	}
	wantLine := "可信的发布公钥: 编进程序 2 把（标识 " + strings.Join(ids, "、") + "） · 本机钉住 0 把"

	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	res, err := doAutoUpgradeWith(h.sys, "enable", autoUpgradeOpts{Mode: termproto.AutoUpgradeSilent}, dataDir, false)
	if err != nil || res.Mode != termproto.AutoUpgradeSilent || len(res.PinnedKeyIDs) != 0 || strings.Join(res.EmbeddedKeyIDs, ",") != strings.Join(ids, ",") {
		t.Fatalf("有编进程序的公钥时，不带 --trust-key 也应能开启静默模式: %+v %v", res, err)
	}
	set, ok := agent.ReadAutoSettings(dataDir)
	if !ok || set.Problem != "" || len(set.TrustKeys) != 0 || len(set.Keys()) != 2 {
		t.Fatalf("Agent 读到的设置: 没钉公钥，可信名单是编进程序的 2 把: %+v ok=%v", set, ok)
	}

	env := testEnv(t)
	if code := cmdAutoUpgrade(context.Background(), []string{"status", "--data-dir", dataDir}, env); code != 0 {
		t.Fatalf("status: %d %s", code, env.Stderr.(*syncBuffer).String())
	}
	out := env.Stdout.(*syncBuffer).String()
	if !strings.Contains(out, wantLine) || strings.Contains(out, "没有任何可信的发布公钥") {
		t.Errorf("status 应列出编进程序的公钥标识、且不报「没有公钥」，期望含 %q:\n%s", wantLine, out)
	}
	for _, text := range embedded {
		if strings.Contains(out, text) || strings.Contains(out, strings.TrimPrefix(text, "rmpub1:")) {
			t.Errorf("status 只应显示公钥标识，不显示完整公钥:\n%s", out)
		}
	}

	// 钉住一把额外的公钥：叠加在编进程序的之上，两组分开显示。
	extra := testReleaseKey(7)
	if _, err := doAutoUpgradeWith(h.sys, "enable", autoUpgradeOpts{Mode: termproto.AutoUpgradeSilent, TrustKeys: []string{extra}}, dataDir, false); err != nil {
		t.Fatal(err)
	}
	if set, _ := agent.ReadAutoSettings(dataDir); len(set.Keys()) != 3 {
		t.Fatalf("钉住 1 把 + 编进程序 2 把 = 可信 3 把，得到 %d 把", len(set.Keys()))
	}
}

// auto-upgrade keys：列出这个程序编进去的可信发布公钥。格式是固定的——发布脚本靠它从真正要发布的 Agent 程序里，
// 核对签名用的钥匙确实在这个版本的信任名单里（不在的话，所有主机都会拒绝这个版本的签名）。
func TestAutoUpgradeKeysListsTheEmbeddedKeys(t *testing.T) {
	run := func(args ...string) (int, string, string) {
		env := testEnv(t)
		code := cmdAutoUpgrade(context.Background(), args, env)
		return code, env.Stdout.(*syncBuffer).String(), env.Stderr.(*syncBuffer).String()
	}

	// 没有编进公钥（自行编译 / 测试构建）：明确说没有，退出码仍是 0。
	if code, out, _ := run("keys"); code != 0 || !strings.Contains(out, "没有编进发布公钥") {
		t.Fatalf("没有编进公钥时应明确说明: code=%d out=%q", code, out)
	}

	embedded := []string{testReleaseKey(8), testReleaseKey(9)}
	restore := trust.SetTextsForTest(embedded)
	defer restore()
	code, out, errOut := run("keys")
	if code != 0 || errOut != "" {
		t.Fatalf("keys: code=%d err=%q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "编进程序的发布公钥（2 把）") {
		t.Fatalf("应有一行标题 + 每把公钥一行:\n%s", out)
	}
	for i, text := range embedded {
		pub, _ := relsign.ParsePublicKey(text)
		if want := "  " + relsign.KeyID(pub) + "  " + text; lines[i+1] != want {
			t.Errorf("第 %d 行 = %q，期望 %q（格式固定：两个空格缩进、标识、两个空格、rmpub1:公钥）", i+1, lines[i+1], want)
		}
	}
	// 不碰数据目录：指定一个不存在的目录也一样成功，并且不会创建它。
	missing := t.TempDir() + "/not-created"
	if code, _, _ := run("keys", "--data-dir", missing); code != 0 {
		t.Fatalf("keys 不应依赖数据目录: %d", code)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("keys 不应创建数据目录")
	}
}

// install --auto-upgrade / --auto-window：在动手安装之前就把不合规的参数挡住。
func TestInstallAutoUpgradeOpts(t *testing.T) {
	restore := trust.SetTextsForTest([]string{testReleaseKey(5)})
	defer restore()
	for _, tc := range []struct {
		name, mode, window, goos string
		want                     autoUpgradeOpts
		wantErr                  string // 空 = 应成功
	}{
		{name: "两个选项都没给：安装时不碰自动升级", goos: "linux"},
		{name: "提示模式", mode: "notify", goos: "linux", want: autoUpgradeOpts{Mode: "notify"}},
		{name: "大小写与空白被规整", mode: " Silent ", window: " 03:00-05:00 ", goos: "linux", want: autoUpgradeOpts{Mode: "silent", Window: "03:00-05:00"}},
		{name: "静默模式可跨午夜", mode: "silent", window: "23:00-02:00", goos: "linux", want: autoUpgradeOpts{Mode: "silent", Window: "23:00-02:00"}},
		{name: "只给时间段", window: "03:00-05:00", goos: "linux", wantErr: "--auto-window 需要同时指定 --auto-upgrade silent"},
		{name: "模式不认识", mode: "bogus", goos: "linux", wantErr: "--auto-upgrade 应为 notify"},
		{name: "提示模式不带时间段", mode: "notify", window: "03:00-05:00", goos: "linux", wantErr: "--auto-window 只用于 --auto-upgrade silent"},
		{name: "时间段写法不对", mode: "silent", window: "whenever", goos: "linux", wantErr: "--auto-window 不对"},
		{name: "macOS 暂不支持", mode: "notify", goos: "darwin", wantErr: "暂不支持自动升级"},
		{name: "Windows 暂不支持", mode: "silent", goos: "windows", wantErr: "暂不支持自动升级"},
	} {
		got, err := installAutoUpgradeOpts(tc.mode, tc.window, tc.goos)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: 应报错含 %q，得到 %v", tc.name, tc.wantErr, err)
			}
			continue
		}
		if err != nil || got.Mode != tc.want.Mode || got.Window != tc.want.Window || len(got.TrustKeys) != 0 {
			t.Errorf("%s: 得到 %+v %v，期望 %+v", tc.name, got, err, tc.want)
		}
	}

	// 程序里没有编进发布公钥（自行编译）：静默模式在安装之前就被拒绝，并指出出路；提示模式不受影响。
	none := trust.SetTextsForTest(nil)
	defer none()
	if _, err := installAutoUpgradeOpts("silent", "", "linux"); err == nil || !strings.Contains(err.Error(), "编进了发布公钥") || !strings.Contains(err.Error(), "--auto-upgrade notify") {
		t.Errorf("没有编进公钥时静默模式应被拒绝并指出出路: %v", err)
	}
	if got, err := installAutoUpgradeOpts("notify", "", "linux"); err != nil || got.Mode != "notify" {
		t.Errorf("提示模式不需要公钥: %+v %v", got, err)
	}
}

// 参数不合规时 install 在动手之前就退出（用法错误 2）：不创建身份、不写任何文件。
func TestInstallRejectsBadAutoUpgradeBeforeTouchingTheSystem(t *testing.T) {
	for name, extra := range map[string][]string{
		"模式不认识":       {"--auto-upgrade", "bogus"},
		"只有时间段":       {"--auto-window", "03:00-05:00"},
		"静默模式但没有编进公钥": {"--auto-upgrade", "silent"}, // TestMain 默认清空编进程序的公钥
	} {
		dataDir, cfgDir := t.TempDir(), t.TempDir()
		args := append([]string{"--server", "https://relay.example.com", "--data-dir", dataDir, "--config-dir", cfgDir}, extra...)
		env := testEnv(t)
		if code := cmdInstall(context.Background(), args, env); code != 2 {
			t.Errorf("%s: 应以用法错误（2）退出，得到 %d；stderr=%s", name, code, env.Stderr.(*syncBuffer).String())
			continue
		}
		if !strings.Contains(env.Stderr.(*syncBuffer).String(), "参数错误") {
			t.Errorf("%s: 应说明参数错误: %s", name, env.Stderr.(*syncBuffer).String())
		}
		for _, d := range []string{dataDir, cfgDir} {
			if entries, _ := os.ReadDir(d); len(entries) != 0 {
				t.Errorf("%s: 被拒绝时不应写任何文件，%s 里有 %d 项", name, d, len(entries))
			}
		}
	}
}

// 装好服务之后开启自动升级：同意文件写在服务使用的数据目录里，输出与 auto-upgrade enable 一致；
// 失败不回滚服务，只给出原因与可以直接执行的补救命令（调用方据此以非零退出码结束）。
func TestEnableAutoUpgradeAfterInstall(t *testing.T) {
	restore := trust.SetTextsForTest([]string{testReleaseKey(5)})
	defer restore()

	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	env := testEnv(t)
	if !enableAutoUpgradeAfterInstall(env, h.sys, autoUpgradeOpts{Mode: "silent", Window: "03:00-05:00"}, dataDir, false) {
		t.Fatalf("装好之后应能开启自动升级: %s", env.Stdout.(*syncBuffer).String())
	}
	set, ok := agent.ReadAutoSettings(dataDir)
	if !ok || set.Mode != termproto.AutoUpgradeSilent || set.WindowText != "03:00-05:00" || set.Problem != "" {
		t.Fatalf("同意文件应写进服务的数据目录: %+v ok=%v", set, ok)
	}
	out := env.Stdout.(*syncBuffer).String()
	for _, want := range []string{"已开启自动升级（静默", "升级时间段: 03:00-05:00", "可信的发布公钥: 编进程序 1 把", "auto-upgrade disable"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里应有 %q:\n%s", want, out)
		}
	}

	// 没有装服务（或前提不满足）：不写同意文件，说明服务已装好但没能开启，并给出补救命令（带 --window / --user / --data-dir）。
	bare := newFakeHost(t, "linux", 0)
	dataDir2 := t.TempDir()
	env2 := testEnv(t)
	if enableAutoUpgradeAfterInstall(env2, bare.sys, autoUpgradeOpts{Mode: "silent", Window: "03:00-05:00"}, dataDir2, true) {
		t.Fatal("没有装好的服务时不应报告成功")
	}
	out2 := env2.Stdout.(*syncBuffer).String()
	for _, want := range []string{"服务已经装好，但没能开启自动升级", "auto-upgrade enable --mode silent --window 03:00-05:00 --user --data-dir " + dataDir2} {
		if !strings.Contains(out2, want) {
			t.Errorf("失败时输出里应有 %q:\n%s", want, out2)
		}
	}
	if _, ok := agent.ReadAutoSettings(dataDir2); ok {
		t.Fatal("失败时不应写同意文件")
	}
}

func TestAutoUpgradeSilentEnableRejectsBadOptions(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	good := testReleaseKey(1)
	cases := map[string]autoUpgradeOpts{
		"时间段写法不对":      {Mode: "silent", Window: "3-5", TrustKeys: []string{good}},
		"时间段起止相同":      {Mode: "silent", Window: "03:00-03:00", TrustKeys: []string{good}},
		"公钥不是十六进制":     {Mode: "silent", TrustKeys: []string{"rmpub1:" + strings.Repeat("zz", 32)}},
		"公钥长度不对":       {Mode: "silent", TrustKeys: []string{"rmpub1:abcd"}},
		"提示模式不接受时间段":   {Mode: "notify", Window: "03:00-05:00"},
		"提示模式不接受公钥":    {Mode: "notify", TrustKeys: []string{good}},
		"公钥个数超过上限":     {Mode: "silent", TrustKeys: manyKeys(9)},
		"没有公钥也没有编进程序的": {Mode: "silent", Window: "03:00-05:00"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			if _, err := doAutoUpgradeWith(h.sys, "enable", opts, dataDir, false); err == nil {
				t.Fatal("应被拒绝")
			}
			if _, statErr := os.Stat(agent.AutoUpgradePath(dataDir)); statErr == nil {
				t.Fatal("被拒绝的参数不应写同意文件")
			}
		})
	}
}

func manyKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = testReleaseKey(byte(i + 1))
	}
	return out
}

// 所有者重新 enable 是明确的动作：之前因为连续失败而暂停的版本重新获得机会；收回同意时记录一并清掉。
func TestAutoUpgradeEnableAndDisableClearThePausedAttempts(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	opts := autoUpgradeOpts{Mode: termproto.AutoUpgradeSilent, TrustKeys: []string{testReleaseKey(1)}}
	if _, err := doAutoUpgradeWith(h.sys, "enable", opts, dataDir, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := agent.RecordAutoAttempt(dataDir, "v1.7.0", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !agent.ReadAutoAttempts(dataDir).Paused("v1.7.0") {
		t.Fatal("前提：v1.7.0 已经试满")
	}
	if _, err := doAutoUpgradeWith(h.sys, "enable", opts, dataDir, false); err != nil {
		t.Fatal(err)
	}
	if agent.ReadAutoAttempts(dataDir).Paused("v1.7.0") {
		t.Fatal("重新 enable 应清除暂停")
	}

	_ = agent.RecordAutoAttempt(dataDir, "v1.7.0", time.Now())
	if _, err := doAutoUpgrade(h.sys, "disable", "", dataDir, false); err != nil {
		t.Fatal(err)
	}
	if got := agent.ReadAutoAttempts(dataDir); got.Count != 0 || got.Unreadable {
		t.Fatalf("收回同意应清掉尝试记录: %+v", got)
	}
}

func TestAutoUpgradeStatusExplainsTheSilentSettings(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	key := testReleaseKey(3)
	if _, err := doAutoUpgradeWith(h.sys, "enable", autoUpgradeOpts{Mode: termproto.AutoUpgradeSilent, Window: "23:00-02:00", TrustKeys: []string{key}}, dataDir, false); err != nil {
		t.Fatal(err)
	}
	pub, _ := relsign.ParsePublicKey(key)
	id := relsign.KeyID(pub)

	status := func() string {
		env := testEnv(t)
		if code := cmdAutoUpgrade(context.Background(), []string{"status", "--data-dir", dataDir}, env); code != 0 {
			t.Fatalf("status 应成功: %d %s", code, env.Stderr.(*syncBuffer).String())
		}
		return env.Stdout.(*syncBuffer).String()
	}
	out := status()
	for _, want := range []string{"静默模式", "升级时间段: 23:00-02:00", "可信的发布公钥: 编进程序 0 把 · 本机钉住 1 把（标识 " + id + "）"} {
		if !strings.Contains(out, want) {
			t.Errorf("status 里应有 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, key) || strings.Contains(out, strings.TrimPrefix(key, "rmpub1:")) {
		t.Errorf("status 只应显示公钥标识，不显示完整公钥:\n%s", out)
	}

	// 试满之后说明已暂停，并给出出路；静默升级的最近一轮决定也展示出来。
	for i := 0; i < 2; i++ {
		_ = agent.RecordAutoAttempt(dataDir, "v1.7.0", time.Now())
	}
	_ = agent.WriteAutoCheckNote(dataDir, agent.AutoCheckNote{
		At: 1_800_000_000, Status: termproto.AutoOfferAvailable, Latest: "v1.7.0", Current: "v1.6.0",
		Install: "paused", Detail: "v1.7.0 已经尝试升级 2 次都没有成功，已暂停",
	})
	out = status()
	for _, want := range []string{"升级尝试: v1.7.0 已尝试 2 次", "已暂停", "auto-upgrade enable", "静默升级: v1.7.0 已经尝试升级 2 次都没有成功"} {
		if !strings.Contains(out, want) {
			t.Errorf("试满之后 status 里应有 %q:\n%s", want, out)
		}
	}
}

func TestAutoUpgradeStatusWarnsWhenSilentCannotWork(t *testing.T) {
	// 同意文件被手工改坏（时间段写法不对 / 没有任何公钥）：status 明确说静默升级不会进行。
	dataDir := t.TempDir()
	if err := os.WriteFile(agent.AutoUpgradePath(dataDir), []byte(`{"mode":"silent","window":"whenever"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t)
	if code := cmdAutoUpgrade(context.Background(), []string{"status", "--data-dir", dataDir}, env); code != 0 {
		t.Fatalf("status: %d", code)
	}
	out := env.Stdout.(*syncBuffer).String()
	for _, want := range []string{"没有任何可信的发布公钥：静默升级不会进行", "设置有问题，静默升级不会进行"} {
		if !strings.Contains(out, want) {
			t.Errorf("应有 %q:\n%s", want, out)
		}
	}
}

func TestAutoUpgradeDisableAndStatusWorkEverywhere(t *testing.T) {
	// 收回同意永远是安全的，不需要任何前提；status 也只读文件。
	h := newFakeHost(t, "freebsd", 0)
	dataDir := t.TempDir()
	if err := agent.WriteAutoUpgrade(dataDir, agent.AutoUpgradeConfig{Mode: termproto.AutoUpgradeNotify}); err != nil {
		t.Fatal(err)
	}
	res, err := doAutoUpgrade(h.sys, "status", "", dataDir, false)
	if err != nil || res.Mode != termproto.AutoUpgradeNotify {
		t.Fatalf("status: %+v %v", res, err)
	}
	if res.Capable {
		t.Fatal("暂不支持的系统上，status 应指出这个同意不会生效（Capable=false）")
	}
	if res, err := doAutoUpgrade(h.sys, "disable", "", dataDir, false); err != nil || res.Mode != "" {
		t.Fatalf("disable: %+v %v", res, err)
	}
	if _, err := doAutoUpgrade(h.sys, "enable-everything", "", dataDir, false); err == nil {
		t.Fatal("不认识的动作应报错")
	}
}

func TestAutoUpgradeStatusKnowsTheServiceEvenWhenTheUserCannotChangeIt(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	h.sys.euid = 1000 // 普通用户查看系统级安装
	dataDir := t.TempDir()
	if err := agent.WriteAutoUpgrade(dataDir, agent.AutoUpgradeConfig{Mode: termproto.AutoUpgradeNotify}); err != nil {
		t.Fatal(err)
	}
	res, err := doAutoUpgrade(h.sys, "status", "", dataDir, false)
	if err != nil || res.Mode != termproto.AutoUpgradeNotify || !res.Capable {
		t.Fatalf("服务确实装着，只是这个用户改不了；不能报「不会生效」: %+v %v", res, err)
	}
}

func TestAutoUpgradeCommandParsing(t *testing.T) {
	dataDir := t.TempDir()
	run := func(args ...string) (int, string, string) {
		env := testEnv(t)
		code := cmdAutoUpgrade(context.Background(), args, env)
		return code, env.Stdout.(*syncBuffer).String(), env.Stderr.(*syncBuffer).String()
	}

	// status 只读：不依赖本机装了什么，安全地用真实环境跑。动作在选项之前、之后、缺省都可以。
	for _, args := range [][]string{
		{"status", "--data-dir", dataDir},
		{"--data-dir", dataDir, "status"},
		{"--data-dir", dataDir},
	} {
		code, out, errOut := run(args...)
		if code != 0 || !strings.Contains(out, "自动升级: 未开启（默认）") {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}

	// disable 对一个没有同意文件的数据目录也成功。
	if code, out, _ := run("disable", "--data-dir", dataDir); code != 0 || !strings.Contains(out, "已关闭") {
		t.Errorf("disable: code=%d out=%q", code, out)
	}

	// 用法错误（2）：不认识的动作、多余的参数、不存在的选项、--mode 用在 enable 以外、enable 缺 --mode。
	for _, args := range [][]string{
		{"maybe", "--data-dir", dataDir},
		{"status", "extra", "--data-dir", dataDir},
		{"--no-such-flag"},
		{"status", "--mode", "notify", "--data-dir", dataDir},
		{"disable", "--mode", "notify", "--data-dir", dataDir},
		{"status", "--window", "03:00-05:00", "--data-dir", dataDir},
		{"disable", "--trust-key", testReleaseKey(1), "--data-dir", dataDir},
		{"keys", "--mode", "notify"},
		{"keys", "extra"},
		{"enable", "--data-dir", dataDir},
	} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%v 应以用法错误（2）退出，得到 %d", args, code)
		}
	}

	// 值不合规是运行错误（1），并且什么都不写：没有可信公钥的静默模式被拒绝；不认识的模式被拒绝；
	// 时间段 / 公钥写法不对被拒绝；提示模式带时间段被拒绝。
	for _, args := range [][]string{
		{"enable", "--mode", "silent", "--data-dir", dataDir},
		{"enable", "--mode", "bogus", "--data-dir", dataDir},
		{"enable", "--mode", "silent", "--window", "nonsense", "--trust-key", testReleaseKey(1), "--data-dir", dataDir},
		{"enable", "--mode", "silent", "--trust-key", "rmpub1:abcd", "--data-dir", dataDir},
		{"enable", "--mode", "notify", "--window", "03:00-05:00", "--data-dir", dataDir},
	} {
		code, _, errOut := run(args...)
		if code != 1 || errOut == "" {
			t.Errorf("%v 应被拒绝（1）并给出原因: code=%d err=%q", args, code, errOut)
		}
	}
	if _, err := os.Stat(agent.AutoUpgradePath(dataDir)); err == nil {
		t.Fatal("这些命令都不应写同意文件")
	}
}

func TestAutoUpgradeDescribesTheModes(t *testing.T) {
	notify := describeAutoUpgradeMode(termproto.AutoUpgradeNotify)
	silent := describeAutoUpgradeMode(termproto.AutoUpgradeSilent)
	if !strings.Contains(notify, "不下载") || !strings.Contains(silent, "验签") {
		t.Errorf("描述应当让所有者看清两种模式的差别: %q / %q", notify, silent)
	}
}

func TestAutoUpgradeStatusShowsTheLastCheckAndDisableClearsIt(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	if _, err := doAutoUpgrade(h.sys, "enable", termproto.AutoUpgradeNotify, dataDir, false); err != nil {
		t.Fatal(err)
	}
	if res, err := doAutoUpgrade(h.sys, "status", "", dataDir, false); err != nil || res.Check != nil {
		t.Fatalf("还没检查过：status 没有结果可展示: %+v %v", res, err)
	}

	note := agent.AutoCheckNote{At: 1_800_000_000, Status: termproto.AutoOfferAvailable, Latest: "v1.7.0", Current: "v1.6.0"}
	if err := agent.WriteAutoCheckNote(dataDir, note); err != nil {
		t.Fatal(err)
	}
	res, err := doAutoUpgrade(h.sys, "status", "", dataDir, false)
	if err != nil || res.Check == nil || *res.Check != note {
		t.Fatalf("status 应带上 Agent 最近一次检查的结果: %+v %v", res, err)
	}
	line := describeAutoCheckNote(note)
	for _, want := range []string{"Hub 上有新版本 v1.7.0", "本机 v1.6.0"} {
		if !strings.Contains(line, want) {
			t.Errorf("描述里应有 %q: %s", want, line)
		}
	}

	// 收回同意时一并清掉检查结果；之后即使磁盘上还有一份旧的，status 也不展示（没有同意就没有「上一次检查」）。
	if _, err := doAutoUpgrade(h.sys, "disable", "", dataDir, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := agent.ReadAutoCheckNote(dataDir); ok {
		t.Fatal("收回同意后应清掉检查结果")
	}
	if err := agent.WriteAutoCheckNote(dataDir, note); err != nil {
		t.Fatal(err)
	}
	if res, err := doAutoUpgrade(h.sys, "status", "", dataDir, false); err != nil || res.Mode != "" || res.Check != nil {
		t.Fatalf("没有同意时不展示旧的检查结果: %+v %v", res, err)
	}
}

func TestAutoUpgradeDescribesEveryCheckOutcome(t *testing.T) {
	for status, want := range map[string]string{
		termproto.AutoOfferAvailable:   "新版本",
		termproto.AutoOfferUpToDate:    "已是最新",
		termproto.AutoOfferDisabled:    "总闸",
		termproto.AutoOfferUnavailable: "没有适用于本机",
		termproto.AutoOfferNotAllowed:  "没有开启",
		"no_reply":                     "没有回答",
	} {
		got := describeAutoCheckNote(agent.AutoCheckNote{At: 1_800_000_000, Status: status, Latest: "v1.7.0", Current: "v1.6.0"})
		if !strings.Contains(got, want) || !strings.Contains(got, "2027-") {
			t.Errorf("%s 的描述里应有 %q 与检查时间: %s", status, want, got)
		}
	}
}

func TestAutoUpgradeStatusCommandPrintsTheLastCheck(t *testing.T) {
	dataDir := t.TempDir()
	run := func() string {
		env := testEnv(t)
		if code := cmdAutoUpgrade(context.Background(), []string{"status", "--data-dir", dataDir}, env); code != 0 {
			t.Fatalf("status 应成功: %d %s", code, env.Stderr.(*syncBuffer).String())
		}
		return env.Stdout.(*syncBuffer).String()
	}
	if out := run(); strings.Contains(out, "上一次检查") {
		t.Fatalf("没有同意时不应出现检查结果: %s", out)
	}
	if err := agent.WriteAutoUpgrade(dataDir, agent.AutoUpgradeConfig{Mode: termproto.AutoUpgradeNotify}); err != nil {
		t.Fatal(err)
	}
	if out := run(); !strings.Contains(out, "上一次检查: 还没有") || !strings.Contains(out, "自动升级: 已开启 · 提示模式") {
		t.Fatalf("已开启但还没检查过: %s", out)
	}
	if err := agent.WriteAutoCheckNote(dataDir, agent.AutoCheckNote{At: 1_800_000_000, Status: termproto.AutoOfferUpToDate, Latest: "v1.7.0", Current: "v1.7.0"}); err != nil {
		t.Fatal(err)
	}
	if out := run(); !strings.Contains(out, "上一次检查: ") || !strings.Contains(out, "已是最新") {
		t.Fatalf("应展示上一次检查: %s", out)
	}
}
