package installer

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/relsign"
)

func testSignatureText(t *testing.T) string {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	sig, err := relsign.Sign(priv, "1.2.3", "linux-amd64", strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestDistSignatureReadsTheSidecar(t *testing.T) {
	dir := t.TempDir()
	sig := testSignatureText(t)
	writeFile(t, dir, "relaymesh-agent-linux-amd64", "linux")
	writeFile(t, dir, "relaymesh-agent-linux-amd64.sig", sig+"\n")
	d := NewDist(dir)

	got, ok := d.Signature("relaymesh-agent-linux-amd64")
	if !ok || got != sig {
		t.Fatalf("Signature = %q, %v", got, ok)
	}

	// 只认第一行、去掉首尾空白（手工生成的文件常带换行或 Windows 换行）。
	writeFile(t, dir, "relaymesh-agent-linux-amd64.sig", "  "+sig+"  \r\nsecond line is ignored\n")
	if got, ok := d.Signature("relaymesh-agent-linux-amd64"); !ok || got != sig {
		t.Fatalf("带空白与多行: %q, %v", got, ok)
	}
}

func TestDistSignatureRejectsAnythingThatIsNotASignature(t *testing.T) {
	dir := t.TempDir()
	d := NewDist(dir)
	name := "relaymesh-agent-linux-amd64"
	writeFile(t, dir, name, "linux")

	if _, ok := d.Signature(name); ok {
		t.Fatal("没有签名文件应返回 false")
	}
	for label, content := range map[string]string{
		"空文件":      "",
		"只有空白":     "  \n",
		"不是签名":     "hello world\n",
		"签名截断":     testSignatureText(t)[:60] + "\n",
		"前面多了内容":   "# comment\n" + testSignatureText(t) + "\n",
		"过大的垃圾文件":  strings.Repeat("x", maxSignatureFileBytes+1),
		"签名后面粘着垃圾": testSignatureText(t) + "garbage\n",
		"换了方案前缀":   strings.Replace(testSignatureText(t), "rmsig1:", "rmsig2:", 1),
	} {
		writeFile(t, dir, name+".sig", content)
		if got, ok := d.Signature(name); ok {
			t.Errorf("%s: 不应被接受，得到 %q", label, got)
		}
	}
}

func TestDistSignatureRefusesNonRegularFilesAndStrangeNames(t *testing.T) {
	dir := t.TempDir()
	d := NewDist(dir)
	name := "relaymesh-agent-linux-amd64"
	writeFile(t, dir, name, "linux")
	if err := os.Mkdir(filepath.Join(dir, name+".sig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Signature(name); ok {
		t.Fatal("目录不是签名文件")
	}

	sig := testSignatureText(t)
	writeFile(t, dir, "relaymesh-agent-linux-arm64.sig", sig)
	writeFile(t, dir, "outside.sig", sig)
	for _, bad := range []string{"", "../outside", "outside", "relaymesh-agent-linux-amd64.sig", "relaymesh-agent-linux-arm64.sig", "relaymesh-agent-freebsd-amd64", "relaymesh-agent-linux-amd64/../x"} {
		if got, ok := d.Signature(bad); ok {
			t.Errorf("Signature(%q) 不应被接受: %q", bad, got)
		}
	}
	// 只放了签名、没有程序本身：Signature 只看签名文件，是否有程序由调用方（List / Checksums）判断。
	if got, ok := d.Signature("relaymesh-agent-linux-arm64"); !ok || got != sig {
		t.Errorf("签名文件本身合规时应读到: %q %v", got, ok)
	}
}

func TestDistSignatureFollowsOperatorSymlinks(t *testing.T) {
	src := t.TempDir()
	dir := t.TempDir()
	sig := testSignatureText(t)
	target := writeFile(t, src, "built.sig", sig+"\n")
	if err := os.Symlink(target, filepath.Join(dir, "relaymesh-agent-linux-amd64.sig")); err != nil {
		t.Skip("无法创建符号链接: ", err)
	}
	if got, ok := NewDist(dir).Signature("relaymesh-agent-linux-amd64"); !ok || got != sig {
		t.Fatalf("部署方做的符号链接应可读: %q %v", got, ok)
	}
}

func TestDistSignatureOnMissingDist(t *testing.T) {
	var nilDist *Dist
	for label, d := range map[string]*Dist{"nil": nilDist, "空路径": NewDist(""), "目录不存在": NewDist(filepath.Join(t.TempDir(), "nope"))} {
		if got, ok := d.Signature("relaymesh-agent-linux-amd64"); ok {
			t.Errorf("%s: 应返回 false，得到 %q", label, got)
		}
	}
}

// 签名文件不是可分发的文件：/i/ 的白名单（List / Open / Checksums）都不会发出它。
func TestDistNeverServesSignatureSidecars(t *testing.T) {
	dir := t.TempDir()
	name := "relaymesh-agent-linux-amd64"
	writeFile(t, dir, name, "linux")
	writeFile(t, dir, name+".sig", testSignatureText(t))
	d := NewDist(dir)

	if got := d.List(); len(got) != 1 || got[0].Name != name {
		t.Fatalf("List = %+v", got)
	}
	if _, _, err := d.Open(name + ".sig"); err == nil {
		t.Fatal("不应能经 Open 取到签名文件")
	}
	sums, err := d.Checksums()
	if err != nil || strings.Contains(string(sums), ".sig") {
		t.Fatalf("校验文件里不应出现签名文件: %q %v", sums, err)
	}
}
