package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/config"
	"github.com/teacat99/RelayMesh/v2/internal/model"
	"github.com/teacat99/RelayMesh/v2/internal/termproto"
)

// 凭据终端相关字段的上限。它们只是「防写坏」的结构性边界，不是对模型行为的限制。
const (
	CredentialMaxTerminalHosts = 100
	CredentialMaxCIDRs         = 50
	CredentialMaxAllowedDirs   = 50
	CredentialMaxAllowedPaths  = termproto.MaxFileRoots
	credentialMaxItemLen       = 64
	credentialMaxDirLen        = 512

	credentialLimitMaxConcurrent = 64
	credentialLimitMaxSeconds    = 24 * 3600
	credentialLimitMaxOutput     = 64 << 20
)

var terminalHostCodePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// 允许通过 REST 更新的凭据字段。未知字段一律拒绝，避免任意列被改写。
var credentialUpdatableKeys = map[string]struct{}{
	"name":              {},
	"note":              {},
	"host_name":         {},
	"is_active":         {},
	"permissions":       {},
	"terminal_hosts":    {},
	"terminal_mode_cap": {},
	"terminal_limits":   {},
	"expires_at":        {},
	"allowed_cidrs":     {},
}

// 只读字段：客户端把整条记录原样回传时会带上它们，这里静默忽略而不是报错。
var credentialReadOnlyKeys = map[string]struct{}{
	"id": {}, "token": {}, "created_at": {}, "updated_at": {},
	"last_used_at": {}, "last_used_ip": {}, "capability": {},
	"is_env": {}, "tier": {}, "token_weakness": {},
}

// SanitizeCredentialUpdates 校验并归一化 REST 传入的凭据更新，返回可直接交给 GORM 的 map。
func SanitizeCredentialUpdates(in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for key, raw := range in {
		if _, ro := credentialReadOnlyKeys[key]; ro {
			continue
		}
		if _, ok := credentialUpdatableKeys[key]; !ok {
			return nil, NewInvalidInputError(fmt.Sprintf("unknown credential field %q", key))
		}
		switch key {
		case "name":
			s, ok := raw.(string)
			if !ok {
				return nil, NewInvalidInputError("name must be a string")
			}
			out[key] = s
		case "note":
			s, ok := raw.(string)
			if !ok {
				return nil, NewInvalidInputError("note must be a string")
			}
			out[key] = s
		case "host_name":
			s, ok := raw.(string)
			if !ok {
				return nil, NewInvalidInputError("host_name must be a string")
			}
			s = strings.TrimSpace(s)
			if len(s) > CredentialNameMaxLen {
				return nil, NewInvalidInputError(fmt.Sprintf("host_name exceeds %d characters", CredentialNameMaxLen))
			}
			out[key] = s
		case "is_active":
			b, ok := raw.(bool)
			if !ok {
				return nil, NewInvalidInputError("is_active must be a boolean")
			}
			out[key] = b
		case "permissions":
			perms, err := coercePermissions(raw)
			if err != nil {
				return nil, err
			}
			out[key] = perms
		case "terminal_hosts":
			list, err := coerceStringList(raw, "terminal_hosts")
			if err != nil {
				return nil, err
			}
			hosts, err := NormalizeTerminalHosts(list)
			if err != nil {
				return nil, err
			}
			out[key] = hosts
		case "terminal_mode_cap":
			s, ok := raw.(string)
			if !ok {
				return nil, NewInvalidInputError("terminal_mode_cap must be a string")
			}
			if err := ValidateTerminalModeCap(strings.TrimSpace(s)); err != nil {
				return nil, err
			}
			out[key] = strings.TrimSpace(s)
		case "terminal_limits":
			limits, err := coerceTerminalLimits(raw)
			if err != nil {
				return nil, err
			}
			out[key] = limits
		case "expires_at":
			t, err := coerceOptionalTime(raw)
			if err != nil {
				return nil, err
			}
			if t == nil {
				out[key] = nil
			} else {
				out[key] = *t
			}
		case "allowed_cidrs":
			list, err := coerceStringList(raw, "allowed_cidrs")
			if err != nil {
				return nil, err
			}
			cidrs, err := NormalizeAllowedCIDRs(list)
			if err != nil {
				return nil, err
			}
			out[key] = cidrs
		}
	}
	return out, nil
}

// ValidateTerminalModeCap 校验令牌的执行模式上限。
func ValidateTerminalModeCap(v string) error {
	switch v {
	case model.TerminalModeFollow, model.TerminalModeAuto, model.TerminalModeApprove, model.TerminalModeReadonly:
		return nil
	}
	return NewInvalidInputError("terminal_mode_cap must be one of: (empty), auto, approve, readonly")
}

// NormalizeTerminalHosts 去重、裁剪并校验机器码列表；包含 "*" 时折叠为 ["*"]。
func NormalizeTerminalHosts(in []string) (model.StringList, error) {
	seen := make(map[string]struct{}, len(in))
	out := make(model.StringList, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == "*" {
			return model.StringList{"*"}, nil
		}
		if !terminalHostCodePattern.MatchString(item) {
			return nil, NewInvalidInputError(fmt.Sprintf("invalid terminal host code %q", item))
		}
		if _, dup := seen[item]; dup {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) > CredentialMaxTerminalHosts {
		return nil, NewInvalidInputError(fmt.Sprintf("terminal_hosts exceeds %d entries", CredentialMaxTerminalHosts))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// NormalizeAllowedCIDRs 校验来源白名单；每项可以是 CIDR 或单个 IP，单个 IP 保持原样。
func NormalizeAllowedCIDRs(in []string) (model.StringList, error) {
	seen := make(map[string]struct{}, len(in))
	out := make(model.StringList, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(item); err != nil {
			if net.ParseIP(item) == nil {
				return nil, NewInvalidInputError(fmt.Sprintf("invalid allowed_cidrs entry %q", item))
			}
		}
		if _, dup := seen[item]; dup {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) > CredentialMaxCIDRs {
		return nil, NewInvalidInputError(fmt.Sprintf("allowed_cidrs exceeds %d entries", CredentialMaxCIDRs))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ValidateTerminalLimits 校验令牌级限额（全部为零表示不设限，合法）。
// AllowWholeFS 开启时 AllowedPaths 照常校验并保留（关闭开关后恢复为目录限制，永远不会比管理员显式配置的更宽），
// 但它们不起作用，运行时一律以 AllowWholeFS 为准。
func ValidateTerminalLimits(l model.TerminalLimits) (model.TerminalLimits, error) {
	if l.MaxConcurrent < 0 || l.MaxConcurrent > credentialLimitMaxConcurrent {
		return l, NewInvalidInputError(fmt.Sprintf("max_concurrent must be between 0 and %d", credentialLimitMaxConcurrent))
	}
	if l.MaxWaitSeconds < 0 || l.MaxWaitSeconds > credentialLimitMaxSeconds {
		return l, NewInvalidInputError(fmt.Sprintf("max_wait_seconds must be between 0 and %d", credentialLimitMaxSeconds))
	}
	if l.MaxTimeoutSeconds < 0 || l.MaxTimeoutSeconds > credentialLimitMaxSeconds {
		return l, NewInvalidInputError(fmt.Sprintf("max_timeout_seconds must be between 0 and %d", credentialLimitMaxSeconds))
	}
	if l.MaxOutputBytes < 0 || l.MaxOutputBytes > credentialLimitMaxOutput {
		return l, NewInvalidInputError(fmt.Sprintf("max_output_bytes must be between 0 and %d", credentialLimitMaxOutput))
	}
	if len(l.AllowedDirs) > CredentialMaxAllowedDirs {
		return l, NewInvalidInputError(fmt.Sprintf("allowed_dirs exceeds %d entries", CredentialMaxAllowedDirs))
	}
	dirs := make([]string, 0, len(l.AllowedDirs))
	for _, d := range l.AllowedDirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if len(d) > credentialMaxDirLen {
			return l, NewInvalidInputError(fmt.Sprintf("allowed_dirs entry exceeds %d characters", credentialMaxDirLen))
		}
		dirs = append(dirs, d)
	}
	l.AllowedDirs = dirs
	if len(l.AllowedDirs) == 0 {
		l.AllowedDirs = nil
	}
	paths, err := NormalizeAllowedPaths(l.AllowedPaths)
	if err != nil {
		return l, err
	}
	l.AllowedPaths = paths
	return l, nil
}

// NormalizeAllowedPaths 校验并归一化文件工具的允许目录：去空白、去重、限条数与长度，写法必须是明确的绝对路径
// （见 termproto.RootProblem）。文件系统根目录、相对路径、含 .. 的写法一律拒绝——这是一条强制的访问边界，
// 不接受含糊的写法。
func NormalizeAllowedPaths(in []string) ([]string, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > credentialMaxDirLen {
			return nil, NewInvalidInputError(fmt.Sprintf("allowed_paths entry exceeds %d characters", credentialMaxDirLen))
		}
		if problem := termproto.RootProblem(p); problem != "" {
			return nil, NewInvalidInputError(fmt.Sprintf("allowed_paths entry %q is invalid: %s", clipForError(p), problem))
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) > CredentialMaxAllowedPaths {
		return nil, NewInvalidInputError(fmt.Sprintf("allowed_paths exceeds %d entries", CredentialMaxAllowedPaths))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func clipForError(s string) string {
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

// CredentialFilesError 在凭据打开了文件权限位却没有任何访问范围时返回错误。
// 文件权限没有「默认全部可访问」的含义：既不指定目录、也没有明确开启「允许整个文件系统」，就等于没有范围，
// 不允许保存这样的组合。「允许整个文件系统」只能由 allow_whole_fs 显式打开——在 allowed_paths 里写 `/`
// 仍然被拒绝（见 NormalizeAllowedPaths），免得一个手误就等于不设防。
func CredentialFilesError(c *model.MCPCredential) error {
	if c.Permissions.TerminalFiles && !c.TerminalLimits.HasFileScope() {
		return NewInvalidInputError("文件权限必须同时指定允许访问的目录（allowed_paths），或明确开启「允许整个文件系统」（allow_whole_fs）")
	}
	return nil
}

// ValidateCredentialTerminal 校验并就地归一化一条完整凭据的终端相关字段（创建时调用）。
func ValidateCredentialTerminal(c *model.MCPCredential) error {
	c.Permissions = c.Permissions.Normalized()
	c.TerminalModeCap = strings.TrimSpace(c.TerminalModeCap)
	if err := ValidateTerminalModeCap(c.TerminalModeCap); err != nil {
		return err
	}
	hosts, err := NormalizeTerminalHosts(c.TerminalHosts)
	if err != nil {
		return err
	}
	c.TerminalHosts = hosts
	cidrs, err := NormalizeAllowedCIDRs(c.AllowedCIDRs)
	if err != nil {
		return err
	}
	c.AllowedCIDRs = cidrs
	limits, err := ValidateTerminalLimits(c.TerminalLimits)
	if err != nil {
		return err
	}
	c.TerminalLimits = limits
	return CredentialFilesError(c)
}

// CredentialTerminalTokenError 在凭据带终端权限（或「运行预设」权限）而令牌强度不足时返回错误。
// 终端权限等价于远程代码执行，「运行预设」让一个智能体去唤醒另一个智能体（#179），令牌都必须不可猜；服务端自动生成的令牌始终合格。
func CredentialTerminalTokenError(c *model.MCPCredential) error {
	if !c.Permissions.HasTerminal() && !c.Permissions.Launcher {
		return nil
	}
	if reason := config.MCPTokenWeakness(c.Token); reason != "" {
		what := "终端权限"
		if !c.Permissions.HasTerminal() {
			what = "「运行预设」权限"
		}
		return NewInvalidInputError("令牌强度不足（" + reason + "），不能授予" + what + "；请重新生成令牌后再开启")
	}
	return nil
}

// TouchCredentialUsage 记录令牌最近一次使用的时间与来源，不改动 updated_at。
func (s *Store) TouchCredentialUsage(ctx context.Context, id uint, ip string, at time.Time) error {
	if id == 0 {
		return nil
	}
	if len(ip) > 64 {
		ip = ip[:64]
	}
	return s.db.WithContext(ctx).Model(&model.MCPCredential{}).
		Where("id = ?", id).
		UpdateColumns(map[string]any{"last_used_at": at, "last_used_ip": ip}).Error
}

func coercePermissions(raw any) (model.Permissions, error) {
	switch v := raw.(type) {
	case model.Permissions:
		return v.Normalized(), nil
	case map[string]any:
		b, err := json.Marshal(v)
		if err != nil {
			return model.Permissions{}, NewInvalidInputError("invalid permissions")
		}
		var p model.Permissions
		if err := json.Unmarshal(b, &p); err != nil {
			return model.Permissions{}, NewInvalidInputError("invalid permissions")
		}
		return p.Normalized(), nil
	}
	return model.Permissions{}, NewInvalidInputError("permissions must be an object")
}

func coerceStringList(raw any, field string) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case []string:
		return v, nil
	case model.StringList:
		return []string(v), nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, NewInvalidInputError(field + " must be an array of strings")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, NewInvalidInputError(field + " must be an array of strings")
}

func coerceTerminalLimits(raw any) (model.TerminalLimits, error) {
	switch v := raw.(type) {
	case nil:
		return model.TerminalLimits{}, nil
	case model.TerminalLimits:
		return ValidateTerminalLimits(v)
	case map[string]any:
		b, err := json.Marshal(v)
		if err != nil {
			return model.TerminalLimits{}, NewInvalidInputError("invalid terminal_limits")
		}
		var l model.TerminalLimits
		if err := json.Unmarshal(b, &l); err != nil {
			return model.TerminalLimits{}, NewInvalidInputError("invalid terminal_limits")
		}
		return ValidateTerminalLimits(l)
	}
	return model.TerminalLimits{}, NewInvalidInputError("terminal_limits must be an object")
}

func coerceOptionalTime(raw any) (*time.Time, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case time.Time:
		t := v
		return &t, nil
	case *time.Time:
		return v, nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, nil
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, NewInvalidInputError("expires_at must be an RFC3339 timestamp")
		}
		return &t, nil
	}
	return nil, NewInvalidInputError("expires_at must be an RFC3339 timestamp or null")
}
