package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMemStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSanitizeCredentialUpdates(t *testing.T) {
	t.Run("只读字段被忽略，未知字段被拒绝", func(t *testing.T) {
		out, err := SanitizeCredentialUpdates(map[string]any{
			"id": 1, "token": "x", "created_at": "x", "is_env": true, "tier": "relaymesh",
			"last_used_at": "x", "note": "hello",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 || out["note"] != "hello" {
			t.Fatalf("只应保留 note: %#v", out)
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"permissions_json": "x"}); err == nil {
			t.Fatal("未知字段必须被拒绝，避免任意列被改写")
		}
	})

	t.Run("主机列表归一化", func(t *testing.T) {
		out, err := SanitizeCredentialUpdates(map[string]any{"terminal_hosts": []any{" abc-1 ", "abc-1", "", "def_2"}})
		if err != nil {
			t.Fatal(err)
		}
		hosts := out["terminal_hosts"].(model.StringList)
		if len(hosts) != 2 || hosts[0] != "abc-1" || hosts[1] != "def_2" {
			t.Fatalf("应去重去空: %#v", hosts)
		}
		out, err = SanitizeCredentialUpdates(map[string]any{"terminal_hosts": []any{"a", "*", "b"}})
		if err != nil {
			t.Fatal(err)
		}
		if hosts := out["terminal_hosts"].(model.StringList); len(hosts) != 1 || hosts[0] != "*" {
			t.Fatalf("含 * 时应折叠为 [*]: %#v", hosts)
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"terminal_hosts": []any{"bad host!"}}); err == nil {
			t.Fatal("非法机器码必须被拒绝")
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"terminal_hosts": "abc"}); err == nil {
			t.Fatal("非数组必须被拒绝")
		}
	})

	t.Run("来源白名单", func(t *testing.T) {
		if _, err := SanitizeCredentialUpdates(map[string]any{"allowed_cidrs": []any{"10.0.0.0/8", "192.168.1.5", "fd00::/8"}}); err != nil {
			t.Fatalf("合法 CIDR / 单 IP 应通过: %v", err)
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"allowed_cidrs": []any{"10.0.0.0/99"}}); err == nil {
			t.Fatal("非法 CIDR 必须被拒绝")
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"allowed_cidrs": []any{"not-an-ip"}}); err == nil {
			t.Fatal("非法 IP 必须被拒绝")
		}
	})

	t.Run("模式上限与限额", func(t *testing.T) {
		for _, ok := range []string{"", "auto", "approve", "readonly"} {
			if _, err := SanitizeCredentialUpdates(map[string]any{"terminal_mode_cap": ok}); err != nil {
				t.Fatalf("%q 应合法: %v", ok, err)
			}
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"terminal_mode_cap": "root"}); err == nil {
			t.Fatal("未知模式必须被拒绝")
		}
		out, err := SanitizeCredentialUpdates(map[string]any{"terminal_limits": map[string]any{
			"max_concurrent": float64(4), "allowed_dirs": []any{" /srv ", ""},
		}})
		if err != nil {
			t.Fatal(err)
		}
		lim := out["terminal_limits"].(model.TerminalLimits)
		if lim.MaxConcurrent != 4 || len(lim.AllowedDirs) != 1 || lim.AllowedDirs[0] != "/srv" {
			t.Fatalf("限额不符: %#v", lim)
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"terminal_limits": map[string]any{"max_concurrent": float64(-1)}}); err == nil {
			t.Fatal("负数限额必须被拒绝")
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"terminal_limits": map[string]any{"max_concurrent": float64(1000)}}); err == nil {
			t.Fatal("超出结构性上限必须被拒绝")
		}
	})

	t.Run("到期时间", func(t *testing.T) {
		out, err := SanitizeCredentialUpdates(map[string]any{"expires_at": "2030-01-02T03:04:05Z"})
		if err != nil {
			t.Fatal(err)
		}
		if got := out["expires_at"].(time.Time); got.Year() != 2030 {
			t.Fatalf("时间解析错误: %v", got)
		}
		out, err = SanitizeCredentialUpdates(map[string]any{"expires_at": nil})
		if err != nil || out["expires_at"] != nil {
			t.Fatalf("null 应清除到期时间: %#v %v", out, err)
		}
		out, err = SanitizeCredentialUpdates(map[string]any{"expires_at": ""})
		if err != nil || out["expires_at"] != nil {
			t.Fatalf("空串应清除到期时间: %#v %v", out, err)
		}
		if _, err := SanitizeCredentialUpdates(map[string]any{"expires_at": "tomorrow"}); err == nil {
			t.Fatal("非 RFC3339 必须被拒绝")
		}
	})
}

func TestCredentialTerminalFieldsRoundTrip(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	cred := &model.MCPCredential{
		Name:            "exec-agent",
		Permissions:     model.Permissions{Terminal: true},
		TerminalHosts:   model.StringList{"host-a", "host-b"},
		TerminalModeCap: model.TerminalModeApprove,
		TerminalLimits:  model.TerminalLimits{MaxConcurrent: 3, AllowedDirs: []string{"/srv/app"}},
		AllowedCIDRs:    model.StringList{"10.0.0.0/8"},
	}
	exp := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	cred.ExpiresAt = &exp
	if err := st.CreateCredential(ctx, cred); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetCredential(ctx, cred.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Permissions.Terminal || got.Permissions.TerminalFiles {
		t.Fatalf("权限位不符: %#v", got.Permissions)
	}
	if len(got.TerminalHosts) != 2 || got.TerminalModeCap != "approve" || got.TerminalLimits.MaxConcurrent != 3 ||
		len(got.TerminalLimits.AllowedDirs) != 1 || len(got.AllowedCIDRs) != 1 {
		t.Fatalf("终端字段未完整往返: %#v", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("到期时间不符: %v want %v", got.ExpiresAt, exp)
	}
	if !got.AllowsTerminalHost("host-a") || got.AllowsTerminalHost("host-z") {
		t.Fatal("AllowsTerminalHost 判定错误")
	}

	// 更新：清除限额与到期时间，改成全部主机
	updated, err := st.UpdateCredential(ctx, cred.ID, map[string]any{
		"terminal_hosts":  []any{"*"},
		"terminal_limits": map[string]any{},
		"expires_at":      nil,
		"allowed_cidrs":   []any{},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(updated.TerminalHosts) != 1 || updated.TerminalHosts[0] != "*" || !updated.TerminalLimits.IsZero() ||
		updated.ExpiresAt != nil || len(updated.AllowedCIDRs) != 0 {
		t.Fatalf("更新后字段不符: %#v", updated)
	}
	if !updated.AllowsTerminalHost("anything") {
		t.Fatal("[*] 应允许任意主机")
	}

	// 没有终端权限时，即使配置了主机也不允许
	noPerm, err := st.UpdateCredential(ctx, cred.ID, map[string]any{"permissions": model.AllPermissions()})
	if err != nil {
		t.Fatal(err)
	}
	if noPerm.Permissions.HasTerminal() || noPerm.AllowsTerminalHost("host-a") {
		t.Fatal("AllPermissions 不得带终端权限")
	}
}

func TestTerminalPermissionRequiresStrongToken(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()

	// 手工指定的弱令牌：没有终端权限时允许（保持既有行为）
	weakPlain := &model.MCPCredential{Name: "weak-plain", Token: "short-token", Permissions: model.AllPermissions()}
	if err := st.CreateCredential(ctx, weakPlain); err != nil {
		t.Fatalf("无终端权限的弱令牌应允许: %v", err)
	}
	// 同一把弱令牌不能被授予终端权限
	if _, err := st.UpdateCredential(ctx, weakPlain.ID, map[string]any{"permissions": model.Permissions{Terminal: true}}); err == nil {
		t.Fatal("弱令牌不得被授予终端权限")
	}
	// 创建时直接带终端权限也不行
	weakTerm := &model.MCPCredential{Name: "weak-term", Token: "changeme-changeme-changeme-1234", Permissions: model.Permissions{Terminal: true}}
	if err := st.CreateCredential(ctx, weakTerm); err == nil {
		t.Fatal("带占位符的令牌不得带终端权限创建")
	}
	// 「运行预设」（#179）让一个智能体去唤醒另一个：和终端权限同一条强度要求
	if _, err := st.UpdateCredential(ctx, weakPlain.ID, map[string]any{"permissions": model.Permissions{Launcher: true}}); err == nil {
		t.Fatal("弱令牌不得被授予「运行预设」权限")
	} else if !strings.Contains(err.Error(), "运行预设") {
		t.Fatalf("错误应点明是「运行预设」权限而不是终端权限: %v", err)
	}
	weakLauncher := &model.MCPCredential{Name: "weak-launcher", Token: "short-token", Permissions: model.Permissions{Launcher: true}}
	if err := st.CreateCredential(ctx, weakLauncher); err == nil {
		t.Fatal("弱令牌不得带「运行预设」权限创建")
	}
	strongLauncher := &model.MCPCredential{Name: "strong-launcher", Permissions: model.Permissions{Feedback: true, Launcher: true}}
	if err := st.CreateCredential(ctx, strongLauncher); err != nil {
		t.Fatalf("自动生成的令牌应可带「运行预设」权限: %v", err)
	}
	// 服务端生成的令牌始终合格
	strong := &model.MCPCredential{
		Name: "strong-term", Permissions: model.Permissions{Terminal: true, TerminalFiles: true},
		TerminalLimits: model.TerminalLimits{AllowedPaths: []string{"/srv/data"}},
	}
	if err := st.CreateCredential(ctx, strong); err != nil {
		t.Fatalf("自动生成的令牌应可带终端权限: %v", err)
	}
	// 更新里不触碰权限时，不因令牌强度阻塞（例如只改备注）
	if _, err := st.UpdateCredential(ctx, weakPlain.ID, map[string]any{"note": "ok"}); err != nil {
		t.Fatalf("不改权限的更新不应被拦截: %v", err)
	}
}

func TestTouchCredentialUsageDoesNotBumpUpdatedAt(t *testing.T) {
	st := newMemStore(t)
	ctx := context.Background()
	cred := &model.MCPCredential{Name: "touch", Permissions: model.AllPermissions()}
	if err := st.CreateCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetCredential(ctx, cred.ID)

	at := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if err := st.TouchCredentialUsage(ctx, cred.ID, "203.0.113.9", at); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetCredential(ctx, cred.ID)
	if after.LastUsedAt == nil || !after.LastUsedAt.Equal(at) || after.LastUsedIP != "203.0.113.9" {
		t.Fatalf("最近使用未记录: %#v", after)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatal("记录最近使用不应改动 updated_at")
	}
}

// 老库（没有任何新列）升级后：旧行可读，新字段为零值，权限里没有终端位；再次迁移是幂等的。
func TestLegacyCredentialTableMigratesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`CREATE TABLE mcp_credentials (
		id integer PRIMARY KEY AUTOINCREMENT,
		name text NOT NULL,
		token text NOT NULL,
		host_name text,
		is_active numeric DEFAULT true,
		permissions text,
		note text,
		created_at datetime,
		updated_at datetime
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`CREATE UNIQUE INDEX idx_mcp_credentials_name ON mcp_credentials(name)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`CREATE UNIQUE INDEX idx_mcp_credentials_token ON mcp_credentials(token)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec(`INSERT INTO mcp_credentials (name, token, host_name, is_active, permissions, note, created_at, updated_at)
		VALUES ('old-agent', 'rm-legacy-token-0123456789abcdef', 'wsl', 1,
		'{"feedback":true,"sessions":true,"system_info":true,"skills":true,"configure":true,"execute":true}',
		'old', datetime('now'), datetime('now'))`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := legacy.DB()
	_ = sqlDB.Close()

	for i := 0; i < 2; i++ { // 第二次打开验证迁移幂等
		st, err := New(path)
		if err != nil {
			t.Fatalf("第 %d 次打开旧库失败: %v", i+1, err)
		}
		creds, err := st.ListCredentials(context.Background())
		if err != nil || len(creds) != 1 {
			t.Fatalf("旧凭据应可读: %v %d", err, len(creds))
		}
		c := creds[0]
		if c.Name != "old-agent" || !c.IsActive || !c.Permissions.Feedback || c.Permissions.HasTerminal() {
			t.Fatalf("旧凭据内容或权限被改写: %#v", c)
		}
		if len(c.TerminalHosts) != 0 || c.TerminalModeCap != "" || !c.TerminalLimits.IsZero() ||
			c.ExpiresAt != nil || len(c.AllowedCIDRs) != 0 || c.LastUsedAt != nil {
			t.Fatalf("新字段应为零值: %#v", c)
		}
		_ = st.Close()
	}
}
