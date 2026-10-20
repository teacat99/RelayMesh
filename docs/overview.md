# RelayMesh 功能地图

这份手册随 RelayMesh 一起发布，和你当前连接的这台 Hub 是同一个版本。想知道某个功能「是什么、怎么用、在哪个页面」，先读这一篇，再按下面的「手册主题索引」跳到细分主题。手册里查不到的事，就如实告诉用户「手册里没有」，不要凭印象编。

## RelayMesh 是什么

RelayMesh 是一个单二进制的人机协同中枢：同一个服务同时提供三样东西。

- **MCP 端点**（`/mcp`）：给 AI 客户端（Cursor、Claude、ChatGPT 连接器等）用，工具清单由这把凭据的权限位决定。
- **Web 控制台**：给人用，看会话、回复、管任务与文档、管凭据、连远程主机。
- **实时流**（SSE）：Web 页面不用刷新就能看到智能体的新进展。

控制台分两个模式，侧栏左上角的 `RelayMesh | ExecMesh` 切换器可以来回切（切换器只在远程终端可用时出现）：

- **RelayMesh**：会话与交付——反馈对话、任务清单、项目文档库、确认项、追加指令收件箱。
- **ExecMesh**：终端与模型执行——在你授权的远程主机上开真实终端，或者让模型在这些主机上执行命令、读写文件。

## 几个核心概念

- **工作流（`workflow_id`）**：一个项目里多轮会话的聚合键。智能体每次调用治理类工具都要带上它，Web 侧栏按它折叠会话；上下文被压缩之后，用 `list_sessions`（`group_by: "workflow"`）可以找回它。
- **项目**：把若干工作流归在一起，带有知识库、模块、授权与审计，详见 `projects-and-access` 主题。
- **凭据（令牌）**：智能体连 `/mcp` 用的身份。它的**权限位**决定 `tools/list` 里能看到哪些工具，详见 `credentials-and-permissions` 主题。
- **阶段**：讨论、方案、开发三段；讨论与方案阶段只读，不改源码。
- **编号引用**：任务写 `#12`，确认项写 `Q12`，文档写 `[[路径]]`，会话写 `sess-xxxxxxxx`；写在汇报里，Web 会自动变成可点击的链接。

## 我该用哪个工具

| 想做的事 | 工具 | 需要的权限位 | 详见手册主题 |
|---|---|---|---|
| 向用户汇报、提问、交付总结 | `interactive_feedback`（内容写进 `summary`） | `feedback` | `async-mode`、`faq` |
| 收到「等待回执」后继续等用户回复 | `continue_feedback_session` | `feedback` | `async-mode` |
| 取反馈里用户附的图片 | `get_session_image` | `feedback` | `faq` |
| 不阻塞地提问、计划确认、文档决策 | `async_ask` | `feedback` 或 `sessions` | `confirm-items` |
| 异步对话里与用户对话 | `chat`（工作流开启异步对话时） | `feedback` 或 `sessions` | `async-mode` |
| 读用户在你干活时追加的话 | `workflow_context`（`inbox` / `inbox_ack`） | `feedback` 或 `sessions` | `inbox` |
| 保存检查点、会话文档、AI 笔记 | `workflow_context`（`checkpoint_save`、`session_doc_save` 等） | `feedback` 或 `sessions` | `faq` |
| 维护任务清单、模块规划 | `task_board`，以及 `workflow_context` 的 `task_*` | `feedback` 或 `sessions` | `shared-task-board` |
| 存取项目文档、授权文档访问 | `project_docs` | `feedback` 或 `sessions` | `project-docs`、`projects-and-access` |
| 查最近的会话、取某个工作流的历史 | `list_sessions`、`get_session_history` | `sessions` | `faq` |
| 读这份使用手册 | `usage_guide` | `feedback` 或 `sessions` | 本主题 |
| 取环境与系统信息 | `get_system_info` | `system_info` | `faq` |
| 读取用户自定义的规范（外置技能） | `manage_skills` | `skills` | `faq` |
| 指挥官侧管理自驾任务 | `configure_task` | `configure` | `faq` |
| 执行侧向指挥官上报进度 | `report_progress` | `execute` | `faq` |
| 在远程主机上执行命令 | `terminal_targets`、`terminal_exec`、`terminal_run` | `terminal` | `terminal-operations`、`execmesh-migration` |
| 读写远程主机上的文件 | `terminal_files` | `terminal_files` | `execmesh-migration` |
| 唤醒项目里的子会话（运行预设） | `preset_run` | `launcher`，且调用方是项目管理 Agent | `credentials-and-permissions`、`terminal-operations` |

几点说明：

- 工具能不能看见只由凭据的权限位决定。看不到某个工具，通常不是出错，而是这把凭据没有对应的权限位，让用户去「设置」里的凭据页调整。
- 带 `terminal_compat` 的凭据，终端工具改用与独立 ExecMesh 同名的 `targets_list`、`target_info`、`exec`、`exec_start`、`job_status`、`job_output`、`job_cancel`，原生的 `terminal_*` 对它不可见，对照表见 `execmesh-migration` 主题。
- 只带终端权限、不带任何治理权限的凭据（ExecMesh 档）看不到 `usage_guide` 和其他治理类工具。
- 每个工具自己的说明（`tools/list` 里的 `description`）永远是它动作与参数的权威清单，这里只给地图。

## Web 页面地图

| 地址 | 干什么 | 详见手册主题 |
|---|---|---|
| `/` | 会话首页：侧栏按工作流聚合会话，右侧是对话、输入框、待确认条、追加指令 | `async-mode`、`inbox`、`confirm-items` |
| `/work/<项目>` | 任务页：左侧模块规划树，右侧按规划顺序分组的任务列表与详情 | `shared-task-board` |
| `/group`、`/group/<群>` | 群聊：群列表与单个群（手册里暂时没有专门的主题） | — |
| `/projects`、`/projects/<项目>` | 项目列表与项目页，页签有 `kb`（知识库）、`modules`、`module`、`settings`、`migration`、`grants`、`audit` | `projects-and-access`、`project-docs`、`project-migration` |
| `/projects/<项目>/docs/<工作流>/<文档>` | 项目里的单篇文档：阅读、编辑、修订历史、评论、决策卡 | `project-docs` |
| `/docs/<工作流>` | 还没归入项目的工作流的文档库（属于项目的工作流会自动跳到项目页） | `project-docs` |
| `/archive` | 历史会话归档库：已归档的反馈会话 | — |
| `/settings` | 设置：登录与凭据、权限位、推送、OAuth、终端偏好、记录与保留等 | `credentials-and-permissions`、`notifications`、`oauth` |
| `/exec` | ExecMesh：终端会话，侧栏是会话清单 | `terminal-operations` |
| `/exec/t/<会话 ID>` | 一个具体的终端会话，地址可收藏、可在别的设备打开 | `terminal-operations` |
| `/exec/r/<线程>` | 模型执行线程：看模型在主机上跑的每条命令 | `terminal-operations`、`execmesh-migration` |
| `/exec/hosts` | 主机管理：接入主机、备注、模型访问授权、默认生命周期 | `terminal-operations` |
| `/exec/archive` | ExecMesh 的归档：已归档的终端会话（打开它不会取消归档） | `terminal-operations` |
| `/oauth/consent` | OAuth 授权同意页（连接器接入时弹出） | `oauth` |

## 手册主题索引

用 `usage_guide` 的 `list` 动作可以随时拿到这份清单（主题编号、标题、摘要、篇幅）。

| 主题编号 | 什么时候读它 |
|---|---|
| `overview` | 不知道从哪里开始、想找「哪个功能在哪个工具或页面」时（本篇） |
| `faq` | 遇到具体问题或报错：看不到工具、收不到回复、推送没响、凭据权限不对…… |
| `projects-and-access` | 项目、知识库、模块、授权（`off` / `audit` / `enforce`）、项目管理 Agent |
| `shared-task-board` | 任务清单怎么用：模块规划、状态与阶段、管理 Agent 与普通 Agent 的分工 |
| `credentials-and-permissions` | 凭据类型与每个权限位的含义、令牌三档、能力 URL、运行预设 |
| `confirm-items` | `async_ask`：不阻塞的提问、计划确认、文档决策卡、风险等级 |
| `async-mode` | 异步对话：`wait:false` 发进度、授权档位、回复窗口 |
| `inbox` | 追加指令收件箱：用户在你干活时补充的话如何送达、已读与确认 |
| `project-docs` | 项目文档库：保存、搜索、修订、导入导出、决策卡 |
| `notifications` | 手机或桌面推送（ntfy）的配置与行为 |
| `terminal-operations` | 远程终端与模型执行的运维手册（很长，用 `search` 找小节） |
| `execmesh-migration` | 从独立 ExecMesh 迁移、兼容命名、能力 URL、SSH 主机、远程文件 |
| `oauth` | ChatGPT、Claude 等连接器通过 OAuth 2.1 接入 |
| `project-migration` | 项目换目录或搬到另一台机器后，把旧工作流改绑过去 |
| `deploy-with-ai` | 部署、升级、备份、回滚，以及让 AI 助手帮你部署 |

## 怎么连上 RelayMesh

- **HTTP MCP**：端点 `http://<主机>:18775/mcp`，令牌放进请求头 `Authorization: Bearer <令牌>`。浏览器里要用麦克风录音，Web 控制台请走 `https://<主机>:18776/`（自带自签名证书，局域网可用），MCP 客户端仍然建议用 HTTP 端点，避免自签名证书被拒。
- **本机 stdio 直启**：`go run github.com/teacat99/RelayMesh/v2/cmd/relaymesh@latest mcp stdio`，适合 Cursor、Claude Desktop 在本机直接拉起，数据放在系统标准用户目录，不会往代码目录写文件；开多个窗口时共用同一个控制台（第一个进程当主进程，其余转发给它，主进程退出后自动接管）。
- **只能填网址的连接器**：用 ExecMesh 档凭据的「能力 URL」，见 `execmesh-migration` 主题。
- **ChatGPT、Claude 等连接器**：用 OAuth 2.1，见 `oauth` 主题。
- **部署、升级、备份与回滚**：见 `deploy-with-ai` 主题。

## 用 usage_guide 读这份手册

- `list`：列出全部主题，没有参数。
- `search`：用关键词找小节，例如 `{"action":"search","query":"能力 URL"}`；多个词之间是「并且」，命中会给出主题编号、小节标题和一小段上下文。
- `get`：读一个主题，例如 `{"action":"get","topic":"faq"}`；加 `section` 只读某一小节（写小节标题或它的前缀）；篇幅长的主题会分页，按回执里的 `next_offset` 继续读，每页大小用 `max_chars` 调。首页会带上全篇的小节目录。

回答用户之前先读手册，手册里没有的就说没有；手册里讲的网页操作步骤是给用户看的，你自己用的是对应的 MCP 工具。
