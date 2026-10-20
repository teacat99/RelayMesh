# 凭据与权限位

智能体连接 RelayMesh 用的是 MCP 凭据（令牌）。每把凭据带一组**权限位**，权限位决定它在 `tools/list` 里能看到哪些工具，也决定 `tools/call` 能调用哪些工具——两处由同一组权限位决定。看不到某个工具，通常不是出错，而是这把凭据没有对应的权限位。

## 凭据在哪里管理

- Web 控制台「设置 → MCP 与安全 → MCP 凭据管理」：新建、编辑、停用、删除、重新生成令牌，编辑器里有「RelayMesh 模式 / ExecMesh 模式 / 自定义」的预设，并实时预览「这把凭据能看到哪些工具」。
- MCP 令牌只能访问 `/mcp`，调用 REST 或 SSE 管理接口一律返回 `403 mcp_token_forbidden`，没有兼容开关。
- 列表里的令牌是打码显示的；查看已有令牌的明文需要再次验证 Web 密码。凭据库最多保存 50 把凭据。

## 凭据的几种来源

| 来源 | 得到的权限 | 说明 |
|---|---|---|
| 凭据库里的令牌（在 Web 创建） | 创建时勾选的权限位 | 可以限定终端主机、限额、有效期、来源 IP；终端类权限和运行预设要求强随机令牌 |
| 环境变量令牌 `RELAYMESH_MCP_TOKEN` | 治理类全部权限位：`feedback`、`sessions`、`system_info`、`skills`、`configure`、`execute` | 全局令牌，放在请求头 `Authorization: Bearer <令牌>`，也可放在查询串 `?token=`；永远不带终端和运行预设 |
| 环境变量令牌 `RELAYMESH_CONFIGURE_TOKEN` | 只有 `configure`（任务指挥） | 给自驾（autopilot）模式里的指挥官一侧用，日志与审计里显示为 `env:configure`；看不到沟通、会话、终端等其他工具 |
| 环境变量令牌 `RELAYMESH_EXECUTION_TOKEN` | 只有 `execute`（自驾上报） | 给自驾模式里的执行一侧用，显示为 `env:execute`；只能 `report_progress`，「执行」指执行自驾任务，与远程执行命令无关 |
| 没有设置任何令牌（开放访问） | 同上，治理类全部权限位 | 只适合本机；公网务必设置令牌 |
| 本机 stdio（`mcp stdio`） | 同上，治理类全部权限位 | 作为 MCP 客户端的子进程运行；开多个窗口时，后起的进程经「只认本机回环 + 数据目录里一把随机钥匙」的转发入口把消息交给主进程，用的仍是这条本机 stdio 凭据（常驻服务上没有这个入口） |
| 能力 URL | 只有终端执行，ExecMesh 档 | 令牌写在专用网址 `/mcp/c/rmc-…` 里，给只能填网址的连接器用，见 `execmesh-migration` 主题第 8 节 |
| OAuth 访问令牌（`rmo_` 前缀） | 映射成隐藏凭据，复用 ExecMesh 的全部闸门 | 见 `oauth` 主题 |

开放访问、环境变量令牌与本地 stdio 永远不能执行远程命令：远程执行必须由人在凭据页显式授予。

## 权限位一览

Web 里每个权限位有自己的名字，下面的 JSON 键就是凭据上存的字段。

| 权限位 | Web 里的名字 | 解锁的工具 | 备注 |
|---|---|---|---|
| `feedback` | 沟通反馈 | `interactive_feedback`、`continue_feedback_session`、`get_session_image` | 与用户的主通道 |
| `sessions` | 会话查询 | `list_sessions`、`get_session_history` | 查最近会话与历史 |
| `feedback` 或 `sessions` | （任一开启） | `workflow_context`、`task_board`、`project_docs`、`async_ask`、`chat`、`usage_guide` | 治理类的共用工具：检查点与收件箱、任务板、项目文档、确认项、聊天、使用手册 |
| `system_info` | 系统信息 | `get_system_info` | |
| `skills` | 规范管理 | `manage_skills` | 外置技能（用户自定义规范） |
| `configure` | 任务指挥 | `configure_task` | 指挥官侧管理自驾任务 |
| `execute` | 自驾上报 | `report_progress` | 自驾（autopilot）模式下上报进度，与「执行命令」无关 |
| `terminal` | 终端执行 | `terminal_targets`、`terminal_exec`、`terminal_run` | 默认关闭，等同远程执行权限 |
| `terminal_files` | 终端文件 | `terminal_targets`、`terminal_files` | 默认关闭；只能访问你指定的目录 |
| `terminal_compat` | ExecMesh 兼容命名 | `targets_list`、`target_info`、`exec`、`exec_start`、`job_status`、`job_output`、`job_cancel` | 只改名字与返回形状，不增加能力；需要同时有 `terminal`，开启后原生 `terminal_*` 名字对这把凭据不可见 |
| `launcher` | 运行预设 | `preset_run` | 默认关闭；见下面「运行预设」一节 |

## 令牌三档与预设

- **RelayMesh 档**：只有治理类权限，没有任何终端权限。编辑器的「RelayMesh 模式」预设把六个治理位全部打开、终端关闭。
- **ExecMesh 档**：只有终端类权限（`terminal`、`terminal_files`），模型只看到终端工具和精简的执行说明，不要求 `workflow_id`，也看不到 `usage_guide` 等治理类工具；用户在 Web 里看每条命令的实时记录。编辑器的「ExecMesh 模式」预设只打开 `terminal`。
- **混合档**：两类权限都有；调用终端工具时缺少 `workflow_id` 的记录会进「未归属」线程。
- 一个权限位都不选的凭据没有任何工具，不能保存；带了 `launcher` 的凭据不再算任何一个预设，会显示为「自定义」。
- 改完权限保存之后，客户端重新拉取 `tools/list` 才会看到变化（有的 MCP 客户端需要重新连接或刷新工具列表）。

## 终端类权限的附加条件

只打开 `terminal` 或 `terminal_files` 远远不够，要同时满足多道闸：

- 服务端设置了 `RELAYMESH_TERMINAL_EXEC=on`；文件工具还要 `RELAYMESH_TERMINAL_FILES=on`。
- 令牌是凭据库里的强随机令牌，并且授权了具体的主机（或明确选择「全部主机」）。
- 主机在主机页打开了「允许模型访问」，没有暂停，本机没有用 `--deny-model-exec` 一票否决。
- 可选的限制：执行模式上限（跟随主机 / 自动执行 / 需人工批准 / 只读）、最大并发、等待与超时上限、输出上限、允许的工作目录、来源 IP（每行一条 CIDR 或单个 IP）、到期时间。
- `terminal_files` 必须填写允许访问的目录（最多 20 个，不接受文件系统根、相对路径和 `..`），或者明确打开高风险开关「允许整个文件系统」。

授予或放宽终端能力（打开权限位、增删主机授权、改限额、重新启用、重新生成令牌）需要先输入 Web 密码解锁；停用、删除、清空授权这类收紧操作只需要登录。详见 `terminal-operations` 与 `execmesh-migration` 主题。

## 什么算强令牌

终端类权限和运行预设在服务端强制要求「强令牌」，防止弱令牌被授予远程能力。判定规则是：

- 长度不少于 24 个字符；
- 至少有 8 种不同的字符；
- 不含 `password`、`default`、`secret`、`your-token` 这类默认值或占位符的写法。

凭据库里的令牌由服务端生成（`rm-` 加 64 位十六进制随机数，能力 URL 凭据用 `rmc-` 前缀），天然满足这些要求，不需要自己想。环境变量令牌 `RELAYMESH_MCP_TOKEN` 是你自己写的值，永远只有治理类权限，没有终端权限和运行预设。

## 运行预设（`launcher` 与 `preset_run`）

`preset_run` 让项目的管理 Agent 运行本项目里已有的「非交互续聊」预设，去唤醒任务板上的子会话。两道闸缺一不可：

1. 凭据带有 `launcher` 权限位。没有这一位，`tools/list` 里根本没有这个工具，所以它同时是一个随时能关掉的总闸。
2. 调用方是所在项目的管理 Agent（用户在项目页勾选，见 `projects-and-access` 主题）。

关于 `launcher`：

- 默认关闭，也不在「RelayMesh 模式」预设里；授予时需要重新验证密码，并且凭据必须是强令牌。
- 必须同时开启治理类工具（例如 `feedback`、`sessions`），因为管理 Agent 要靠它们指派任务、向用户汇报。
- 能力 URL 凭据不能带这一位。

`preset_run` 的动作是 `list`（列出你能运行的预设）、`status`（一个预设的当前状态）、`run`（启动）。它只能「按下运行」：主机、目录、对话、模型、权限姿态和提示词都由用户在预设里定好，调用方既改不了也看不到。它走的是网页上点「运行」的同一条路径（总闸、暂停、主机的模型访问、需批准、令牌限额、防重复一个不少），另有频率护栏：每个管理 Agent 同时最多 3 条、每小时 10 次，同一个预设两次运行至少间隔 5 分钟，全部管理 Agent 合计每天 30 次。每次运行与每次拒绝都进审计（`launcher.run`）和聊天系统行，不含提示词与命令。

诚实的边界：调用方的身份来自它自己声明的 `project_directory` 与 `host_name`，不是密码学证明，真正的边界是令牌和 `launcher` 权限位；完整说明见 `terminal-operations` 主题第 8.12 节。

## 看不到某个工具时怎么排查

1. 先看 `tools/list` 里到底有哪些工具，对照上面的权限位表，确定缺的是哪一位。
2. 凭据是不是 ExecMesh 档：它只有终端工具，没有 `workflow_context`、`task_board`、`usage_guide` 等治理类工具。
3. 凭据是不是开了 `terminal_compat`：这时原生的 `terminal_*` 名字不可见，只有 `targets_list`、`exec` 这套兼容名。
4. 终端类工具还要看服务端的环境变量闸门和主机授权，见上面「终端类权限的附加条件」。
5. `preset_run` 还要调用方是项目管理 Agent；工具看得见但调用被拒绝，多半是没被勾选为管理 Agent。
6. 让用户在「设置 → MCP 与安全」里调整对应的权限位，保存后重新连接或刷新工具列表。
