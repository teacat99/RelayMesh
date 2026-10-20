package cli

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// Web 一键升级的 Agent 端（Linux）：判断「我是不是装成服务运行的」，以及如何把升级进程放到自己的进程树之外启动。
// 与远程卸载共用同一套判断与同一种放置方式（见 selfuninstall_test.go）。

func TestLinuxSelfUpgraderNeedsEverySignOfBeingAService(t *testing.T) {
	cfg := resolved{dataDir: t.TempDir()}
	cases := []struct {
		name string
		mod  func(*fakeSelfOps)
		want bool
	}{
		{"systemd 管理的系统级服务", func(*fakeSelfOps) {}, true},
		{"没有 INVOCATION_ID（前台运行 / 容器里）", func(f *fakeSelfOps) { delete(f.env, "INVOCATION_ID") }, false},
		{"有 INVOCATION_ID 但服务单元文件不存在（从某个服务里打开的终端里手动运行）", func(f *fakeSelfOps) { f.units[false] = false }, false},
		{"没有 systemd-run", func(f *fakeSelfOps) { f.missing["systemd-run"] = true }, false},
		{"普通用户：看的是用户级单元，系统级单元存在不算", func(f *fakeSelfOps) { f.euid = 1000 }, false},
		{"普通用户且用户级单元存在", func(f *fakeSelfOps) { f.euid = 1000; f.units = map[bool]bool{true: true} }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSelfOps()
			tc.mod(f)
			got := linuxSelfUpgrader(f.ops(), testEnv(t), cfg) != nil
			if got != tc.want {
				t.Fatalf("能力 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

func TestLinuxSelfUpgraderHandsOffToTheInstalledBinaryOutsideTheServiceCgroup(t *testing.T) {
	f := newFakeSelfOps()
	dataDir := t.TempDir()
	su := linuxSelfUpgrader(f.ops(), testEnv(t), resolved{dataDir: dataDir})
	if su == nil {
		t.Fatal("应具备能力")
	}
	staged := agent.StagedBinaryPath(dataDir)
	if err := su.Handoff(context.Background(), staged, "1.8.0"); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 1 || f.ran[0][0] != "systemd-run" {
		t.Fatalf("应只调用一次 systemd-run: %v", f.ran)
	}
	got := strings.Join(f.ran[0][1:], " ")
	for _, want := range []string{
		"--collect",
		"--unit=relaymesh-agent-upgrade-" + strconv.FormatInt(f.startedAt.Unix(), 10),
		// 由已安装的（旧）程序执行升级：「新版本起不来就还原」不能依赖还没被证明能跑的新程序。
		"-- /usr/local/bin/relaymesh-agent upgrade --from " + staged + " --auto-rollback --remove-source --expect-version 1.8.0 --data-dir " + dataDir,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("systemd-run 参数缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--user") {
		t.Errorf("系统级服务不应带 --user: %s", got)
	}
}

func TestLinuxSelfUpgraderHandoffVariants(t *testing.T) {
	staged := "/var/lib/relaymesh-agent/upgrade/relaymesh-agent.new"

	t.Run("替换过的二进制路径带 (deleted) 后缀", func(t *testing.T) {
		f := newFakeSelfOps()
		f.exe = "/usr/local/bin/relaymesh-agent (deleted)"
		su := linuxSelfUpgrader(f.ops(), testEnv(t), resolved{dataDir: "/var/lib/relaymesh-agent"})
		if err := su.Handoff(context.Background(), staged, "1.8.0"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(f.ran[0], " "), "(deleted)") {
			t.Fatalf("不应带 (deleted): %v", f.ran[0])
		}
	})

	t.Run("用户级服务：瞬态单元与升级命令都带 --user", func(t *testing.T) {
		f := newFakeSelfOps()
		f.euid = 1000
		f.units = map[bool]bool{true: true}
		f.exe = "/home/alice/.local/bin/relaymesh-agent"
		su := linuxSelfUpgrader(f.ops(), testEnv(t), resolved{dataDir: "/home/alice/.local/state/relaymesh-agent"})
		if err := su.Handoff(context.Background(), staged, "1.8.0"); err != nil {
			t.Fatal(err)
		}
		args := f.ran[0][1:]
		if args[0] != "--user" {
			t.Fatalf("systemd-run 应带 --user: %v", args)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-- /home/alice/.local/bin/relaymesh-agent upgrade --from "+staged+" --auto-rollback --remove-source --expect-version 1.8.0 --user --data-dir /home/alice/.local/state/relaymesh-agent") {
			t.Fatalf("升级命令不对: %s", joined)
		}
	})

	t.Run("主程序子命令形态带前缀参数", func(t *testing.T) {
		f := newFakeSelfOps()
		f.exe = "/usr/local/bin/relaymesh"
		env := testEnv(t)
		env.ServiceArgs = []string{"agent"}
		su := linuxSelfUpgrader(f.ops(), env, resolved{dataDir: "/var/lib/relaymesh-agent"})
		if err := su.Handoff(context.Background(), staged, "1.8.0"); err != nil {
			t.Fatal(err)
		}
		if joined := strings.Join(f.ran[0][1:], " "); !strings.Contains(joined, "-- /usr/local/bin/relaymesh agent upgrade --from") {
			t.Fatalf("应在可执行文件之后带上前缀 agent: %s", joined)
		}
	})

	t.Run("systemd-run 失败就如实报错（Agent 据此告诉 Hub，并清理暂存文件）", func(t *testing.T) {
		f := newFakeSelfOps()
		f.runErr = errors.New("exit status 1")
		f.runOut = "Failed to start transient service unit: Access denied"
		su := linuxSelfUpgrader(f.ops(), testEnv(t), resolved{dataDir: "/var/lib/relaymesh-agent"})
		err := su.Handoff(context.Background(), staged, "1.8.0")
		if err == nil || !strings.Contains(err.Error(), "Access denied") {
			t.Fatalf("应带上 systemd-run 的输出: %v", err)
		}
	})

	t.Run("取不到可执行文件路径", func(t *testing.T) {
		ops := newFakeSelfOps().ops()
		ops.executable = func() (string, error) { return "", errors.New("no /proc") }
		su := linuxSelfUpgrader(ops, testEnv(t), resolved{dataDir: "/var/lib/relaymesh-agent"})
		if err := su.Handoff(context.Background(), staged, "1.8.0"); err == nil {
			t.Fatal("应报错")
		}
	})
}

func TestLinuxSelfUpgraderGateAndFilesLiveInTheDataDir(t *testing.T) {
	f := newFakeSelfOps()
	dataDir := t.TempDir()
	su := linuxSelfUpgrader(f.ops(), testEnv(t), resolved{dataDir: dataDir})

	if su.Allowed() {
		t.Fatal("默认必须是不允许的：开关只能由主机所有者在本机打开")
	}
	if err := agent.SetWebUpgradeAllowed(dataDir, true); err != nil {
		t.Fatal(err)
	}
	if !su.Allowed() {
		t.Fatal("所有者打开开关后应读到允许（每次现读，不缓存）")
	}
	if err := agent.SetWebUpgradeAllowed(dataDir, false); err != nil {
		t.Fatal(err)
	}
	if su.Allowed() {
		t.Fatal("所有者关闭开关后应立刻读到不允许")
	}
	if su.StageDir != agent.UpgradeDir(dataDir) {
		t.Fatalf("暂存目录应在数据目录里: %s", su.StageDir)
	}

	if su.Note() != nil {
		t.Fatal("没有升级过就没有结果")
	}
	want := termproto.UpgradeNote{Outcome: termproto.UpgradeOutcomeRolledBack, From: "1.7.0", To: "1.8.0", At: 1_800_000_000, Detail: "x"}
	if err := agent.WriteUpgradeNote(dataDir, want); err != nil {
		t.Fatal(err)
	}
	if got := su.Note(); got == nil || *got != want {
		t.Fatalf("Note 应读到升级进程留下的结果: %+v", got)
	}

	if _, ok := agent.ReadOnlineMarker(dataDir); ok {
		t.Fatal("握手之前没有上线标记")
	}
	su.Connected("1.8.0")
	m, ok := agent.ReadOnlineMarker(dataDir)
	if !ok || m.Version != "1.8.0" || m.At != f.startedAt.Unix() {
		t.Fatalf("握手成功后应留下上线标记: %+v ok=%v", m, ok)
	}
}
