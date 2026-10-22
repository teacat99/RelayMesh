//go:build !windows

package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/agent"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// web-upgrade on|off|status：主机所有者在本机决定是否允许从 Web 升级。开关只是数据目录里的一个文件。

func TestWebUpgradeOnOffRoundTrip(t *testing.T) {
	h, _ := installedHost(t, "linux", 0, common{})
	dataDir := t.TempDir()

	res, err := doWebUpgrade(h.sys, "status", dataDir, false)
	if err != nil || res.Allowed {
		t.Fatalf("默认不允许: %+v %v", res, err)
	}

	res, err = doWebUpgrade(h.sys, "on", dataDir, false)
	if err != nil || !res.Allowed || res.FlagPath != agent.WebUpgradeFlagPath(dataDir) {
		t.Fatalf("打开后应允许: %+v %v", res, err)
	}
	if !agent.WebUpgradeAllowed(dataDir) {
		t.Fatal("Agent 读到的开关应与命令一致")
	}

	res, err = doWebUpgrade(h.sys, "off", dataDir, false)
	if err != nil || res.Allowed || agent.WebUpgradeAllowed(dataDir) {
		t.Fatalf("关闭后应不允许: %+v %v", res, err)
	}
	if _, err := doWebUpgrade(h.sys, "off", dataDir, false); err != nil {
		t.Fatalf("重复关闭不应报错: %v", err)
	}
}

func TestWebUpgradeOnRefusesWhereTheAgentCouldNotUpgradeItself(t *testing.T) {
	t.Run("没有装常驻服务", func(t *testing.T) {
		h := newFakeHost(t, "linux", 0)
		dataDir := t.TempDir()
		_, err := doWebUpgrade(h.sys, "on", dataDir, false)
		if err == nil || !strings.Contains(err.Error(), "没有检测到已安装的 RelayMesh Agent 常驻服务") {
			t.Fatalf("应说明只有常驻服务才能从 Web 升级: %v", err)
		}
		if agent.WebUpgradeAllowed(dataDir) {
			t.Fatal("拒绝时不应写开关（否则会造成「以为开了」的错觉）")
		}
	})
	t.Run("普通用户面对系统级安装", func(t *testing.T) {
		h, _ := installedHost(t, "linux", 0, common{})
		h.sys.euid = 1000
		dataDir := t.TempDir()
		if _, err := doWebUpgrade(h.sys, "on", dataDir, false); !errors.Is(err, errUpgradeNeedRoot) {
			t.Fatalf("应提示需要管理员权限: %v", err)
		}
		if agent.WebUpgradeAllowed(dataDir) {
			t.Fatal("拒绝时不应写开关")
		}
	})
	for _, goos := range []string{"darwin", "freebsd"} {
		t.Run("暂不支持的系统 "+goos, func(t *testing.T) {
			h := newFakeHost(t, goos, 0)
			dataDir := t.TempDir()
			_, err := doWebUpgrade(h.sys, "on", dataDir, false)
			if err == nil || !strings.Contains(err.Error(), "暂不支持从 Web 一键升级") {
				t.Fatalf("应明确说明暂不支持，并指向手动路径: %v", err)
			}
			if agent.WebUpgradeAllowed(dataDir) {
				t.Fatal("拒绝时不应写开关")
			}
		})
	}
}

func TestWebUpgradeOffAndStatusWorkEverywhere(t *testing.T) {
	// 收回授权永远是安全的，不需要任何前提；status 也只读文件。
	h := newFakeHost(t, "freebsd", 0)
	dataDir := t.TempDir()
	if err := agent.SetWebUpgradeAllowed(dataDir, true); err != nil {
		t.Fatal(err)
	}
	if res, err := doWebUpgrade(h.sys, "status", dataDir, false); err != nil || !res.Allowed {
		t.Fatalf("status: %+v %v", res, err)
	}
	if res, err := doWebUpgrade(h.sys, "off", dataDir, false); err != nil || res.Allowed {
		t.Fatalf("off: %+v %v", res, err)
	}
	if _, err := doWebUpgrade(h.sys, "enable-everything", dataDir, false); err == nil {
		t.Fatal("不认识的动作应报错")
	}
}

func TestWebUpgradeStatusShowsTheLastUpgrade(t *testing.T) {
	h := newFakeHost(t, "linux", 0)
	dataDir := t.TempDir()
	note := termproto.UpgradeNote{Outcome: termproto.UpgradeOutcomeRolledBack, From: "1.7.0", To: "1.8.0", At: 1_800_000_000, Detail: "新版本没有连上服务端"}
	if err := agent.WriteUpgradeNote(dataDir, note); err != nil {
		t.Fatal(err)
	}
	res, err := doWebUpgrade(h.sys, "status", dataDir, false)
	if err != nil || res.Note == nil || *res.Note != note {
		t.Fatalf("status 应带上上一次升级的结果: %+v %v", res, err)
	}
	line := describeUpgradeNote(note)
	for _, want := range []string{"已回滚", "1.7.0 → 1.8.0", "新版本没有连上服务端"} {
		if !strings.Contains(line, want) {
			t.Errorf("描述里应有 %q: %s", want, line)
		}
	}
}

func TestWebUpgradeCommandParsing(t *testing.T) {
	dataDir := t.TempDir()
	run := func(args ...string) (int, string, string) {
		env := testEnv(t)
		code := cmdWebUpgrade(context.Background(), args, env)
		return code, env.Stdout.(*syncBuffer).String(), env.Stderr.(*syncBuffer).String()
	}

	// status 只读：不依赖本机装了什么，安全地用真实环境跑。动作在选项之前、之后、缺省都可以。
	for _, args := range [][]string{
		{"status", "--data-dir", dataDir},
		{"--data-dir", dataDir, "status"},
		{"--data-dir", dataDir},
	} {
		code, out, errOut := run(args...)
		if code != 0 || !strings.Contains(out, "从 Web 升级: 未允许") {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}

	// off 对一个不存在开关的数据目录也成功。
	if code, out, _ := run("off", "--data-dir", dataDir); code != 0 || !strings.Contains(out, "已关闭") {
		t.Errorf("off: code=%d out=%q", code, out)
	}

	for _, args := range [][]string{
		{"maybe", "--data-dir", dataDir},
		{"status", "extra", "--data-dir", dataDir},
		{"--no-such-flag"},
	} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%v 应以用法错误（2）退出，得到 %d", args, code)
		}
	}
	if _, err := os.Stat(agent.WebUpgradeFlagPath(dataDir)); err == nil {
		t.Fatal("这些命令都不应打开开关")
	}
}
