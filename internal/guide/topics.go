package guide

// registry 是手册的主题登记表（按阅读顺序）。docs/ 下每新增一个 .md 都必须在这里登记（guide_test 守着）：
// 这样「哪些文档算手册、它回答什么问题」永远是一次有意识的决定，而不是目录里碰巧多了个文件。
//
// Summary 写成读者会怎么问的样子：智能体靠 list 的摘要决定读哪一篇，靠 search 的关键词找小节。
var registry = []Topic{
	{
		ID: "overview", File: "overview.md", Title: "功能地图",
		Summary:  "RelayMesh 是什么、按权限有哪些 MCP 工具、每件事去 Web 的哪里做、该读哪一篇手册。不知道从哪里开始时先读它。",
		Keywords: []string{"总览", "入门", "介绍", "功能地图", "能做什么", "工具列表", "tools", "从哪里开始", "入口", "页面", "导航"},
	},
	{
		ID: "faq", File: "faq.md", Title: "常见问题与排错",
		Summary:  "智能体接入后最常遇到的问题：看不到某个工具、被拒绝（403 / 不允许）、等不到回复、收件箱 / 任务板 / 文档怎么用、该去问谁。",
		Keywords: []string{"问题", "报错", "排错", "troubleshooting", "not allowed", "denied", "拒绝", "看不到", "怎么办", "不会用"},
	},
	{
		ID: "projects-and-access", File: "projects-and-access.md", Title: "项目、知识库与授权",
		Summary:  "项目是什么（把目录归成一组）、项目知识库与模块、访问控制三档（关闭 / 仅审计 / 强制执行）、授权范围与角色、谁是项目管理 Agent。",
		Keywords: []string{"project", "项目", "acl", "grant", "授权", "访问控制", "审计", "enforce", "audit", "role", "角色", "管理 Agent", "知识库", "模块", "权限不足"},
	},
	{
		ID: "shared-task-board", File: "shared-task-board.md", Title: "共享任务板",
		Summary:  "任务与规划树（模块 / 功能）、任务状态与阶段、同一项目的工作流共用一张板、指派 / 接取 / 交付 / 验收协议、管理 Agent 与普通 Agent 各能做什么。",
		Keywords: []string{"task_board", "任务", "任务板", "看板", "task", "指派", "分配", "派任务", "认领", "assign", "assignee", "claim", "接取", "交付", "deliver", "验收", "board", "module", "规划", "workflow_context"},
	},
	{
		ID: "credentials-and-permissions", File: "credentials-and-permissions.md", Title: "凭据与权限位",
		Summary:  "MCP 令牌（凭据）有哪些权限位、每一位开放哪些工具、令牌档位、能力 URL、弱令牌规则、运行预设权限位；工具看不到或不允许时先查这里。",
		Keywords: []string{"token", "令牌", "credential", "凭据", "permission", "permissions", "权限", "权限位", "tools/list", "capability", "能力 URL", "launcher", "preset_run", "运行预设", "弱令牌", "weak", "usage_guide"},
	},
	{
		ID: "confirm-items", File: "confirm-items.md", Title: "确认项（async_ask）",
		Summary:  "不阻塞的提问、计划确认与文档决策卡：怎么提问、风险分级与硬规则、用户在哪里回答、答复怎么回到你手里。",
		Keywords: []string{"async_ask", "确认项", "确认", "提问", "question", "plan", "决策卡", "default_option", "Q12", "ask"},
	},
	{
		ID: "async-mode", File: "async-mode.md", Title: "异步对话模式",
		Summary:  "发完进度就继续干活：wait:false 与回复窗口、什么时候该等什么时候不该等、授权档位、界面里能看到什么。",
		Keywords: []string{"async", "异步", "回复窗口", "wait", "window", "interactive_feedback", "非阻塞", "进度通知", "notice"},
	},
	{
		ID: "inbox", File: "inbox.md", Title: "追加指令收件箱",
		Summary:  "用户在你工作期间追加的消息怎么送达：inbox 读取、inbox_ack 确认、三层提醒、引用某条回复、Cursor hook 的安装与排错。",
		Keywords: []string{"inbox", "inbox_ack", "追加", "收件箱", "hook", "提醒", "撤回", "reminder"},
	},
	{
		ID: "project-docs", File: "project-docs.md", Title: "项目文档库",
		Summary:  "project_docs 工具：保存设计 / 决策 / 运行手册，按路径与章节读写，决策卡、历史版本与链接，老文档并入项目知识库。",
		Keywords: []string{"project_docs", "文档", "design", "决策", "section", "expected_rev", "知识库", "export", "import", "artifact"},
	},
	{
		ID: "notifications", File: "notifications.md", Title: "手机 / 桌面推送（ntfy）",
		Summary:  "给用户发推送：快速开始、自建 ntfy 与访问控制、推送内容与时机、防打扰与可靠性、常见问题。",
		Keywords: []string{"ntfy", "推送", "通知", "push", "手机", "notification"},
	},
	{
		ID: "terminal-operations", File: "terminal-operations.md", Title: "远程终端与模型执行（运维手册）",
		Summary:  "ExecMesh：升级与回滚、数据放在哪里、SSH 网关、终端启动器（含项目管理 Agent 运行预设 8.12）、长任务、输出追赶等。很长，请用 search 找小节或用 section 读。",
		Keywords: []string{"terminal", "ExecMesh", "exec", "终端", "ssh", "启动器", "launcher", "preset", "预设", "headless", "无人值守", "启用 Agent", "agent", "主机", "升级", "回滚", "data", "数据目录"},
	},
	{
		ID: "execmesh-migration", File: "execmesh-migration.md", Title: "从独立 ExecMesh 迁移",
		Summary:  "三种令牌档位、原生与兼容工具名、人工批准流程、能力 URL、SSH 主机、远程文件（terminal_files）、命令在主机上如何执行。",
		Keywords: []string{"execmesh", "terminal_exec", "terminal_files", "兼容命名", "能力 URL", "capability", "ssh", "批准", "approval", "目标", "target"},
	},
	{
		ID: "oauth", File: "oauth.md", Title: "OAuth 2.1 授权服务器",
		Summary:  "为只能填网址的连接器提供 OAuth：环境变量、发现端点、范围、用户批准、客户端注册。",
		Keywords: []string{"oauth", "授权服务器", "connector", "连接器", "discovery", "客户端注册"},
	},
	{
		ID: "project-migration", File: "project-migration.md", Title: "项目迁移与工作流改绑",
		Summary:  "目录或主机变了、工作流「丢了」时怎么迁移与改绑、安全与回滚、注意事项。",
		Keywords: []string{"迁移", "改绑", "workflow", "目录变了", "rebind", "项目目录", "丢了"},
	},
	{
		ID: "deploy-with-ai", File: "deploy-with-ai.md", Title: "部署、升级与备份",
		Summary:  "让 AI 助手帮用户部署 RelayMesh：Docker Compose / 单二进制 + systemd / stdio / 前台四条路径，验证脚本、升级、备份、回滚与排错。",
		Keywords: []string{"deploy", "部署", "docker", "compose", "升级", "upgrade", "备份", "backup", "回滚", "安装", "install", "systemd"},
	},
}
