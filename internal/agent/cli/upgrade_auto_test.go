//go:build !windows

// 自动升级（Web 一键升级的升级进程）的测试：沿用升级测试的假主机，再加上「数据目录」里的上线标记与结果文件。
// 要钉住的是回滚这条安全网：新版本没连上服务端，就必须把旧程序放回去，而不是让一台远程主机永远失联。

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

type autoUpgrade struct {
	h       *fakeHost
	env     Env
	dataDir string
	staged  string
	opts    upgradeOptions
}

func newAutoUpgrade(t *testing.T) *autoUpgrade {
	t.Helper()
	h, env := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	staged := agent.StagedBinaryPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte(newAgentBody), 0o700); err != nil {
		t.Fatal(err)
	}
	return &autoUpgrade{h: h, env: env, dataDir: dataDir, staged: staged, opts: upgradeOptions{
		from: staged, autoRollback: true, expect: "1.8.0", dataDir: dataDir, timeout: 5 * time.Second, removeSource: true,
	}}
}

// onRestart 登记「每重启一次服务（第 n 次）要发生什么」，用来模拟新进程起来之后是否连上了服务端。
func (a *autoUpgrade) onRestart(f func(n int)) {
	n := 0
	a.h.hook = func(line string) {
		if strings.HasPrefix(line, "systemctl restart") || strings.HasPrefix(line, "systemctl --user restart") {
			n++
			f(n)
		}
	}
}

func (a *autoUpgrade) markOnline(t *testing.T, version string) {
	t.Helper()
	if err := agent.WriteOnlineMarker(a.dataDir, version, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func (a *autoUpgrade) run() (upgradeResult, error) {
	return doUpgradeWith(a.env, a.h.sys, a.opts)
}

func (a *autoUpgrade) note(t *testing.T) termproto.UpgradeNote {
	t.Helper()
	n := agent.ReadUpgradeNote(a.dataDir)
	if n == nil {
		t.Fatal("升级进程应留下结果文件")
	}
	return *n
}

func (a *autoUpgrade) stagedGone() bool {
	_, err := os.Stat(a.staged)
	return errors.Is(err, os.ErrNotExist)
}

func TestAutoUpgradeSuccessWaitsForTheNewVersionAndKeepsThePreviousBinary(t *testing.T) {
	a := newAutoUpgrade(t)
	agentUnit, holderUnit := a.h.read(t, sysAgentUnit), a.h.read(t, sysHolderUnit)
	// 一份来自上一次升级的旧标记不能被当成这次的：升级进程在替换之前先把它清掉。
	a.markOnline(t, "1.8.0")
	a.onRestart(func(int) { a.markOnline(t, "1.8.0") })

	res, err := a.run()
	if err != nil {
		t.Fatal(err)
	}
	if got := a.h.read(t, sysAgentBin); got != newAgentBody {
		t.Fatalf("可执行文件应换成新版本: %q", got)
	}
	if got := a.h.read(t, sysAgentBin+".prev"); got != oldAgentBody {
		t.Fatalf("旧版本应保留为 .prev，需要时可手动换回: %q", got)
	}
	if !res.Confirmed || res.PrevPath != sysAgentBin+".prev" {
		t.Fatalf("结果不对: %+v", res)
	}
	if !a.stagedGone() {
		t.Fatal("暂存的新程序用完应删除")
	}
	if a.h.read(t, sysAgentUnit) != agentUnit || a.h.read(t, sysHolderUnit) != holderUnit {
		t.Fatal("升级不应改动任何服务配置")
	}
	// 只重启 Agent，一次；等上线靠标记而不是反复 is-active。
	if got := strings.Join(a.h.cmds, "\n"); got != "systemctl restart relaymesh-agent.service" {
		t.Fatalf("命令序列不对:\n%s", got)
	}
	n := a.note(t)
	if n.Outcome != termproto.UpgradeOutcomeOK || n.From != "1.7.0-test" || n.To != "1.8.0" || n.At == 0 {
		t.Fatalf("结果文件不对: %+v", n)
	}
}

func TestAutoUpgradeRollsBackWhenTheNewVersionNeverComesOnline(t *testing.T) {
	a := newAutoUpgrade(t)
	a.opts.timeout = 3 * time.Second
	a.onRestart(func(n int) {
		if n == 2 { // 第二次重启发生在还原之后：起来的是旧版本，它会连上
			a.markOnline(t, a.env.Version)
		}
	})

	res, err := a.run()
	if err == nil {
		t.Fatal("回滚不是成功，应返回错误让进程以非零退出")
	}
	for _, want := range []string{"新版本 1.8.0 没有在 3 秒内连上服务端", "已还原为旧版本并确认它重新连上了服务端"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误里应有 %q: %v", want, err)
		}
	}
	if res.Confirmed {
		t.Fatal("回滚不能标记为已确认")
	}
	if got := a.h.read(t, sysAgentBin); got != oldAgentBody {
		t.Fatalf("应还原为旧程序: %q", got)
	}
	want := strings.Join([]string{
		"systemctl restart relaymesh-agent.service",
		"systemctl reset-failed relaymesh-agent.service", // 新版本反复崩溃可能触发启动频率限制，先清掉
		"systemctl restart relaymesh-agent.service",
	}, "\n")
	if got := strings.Join(a.h.cmds, "\n"); got != want {
		t.Fatalf("命令序列不对:\n%s", got)
	}
	n := a.note(t)
	if n.Outcome != termproto.UpgradeOutcomeRolledBack || n.From != "1.7.0-test" || n.To != "1.8.0" ||
		!strings.Contains(n.Detail, "没有在 3 秒内连上服务端") || !strings.Contains(n.Detail, "已还原为旧版本 1.7.0-test") {
		t.Fatalf("结果应说明已回滚及原因（旧版本上线后的第一次握手会把它带给 Hub）: %+v", n)
	}
	if !a.stagedGone() {
		t.Fatal("失败后暂存的新程序也应删除")
	}
}

func TestAutoUpgradeDoesNotMistakeAStaleMarkerForTheNewVersion(t *testing.T) {
	a := newAutoUpgrade(t)
	a.opts.timeout = 2 * time.Second
	a.markOnline(t, "1.8.0") // 上一次（失败后又重试的）升级留下的
	a.onRestart(func(n int) {
		if n == 2 {
			a.markOnline(t, a.env.Version)
		}
	})
	_, err := a.run()
	if err == nil || !strings.Contains(err.Error(), "已还原为旧版本") {
		t.Fatalf("新进程没有写新标记，必须回滚，而不是被旧标记蒙混过关: %v", err)
	}
	if got := a.h.read(t, sysAgentBin); got != oldAgentBody {
		t.Fatalf("应还原为旧程序: %q", got)
	}
}

func TestAutoUpgradeReportsWhenEvenTheRolledBackVersionStaysOffline(t *testing.T) {
	a := newAutoUpgrade(t)
	a.opts.timeout = 2 * time.Second
	// 旧版本也一直没有上线：程序已经还原，但如实告诉管理员「它也没连上」，别装作回滚成功。
	_, err := a.run()
	if err == nil || !strings.Contains(err.Error(), "它也没有在 60 秒内连上服务端") {
		t.Fatalf("应如实说明还原后仍没有上线: %v", err)
	}
	if got := a.h.read(t, sysAgentBin); got != oldAgentBody {
		t.Fatalf("程序仍应已还原: %q", got)
	}
	n := a.note(t)
	if n.Outcome != termproto.UpgradeOutcomeFailed || !strings.Contains(n.Detail, "它也没有连上服务端") {
		t.Fatalf("结果应是失败并说明原因: %+v", n)
	}
}

func TestAutoUpgradeRestartFailureRollsBackToo(t *testing.T) {
	a := newAutoUpgrade(t)
	a.h.runErr["systemctl restart"] = errors.New("exit status 1")
	a.h.runOut["systemctl restart"] = "Failed to restart relaymesh-agent.service: Unit is masked."

	_, err := a.run()
	if err == nil || !strings.Contains(err.Error(), "旧版本已还原，但重启服务失败") || !strings.Contains(err.Error(), "Unit is masked") {
		t.Fatalf("重启失败时应还原旧程序，并带上原始输出: %v", err)
	}
	if got := a.h.read(t, sysAgentBin); got != oldAgentBody {
		t.Fatalf("应还原为旧程序: %q", got)
	}
	n := a.note(t)
	if n.Outcome != termproto.UpgradeOutcomeFailed || !strings.Contains(n.Detail, "重启服务失败") {
		t.Fatalf("结果应是失败并说明原因: %+v", n)
	}
}

func TestAutoUpgradeAbortsBeforeChangingAnythingWhenThePreviousBinaryCannotBeSaved(t *testing.T) {
	a := newAutoUpgrade(t)
	// 让 .prev 的位置被一个非空目录占着，保存旧版本必然失败。
	if err := os.MkdirAll(filepath.Join(a.h.sys.p(sysAgentBin+".prev"), "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := a.run()
	if err == nil || !strings.Contains(err.Error(), "无法把旧程序保存为") {
		t.Fatalf("应在动手之前就放弃: %v", err)
	}
	if got := a.h.read(t, sysAgentBin); got != oldAgentBody {
		t.Fatalf("旧程序不应被改动: %q", got)
	}
	if len(a.h.cmds) != 0 {
		t.Fatalf("不应重启服务: %v", a.h.cmds)
	}
	if n := a.note(t); n.Outcome != termproto.UpgradeOutcomeFailed || !strings.Contains(n.Detail, "无法保存旧版本") {
		t.Fatalf("结果应是失败并说明原因: %+v", n)
	}
}

func TestAutoUpgradeRefusesWhenNothingIsInstalled(t *testing.T) {
	a := newAutoUpgrade(t)
	empty := newFakeHost(t, "linux", 0)
	_, err := doUpgradeWith(a.env, empty.sys, a.opts)
	if !errors.Is(err, errNothingToUpgrade) {
		t.Fatalf("没装服务应拒绝: %v", err)
	}
	if len(empty.cmds) != 0 {
		t.Fatalf("不应执行任何命令: %v", empty.cmds)
	}
	if !a.stagedGone() {
		t.Fatal("暂存的新程序用完即删，即使升级根本没有开始（下次 Agent 会重新下载）")
	}
}

func TestRemoveSourceNeverDeletesTheInstalledBinary(t *testing.T) {
	// 即使数据目录的暂存目录恰好就是程序所在的目录（配置错误），也绝不能把已安装的程序删掉。
	h, env := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()
	stage := agent.UpgradeDir(dataDir)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	// 在暂存目录里放一个指向已安装程序的符号链接，冒充「下载的新程序」。
	link := filepath.Join(stage, "relaymesh-agent.new")
	if err := os.Symlink(h.sys.p(sysAgentBin), link); err != nil {
		t.Fatal(err)
	}
	_, err := doUpgradeWith(env, h.sys, upgradeOptions{from: link, removeSource: true, dataDir: dataDir})
	if err == nil || !strings.Contains(err.Error(), "已安装的程序本身") {
		t.Fatalf("应拒绝: %v", err)
	}
	if got := h.read(t, sysAgentBin); got != oldAgentBody {
		t.Fatalf("已安装的程序必须还在: %q", got)
	}
}

func TestUpgradeFromAFileReplacesWithoutNeedingTheRunningBinaryToBeNew(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	staged := filepath.Join(t.TempDir(), "downloaded")
	if err := os.WriteFile(staged, []byte(newAgentBody), 0o700); err != nil {
		t.Fatal(err)
	}
	h.runOut["systemctl is-active"] = "active"

	res, err := doUpgradeWith(env, h.sys, upgradeOptions{from: staged})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.read(t, sysAgentBin); got != newAgentBody {
		t.Fatalf("应换成 --from 指定的文件: %q", got)
	}
	if res.PrevPath != "" || res.Confirmed {
		t.Fatalf("没有 --auto-rollback 就不留旧程序、也不等上线: %+v", res)
	}
	if h.exists(sysAgentBin + ".prev") {
		t.Fatal("没有 --auto-rollback 不应留 .prev")
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatal("没有 --remove-source 就不应删除 --from 的文件")
	}
	if got := strings.Join(h.cmds, "\n"); got != "systemctl restart relaymesh-agent.service\nsystemctl is-active relaymesh-agent.service" {
		t.Fatalf("手动升级的命令序列不应变:\n%s", got)
	}
}

func TestUpgradeFromAMissingFileOrTheInstalledBinaryItselfIsRefused(t *testing.T) {
	h, env := installedHost(t, "linux", 0, common{})
	if _, err := doUpgradeWith(env, h.sys, upgradeOptions{from: filepath.Join(t.TempDir(), "nope")}); err == nil || !strings.Contains(err.Error(), "找不到要换上的新程序") {
		t.Fatalf("文件不存在应明确报错: %v", err)
	}
	if _, err := doUpgradeWith(env, h.sys, upgradeOptions{from: h.sys.p(sysAgentBin)}); err == nil || !strings.Contains(err.Error(), "已安装的程序本身") {
		t.Fatalf("--from 指向已安装的程序本身应拒绝: %v", err)
	}
	if got := h.read(t, sysAgentBin); got != oldAgentBody || len(h.cmds) != 0 {
		t.Fatal("拒绝时不应有任何改动")
	}
}

func TestUpgradeOptionsValidation(t *testing.T) {
	dataDir := t.TempDir()
	staged := agent.StagedBinaryPath(dataDir)
	cases := []struct {
		name string
		goos string
		o    upgradeOptions
		want string // 空表示应通过
	}{
		{"手动升级的零值", "linux", upgradeOptions{}, ""},
		{"完整的自动升级", "linux", upgradeOptions{from: staged, autoRollback: true, expect: "1.8.0", dataDir: dataDir, removeSource: true}, ""},
		{"expect-version 不与 auto-rollback 合用", "linux", upgradeOptions{expect: "1.8.0"}, "--expect-version 只与 --auto-rollback 合用"},
		{"auto-rollback 缺少版本号", "linux", upgradeOptions{autoRollback: true, dataDir: dataDir}, "--expect-version"},
		{"auto-rollback 的版本号夹带路径", "linux", upgradeOptions{autoRollback: true, expect: "../x", dataDir: dataDir}, "--expect-version"},
		{"auto-rollback 只支持 Linux", "darwin", upgradeOptions{autoRollback: true, expect: "1.8.0", dataDir: dataDir}, "只支持 Linux"},
		{"remove-source 需要 from", "linux", upgradeOptions{removeSource: true, dataDir: dataDir}, "--remove-source 需要同时给出 --from"},
		{"remove-source 只能删暂存目录里的文件", "linux", upgradeOptions{from: filepath.Join(t.TempDir(), "agent"), removeSource: true, dataDir: dataDir}, "只能删除数据目录的暂存目录"},
		{"remove-source 不能指向暂存目录的上一级", "linux", upgradeOptions{from: filepath.Join(dataDir, "agent"), removeSource: true, dataDir: dataDir}, "只能删除数据目录的暂存目录"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			err := o.validate(tc.goos)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("应通过: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("应报错 %q: %v", tc.want, err)
			}
		})
	}

	// 通过校验时补全默认值：超时有默认，--from 变成绝对路径。
	o := upgradeOptions{from: staged, autoRollback: true, expect: "1.8.0", dataDir: dataDir}
	if err := o.validate("linux"); err != nil {
		t.Fatal(err)
	}
	if o.timeout != defaultOnlineTimeout || !filepath.IsAbs(o.from) {
		t.Fatalf("默认值没补全: %+v", o)
	}
}

func TestUpgradeCommandRejectsBadFlagCombinationsBeforeTouchingAnything(t *testing.T) {
	for _, args := range [][]string{
		{"--expect-version", "1.8.0"},
		{"--auto-rollback"},
		{"--auto-rollback", "--expect-version", "bad version"},
		{"--remove-source"},
		{"--bogus"},
	} {
		env := testEnv(t)
		if code := cmdUpgrade(context.Background(), args, env); code != 2 {
			t.Errorf("upgrade %v 应以用法错误（2）退出，得到 %d", args, code)
		}
	}
}
