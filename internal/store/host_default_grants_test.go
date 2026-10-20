package store

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

func TestHostDefaultStateAndValidation(t *testing.T) {
	for in, want := range map[string]string{"": "deny", "deny": "deny", "allow": "allow", "ALLOW": "deny", "x": "deny"} {
		if got := HostDefaultState(in); got != want {
			t.Errorf("HostDefaultState(%q) = %q，期望 %q", in, got, want)
		}
	}
	for _, ok := range []string{"", "deny", "allow"} {
		if err := ValidateHostDefaultForNewModels(ok); err != nil {
			t.Errorf("%q 应合法: %v", ok, err)
		}
	}
	for _, bad := range []string{"ALLOW", "yes", "true", " allow"} {
		if err := ValidateHostDefaultForNewModels(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
}

func saveHost(t *testing.T, st *Store, code, status, def string) {
	t.Helper()
	rec := &model.TerminalHost{MachineCode: code, PublicKey: "k-" + code, Status: status, DefaultForNewModels: def}
	if err := st.SaveTerminalHost(context.Background(), rec); err != nil {
		t.Fatalf("写入主机 %s: %v", code, err)
	}
}

func mkToken(t *testing.T, st *Store, name string, mut func(*model.MCPCredential)) *model.MCPCredential {
	t.Helper()
	c := &model.MCPCredential{Name: name, IsActive: true, Permissions: model.Permissions{Terminal: true}}
	if mut != nil {
		mut(c)
	}
	if err := st.CreateCredential(context.Background(), c); err != nil {
		t.Fatalf("创建令牌 %s: %v", name, err)
	}
	return c
}

func hostsOfToken(t *testing.T, st *Store, id uint) []string {
	t.Helper()
	c, err := st.GetCredential(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return slices.Clone([]string(c.TerminalHosts))
}

func TestDefaultAllowHostCodes(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	saveHost(t, st, "RM-B", "enabled", model.HostDefaultAllow)
	saveHost(t, st, "RM-A", "enabled", model.HostDefaultAllow)
	saveHost(t, st, "RM-DENY", "enabled", model.HostDefaultDeny)
	saveHost(t, st, "RM-EMPTY", "enabled", "")
	saveHost(t, st, "RM-OFF", "disabled", model.HostDefaultAllow)
	saveHost(t, st, "RM-BLOCK", "blocked", model.HostDefaultAllow)

	got, err := st.DefaultAllowHostCodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"RM-A", "RM-B"}) {
		t.Fatalf("只应返回已启用且默认授予的主机，且按机器码排序: %v", got)
	}
}

func TestSetTerminalHostModelDefaultForNewModels(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	saveHost(t, st, "RM-X", "enabled", "")
	allow, deny, bad := "allow", "deny", "maybe"

	if _, _, err := st.SetTerminalHostModel(ctx, "RM-X", HostModelUpdate{DefaultForNewModels: &bad}); err == nil {
		t.Fatal("非法取值必须被拒绝")
	}
	// 空串与 deny 是同一状态：改成 deny 不算变化。
	if _, changed, err := st.SetTerminalHostModel(ctx, "RM-X", HostModelUpdate{DefaultForNewModels: &deny}); err != nil || changed {
		t.Fatalf("deny 与空串等价，不应视为变化: changed=%v err=%v", changed, err)
	}
	host, changed, err := st.SetTerminalHostModel(ctx, "RM-X", HostModelUpdate{DefaultForNewModels: &allow})
	if err != nil || !changed || host.DefaultForNewModels != "allow" {
		t.Fatalf("改成 allow: changed=%v host=%+v err=%v", changed, host, err)
	}
	if _, changed, _ := st.SetTerminalHostModel(ctx, "RM-X", HostModelUpdate{DefaultForNewModels: &allow}); changed {
		t.Fatal("重复设置不应视为变化")
	}
	rec, _ := st.GetTerminalHost(ctx, "RM-X")
	if rec == nil || rec.DefaultForNewModels != "allow" {
		t.Fatalf("应落库为 allow: %+v", rec)
	}
	// 与其他字段一起改时互不干扰。
	on := true
	host, _, err = st.SetTerminalHostModel(ctx, "RM-X", HostModelUpdate{AllowModel: &on, DefaultForNewModels: &deny})
	if err != nil || !host.AllowModel || host.DefaultForNewModels != "deny" {
		t.Fatalf("同时修改多个字段: %+v err=%v", host, err)
	}
}

func TestGrantHostToTerminalCredentials(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	plain := mkToken(t, st, "普通", nil)
	other := mkToken(t, st, "已有其他", func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"RM-OTHER"} })
	has := mkToken(t, st, "已含目标", func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"RM-NEW"} })
	star := mkToken(t, st, "通配", func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"*"} })
	capTok := mkToken(t, st, "能力", func(c *model.MCPCredential) {
		c.Capability = true
		c.TerminalHosts = model.StringList{"RM-CAP"}
	})
	noTerm := mkToken(t, st, "无终端", func(c *model.MCPCredential) { c.Permissions = model.Permissions{Feedback: true} })
	inactive := mkToken(t, st, "已停用", func(c *model.MCPCredential) { c.IsActive = false })

	// 预览不写入。
	prev, err := st.PreviewHostGrant(ctx, "RM-NEW")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prev.Granted, []string{"已停用", "已有其他", "普通"}) || prev.Full != 0 {
		t.Fatalf("预览结果不符: %+v", prev)
	}
	if got := hostsOfToken(t, st, plain.ID); len(got) != 0 {
		t.Fatalf("预览不应写入: %v", got)
	}

	res, err := st.GrantHostToTerminalCredentials(ctx, "RM-NEW")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Granted, prev.Granted) {
		t.Fatalf("执行结果应与预览一致: %+v vs %+v", res, prev)
	}
	for name, tc := range map[string]struct {
		id   uint
		want []string
	}{
		"普通":   {plain.ID, []string{"RM-NEW"}},
		"已有其他": {other.ID, []string{"RM-OTHER", "RM-NEW"}},
		"已含目标": {has.ID, []string{"RM-NEW"}},
		"通配":   {star.ID, []string{"*"}},
		"能力":   {capTok.ID, []string{"RM-CAP"}},
		"无终端":  {noTerm.ID, nil},
		"已停用":  {inactive.ID, []string{"RM-NEW"}},
	} {
		if got := hostsOfToken(t, st, tc.id); !slices.Equal(got, tc.want) {
			t.Errorf("%s 的名单 = %v，期望 %v", name, got, tc.want)
		}
	}

	// 幂等。
	again, err := st.GrantHostToTerminalCredentials(ctx, "RM-NEW")
	if err != nil || len(again.Granted) != 0 {
		t.Fatalf("再次授予不应有新增: %+v err=%v", again, err)
	}
}

func TestGrantHostSkipsFullLists(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	full := make(model.StringList, 0, CredentialMaxTerminalHosts)
	for i := 0; i < CredentialMaxTerminalHosts; i++ {
		full = append(full, fmt.Sprintf("RM-H%03d", i))
	}
	fullTok := mkToken(t, st, "名单已满", func(c *model.MCPCredential) { c.TerminalHosts = full })
	roomy := mkToken(t, st, "还有空位", nil)

	res, err := st.GrantHostToTerminalCredentials(ctx, "RM-NEW")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Granted, []string{"还有空位"}) || res.Full != 1 {
		t.Fatalf("名单已满的令牌应被跳过并计数: %+v", res)
	}
	if got := hostsOfToken(t, st, fullTok.ID); len(got) != CredentialMaxTerminalHosts {
		t.Fatalf("已满名单不应被改动: %d", len(got))
	}
	if got := hostsOfToken(t, st, roomy.ID); !slices.Equal(got, []string{"RM-NEW"}) {
		t.Fatalf("有空位的令牌应被授予: %v", got)
	}
}

func TestGrantHostRejectsMalformedCode(t *testing.T) {
	st := newMemStore(t)
	mkToken(t, st, "普通", nil)
	for _, bad := range []string{"", "*", "a b", "bad;code"} {
		if _, err := st.GrantHostToTerminalCredentials(context.Background(), bad); err == nil {
			t.Errorf("%q 不是合法的机器码，必须被拒绝（尤其不能把 * 写进名单）", bad)
		}
	}
}
