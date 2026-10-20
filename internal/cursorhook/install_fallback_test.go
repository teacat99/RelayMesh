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
	"runtime"
	"strings"
	"sync"
	"testing"
)

// 本文件覆盖 install.sh 的来源顺序：自建镜像 → 本 Hub → GitHub Release（Hub 没有 agent-dist 时的回退）。
// 回退要走真实的 GitHub 地址格式，所以用一个 curl 外壳把发往 https://github.com/ 的请求改到本地假站点，
// 同时记下每个请求的原始地址，用来断言「到底去了哪里、取的哪个版本」。

// hookAssetName 是本机平台对应的 hook 二进制名，与 install.sh 按 uname 算出的一致。
func hookAssetName(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("需要 curl")
	}
	switch runtime.GOOS {
	case "linux", "darwin":
	default:
		t.Skipf("install.sh 只在 linux / darwin 上测试（当前 %s）", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		t.Skipf("install.sh 不支持的架构 %s", runtime.GOARCH)
	}
	return "relaymesh-hook-" + runtime.GOOS + "-" + runtime.GOARCH
}

var (
	hookOnce    sync.Once
	hookPayload []byte
	hookErr     error
)

// realHookBinary 构建一次真实的 relaymesh-hook，本包的所有测试复用。
func realHookBinary(t *testing.T) []byte {
	t.Helper()
	hookOnce.Do(func() {
		dir, err := os.MkdirTemp("", "rm-hook-build-")
		if err != nil {
			hookErr = err
			return
		}
		defer os.RemoveAll(dir)
		out := filepath.Join(dir, "relaymesh-hook")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/relaymesh-hook")
		cmd.Dir = filepath.Join("..", "..")
		if b, err := cmd.CombinedOutput(); err != nil {
			hookErr = fmt.Errorf("构建 hook: %v\n%s", err, b)
			return
		}
		hookPayload, hookErr = os.ReadFile(out)
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	return hookPayload
}

func checksumsFor(name string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
}

// fakeSite 按「路径 → 内容」出文件，其余 404，并记下收到的路径。
type fakeSite struct {
	*httptest.Server
	mu   sync.Mutex
	hits []string
}

func newFakeSite(t *testing.T, files map[string][]byte) *fakeSite {
	t.Helper()
	f := &fakeSite{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.URL.Path)
		f.mu.Unlock()
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSite) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hits)
}

// curlShim 在发往 https://github.com/ 时改道到 $SHIM_GITHUB，其余原样转发给真 curl；所有 http(s) 地址追加到 $SHIM_LOG。
const curlShim = `#!/bin/sh
for a in "$@"; do
  case "$a" in http://*|https://*) echo "$a" >> "$SHIM_LOG" ;; esac
done
n=$#
while [ "$n" -gt 0 ]; do
  a="$1"; shift
  case "$a" in
    https://github.com/*) a="$SHIM_GITHUB/${a#https://github.com/}" ;;
  esac
  set -- "$@" "$a"
  n=$((n-1))
done
exec "$SHIM_REAL_CURL" "$@"
`

type installResult struct {
	out     string
	err     error
	root    string // 临时根目录（TMPDIR、HOME 都在里面）
	hookDir string
	proj    string
	curlLog []string // 脚本发出的每个 http(s) 地址（改道之前的原始地址）
}

// runInstallSh 在隔离的 HOME / 项目目录里跑 install.sh（curl 走外壳）；env 里的变量覆盖默认值，值为空串表示不设置。
func runInstallSh(t *testing.T, script []byte, github *fakeSite, env map[string]string) installResult {
	t.Helper()
	realCurl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("需要 curl")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	proj := filepath.Join(root, "proj")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{home, proj, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curlShim), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(root, "install.sh")
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "curl.log")
	vars := map[string]string{
		"HOME":           home,
		"USERPROFILE":    home,
		"PATH":           bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMPDIR":         root,
		"SHIM_LOG":       logPath,
		"SHIM_GITHUB":    github.URL,
		"SHIM_REAL_CURL": realCurl,
	}
	for k, v := range env {
		vars[k] = v
	}
	cmd := exec.Command("sh", scriptPath, "--workflow", "wf-fallback")
	cmd.Dir = proj
	for k, v := range vars {
		if v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	out, runErr := cmd.CombinedOutput()
	res := installResult{out: string(out), err: runErr, root: root, hookDir: filepath.Join(home, ".local", "bin"), proj: proj}
	if b, err := os.ReadFile(logPath); err == nil {
		res.curlLog = strings.Fields(string(b))
	}
	return res
}

func (r installResult) logHas(sub string) bool {
	for _, l := range r.curlLog {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func (r installResult) logCount(sub string) int {
	n := 0
	for _, l := range r.curlLog {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func (r installResult) mustInstall(t *testing.T, name string, payload []byte) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("install.sh 应成功：%v\n%s", r.err, r.out)
	}
	got, err := os.ReadFile(filepath.Join(r.hookDir, name))
	if err != nil || len(got) != len(payload) {
		t.Fatalf("二进制应下载到 HOOK_DIR：%v\n%s", err, r.out)
	}
	if _, err := os.Stat(filepath.Join(r.proj, ".cursor", "hooks.json")); err != nil {
		t.Fatalf("应登记 hooks.json：%v\n%s", err, r.out)
	}
	if left, _ := filepath.Glob(filepath.Join(r.root, "relaymesh-hook.*")); len(left) != 0 {
		t.Fatalf("临时文件应清理干净：%v", left)
	}
}

func (r installResult) mustFail(t *testing.T, name string) {
	t.Helper()
	if r.err == nil {
		t.Fatalf("install.sh 应失败\n%s", r.out)
	}
	if _, err := os.Stat(filepath.Join(r.hookDir, name)); err == nil {
		t.Fatalf("失败时不能留下二进制：%s", filepath.Join(r.hookDir, name))
	}
}

// 带 agent-dist 的 Hub：照旧从 Hub 下载，不碰 GitHub，也不打多余的提示。
func TestInstallShHubWithDistDoesNotFallBack(t *testing.T) {
	name := hookAssetName(t)
	payload := realHookBinary(t)
	hub := newFakeSite(t, map[string][]byte{
		"/hooks/cursor/bin/checksums.txt": []byte(checksumsFor(name, payload)),
		"/hooks/cursor/bin/" + name:       payload,
	})
	github := newFakeSite(t, nil)
	res := runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{"RELAYMESH_HOOK_INSTALL_BASE": hub.URL})
	res.mustInstall(t, name, payload)
	if res.logHas("github.com") || github.hitCount() != 0 {
		t.Fatalf("Hub 有 hook 时不应访问 GitHub：%v", res.curlLog)
	}
	if strings.Contains(res.out, "GitHub Release") {
		t.Fatalf("不应提示回退：\n%s", res.out)
	}
}

// Hub 没有 agent-dist（404）+ Hub 是正式版本 → 取同版本的 Release，并校验 SHA256。
func TestInstallShFallsBackToPinnedRelease(t *testing.T) {
	name := hookAssetName(t)
	payload := realHookBinary(t)
	hub := newFakeSite(t, nil)
	github := newFakeSite(t, map[string][]byte{
		"/teacat99/RelayMesh/releases/download/v2.0.0/checksums.txt": []byte(checksumsFor(name, payload)),
		"/teacat99/RelayMesh/releases/download/v2.0.0/" + name:       payload,
	})
	res := runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{"RELAYMESH_HOOK_INSTALL_BASE": hub.URL})
	res.mustInstall(t, name, payload)
	for _, want := range []string{
		"https://github.com/teacat99/RelayMesh/releases/download/v2.0.0/checksums.txt",
		"https://github.com/teacat99/RelayMesh/releases/download/v2.0.0/" + name,
	} {
		if !res.logHas(want) {
			t.Errorf("应请求 %s，实际：%v", want, res.curlLog)
		}
	}
	if res.logHas("releases/latest") {
		t.Errorf("正式版本应取同版本，不应取 latest：%v", res.curlLog)
	}
	for _, want := range []string{"改从 GitHub Release 下载", "GitHub Release v2.0.0", "SHA256 校验通过"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("提示里应有 %q：\n%s", want, res.out)
		}
	}
}

// Hub 是开发版 / 版本未知 → 没有可对应的 Release，取最新，并提醒版本可能不一致。
func TestInstallShFallsBackToLatestWhenHubIsNotAFinalRelease(t *testing.T) {
	name := hookAssetName(t)
	payload := realHookBinary(t)
	for _, hubVersion := range []string{"v1.6.3-dev-20261020-15e4726", "dev", ""} {
		hub := newFakeSite(t, nil)
		github := newFakeSite(t, map[string][]byte{
			"/teacat99/RelayMesh/releases/latest/download/checksums.txt": []byte(checksumsFor(name, payload)),
			"/teacat99/RelayMesh/releases/latest/download/" + name:       payload,
		})
		res := runInstallSh(t, RenderInstallSh(hubVersion), github, map[string]string{"RELAYMESH_HOOK_INSTALL_BASE": hub.URL})
		res.mustInstall(t, name, payload)
		if !res.logHas("https://github.com/teacat99/RelayMesh/releases/latest/download/"+name) || res.logHas("releases/download/") {
			t.Errorf("Hub 版本 %q 应取最新 Release：%v", hubVersion, res.curlLog)
		}
		if !strings.Contains(res.out, "取最新 Release") {
			t.Errorf("应提醒版本可能不一致：\n%s", res.out)
		}
	}
}

// RELAYMESH_VERSION 与 RELAYMESH_REPO 可以覆盖 Hub 写入的版本与默认仓库；latest 表示最新。
func TestInstallShFallbackVersionAndRepoOverrides(t *testing.T) {
	name := hookAssetName(t)
	payload := realHookBinary(t)
	files := map[string][]byte{}
	for _, p := range []string{
		"/acme/Fork/releases/download/v1.9.0/",
		"/teacat99/RelayMesh/releases/latest/download/",
	} {
		files[p+"checksums.txt"] = []byte(checksumsFor(name, payload))
		files[p+name] = payload
	}
	hub := newFakeSite(t, nil)
	github := newFakeSite(t, files)

	res := runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{
		"RELAYMESH_HOOK_INSTALL_BASE": hub.URL,
		"RELAYMESH_VERSION":           "v1.9.0",
		"RELAYMESH_REPO":              "acme/Fork",
	})
	res.mustInstall(t, name, payload)
	if !res.logHas("https://github.com/acme/Fork/releases/download/v1.9.0/" + name) {
		t.Errorf("应取 acme/Fork 的 v1.9.0：%v", res.curlLog)
	}

	res = runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{
		"RELAYMESH_HOOK_INSTALL_BASE": hub.URL,
		"RELAYMESH_VERSION":           "latest",
	})
	res.mustInstall(t, name, payload)
	if !res.logHas("https://github.com/teacat99/RelayMesh/releases/latest/download/"+name) || res.logHas("releases/download/") {
		t.Errorf("RELAYMESH_VERSION=latest 应取最新：%v", res.curlLog)
	}
}

// 校验失败直接退出：不换源重试，也不留下二进制。
func TestInstallShFallbackRejectsChecksumMismatch(t *testing.T) {
	name := hookAssetName(t)
	payload := realHookBinary(t)
	hub := newFakeSite(t, nil)
	github := newFakeSite(t, map[string][]byte{
		"/teacat99/RelayMesh/releases/download/v2.0.0/checksums.txt": []byte(checksumsFor(name, []byte("不是这个文件"))),
		"/teacat99/RelayMesh/releases/download/v2.0.0/" + name:       payload,
	})
	res := runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{"RELAYMESH_HOOK_INSTALL_BASE": hub.URL})
	res.mustFail(t, name)
	if !strings.Contains(res.out, "SHA256 校验失败") {
		t.Errorf("应报校验失败：\n%s", res.out)
	}
	if n := res.logCount("checksums.txt"); n != 2 { // Hub 一次（404）+ GitHub 一次
		t.Errorf("校验失败后不能重试：checksums.txt 请求 %d 次，%v", n, res.curlLog)
	}
}

// Release 的校验文件里没有这个平台的条目：拒绝安装，不能放行没有校验的二进制。
func TestInstallShFallbackRequiresChecksumEntry(t *testing.T) {
	name := hookAssetName(t)
	payload := realHookBinary(t)
	hub := newFakeSite(t, nil)
	github := newFakeSite(t, map[string][]byte{
		"/teacat99/RelayMesh/releases/download/v2.0.0/checksums.txt": []byte(checksumsFor("relaymesh-hook-plan9-amd64", payload)),
		"/teacat99/RelayMesh/releases/download/v2.0.0/" + name:       payload,
	})
	res := runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{"RELAYMESH_HOOK_INSTALL_BASE": hub.URL})
	res.mustFail(t, name)
	if !strings.Contains(res.out, "校验文件里没有 "+name) {
		t.Errorf("应说明校验文件里没有该平台条目：\n%s", res.out)
	}
}

// Hub 与 GitHub 都下不到：说清楚试过哪里，并给出手动安装与「改取最新」的办法。
func TestInstallShFallbackBothSourcesFail(t *testing.T) {
	name := hookAssetName(t)
	hub := newFakeSite(t, nil)
	github := newFakeSite(t, nil)
	res := runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{"RELAYMESH_HOOK_INSTALL_BASE": hub.URL})
	res.mustFail(t, name)
	for _, want := range []string{
		"从 GitHub Release v2.0.0 下载 " + name + " 失败",
		"RELAYMESH_VERSION=latest",
		"https://github.com/teacat99/RelayMesh/releases",
		"chmod +x",
		"install --workflow",
	} {
		if !strings.Contains(res.out, want) {
			t.Errorf("提示里应有 %q：\n%s", want, res.out)
		}
	}
}

// 自建镜像：只用镜像，不碰 Hub 与 GitHub；镜像失败不会悄悄改去 GitHub。
func TestInstallShMirrorOnly(t *testing.T) {
	name := hookAssetName(t)
	payload := realHookBinary(t)
	hub := newFakeSite(t, nil)
	github := newFakeSite(t, nil)
	mirror := newFakeSite(t, map[string][]byte{
		"/dl/checksums.txt": []byte(checksumsFor(name, payload)),
		"/dl/" + name:       payload,
	})
	res := runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{
		"RELAYMESH_HOOK_INSTALL_BASE": hub.URL,
		"RELAYMESH_DOWNLOAD_BASE":     mirror.URL + "/dl/",
	})
	res.mustInstall(t, name, payload)
	if hub.hitCount() != 0 || github.hitCount() != 0 || mirror.hitCount() != 2 {
		t.Errorf("只应访问镜像：hub=%d github=%d mirror=%d %v", hub.hitCount(), github.hitCount(), mirror.hitCount(), res.curlLog)
	}

	// 只给镜像、不给 Hub 地址也能装；镜像里没有文件时报错，且不改去 GitHub。
	empty := newFakeSite(t, nil)
	res = runInstallSh(t, RenderInstallSh("v2.0.0"), github, map[string]string{"RELAYMESH_DOWNLOAD_BASE": empty.URL})
	res.mustFail(t, name)
	if !strings.Contains(res.out, "自建镜像") || github.hitCount() != 0 || res.logHas("github.com") {
		t.Errorf("镜像失败应报错而不是改去 GitHub：%v\n%s", res.curlLog, res.out)
	}
}

// Hub 地址与镜像都没给：照旧报错。
func TestInstallShNeedsHubBaseOrMirror(t *testing.T) {
	hookAssetName(t)
	res := runInstallSh(t, RenderInstallSh("v2.0.0"), newFakeSite(t, nil), nil)
	if res.err == nil || !strings.Contains(res.out, "RELAYMESH_HOOK_INSTALL_BASE") {
		t.Fatalf("应提示设置 RELAYMESH_HOOK_INSTALL_BASE：%v\n%s", res.err, res.out)
	}
	if len(res.curlLog) != 0 {
		t.Fatalf("没有地址时不应发任何请求：%v", res.curlLog)
	}
}

// Hub 写入的版本号只允许 vX.Y.Z 与 vX.Y.Z-alpha|beta|rc.N（预览版有自己的 Release）；
// 任何别的写法（含开发版、想往脚本里塞内容的）一律留空。
func TestReleaseTag(t *testing.T) {
	cases := map[string]string{
		"v2.0.0":                      "v2.0.0",
		"2.0.0":                       "v2.0.0",
		"  v10.20.30  ":               "v10.20.30",
		"":                            "",
		"dev":                         "",
		"v2.0":                        "",
		"v2.0.0-rc.1":                 "v2.0.0-rc.1",
		"2.0.0-rc.12":                 "v2.0.0-rc.12",
		"v2.0.0-beta.2":               "v2.0.0-beta.2",
		"v2.0.0-alpha.10":             "v2.0.0-alpha.10",
		"v2.0.0-rc":                   "",
		"v2.0.0-rc.":                  "",
		"v2.0.0-rc.1.2":               "",
		"v2.0.0-RC.1":                 "",
		"v2.0.0-pre.1":                "",
		"v2.0.0-rc.1000":              "",
		"v2.0.0-rc.1+build":           "",
		"v2.0.0-rc.1\nrm -rf /":       "",
		"v2.0.0-rc.1'; id; '":         "",
		"v1.6.3-dev-20261020-15e4726": "",
		"v2.0.0\n":                    "v2.0.0", // 首尾空白会被去掉；写进脚本的永远是重新拼出来的 vX.Y.Z
		"v2.0.0\nrm -rf /":            "",
		"v2.0.0'; rm -rf /; echo '":   "",
		"v2.0.0 && id":                "",
		"$(id)":                       "",
		"v12345.0.0":                  "",
	}
	for in, want := range cases {
		if got := ReleaseTag(in); got != want {
			t.Errorf("ReleaseTag(%q) = %q，想要 %q", in, got, want)
		}
	}
}

// 原文里恰好一个渲染标记，渲染后不留标记，两种脚本都一样；原文本身仍是「留空 = 最新」。
func TestInstallScriptsVersionMarker(t *testing.T) {
	for name, raw := range map[string][]byte{"install.sh": InstallSh(), "install.ps1": InstallPs1()} {
		if n := strings.Count(string(raw), hubVersionMarker); n != 1 {
			t.Fatalf("%s 应恰好有 1 个渲染标记，实际 %d", name, n)
		}
	}
	sh := string(RenderInstallSh("v2.0.0"))
	ps1 := string(RenderInstallPs1("2.0.0"))
	if !strings.Contains(sh, "\nHUB_VERSION='v2.0.0'\n") || strings.Contains(sh, hubVersionMarker) {
		t.Errorf("install.sh 渲染结果不对")
	}
	if !strings.Contains(ps1, "\n$hubVersion = 'v2.0.0'\n") || strings.Contains(ps1, hubVersionMarker) {
		t.Errorf("install.ps1 渲染结果不对")
	}
	if strings.Count(sh, "\n") != strings.Count(string(InstallSh()), "\n") {
		t.Errorf("渲染不应增减行数")
	}
	empty := string(RenderInstallSh("dev"))
	if !strings.Contains(empty, "\nHUB_VERSION=''\n") {
		t.Errorf("非正式版本应写入空串")
	}
	// 渲染后的脚本语法仍然正确（含 dash）。
	script := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(script, []byte(sh), 0o755); err != nil {
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
}

// PowerShell 脚本没法在这里执行，至少锁住它与 install.sh 一致的约定：来源顺序、Release 地址格式、校验与 TLS 设置。
// 机器上有 pwsh 时再做一次真实的语法解析。
func TestInstallPs1FallbackContract(t *testing.T) {
	body := string(RenderInstallPs1("v2.0.0"))
	for _, want := range []string{
		"RELAYMESH_HOOK_INSTALL_BASE", "RELAYMESH_DOWNLOAD_BASE", "RELAYMESH_VERSION", "RELAYMESH_REPO",
		"https://github.com/$repo/releases/latest/download",
		"https://github.com/$repo/releases/download/$tag",
		"checksums.txt", "Get-FileHash", "SHA256 校验失败", "Tls12", "/hooks/cursor/bin",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("install.ps1 应包含 %q", want)
		}
	}
	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		script := filepath.Join(t.TempDir(), "install.ps1")
		if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		// -Command 之后的所有参数都会被拼进命令文本，所以路径直接写进命令里（t.TempDir 的路径不含单引号）。
		parse := fmt.Sprintf(`$e = $null; [void][System.Management.Automation.Language.Parser]::ParseFile('%s', [ref]$null, [ref]$e); if ($e.Count -gt 0) { $e | ForEach-Object { $_.Message }; exit 1 }`, script)
		if out, err := exec.Command(pwsh, "-NoProfile", "-Command", parse).CombinedOutput(); err != nil {
			t.Fatalf("pwsh 语法解析失败：%v\n%s", err, out)
		}
	}
}
