package cursorhook

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallShSyntax(t *testing.T) {
	script := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(script, InstallSh(), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	if _, err := exec.LookPath("dash"); err == nil {
		if out, err := exec.Command("dash", "-n", script).CombinedOutput(); err != nil {
			t.Fatalf("dash -n: %v\n%s", err, out)
		}
	}
	body := string(InstallSh())
	if !strings.Contains(body, "RELAYMESH_HOOK_INSTALL_BASE") {
		t.Fatal("应文档化 RELAYMESH_HOOK_INSTALL_BASE")
	}
	if !strings.Contains(body, "sha256") && !strings.Contains(body, "SHA256") {
		t.Fatal("应校验 SHA256")
	}
}

func TestInstallShEndToEndAgainstFakeHub(t *testing.T) {
	root := filepath.Join("/tmp", "rm-hook-installsh-"+sanitizeName(t.Name()))
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "home")
	proj := filepath.Join(root, "proj")
	hookDir := filepath.Join(home, ".local", "bin")
	for _, d := range []string{home, proj, hookDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	name := "relaymesh-hook-linux-amd64"
	binPath := filepath.Join(root, "build", name)
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join("..", "..")
	if out, err := exec.Command("go", "build", "-o", binPath, "./cmd/relaymesh-hook").CombinedOutput(); err != nil {
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/relaymesh-hook")
		cmd.Dir = repo
		if out2, err2 := cmd.CombinedOutput(); err2 != nil {
			t.Fatalf("构建 hook: %v\n%s\n%s", err2, out, out2)
		}
	}
	payload, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hooks/cursor/bin/checksums.txt":
			w.Write([]byte(sums))
		case "/hooks/cursor/bin/" + name:
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	scriptPath := filepath.Join(root, "install.sh")
	if err := os.WriteFile(scriptPath, InstallSh(), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", scriptPath, "--workflow", "wf-e2e")
	cmd.Dir = proj
	cmd.Env = []string{
		"HOME=" + home, "USERPROFILE=" + home,
		"PATH=" + os.Getenv("PATH"),
		"RELAYMESH_HOOK_INSTALL_BASE=" + srv.URL,
		"TMPDIR=" + root,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	dest := filepath.Join(hookDir, name)
	got, err := os.ReadFile(dest)
	if err != nil || len(got) != len(payload) {
		t.Fatal("二进制应下载到 HOOK_DIR")
	}
	if _, err := os.Stat(filepath.Join(proj, ".cursor", "hooks.json")); err != nil {
		t.Fatalf("应登记 hooks.json: %s\n%s", err, out)
	}
	if strings.Contains(string(out), "Bearer") || strings.Contains(string(out), "token=") {
		t.Fatal("安装输出不能含令牌")
	}
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "t"
	}
	return b.String()
}
