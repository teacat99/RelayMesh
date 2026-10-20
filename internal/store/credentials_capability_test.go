package store

import (
	"context"
	"strings"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

const capHost = "RM-AAAA-BBBB-CCCC-DDDD"

func capCred(name string) *model.MCPCredential {
	return &model.MCPCredential{
		Name:          name,
		Capability:    true,
		Permissions:   model.Permissions{Terminal: true},
		TerminalHosts: model.StringList{capHost},
	}
}

func TestCreateCapabilityCredential(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	c := capCred("chatgpt")
	if err := st.CreateCredential(ctx, c); err != nil {
		t.Fatalf("创建能力凭据: %v", err)
	}
	if !c.Capability || !IsCapabilityTokenShape(c.Token) {
		t.Fatalf("能力凭据应带能力形状的令牌: %#v", c)
	}
	got, err := st.GetCredential(ctx, c.ID)
	if err != nil || !got.Capability {
		t.Fatalf("能力标记应落库: %v %#v", err, got)
	}
	found, err := st.FindCredentialByToken(ctx, c.Token)
	if err != nil || found == nil || found.ID != c.ID {
		t.Fatalf("应能按令牌找到: %v %#v", err, found)
	}

	// 普通凭据不受影响，仍用普通前缀。
	ord := &model.MCPCredential{Name: "ordinary", Permissions: model.Permissions{Terminal: true}}
	if err := st.CreateCredential(ctx, ord); err != nil {
		t.Fatal(err)
	}
	if ord.Capability || !strings.HasPrefix(ord.Token, CredentialTokenPrefix) || IsCapabilityTokenShape(ord.Token) {
		t.Fatalf("普通凭据令牌不对: %#v", ord)
	}
}

func TestCreateCapabilityRejectsViolations(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	cases := []struct {
		name string
		mut  func(*model.MCPCredential)
	}{
		{"带治理权限", func(c *model.MCPCredential) { c.Permissions.Feedback = true }},
		{"带运行预设位（#179）", func(c *model.MCPCredential) { c.Permissions.Launcher = true }},
		{"没有终端权限", func(c *model.MCPCredential) { c.Permissions = model.Permissions{} }},
		{"全部主机", func(c *model.MCPCredential) { c.TerminalHosts = model.StringList{"*"} }},
		{"没有授权主机", func(c *model.MCPCredential) { c.TerminalHosts = nil }},
		{"自带令牌", func(c *model.MCPCredential) { c.Token = "rmc-" + strings.Repeat("a", 64) }},
	}
	for _, tc := range cases {
		c := capCred("bad-" + tc.name)
		tc.mut(c)
		if err := st.CreateCredential(ctx, c); err == nil {
			t.Errorf("%s：应被拒绝", tc.name)
		}
	}
	if list, _ := st.ListCredentials(ctx); len(list) != 0 {
		t.Fatalf("被拒绝的凭据不应落库: %d", len(list))
	}

	// 普通凭据不得占用能力令牌前缀。
	ord := &model.MCPCredential{Name: "squatter", Token: "rmc-" + strings.Repeat("b", 64), Permissions: model.Permissions{Terminal: true}}
	if err := st.CreateCredential(ctx, ord); err == nil {
		t.Fatal("普通凭据不得使用能力令牌前缀")
	}
}

func TestUpdateCapabilityCredential(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	c := capCred("upd")
	if err := st.CreateCredential(ctx, c); err != nil {
		t.Fatal(err)
	}

	// 放宽：加治理权限、改成全部主机都被拒绝。
	if _, err := st.UpdateCredential(ctx, c.ID, map[string]any{"permissions": model.Permissions{Terminal: true, Feedback: true}}); err == nil {
		t.Error("不得给能力凭据加治理权限")
	}
	if _, err := st.UpdateCredential(ctx, c.ID, map[string]any{"permissions": model.Permissions{Terminal: true, Launcher: true}}); err == nil {
		t.Error("不得给能力凭据加「运行预设」位（#179）")
	}
	if _, err := st.UpdateCredential(ctx, c.ID, map[string]any{"terminal_hosts": []any{"*"}}); err == nil {
		t.Error("不得把能力凭据改成全部主机")
	}

	// 收紧与普通修改允许：换主机、清空主机、取消终端权限、改名。
	other := "RM-EEEE-FFFF-GGGG-HHHH"
	if _, err := st.UpdateCredential(ctx, c.ID, map[string]any{"terminal_hosts": []any{other}, "name": "renamed"}); err != nil {
		t.Errorf("换成另一台具体主机应允许: %v", err)
	}
	if _, err := st.UpdateCredential(ctx, c.ID, map[string]any{"terminal_hosts": []any{}}); err != nil {
		t.Errorf("清空主机授权是收紧，应允许: %v", err)
	}
	if _, err := st.UpdateCredential(ctx, c.ID, map[string]any{"permissions": model.Permissions{}}); err != nil {
		t.Errorf("取消终端权限是收紧，应允许: %v", err)
	}

	// capability 是只读字段：更新里带它被忽略，不能把能力凭据变回普通凭据，也不能把普通凭据变成能力凭据。
	if out, err := SanitizeCredentialUpdates(map[string]any{"capability": false, "note": "x"}); err != nil || len(out) != 1 {
		t.Fatalf("capability 应被静默忽略: %#v %v", out, err)
	}
	got, _ := st.GetCredential(ctx, c.ID)
	if !got.Capability {
		t.Fatal("能力凭据的类型不可更改")
	}
}

func TestRegenerateKeepsCredentialKind(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	c := capCred("regen")
	if err := st.CreateCredential(ctx, c); err != nil {
		t.Fatal(err)
	}
	old := c.Token
	cred, tok, err := st.RegenerateCredentialToken(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !cred.Capability || tok == old || !IsCapabilityTokenShape(tok) {
		t.Fatalf("能力凭据重新生成后仍应是能力令牌: %q -> %q", old, tok)
	}
	if found, _ := st.FindCredentialByToken(ctx, old); found != nil {
		t.Fatal("旧令牌必须立即失效")
	}

	ord := &model.MCPCredential{Name: "ord-regen", Permissions: model.Permissions{Terminal: true}}
	if err := st.CreateCredential(ctx, ord); err != nil {
		t.Fatal(err)
	}
	_, tok2, err := st.RegenerateCredentialToken(ctx, ord.ID)
	if err != nil || IsCapabilityTokenShape(tok2) || !strings.HasPrefix(tok2, CredentialTokenPrefix) {
		t.Fatalf("普通凭据重新生成后仍应是普通令牌: %q %v", tok2, err)
	}
}
