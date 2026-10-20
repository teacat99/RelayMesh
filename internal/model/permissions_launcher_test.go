package model

import (
	"encoding/json"
	"testing"
)

// 「运行预设」位（#179）：默认关、不含在全部权限里、只有它自己能打开 preset_run 工具。
func TestLauncherPermissionIsOptIn(t *testing.T) {
	if AllPermissions().Launcher {
		t.Fatal("AllPermissions() 不能隐式带「运行预设」：开放访问 / 环境变量令牌 / 本地 stdio 都不该获得拉起智能体的能力")
	}
	if (Permissions{}).AllowsTool("preset_run") {
		t.Fatal("没有这一位的凭据不能使用 preset_run")
	}
	if !(Permissions{Launcher: true}).AllowsTool("preset_run") {
		t.Fatal("带这一位的凭据应该能使用 preset_run")
	}
	// 其他权限位（含终端执行位）都不能顺带打开它。
	others := []Permissions{
		AllPermissions(),
		{Terminal: true}, {TerminalFiles: true}, {Terminal: true, TerminalCompat: true},
		{Feedback: true}, {Sessions: true}, {Configure: true}, {Execute: true},
	}
	for i, p := range others {
		if p.AllowsTool("preset_run") {
			t.Errorf("第 %d 组权限不该允许 preset_run：%+v", i, p)
		}
	}
	// 反过来，这一位也不会带来别的工具。
	only := Permissions{Launcher: true}
	for _, tool := range []string{"interactive_feedback", "task_board", "workflow_context", "project_docs", "terminal_exec", "terminal_files", "get_system_info"} {
		if only.AllowsTool(tool) {
			t.Errorf("只带「运行预设」位不该允许 %s", tool)
		}
	}
}

// 旧凭据的 JSON 里没有 launcher 字段：读出来必须是 false；写出去再读回来保持原值。
func TestLauncherPermissionJSON(t *testing.T) {
	var old Permissions
	if err := json.Unmarshal([]byte(`{"feedback":true,"sessions":true}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Launcher {
		t.Fatal("缺省必须是 false")
	}
	raw, err := json.Marshal(Permissions{Feedback: true, Launcher: true})
	if err != nil {
		t.Fatal(err)
	}
	var back Permissions
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Launcher || !back.Feedback {
		t.Fatalf("往返之后丢了字段：%s -> %+v", raw, back)
	}
	// 经 Value / Scan（数据库列）往返同样保持。
	v, err := Permissions{Launcher: true}.Value()
	if err != nil {
		t.Fatal(err)
	}
	var scanned Permissions
	if err := scanned.Scan(v); err != nil {
		t.Fatal(err)
	}
	if !scanned.Launcher {
		t.Fatalf("Value/Scan 往返丢了 launcher：%+v", scanned)
	}
}

// 能力 URL 凭据（令牌写在网址里，泄露即口令泄露）不能带「运行预设」：授权面必须尽量窄。
func TestCapabilityCredentialCannotCarryLauncher(t *testing.T) {
	ok := MCPCredential{Capability: true, Permissions: Permissions{Terminal: true}, TerminalHosts: StringList{"RM-AAAA"}}
	if p := ok.CapabilityProblem(); p != "" {
		t.Fatalf("普通的能力 URL 凭据应合规: %s", p)
	}
	bad := ok
	bad.Permissions.Launcher = true
	if p := bad.CapabilityProblem(); p == "" {
		t.Fatal("带「运行预设」的能力 URL 凭据必须被拒绝")
	}
	// 不是能力 URL 凭据：不受这条约束。
	plain := MCPCredential{Permissions: Permissions{Feedback: true, Launcher: true}}
	if p := plain.CapabilityProblem(); p != "" {
		t.Fatalf("普通凭据不受能力 URL 约束: %s", p)
	}
}
