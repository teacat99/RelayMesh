package agent

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/relsign"
	"github.com/teacat99/RelayMesh/v2/internal/relsign/trust"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

func testKeyPair(seed byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

func pubText(seed byte) string {
	pub, _ := testKeyPair(seed)
	return relsign.FormatPublicKey(pub)
}

func TestSettingsOfAValidSilentConfig(t *testing.T) {
	cfg := AutoUpgradeConfig{Mode: termproto.AutoUpgradeSilent, Window: "03:00-05:00", TrustKeys: []string{pubText(1), pubText(2)}}
	s := cfg.Settings()
	if s.Problem != "" || s.Mode != termproto.AutoUpgradeSilent || s.WindowText != "03:00-05:00" || s.Window.Always() || len(s.TrustKeys) != 2 {
		t.Fatalf("Settings = %+v", s)
	}
	if got := len(s.Keys()); got != 2 {
		t.Fatalf("没有编进程序的公钥时，信任名单就是钉住的那几把，得到 %d 把", got)
	}
}

func TestSettingsReportProblemsInsteadOfIgnoringThem(t *testing.T) {
	for name, cfg := range map[string]AutoUpgradeConfig{
		"时间段写法不对": {Mode: "silent", Window: "whenever"},
		"时间段起止相同": {Mode: "silent", Window: "03:00-03:00"},
		"公钥无法解析":  {Mode: "silent", TrustKeys: []string{"rmpub1:nothex"}},
		"一把好一把坏":  {Mode: "silent", TrustKeys: []string{pubText(1), "garbage"}},
		"公钥太多": {Mode: "silent", TrustKeys: func() []string {
			var v []string
			for i := 0; i <= maxPinnedKeys; i++ {
				v = append(v, pubText(byte(i+1)))
			}
			return v
		}()},
	} {
		if s := cfg.Settings(); s.Problem == "" {
			t.Errorf("%s：应当报出问题，而不是悄悄忽略", name)
		}
	}
}

func TestSettingsDeduplicateKeysByIdentity(t *testing.T) {
	pub, _ := testKeyPair(1)
	cfg := AutoUpgradeConfig{Mode: "silent", TrustKeys: []string{relsign.FormatPublicKey(pub), strings.TrimPrefix(relsign.FormatPublicKey(pub), "rmpub1:")}}
	if got := len(cfg.Settings().TrustKeys); got != 1 {
		t.Fatalf("同一把公钥的两种写法应只算一把，得到 %d", got)
	}
}

// 信任名单 = 编进程序的 ∪ 主机所有者钉住的；同一把钥匙在两处出现只算一次。
// 编进程序的公钥由测试直接交给 keysWith，不去改写包级变量（后台协程还在读它时改写会是数据竞争）。
func TestKeysAreTheUnionOfEmbeddedAndPinned(t *testing.T) {
	pub9, _ := testKeyPair(9)
	pub1, _ := testKeyPair(1)
	embedded := []ed25519.PublicKey{pub9, pub1}
	s := AutoUpgradeConfig{Mode: "silent", TrustKeys: []string{pubText(1), pubText(2)}}.Settings()
	keys := s.keysWith(embedded)
	if len(keys) != 3 {
		t.Fatalf("应有 3 把（9、1、2），得到 %d", len(keys))
	}
	ids := map[string]bool{}
	for _, k := range keys {
		ids[relsign.KeyID(k)] = true
	}
	for _, seed := range []byte{9, 1, 2} {
		pub, _ := testKeyPair(seed)
		if !ids[relsign.KeyID(pub)] {
			t.Errorf("名单里缺少种子 %d 的公钥", seed)
		}
	}
	// 只有编进程序的公钥时（所有者没有钉任何钥匙）名单也不为空。
	if got := len((AutoSettings{}).keysWith(embedded)); got != 2 {
		t.Fatalf("只有编进程序的公钥: 得到 %d 把，期望 2", got)
	}
	// 发布的构建里 Keys() 就是用编进程序的公钥走这条路径。
	if got, want := len((AutoSettings{}).Keys()), len(embeddedReleaseKeys()); got != want {
		t.Fatalf("Keys() 应等于编进程序的公钥（%d 把），得到 %d 把", want, got)
	}
}

func TestEmbeddedReleaseKeysComeFromTheTrustRoot(t *testing.T) {
	// Agent 的「编进程序的公钥」就是信任根（internal/relsign/trust）里生效的那一份：发布构建里是主钥 + 备用钥，
	// 名单本身的完整性检查在 trust 的测试里；这个包的测试默认把它清空（见 main_test.go），这里换成两把已知的钥匙确认接线。
	restore := trust.SetTextsForTest([]string{pubText(1), pubText(2)})
	defer restore()
	embedded := embeddedReleaseKeys()
	if len(embedded) != 2 {
		t.Fatalf("应取到信任根里的 2 把公钥，得到 %d 把", len(embedded))
	}
	for i, k := range embedded {
		if got, want := relsign.FormatPublicKey(k), []string{pubText(1), pubText(2)}[i]; got != want {
			t.Errorf("第 %d 把公钥 = %s，期望 %s", i, got, want)
		}
	}
	// 没有所有者钉的公钥时，可信名单就是编进程序的；钉了的叠加在其后，重复的只算一把。
	if got := len((AutoSettings{}).Keys()); got != 2 {
		t.Fatalf("没钉任何公钥时可信名单应是编进程序的 2 把，得到 %d 把", got)
	}
	pinned, _ := testKeyPair(3)
	dup, _ := testKeyPair(1)
	if got := len((AutoSettings{TrustKeys: []ed25519.PublicKey{pinned, dup}}).Keys()); got != 3 {
		t.Fatalf("钉住一把新的和一把重复的：可信名单应是 3 把，得到 %d 把", got)
	}
}

func TestWriteAutoUpgradeValidatesSilentOnlyFields(t *testing.T) {
	dir := t.TempDir()
	for name, cfg := range map[string]AutoUpgradeConfig{
		"提示模式带时间段":    {Mode: "notify", Window: "03:00-05:00"},
		"提示模式带公钥":     {Mode: "notify", TrustKeys: []string{pubText(1)}},
		"静默模式时间段写法不对": {Mode: "silent", Window: "x"},
		"静默模式公钥写法不对":  {Mode: "silent", TrustKeys: []string{"x"}},
	} {
		if err := WriteAutoUpgrade(dir, cfg); err == nil {
			t.Errorf("%s：应当被拒绝", name)
		}
		if _, err := os.Stat(AutoUpgradePath(dir)); err == nil {
			t.Fatalf("%s：被拒绝时不应写文件", name)
		}
	}

	want := AutoUpgradeConfig{Mode: "silent", Window: "23:00-02:00", TrustKeys: []string{pubText(1)}}
	if err := WriteAutoUpgrade(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadAutoUpgrade(dir)
	if !ok || got.Mode != want.Mode || got.Window != want.Window || len(got.TrustKeys) != 1 || got.TrustKeys[0] != want.TrustKeys[0] {
		t.Fatalf("往返不一致: %+v ok=%v", got, ok)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(AutoUpgradePath(dir)); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("同意文件应为 0600: %v %v", st, err)
		}
	}
	s, ok := ReadAutoSettings(dir)
	if !ok || s.Problem != "" || s.WindowText != "23:00-02:00" {
		t.Fatalf("ReadAutoSettings: %+v ok=%v", s, ok)
	}
}

// 手工改坏的同意文件：读得出模式，但设置里的问题被如实报出，静默安装据此拒绝。
func TestReadAutoSettingsSurfacesHandEditedProblems(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(AutoUpgradePath(dir), []byte(`{"mode":"silent","window":"later","trust_keys":["rmpub1:zz"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, ok := ReadAutoSettings(dir)
	if !ok || s.Mode != termproto.AutoUpgradeSilent || s.Problem == "" {
		t.Fatalf("应读出模式并报出问题: %+v ok=%v", s, ok)
	}
	if _, ok := ReadAutoSettings(t.TempDir()); ok {
		t.Fatal("没有同意文件时应返回 false")
	}
}

// ---------- 尝试记录 ----------

func TestAutoAttemptsCountPerVersionAndPauseAtTheLimit(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)

	if a := ReadAutoAttempts(dir); a.Count != 0 || a.Unreadable || a.Paused("v1.7.0") {
		t.Fatalf("没有记录：什么都没试过: %+v", a)
	}
	if err := RecordAutoAttempt(dir, "v1.7.0", now); err != nil {
		t.Fatal(err)
	}
	a := ReadAutoAttempts(dir)
	if a.Version != "v1.7.0" || a.Count != 1 || a.At != now.Unix() || a.Paused("v1.7.0") {
		t.Fatalf("第一次: %+v", a)
	}
	if err := RecordAutoAttempt(dir, "v1.7.0", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a = ReadAutoAttempts(dir)
	if a.Count != maxAutoAttempts || !a.Paused("v1.7.0") {
		t.Fatalf("同一个版本试满后应暂停: %+v", a)
	}
	// 暂停只针对这个版本：出现另一个版本就重新给机会，并且从 1 重新数。
	if a.Paused("v1.7.1") {
		t.Fatal("换一个版本不应被暂停")
	}
	if err := RecordAutoAttempt(dir, "v1.7.1", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if a = ReadAutoAttempts(dir); a.Version != "v1.7.1" || a.Count != 1 || a.Paused("v1.7.1") {
		t.Fatalf("换版本后重新数: %+v", a)
	}
}

func TestAutoAttemptsFailClosedOnUnreadableRecords(t *testing.T) {
	bad := map[string]string{
		"不是 JSON":   "not json",
		"空对象":       `{}`,
		"版本写法不对":    `{"version":"v1 2","count":1}`,
		"次数为零":      `{"version":"v1.7.0","count":0}`,
		"次数为负":      `{"version":"v1.7.0","count":-3}`,
		"次数大得离谱":    `{"version":"v1.7.0","count":100000}`,
		"超过大小上限的垃圾": strings.Repeat("x", maxAutoAttemptsBytes+10),
	}
	for name, content := range bad {
		dir := t.TempDir()
		if err := os.MkdirAll(UpgradeDir(dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(AutoAttemptsPath(dir), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		a := ReadAutoAttempts(dir)
		if !a.Unreadable || !a.Paused("v1.7.0") || !a.Paused("anything") {
			t.Errorf("%s：读不清时应按「已试满」处理，得到 %+v", name, a)
		}
		// 记录坏了也不会让下一次记录崩溃：从 1 重新数，记录恢复可读。
		if err := RecordAutoAttempt(dir, "v1.7.0", time.Now()); err != nil {
			t.Errorf("%s：RecordAutoAttempt: %v", name, err)
		}
		if a := ReadAutoAttempts(dir); a.Unreadable || a.Count != 1 {
			t.Errorf("%s：重新记录后应恢复可读，得到 %+v", name, a)
		}
	}
	if !ReadAutoAttempts("").Unreadable {
		t.Error("数据目录为空应按读不清处理")
	}
	if err := RecordAutoAttempt("", "v1.7.0", time.Now()); err == nil {
		t.Error("数据目录为空应报错")
	}
	if err := RecordAutoAttempt(t.TempDir(), "bad version", time.Now()); err == nil {
		t.Error("版本写法不合规应报错")
	}
}

func TestAutoAttemptsRefuseNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("符号链接在 Windows 上需要特权")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(UpgradeDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"version":"v1.7.0","count":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, AutoAttemptsPath(dir)); err != nil {
		t.Fatal(err)
	}
	if !ReadAutoAttempts(dir).Unreadable {
		t.Fatal("符号链接不是普通文件，应按读不清处理")
	}
}

func TestAutoAttemptsFilePermissionsAndClear(t *testing.T) {
	dir := t.TempDir()
	if err := RecordAutoAttempt(dir, "v1.7.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(AutoAttemptsPath(dir)); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("记录文件应为 0600: %v %v", st, err)
		}
		if st, err := os.Stat(UpgradeDir(dir)); err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("升级目录应为 0700: %v %v", st, err)
		}
	}
	if err := ClearAutoAttempts(dir); err != nil {
		t.Fatal(err)
	}
	if a := ReadAutoAttempts(dir); a.Count != 0 || a.Unreadable {
		t.Fatalf("清除后应没有记录: %+v", a)
	}
	if err := ClearAutoAttempts(dir); err != nil {
		t.Fatalf("清除一个不存在的记录不算错: %v", err)
	}
	if err := ClearAutoAttempts(""); err == nil {
		t.Fatal("数据目录为空应报错")
	}
}
