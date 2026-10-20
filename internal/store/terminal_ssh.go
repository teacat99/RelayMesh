package store

import (
	"context"
	"errors"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

// MaxSSHHosts 是可登记的 SSH 主机数量上限：每台主机都可能占用连接与轮询资源，上限防止被灌满。
const MaxSSHHosts = 50

// ErrSSHHostLimit 表示 SSH 主机数量已达上限。
var ErrSSHHostLimit = errors.New("ssh host limit reached")

// CreateSSHHost 创建一台 SSH 主机。调用方已经完成校验与凭据密封；这里只保证
// 类型正确、数量不超限、机器码不重复（重复返回 ErrTerminalHostExists，调用方可换一个再试）。
func (s *Store) CreateSSHHost(ctx context.Context, h *model.TerminalHost) error {
	if h == nil || h.Kind != model.HostKindSSH || h.MachineCode == "" {
		return errors.New("store: 不是合法的 SSH 主机记录")
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.TerminalHost{}).Where("kind = ?", model.HostKindSSH).Count(&n).Error; err != nil {
			return err
		}
		if n >= MaxSSHHosts {
			return ErrSSHHostLimit
		}
		var exists int64
		if err := tx.Model(&model.TerminalHost{}).Where("machine_code = ?", h.MachineCode).Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			return ErrTerminalHostExists
		}
		return tx.Create(h).Error
	})
}

// SSHConnUpdate 是对 SSH 主机连接信息的部分更新：nil 表示不改。
type SSHConnUpdate struct {
	Addr        *string
	User        *string
	Auth        *string
	Secret      *string // 已密封
	HostKeyType *string
	HostKey     *string
	Fingerprint *string
	KeyState    *string
	Error       *string // 最近一次失败的原因；重新固定主机密钥时用它清除旧的错误
	Gateway     *string // 网关主机的机器码；空串表示改为由 Hub 直连
	TermOnly    *bool   // 经网关接入的主机是否只能当人工终端用（探测不出 POSIX 环境）
}

// CountSSHHostsViaGateway 统计经某台网关主机接入的 SSH 主机数量（删除网关之前用来拦截）。
func (s *Store) CountSSHHostsViaGateway(ctx context.Context, gateway string) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.TerminalHost{}).
		Where("kind = ? AND ssh_gateway = ?", model.HostKindSSH, gateway).Count(&n).Error
	return n, err
}

// ListSSHHostCodesViaGateway 列出经某台网关主机接入的全部 SSH 主机的机器码——**不分状态**（停用的主机也算：
// 它的凭据仍应保留在网关上，重新启用后要能继续使用）。网关据此回收名单之外的凭据，所以绝不能漏。
func (s *Store) ListSSHHostCodesViaGateway(ctx context.Context, gateway string) ([]string, error) {
	var codes []string
	err := s.db.WithContext(ctx).Model(&model.TerminalHost{}).
		Where("kind = ? AND ssh_gateway = ?", model.HostKindSSH, gateway).
		Order("machine_code").Pluck("machine_code", &codes).Error
	return codes, err
}

// UpdateSSHConn 更新 SSH 主机的连接信息；记录不存在或不是 SSH 主机时返回 ErrTerminalHostNotFound。
func (s *Store) UpdateSSHConn(ctx context.Context, code string, u SSHConnUpdate) error {
	updates := map[string]any{"updated_at": time.Now()}
	set := func(col string, v *string) {
		if v != nil {
			updates[col] = *v
		}
	}
	set("ssh_addr", u.Addr)
	set("ssh_user", u.User)
	set("ssh_auth", u.Auth)
	set("ssh_secret", u.Secret)
	set("ssh_host_key_type", u.HostKeyType)
	set("ssh_host_key", u.HostKey)
	set("ssh_fingerprint", u.Fingerprint)
	set("ssh_key_state", u.KeyState)
	set("ssh_error", u.Error)
	set("ssh_gateway", u.Gateway)
	if u.TermOnly != nil {
		updates["ssh_term_only"] = *u.TermOnly
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Model(&model.TerminalHost{}).Where("machine_code = ? AND kind = ?", code, model.HostKindSSH).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrTerminalHostNotFound
		}
		return nil
	})
}

// SSHCheck 是一次连接尝试的结果。
type SSHCheck struct {
	At          time.Time
	OK          bool
	Err         string // 已净化、不含凭据
	KeyMismatch bool   // 主机密钥与固定值不一致：置位后保持，直到管理员重新确认指纹
	Hostname    string
	OS          string
	Arch        string
	// TermOnly 非 nil 时（仅成功的检查）刷新「只能当人工终端用」标记：目标环境可能被重装成了别的系统。
	TermOnly *bool
}

// RecordSSHCheck 记录一次连接尝试。成功时清除错误并刷新主机自报信息；密钥不一致时置位 mismatch（只升不降）。
func (s *Store) RecordSSHCheck(ctx context.Context, code string, c SSHCheck) error {
	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	updates := map[string]any{"ssh_checked_at": &at, "ssh_error": clipRunes(c.Err, 200)}
	if c.OK {
		updates["ssh_error"] = ""
		updates["ssh_ok_at"] = &at
		updates["last_seen_at"] = &at
		if c.Hostname != "" {
			updates["hostname"] = clipRunes(c.Hostname, 128)
		}
		if c.OS != "" {
			updates["os"] = clipRunes(c.OS, 64)
		}
		if c.Arch != "" {
			updates["arch"] = clipRunes(c.Arch, 32)
		}
		if c.TermOnly != nil {
			updates["ssh_term_only"] = *c.TermOnly
		}
	}
	if c.KeyMismatch {
		updates["ssh_key_state"] = model.SSHKeyMismatch
	}
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		res := tx.Model(&model.TerminalHost{}).Where("machine_code = ? AND kind = ?", code, model.HostKindSSH).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrTerminalHostNotFound
		}
		return nil
	})
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
