package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 「Web 一键升级」在数据目录里留下的小文件：开关、上线标记、上一次结果。
// Agent 与升级进程是两个进程，靠这些文件交换信息，所以路径与读写语义要稳。

func TestWebUpgradeFlag(t *testing.T) {
	dir := t.TempDir()
	if WebUpgradeAllowed(dir) {
		t.Fatal("默认必须是关闭的：没有主机所有者的明确操作，Hub 不能升级这台机器")
	}
	if err := SetWebUpgradeAllowed(dir, true); err != nil {
		t.Fatal(err)
	}
	if !WebUpgradeAllowed(dir) {
		t.Fatal("打开后应读到允许")
	}
	if err := SetWebUpgradeAllowed(dir, false); err != nil {
		t.Fatal(err)
	}
	if WebUpgradeAllowed(dir) {
		t.Fatal("关闭后应读到不允许")
	}
	if err := SetWebUpgradeAllowed(dir, false); err != nil {
		t.Fatalf("重复关闭不应报错: %v", err)
	}
}

func TestWebUpgradeFlagNeedsRegularFileAndDataDir(t *testing.T) {
	if WebUpgradeAllowed("") {
		t.Fatal("没有数据目录就不可能有开关")
	}
	if err := SetWebUpgradeAllowed("", true); err == nil {
		t.Fatal("数据目录为空应报错，而不是写到当前目录")
	}

	if runtime.GOOS == "windows" {
		t.Skip("符号链接在 Windows 上需要额外权限")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, WebUpgradeFlagPath(dir)); err != nil {
		t.Fatal(err)
	}
	if WebUpgradeAllowed(dir) {
		t.Fatal("开关文件是符号链接时不应生效：只认主机所有者亲手放在数据目录里的普通文件")
	}
	// 开关目录本身也不算。
	dir2 := t.TempDir()
	if err := os.Mkdir(WebUpgradeFlagPath(dir2), 0o755); err != nil {
		t.Fatal(err)
	}
	if WebUpgradeAllowed(dir2) {
		t.Fatal("开关路径是目录时不应生效")
	}
}

func TestWebUpgradeFlagCreatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not", "yet", "there")
	if err := SetWebUpgradeAllowed(dir, true); err != nil {
		t.Fatal(err)
	}
	if !WebUpgradeAllowed(dir) {
		t.Fatal("数据目录不存在时应连同目录一起创建")
	}
}

func TestOnlineMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadOnlineMarker(dir); ok {
		t.Fatal("没写过就不应读到")
	}
	now := time.Unix(1_800_000_000, 0)
	if err := WriteOnlineMarker(dir, "1.2.3", now); err != nil {
		t.Fatal(err)
	}
	m, ok := ReadOnlineMarker(dir)
	if !ok || m.Version != "1.2.3" || m.PID != os.Getpid() || m.At != now.Unix() {
		t.Fatalf("读回的标记不对: %+v ok=%v", m, ok)
	}

	// 新的覆盖旧的。
	if err := WriteOnlineMarker(dir, "1.2.4", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if m, _ := ReadOnlineMarker(dir); m.Version != "1.2.4" {
		t.Fatalf("应读到最新的标记: %+v", m)
	}

	RemoveOnlineMarker(dir)
	if _, ok := ReadOnlineMarker(dir); ok {
		t.Fatal("清除后不应读到")
	}
	RemoveOnlineMarker(dir) // 重复清除不崩溃
}

func TestOnlineMarkerRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(UpgradeDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(UpgradeDir(dir), onlineMarkerFile)
	for _, content := range []string{"", "not json", `{"pid":1}`, `{"version":""}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if m, ok := ReadOnlineMarker(dir); ok {
			t.Errorf("%q 不应被当成有效的上线标记: %+v", content, m)
		}
	}
}

func TestUpgradeNoteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if ReadUpgradeNote(dir) != nil {
		t.Fatal("没有结果文件时应为 nil")
	}
	want := termproto.UpgradeNote{Outcome: termproto.UpgradeOutcomeRolledBack, From: "1.0.0", To: "1.1.0", At: 1_800_000_000, Detail: "新版本没有在 90 秒内连上服务端"}
	if err := WriteUpgradeNote(dir, want); err != nil {
		t.Fatal(err)
	}
	got := ReadUpgradeNote(dir)
	if got == nil || *got != want {
		t.Fatalf("读回的结果不对: %+v", got)
	}

	// 损坏的文件、不认识的结果都不能把握手带崩。
	path := filepath.Join(UpgradeDir(dir), upgradeNoteFile)
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ReadUpgradeNote(dir) != nil {
		t.Fatal("损坏的结果文件应读成 nil")
	}
	if err := os.WriteFile(path, []byte(`{"outcome":"exploded","from":"1","to":"2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := ReadUpgradeNote(dir); n != nil {
		t.Fatalf("不认识的结果应被丢弃: %+v", n)
	}
	if ReadUpgradeNote("") != nil {
		t.Fatal("数据目录为空应为 nil")
	}
}

func TestUpgradePathsLiveUnderDataDir(t *testing.T) {
	dir := "/var/lib/relaymesh-agent"
	for name, got := range map[string]string{
		"flag":   WebUpgradeFlagPath(dir),
		"dir":    UpgradeDir(dir),
		"staged": StagedBinaryPath(dir),
	} {
		if filepath.Dir(got) != dir && filepath.Dir(got) != filepath.Join(dir, "upgrade") {
			t.Errorf("%s 路径跑出了数据目录: %s", name, got)
		}
	}
	if filepath.Base(StagedBinaryPath(dir)) != "relaymesh-agent.new" {
		t.Errorf("暂存文件名变了: %s", StagedBinaryPath(dir))
	}
}
