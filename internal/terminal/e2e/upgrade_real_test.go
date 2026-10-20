//go:build e2e && linux

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// 一台已经装好旧版本 Agent 的主机，通过「升级窗口」升级到服务端自带的新版本——全程用真实进程：
//
//	Hub（带一份新版本的分发目录） ← 旧版本 Agent + 会话守护进程（由 `install --user` 真实安装）
//	浏览器一侧：打开升级窗口 → 取到的 install.sh 经 curl 下载 → `agent upgrade` 真实执行
//
// 唯一的替身是 systemctl：测试里的 `systemctl --user` 是一个小脚本，按安装器写出的单元文件里的 ExecStart
// 启停进程。这样既验证安装器真实生成的单元，也不会碰这台机器上的真实服务（HOME、XDG_*、PATH 全部指向临时目录）。
//
// 要验证的承诺：
//  1. 升级后程序是新版本、Hub 里的版本跟着更新、升级窗口自动关闭、票据立刻失效；
//  2. 服务配置一个字节都没变；
//  3. 会话守护进程没有被重启（进程号不变），里面的 Shell 会话原样存活——这是「升级不打断正在运行的命令」的底线。

// 最小的 `systemctl --user` 替身：只认识安装 / 升级会用到的子命令。
const fakeSystemctl = `#!/bin/sh
state="${RM_FAKE_SYSD:?}"
[ "${1:-}" = "--user" ] && shift
cmd="${1:-}"
[ $# -gt 0 ] && shift
unit="${1:-}"
unitfile="${XDG_CONFIG_HOME:?}/systemd/user/${unit}"
pidfile="${state}/${unit}.pid"
alive() { [ -f "${pidfile}" ] && kill -0 "$(cat "${pidfile}")" 2>/dev/null; }
case "${cmd}" in
  start | restart)
    [ -f "${unitfile}" ] || { echo "Unit ${unit} not found." >&2; exit 5; }
    if alive; then
      [ "${cmd}" = "start" ] && exit 0
      _p="$(cat "${pidfile}")"
      kill "${_p}" 2>/dev/null || true
      _i=0
      while kill -0 "${_p}" 2>/dev/null && [ "${_i}" -lt 100 ]; do sleep 0.1; _i=$((_i + 1)); done
      kill -9 "${_p}" 2>/dev/null || true
    fi
    _line="$(sed -n 's/^ExecStart=//p' "${unitfile}" | head -n 1)"
    nohup sh -c "exec ${_line}" >>"${state}/${unit}.log" 2>&1 </dev/null &
    echo $! >"${pidfile}"
    exit 0
    ;;
  is-active)
    if alive; then echo active; exit 0; fi
    echo inactive
    exit 3
    ;;
  stop | disable)
    if alive; then kill "$(cat "${pidfile}")" 2>/dev/null || true; fi
    exit 0
    ;;
  *) exit 0 ;;
esac
`

// cleanEnv 复制当前环境，去掉会影响 Agent 行为或指向真实用户目录的变量。
func cleanEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if strings.HasPrefix(key, "RELAYMESH_") || strings.HasPrefix(key, "XDG_") || key == "HOME" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func copyExec(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

func pidOf(t *testing.T, sysd, unit string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sysd, unit+".pid"))
	if err != nil {
		t.Fatalf("读取 %s 的进程号失败: %v", unit, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("进程号不是数字: %q", b)
	}
	return pid
}

func TestRealAgentUpgradeThroughTheUpgradeWindow(t *testing.T) {
	hubBin, _ := binaries(t)
	oldBin, newBin := os.Getenv("RM_E2E_AGENT_OLD"), os.Getenv("RM_E2E_AGENT_NEW")
	if oldBin == "" || newBin == "" {
		t.Skip("设置 RM_E2E_AGENT_OLD / RM_E2E_AGENT_NEW 为两个不同版本的 relaymesh-agent 后运行（scripts/e2e-holder/run.sh 会构建）")
	}
	for _, tool := range []string{"curl", "sh", "sed", "nohup"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("需要 %s", tool)
		}
	}
	const oldVer, newVer = "v1.6.0", "v1.7.0"

	// 服务端自带的分发目录：新版本的 Agent + VERSION。
	dist := t.TempDir()
	copyExec(t, newBin, filepath.Join(dist, "relaymesh-agent-linux-"+runtime.GOARCH))
	if err := os.WriteFile(filepath.Join(dist, "VERSION"), []byte(newVer+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 代码指纹（#143）：scripts/e2e-holder/run.sh 用 -X main.rev 给旧 / 新两个二进制编进不同的指纹；
	// 没带指纹的构建（手动设置 RM_E2E_AGENT_OLD / NEW 运行本测试）跳过指纹相关的断言，仍按版本号判断。
	revOld, revNew := os.Getenv("RM_E2E_REV_OLD"), os.Getenv("RM_E2E_REV_NEW")
	withRev := revOld != "" && revNew != ""
	revBump := 0
	setDistRev := func(rev string) {
		t.Helper()
		p := filepath.Join(dist, "REV")
		if err := os.WriteFile(p, []byte(rev+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// 服务端按「大小 + 修改时间」缓存 REV：指纹长度相同，所以显式推进修改时间，保证改动立刻被看到。
		revBump++
		at := time.Now().Add(time.Duration(revBump) * time.Minute)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if withRev {
		setDistRev(revNew)
		t.Logf("指纹断言已启用：旧 %s → 新 %s", revOld, revNew)
	} else {
		t.Log("没有 RM_E2E_REV_OLD / RM_E2E_REV_NEW：跳过指纹断言，只按版本号判断")
	}
	h := startHub(t, hubBin, "RELAYMESH_AGENT_DIST_DIR="+dist)

	// 假的家目录与假的 systemctl。路径刻意很短：守护进程的本地套接字有长度上限。
	root, err := os.MkdirTemp("", "rmu")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "h")
	sysd, shim := filepath.Join(root, "sysd"), filepath.Join(root, "bin")
	for _, d := range []string{home, sysd, shim} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(shim, "systemctl"), []byte(fakeSystemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := filepath.Glob(filepath.Join(sysd, "*.log"))
			for _, l := range logs {
				t.Logf("---- %s ----\n%s", filepath.Base(l), tail(l, 30))
			}
		}
		pids, _ := filepath.Glob(filepath.Join(sysd, "*.pid"))
		for _, f := range pids {
			b, _ := os.ReadFile(f)
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if pid <= 1 {
				continue
			}
			// 只清理确实是本测试起的进程：命令行里要有假家目录。
			if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && strings.Contains(string(cmdline), home) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		_ = os.RemoveAll(root)
	})
	// 数据 / 配置目录必须显式指到假家目录：以 root 运行时，Agent 的默认数据目录是 /var/lib/relaymesh-agent，
	// 完全不理会 HOME / XDG_*——漏了这两行，旧版 Agent 与守护进程就会接上这台机器上真实在用的守护进程
	// （改写真实会话的记录、把真实 Agent 挤下线，见 .cursor/decisions.md D-213）。服务单元由安装器按这两个值写出 --data-dir。
	env := cleanEnv(
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"RELAYMESH_AGENT_DATA_DIR="+filepath.Join(home, "data"),
		"RELAYMESH_AGENT_CONFIG_DIR="+filepath.Join(home, "config"),
		"PATH="+shim+":"+os.Getenv("PATH"),
		"RM_FAKE_SYSD="+sysd,
	)
	run := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// 先确认隔离真的生效：用与服务完全相同的环境探测守护进程。此刻不该有任何守护进程；
	// 若连上了，说明数据目录解析到了别处（例如这台机器上真实在用的那个），继续安装只会去碰真实服务，必须立刻停下。
	if out, err := run(oldBin, "holder", "status"); err == nil {
		t.Fatalf("隔离失效：安装之前就探测到一个正在运行的会话守护进程，拒绝继续（避免碰到真实服务）:\n%s", out)
	}

	// —— 1. 真实安装旧版本（用户级；--no-linger 避免对真实系统执行 loginctl）——
	if out, err := run(oldBin, "install", "--user", "--no-linger", "--server", h.agentURL(), "--pin", h.pin); err != nil {
		t.Fatalf("安装旧版本失败: %v\n%s", err, out)
	}
	installed := filepath.Join(home, ".local", "bin", "relaymesh-agent")
	unitPath := filepath.Join(home, ".config", "systemd", "user", "relaymesh-agent.service")
	holderUnitPath := filepath.Join(home, ".config", "systemd", "user", "relaymesh-holder.service")
	unitBefore, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	holderUnitBefore, err := os.ReadFile(holderUnitPath)
	if err != nil {
		t.Fatal(err)
	}

	code := h.api.enrollOnly()
	v := waitHost(t, h, code, "旧版本主机上线且守护进程就绪", func(v terminal.HostView) bool {
		return v.Online && v.Holder != nil && v.Holder.State == "ready"
	})
	if v.AgentVersion != oldVer {
		t.Fatalf("前提：主机应报告旧版本 %s，实际 %q", oldVer, v.AgentVersion)
	}
	// 升级提醒：服务端自带的版本更新，主机被标为「更旧」。
	if !v.UpgradeAvailable || v.UpgradeKind != "older" || v.LatestAgentVersion != newVer {
		t.Fatalf("旧版本主机应被提醒升级: available=%v kind=%q latest=%q", v.UpgradeAvailable, v.UpgradeKind, v.LatestAgentVersion)
	}
	if withRev {
		// 真实二进制经 -X main.rev 编进的指纹：Agent 握手自报、守护进程经 ready 帧上报，服务端自带的指纹来自 REV 文件。
		if v.AgentRev != revOld || v.Holder.Rev != revOld || v.LatestAgentRev != revNew {
			t.Fatalf("指纹应贯通 Agent / 守护进程 / 分发目录: agent=%q holder=%q latest=%q（想要 %q / %q / %q）",
				v.AgentRev, v.Holder.Rev, v.LatestAgentRev, revOld, revOld, revNew)
		}
		// 服务端自带的 Agent 与这台主机的代码一致（只是版本号 v1.7.0 与 v1.6.0 不同）：不该提醒升级，并明说代码一致。
		setDistRev(revOld)
		same := waitHost(t, h, code, "服务端自带的指纹与主机一致", func(v terminal.HostView) bool { return v.LatestAgentRev == revOld })
		if same.UpgradeAvailable || !same.UpToDateByRev {
			t.Fatalf("指纹一致时不该提醒升级，并应标明代码一致: available=%v kind=%q upToDateByRev=%v", same.UpgradeAvailable, same.UpgradeKind, same.UpToDateByRev)
		}
		// 服务端换了一批代码不同的 Agent：重新提醒。
		setDistRev(revNew)
		waitHost(t, h, code, "服务端自带的指纹换回新的，重新提醒升级", func(v terminal.HostView) bool {
			return v.LatestAgentRev == revNew && v.UpgradeAvailable && v.UpgradeKind == "older" && !v.UpToDateByRev
		})
	}

	// 一个正在运行的 Shell 会话，带着只在这个 Shell 里存在的变量。
	sess := h.api.openSession(code, "upgrade-e2e")
	tc := h.api.attach(h, code, sess.SID, 0)
	marker := "kept-" + randHex(4)
	tc.send("export RM_MARK=" + marker + "; echo SH:$$:END\r")
	tc.waitFor(fmt.Sprintf("SH:%d:END", sess.PID), 10*time.Second)
	tc.close()
	agentPIDBefore, holderPIDBefore := pidOf(t, sysd, "relaymesh-agent.service"), pidOf(t, sysd, "relaymesh-holder.service")

	// —— 2. 打开升级窗口（先关掉发现窗口：之后能取到安装文件的只可能是升级票据）——
	if c := h.api.call("DELETE", "/discovery", nil, nil); c != 200 {
		t.Fatalf("关闭发现窗口失败: HTTP %d", c)
	}
	var ow struct {
		UpgradeWindow *terminal.UpgradeWindowView `json:"upgrade_window"`
	}
	if c := h.api.call("POST", "/hosts/"+code+"/upgrade-window", map[string]any{}, &ow); c != 200 || ow.UpgradeWindow == nil || !ow.UpgradeWindow.Open {
		t.Fatalf("开启升级窗口失败: HTTP %d %+v", c, ow)
	}
	ticket := ow.UpgradeWindow.Ticket
	if ow.UpgradeWindow.RemainingSeconds < 590 || ow.UpgradeWindow.RemainingSeconds > 600 {
		t.Fatalf("默认应是 10 分钟: %d 秒", ow.UpgradeWindow.RemainingSeconds)
	}
	if hv, _ := h.api.host(code); hv.UpgradeWindow == nil || hv.UpgradeWindow.Ticket != ticket {
		t.Fatalf("主机列表里应带着升级窗口: %+v", hv.UpgradeWindow)
	}

	// —— 3. 按 Web 上给出的命令形态真实执行 ——
	script := fmt.Sprintf("curl -fsSL %s/i/%s/install.sh | sh -s -- agent upgrade --user --server %s", h.base(), ticket, h.base())
	out, err := run("sh", "-c", script)
	if err != nil || !strings.Contains(out, "已升级") {
		t.Fatalf("升级命令失败: %v\n%s", err, out)
	}
	t.Logf("升级输出:\n%s", out)

	// 程序已经是新版本，服务配置一个字节都没变，安装目录里没有留下临时文件。
	if vOut, err := run(installed, "version"); err != nil || !strings.Contains(vOut, newVer) {
		t.Fatalf("已安装的程序应是新版本: %v %q", err, vOut)
	}
	if b, _ := os.ReadFile(unitPath); string(b) != string(unitBefore) {
		t.Fatal("升级不应改动 Agent 的服务配置")
	}
	if b, _ := os.ReadFile(holderUnitPath); string(b) != string(holderUnitBefore) {
		t.Fatal("升级不应改动守护进程的服务配置")
	}
	if left, _ := filepath.Glob(filepath.Join(home, ".local", "bin", ".*upgrade*")); len(left) != 0 {
		t.Fatalf("不应留下临时文件: %v", left)
	}

	// Hub 一侧：版本更新、不再提醒、升级窗口自动关闭、票据立刻失效。
	got := waitHost(t, h, code, "主机带着新版本回来", func(v terminal.HostView) bool {
		return v.Online && v.AgentVersion == newVer && v.Holder != nil && v.Holder.State == "ready"
	})
	if got.UpgradeAvailable || got.UpgradeWindow != nil {
		t.Fatalf("升级后不应再提醒，升级窗口应已自动关闭: available=%v window=%+v", got.UpgradeAvailable, got.UpgradeWindow)
	}
	if withRev {
		// Agent 换成了新代码；守护进程没有重启，仍是旧代码——界面据这两个指纹的差异标「守护进程较旧」。
		if got.AgentRev != revNew || got.Holder == nil || got.Holder.Rev != revOld {
			t.Fatalf("升级后 Agent 的指纹应换成新的、守护进程的仍是旧的: agent=%q holder=%+v（想要 %q / %q）", got.AgentRev, got.Holder, revNew, revOld)
		}
	}
	resp, err := http.Get(h.base() + "/i/" + ticket + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("窗口关闭后票据应失效，实际 HTTP %d", resp.StatusCode)
	}

	// —— 4. 底线：Agent 换了、守护进程没动、Shell 会话原样存活 ——
	if now := pidOf(t, sysd, "relaymesh-agent.service"); now == agentPIDBefore {
		t.Fatal("Agent 应已被重启")
	}
	if now := pidOf(t, sysd, "relaymesh-holder.service"); now != holderPIDBefore || !pidAlive(holderPIDBefore) {
		t.Fatalf("会话守护进程不应被重启（进程号 %d → %d）", holderPIDBefore, now)
	}
	_, list := h.api.sessions(code)
	s, ok := sessionByID(list, sess.SID)
	if !ok || s.State != "running" || s.PID != sess.PID {
		t.Fatalf("会话应原样保留（同一个 Shell 进程）: ok=%v %+v", ok, s)
	}
	tc2 := h.api.attach(h, code, sess.SID, 0)
	tc2.send("echo VAR:$RM_MARK:END\r")
	tc2.waitFor("VAR:"+marker+":END", 10*time.Second) // Shell 变量还在：是升级之前的那个 Shell
	tc2.send("exit\r")
}
