package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"gorm.io/gorm"

	"github.com/teacat99/RelayMesh/v2/internal/model"
)

// 主机「对新建模型的默认态」（D-201 / P7）。
//
// 设计要点：默认态只在「新增主机」与「新建模型令牌」这两个时刻生效，并且生效方式是**把主机写成令牌自己主机名单里的
// 明确条目**——运行期间不存在任何继承：判定一个令牌能否使用某台主机，始终只看令牌自己的名单（model.MCPCredential.AllowsTerminalHost）。
// 这样管理员在令牌上看到的名单就是事实，事后改主机的默认态不会让既有令牌悄悄多出或少掉权限。

// HostDefaultState 把库里的默认态归一成 allow / deny（空串等价于 deny）。
func HostDefaultState(v string) string {
	if v == model.HostDefaultAllow {
		return model.HostDefaultAllow
	}
	return model.HostDefaultDeny
}

// ValidateHostDefaultForNewModels 校验默认态取值：空串或 deny 表示不预先授予，allow 表示预先授予。
func ValidateHostDefaultForNewModels(v string) error {
	switch v {
	case "", model.HostDefaultDeny, model.HostDefaultAllow:
		return nil
	}
	return errors.New("新模型默认态只能是 allow / deny")
}

// DefaultAllowHostCodes 返回「对新建模型令牌预先授予」的主机机器码：已启用、默认态为 allow，按机器码排序。
func (s *Store) DefaultAllowHostCodes(ctx context.Context) ([]string, error) {
	var codes []string
	err := s.db.WithContext(ctx).Model(&model.TerminalHost{}).
		Where("status = ? AND default_for_new_models = ?", "enabled", model.HostDefaultAllow).
		Order("machine_code ASC").Pluck("machine_code", &codes).Error
	if err != nil {
		return nil, err
	}
	if len(codes) > CredentialMaxTerminalHosts {
		codes = codes[:CredentialMaxTerminalHosts]
	}
	return codes, nil
}

// credentialTakesDefaultGrant 判断一个令牌是否参与「默认授予」：
// 带终端或文件能力、已经是「全部主机」的不需要、能力 URL 令牌（要求逐台授权并随连接器分发）从不被自动授予。
func credentialTakesDefaultGrant(c *model.MCPCredential) bool {
	if c.Capability || !(c.Permissions.Terminal || c.Permissions.TerminalFiles) {
		return false
	}
	for _, h := range c.TerminalHosts {
		if h == "*" {
			return false
		}
	}
	return true
}

// HostGrantResult 是把一台主机写入模型令牌名单的结果。
type HostGrantResult struct {
	// Granted 是这次新写入名单的令牌名称（令牌名不是机密，可用于审计与界面提示）。
	Granted []string `json:"granted"`
	// Full 是名单已满（达到 CredentialMaxTerminalHosts）、无法再写入的令牌数。
	Full int `json:"full"`
}

// PreviewHostGrant 返回把 machineCode 授予现有模型令牌时会被改动的令牌名称，不写入任何数据。
func (s *Store) PreviewHostGrant(ctx context.Context, machineCode string) (HostGrantResult, error) {
	var res HostGrantResult
	creds, err := s.ListCredentials(ctx)
	if err != nil {
		return res, err
	}
	for i := range creds {
		c := &creds[i]
		if !credentialTakesDefaultGrant(c) || c.AllowsTerminalHost(machineCode) {
			continue
		}
		if len(c.TerminalHosts) >= CredentialMaxTerminalHosts {
			res.Full++
			continue
		}
		res.Granted = append(res.Granted, c.Name)
	}
	sort.Strings(res.Granted)
	return res, nil
}

// GrantHostToTerminalCredentials 把 machineCode 写入所有「带终端能力、不是通配、尚未包含它」的模型令牌的主机名单。
// 整个过程在同一个事务里完成：要么全部写入，要么都不写。
func (s *Store) GrantHostToTerminalCredentials(ctx context.Context, machineCode string) (HostGrantResult, error) {
	var res HostGrantResult
	if !terminalHostCodePattern.MatchString(machineCode) {
		return res, NewInvalidInputError(fmt.Sprintf("invalid terminal host code %q", machineCode))
	}
	err := s.WithTx(ctx, func(tx *gorm.DB) error {
		res = HostGrantResult{}
		var creds []model.MCPCredential
		if err := tx.Order("created_at ASC").Find(&creds).Error; err != nil {
			return err
		}
		for i := range creds {
			c := &creds[i]
			if !credentialTakesDefaultGrant(c) || c.AllowsTerminalHost(machineCode) {
				continue
			}
			if len(c.TerminalHosts) >= CredentialMaxTerminalHosts {
				res.Full++
				continue
			}
			next := make(model.StringList, 0, len(c.TerminalHosts)+1)
			next = append(next, c.TerminalHosts...)
			next = append(next, machineCode)
			if err := tx.Model(&model.MCPCredential{}).Where("id = ?", c.ID).Update("terminal_hosts", next).Error; err != nil {
				return err
			}
			res.Granted = append(res.Granted, c.Name)
		}
		return nil
	})
	if err != nil {
		return HostGrantResult{}, err
	}
	sort.Strings(res.Granted)
	return res, nil
}
