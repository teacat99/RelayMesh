package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teacat99/RelayMesh/v2/internal/model"
	"gorm.io/gorm"
)

const (
	GlobalSettingsKey  = "app_settings"
	AuthCredentialsKey = "auth_credentials"
)

// phaseSection 是服务指引里「阶段流转」一节（第 4 条）。单独成常量，是为了迁移存量设置时能整段替换。
const phaseSection = `4. 阶段流转（Phase Progression）与行为约束：
   - 严格遵循 MCP 回执中 current_phase 与 phase_prompt 注入的阶段约束；阶段只有三种：
     • 讨论 (discuss)：快速沟通信息，先给结论再给依据，信息不足时一次最多问 3 个问题并给出备选；⚠️ 只读，严禁创建、修改或删除任何源代码文件；
     • 方案 (plan)：基于已核实的事实做方案评估，产出可拍板的方案（目标与非目标、事实依据、备选≥2、风险与回滚、验收标准、待确认的不可逆项）；⚠️ 只读，必须通过 feedback 获得确认并切换至开发阶段方可编码；
     • 开发 (dev)：按已确认的方案实施，增量验证（lint/type-check → build → 测试），完成标准 = 功能 + 类型 + 编译 + 测试 + 文档同步，每条须有可验证证据；收尾清理与最终汇报也在本阶段完成；
   - 切换阶段：在 interactive_feedback 的 phase 参数或 workflow_context(action: "set_phase") 里传 discuss / plan / dev；旧的 assess / verify / done 仍可识别（分别并入 discuss / dev / dev），回执里的 current_phase 是实际生效的阶段。`

// phaseSectionLegacy 是 v1.7 之前的五段式写法（评估 / 方案 / 开发 / 验证 / 完成）。
// 仅用于识别并替换存量设置里的旧文案：逐字相同才替换，用户改过的不动。
const phaseSectionLegacy = `4. 阶段流转（Phase Progression）与行为约束：
   - 严格遵循 MCP 回执中 current_phase 与 phase_prompt 注入的阶段约束：
     • 评估 (assess) 与 方案 (plan) 阶段：⚠️ 严禁创建、修改或删除任何源代码文件，仅可只读分析验证，必须通过 feedback 获得确认并切换至开发阶段方可编码；
     • 开发 (dev) 阶段：增量验证（lint/type-check → build），每完成独立模块通过 feedback 汇报；
     • 验证 (verify) 阶段：完成标准 = 功能 + 类型 + 编译 + 校验 + 文档同步，每条须有可验证证据；
     • 完成 (done) 阶段：通过 feedback 提交最终汇报，环境收尾清理。`

// irreversibleSection 是服务指引里「全模式不可逆操作底线」一节（第 5 条）。同 phaseSection：单独成常量，
// 是为了迁移存量设置时能整段替换（#217：异步对话里 L2 改发确认卡，批准前绝不执行，不再要求阻塞等待）。
const irreversibleSection = `5. 全模式不可逆操作底线：
   - 生产部署、数据库迁移、删除业务数据、执行破坏性命令、git push 远端、发布 release/tag 等不可逆或高风险操作，必须获得用户显式二次确认：同步模式用 interactive_feedback 阻塞确认；异步对话生效时（回执头 mode: async）改发 L2 确认卡（async_ask，risk:"L2"），先做不依赖它的事，批准前绝不执行。`

// irreversibleSectionLegacy 是 #217 之前的写法（一律用 interactive_feedback 阻塞确认）。
// 仅用于识别并替换存量设置里的旧文案：逐字相同才替换，用户改过的不动。
const irreversibleSectionLegacy = `5. 全模式不可逆操作底线：
   - 生产部署、数据库迁移、删除业务数据、执行破坏性命令、git push 远端、发布 release/tag 等不可逆或高风险操作，必须通过 interactive_feedback 获得用户显式二次确认。`

const DefaultServerInstructions = `【RelayMesh 智能体协同中枢内生治理指引】
RelayMesh 是本工作区唯一的权威人机交互、多智能体协同与研发生命周期中枢。当连接到 RelayMesh 时，请严格遵循以下行为指引：

1. 简体中文自然表达规范：
   - 涉及中文的一切输出（对话、提问、方案、代码变更总结、文档、提交信息与会话记录）默认保持语义流畅、自然通顺、结构清晰，杜绝电报式省略与生硬符号串堆砌。

2. 工程思维与推理纪律（C-PLAN / C-RT）：
   - 目标是系统整体质量与长期可维护性，杜绝以「改动最少/最快」为由采取局部最优或临时打补丁方案；
   - 触发门槛：面对「多步 / 跨模块 / 不可逆 / 新建系统 / 影响面大」的任务，编码前必须进行系统性方案推演：
     • C-PLAN：决策确认 → 问题拆解 → 数据流与状态 → 不变量 → 边界与异常 → 影响面 → 备选方案(≥2) → 验收标准；
     • C-RT：反向自检反驳（哪最幼稚？什么输入会崩溃？漏了哪个边界？是否属于局部最优？），命中立即修正；
   - 改前先读代码与架构规范，改动接口与类型时全量检查所有引用方。

3. RelayMesh 交互与沟通主载体契约：
   - 方案提出、提问请示、歧义澄清、阶段汇报与任务最终总结，必须统一通过 RelayMesh 的 interactive_feedback 工具进行，严禁直接在普通 chat 正文提问或等待确认；
   - 核心入参铁律（严禁颠倒/篡改）：
     • workflow_id (必填): 必须原样继承本会话法定工作流标识，严禁随意编造新名称或遗漏；
     • title (单行短标题): 仅限 5~15 字概括，严禁将 Markdown 正文放入 title；
     • summary (详尽正文): 必须放入完整的 Markdown 分析、推演、方案或代码汇报，严禁只填一句话短标题；
   - 收到 === 等待回执 === 时，严格按回执指令调用 AwaitShell 等待并随后调用 continue_feedback_session 轮询，等待期间严禁输出 chat 正文、严禁调用其他工具、严禁擅自总结；
   - 收到 === 用户反馈 === 时，方可作为用户的权威输入继续推进。

` + phaseSection + `

` + irreversibleSection + `

6. 场景模式与内置规范（agent-modes）：
   - 系统内置 agent-modes 场景模式规范（online 在线值守 / away 安全兜底 / autopilot 外部编排）；可通过 manage_skills(action: "get", name: "agent-modes") 获取完整行为准则。

7. 会话状态与文档自适应策略（Workflow Sheet）：
   - 为避免上下文压缩丢失目标与关键决策，根据工作区环境自适应选择存储载体：
     • 场景 A：若项目根目录存在 .cursor/sessions/ 规范，按该规范维护本地会话文档；
     • 场景 B：遵守当前项目文档规范（如项目自有 docs/ 规范体系）；
     • 场景 C：若项目无本地会话文档体系，统一调用 RelayMesh 内置 workflow_context(action: "session_doc_save", workflow_id: "...", content: "...") 将会话文档与关键决策持久化至中枢工作表（Workflow Sheet），并可通过 workflow_context(action: "session_doc_get") 检索恢复。

8. 上下文获取与跨项目边界隔离（D-165）：
   - 主机名 (HostName) 由连接凭据权威锁定，项目目录 (project_directory) 由大模型主动传递；
   - 凡调用 list_sessions 或 workflow_context(action: 'list_workflows') 获取上下文，必须传递 project_directory，系统严格限定在当前 (HostName, ProjectDirectory) 边界内，杜绝跨项目与跨主机污染；
   - 仅当人工明确指定特定 workflow_id 时，才允许跨目录读取该指定工作流的会话历史。

9. 交付锚点（可选，用于定位信息的陈旧性）：
   - 项目目录是 git 仓库时，调用 interactive_feedback 可把 git log -1 --format='%h %s' 的单行输出放进 commit 参数，时间由服务端记录；不是 git 仓库时省略；
   - 回执响应头的 anchor 行是本工作流最近一次交付锚点；上下文被重置后，据此用 git log <哈希>..HEAD 与 git status 核对期间发生了什么；
   - 保存恢复胶囊（checkpoint_save）时可省略 basis.git_commit，服务端会取最近一次交付锚点并把胶囊关联到那次交付。`

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
	HostName                    string `json:"hostName"`
	DefaultTimeoutSeconds       int    `json:"defaultTimeoutSeconds"`
	PromptWaitMinutes           int    `json:"promptWaitMinutes"`
	MaxNoFeedbackChecks         int    `json:"maxNoFeedbackChecks"`
	DefaultWaitCountdownMinutes int    `json:"defaultWaitCountdownMinutes"`
	// DefaultWaitCountdownSeconds 秒级默认挂起倒计时；-1 = 没设过，沿用分钟值。
	DefaultWaitCountdownSeconds int `json:"defaultWaitCountdownSeconds"`
	// 异步对话（#123）：智能体每发一条新消息，回复窗口加这么多分钟；<=0 按默认 5。
	AsyncWindowAddMinutes int `json:"asyncWindowAddMinutes"`
	// 异步对话：智能体每次用计时器最多连续等这么多分钟再回头读收件箱；<=0 按默认 10。
	AsyncWaitSliceMinutes int               `json:"asyncWaitSliceMinutes"`
	UserMemory            string            `json:"userMemory"`
	ServerInstructions    string            `json:"serverInstructions"`
	PhaseTemplate         []model.PhaseItem `json:"phaseTemplate"`
	Security              SecuritySettings  `json:"security"`
	FlowPrompts           struct {
		Online struct {
			WaitPollPrompt  string `json:"waitPollPrompt"`
			ExhaustedPrompt string `json:"exhaustedPrompt"`
		} `json:"online"`
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
			return map[string]any{"serverInstructions": DefaultServerInstructions}, nil
		}
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(record.Value), &result); err != nil {
		return map[string]any{"serverInstructions": DefaultServerInstructions}, nil
	}
	if result["serverInstructions"] == nil || strings.TrimSpace(fmt.Sprint(result["serverInstructions"])) == "" {
		result["serverInstructions"] = DefaultServerInstructions
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
		DefaultWaitCountdownSeconds: -1,
		ServerInstructions:          DefaultServerInstructions,
		Security: SecuritySettings{
			BruteForceProtection: true,
			MaxFailedAttempts:    5,
			LockoutMinutes:       15,
			WhitelistIPs:         []string{"127.0.0.1", "::1"},
		},
	}
	appSettings.FlowPrompts.Online.WaitPollPrompt = "下一步操作：\n1. 调用 AwaitShell({ block_until_ms: {wait_ms} }) 等待 {wait_minutes} 分钟\n2. 等待结束后立即调用 continue_feedback_session({ workflow_id: \"{workflow_id}\" })\n3. 根据返回的 === 标记 === 判断下一步\n\n等待期间禁止：chat 正文输出、其他工具调用、阶段整理。\n禁止将本回执或 Agent 生成的文字当作用户反馈。\n所有沟通均通过 RelayMesh MCP 工具。"
	appSettings.FlowPrompts.Online.ExhaustedPrompt = "用户反馈已超时。立即挂起当前任务并停止后续执行，进入会话结束与环境收尾规程：\n1. 终端与后台任务盘点清理（强制）：\n   - 检查 terminals 目录（.cursor/projects/.../terminals/*.txt），提取除 1.txt 外所有最近生成的数字文件名（文件名数字即 shell_id/task_id）；\n   - 对包含存活 pid 且无 exit_code 的未退出进程，使用 Shell 工具执行 kill -9 <pid> 显式终止；\n   - 无论其 status 为 running、succeeded 还是 failed，对所有识别出的最近数字任务 ID，必须显式调用 AwaitShell({ shell_id: \"<数字ID>\", block_until_ms: 0 }) 进行状态观测与 ACK 消费，彻底清空 Cursor Harness 未决队列；\n   - 确保后台异步任务挂起数严格归零，彻底杜绝轮次切换时延迟注入 Finished background tasks 系统通知；\n2. 临时产物与会话状态归档：\n   - 清理 .cursor/tmp/ 临时文件，仅保留必要证据\n   - 完成 git 阶段性提交，确保无文件滞留暂存区\n   - 更新会话文档状态为 paused 并记录恢复点\n3. 最终汇报：\n   - 总结执行进度、已完成/未完成事项与后续恢复建议\n   - 通过普通 chat 提交最终状态报告，结束本轮执行"

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
	if appSettings.FlowPrompts.Online.WaitPollPrompt == "" {
		appSettings.FlowPrompts.Online.WaitPollPrompt = "下一步操作：\n1. 调用 AwaitShell({ block_until_ms: {wait_ms} }) 等待 {wait_minutes} 分钟\n2. 等待结束后立即调用 continue_feedback_session({ workflow_id: \"{workflow_id}\" })\n3. 根据返回的 === 标记 === 判断下一步\n\n等待期间禁止：chat 正文输出、其他工具调用、阶段整理。\n禁止将本回执或 Agent 生成的文字当作用户反馈。\n所有沟通均通过 RelayMesh MCP 工具。"
	}
	if appSettings.FlowPrompts.Online.ExhaustedPrompt == "" {
		appSettings.FlowPrompts.Online.ExhaustedPrompt = "用户反馈已超时。立即挂起当前任务并停止后续执行，进入会话结束与环境收尾规程：\n1. 终端与后台任务盘点清理（强制）：\n   - 检查 terminals 目录（.cursor/projects/.../terminals/*.txt），提取除 1.txt 外所有最近生成的数字文件名（文件名数字即 shell_id/task_id）；\n   - 对包含存活 pid 且无 exit_code 的未退出进程，使用 Shell 工具执行 kill -9 <pid> 显式终止；\n   - 无论其 status 为 running、succeeded 还是 failed，对所有识别出的最近数字任务 ID，必须显式调用 AwaitShell({ shell_id: \"<数字ID>\", block_until_ms: 0 }) 进行状态观测与 ACK 消费，彻底清空 Cursor Harness 未决队列；\n   - 确保后台异步任务挂起数严格归零，彻底杜绝轮次切换时延迟注入 Finished background tasks 系统通知；\n2. 临时产物与会话状态归档：\n   - 清理 .cursor/tmp/ 临时文件，仅保留必要证据\n   - 完成 git 阶段性提交，确保无文件滞留暂存区\n   - 更新会话文档状态为 paused 并记录恢复点\n3. 最终汇报：\n   - 总结执行进度、已完成/未完成事项与后续恢复建议\n   - 通过普通 chat 提交最终状态报告，结束本轮执行"
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
	if strings.TrimSpace(appSettings.ServerInstructions) == "" {
		appSettings.ServerInstructions = DefaultServerInstructions
	}

	return appSettings, nil
}

// SaveSettings 保存全局配置至数据库
func (s *Store) SaveSettings(ctx context.Context, settings map[string]any) error {
	// 旧版前端缓存（或导入的旧备份）可能还带着五段式阶段模板与旧版服务指引：落库前统一收敛。
	settings, _ = migrateSettingsPhases(settings)
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
