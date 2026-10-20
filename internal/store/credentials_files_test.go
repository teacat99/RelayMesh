package store

import (
	"context"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 文件权限没有「默认全部可访问」的含义：打开权限位就必须同时给出允许目录。

func TestAllowedPathsNormalization(t *testing.T) {
	got, err := NormalizeAllowedPaths([]string{" /srv/data ", "", "/srv/data", "~/work", `C:\Users\dev\proj`, "D:/tmp/x", "~"})
	if err != nil {
		t.Fatalf("合法写法应通过: %v", err)
	}
	want := []string{"/srv/data", "~/work", `C:\Users\dev\proj`, "D:/tmp/x", "~"} // 单独的 ~ 是整个主目录：是管理员明确的选择，不同于文件系统根
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("应去空白、去空项、去重并保持顺序: got %q want %q", got, want)
	}
	if got, err := NormalizeAllowedPaths(nil); err != nil || got != nil {
		t.Fatalf("空列表应归一化为 nil: %#v %v", got, err)
	}

	bad := []string{
		"/", `C:\`, "C:", "relative/x", "./x", "../x", "/a/../b", "~user/x", `\\srv\share\x`, "//srv/x",
		"/srv/\x00x", "/srv/line\nbreak", "/" + strings.Repeat("a", 600),
	}
	for _, p := range bad {
		if _, err := NormalizeAllowedPaths([]string{p}); err == nil {
			t.Errorf("%q 必须被拒绝", p)
		}
	}

	many := make([]string, 0, CredentialMaxAllowedPaths+1)
	for i := 0; i <= CredentialMaxAllowedPaths; i++ {
		many = append(many, "/srv/d"+strings.Repeat("x", i))
	}
	if _, err := NormalizeAllowedPaths(many); err == nil {
		t.Fatalf("超过 %d 条必须被拒绝", CredentialMaxAllowedPaths)
	}
}

func TestSanitizeCredentialUpdatesAllowedPaths(t *testing.T) {
	out, err := SanitizeCredentialUpdates(map[string]any{"terminal_limits": map[string]any{"allowed_paths": []any{" /srv/a ", "/srv/a", "/srv/b"}}})
	if err != nil {
		t.Fatal(err)
	}
	lim := out["terminal_limits"].(model.TerminalLimits)
	if len(lim.AllowedPaths) != 2 || lim.AllowedPaths[0] != "/srv/a" || lim.AllowedPaths[1] != "/srv/b" {
		t.Fatalf("允许目录应归一化: %#v", lim.AllowedPaths)
	}
	if _, err := SanitizeCredentialUpdates(map[string]any{"terminal_limits": map[string]any{"allowed_paths": []any{"/"}}}); err == nil {
		t.Fatal("文件系统根目录必须被拒绝")
	}
}

func TestFilePermissionRequiresAllowedPaths(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	// 创建：文件权限没有允许目录 -> 拒绝；两种形式（没有 terminal_limits / 有但没有 allowed_paths）
	noLimits := &model.MCPCredential{Name: "files-nolimits", Permissions: model.Permissions{Terminal: true, TerminalFiles: true}}
	if err := st.CreateCredential(ctx, noLimits); err == nil {
		t.Fatal("文件权限没有允许目录不得创建")
	}
	otherLimits := &model.MCPCredential{
		Name: "files-otherlimits", Permissions: model.Permissions{Terminal: true, TerminalFiles: true},
		TerminalLimits: model.TerminalLimits{MaxConcurrent: 2, AllowedDirs: []string{"/srv"}},
	}
	if err := st.CreateCredential(ctx, otherLimits); err == nil {
		t.Fatal("allowed_dirs 不能代替 allowed_paths：命令执行的目录限制与文件的访问范围是两回事")
	}

	// 创建：给了允许目录 -> 通过，并可原样读回
	ok := &model.MCPCredential{
		Name: "files-ok", Permissions: model.Permissions{Terminal: true, TerminalFiles: true},
		TerminalLimits: model.TerminalLimits{AllowedPaths: []string{" /srv/data ", "/srv/data", "~/work"}},
	}
	if err := st.CreateCredential(ctx, ok); err != nil {
		t.Fatalf("带允许目录的文件权限应通过: %v", err)
	}
	got, err := st.GetCredential(ctx, ok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Permissions.TerminalFiles || len(got.TerminalLimits.AllowedPaths) != 2 ||
		got.TerminalLimits.AllowedPaths[0] != "/srv/data" || got.TerminalLimits.AllowedPaths[1] != "~/work" {
		t.Fatalf("允许目录未按归一化后的值往返: %#v", got.TerminalLimits)
	}

	// 更新：清空限额但权限仍开着 -> 拒绝（不能通过「改限额」悄悄把范围变成无限）
	if _, err := st.UpdateCredential(ctx, ok.ID, map[string]any{"terminal_limits": map[string]any{}}); err == nil {
		t.Fatal("文件权限仍开着时不得清空允许目录")
	}
	if _, err := st.UpdateCredential(ctx, ok.ID, map[string]any{"terminal_limits": map[string]any{"max_concurrent": float64(2)}}); err == nil {
		t.Fatal("换一份没有 allowed_paths 的限额同样应被拒绝")
	}
	// 更新失败不应留下半改的状态
	still, _ := st.GetCredential(ctx, ok.ID)
	if len(still.TerminalLimits.AllowedPaths) != 2 {
		t.Fatalf("被拒绝的更新不得改动已有数据: %#v", still.TerminalLimits)
	}

	// 更新：同一次请求里关闭权限并清空限额 -> 允许
	off, err := st.UpdateCredential(ctx, ok.ID, map[string]any{
		"permissions":     model.Permissions{Terminal: true},
		"terminal_limits": map[string]any{},
	})
	if err != nil {
		t.Fatalf("关闭文件权限后可以清空允许目录: %v", err)
	}
	if off.Permissions.TerminalFiles || !off.TerminalLimits.IsZero() {
		t.Fatalf("更新结果不符: %#v", off)
	}

	// 更新：已经没有允许目录时再打开权限位 -> 拒绝；同时给出目录 -> 允许
	if _, err := st.UpdateCredential(ctx, ok.ID, map[string]any{"permissions": model.Permissions{Terminal: true, TerminalFiles: true}}); err == nil {
		t.Fatal("没有允许目录时不得打开文件权限")
	}
	on, err := st.UpdateCredential(ctx, ok.ID, map[string]any{
		"permissions":     model.Permissions{Terminal: true, TerminalFiles: true},
		"terminal_limits": map[string]any{"allowed_paths": []any{"/srv/data"}},
	})
	if err != nil {
		t.Fatalf("同时给出允许目录应通过: %v", err)
	}
	if !on.Permissions.TerminalFiles || len(on.TerminalLimits.AllowedPaths) != 1 {
		t.Fatalf("更新结果不符: %#v", on)
	}

	// 只改备注等不相关字段，不受这条规则影响
	if _, err := st.UpdateCredential(ctx, on.ID, map[string]any{"note": "unrelated"}); err != nil {
		t.Fatalf("无关更新不应被拦截: %v", err)
	}
}

func TestLimitsIsZeroCountsAllowedPaths(t *testing.T) {
	if (model.TerminalLimits{AllowedPaths: []string{"/srv"}}).IsZero() {
		t.Fatal("只有 allowed_paths 的限额不是零值（否则会在序列化时被丢掉）")
	}
	if !(model.TerminalLimits{}).IsZero() {
		t.Fatal("空限额应是零值")
	}
	if (model.TerminalLimits{AllowWholeFS: true}).IsZero() {
		t.Fatal("只有 allow_whole_fs 的限额不是零值（否则开关会在序列化时被丢掉）")
	}
}

// 「允许整个文件系统」是显式的高风险开关：只有 allow_whole_fs 能放开，在允许目录里写 `/` 仍然被拒绝（防手误）。
func TestWholeFSSwitchIsTheOnlyWayToTheFileSystemRoot(t *testing.T) {
	// 目录里写根仍被拒绝，并指向这个开关
	for _, p := range []string{"/", `C:\`, "*", "*/"} {
		_, err := NormalizeAllowedPaths([]string{p})
		if err == nil {
			t.Fatalf("%q 不能作为允许目录", p)
		}
	}
	_, err := NormalizeAllowedPaths([]string{"/"})
	if err == nil || !strings.Contains(err.Error(), "允许整个文件系统") {
		t.Fatalf("拒绝根目录时应指向「允许整个文件系统」开关: %v", err)
	}
	// 即使同时打开了开关，目录列表也照常校验（开关不是绕过校验的后门）
	if _, err := ValidateTerminalLimits(model.TerminalLimits{AllowWholeFS: true, AllowedPaths: []string{"/"}}); err == nil {
		t.Fatal("开关打开时 allowed_paths 里的非法写法仍应被拒绝")
	}

	for name, c := range map[string]*model.MCPCredential{
		"只有开关（没有目录）": {Permissions: model.Permissions{TerminalFiles: true}, TerminalLimits: model.TerminalLimits{AllowWholeFS: true}},
		"开关 + 目录":    {Permissions: model.Permissions{TerminalFiles: true}, TerminalLimits: model.TerminalLimits{AllowWholeFS: true, AllowedPaths: []string{"/srv"}}},
		"只有目录":       {Permissions: model.Permissions{TerminalFiles: true}, TerminalLimits: model.TerminalLimits{AllowedPaths: []string{"/srv"}}},
		"没有文件权限位":    {Permissions: model.Permissions{Terminal: true}},
	} {
		if err := CredentialFilesError(c); err != nil {
			t.Errorf("%s 应合法: %v", name, err)
		}
	}
	none := &model.MCPCredential{Permissions: model.Permissions{TerminalFiles: true}, TerminalLimits: model.TerminalLimits{MaxConcurrent: 2}}
	err = CredentialFilesError(none)
	if err == nil || !strings.Contains(err.Error(), "allow_whole_fs") {
		t.Fatalf("没有任何范围应被拒绝，并说明可以明确开启整个文件系统: %v", err)
	}
}

func TestWholeFSSwitchRoundTripsAndKeepsTheDirectories(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	// 创建：只有开关，没有目录
	c := &model.MCPCredential{
		Name: "whole-create", Permissions: model.Permissions{Terminal: true, TerminalFiles: true},
		TerminalLimits: model.TerminalLimits{AllowWholeFS: true},
	}
	if err := st.CreateCredential(ctx, c); err != nil {
		t.Fatalf("只有开关的文件权限应通过: %v", err)
	}
	got, err := st.GetCredential(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TerminalLimits.AllowWholeFS || len(got.TerminalLimits.AllowedPaths) != 0 || !got.TerminalLimits.HasFileScope() {
		t.Fatalf("开关未往返: %#v", got.TerminalLimits)
	}

	// 更新：开关打开时保留（并校验）目录设置
	on, err := st.UpdateCredential(ctx, c.ID, map[string]any{
		"terminal_limits": map[string]any{"allow_whole_fs": true, "allowed_paths": []any{" /srv/data ", "/srv/data"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !on.TerminalLimits.AllowWholeFS || len(on.TerminalLimits.AllowedPaths) != 1 || on.TerminalLimits.AllowedPaths[0] != "/srv/data" {
		t.Fatalf("开关打开时目录应被保留并归一化: %#v", on.TerminalLimits)
	}

	// 关闭开关：回到目录限制（永远不会比管理员显式配置的更宽）
	off, err := st.UpdateCredential(ctx, c.ID, map[string]any{
		"terminal_limits": map[string]any{"allowed_paths": []any{"/srv/data"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if off.TerminalLimits.AllowWholeFS || len(off.TerminalLimits.AllowedPaths) != 1 {
		t.Fatalf("关闭开关后应只剩目录限制: %#v", off.TerminalLimits)
	}

	// 关闭开关却没有任何目录、文件权限仍开着 -> 拒绝（不能悄悄变成无范围）
	if _, err := st.UpdateCredential(ctx, c.ID, map[string]any{"terminal_limits": map[string]any{"allow_whole_fs": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateCredential(ctx, c.ID, map[string]any{"terminal_limits": map[string]any{}}); err == nil {
		t.Fatal("关闭开关又不给目录，文件权限仍开着时必须被拒绝")
	}
	still, _ := st.GetCredential(ctx, c.ID)
	if !still.TerminalLimits.AllowWholeFS {
		t.Fatalf("被拒绝的更新不得改动已有数据: %#v", still.TerminalLimits)
	}

	// 关闭文件权限位：开关作为设置被保留（但不产生任何访问，见 model.EffectiveWholeFS）
	noFiles, err := st.UpdateCredential(ctx, c.ID, map[string]any{"permissions": model.Permissions{Terminal: true}})
	if err != nil {
		t.Fatalf("关闭文件权限位应允许: %v", err)
	}
	if !noFiles.TerminalLimits.AllowWholeFS || model.EffectiveWholeFS(noFiles.Permissions, noFiles.TerminalLimits) {
		t.Fatalf("关闭权限位后开关只是被保留的设置，不应生效: %#v %#v", noFiles.Permissions, noFiles.TerminalLimits)
	}
	// 再次打开权限位：由于开关还在，不需要重新给目录（这一步的授权转变由 API 层要求重新验证密码，见 internal/api）
	again, err := st.UpdateCredential(ctx, c.ID, map[string]any{"permissions": model.Permissions{Terminal: true, TerminalFiles: true}})
	if err != nil {
		t.Fatalf("开关仍在时再次打开权限位应通过: %v", err)
	}
	if !model.EffectiveWholeFS(again.Permissions, again.TerminalLimits) {
		t.Fatalf("应重新生效: %#v %#v", again.Permissions, again.TerminalLimits)
	}
}

func TestEffectiveWholeFS(t *testing.T) {
	on := model.TerminalLimits{AllowWholeFS: true}
	for _, tc := range []struct {
		name  string
		perms model.Permissions
		lim   model.TerminalLimits
		want  bool
	}{
		{"权限位 + 开关", model.Permissions{TerminalFiles: true}, on, true},
		{"只有开关", model.Permissions{Terminal: true}, on, false},
		{"只有权限位", model.Permissions{TerminalFiles: true}, model.TerminalLimits{AllowedPaths: []string{"/srv"}}, false},
		{"都没有", model.Permissions{}, model.TerminalLimits{}, false},
	} {
		if got := model.EffectiveWholeFS(tc.perms, tc.lim); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
