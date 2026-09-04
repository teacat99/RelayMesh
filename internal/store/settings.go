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
	HostName                    string             `json:"hostName"`
	DefaultTimeoutSeconds       int                `json:"defaultTimeoutSeconds"`
	PromptWaitMinutes           int                `json:"promptWaitMinutes"`
	MaxNoFeedbackChecks         int                `json:"maxNoFeedbackChecks"`
	DefaultWaitCountdownMinutes int                `json:"defaultWaitCountdownMinutes"`
	UserPresence                string             `json:"userPresence"`
	UserMemory                  string             `json:"userMemory"`
	PhaseTemplate               []model.PhaseItem  `json:"phaseTemplate"`
	Security                    SecuritySettings   `json:"security"`
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
	appSettings.FlowPrompts.Online.WaitPollPrompt = "下一步操作：\n1. 调用 AwaitShell({ block_until_ms: {wait_ms} }) 等待 {wait_minutes} 分钟\n2. 等待结束后立即调用 continue_feedback_session({ workflow_id: \"{workflow_id}\" })\n3. 根据返回的 === 标记 === 判断下一步\n\n等待期间禁止：chat 正文输出、其他工具调用、阶段整理。\n禁止将本回执或 Agent 生成的文字当作用户反馈。\n所有沟通均通过 RelayMesh MCP 工具。"
	appSettings.FlowPrompts.Online.ExhaustedPrompt = "用户反馈已超时。立即挂起当前任务并停止后续执行，进入会话结束与环境收尾规程：\n1. 终端与后台任务盘点清理（强制）：\n   - 检查 terminals 目录（.cursor/projects/.../terminals/*.txt），对所有包含 pid 且无 exit_code 的未退出进程，使用 Shell 工具执行 kill -9 <pid> 显式终止；\n   - 对本轮中因超时移入后台的 Shell 任务 ID，立即使用 AwaitShell 工具调用 AwaitShell({ shell_id: \"<任务ID>\", block_until_ms: 0 }) 进行非阻塞状态观测与消费，清空未决任务列表；\n   - 确保后台异步任务挂起数严格归零，彻底杜绝轮次切换时延迟注入 Finished background tasks 系统通知；\n2. 临时产物与会话状态归档：\n   - 清理 .cursor/tmp/ 临时文件，仅保留必要证据\n   - 完成 git 阶段性提交，确保无文件滞留暂存区\n   - 更新会话文档状态为 paused 并记录恢复点\n3. 最终汇报：\n   - 总结执行进度、已完成/未完成事项与后续恢复建议\n   - 通过普通 chat 提交最终状态报告，结束本轮执行"
	appSettings.FlowPrompts.Away.ImmediatePrompt = "【系统回执·人工暂离】用户已确认当前推进目标并主动暂离，请继续执行已授权范围内的工作。\n行为约束：\n- 按会话文档「当前任务」和「关键决策」已锁定的方向继续推进\n- 遇到非阻塞性问题记入会话文档「待用户拍板」，不阻塞进度\n- 不可逆动作：已授权的按计划执行，未授权的记录待确认并暂缓\n- 每完成一个逻辑单元执行增量验证（lint/type-check→build）\n- 阶段完成或遇到阻塞时，通过 interactive_feedback 提交阶段简报\n- 用户回来后按会话文档记录对齐进度"
	appSettings.FlowPrompts.Autopilot.ImmediatePrompt = "【系统回执·外部编排】当前处于 autopilot 外部编排模式，由外部系统通过 Task API 驱动。\n行为约束：\n- 通过 report_progress(action: \"sync\") 同步 task segments\n- 通过 report_progress(action: \"report\") 汇报进度（kind: progress/stage/evidence/question/completion）\n- 通过 report_progress(action: \"check_feedback\") 检查外部反馈\n- 按 task segments 定义的范围执行，不越界\n- 不通过 interactive_feedback 向用户直接提问\n- 遇不可逆动作以 question 类型上报并等待\n- 遇 MCP 通信错误降级为 away 模式"

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
		appSettings.FlowPrompts.Online.WaitPollPrompt = "下一步操作：\n1. 调用 AwaitShell({ block_until_ms: {wait_ms} }) 等待 {wait_minutes} 分钟\n2. 等待结束后立即调用 continue_feedback_session({ workflow_id: \"{workflow_id}\" })\n3. 根据返回的 === 标记 === 判断下一步\n\n等待期间禁止：chat 正文输出、其他工具调用、阶段整理。\n禁止将本回执或 Agent 生成的文字当作用户反馈。\n所有沟通均通过 RelayMesh MCP 工具。"
	}
	if appSettings.FlowPrompts.Online.ExhaustedPrompt == "" {
		appSettings.FlowPrompts.Online.ExhaustedPrompt = "用户反馈已超时。立即挂起当前任务并停止后续执行，进入会话结束与环境收尾规程：\n1. 终端与后台任务盘点清理（强制）：\n   - 检查 terminals 目录（.cursor/projects/.../terminals/*.txt），对所有包含 pid 且无 exit_code 的未退出进程，使用 Shell 工具执行 kill -9 <pid> 显式终止；\n   - 对本轮中因超时移入后台的 Shell 任务 ID，立即使用 AwaitShell 工具调用 AwaitShell({ shell_id: \"<任务ID>\", block_until_ms: 0 }) 进行非阻塞状态观测与消费，清空未决任务列表；\n   - 确保后台异步任务挂起数严格归零，彻底杜绝轮次切换时延迟注入 Finished background tasks 系统通知；\n2. 临时产物与会话状态归档：\n   - 清理 .cursor/tmp/ 临时文件，仅保留必要证据\n   - 完成 git 阶段性提交，确保无文件滞留暂存区\n   - 更新会话文档状态为 paused 并记录恢复点\n3. 最终汇报：\n   - 总结执行进度、已完成/未完成事项与后续恢复建议\n   - 通过普通 chat 提交最终状态报告，结束本轮执行"
	}

	if appSettings.Security.MaxFailedAttempts <= 0 {
		appSettings.Security.MaxFailedAttempts = 5
	}
	if appSettings.Security.LockoutMinutes <= 0 {
		appSettings.Security.LockoutMinutes = 15
	}
	if len(appSettings.PhaseTemplate) == 0 {
		appSettings.PhaseTemplate = model.DefaultPhaseTemplate()
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

