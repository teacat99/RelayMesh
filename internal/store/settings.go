package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/teacat99/RelayMesh/internal/model"
	"gorm.io/gorm"
)

const (
	GlobalSettingsKey = "app_settings"
	AuthCredentialsKey = "auth_credentials"
)

type AuthCredentials struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SecuritySettings struct {
	BruteForceProtection bool     `json:"bruteForceProtection"` // 是否开启反爆破保护
	MaxFailedAttempts    int      `json:"maxFailedAttempts"`    // 最大连续失败尝试次数 (默认 5)
	LockoutMinutes       int      `json:"lockoutMinutes"`       // 封禁时长 (分钟，默认 15)
	WhitelistIPs         []string `json:"whitelistIps"`         // 白名单 IP 列表
}

type GlobalAppSettings struct {
	HostName                    string           `json:"hostName"`
	DefaultTimeoutSeconds       int              `json:"defaultTimeoutSeconds"`
	PromptWaitMinutes           int              `json:"promptWaitMinutes"`
	MaxNoFeedbackChecks         int              `json:"maxNoFeedbackChecks"`
	DefaultWaitCountdownMinutes int              `json:"defaultWaitCountdownMinutes"`
	UserPresence                string           `json:"userPresence"`
	Security                    SecuritySettings `json:"security"`
	FlowPrompts                 struct {
		Online struct {
			WaitPollPrompt  string `json:"waitPollPrompt"`
			ExhaustedPrompt string `json:"exhaustedPrompt"`
		} `json:"online"`
		Away struct {
			ImmediatePrompt string `json:"immediatePrompt"`
		} `json:"away"`
		Autopilot struct {
			ImmediatePrompt string `json:"immediatePrompt"`
		} `json:"autopilot"`
	} `json:"flowPrompts"`
}

// GetSettings 从数据库获取全局配置，若不存在返回默认 map
func (s *Store) GetSettings(ctx context.Context) (map[string]any, error) {
	return s.GetSettingsWithDB(ctx, s.db)
}

// GetSettingsWithDB 从指定 DB (支持事务 tx) 获取全局配置
func (s *Store) GetSettingsWithDB(ctx context.Context, db *gorm.DB) (map[string]any, error) {
	var record model.SystemSetting
	err := db.WithContext(ctx).Where("key = ?", GlobalSettingsKey).First(&record).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return map[string]any{}, nil
		}
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(record.Value), &result); err != nil {
		return map[string]any{}, nil
	}
	return result, nil
}

// GetGlobalAppSettings 获取强类型系统全局配置，含完备默认值
func (s *Store) GetGlobalAppSettings(ctx context.Context) (*GlobalAppSettings, error) {
	return s.GetGlobalAppSettingsWithDB(ctx, s.db)
}

// GetGlobalAppSettingsWithDB 从指定 DB (支持事务 tx) 获取强类型系统全局配置
func (s *Store) GetGlobalAppSettingsWithDB(ctx context.Context, db *gorm.DB) (*GlobalAppSettings, error) {
	settingsMap, err := s.GetSettingsWithDB(ctx, db)
	if err != nil {
		return nil, err
	}

	appSettings := &GlobalAppSettings{
		PromptWaitMinutes:           2,
		MaxNoFeedbackChecks:         24,
		DefaultWaitCountdownMinutes: 2,
		UserPresence:                "online",
		Security: SecuritySettings{
			BruteForceProtection: true,
			MaxFailedAttempts:    5,
			LockoutMinutes:       15,
			WhitelistIPs:         []string{"127.0.0.1", "::1"},
		},
	}
	appSettings.FlowPrompts.Online.WaitPollPrompt = "严格执行：等待 {wait_minutes} 分钟，期间不得输出、调用工具或执行任何其他操作。等待结束后，第一步调用 continue_feedback_session 工具获取反馈；本指令不得作为循环触发或循环判定条件；循环与否仅由 continue_feedback_session 的返回结果决定。"
	appSettings.FlowPrompts.Online.ExhaustedPrompt = "用户反馈已超时。立即挂起当前任务，停止后续执行，并终止终端中仍在运行的后台进程。随后总结当前会话的执行进度、已完成事项、未完成事项及后续恢复点；总结完成后结束本轮执行，不再进行其他操作。"
	appSettings.FlowPrompts.Away.ImmediatePrompt = "【系统回执·用户暂离】用户当前处于暂离状态。请将非阻塞性问题记录暂存，优先推进已明确授权的开发范围，不可逆动作一律暂缓。"
	appSettings.FlowPrompts.Autopilot.ImmediatePrompt = "【系统回执·托管自驾】当前处于 M-C 自驾模式，方案已自动接管授权。请严格按照规划目标推进，如遇不可逆高风险操作（DB迁移/部署/破坏性命令）请立即停下。"

	if len(settingsMap) > 0 {
		bytes, err := json.Marshal(settingsMap)
		if err == nil {
			_ = json.Unmarshal(bytes, appSettings)
		}
	}

	if appSettings.PromptWaitMinutes <= 0 {
		appSettings.PromptWaitMinutes = 2
	}
	if appSettings.MaxNoFeedbackChecks <= 0 {
		appSettings.MaxNoFeedbackChecks = 24
	}
	if appSettings.DefaultWaitCountdownMinutes < 0 {
		appSettings.DefaultWaitCountdownMinutes = 2
	}
	if appSettings.UserPresence == "" {
		appSettings.UserPresence = "online"
	}
	if appSettings.FlowPrompts.Online.WaitPollPrompt == "" {
		appSettings.FlowPrompts.Online.WaitPollPrompt = "严格执行：等待 {wait_minutes} 分钟，期间不得输出、调用工具或执行任何其他操作。等待结束后，第一步调用 continue_feedback_session 工具获取反馈；本指令不得作为循环触发或循环判定条件；循环与否仅由 continue_feedback_session 的返回结果决定。"
	}
	if appSettings.FlowPrompts.Online.ExhaustedPrompt == "" {
		appSettings.FlowPrompts.Online.ExhaustedPrompt = "用户反馈已超时。立即挂起当前任务，停止后续执行，并终止终端中仍在运行的后台进程。随后总结当前会话的执行进度、已完成事项、未完成事项及后续恢复点；总结完成后结束本轮执行，不再进行其他操作。"
	}

	if appSettings.Security.MaxFailedAttempts <= 0 {
		appSettings.Security.MaxFailedAttempts = 5
	}
	if appSettings.Security.LockoutMinutes <= 0 {
		appSettings.Security.LockoutMinutes = 15
	}

	return appSettings, nil
}

// SaveSettings 保存全局配置至数据库
func (s *Store) SaveSettings(ctx context.Context, settings map[string]any) error {
	bytes, err := json.Marshal(settings)
	if err != nil {
		return err
	}

	record := model.SystemSetting{
		Key:       GlobalSettingsKey,
		Value:     string(bytes),
		UpdatedAt: time.Now(),
	}

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Save(&record).Error
	})
}

// GetAuthCredentials 获取数据库中持久化的账号密码
func (s *Store) GetAuthCredentials(ctx context.Context) (*AuthCredentials, error) {
	return s.GetAuthCredentialsWithDB(ctx, s.db)
}

// GetAuthCredentialsWithDB 从指定 DB (支持事务) 获取数据库中持久化的账号密码
func (s *Store) GetAuthCredentialsWithDB(ctx context.Context, db *gorm.DB) (*AuthCredentials, error) {
	var record model.SystemSetting
	err := db.WithContext(ctx).Where("key = ?", AuthCredentialsKey).First(&record).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}

	var creds AuthCredentials
	if err := json.Unmarshal([]byte(record.Value), &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

// SaveAuthCredentials 保存或更新账号密码至数据库
func (s *Store) SaveAuthCredentials(ctx context.Context, username, password string) error {
	creds := AuthCredentials{
		Username:  username,
		Password:  password,
		UpdatedAt: time.Now(),
	}
	bytes, err := json.Marshal(creds)
	if err != nil {
		return err
	}

	record := model.SystemSetting{
		Key:       AuthCredentialsKey,
		Value:     string(bytes),
		UpdatedAt: time.Now(),
	}

	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Save(&record).Error
	})
}

// ResetAuthCredentials 删除数据库中的账号密码记录，回退为环境变量值
func (s *Store) ResetAuthCredentials(ctx context.Context) error {
	return s.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Where("key = ?", AuthCredentialsKey).Delete(&model.SystemSetting{}).Error
	})
}

