# RelayMesh ⚡

<p align="center">
  <strong>高可靠 AI Agent 与人类协同交互中继网关</strong><br>
  <em>High-Reliability Human-in-the-Loop & Agentic Relay Hub for Modern AI Development</em>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=flat-square&logo=go" alt="Go Version" />
  <img src="https://img.shields.io/badge/Vue-3.5%2B-4FC08D?style=flat-square&logo=vue.js" alt="Vue Version" />
  <img src="https://img.shields.io/badge/TailwindCSS-v4.0-38B2AC?style=flat-square&logo=tailwind-css" alt="Tailwind CSS" />
  <img src="https://img.shields.io/badge/Protocol-Model%20Context%20Protocol%20(MCP)-blueviolet?style=flat-square" alt="MCP Protocol" />
  <img src="https://img.shields.io/badge/Architecture-Single%20Binary%20%2B%20embed.FS-0052CC?style=flat-square" alt="Single Binary" />
  <img src="https://img.shields.io/badge/Memory-~40MB%20idle-success?style=flat-square" alt="Memory" />
  <img src="https://img.shields.io/badge/License-Apache_2.0-blue?style=flat-square" alt="License" />
</p>

---

## 📖 项目简介 (Introduction)

**RelayMesh** 是一款专为 AI 辅助编程与自动化 Agent 打造的高性能、高可靠**人机协同交互与状态中继网关**。

在长周期开发与自动化任务中，AI Agent（如 Cursor、Claude Desktop、VS Code、Codex）常面临**上下文压缩丢失决策**、**等待人工确认时频繁超时报错 (`-32001`)**、**Web 界面简陋难用**、以及**公网部署缺乏零信任安全**等痛点。

RelayMesh 基于 **Go 1.26** 与 **Vue 3.5** 纯原生重构，支持 **原生 stdio 本地直启（随 AI 客户端自启退出、零配置开箱即用）** 与 **Streamable HTTP MCP** 双 Transport 架构，提供沉浸式桌面级 Web 控制台、毫秒级 SSE 实时事件流与单二进制内嵌托管，空载常驻内存约 **40 MB**（日常使用约 70 MB），兼顾本地极简免配置运行与公网高安全生产部署。

**2.0 起，它不止是「人与 AI 的交互中继」**：同一个单二进制里还带着**异步协作**（发了就走的异步对话、不阻塞的确认项、追加指令收件箱、聊天与群聊）、**项目、文档与任务**（项目知识库、任务清单与共享任务板、编号引用、内置使用手册），以及 **ExecMesh 远程终端与模型执行**（在你授权的主机上开真实终端，或让模型执行命令、读写文件）。下面「核心特性」开头的「功能总览」一张表就能看全。

---

> 🤖 **想让 AI 助手帮你部署？** 把 [`docs/deploy-with-ai.md`](docs/deploy-with-ai.md) 交给它（里面有一段可直接复制的提示词）：它会先做只读的环境检查、给你一份计划，**你确认后**才动手，部署完逐项验证，最后告诉你访问地址、口令放在哪里、怎么升级与回滚。

---

## 🚀 5 分钟上手 (Quick Start)

先选一条路，几分钟就能跑起来：

| 你的情况 | 推荐方式 | 大约用时 |
|---|---|---|
| 想先在自己电脑上试一试，配合 Cursor / Claude Desktop 使用 | **A. 本机直启**（不装服务、不用 Docker） | 1 分钟 |
| 有一台服务器 / NAS / 常开的电脑，想让手机、同事也能访问 | **B. Docker Compose 常驻**（推荐） | 3 分钟 |
| 不想自己敲命令 | **C. 让 AI 助手代劳**：见上面的提示，或 [AI 部署指南](docs/deploy-with-ai.md) | 复制一段提示词 |

### A. 本机直启（Cursor / Claude Desktop）

在 Cursor 的 `~/.cursor/mcp.json`（Claude Desktop 是 `claude_desktop_config.json`）里加入下面任意一段，重启客户端即可——**不需要先启动任何服务**，RelayMesh 随客户端启动、随客户端退出。

**已有 Go 1.26+：**

```json
{
  "mcpServers": {
    "relaymesh": {
      "command": "go",
      "args": ["run", "github.com/teacat99/RelayMesh/v2/cmd/relaymesh@latest", "mcp", "stdio"]
    }
  }
}
```

> 第一次运行要下载依赖并编译，可能需要几十秒到几分钟，之后有缓存。

**没有 Go：先装单二进制**（自动识别系统与架构，下载后校验 SHA256）：

```bash
curl -fsSL https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.sh | sh
relaymesh version      # 期望：RelayMesh v<版本号>
```

然后把配置里的 `command` 写成 `relaymesh` 的**绝对路径**（`which relaymesh` 可查；普通用户会装到 `~/.local/bin`，macOS 上它默认不在 `PATH` 里），`args` 写 `["mcp", "stdio"]`。

**Windows**：`install.sh` 只适用于 Linux / macOS。到 [Releases](https://github.com/teacat99/RelayMesh/releases) 下载 `relaymesh-windows-amd64.exe`，放在一个固定目录（例如 `C:\Tools\relaymesh.exe`），配置里的 `command` 写它的完整路径（JSON 里反斜杠要写两遍：`C:\\Tools\\relaymesh.exe`），`args` 同样是 `["mcp", "stdio"]`；或者装 Docker Desktop，直接走下面的 B。

客户端启动 RelayMesh 之后，Web 控制台会同时在 <http://127.0.0.1:18775/> 提供服务（stdio 模式不会自动弹出浏览器）。会话数据放在系统的用户数据目录（Linux `~/.local/share/relaymesh`、macOS `~/Library/Application Support/relaymesh`、Windows `%LOCALAPPDATA%\relaymesh`），不会往你的项目目录写任何文件。单窗口使用时，网页侧栏默认收起（只有一个会话，不占地方），出现第二个工作流时自动展开；你手动展开 / 收起过就一直按你的选择。

**开了多个编辑器窗口也没问题**：第一个起来的进程是「主进程」，持有数据库、端口和网页控制台；之后的窗口发现端口已被占用、确认对面是 RelayMesh 的 stdio 进程后，不再报错退出，而是把自己的 MCP 消息转给主进程处理，各自按自己的 `workflow_id` 开会话，全部汇总在同一个控制台里。转发入口只认本机回环地址，并要求带上数据目录里一把只有本人可读的随机钥匙（每次主进程启动都会重新生成）；因此需要所有窗口由同一个系统用户启动、共用同一份数据目录。主进程所在的窗口关闭后，剩下的窗口会在几秒内自动接管成新的主进程，控制台与已有会话保持不变；这一刻正在等答复的调用会收到一条「主进程断开，请重新发起」的可重试错误。如果占着端口的是常驻服务（Docker / `relaymesh serve`）或别的程序，stdio 进程仍然直接报错退出，并说明该怎么办（改用常驻服务的 HTTP MCP 地址，或用 `RELAYMESH_PORT` 给 stdio 换一个端口）。

### B. 服务器常驻（Docker Compose）

> ⚠️ **先看这条：默认监听所有网卡。** 局域网里（云服务器上就是整个互联网）的人都能连到 `18775` / `18776`。下面的命令会先生成随机的 Web 密码与 MCP 令牌——**请不要跳过这一步**：不设口令就等于把一个免登录的中枢开在了网上，任何人都能读你的会话、给你的智能体下指令。只想在本机用，就把 `.env` 里的 `RELAYMESH_PUBLISH_BIND` 改成 `127.0.0.1`。

```bash
mkdir -p ~/relaymesh && cd ~/relaymesh
curl -fsSLO https://raw.githubusercontent.com/teacat99/RelayMesh/main/docker-compose.yml

# 生成 .env：随机的 Web 密码与 MCP 令牌，权限 600（不要提交到 Git）
cat > .env <<EOF
RELAYMESH_WEB_USERNAME=admin
RELAYMESH_WEB_PASSWORD=$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)
RELAYMESH_MCP_TOKEN=rmk_$(openssl rand -hex 24)
# 监听地址：0.0.0.0（默认）= 所有网卡，局域网 / 手机也能访问；只想本机用、或要放在反向代理后面（公网），改成 127.0.0.1
RELAYMESH_PUBLISH_BIND=0.0.0.0
EOF
chmod 600 .env

docker compose up -d
```

十几秒后 `docker compose ps` 会显示 `healthy`。打开 <http://localhost:18775/>，用 `admin` 和 `.env` 里的 `RELAYMESH_WEB_PASSWORD` 登录（`grep PASSWORD .env` 可以查看）。

> 🔐 **口令与令牌是你的第一道门。** 没有 `openssl`？见 [AI 部署指南](docs/deploy-with-ai.md) 里的备用命令。不要使用文档里出现过的示例口令，也不要把 `.env` 提交到 Git；MCP 令牌与 Web 密码相互独立，前者给 AI 客户端用，后者给你登录网页用。
>
> 想确认没有被外人访问到：在**另一台机器**上执行 `curl -s http://<服务器IP>:18775/api/v1/auth/status`，应当看到 `"auth_required":true`；如果你本来只想本机用却返回了 JSON，说明端口对外开着——写上 `RELAYMESH_PUBLISH_BIND=127.0.0.1` 再 `docker compose up -d`。

### 部署之后

1. **验证**：`curl -s http://127.0.0.1:18775/api/v1/auth/status` 返回 JSON（设置了密码与令牌时能看到 `"auth_required":true`、`"mcp_auth_required":true`）；更完整的逐项检查脚本见 [AI 部署指南第 8 节](docs/deploy-with-ai.md)。
2. **连接 AI 客户端**：见下文「AI 客户端配置」，地址是 `http://localhost:18775/mcp`，令牌放进请求头 `Authorization: Bearer <令牌>`。
3. **让手机 / 同事访问**：默认就可以——同一网络里打开 `http://<服务器IP>:18775/`（手机要语音输入请用 `https://<服务器IP>:18776/`）；云服务器记得在安全组 / 防火墙放行这两个端口。**只想本机用，或要用域名和 HTTPS**：把 `.env` 里的 `RELAYMESH_PUBLISH_BIND` 改成 `127.0.0.1` 再 `docker compose up -d`，反向代理见「反向代理配置」（Caddy 三行即可）。
4. **用 Web 终端连接你的其他主机**（可选）：见「远程终端 Agent」。
5. **升级 / 备份 / 回滚**：见「升级、备份、回滚与卸载」。

### 📚 文档索引

> 💡 **不会用？直接问接入的会话。** 这些文档同时内置在 Hub 里：已接入的智能体用只读的 MCP 工具 **`usage_guide`**（`list` / `search` / `get`）按需查阅，永远和当前版本一致——问它「怎么派任务」「令牌看不到某个工具怎么办」，它会先读手册再回答，不必凭记忆。

| 想了解 | 文档 |
|---|---|
| 功能地图：RelayMesh 是什么、有哪些工具、每件事去网页的哪里做 | [docs/overview.md](docs/overview.md) |
| 常见问题与排错（看不到工具、被拒绝、等不到回复……） | [docs/faq.md](docs/faq.md) |
| 项目、知识库与授权（访问控制三档、角色、项目管理 Agent） | [docs/projects-and-access.md](docs/projects-and-access.md) |
| 共享任务板：规划树、任务状态、指派 / 接取 / 交付 / 验收 | [docs/shared-task-board.md](docs/shared-task-board.md) |
| 凭据与权限位：每一位开放哪些工具、令牌档位、弱令牌、运行预设 | [docs/credentials-and-permissions.md](docs/credentials-and-permissions.md) |
| 让 AI 助手帮你部署（含可复制的提示词、验证脚本、排错表） | [docs/deploy-with-ai.md](docs/deploy-with-ai.md) |
| 远程终端的升级、回滚、数据位置、SSH 网关 | [docs/terminal-operations.md](docs/terminal-operations.md) |
| 从独立 ExecMesh 迁移到 RelayMesh 的 ExecMesh 档 | [docs/execmesh-migration.md](docs/execmesh-migration.md) |
| OAuth 2.1：ChatGPT / Claude 等连接器接入 | [docs/oauth.md](docs/oauth.md) |
| 追加指令收件箱：智能体干活时你补充的话 | [docs/inbox.md](docs/inbox.md) |
| 异步对话模式：发完进度继续干活 | [docs/async-mode.md](docs/async-mode.md) |
| 确认项：不阻塞的提问、计划确认与文档决策 | [docs/confirm-items.md](docs/confirm-items.md) |
| 项目文档库与任务清单 | [docs/project-docs.md](docs/project-docs.md) |
| 手机 / 桌面推送（ntfy） | [docs/notifications.md](docs/notifications.md) |
| 项目换目录 / 搬到另一台机器后的工作流改绑 | [docs/project-migration.md](docs/project-migration.md) |

---

## ✨ 核心特性 (Key Features)

### 🧭 功能总览（2.0）

| 能力 | 做什么 | 详细说明 |
|---|---|---|
| **人机交互中继** | `interactive_feedback` 阻塞式汇报与提问，服务端长轮询根治客户端超时（`-32001`）；多草稿箱、图片灯箱、语音输入；你提前写好的话 0ms 直取 | 下文特性 1–5 |
| **追加指令收件箱** | 智能体干活时你随时补充的话，状态看得见（已排队 → 已读取 → 已确认），hook 在它两次工具调用之间把它叫回来 | [docs/inbox.md](docs/inbox.md) |
| **异步协作** | 异步对话（发了就走）与授权档位、确认项（`async_ask`：不阻塞的提问 / 计划确认 / 文档决策）、聊天与群聊、手机 / 桌面推送（ntfy） | [异步对话](docs/async-mode.md) · [确认项](docs/confirm-items.md) · [推送](docs/notifications.md) |
| **项目、文档与任务** | 项目与授权（访问控制三档）、项目文档库与 HTML Artifact、任务清单与共享任务板（指派 / 接取 / 交付 / 验收）、编号引用（`#12` `Q12` `[[文档]]`）、内置使用手册 `usage_guide` | [项目与授权](docs/projects-and-access.md) · [文档库](docs/project-docs.md) · [任务板](docs/shared-task-board.md) |
| **Agent 状态与会话守护** | hook 上报「Agent 在干什么」，网页显示身份与状态；停摆检测与一键唤醒、非交互续聊预设（可选自动续跑）、待接入会话一键「启用 Agent」 | [docs/terminal-operations.md](docs/terminal-operations.md) 第 8 节 |
| **远程 Web 终端（ExecMesh）** | 在已装 Agent 的 Linux / macOS / Windows 主机上打开真实 PTY 终端；会话常驻、历史回放与搜索；终端内容只存在主机本机，服务端不存 | [docs/terminal-operations.md](docs/terminal-operations.md) |
| **模型执行与远程文件** | `terminal_targets` / `terminal_exec` / `terminal_run` 让模型在授权主机上执行命令（默认关闭，多道闸、可审计）；`terminal_files` 在你指定的目录内查看与取回文件 | [docs/execmesh-migration.md](docs/execmesh-migration.md) |
| **SSH 主机与网关** | 没装 Agent 的 SSH 主机，经服务端直连或经内网跳板（网关）接入，人和模型都能用；登录凭据只存在网关自己的存储里 | [docs/terminal-operations.md](docs/terminal-operations.md) 第 4 节 |
| **接入方式** | 本机 stdio 直启（多窗口自动汇合）、HTTP MCP（Bearer 令牌）、只能填网址的连接器用能力 URL、ChatGPT / Claude 等连接器用 OAuth 2.1 | [OAuth](docs/oauth.md) · [能力 URL](docs/execmesh-migration.md) |
| **安全与部署** | Web 强鉴权与反爆破、MCP 令牌按权限位分档、Agent 升级包验签；四种部署方式（`go run` 即下即跑 / 预编译单二进制 / Docker Compose / 源码），升级迁移只增不改 | [凭据与权限](docs/credentials-and-permissions.md) · [部署指南](docs/deploy-with-ai.md) |

> 💡 每个能力对应哪个 MCP 工具、哪个网页页面，见 [docs/overview.md](docs/overview.md)（功能地图）。

### ⚡ 0. 原生本地 stdio 直启与双 Transport 架构 (New in v1.2.0)
- **单一配置开箱即用**：无需克隆仓库、无需配置 Token、无需手动启动后台守护，Cursor / Claude 配置文件一条 `go run ...@latest mcp stdio` 即可即时运行；
- **双 Transport 共享内核**：stdio 与 HTTP 双 Transport 共享同一个状态存储、同一个 `mcp.Server` 实例，stdio 运行期间自动伴随本地 Web 控制台；
- **工作区绝对零污染**：stdio 模式下数据库与自签名证书自动存放于 OS 标准用户目录（Linux: `~/.local/share/relaymesh`；macOS: `Application Support`；Windows: `%LOCALAPPDATA%`），绝不向代码项目目录写入任何临时文件；
- **多窗口自动汇合**：第一个进程是主进程（数据库 / 端口 / 控制台），之后的窗口自动接入它、各自开会话；主进程窗口关闭后其余窗口自动接管；
- **全生命周期平滑管理**：客户端关闭 `stdin` 时平滑退出，端口被别的程序占用时自动 Fail Fast 并说明怎么办，stdout 严格独占 MCP JSON-RPC 消息流。

### 🤖 1. 标准 Streamable HTTP MCP 协议中枢
- **双角色无缝支持**：
  - **人机交互角色 (Human Feedback)**：`interactive_feedback`、`continue_feedback_session`、`get_session_image`（新增：协议级原生图片看图与提取工具）、`get_session_history`、`list_sessions`；
  - **任务编排与执行角色 (Task Orchestrator)**：`configure_task`、`report_progress`（支持增量同步 `sync`、结构化报告 `report` 与非阻塞检查 `check_feedback`）。
- **根治客户端超时机制**：服务端精细化 Long-Polling（40s 挂起上限）配合单调递增因果游标，彻底告别 `-32001 Request timed out`。
- **秒回直取与提前暂存**：用户可在 AI 运行期间随时提前追加意见或暂存留言，AI 再次接入时 **0ms 毫秒级直取**。
- **追加指令收件箱（带状态，三层发现，可手动开关）**：智能体埋头干活时你追加的每条指令单独入库，网页上实时显示「已排队 ✓ → Agent 已读取 ✓✓ → Agent 已确认 ✓✓✓」与被发现的路径，读后还能撤回并通知它；智能体不必等到下一次交互——工具回执末尾的 `inbox:` 提示行、Cursor 的 `postToolUse` / `stop` hook（一条命令安装，令牌不落盘，出任何问题都静默放行）、MCP 使用说明里的提示词条款会在工作途中把它叫回来；总开关与三层各自的开关都在设置里，`RELAYMESH_INBOX=off` 可在部署层强制关闭。详见 [docs/inbox.md](docs/inbox.md)。

### 💻 2. 沉浸式现代 Web 交互控制台
- **极简工程美学**：基于 Tailwind CSS v4 与 Radix Vue 打造，纯单页沉浸式工作区，支持 **跟随系统 💻 / 浅色 ☀️ / 深色 🌙** 3 态循环切换。
- **全通侧栏与工作流聚合**：统一以 `workflow_id` 进行多轮会话折叠聚合，支持全文检索与类型过滤，支持 PC 端侧边栏平滑折叠展开（本机 stdio 直驱且只有一个工作流时默认收起，出现第二个工作流自动展开；你手动展开 / 收起过就按你的选择）。
- **预览视图（2.0 默认的新会话视图）**：统一消息卡、右侧轨、整段历史搜索、未读与「以下为新消息」分隔、会话级状态、群聊卡片与成员头像；加载更快——离屏卡片跳过渲染、先取索引再按需取正文、渐进渲染、次要请求让路。不习惯随时点「回到经典视图」，每台设备各自记住；
- **新建会话「高级选项」**：侧栏底部「新建会话」里默认收起，可预先指定工作流编号、首条指令（进收件箱，智能体接入后第一次调用就读到）、初始阶段、项目目录与主机；不填时与以前完全一样；
- **悬浮操作与底部避让**：毛玻璃悬浮反馈提交栏，支持高度自由拖拽与记忆、多行快捷预设标签与状态联动。
- **手机 / 桌面推送（ntfy，默认关闭）**：智能体发起新的反馈请求时，由服务端直接推送到订阅了主题的 ntfy 应用，网页没开、手机锁屏也能收到。默认只发会话标题与项目名，不含汇报正文；支持访问令牌 / 用户名密码、延迟推送（你在网页上处理掉了就不再打扰）、去重与限流；令牌只写不读，设置页可直接试发。另可选（同样默认关闭）：模型经 ExecMesh 在主机上跑的长命令结束时推送一条结果——成功 / 失败（带退出码）/ 超时 / 被中断，只推送跑够设定时长的命令，你自己取消的不打扰，绝不带命令输出。详见 [docs/notifications.md](docs/notifications.md)。

### 📝 3. 工作流级多草稿箱系统 (Multi-Drafts)
- **多卡槽独立构思**：单个工作流支持最多 5 个独立草稿卡槽，支持快捷键 `Ctrl + ←` / `Ctrl + →` 左右平移切换。
- **前后端双重防丢持久化**：输入时本地 `localStorage` 0 延迟响应，400ms 防抖同步写入后端 SQLite 数据库；切换会话或重新部署后草稿与附件全量平滑恢复。
- **撤回草稿绝对保护**：输入区已有内容时点击撤回，系统自动开辟新卡槽暂存编辑内容，确保 100% 零覆盖、零丢失。

### 🔍 4. 工业级 Markdown 排版与严格字符网格
- **CJK 严格 2ch 网格对齐**：通过自定义字符级扫描与样式约束，强制中文字符与英文字符严格呈现 2:1 等宽网格，彻底解决 ASCII 流程图（`┌─┐`、`│`、`└─┘`）在不同操作系统下的错位问题。
- **极简手势图片灯箱**：全站图片打通（Markdown 正文、历史记录、输入框附件），纯净无边框无按钮，支持鼠标滚轮（0.4x~5x）无级平滑缩放、双指捏合与点击遮罩退出。

### 🎙️ 5. 双端口原生 HTTPS 与 ASR 语音输入
- **零依赖自签名 TLS 引擎**：Go 单二进制内置证书生成器，启动时自动生成覆盖本地网卡的 10 年有效期 RSA 证书。
- **协议安全分流**：
  - **HTTPS 端口 (`18776`)**：Web UI 访问，直接解锁现代浏览器麦克风录音权限 (`getUserMedia`)，内置小米 MIMO 流式 ASR 语音转文字；
  - **HTTP 端口 (`18775`)**：MCP 服务端点，规避 AI 客户端连接自签名证书时的 TLS 拒绝报错。

### 🛡️ 6. 公网生产零信任安全闭环
- **Web 访问强鉴权**：支持管理员账号密码认证与 7 天有效期 JWT 验签；
- **内存级原子反爆破限流器 (RateLimiter & Lockout)**：基于客户端 IP 的失败计数与阶梯式自动封禁，防暴力穷举；
- **MCP 全局 Token 保护**：支持 `RELAYMESH_MCP_TOKEN`，支持 Header（`Bearer`）与 URL Query（`?token=`）双通道认证；
- **CLI 安全重置**：支持在服务器执行 `./relaymesh reset-auth`，一键重置自定义凭据为环境变量初始设定。
- **项目迁移不丢会话**：项目换目录或换机器后，用 `./relaymesh rebind-workflow` 把旧工作流改绑到新的「项目目录 + 主机」（默认只预演、写入前自动快照、可原路改回），见 [docs/project-migration.md](docs/project-migration.md)。

### 🖥️ 7. 远程 Web 终端与通用 Agent (New in v2.0.0)
- **ExecMesh 模式**：侧栏左上角的 `RelayMesh | ExecMesh` 切换器（仅在远程终端可用时出现）在「会话与交付」和「终端」之间切换，两个模式各自记住上次停留的位置。ExecMesh 的侧栏就是一份普通的会话清单：终端会话按最近活动日期排列，可搜索、按「人工 / 运行中 / 已结束」筛选、置顶、重命名、复制会话 ID；点开后右侧才显示终端。地址形如 `/exec/t/<会话 ID>`，可直接收藏或分享给自己的另一台设备（旧地址 `/terminal` 会自动跳转）。主机管理（接入、备注、停用 / 删除、默认生命周期、录制开关）在 `/exec/hosts`；
- **像本机一样连接任意主机**：在 ExecMesh 里打开 Linux / macOS / Windows 主机的真实 PTY（Windows 使用 ConPTY；xterm.js 渲染，支持 vim / tmux 等全屏程序、中文与宽字符、粘贴与复制、窗口自适应，手机端提供控制键栏；文字大小用 Ctrl + 滚轮缩放、Ctrl + 0 恢复默认，切换与新建会话都在左侧栏）；
- **固定终端宽度（可选，默认关闭）**：ExecMesh 的终端默认跟随浏览器窗口改变列数，换一台设备看同一个会话，历史排版的宽度就跟着变。在「设置 → 终端偏好 → 固定终端宽度」打开并选一个列数（40–400，常用 80 / 100 / 120 / 132 / 160 / 200，第一次打开填入暂定的 120）后，所有设备上的这个终端都保持同一个列数：窗口放得下就保持你设置的字号，放不下（比如手机）就自动把字号缩小到刚好放下（最小 4 px，每次 0.5 px），终端右下角会显示「固定 N 列 · X px」，连最小字号都放不下时才出现横向滚动条。这个设置保存在服务端、所有设备共用，别的设备改了之后已打开的终端在约 15 秒内跟随；它只决定浏览器上报的终端尺寸，不涉及主机上的任何数据，登录即可修改。行数（终端高度）仍然跟随最近一次上报的设备。推荐的列数之后会结合 Cursor / Claude Code / Codex 与常用命令行的实际排版再评估，120 只是暂定值；
- **终端限高（按行数，可选，默认关闭）**：长时间运行、一直刷输出的会话，其记录在主机上会越来越大。在「设置 → 记录与保留 → 终端限高」设一个行数（5,000–10,000,000）后，每个会话在主机上的记录**至少保留最近这么多行**输出（按换行符统计），更早的内容以分段为单位整段丢弃；它与字节配额（默认每会话 64 MiB）互相独立，哪个先到就先按哪个丢，服务端设置与主机本机的 `--record-session-lines` 取更严的一个（服务端只能让它更严格）。丢掉的内容无法恢复，所以修改需要再次验证密码、写入审计（`record_policy`），从「不限制」收紧或调小还要二次确认；只作用于运行中和之后新建的会话，已结束会话的记录仍按保留期清理。这是按分段丢弃，刚设置时仍在写入的那一段（最大 1 MiB）要等新增输出满设定行数之后才会被丢掉，稳态下保留的行数在设定值到设定值多出约一成之间。需要新版守护进程（旧守护进程仍只按字节配额，设置页会告诉你有几台在线主机尚未生效；升级 Agent 后用 `relaymesh-agent holder restart` 换成新版）。推荐的行数之后会按记录文件大小再评估，现在的 100,000 行只是暂定值；
- **会话常驻，且挺过 Agent 的升级 / 重启 / 崩溃**：Shell 进程运行在被控主机上，不随浏览器关闭而结束。`relaymesh-agent install` 默认同时安装一个独立的「会话守护进程」来持有这些 PTY，Agent 被升级、重启甚至 `kill -9`，正在运行的命令都不受影响，新 Agent 接上后是同一个会话、同一个 Shell；刷新或重连后自动回放断线期间的输出，同一会话可在多个浏览器间接力。有守护进程的主机在界面上标注「重启不中断」，没有守护进程时（例如临时运行）则如实标注「重启会中断」；
- **生命周期由你决定**：默认「空闲不回收」——只要不是主机重启、守护进程被终止这类不可抗力，会话不会因为没人用而被收走（它与上面的「重启不中断」是两回事：前者决定没人用时要不要被收走，后者说的是 Agent 升级 / 重启 / 崩溃时会话还在不在）。也可以在主机页设置「默认生命周期」，或在单个会话上设置「空闲 N 后回收」（1 小时 ~ 30 天；「空闲」指没有任何浏览器连接、也没有输入，程序自己的输出不算）。回收由主机上的守护进程执行，不依赖服务端在线。因主机重启等不可抗力而结束的会话，会写明原因，并提供「在原目录重建会话」；
- **多会话与并排**：同时保持连接的会话数默认 4 个（主机页可调 2~8，其余先断开、再点开时接着回放，会话在主机上不受影响）；后台会话有新输出时，标签和侧栏上会出现提示点。桌面端可以把两个会话左右并排显示（顶栏「并排」按钮，或会话菜单里的「在右侧并排打开」），分隔条可拖动、比例会记住，两个窗格各自独立输入、各自同步终端尺寸；手机端不提供并排；
- **免 Token 的机器码接入**：Agent 首次运行生成本机 Ed25519 身份与机器码 `RM-XXXX-XXXX-XXXX-XXXX`；管理员在 Web「接入主机」打开发现窗口，核对机器码后启用并备注即可。服务端只保存公钥，不存在可冒充主机的共享秘密；窗口之外的陌生机器码一律拒绝；
- **默认安全闸**：仅当「已设置 Web 登录密码 且 JWT 密钥足够强」时才可用；每次使用前需再次输入密码解锁（15 分钟无操作自动上锁，可随时手动上锁）；启用、停用、开会话、强制结束等操作全部写入审计日志；设置 `RELAYMESH_TERMINAL=off` 可整体关闭；
- **独立 Agent**：单一静态二进制 `relaymesh-agent`，也可通过主程序的 `relaymesh agent` 子命令使用；支持前台运行、临时身份运行，以及注册为常驻服务（Linux systemd / macOS launchd / Windows 计划任务）。安装方式见下文「远程终端 Agent」。
- **终端记录与历史回放**：终端上显示什么就记录什么（PTY 输出流，含 Shell 对键入的回显；不另存键入字节，隐藏输入的密码因为不回显，不会出现在记录里）。记录**只写在被控主机本机**（分段压缩，超过配额或保留期自动滚动清理，磁盘空间不足时暂停并留下缺口标记），RelayMesh 服务端数据库只保存会话名称、起止时间与统计，不保存任何终端内容。已结束的会话可在侧栏「历史会话」里只读回放、搜索，并下载为纯文本 / 原始输出 / asciicast；单个会话可选「无痕」（完全不记录），主机上还可用 `--no-record` 一票否决（服务端无法覆盖）。查看与下载记录同样需要先解锁，并写入审计日志；
- **`clear`、全屏界面之后也能回看历史**：`clear` 或 Agent 类全屏界面会把屏幕擦掉，浏览器里的回滚缓冲区也就没有更早的内容。实时终端往上滚到最顶端后继续往上滚，**第一次滚动就直接**从会话记录里读出更早的内容，**接在一条文字分割线上方**（读取期间顶端有读取进度；分割线写明「上面是历史、下面是实时」以及当前是运行中还是已结束），实时内容仍在下面，之后照常输入，终端行列数不变（不会给主机发尺寸变化）。滚轮、触屏下拉与 Shift + PageUp 都可以；一次手势（含触控板惯性滚动的尾巴）只读取一次；全屏程序与鼠标上报模式下滚动仍交给程序本身，这时用右上角的「历史日志」按钮打开完整记录图层（有「平铺 / 还原」两种显示）。分割线只是显示层的一行文字，**不会写进主机上的记录**，已接上的历史也不会重复接续（再到顶只给出带当前状态的提示「已是这个会话记录的开头」）；接上的历史按当前终端宽度重新折行，接上之后 `clear` 不会把它抹掉，实时内容里的 `clear` 处会放上分割线、清屏前的内容由历史接上；历史里的全屏程序（`vim`、`less`、`htop` 等）退出时，备用屏幕会连同内容一起消失，所以历史日志（含「平铺」显示的完整记录）会把它**退出前的最后一屏**以纯文本保留在原位置，上下用「↓ / ↑ 全屏程序退出前的最后一屏」两条分割线夹住，整体仍是一条连续平铺的历史。已结束的会话同样可用。无痕会话不记录，所以没有历史日志；
- **让模型在远程主机上执行命令（ExecMesh 档，默认关闭）**：为没有自带执行环境的 AI 客户端提供三个 MCP 工具——`terminal_targets`（看有哪些主机可用、各自的 shell / 家目录 / 执行模式）、`terminal_exec`（在主机上运行一条非交互命令，可设工作目录、超时、等待时长与输出上限，长命令先返回 `running` 再用 `terminal_run` 查询）、`terminal_run`（`status` 等待结果 / `output` 分页读取完整输出 / `cancel` 取消，取消会结束整个进程组）。命令在被控主机上以守护进程为载体运行，Agent 或服务端重启都不会打断它，结果事后可取回。完整输出**只保存在被控主机**，服务端数据库只存脱敏后的命令、状态、退出码和少量预览。启用要同时满足多道闸，缺一不可：服务端设置 `RELAYMESH_TERMINAL_EXEC=on`；令牌是凭据库里的强随机令牌且勾选「终端」权限，并只授权给指定主机（环境变量令牌、免鉴权访问与本地 stdio 永远不能执行命令）；管理员在主机页打开「允许模型访问」；全局 / 单机暂停开关未启用；主机本机没有用 `--deny-model-exec` 一票否决。每台主机可设「自动 / 需批准 / 只读」三种模式（令牌上限只能更严格）：需批准模式下命令先停在待批准，由人在 Web 上批准后才真正执行。令牌可再限制并发数、等待与超时上限、输出上限和允许的工作目录。管理员随时可以「暂停模型」，进行中的命令会被结束；删除 / 重新生成 / 停用令牌同样会结束它名下进行中的命令。授予或放宽令牌的终端能力（打开终端权限位、增删主机授权、改限额、重新启用、重新生成）需要先输入密码解锁，停用 / 删除 / 清空授权这类收紧操作只需登录；
- **令牌三档与 ExecMesh 兼容命名**：MCP 令牌按权限分成三档——只有治理权限的「RelayMesh 档」、只有终端执行权限的「ExecMesh 档」（模型只看到终端工具和精简的执行说明，不要求 `workflow_id`，你在 Web 里看每条命令的实时记录）、两者都有的混合档（执行类调用缺少 `workflow_id` 时记入「未归属」线程）。设置里的令牌编辑器有「RelayMesh 模式 / ExecMesh 模式」预设和实时的“这把令牌能看到哪些工具”预览。ExecMesh 档令牌还可勾选「ExecMesh 兼容命名」，改用 `targets_list` / `target_info` / `exec` / `exec_start` / `job_status` / `job_output` / `job_cancel` 七个与独立 ExecMesh 同名的工具（原生三个工具对这把令牌不可见），已有的提示词与工作流无需改写。从独立 ExecMesh 迁移的对照表、返回字段差异与错误码见 [docs/execmesh-migration.md](docs/execmesh-migration.md)。另外请注意：MCP 令牌现在只能访问 `/mcp`，调用 REST / SSE 管理接口一律返回 `403 mcp_token_forbidden`（含终端相关接口），没有兼容开关。
- **能力 URL（只能填一个网址的连接器）**：有些连接器没法设置请求头，只能填一个网址。新建 ExecMesh 档凭据时「接入方式」选「能力 URL」，令牌就写在专用网址 `/mcp/c/rmc-…` 里：它只能做终端执行、必须逐台授权主机、默认 30 天有效期，网址在创建后显示，之后需再次验证 Web 密码才能重新查看；放进请求头 / 查询串 / 旧式路径都会被拒绝，访问日志里只记成 `/mcp/c/***`，删除、停用或重新生成后旧网址立即失效。网址等同口令，反向代理自己的访问日志需要你自行脱敏。详见 [docs/execmesh-migration.md](docs/execmesh-migration.md) 第 8 节。
- **OAuth 2.1（ChatGPT / Claude 等连接器）**：Hub 内置授权服务器与受保护资源元数据，支持授权码 + PKCE（S256）、CIMD / DCR、刷新令牌轮换与撤销；OAuth 访问令牌（`rmo_` 前缀）映射为隐藏 MCP 凭据，复用全部 ExecMesh 闸门，且不能访问 REST / SSE。**生产请务必设置** `RELAYMESH_PUBLIC_URL`（或在设置页 OAuth 策略中填写对外地址）；设置页 **OAuth 授权服务器总开关默认关闭**，开启后才暴露元数据与 `/oauth/*`。环境变量 `RELAYMESH_OAUTH=off` 为更高优先级硬关闭。详见 [docs/oauth.md](docs/oauth.md)。
- **SSH 主机（没有 Agent 也能让模型执行）**：装不了 Agent 的 POSIX 主机，可由服务端经 SSH 直接登记，模型用同样的三个工具执行命令，闸门、模式、审计与线程页完全一致。接入时先探测并由你核对主机密钥指纹，服务端会重新探测并比对后才登录；之后每次连接都必须出示这把固定的密钥，对不上就拒绝连接、不向对方发送任何凭据，也不会自动重新固定。登录凭据用 `RELAYMESH_SSH_SECRET_KEY` 加密保存（未设置或太弱时这条「服务端直连」的路径关闭；经网关接入的主机不受影响，见下一条）；命令文本经标准输入传递，不出现在 SSH 命令行里；完整输出只留在远端运行目录（保留 7 天）。两种接入方式（这里的服务端直连，和下一条的经网关转发）在主机管理上完全一致：同一套主机卡片，同一套「模型访问」授权——模型能不能用只由「允许模型使用」和令牌上的主机授权决定；网页上人工打开终端不受限制（当前是单用户模式，没有人工权限设计）。服务端直连的 SSH 主机也能在浏览器里开人工终端：会话由服务端持有，刷新浏览器、断线重连都能接上并补回输出，**不做记录、没有无痕入口**；服务端重启、停用 / 删除这台主机、改动它的地址 / 用户或总闸关闭时，会话随之结束（同时最多 8 个/台、共 32 个；空闲回收默认永久，可在主机页设「默认生命周期」，已结束的会话保留 1 小时供回看）。它没有文件传输，也没有主机本机的一票否决——请使用权限受限的专用账号；服务端直连要求目标是 POSIX 环境（登记时探测）。目标是网络设备等非 POSIX 系统、只有内网才能到达、或想让会话在服务端重启后保留并留下记录，请走下一条「SSH 网关」。详见 [docs/execmesh-migration.md](docs/execmesh-migration.md) 第 9 节。
- **SSH 网关（经内网跳板接入没有 Agent 的 SSH 主机，人和模型都能用）**：把一台已装 Agent 的主机（或一个专用的小容器，见下文「作为 SSH 网关运行」）当作「网关」，没有装 Agent、只有网关能到达的 SSH 主机（内网服务器、网络设备、不能装软件的机器）就能在浏览器里打开真实的 PTY 终端，也能像 Agent 主机一样让模型执行命令。网关**不是另一个程序，而是 Agent 多开的一项能力**，**默认开启**（与「主机服务」一样是 Agent 的标配，不分版本，不必额外加参数）：不想让某台主机承担中转时，加 `--no-ssh-gateway`（也可写 `--ssh-gateway=false`，或环境变量 `RELAYMESH_AGENT_SSH_GATEWAY=off`；Web「接入主机」里取消勾选「SSH 网关」，生成的安装命令就会带上）关闭；临时身份（`--temp`）不提供网关。旧版本的 Agent 没有这项能力，主机清单会提醒升级（见下文「Agent 升级」）。**登录凭据只保存在网关自己的存储里**：在 Web 上登记时，凭据只交给网关一次，由它真实登录验证后密封（AES-256-GCM）保存在数据目录的 `ssh-gateway/` 下——密封密钥默认是数据目录里自动生成的文件，也可以用环境变量 `RELAYMESH_AGENT_SSH_CRED_KEY`（至少 16 个字符）外置，这样凭据库文件单独被拷走也无法解开。服务端的数据库里没有任何副本，也**不需要** `RELAYMESH_SSH_SECRET_KEY`，永远不会自己去连这些主机。网关能连到哪里由网关本机的 `--ssh-allow` 限定（网段 / IP / 主机名，可重复；也可用 `RELAYMESH_AGENT_SSH_ALLOW=10.0.0.0/8,build-*.lan`，逗号分隔，主机名可含 `*`）：这是已保存的凭据能被用到哪里的**唯一边界**，名单之外的目标由网关本机直接拒绝，服务端即使被攻陷也无法越界（没有名单就不限制目标，启动横幅会提醒）；`--deny-model-exec`（一票否决）同样管到经它接入的主机。目标的主机密钥必须已在登记时核对固定，对不上就拒绝、不发送任何凭据。会话运行在网关的会话守护进程里（刷新浏览器、重启服务端都不中断），终端记录只存在网关本机，模型命令的完整输出留在目标主机的运行目录里。在 Web 上点「SSH 主机」，「接入路径」选「经网关」并选好网关：主机密钥由网关去探测，你核对指纹后才填写凭据；之后在主机卡片上点「打开终端 / 打开无痕终端」，或在侧栏「新建终端」菜单里选它；要让模型使用，与其他主机一样在「模型访问」里授权。登记时探测不出 POSIX 环境（没有 `sh` / `base64`，例如网络设备）的目标只能当人工终端用。停用、删除这台 SSH 主机，或改动它的地址 / 用户 / 网关时，它在网关上的会话随之结束，网关上的凭据也会被清掉；网关的凭据库丢了时，主机显示「凭据缺失」，在主机页重新填写即可；网关仍被 SSH 主机使用时不能删除（界面会先说明）；网关离线时这些主机显示「网关不可用」，恢复后自动恢复。详见 [docs/terminal-operations.md](docs/terminal-operations.md) 第 4 节。
- **模型 × 主机授权与「新模型默认」**：令牌上的「终端主机」名单始终是唯一的判断依据。为了不必每次手动勾选，每台主机可以设置「新模型默认」——允许（新建令牌时预先选中这台主机；接入主机、登记 SSH 主机时也可一并授予现有的终端令牌）/ 不允许（默认）。它只是预填规则，**不在运行期继承**：授予后写成令牌上明确的名单并记入审计（`model_host_grant`），通配令牌、能力 URL 令牌与没有终端权限的令牌不会被自动授予。ExecMesh 的设置 →「模型执行与权限」里有「模型 × 主机」矩阵，可整行 / 整列批量选择；授予需要先解锁，收回只需登录。设置页的分类按模块归属（通用 / RelayMesh / ExecMesh），页签末尾有「去 ExecMesh / RelayMesh 的设置」的一键跳转；
- **远程文件（terminal_files，默认关闭）**：令牌可再勾选「终端文件」，让模型在被授权主机上查看和取回文件，只限于你为令牌指定的目录之内。一个工具、四个动作：`stat`（是否存在、类型、大小、修改时间）、`hash`（SHA-256）、`pull_prepare`（为一个普通文件签发 120 秒内有效的**一次性**下载地址，模型用普通 GET 取走，内容只流经服务端内存，不落盘、不进数据库）、`push_url`（让主机自己去下载一个 http(s) 网址并原子写入指定路径，可带 sha256 校验；模型不能把内容直接推给主机）。启用要同时满足：服务端 `RELAYMESH_TERMINAL_FILES=on`（并同时 `RELAYMESH_TERMINAL_EXEC=on`）；令牌带「终端文件」权限并**必须填写允许访问的目录**（没有“默认全部”，不接受文件系统根、相对路径和 `..`；确实要让某把令牌读写主机上任意位置的文件，只能明确打开高风险开关「允许整个文件系统」——默认关闭，开启要刚刚重新输入过密码，开关与之后的每次文件操作都进审计，需要 Agent 升级到支持的版本，见 [docs/execmesh-migration.md](docs/execmesh-migration.md) 10.5）；主机已授权、允许模型访问、未暂停、没有被 `--deny-model-exec` 一票否决。路径授权由主机上的 Agent 自己执行，符号链接不会被跟随到允许目录之外；`push_url` 默认只允许公网地址（回环 / 私网 / 链路本地 / 云元数据地址在连接之前就被拒绝），主机拥有者可用 `--file-url-allow-private` 在本机放开；文件传输要求主机与令牌的模式都是「自动」。所有访问（含被拒绝的越界尝试）都进审计，只记路径、大小与校验值前缀。SSH 主机不支持。详见 [docs/execmesh-migration.md](docs/execmesh-migration.md) 第 10 节。
- **Agent 升级提醒、升级窗口与一键升级（旧版 Agent 不会被自动升级）**：升级始终是**主机自己去取**，我们刻意不做「服务端往主机推送可执行代码」的通道——那会把服务端失陷变成所有主机上的任意代码执行。版本落后于服务端自带安装文件的 Agent，在主机清单里显示「可升级」徽标，点开有两条路：① **一键升级**——主机所有者先在本机执行 `relaymesh-agent web-upgrade on` 允许（默认关闭，开关只存在主机本地，服务端无法替你打开），之后管理员在 Web 点一下，Agent 自己下载新版本、校验 SHA-256、试运行确认版本，再由独立的升级进程替换并只重启 Agent；新版本连不上服务端就自动还原旧版本并把原因带回 Web。主机上还有运行中的会话或模型运行时，点击前就会提醒并要求二次确认（会话由守护进程持有时不会中断，没有守护进程时会被结束，界面会明确区分）；需要 Linux 且由 systemd 管理的常驻服务。② **升级窗口 + 命令**——按主机的操作系统生成升级命令，并为**这一台**主机开一个 **10 分钟的升级窗口**（票据 128 位随机、只在服务端内存里；窗口只放行本来就公开的安装文件，可「延时」最长 60 分钟或「提前结束」），命令在主机上执行 `relaymesh-agent upgrade`（用户级安装加 `--user`），任何系统都可用。两条路径都只替换 Agent 程序并只重启 Agent 服务，会话守护进程与里面的终端会话不受影响。「最新版本」取自服务端安装目录里的 `VERSION`，没有这个文件就不提醒。
- **升级、回滚与数据位置**：升级与回滚时服务端数据库迁移只增不改（旧版二进制可回开新库，往返演练数据完整），Agent 升级不打断会话，服务端与 Agent 的新旧版本可以混用（旧 Agent 上的模型执行会立即得到明确的 `host_unsupported`）；终端显示内容与模型运行的完整输出只留在被控主机上，服务端数据库里没有。逐项说明、从 v1.6.x 升级的行为变化清单与记录隐私说明见 [docs/terminal-operations.md](docs/terminal-operations.md)。

### 🤝 8. 异步协作、聊天与会话守护 (New in v2.0.0)
- **异步对话模式（按工作流开启，默认关闭）**：在会话页工具栏的 hook 芯片里打开「异步对话」后，智能体用 `interactive_feedback` 加 **`wait:false`** 发布进度，**发完立刻继续干活**，不再停下来等你回复；你的追加与答复由 hook 在它两次工具调用之间送达，需要拍板的事走 `async_ask` / 计划确认。生效后阶段滑块让位给**授权档位**（默认「标准授权」，可自己加 L0 / L1 档，每个工作流各自选档），时间线上会出现不算轮次的「智能体进度」气泡与任务更新卡。**不可逆操作（部署、推送、删数据、改凭据、不可逆迁移）在任何档位下都要停下来问你**，服务端强制，没有「放飞」一档；没收到过 hook 的工作流不能开启，hook 超过 60 分钟没动静会自动降级为同步，随时可一键关闭。从没开过的工作流，回执与以前逐字一致。详见 [docs/async-mode.md](docs/async-mode.md)。
- **确认项（不阻塞的提问、计划确认、文档决策）**：智能体用 MCP 工具 **`async_ask`** 提问后**立即继续干活**，不再停下来等；你在会话页输入框上方的「待确认」条里逐张作答，或对 L0 / L1 且有推荐的项一键「全部采用推荐」（提交前先列出全部结果，整批原子生效），答复经收件箱回到智能体。同一套机制还承载**计划确认**（绑定任务，批准后再动手）与**文档决策卡**（改了选项会让旧答复作废并重新确认）。**不可逆 / 高风险（L2）的决定在服务端被拒绝走这条路**，必须用阻塞式 `interactive_feedback`，也永远不进批量；答复一经提交即为终局，答错了发一条追加说明更正。每个确认项有按工作流递增、永不复用的编号 `Q12`。详见 [docs/confirm-items.md](docs/confirm-items.md)。
- **聊天与群聊（异步对话开启后）**：智能体用 MCP 工具 `chat` 与你对话（`read` / `send` / `members` / `status` / `end`；发送永不阻塞，需要你拍板的事仍走 `interactive_feedback` 或 `async_ask`）。你也可以在网页 `/group` 建群、把多个工作流里的智能体拉进同一个群：用 `@` 点名成员（`@all`，或 `@human` 把消息推给你并穿透免打扰），消息可以带项目文档或 HTML Artifact 卡片、在侧边预览；群只能由你创建、由你拉人，智能体不能自行建群；
- **Agent 身份与状态**：Cursor hook 除了查收件箱，还把「Agent 现在在干什么」报给服务端；会话页顶栏的状态徽标显示身份与状态（空闲 / 思考中 / 执行工具 / 等待批准 / 已中止 / 出错 / 已结束），并带上来源与用时。hook 出任何问题都静默放行，不影响智能体干活。详见 [docs/inbox.md](docs/inbox.md)；
- **会话守护与非交互续聊**：给终端启动预设开「会话守护」后，Hub 每分钟判断会话是否意外结束或停摆（默认 30 分钟没有动静，可配 5～720 分钟），并在启动器里给出处理入口——**只做监测、通知与「你点一下才唤醒」，不会自动重启**。「非交互续聊」预设把 `agent -p --resume …` 这样的命令包成一键运行（经主机的执行通道，不开终端），可固定模型、对话与提示词；「异常退出时自动续跑」默认关闭，开启后有次数预算与熔断。网页新建、还没有智能体接入的会话可以一键「启用 Agent」，在指定主机与目录里无人值守地新开一个 Cursor CLI 对话。详见 [docs/terminal-operations.md](docs/terminal-operations.md) 第 8 节。
- **项目管理 Agent 运行预设（`preset_run`，默认关闭）**：令牌可再勾选「运行预设」（授予要重新输入密码解锁；需要强令牌；能力 URL 凭据不能带），并且调用方必须是你在项目页「任务管理」里勾选的**管理 Agent**——两道闸都过，它才能经 MCP 工具 `preset_run`（`list` / `status` / `run`）运行**本项目里已有的**「非交互续聊」预设，去唤醒任务板上的子会话。它只能「按下运行」：主机、目录、对话、模型、权限姿态与提示词都在预设里由你定好，它改不了也看不到；没有勾选权限位的令牌连这个工具都看不见，也不能运行自己工作流或别的项目的预设。走的是网页上点「运行」的同一条路径（总闸、暂停、主机模型访问、需批准、令牌限额、防重复一个不少），另有频率护栏（同时最多 3 条、每小时 10 次、同一预设间隔 5 分钟、全部管理 Agent 合计每天 30 次）；每次运行与每次拒绝都进审计与聊天系统行，不含提示词与命令。详见 [docs/terminal-operations.md](docs/terminal-operations.md) 第 8.12 节。

### 🗂️ 9. 项目、文档与任务 (New in v2.0.0)
- **项目与授权**：把若干工作流归成一个项目，项目带着知识库、模块、授权与审计；访问控制有 `off` / `audit` / `enforce` 三档（新建项目默认 `audit`），授权角色分读 / 评论 / 编辑 / 管理，由你在项目页发放（对某个模块或文档有管理权的智能体，可以把读 / 评论 / 编辑权限转授给别的智能体，管理权不能转授）；你在项目页勾选的「管理 Agent」获得整块任务板的编辑权。详见 [docs/projects-and-access.md](docs/projects-and-access.md)；
- **项目文档库**：按 workflow 存放 Markdown 设计/决策/运行手册；会话页头「任务」旁有「项目」入口（带文档篇数角标）：工作流已归入项目的，点开就是这个项目的知识库，单篇文档在项目页里阅读、编辑、看修订历史与评论（`/projects/<项目>/docs/<工作流>/<文档>`；老的 `/docs/...` 链接会自动跳到这里）；还没归入项目的工作流入口不消失，仍打开老的文档页 `/docs/<工作流>`，页内提示可以去项目页归入；MCP 工具 **`project_docs`**（与 `task_board` 同权限）支持 overview / save / search / export / import / blueprint；正文内 ` ```decision ` 围栏为决策卡，可在文档页直接作答（同步成确认项，见第 8 节的「确认项」）。详见 [docs/project-docs.md](docs/project-docs.md)。
- **HTML Artifact、共享数据、评论与反链（文档库的延伸）**：文档可以存成在沙箱里运行的 HTML 页面（只能跑脚本：没有网络、没有 Cookie、碰不到外层页面），页面通过 `window.relaymesh.data` 与智能体读写同一份 JSON 键值数据（最多 200 个键、合计 256 KB，写入进审计，多方并发写用 `expected_version` 防覆盖），可以做成人和多个智能体共用的看板；你在文档上留的评论会进智能体的收件箱，处理完标记已解决；`links` 能查出文档的出链与反链，改名或删除前先看一眼；工作流或整个项目的文档库都可以在网页上一键打包下载为 ZIP，也能经 MCP 导出 / 导入。详见 [docs/project-docs.md](docs/project-docs.md)；
- **任务清单（独立于会话）**：把「要做的事」从会话里拆出来，像工作表一样独立保存，并按项目（workflow）组织。每个会话页头「工作表」按钮旁有一个「任务」入口（带「进行中 / 验证中」角标），点击后**整个会话页切换成这个项目的任务页**（`/work/<项目>`，返回按钮和浏览器后退都回到会话；侧栏的「任务清单」进入当前会话所属的项目）。页面左侧是项目的**模块规划树**（模块 → 功能两级，窄屏收进左侧抽屉）：点节点按模块 / 功能筛选，「未归类」单独一类；「编辑规划」里可以加模块、加功能、改名、上移 / 下移、删除（删除前先说明后果：里面的任务会变成「未归类」，任务本身不会被删除）。右侧任务列表**跟随规划树的顺序分组**，每个模块里先看正在做的，一批任务（批次）的子任务按规划顺序缩进在父任务下；**每一行都有进度条**——填写了完成度的画实色并带数字，没填的按子任务比例或状态推导、画淡色且不带数字，已完成画满条；分组头显示「已完成 / 总数」与模块进度；也可以切到「最近进展」，不分组平铺、最近动过的排在最前。任务详情做了减负：状态、标题、所属模块、完成度、当前进度、结论和最近 5 条进展，类型 / 标签 / 关联会话 / 来源收进「更多信息」。智能体通过 **`task_board`**（`overview` / `list` / `plan` / `batch`：一次定义整棵模块规划、成批建任务与更新，清单为空时 `overview` 直接给出用法）以及 `workflow_context` 的 `task_create` / `task_update` / `task_get` / `task_list`（支持 `module` 与 `percent`）维护它们；模块名是**封闭名单**——任务只能标注已有的模块路径，各会话的标注不会各写各的；提示词会引导模型首次使用时按项目稳定的功能域（外加发布运维、质量测试、文档待办这类横切工作线）规划模块，而不是按轮次 / 日期 / 会话 / 阶段，之后每次回到清单先看 `overview`（附维护守则）并把新任务归入已有路径；服务端还会做轻量的「结构体检」——模块名带着轮次 / 日期 / 阶段的痕迹或是「其他 / 杂项」、顶层模块过多、有未归类任务时，`overview` / `plan` / `batch` 的回执与回执头里的 `tasks` 行会提醒（只提醒、不拒绝）。任务状态**绑定智能体自己的开发流程**：待办 → 进行中 → **验证中**（开发完成，在跑回归 / e2e / 热更冒烟）→ 已完成——验证通过后由智能体**直接置为「已完成」并同时写下结论**（做了什么、怎么验证的；没写结论的完成会被服务端拒绝并说明怎么办），新建任务不能直接是「已完成」，已完成的任务智能体发现问题可以重新打开（时间线记明是谁做的）；不再有需要你逐条点头的「待确认」环节，你随时可以手动改任何状态，验收后发现问题就在任务上「发起改进需求」；批次下进行中和验证中的子任务同样会单独露出来，保证页头角标与列表里看到的一致；会话阶段也由五段收敛为「讨论 / 方案 / 开发」三段（旧的评估 / 验证 / 完成在各入口自动映射，存量数据启动时迁移）；
- **共享任务板**：同一项目的工作流共用一块任务板与 `#编号`。你（或你指定的管理 Agent）可以把任务指派给另一个工作流，对方接取或婉拒（要写理由）、干完交付，由指派人验收（写下结论后完成）或退回；普通智能体只能写自己的任务、读其余的。详见 [docs/shared-task-board.md](docs/shared-task-board.md)；
- **编号引用**：汇报、任务描述里写 `#12`（任务）、`Q12`（确认项）、`[[文档路径]]`（文档）、`sess-xxxxxxxx`（会话），网页里自动变成可点击的链接，点一下直达详情；
- **内置使用手册（`usage_guide`，只读）**：上面「文档索引」里的这些文档（功能地图、常见问题、项目与授权、任务板、凭据与权限位、确认项、收件箱、终端运维……共 15 篇）随 Hub 一起编译进二进制，已接入的智能体用 MCP 工具 `usage_guide` 随时查阅，永远和它连着的这个版本一致——**你不会用的时候，直接问接入的会话**，它会先读手册再回答。三个动作：`list`（全部主题、摘要与篇幅）、`search`（按关键词找小节，支持「指派 / 认领」这类同义问法，没有就如实说没有）、`get`（读一篇或其中一节，长文自动分页并带目录）。只读、不需要 `workflow_id`，不会生成会话也不会改任何数据；任何带「沟通反馈」或「会话查询」权限的令牌自动可用（ExecMesh 档与只有系统信息 / 技能 / 配置的令牌看不到），初始化指引里也有对应一节，凭据页的工具预览会自动列出它。手册的维护有测试守着：每个 MCP 工具、每个权限位、每个动作枚举都必须在手册里有说明，文档里的相对链接必须有效，不得含密钥或私有路径。

---

## 🏗️ 系统架构图 (Architecture)

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                              AI 开发客户端 (AI Clients)                      │
│                  Cursor  /  Claude Desktop  /  VS Code  /  Codex            │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ Streamable HTTP MCP (POST /mcp)
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                            RelayMesh 协作中枢核心                            │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                     Go 1.26 单二进制服务内核 (18775/18776)            │  │
│  │  - Streamable HTTP MCP Server    - 内存级反爆破限流器 (Lockout)         │  │
│  │  - SSE 实时事件广播 Broker         - 零依赖 TLS 证书引擎 (18776)         │  │
│  │  - RESTful 业务状态与草稿 API     - embed.FS 纯静态网页内嵌托管         │  │
│  └───────────────────────────────────┬───────────────────────────────────┘  │
│                                      │ 持久化读写                           │
│                                      ▼                                      │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                   纯 Go SQLite 数据库引擎 (无 CGO 依赖)               │  │
│  │    会话与反馈 / 工作项 / 项目文档 / 终端 / 审计 …（共 50 余张表）     │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ SSE 实时推送 & REST API
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Vue 3.5 + Tailwind v4 Web 控制台                      │
│      沉浸式工作区  /  多草稿箱 (Ctrl+←/→)  /  手势图片灯箱  /  ASR 语音录音  │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 🚀 部署与运行方式 (Installation & Deployment)

RelayMesh 支持多种部署运行形态，覆盖从「本地极速免安装试用」到「企业级云原生生产部署」的全部场景：

### 模式一：Go 原生即时运行 / 类似 npx 模式（⚡ Go 开发者首选，0 安装即下即跑）

Go 1.26+ 开发者无需手动克隆仓库或配置 Node 环境，直接通过 Go 命令行一键拉取并即时运行内嵌完整 Web 控制台的单二进制：

```bash
# 1. 直接运行最新版 HTTP 服务（自动下载、缓存并运行，类似 npx / uvx 模式）
go run github.com/teacat99/RelayMesh/v2/cmd/relaymesh@latest

# 2. 或直接运行原生 stdio MCP 模式（为 Cursor / Claude 等 MCP Host 提供直启端点）
go run github.com/teacat99/RelayMesh/v2/cmd/relaymesh@latest mcp stdio

# 3. 或全局安装到 $GOPATH/bin 作为系统 CLI 工具使用
go install github.com/teacat99/RelayMesh/v2/cmd/relaymesh@latest
relaymesh            # 启动 HTTP/HTTPS 常驻服务与 Web 控制台
relaymesh mcp stdio  # 启动原生 stdio MCP 服务
```
> ⚠️ **Go 模块路径从 2.0 起带 `/v2`**：这是 Go 对 2.x 及以上版本的硬性规定（模块路径末尾必须是 `/v2`）。以前用的不带 `/v2` 的写法（`github.com/teacat99/RelayMesh/cmd/relaymesh@latest`）只会停在 1.x 的最后一版、不会再收到 2.x，请改成上面带 `/v2` 的写法；Cursor / Claude 等 MCP 配置里的 `args` 也要同步改。Docker 镜像、预编译二进制和一键安装脚本不受影响，不用改。

> 💡 **桌面环境智能唤醒**：在 macOS / Windows / Linux 桌面或 WSL2 环境下启动时，RelayMesh 将在 800ms 内自动唤醒系统默认浏览器打开 Web 控制台；容器或远程无头服务器则严格静默跳过。

---

### 模式二：预编译单二进制与一键安装脚本（📦 开箱即用，免任何运行环境）

适用于没有 Go 或 Docker 环境的用户，直接下载单一绿色可执行文件，双击或一键执行即可：

#### 1. Linux / macOS 一键安装脚本
```bash
curl -fsSL https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.sh | bash
```

#### 2. GitHub Releases 预编译单二进制直接下载
前往 [GitHub Releases](https://github.com/teacat99/RelayMesh/releases) 下载对应操作系统架构的独立单文件：
- **Linux (x86_64 / amd64)**: `relaymesh-linux-amd64`
- **Linux (ARM64 / aarch64)**: `relaymesh-linux-arm64`
- **macOS (Apple Silicon M系列 / arm64)**: `relaymesh-darwin-arm64`
- **macOS (Intel / amd64)**: `relaymesh-darwin-amd64`
- **Windows (x86_64)**: `relaymesh-windows-amd64.exe`

下载后直接运行：
```bash
# Linux / macOS 赋予执行权限并运行 HTTP 常驻服务
chmod +x relaymesh-*
./relaymesh-linux-amd64

# 或以原生 stdio 模式运行（与 AI 客户端直接交互）
./relaymesh-linux-amd64 mcp stdio
```

> **安装脚本做了什么**：按系统与架构选择对应的 Release 文件，下载后用同一 Release 的 `checksums.txt` 校验 SHA256（不匹配即中止）；root（或 `/usr/local/bin` 可写）装到 `/usr/local/bin/relaymesh`，否则装到 `~/.local/bin/relaymesh`（macOS 上该目录默认不在 `PATH` 里，请用绝对路径运行或自行加入 `PATH`）。固定版本：`curl -fsSL …/install.sh | sh -s -- v<版本号>`。
>
> **数据放在哪里**：服务模式默认写入**当前工作目录**下的 `data/`（数据库 + `data/certs/` 自签名证书）——换个目录启动就是一份全新的空数据，常驻使用请用绝对路径的 `RELAYMESH_DB_PATH` 固定位置；stdio 模式写入系统用户数据目录。
>
> **Linux 服务器上常驻运行**：用 systemd 托管（`DynamicUser` + 环境文件，崩溃自动拉起，开机自启），完整的单元文件、备份与升级命令见 [AI 部署指南「路径 B」](docs/deploy-with-ai.md)，人照着做同样可行。

---

### 模式三：Docker / Docker Compose 容器化部署（🐳 多架构支持，生产与常驻推荐）

官方镜像发布于 Docker Hub（`teacat99/relaymesh`，约 37 MB），支持 `linux/amd64` 与 `linux/arm64`；标签是**不带 `v` 的版本号**（如 `2.0.0`）加上 `latest`；预览版带后缀（如 `2.0.0-rc.1`），只有这个完整版本标签，不会顶掉 `latest`。

仓库根目录的 [`docker-compose.yml`](docker-compose.yml) 是一份开箱即用的标准文件：口令、令牌、端口、版本号都通过同目录的 `.env` 设置，**不需要编辑 YAML**。最短流程就是上面「5 分钟上手」的 B：

```bash
mkdir -p ~/relaymesh && cd ~/relaymesh
curl -fsSLO https://raw.githubusercontent.com/teacat99/RelayMesh/main/docker-compose.yml
# 创建 .env（见上文 B，或只写你需要的几行）后：
docker compose up -d
```

`.env` 里可以设置的变量（全部有默认值，什么都不写也能启动）：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `RELAYMESH_PUBLISH_BIND` | `0.0.0.0` | 宿主机监听地址。`0.0.0.0`（默认）= 所有网卡：局域网 / 手机 / 其他机器都能访问——**所以一定要设置下面的密码与令牌**；`127.0.0.1` = 仅本机可访问（只在本机用，或配合同机反向代理时用） |
| `RELAYMESH_PUBLISH_HTTP_PORT` / `RELAYMESH_PUBLISH_HTTPS_PORT` | `18775` / `18776` | 宿主机上的 HTTP（Web + `/mcp`）与 HTTPS（自签名证书，浏览器麦克风需要）端口；端口冲突时修改 |
| `RELAYMESH_WEB_USERNAME` / `RELAYMESH_WEB_PASSWORD` | `admin` / 空 | Web 控制台账号与密码；密码为空 = 免登录（只适合仅本机访问）；远程终端功能要求设置密码 |
| `RELAYMESH_MCP_TOKEN` | 空 | MCP 访问令牌；为空 = `/mcp` 免鉴权 |
| `RELAYMESH_HOST_NAME` | `relaymesh-hub` | 主机名标识，多台机器接入同一个中枢时用来区分 |
| `RELAYMESH_TAG` | `latest` | 镜像版本；想固定版本或回滚时写成具体版本号，例如 `2.0.0`（预览版如 `2.0.0-rc.1`） |

> 口令里不要出现 `$`（compose 会把它当作变量展开）；`.env` 请设置权限 `600`。数据库、证书与身份都在 `./data`，备份 / 迁移就是复制这个目录。

**三种常见场景**

- **只在本机用**：在 `.env` 里写 `RELAYMESH_PUBLISH_BIND=127.0.0.1`，这样密码与令牌可以不设（免登录）。⚠️ 不写这一行又不设密码 = 一个全网可达的免登录中枢。
- **局域网 / 手机访问**：保持默认（所有网卡），并且设置好密码与令牌（上面「5 分钟上手」的命令已经生成）。手机上要用麦克风录音（语音输入），请访问 `https://<服务器IP>:18776/`（自签名证书，浏览器点「继续访问」），MCP 客户端仍然用 `http://<服务器IP>:18775/mcp`。
- **公网（域名 + HTTPS）**：务必写 `RELAYMESH_PUBLISH_BIND=127.0.0.1`，由同一台机器上的 Caddy / Nginx 做 HTTPS 反向代理，见下文「反向代理配置」。**不要把 18775 直接暴露到公网**（口令会以明文传输）。

**不用 Compose 的一行版**（仅本机试用：没有设密码，所以只绑定本机；要给局域网用，先加上 `-e RELAYMESH_WEB_PASSWORD=… -e RELAYMESH_MCP_TOKEN=…`，再去掉两个 `-p` 前面的 `127.0.0.1:`）：

```bash
docker run -d --name relaymesh --restart unless-stopped --user 0:0 \
  -p 127.0.0.1:18775:18775 -p 127.0.0.1:18776:18776 \
  -v "$PWD/data:/app/data" teacat99/relaymesh:latest
```

---

### 模式四：源码克隆与自主构建（🛠️ 适用于深度定制与二次开发）

```bash
# 1. 克隆项目仓库
git clone https://github.com/teacat99/RelayMesh.git
cd RelayMesh

# 2. 构建前端静态资源
pnpm --dir frontend install
pnpm --dir frontend run build

# 3. 编译纯 Go 单二进制产物（前端自动内嵌进 Go 二进制）
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/relaymesh ./cmd/relaymesh

# 4. 运行单二进制服务
./bin/relaymesh
```

---

### ✅ 验证部署是否成功

```bash
curl -s http://127.0.0.1:18775/api/v1/auth/status                  # 期望：200 的 JSON；设置了密码与令牌时含 "auth_required":true 与 "mcp_auth_required":true
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18775/   # 期望：200（Web 首页）
```

经反向代理访问时把地址换成 `https://你的域名`。**对外暴露自查**：在另一台机器上访问 `http://<服务器IP>:18775/api/v1/auth/status`——看到 `"auth_required":true` 才说明别人进不来；返回了 JSON 但 `auth_required` 是 `false`，请马上设置口令，或写上 `RELAYMESH_PUBLISH_BIND=127.0.0.1`。登录、MCP 令牌握手、重启后仍可登录等逐项检查，以及一个可以直接运行的验证脚本，见 [AI 部署指南第 8 节](docs/deploy-with-ai.md)。

### 🔄 升级、备份、回滚与卸载（Docker Compose）

```bash
# 升级到最新版（数据库迁移自动完成）
docker compose pull && docker compose up -d

# 备份：先停服务，保证数据库文件一致
docker compose down && tar czf relaymesh-data-$(date +%F).tgz data && docker compose up -d

# 回滚：在 .env 里写 RELAYMESH_TAG=<旧版本号>（不带 v，例如 2.0.0；想回到升级前的 1.x 就写那个版本号），然后
docker compose up -d
# 想回到最新版：删掉 .env 里的 RELAYMESH_TAG 这一行，再 docker compose pull && docker compose up -d

# 卸载（会删除数据，建议先备份）
docker compose down && cd .. && rm -rf relaymesh
```

服务端数据库迁移只增不改：旧版本可以直接打开新版本升级过的数据库，往返升级与回滚数据完整；新版本才有的功能所产生的数据，旧版本可能不显示，升回去就能看到。单二进制（systemd）的升级、备份与恢复命令见 [AI 部署指南第 12 节](docs/deploy-with-ai.md)；远程终端相关的升级细节见 [docs/terminal-operations.md](docs/terminal-operations.md)。

### 🧯 常见问题速查

| 现象 | 原因与处理 |
|---|---|
| `bind: address already in use` / `port is already allocated` | 端口被占用。Docker 在 `.env` 里改 `RELAYMESH_PUBLISH_HTTP_PORT` / `RELAYMESH_PUBLISH_HTTPS_PORT`；二进制设置 `RELAYMESH_PORT` / `RELAYMESH_HTTPS_PORT`（stdio 直驱时占着端口的如果是另一个 RelayMesh stdio 窗口，会自动接入而不是报这个错） |
| 本机能打开，手机 / 别的电脑打不开 | 先看 `.env` 里有没有 `RELAYMESH_PUBLISH_BIND=127.0.0.1`（那是只给本机用的写法：删掉它或改成 `0.0.0.0`，再 `docker compose up -d`）；再检查服务器防火墙 / 云安全组有没有放行 `18775` / `18776` |
| `https://IP:18776` 提示证书不受信任 | 内置的是自签名证书，点「继续访问」即可（麦克风录音需要 HTTPS）；想要受信任的证书就走反向代理。MCP 客户端请用 `http://…:18775/mcp` |
| MCP 客户端报 `401` | 没带令牌或令牌不对：请求头 `Authorization: Bearer <令牌>`，或地址后加 `?token=<令牌>` |
| `-32001 Request timed out`，或经反向代理后网页不再实时更新 | 反向代理缓冲了 SSE 或超时太短：Nginx 需要 `proxy_buffering off` 与 `proxy_read_timeout 3600s`，Caddy 需要 `flush_interval -1`（见「反向代理配置」） |
| 重启后会话、草稿都没了 | 数据没有持久化：Docker 要确认挂载了 `./data`；二进制默认写**当前目录**的 `data/`，换目录启动就是新数据，请设置绝对路径的 `RELAYMESH_DB_PATH` |
| 忘了密码 | 在网页里改过密码又忘了：执行 `docker compose exec relaymesh /app/relaymesh reset-auth`（二进制：`relaymesh reset-auth`），会清除网页上改过的账号密码，恢复成 `.env` 里的 `RELAYMESH_WEB_PASSWORD`（`grep PASSWORD .env` 可查）。想同时换一个新密码：先改 `.env` 并 `docker compose up -d`（让容器读到新值），再执行这条命令——顺序反了会恢复成旧密码 |

完整的排错表（含 Docker 权限、Compose 版本、`PATH`、没有 openssl 等）见 [AI 部署指南第 11 节](docs/deploy-with-ai.md)。

---

### 🖥️ 附：远程终端 Agent（在被控主机上安装）

远程终端需要在**被控主机**上运行一个 Agent，它会主动连回 RelayMesh（被控主机无需开放任何入站端口）。

**推荐做法：复制 Web 面板生成的命令。** 在「终端 → 接入主机」里先开启发现窗口，页面会按目标系统生成一行安装命令，其中已经带上服务端地址、证书校验方式，以及一张**只在发现窗口开启期间有效的安装票据**。目标主机直接从这台服务端下载安装脚本和 Agent，不需要访问 GitHub。命令的形态如下（`<票据>` 以页面显示为准，请不要转发给他人）：

```bash
# Linux / macOS：常驻为系统服务（需要 root；开机即运行）。
# 面板生成的命令会自己判断：root（含没有 sudo 的容器 / 云主机）不加 sudo，普通用户且有 sudo 才加，不必手动改。
# 手写时：root 直接用下面这条去掉 sudo 的写法即可。
curl -fsSL https://relay.example.com/i/<票据>/install.sh | sudo sh -s -- agent install --server https://relay.example.com

# 常驻为当前用户的服务（无需 root；Linux systemd --user / macOS LaunchAgent）
# Linux 上安装时会自动执行 `loginctl enable-linger`，服务在退出登录后继续运行、开机后无需登录也会启动；
# 卸载时只关闭由安装器开启的 linger（本来就开着的不动）；不想要这个行为加 --no-linger。
curl -fsSL https://relay.example.com/i/<票据>/install.sh | sh -s -- agent install --user --server https://relay.example.com

# 临时运行（身份不落盘，退出后机器码失效；适合一次性排障）
curl -fsSL https://relay.example.com/i/<票据>/install.sh | sh -s -- agent run --temp --server https://relay.example.com
```

Windows 使用 PowerShell 版安装脚本（Windows PowerShell 5.1 与 PowerShell 7 均可）：

```powershell
# 常驻为当前用户（登录 Windows 后自动启动；不需要管理员）
& ([scriptblock]::Create((irm 'https://relay.example.com/i/<票据>/install.ps1').TrimStart([char]0xFEFF))) agent install --user --server 'https://relay.example.com'

# 常驻为系统级（开机自启，以 SYSTEM 运行；需要「以管理员身份运行」的 PowerShell）
& ([scriptblock]::Create((irm 'https://relay.example.com/i/<票据>/install.ps1').TrimStart([char]0xFEFF))) agent install --server 'https://relay.example.com'

# 常驻为当前用户 + 最高权限（以你自己的账户与环境运行，登录时自带完整管理员令牌、不弹 UAC；需要「以管理员身份运行」的 PowerShell）
& ([scriptblock]::Create((irm 'https://relay.example.com/i/<票据>/install.ps1').TrimStart([char]0xFEFF))) agent install --user --highest --server 'https://relay.example.com'

# 临时运行（身份不落盘，退出后机器码失效）
& ([scriptblock]::Create((irm 'https://relay.example.com/i/<票据>/install.ps1').TrimStart([char]0xFEFF))) agent run --temp --server 'https://relay.example.com'
```

- **服务端自带安装源**：两个安装脚本内嵌在服务端二进制里；各平台的 Agent 放在服务端的 `agent-dist/` 目录（**Docker 镜像已内置**，五个平台合计约 37 MB，单个约 7～8 MB）。下载入口是 `/i/<票据>/<文件名>`：票据是 128 位随机值，**随发现窗口同生共死**——开窗时生成，关窗或到期即失效，窗口关闭期间一律返回 404（不透露路径是否存在）；另有按来源 IP 的限速与并发下载上限，且只提供固定文件名清单内的文件。`checksums.txt` 由服务端按实际文件现算，安装脚本据此校验 SHA256，不匹配即中止。
- **没有 `agent-dist/` 时**（单二进制 / `go run` / 源码运行）：页面自动改为 GitHub Release 来源的命令，例如 `curl -fsSL https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.sh | sudo sh -s -- agent install --server https://relay.example.com`，并提示目标主机需要能访问 GitHub、且公开仓库已发布对应版本。想让单二进制部署也自带安装源，把各平台的 `relaymesh-agent-<os>-<arch>[.exe]` 放进可执行文件旁的 `agent-dist/`，或用 `RELAYMESH_AGENT_DIST_DIR` 指定目录；本地预览用 `bash scripts/build-agent-dist.sh` 生成 `bin/agent-dist/`（预览用的 `docker-compose.yml` 已挂载）。
- **证书**：页面按「你正在怎样访问这个页面」自动选择，并折叠成一行结论（可点「修改」）：经受信任证书（域名 / 反向代理）访问时无需额外参数；服务端启用了内置自签名 HTTPS 时按证书指纹校验——命令带 `--pin sha256:<指纹>`，下载脚本时 Linux / macOS 用 `curl -k --pinnedpubkey '<公钥指纹>'`（`-k` 只跳过证书链，公钥指纹仍强制校验，curl 不支持该选项时直接失败而不会降级），Windows 用 PowerShell 的证书指纹回调完成同样的校验；有内置 HTTPS 时不会自动选择明文。明文内网才用 `--insecure`（仅限完全可控的网络）。
- **完整性**：服务端来源时由 `checksums.txt` 校验；GitHub 来源时脚本会从 GitHub Release 下载 `relaymesh-agent-<os>-<arch>`，并用同一 Release 的 `checksums.txt` 校验 SHA256，同样不匹配即中止。
- **日常管理**：`relaymesh-agent id`（打印机器码）、`relaymesh-agent status`（机器码与服务状态）、`relaymesh-agent uninstall [--purge]`（卸载服务，`--purge` 同时删除身份）。Windows 上同样可以用安装脚本转交，例如 `… agent status`、`… agent uninstall --user`。
- **在 Web 上删除主机时清理设备端**：删除只会让服务端忘记这台主机，设备上的 Agent 不会自己消失，而会按间隔继续尝试连接（被服务端拒绝）。删除对话框里：在线、版本足够新、并且是装成常驻服务运行的 Linux / Windows Agent 会出现「同时卸载设备端」选项（默认不勾选）——勾选后 Hub 在删除前向 Agent 下发卸载请求，Agent 在本机另起一个与自身进程树无关的卸载进程（Linux 经 `systemd-run`，Windows 经一次性计划任务）完成 `uninstall --purge`；终端录像（`records/`）与模型命令输出（`runs/`）不属于身份，仍保留在设备的数据目录里。设备离线、Agent 较旧、前台 / 临时运行或 macOS 时没有这个选项，对话框会如实说明原因并给出在设备上手动执行的命令。这个操作与删除主机一样需要登录并再次验证密码，并写入审计日志。
- **身份**：`run` 默认使用持久身份（首次运行时在本机生成并保存，机器码长期不变）；加 `--temp` 改用仅存内存的临时身份（每次运行机器码都不同，退出后主机即消失，需重新启用）；`install` 总是持久身份。`--persist` 可显式写明默认行为（与 `--temp` 互斥）。
- **终端记录**：默认开启，记录保存在 Agent 数据目录下的 `records/`（内容不会上传服务端；临时身份运行时不记录）。选项均可写在 `run` / `install` 之后：`--no-record`（或环境变量 `RELAYMESH_AGENT_NO_RECORD=1`，本机一票否决，服务端无法覆盖）、`--record-session-mb`（单会话上限，默认 64）、`--record-session-lines`（单会话保留的最近行数，默认不限制；Web 里也可以设，见上文「终端限高」）、`--record-host-mb`（本机总量上限，默认 512）、`--record-retention-days`（会话结束后保留天数，默认 30）、`--record-min-free-mb`（磁盘剩余空间低于该值时暂停录制，默认 1024，`-1` 关闭这项保护）。服务端只能让这些限制更严格（见下文 `RELAYMESH_TERMINAL_RECORD*`），不能放宽。
- **模型执行的本机一票否决**：`--deny-model-exec`（或环境变量 `RELAYMESH_AGENT_DENY_MODEL_EXEC=1`，写在 `run` / `install` / `holder run` 之后）。设置后这台主机拒绝一切模型执行，服务端的任何授权都无法覆盖；人工终端不受影响。Agent 与会话守护进程各自独立执行这个否决。除此之外本机不再设默认限制，是否允许、用什么模式由服务端的多道闸控制。模型运行的输出保存在 Agent 数据目录下的 `runs/`（运行结束后默认保留 7 天，并受总量配额限制），不会上传服务端；临时身份运行（`run --temp`）不支持模型执行。
- **文件拉取的网址策略**：`push_url` 让这台主机去下载模型给出的网址，所以默认只允许公网地址，回环 / 私网 / 链路本地 / 云元数据地址一律拒绝。确实需要从内网取文件时，由主机拥有者在本机加 `--file-url-allow-private`（或 `RELAYMESH_AGENT_FILE_URL_ALLOW_PRIVATE=1`，写在 `run` / `install` 之后）；服务端无法设置它。`--deny-model-exec` 同样拦住文件访问，文件能力没有单独的本机开关。
- **会话守护进程**：`install` 默认装成两个服务单元（Linux 两个 systemd 单元 / macOS 两个 launchd / Windows 两个计划任务）：`relaymesh-agent holder` 只监听本机的 Unix 套接字（不开任何网络端口，仅当前用户可访问），持有全部 PTY 与会话记录；Agent 通过它转发。升级只替换 Agent，不会动守护进程，所以不会打断会话；想让守护进程也换成新版本，执行 `relaymesh-agent holder restart`（会结束其中的会话和模型正在运行的命令，命令会先确认；加 `--drain` 则等它们自然结束后再退出）。`--holder embedded` 回到「会话随 Agent 进程结束」的旧行为，`--holder external` 则只使用已有的守护进程；`run` 默认 `auto`（有守护进程就交给它）。`holder status` 列出版本、持久性与全部会话。
- **接入流程**：Agent 首次连接后会出现在 Web「终端 → 接入主机」的待处理列表，核对机器码后点击「启用」即可。没有打开发现窗口时，陌生机器码会被拒绝并自动低频重试，日志里会说明原因。
- **Windows 常驻方式**：以「计划任务」而不是 Windows 服务实现，这样终端会话里的 Shell 能使用用户自己的环境。
  - *当前用户（`--user`，推荐）*：登录触发，任务名 `RelayMeshAgent-<用户>`，程序在 `%LocalAppData%\Programs\RelayMesh Agent`，身份与日志在 `%LocalAppData%\RelayMesh\Agent`；不需要管理员，注销后停止。
  - *系统级（管理员 PowerShell，不加 `--user`）*：开机触发，以 SYSTEM 运行，任务名 `RelayMeshAgent`，程序在 `%ProgramFiles%\RelayMesh Agent`，身份与日志在 `%ProgramData%\RelayMesh\Agent`。SYSTEM 身份下打开的 Shell 权限等同本机最高，请确认有这个需要。
  - *当前用户 + 最高权限（管理员 PowerShell，`--user --highest`）*：介于两者之间——任务名、安装位置、数据目录与 `--user` 完全相同，登录触发、以你自己的账户与环境运行（Shell 的家目录、环境变量、`whoami` 都是你本人），区别只是任务以「最高权限」运行，登录时自动带上完整的管理员令牌，不弹 UAC；其中打开的 Shell 拥有管理员权限，注销后停止。创建这种任务需要管理员令牌，所以安装、修改与卸载它都要在管理员 PowerShell 里执行；`--highest` 必须与 `--user` 合用，且仅适用于 Windows。
  - 任务启动的是无控制台窗口的副本，日志写入 `agent.log`（自动轮转，`--log-file` 可改位置）；任务另带 5 分钟一次的看门狗，Agent 被强制结束后会被自动拉起。`status` 会列出任务状态与日志位置。
  - 也可以不用脚本，直接从 [GitHub Releases](https://github.com/teacat99/RelayMesh/releases) 下载 `relaymesh-agent-windows-amd64.exe` 手动运行 `run --temp`、`install --user` 等命令。
  - *临时运行与管理员权限*：在普通（非管理员）的交互式终端里执行 `run --temp` 时，Agent 会请求提升权限（弹出 UAC），在新的管理员窗口里连接，让终端拥有管理员身份，原窗口等它退出后返回；在 UAC 里选「否」则以当前权限继续。不需要管理员权限，或在脚本 / 计划任务里运行，加 `--no-elevate`（或环境变量 `RELAYMESH_AGENT_NO_ELEVATE=1`）即可；非交互场景从不弹 UAC。
  - *双击运行*：直接双击 `relaymesh-agent-windows-amd64.exe`（不带任何参数）会进入向导，问清服务端地址（自签名证书可粘贴指纹）后以临时身份运行；窗口关闭即退出，出错时窗口会停留等你看清信息。
  - 身份私钥文件的访问权限仅限当前用户、SYSTEM 与管理员组；每次加载都会校验并自动收紧被放宽的权限。

#### 作为 SSH 网关运行（常驻主机或专用容器）

网关和 Agent 是**同一个程序**：网关是 Agent 默认开启的一项能力（含义、信任模型与注意事项见上文「SSH 网关」一条；不想要时用 `--no-ssh-gateway` 关闭），没有单独的"网关安装包"。因为默认开启，**建议用 `--ssh-allow` 把目标限定在需要的网段 / 主机内**——没有名单就不限制目标（凭据仍须由你在 Web 上登记、主机密钥须已核对固定）。**不需要**服务端设置 `RELAYMESH_SSH_SECRET_KEY`（凭据只留在网关上）；模型执行相关的服务端开关只决定"模型能不能用"，与网关本身无关。

- **接入页一键带上（推荐）**：Web「接入主机」里「SSH 网关」默认就是勾选的；按需填写「允许连接的目标」（网段 / IP / 主机名，每行或逗号分隔一个），页面生成的安装命令就会带上对应的 `--ssh-allow`；取消勾选则带上 `--no-ssh-gateway`。支持的系统与普通 Agent 完全一致（Linux / macOS / Windows，系统级或仅当前用户）；临时运行的命令不提供这个选项，因为临时身份不能当网关。
- **限定目标 / 关闭网关**：网关默认就是开的，安装时按需带上选项，例如 `relaymesh-agent install --ssh-allow 10.0.0.0/8 --ssh-allow 'build-*.lan' --server https://relay.example.com`（选项会写进服务单元）。已经装好的主机重新执行一次 `install`，再 `relaymesh-agent holder restart` 让守护进程采用新参数（会结束这台主机上现有的终端会话和模型正在运行的命令；想等它们自然结束，改用 `holder restart --drain`）。这台主机会出现在 Web「SSH 主机」对话框的网关列表里（版本较旧的 Agent 没有这项能力，主机清单会提醒升级）。启动横幅会写明「SSH网关: 已开启」以及当前的允许名单；没有设置名单时横幅会提示建议加上 `--ssh-allow`。想关掉网关：加上 `--no-ssh-gateway` 重新安装并重启守护进程，经它接入的主机会显示「网关不可用」，凭据库仍留在数据目录里。
- **专用容器镜像 `teacat99/relaymesh-agent`**：只含 Agent（约 17 MB，非 root 用户 uid 10001），默认命令就是 `run --ssh-gateway`，放在能到达内网 SSH 目标的机器上即可。镜像随正式版本一起发布到 Docker Hub 与 GHCR；发布之前或想自行构建，用仓库里的 `Dockerfile.agent`：`docker build -f Dockerfile.agent -t relaymesh-agent .`（本地构建的镜像名替换下面的 `image`）。

  ```yaml
  services:
    relaymesh-gateway:
      image: teacat99/relaymesh-agent:latest
      container_name: relaymesh-gateway
      restart: unless-stopped
      # 不写 command：镜像默认就是 ["run", "--ssh-gateway"]。网关现在是 Agent 的默认能力；想关闭，改成 command: ["run", "--no-ssh-gateway"]
      environment:
        - RELAYMESH_AGENT_SERVER=https://relay.example.com
        # 服务端使用内置自签名证书时，填页面上显示的证书指纹：
        # - RELAYMESH_AGENT_PIN=sha256:<指纹>
        # 强烈建议：只允许连接这些目标（网段 / IP / 主机名，逗号分隔）
        - RELAYMESH_AGENT_SSH_ALLOW=10.0.0.0/8,192.168.0.0/16
        # 可选：外置凭据库的密封密钥（至少 16 个字符），不设则使用数据目录里自动生成的密钥文件
        # - RELAYMESH_AGENT_SSH_CRED_KEY=${GATEWAY_CRED_KEY}
      volumes:
        - gateway-data:/data   # 身份（机器码）与加密的凭据库都在这里：重建容器不会换机器，也不会丢凭据
  volumes:
    gateway-data:
  ```

  首次启动前先在 Web「接入主机」打开发现窗口，容器上线后核对机器码再点「启用」。容器使用内嵌会话模式（没有 systemd；会话随容器进程结束，所以请保持它常驻，服务端重启与浏览器刷新不受影响）。容器里同样可以开一个本机终端，那是容器自己的 shell，权限只限于容器之内。不设 `RELAYMESH_AGENT_SSH_ALLOW` 就等于不限制目标，请务必按实际网段填写。挂载宿主机目录代替命名卷时，请先把目录 `chown 10001:10001`。数据卷里的 `ssh-gateway/` 目录保存着登录凭据（密封）：备份卷时请连同密钥一起保管，或者用 `RELAYMESH_AGENT_SSH_CRED_KEY` 把密钥分开存放。
- **不想另外拉镜像**：服务端镜像里已经带着各平台的 Agent（`/app/agent-dist/relaymesh-agent-linux-amd64`，arm64 主机换成 `relaymesh-agent-linux-arm64`），把上面例子里的 `image` 换成 `teacat99/relaymesh:latest`，并加上 `entrypoint: ["/app/agent-dist/relaymesh-agent-linux-amd64"]`、`command: ["run", "--ssh-gateway"]`，数据目录改用 `RELAYMESH_AGENT_DATA_DIR=/app/data` 与卷 `gateway-data:/app/data` 即可，其余相同。

---

## 🔌 AI 客户端配置 (AI Client Setup)

### 1. Cursor IDE 配置
在项目根目录 `.cursor/mcp.json` 或全局配置 `~/.cursor/mcp.json` 中配置：

**原生 stdio 本地直启（⚡ 推荐，零配置免常驻，进程随 Cursor 启动/退出）：**

*方式 A：Go 开发者免克隆免安装（自动拉取最新版即时运行，类似 npx 模式）：*
```json
{
  "mcpServers": {
    "relaymesh": {
      "command": "go",
      "args": [
        "run",
        "github.com/teacat99/RelayMesh/v2/cmd/relaymesh@latest",
        "mcp",
        "stdio"
      ]
    }
  }
}
```

*方式 B：单二进制直启（开箱即用，零运行环境依赖，支持自定义主机名辨识）：*
```json
{
  "mcpServers": {
    "relaymesh": {
      "command": "/path/to/relaymesh",
      "args": [
        "mcp",
        "stdio"
      ],
      "env": {
        "RELAYMESH_HOST_NAME": "wsl"
      }
    }
  }
}
```

**本地常驻 HTTP 服务直连模式（适用于 Docker 或后台长期运行的本地服务）：**
```json
{
  "mcpServers": {
    "relaymesh": {
      "url": "http://localhost:18775/mcp"
    }
  }
}
```

> 🔑 下面示例里的 `<你的 MCP 令牌>` 就是部署时设置的 `RELAYMESH_MCP_TOKEN`（Docker 部署在 `.env` 里）。请填你自己生成的随机值，**不要使用任何文档里出现过的示例令牌**；能用请求头的地方优先用请求头——地址里的 `?token=` 会出现在访问日志与代理日志里。

**公网 URL 鉴权与主机名自报模式（推荐，多主机协同防混淆）：**
```json
{
  "mcpServers": {
    "relaymesh": {
      "url": "https://relaymesh.yourdomain.com/mcp?token=<你的 MCP 令牌>&hostname=wsl"
    }
  }
}
```
> 💡 **多主机辨识（Multi-host Recognition）**：
> - **HTTP 模式**：在 URL 后追加 `&hostname=wsl`（或 `?host_name=macbook`），客户端自报主机名享有最高优先级，Web 控制台将直观显示为 `wsl:/path/to/project`；
> - **stdio 模式**：在 `env` 属性中配置 `"RELAYMESH_HOST_NAME": "wsl"` 即可原生生效。

**公网 Header 鉴权模式（企业标准）：**
```json
{
  "mcpServers": {
    "relaymesh": {
      "url": "https://relaymesh.yourdomain.com/mcp",
      "headers": {
        "Authorization": "Bearer <你的 MCP 令牌>"
      }
    }
  }
}
```

---

### 2. Claude Desktop 配置
在 `claude_desktop_config.json` 中配置：

**原生 stdio 本地直启（⚡ 推荐，随 Claude Desktop 一键自启）：**
```json
{
  "mcpServers": {
    "relaymesh": {
      "command": "go",
      "args": [
        "run",
        "github.com/teacat99/RelayMesh/v2/cmd/relaymesh@latest",
        "mcp",
        "stdio"
      ]
    }
  }
}
```

**本地常驻 HTTP 服务直连模式：**
```json
{
  "mcpServers": {
    "relaymesh": {
      "url": "http://localhost:18775/mcp"
    }
  }
}
```

**公网 Token 鉴权模式（URL 嵌入）：**
```json
{
  "mcpServers": {
    "relaymesh": {
      "url": "https://relaymesh.yourdomain.com/mcp?token=<你的 MCP 令牌>"
    }
  }
}
```

---

### 3. VS Code Cline / Roo Code 配置
在 Cline MCP 设置中添加：
```json
{
  "mcpServers": {
    "relaymesh": {
      "name": "RelayMesh",
      "type": "streamable-http",
      "url": "https://relaymesh.yourdomain.com/mcp?token=<你的 MCP 令牌>"
    }
  }
}
```

---

## 🌐 Nginx / 1Panel 反向代理配置 (Reverse Proxy)

在公网生产环境部署时，建议通过 Nginx 或 1Panel 反向代理统一绑定域名与泛域名 SSL 证书。

### 方式零：Caddy（最省事：自动申请并续期证书，自动处理 SSE 与 WebSocket）

域名的 DNS 指向这台服务器、80 / 443 端口对外开放之后，一个文件就够了（让 RelayMesh 只监听本机：在 `.env` 里写 `RELAYMESH_PUBLISH_BIND=127.0.0.1`，再 `docker compose up -d`）：

```caddyfile
relaymesh.yourdomain.com {
	reverse_proxy 127.0.0.1:18775 {
		flush_interval -1
	}
}
```

`flush_interval -1` 关闭响应缓冲，SSE 实时事件即时推送。启动后访问 `https://relaymesh.yourdomain.com/`，MCP 地址是 `https://relaymesh.yourdomain.com/mcp`。Caddy 与 RelayMesh 在同一台机器（或私网内）时不需要额外设置；经公网 CDN 或其他主机上的反向代理访问时，请参考环境变量表里的 `RELAYMESH_TRUSTED_PROXIES`。

### 方式一：1Panel / 宝塔等面板反向代理

在面板中创建网站并配置反向代理后，修改 proxy 配置文件（如 `/www/sites/relaymesh.yourdomain.com/proxy/relaymesh.conf`），确保包含以下关键指令：

```nginx
location ^~ / {
    proxy_pass http://127.0.0.1:18775;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header REMOTE-HOST $remote_addr;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $http_connection;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Port $server_port;

    # SSE 实时流：禁用缓冲，确保事件即时推送
    proxy_buffering off;
    proxy_cache off;

    # MCP 长轮询：延长超时防止断连（3600s = 1 小时）
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
    proxy_connect_timeout 60s;

    proxy_ssl_server_name off;
    proxy_ssl_name $proxy_host;
}
```

### 方式二：手写 Nginx server 块配置

```nginx
server {
    listen 443 ssl http2;
    server_name relaymesh.yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:18775;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $http_connection;

        # SSE 实时流与 MCP 长轮询必须配置
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;

        client_max_body_size 50M;
    }
}
```

> **关键配置说明**：`proxy_buffering off` 是 SSE 实时事件流的必需项，否则 Nginx 缓冲会导致前端无法收到实时更新。`proxy_read_timeout 3600s` 防止 MCP 长轮询连接被 Nginx 默认 60s 超时切断。

> **远程终端的 WebSocket**：Web 终端使用 `/api/v1/terminal/ws`（浏览器）与 `/api/v1/terminal/agent/ws`（Agent）两个 WebSocket 端点，上面配置里的 `proxy_http_version 1.1`、`Upgrade` / `Connection` 头必须保留（1Panel / OpenResty 同理）；两端都每 15 秒发送一次心跳，不会被空闲超时切断。浏览器握手要求 `Origin` 与请求 `Host` 同源，所以反向代理必须透传 `Host $host`（上面的配置已包含）；若无法透传，请用 `RELAYMESH_ALLOWED_ORIGINS=https://relaymesh.yourdomain.com` 显式追加允许的来源。

---

## ⚙️ 核心环境变量清单 (Environment Variables)

| 环境变量名 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `RELAYMESH_HOST` | 字符串 | `127.0.0.1` | HTTP 服务监听 IP（容器内使用 `0.0.0.0`） |
| `RELAYMESH_PORT` | 整数 | `18775` | HTTP 服务与 MCP 端口 |
| `RELAYMESH_HTTPS_PORT` | 整数 | `18776` | HTTPS 原生安全端口（用于浏览器麦克风录音） |
| `RELAYMESH_HOST_NAME` | 字符串 | 空 | 主机名标识（如 `wsl`, `macbook`, `dev-server`），用于多机环境区分 |
| `RELAYMESH_DB_PATH` | 字符串 | `data/relaymesh.db` | SQLite 数据库文件持久化路径。**默认是相对「当前工作目录」的路径**（Docker 镜像内为 `/app/data/relaymesh.db`）；常驻部署请写绝对路径 |
| `RELAYMESH_PROJECT_ID` | 字符串 | `default` | 默认项目隔离标识 |
| `RELAYMESH_WEB_USERNAME` | 字符串 | `admin` | Web 控制台管理员账号 |
| `RELAYMESH_WEB_PASSWORD` | 字符串 | 空 (免密) | Web 控制台密码（配置后强制开启 JWT 登录鉴权） |
| `RELAYMESH_JWT_SECRET` | 字符串 | 空 (自动生成) | JWT 签名密钥。留空时首次启动自动生成 384 bit 随机密钥并持久化到数据库；显式设置时须为不少于 32 字符的随机串，内置默认值、示例占位符与低熵字符串会被忽略并改用自动生成的密钥 |
| `RELAYMESH_MCP_TOKEN` | 字符串 | 空 (免鉴权) | 全局 MCP 访问 Token（配置后 MCP 请求需携带 Token） |
| `RELAYMESH_NTFY_TOKEN` | 字符串 | 空 | ntfy 推送的访问令牌（`tk_…`）。设置后优先于设置页里保存的令牌，界面提示「由环境变量提供」；适合不想把令牌放进数据库的部署。其余 ntfy 配置在设置页管理，见 [docs/notifications.md](docs/notifications.md) |
| `RELAYMESH_INBOX` | 字符串 | 空 | 追加指令收件箱的部署层开关：设为 `off` 强制关闭全部提醒（回执提示行、hook、提示词条款），界面提示「由环境变量关闭」；已排队的追加不会丢，智能体发起交互时照常带走。其余开关在设置 → 等待策略里管理，见 [docs/inbox.md](docs/inbox.md) |
| `RELAYMESH_TRUSTED_PROXIES` | 字符串 | 空 (回环 + 私网) | 可信反向代理网段：仅当直连对端在此范围内，才采信其 `X-Forwarded-For` / `X-Real-IP` 作为客户端 IP（登录反爆破据此计数）。支持逗号分隔的 IP / CIDR，`default` 表示回环 + 私网，`none` 表示不信任任何代理；经公网 CDN 等非私网反代时需显式填写其出口网段 |
| `RELAYMESH_TLS_ENABLED` | 布尔值 | `true` | 是否启用内置零依赖 HTTPS 自签名 TLS 引擎 |
| `RELAYMESH_TLS_CERT` / `RELAYMESH_TLS_KEY` | 字符串 | `data/certs/cert.pem` / `data/certs/key.pem` | 自签名证书与私钥的位置（不存在时自动生成；同样是相对工作目录的路径，Docker 镜像内为 `/app/data/certs/…`）。证书指纹要在重启后保持不变（Agent 用它校验服务端），所以常驻部署请写在持久目录里 |
| `RELAYMESH_OPEN_BROWSER` | 布尔值 | 桌面环境 `true` | 服务启动时是否自动打开浏览器；容器与无头服务器始终静默跳过，设为 `false` 可在桌面环境关闭 |
| `RELAYMESH_PUBLIC_URL` | 字符串 | 空 | 对外根 URL（如 `https://relaymesh.example.com`），OAuth 的 `issuer` 与受保护资源地址用它；生产环境使用 OAuth 连接器时请设置，见 [docs/oauth.md](docs/oauth.md) |
| `RELAYMESH_OAUTH` | 字符串 | 空 | 设为 `off` 硬关闭全部 OAuth 端点（`/.well-known/*`、`/oauth/*`），见 [docs/oauth.md](docs/oauth.md) |
| `RELAYMESH_TERMINAL` | 字符串 | `auto` | 远程终端总开关：`auto` 表示「已设置 Web 密码且 JWT 密钥足够强」时才启用；`off` 强制关闭（只能关闭，不能绕过安全前置条件强行开启） |
| `RELAYMESH_TERMINAL_EXEC` | 字符串 | 空（关闭） | 模型执行（`terminal_targets` / `terminal_exec` / `terminal_run`）的服务端总开关：设为 `on` 才启用；未设置或其他取值一律关闭。只在终端功能本身可用（已设置 Web 密码且 JWT 密钥足够强）时才生效；关闭时三个工具直接返回 `exec_disabled` 且不写审计 |
| `RELAYMESH_TERMINAL_FILES` | 字符串 | 空（关闭） | 远程文件（`terminal_files`：`stat` / `hash` / `pull_prepare` / `push_url`）的服务端开关：设为 `on` 才启用，并且需要同时设置 `RELAYMESH_TERMINAL_EXEC=on`；令牌还必须带「终端文件」权限和允许访问的目录。关闭时工具返回 `files_disabled`，下载端点 `/files/dl/*` 一律 404；无法识别的取值保持关闭并在日志里警告 |
| `RELAYMESH_SSH_SECRET_KEY` | 字符串 | 空（SSH 后端关闭） | SSH 主机的凭据加密密钥：登记 SSH 主机时填写的口令 / 私钥用它加密保存（AES-256-GCM，并绑定到主机机器码）。必须是足够长的随机值（规则同 MCP 令牌，弱值视为未设置）；独立于 `RELAYMESH_JWT_SECRET`。未设置或太弱时 SSH 后端整体关闭，已登记的 SSH 主机显示「后端未启用」；更换后已保存的凭据无法解开，需要重新填写 |
| `RELAYMESH_TERMINAL_DISCOVERY` | 字符串 | `window` | 新主机发现模式：`window` 由管理员在 Web 上手动开启限时发现窗口；`always` 常开（不建议暴露在公网） |
| `RELAYMESH_AGENT_DIST_DIR` | 字符串 | 空（自动探测） | Agent 各平台二进制（`relaymesh-agent-<os>-<arch>[.exe]`）的分发目录，Web 生成的安装命令据此改为从服务端下载。留空时依次探测可执行文件旁的 `agent-dist/`、容器内 `/app/agent-dist`、当前目录的 `agent-dist/`（Docker 镜像已内置）；都没有时界面回落为 GitHub Release 来源的命令。Cursor hook 程序（`relaymesh-hook-<os>-<arch>[.exe]`）也从这个目录出；没有时 hook 的一键安装脚本自动回退到同版本的 GitHub Release（详见 `docs/inbox.md`「下载来源与回退」）。显式设置的目录即使不存在也按配置走 |
| `RELAYMESH_TERMINAL_RECORD` | 字符串 | `auto` | 终端记录的服务端总开关：`auto` 表示各主机按本机与主机级策略记录；`off` 全局关闭（下发给每台主机的 Agent，只能让记录更严格）；无法识别的取值按 `off` 处理并在日志里说明 |
| `RELAYMESH_TERMINAL_RECORD_DAYS` | 整数 | `0` | 记录保留天数上限（`0` 表示不额外限制，主机本机默认 30 天）；只能比主机本机配置更严格 |
| `RELAYMESH_TERMINAL_RECORD_SESSION_MB` | 整数 | `0` | 单会话记录大小上限，MiB（`0` 表示不额外限制，主机本机默认 64） |
| `RELAYMESH_TERMINAL_RECORD_HOST_MB` | 整数 | `0` | 单台主机记录总量上限，MiB（`0` 表示不额外限制，主机本机默认 512） |
| `RELAYMESH_ALLOWED_ORIGINS` | 字符串 | 空 | 终端浏览器 WebSocket 除「同源」外额外允许的 Origin，逗号分隔，须含协议与端口（如 `https://relaymesh.example.com`）；通常只在反向代理无法透传 `Host` 时需要 |
| `RELAYMESH_CSP` | 字符串 | `enforce` | 网页内容安全策略（CSP）：`enforce` 强制、`report` 仅上报不拦截、`off` 关闭 |
| `RELAYMESH_ALLOW_QUERY_TOKEN` | 布尔值 | `true` | SSE 端点是否仍接受 URL 上的 `?token=`（兼容旧版前端页面）；旧页面都已刷新后，生产环境建议设为 `false`。该开关只管网页登录令牌走 SSE 的写法，**不影响 `/mcp` 的 `?token=` 纯 URL 写法**（为只能填网址的特殊环境长期保留） |
| `RELAYMESH_ASR_API_URL` | 字符串 | MIMO 官方端点 | 语音识别 ASR 服务接口地址 |
| `RELAYMESH_ASR_API_KEY` | 字符串 | 空 | 语音识别 ASR 认证密钥 |

> Docker Compose 部署时，宿主机侧的 `RELAYMESH_PUBLISH_*`、`RELAYMESH_TAG` 是 compose 文件自己的变量（见上文「模式三」与 `docker-compose.yml` 顶部注释），不是服务端读取的环境变量。

### 进阶、调试与 Agent 变量

下面这些变量多数部署用不到，列在这里是为了让代码里读得到的开关，在文档里都查得到。默认值都是保守的（关闭、留空或最小权限）。

**服务端：自驾任务、等待与语音**

| 环境变量名 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `RELAYMESH_CONFIGURE_TOKEN` | 字符串 | 空 | 只带「任务指挥」权限位（`configure_task`）的环境变量令牌，给自驾（autopilot）模式里的指挥官一侧用；放在请求头 `Authorization: Bearer <令牌>`，日志与审计里显示为 `env:configure`。不能执行远程命令，权限位说明见 [docs/credentials-and-permissions.md](docs/credentials-and-permissions.md) |
| `RELAYMESH_EXECUTION_TOKEN` | 字符串 | 空 | 只带「自驾上报」权限位（`report_progress`）的环境变量令牌，给自驾模式里的执行一侧用，显示为 `env:execute`。这里的「执行」指执行自驾任务，与在远程主机上执行命令无关 |
| `RELAYMESH_FEEDBACK_TIMEOUT_SECONDS` | 整数 | `120` | 一次 `interactive_feedback` / `continue_feedback_session` 挂起等待用户回复的默认秒数。实际时长以设置里的「挂起倒计时」为准，它优先于本变量；填 1～5 会被当作显式的短等待强制采用（只用于测试，把挂起缩到秒级） |
| `RELAYMESH_WAIT_AFTER_MINUTES` | 整数 | `5` | 新自驾任务默认的等待分钟数（回执里「请等待 N 分钟」的 N）；创建任务时可在 `configure_task` 里逐个覆盖 |
| `RELAYMESH_MAX_NO_FEEDBACK_CHECKS` | 整数 | `24` | 新自驾任务默认允许的「无新反馈」检查次数上限（写进任务的等待状态） |
| `RELAYMESH_WAIT_INSTRUCTION` | 字符串 | `请等待 {minutes} 分钟后再次调用 MCP 获取反馈。` | 新自驾任务默认的等待提示语，`{minutes}` 会换成等待分钟数 |
| `RELAYMESH_WAIT_EXHAUSTED_INSTRUCTION` | 字符串 | `本次等待窗口已结束；暂无新反馈。如需继续推进，可提交新的 progress 或 question。` | 新自驾任务默认的「等待窗口已结束」提示语，写进任务的等待策略，可被 `configure_task` 逐个覆盖 |
| `RELAYMESH_ASR_MODEL` | 字符串 | `mimo-v2.5-asr` | 语音识别请求使用的模型名；浏览器那一次请求没有指定模型时采用 |

**服务端：调试（生产不要设）**

| 环境变量名 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `RELAYMESH_DEV` | 布尔值 | `false` | 开发模式：设为 `true` 时 Web 框架（Gin）以 debug 模式运行，启动时打印路由，每个请求都有详细日志 |
| `RELAYMESH_SQL_TRACE` | 字符串 | 空（关闭） | 设为一个文件路径后，服务端执行的每条 SQL 都会以一行 JSON（时间、耗时、行数、报错、SQL 原文）追加写入该文件，用于性能排查。文件里有真实数据的片段，只在临时库上用，用完删除 |

**Agent（与命令行选项一一对应；命令行里显式给出的值优先。布尔值认 `1` / `true` / `yes` / `on`）**

| 环境变量名 | 对应选项 | 默认值 | 说明 |
|---|---|---|---|
| `RELAYMESH_AGENT_INSECURE` | `--insecure` | 关闭 | 允许 Agent 用明文 `ws://` 连接服务端，只适合本机或受信内网 |
| `RELAYMESH_AGENT_TEMP` | `--temp` | 关闭 | 用临时身份运行（不留痕、不录制、不支持模型执行）；命令行的 `--persist` 会压过它 |
| `RELAYMESH_AGENT_CONFIG_DIR` | `--config-dir` | 按系统与权限自动选择 | 服务配置与已信任服务器指纹（`known_servers`）的目录。默认：Linux 管理员 `/etc/relaymesh-agent`，普通用户 `$XDG_CONFIG_HOME` 或 `~/.config` 下的 `relaymesh-agent`；macOS 与 Windows 与数据目录相同 |
| `RELAYMESH_AGENT_LOG_FILE` | `--log-file` | 空（日志写到标准错误） | 把日志写入该文件（自动轮转），用于没有控制台的后台运行，例如 Windows 计划任务 |
| `RELAYMESH_AGENT_HOLDER` | `--holder` | `auto` | 终端会话由谁持有：`auto` 有会话守护进程就交给它、否则内嵌；`external` 只用守护进程；`embedded` 与 Agent 同进程，Agent 重启时会话不保留 |
| `RELAYMESH_AGENT_HOLDER_SOCKET` | `--holder-socket` | 数据目录下 | 会话守护进程的本地套接字路径 |

> `RELAYMESH_AGENT_SERVER`、`RELAYMESH_AGENT_PIN`、`RELAYMESH_AGENT_DATA_DIR`、`RELAYMESH_AGENT_DENY_MODEL_EXEC` 等其余 Agent 变量见上文「远程 Web 终端与通用 Agent」一节与 [docs/terminal-operations.md](docs/terminal-operations.md)。

---

## 📦 Docker 镜像发布与 CI/CD 自动化 (Docker Publishing)

### 方式一：GitHub Actions 自动构建与发布（推荐）
项目已内置 `.github/workflows/docker-publish.yml`，当推送 Tag（如 `v2.0.0`）或合并至 `main` 分支时，将自动通过 GitHub Actions 执行多架构（`linux/amd64`, `linux/arm64`）构建，并推送到：
- **Docker Hub**：`teacat99/relaymesh:latest`
- **GitHub Container Registry (GHCR)**：`ghcr.io/<owner>/relaymesh:latest`

> 仅需在 GitHub 仓库的 **Settings -> Secrets and variables -> Actions** 中配置 `DOCKERHUB_USERNAME` 与 `DOCKERHUB_TOKEN`。

---

### 方式二：本地一键发布脚本 (Multi-arch Buildx)
使用项目内置脚本执行多架构构建与推送：

```bash
# 1. 登录 Docker Hub
docker login

# 2. 一键构建并推送多架构镜像（默认 amd64 + arm64）
./scripts/publish-docker.sh v2.0.0
```

---

## 🛠️ 运维与命令行工具 (CLI Tools)

RelayMesh 单二进制内置标准 CLI 常用命令：

```bash
# 1. 默认无参或显式 serve：启动 HTTP/HTTPS 后台服务与 Web 控制台
./relaymesh
./relaymesh serve

# 2. 原生 stdio MCP 模式：直接作为 MCP 客户端的子进程运行
./relaymesh mcp stdio

# 3. 重置 Web 访问账号密码为环境变量默认值（清除数据库中的在线修改记录）
./relaymesh reset-auth
docker compose exec relaymesh /app/relaymesh reset-auth   # Docker 部署（想换新密码：先改 .env 并 docker compose up -d，再执行）

# 4. 项目换了目录 / 搬到另一台机器后，让旧工作流跟着走（默认只预演，详见 docs/project-migration.md）
./relaymesh rebind-workflow --list
./relaymesh rebind-workflow --workflow <工作流 ID> --to-dir /new/project/dir --to-host <新主机名> --apply

# 5. 查看当前版本
./relaymesh version
```

---

## 🧑‍💻 本地开发指南 (Development)

```bash
# 1. 启动前端 Vite 开发热重载服务器 (端口 5173，自动反代 18775 后端)
pnpm --dir frontend run dev

# 2. 启动 Go 后端单二进制服务 (端口 18775 / 18776)
go run ./cmd/relaymesh

# 3. 运行全量单元测试
go test ./...
```

---

## 📄 开源许可证 (License)

本项目遵循 [Apache License 2.0](LICENSE) 开源协议。欢迎个人使用、企业二次开发与商用，保留原作者商标与专利反制权益。
