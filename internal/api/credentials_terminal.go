package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/terminal"
)

// SetTerminal 注入终端 Hub：令牌管理里涉及终端的操作需要它做二次解锁校验，令牌变更后也要通知它取消进行中的运行。
func (h *APIHandler) SetTerminal(hub *terminal.Hub) { h.terminal = hub }

// carriesTerminal 判断一个令牌是否带有任何终端相关的授权。
// 「运行预设」（Launcher，#179）也算：它能让项目管理 Agent 经 ExecMesh 在主机上拉起一个智能体，授予它与授予终端位同级，需要二次解锁。
func carriesTerminal(c *model.MCPCredential) bool {
	return c.Permissions.Terminal || c.Permissions.TerminalFiles || c.Permissions.Launcher ||
		len(c.TerminalHosts) > 0 || c.TerminalModeCap != "" || !c.TerminalLimits.IsZero()
}

// requireTerminalUnlock 对「授予或改变终端能力」的令牌管理操作要求二次解锁（重新输入密码换取的解锁令牌）。
// 终端能力等同于在远端机器上执行命令，不能只凭一个登录 JWT 就授予、放宽或读出：
// 登录令牌一旦泄露，攻击者也不应能为自己签发（或取走）一把能执行命令的钥匙。
// 收紧类的操作（停用 / 删除令牌、取消终端权限、清空主机授权）不经过这里，保持「刹车只需登录」。
// 返回 false 表示已写入响应，调用方应直接返回。
func (h *APIHandler) requireTerminalUnlock(c *gin.Context) bool {
	if h.terminal == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "终端功能在此服务上不可用，无法授予终端权限", "code": "terminal_unavailable",
		})
		return false
	}
	return h.terminal.RequireUnlock(c)
}

// requireFreshTerminalUnlock 同 requireTerminalUnlock，但还要求解锁令牌是刚刚才用密码换取的（见 terminal.FreshUnlockWindow）。
// 授予「允许整个文件系统」这类把访问边界整个拆掉的操作走这里：subject 说明是哪把令牌，写进被拒绝时的审计。
func (h *APIHandler) requireFreshTerminalUnlock(c *gin.Context, subject string) bool {
	if h.terminal == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "终端功能在此服务上不可用，无法授予文件系统访问", "code": "terminal_unavailable",
		})
		return false
	}
	return h.terminal.RequireFreshUnlock(c, subject)
}

// credentialSnapshot 取一把令牌「更新之后」与终端文件范围相关的样子：先取更新前的值，请求里带了的字段整体替换
// （与 store 的更新语义一致：permissions / terminal_limits 都是整体覆盖，不是逐字段合并）。无法解析的字段按「没带」处理，
// 这样的请求随后会被 store 当作非法输入拒绝，不会产生任何授权。
func credentialSnapshot(before *model.MCPCredential, body map[string]any) (model.Permissions, model.TerminalLimits) {
	var perms model.Permissions
	var limits model.TerminalLimits
	if before != nil {
		perms, limits = before.Permissions, before.TerminalLimits
	}
	if raw, ok := body["permissions"]; ok {
		if p, ok := raw.(model.Permissions); ok {
			perms = p
		} else if b, err := json.Marshal(raw); err == nil {
			var p model.Permissions
			if json.Unmarshal(b, &p) == nil {
				perms = p
			}
		}
	}
	if raw, ok := body["terminal_limits"]; ok {
		if l, ok := raw.(model.TerminalLimits); ok {
			limits = l
		} else if b, err := json.Marshal(raw); err == nil {
			var l model.TerminalLimits
			if json.Unmarshal(b, &l) == nil {
				limits = l
			}
		}
	}
	return perms, limits
}

// credentialUpdateGrantsWholeFS 判断一次更新是否让令牌从「没有」整个文件系统的访问变成「有」：
// 文件权限位与 allow_whole_fs 开关同时成立才算有（见 model.EffectiveWholeFS）。
// 单独打开开关（文件权限位关着）或单独打开文件权限位（开关早已存着）都可能完成这个转变，所以按转变前后的结果比较，
// 而不是只看请求里有没有 allow_whole_fs——否则「先存开关、再单独开权限位」就能绕开重新验证密码。
func credentialUpdateGrantsWholeFS(before *model.MCPCredential, body map[string]any) bool {
	var wasEffective bool
	if before != nil {
		wasEffective = model.EffectiveWholeFS(before.Permissions, before.TerminalLimits)
	}
	perms, limits := credentialSnapshot(before, body)
	return !wasEffective && model.EffectiveWholeFS(perms, limits)
}

// wholeFSEffective 报告一把令牌（更新前的样子）此刻是否拥有整个文件系统的访问；nil 表示没有。
func wholeFSEffective(c *model.MCPCredential) bool {
	return c != nil && model.EffectiveWholeFS(c.Permissions, c.TerminalLimits)
}

// wholeFSLabel 是写进审计 / 日志的令牌标识：只有编号与名称，没有令牌内容。
func wholeFSLabel(c *model.MCPCredential) string {
	return fmt.Sprintf("令牌 #%d「%s」", c.ID, c.Name)
}

// wholeFSSubject 说明被拒绝的是哪把令牌上的什么操作（令牌不存在时只有编号）。
func wholeFSSubject(c *model.MCPCredential, id uint) string {
	if c == nil {
		return fmt.Sprintf("令牌 #%d：开启「允许整个文件系统」", id)
	}
	return wholeFSLabel(c) + "：开启「允许整个文件系统」"
}

// permissionsFromBody 取出更新请求里的 permissions 字段（没有或无法解析则返回 nil）。
func permissionsFromBody(body map[string]any) *model.Permissions {
	raw, ok := body["permissions"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var p model.Permissions
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	return &p
}

// credentialUpdateNeedsUnlock 判断一次令牌更新是否会授予或放宽终端能力，需要二次解锁。
//
//   - 请求把终端权限位打开：一律需要（授予）；
//   - 令牌更新前已带终端能力：除「纯收紧」外（改名 / 备注、停用、清空主机授权、关闭终端权限位）都需要，
//     这包括重新启用、延长有效期、改来源白名单、改模式上限与限额——它们都可能放宽可用范围；
//   - 与终端无关的令牌：不需要。
func credentialUpdateNeedsUnlock(before *model.MCPCredential, body map[string]any) bool {
	if p := permissionsFromBody(body); p != nil && (p.Terminal || p.TerminalFiles || p.Launcher) {
		return true
	}
	if before == nil || !carriesTerminal(before) {
		return false
	}
	for k, v := range body {
		switch k {
		case "name", "note", "host_name", "permissions", "capability":
			// 与终端能力无关，或已在上面判断过是否在授予终端位；capability 在更新里是只读字段（会被忽略）。
		case "is_active":
			if on, ok := v.(bool); !ok || on {
				return true // 启用 = 放开；停用 = 刹车
			}
		case "terminal_hosts":
			if list, ok := v.([]any); !ok || len(list) > 0 {
				return true // 清空 = 收紧；其他任何值都可能放宽
			}
		default:
			// terminal_mode_cap / terminal_limits / expires_at / allowed_cidrs 以及未知字段：按保守处理。
			return true
		}
	}
	return false
}
