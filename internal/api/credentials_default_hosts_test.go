package api

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 新建带终端能力的令牌时，按各主机的「对新模型默认授予」预填主机名单（D-201 / P7）。
// 预填写成明确的机器码条目；明确给出的名单（哪怕是空数组）永远优先，通配与能力 URL 令牌不受影响。
func TestCreateCredentialPrefillsHostsFromDefaultAllow(t *testing.T) {
	srv, _ := newConvergenceServer(t, nil)
	ctx := context.Background()
	jwt := wiredLogin(t, srv)
	unlocked := []string{"Authorization", "Bearer " + jwt, "X-Terminal-Unlock", wiredUnlock(t, srv, jwt)}

	hosts := []model.TerminalHost{
		{MachineCode: "RM-ALLOW-0001", PublicKey: "k1", Status: "enabled", DefaultForNewModels: model.HostDefaultAllow},
		{MachineCode: "RM-ALLOW-0002", PublicKey: "k2", Status: "enabled", DefaultForNewModels: model.HostDefaultAllow},
		{MachineCode: "RM-DENY-0001", PublicKey: "k3", Status: "enabled", DefaultForNewModels: model.HostDefaultDeny},
		{MachineCode: "RM-LEGACY-01", PublicKey: "k4", Status: "enabled"}, // 旧库里没有这一列的值：等价于 deny
		{MachineCode: "RM-OFF-00001", PublicKey: "k5", Status: "disabled", DefaultForNewModels: model.HostDefaultAllow},
		{MachineCode: "RM-BLOCK-001", PublicKey: "k6", Status: "blocked", DefaultForNewModels: model.HostDefaultAllow},
	}
	for i := range hosts {
		if err := srv.store.SaveTerminalHost(ctx, &hosts[i]); err != nil {
			t.Fatalf("写入主机 %s: %v", hosts[i].MachineCode, err)
		}
	}

	hostsOf := func(res wiredResp) []string {
		t.Helper()
		if res.Code != http.StatusOK {
			t.Fatalf("创建失败: %d %s", res.Code, res.Raw)
		}
		cred, _ := res.Body["credential"].(map[string]any)
		raw, _ := cred["terminal_hosts"].([]any)
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			out = append(out, v.(string))
		}
		return out
	}

	t.Run("缺省名单按默认态预填，只含已启用且默认授予的主机", func(t *testing.T) {
		res := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "模型A", "permissions": map[string]any{"terminal": true}}, unlocked...)
		if got := hostsOf(res); !slices.Equal(got, []string{"RM-ALLOW-0001", "RM-ALLOW-0002"}) {
			t.Fatalf("预填结果不符: %v", got)
		}
	})
	t.Run("明确给出空数组表示一台也不授予", func(t *testing.T) {
		res := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "模型B", "permissions": map[string]any{"terminal": true}, "terminal_hosts": []string{}}, unlocked...)
		if got := hostsOf(res); len(got) != 0 {
			t.Fatalf("明确的空名单应被尊重: %v", got)
		}
	})
	t.Run("明确给出名单时原样使用", func(t *testing.T) {
		res := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "模型C", "permissions": map[string]any{"terminal": true}, "terminal_hosts": []string{"RM-DENY-0001"}}, unlocked...)
		if got := hostsOf(res); !slices.Equal(got, []string{"RM-DENY-0001"}) {
			t.Fatalf("明确的名单应原样保存: %v", got)
		}
	})
	t.Run("没有终端能力的令牌不预填", func(t *testing.T) {
		res := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "只有反馈", "permissions": map[string]any{"feedback": true}}, unlocked...)
		if got := hostsOf(res); len(got) != 0 {
			t.Fatalf("不带终端能力的令牌不应被预填: %v", got)
		}
	})
	t.Run("能力 URL 令牌不预填（必须逐台授权）", func(t *testing.T) {
		res := wiredCall(srv, "POST", "/api/v1/credentials", map[string]any{"name": "连接器", "capability": true, "permissions": map[string]any{"terminal": true}, "terminal_hosts": []string{"RM-DENY-0001"}}, unlocked...)
		if got := hostsOf(res); !slices.Equal(got, []string{"RM-DENY-0001"}) {
			t.Fatalf("能力 URL 令牌只保留明确给出的主机: %v", got)
		}
	})
}
