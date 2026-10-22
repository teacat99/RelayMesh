# 常见问题与排错

按问题找答案。每个问题下面的答案都尽量短，更完整的说明在对应的手册主题里；手册里没有的事，请如实告诉用户「手册里没有」。

## 连接与访问

### MCP 客户端报 `401`

没带令牌或令牌不对。令牌放在请求头 `Authorization: Bearer <令牌>`，或者在地址后加 `?token=<令牌>`。令牌从 Web 的「设置 → MCP 与安全」里取（查看明文需要再次验证 Web 密码）。凭据的种类与权限见 `credentials-and-permissions` 主题。

### `-32001 Request timed out`，或经反向代理后网页不再实时更新

反向代理缓冲了 SSE，或者超时设得太短。Nginx 需要 `proxy_buffering off` 与 `proxy_read_timeout 3600s`，Caddy 需要 `flush_interval -1`。部署相关见 `deploy-with-ai` 主题。

### 本机能打开，手机或别的电脑打不开

先看 `.env` 里有没有 `RELAYMESH_PUBLISH_BIND=127.0.0.1`（那是只给本机用的写法：删掉它或改成 `0.0.0.0`，再 `docker compose up -d`）；再检查服务器防火墙或云安全组有没有放行 `18775` 与 `18776`。

### `https://IP:18776` 提示证书不受信任

内置的是自签名证书，点「继续访问」即可，浏览器里的麦克风录音需要 HTTPS。想要受信任的证书就走反向代理。MCP 客户端请用 `http://<主机>:18775/mcp`，避免自签名证书被拒。

### 重启后会话和草稿都没了

数据没有持久化。Docker 部署要确认挂载了 `./data`；直接运行二进制时默认写**当前目录**的 `data/`，换目录启动就是新数据，请设置绝对路径的 `RELAYMESH_DB_PATH`。

### 忘了 Web 密码

在网页里改过密码又忘了：执行 `docker compose exec relaymesh /app/relaymesh reset-auth`（二进制：`relaymesh reset-auth`），会清除网页上改过的账号密码，恢复成 `.env` 里的 `RELAYMESH_WEB_PASSWORD`。想同时换一个新密码：先改 `.env` 并 `docker compose up -d`，再执行这条命令——顺序反了会恢复成旧密码。

## 工具与权限

### 为什么看不到某个工具（`task_board`、`terminal_exec`、`preset_run`、`usage_guide`……）

工具能不能看见只由凭据的权限位决定。对照 `credentials-and-permissions` 主题里的权限位表，找出缺的那一位，让用户在「设置 → MCP 与安全」里调整，保存后重新连接或刷新工具列表。常见情况：ExecMesh 档凭据没有治理类工具；开了 `terminal_compat` 之后原生 `terminal_*` 名字不可见；没有 `launcher` 位就没有 `preset_run`。

### 调用提示缺少 `workflow_id`

治理类工具的调用都要带 `workflow_id`，它来自回执头，和你给 `interactive_feedback` 传的是同一个。混合档凭据调用终端工具时缺少它，记录会进「未归属」线程。

### 上下文被压缩后，怎么找回 `workflow_id` 和做到哪了

1. `list_sessions` 加 `group_by: "workflow"`：按工作流汇总最近的会话，找回 `workflow_id`。
2. `get_session_history`：读这个工作流的多轮对话与用户的反馈，也包含你工作期间用户追加的话。
3. `task_board`（默认 `overview`）：看任务清单做到哪了、什么在进行中。
4. `workflow_context`：读检查点、会话文档；`inbox` 动作读用户追加的指令（见 `inbox` 主题）。
5. 项目里的设计依据在 `project_docs`（见 `project-docs` 主题）。

### 怎么看用户在反馈里贴的图片

用 `get_session_image`：它把附图直接通过 MCP 通道取回，适合不能访问外部网址的隔离环境，输出可以选原生图片内容或其他形式（参数以工具说明为准）。需要 `feedback` 权限位。

## 对话与等待

### `interactive_feedback` 返回「等待回执」怎么办

这表示用户还没回复，服务端先给了一个保活回执。严格按回执里写明的等待方式等：用客户端提供的计时或等待工具等回执给的时长，不要用 shell 的 `sleep`，也不要在等待期间输出别的内容；等待结束后调用 `continue_feedback_session` 接着取。只有收到「反馈超时」才进入收尾，期间用户来了消息就继续干活。

### 什么才算用户的话

只有回执里的「用户反馈」才是用户的权威输入。回执本身、自己生成的文字，都不能当成用户的反馈。

### 异步对话里，用户的答复去哪了

进了收件箱。回执末尾出现以 `inbox:` 开头的提示行，就说明有追加还没读：调用 `workflow_context` 的 `inbox` 动作读全文，处理后用 `inbox_ack` 确认。详见 `inbox` 与 `async-mode` 主题。

### 为什么异步对话开不起来，或者自己变回同步了

没收到过 hook 的工作流不能开启异步对话；hook 超过 60 分钟没有动静，会自动降级为同步，随时可以一键关闭。详见 `async-mode` 主题。

### `chat` 工具返回 `not_chat_mode`

这个工作流没有开启异步对话。`chat` 只在异步对话开启的工作流里有效，其他工作流继续用 `interactive_feedback`，也不要自己去把异步对话打开。

## 确认项与计划

### 什么事带默认值提问，不可逆的事怎么问

可逆、有默认值、你能先做不依赖答案的部分的事，用 `async_ask` 提问后继续干活。不可逆或高风险（L2）的事——部署、推送、删数据、改凭据、不可逆迁移——在**异步对话**里同样发一张 `async_ask` 确认卡（`risk:"L2"`），先做不依赖它的事、不用停下来等，但**批准前绝不执行、没有答复就是不批准**：L2 卡不能带默认值（服务端拒绝），也永远不会进「全部采用推荐」的批量、不会自动过期。**同步**（默认）模式下仍用阻塞式的 `interactive_feedback` 问。详见 `confirm-items` 与 `async-mode` 主题。

### 用户答复提交后发现答错了

答复一经提交就是终局，不能改。发一条追加说明更正，由智能体按新的说明处理。

## 任务与文档

### 任务被服务端拒绝：`done` 需要结论 / 模块不存在

- `done` 必须同时写 `conclusion`，新建任务也不能直接是 `done`。
- 模块名是封闭名单，任务只能标注已有的模块路径；先用 `plan` 声明新的模块或功能（报错会列出现有路径）。
- 任务在「未归类」里：用 `batch` 的 `{task_ids, module}` 把它们挪进对应模块。

详见 `shared-task-board` 主题。

### 保存文档提示版本冲突

`project_docs` 更新已有文档需要带 `expected_rev`（你读到的版本号），别人在你之前保存过就会冲突：重新读一次最新版本，合并之后再保存。详见 `project-docs` 主题。

### 写入被拒绝（权限不够 / 没有身份）

见 `projects-and-access` 主题的「被拒绝了怎么办」：补上 `project_directory` 与 `host_name`，或者请用户、负责人智能体授权。

## 各个工具的一句话说明

- `get_system_info`：返回 Hub 的环境与版本信息，需要 `system_info` 权限位。
- `manage_skills`：外置技能目录，保存用户自定义的规范、约定和可复用的说明；`list` 列出名称与摘要，`get` 读取全文，其余动作以工具说明为准。需要 `skills` 权限位。
- `configure_task`：指挥官侧，管理自驾任务，动作有 `create`、`update`、`set_wait_policy`、`send_feedback`、`get`、`list`、`list_updates`、`read_reports`、`ack_reports`、`close`。需要 `configure` 权限位。
- `report_progress`：执行侧，在被 `configure_task` 派了任务的自驾模式下使用。开工或恢复时第一个调用必须是 `sync`；`report` 提交结构化汇报，`check_feedback` 非阻塞地检查有没有新的反馈。需要 `execute` 权限位。
- `workflow_context`：任务恢复胶囊与 AI 笔记。`checkpoint_save` 保存检查点（目标、决策、带证据的完成项），`session_doc_save` 与 `session_doc_get` 保存和读取会话文档，`set_phase` 切换阶段，`inbox` 与 `inbox_ack` 读取并确认用户追加的指令，`task_create` 等动作管理单个任务。
- `list_sessions`、`get_session_history`：查会话与历史，限定在当前 `project_directory` 与主机之内。需要 `sessions` 权限位。

## 推送与通知

### 手机收不到推送

推送默认关闭。在设置里打开并配置 ntfy 的服务器与主题，设置页可以直接试发一条。默认只发会话标题与项目名，不含汇报正文；你在网页上处理掉的请求会按延迟设置不再打扰，并有去重与限流。详见 `notifications` 主题。

## 终端与远程主机

### 命令一直停在「待批准」

主机或令牌的执行模式是「需批准」：命令先停在待批准，由用户在 Web 上批准后才真正执行。向用户说明正在等批准，不要重复提交同一条命令。

### 模型执行得到 `host_unsupported`

这台主机上的 Agent 版本太旧，不支持模型执行；升级 Agent 即可。新旧版本的服务端与 Agent 可以混用，旧 Agent 只会得到这个明确的错误。升级方式见 `terminal-operations` 主题。

### 第一次接入远程主机

见 `terminal-operations` 与 `execmesh-migration` 主题；终端相关的令牌权限与闸门见 `credentials-and-permissions` 主题。

## 部署、升级与备份

- 升级：`docker compose pull && docker compose up -d`，数据库迁移自动完成。
- 备份：先 `docker compose down` 保证数据库文件一致，再打包 `data` 目录，然后重新启动。
- 回滚：在 `.env` 里写 `RELAYMESH_TAG=<旧版本号>`（不带 `v`，例如 `2.0.0`；想回到升级前的 1.x 就写那个版本号），再 `docker compose up -d`；想回到最新版，删掉这一行重新拉取。
- 服务端数据库迁移只增不改：旧版本可以直接打开新版本升级过的数据库，新版本才有的功能产生的数据，旧版本可能不显示，升回去就能看到。

完整步骤、验证清单与更多排错见 `deploy-with-ai` 主题。

## 手册里没有答案的问题

如实告诉用户「手册里没有这个」，并说明你已经查过哪些主题，不要凭印象编造。可以建议用户去对应的 Web 页面（见 `overview` 主题的页面地图）直接看，或者把问题记进任务板留待处理。
