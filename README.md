# RelayMesh ⚡

<p align="center">
  <strong>高可靠 AI Agent 与人类协同交互中继网关</strong><br>
  <em>High-Reliability Human-in-the-Loop & Agentic Relay Hub for Modern AI Development</em>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.24%2B%20%7C%201.25-00ADD8?style=flat-square&logo=go" alt="Go Version" />
  <img src="https://img.shields.io/badge/Vue-3.5%2B-4FC08D?style=flat-square&logo=vue.js" alt="Vue Version" />
  <img src="https://img.shields.io/badge/TailwindCSS-v4.0-38B2AC?style=flat-square&logo=tailwind-css" alt="Tailwind CSS" />
  <img src="https://img.shields.io/badge/Protocol-Model%20Context%20Protocol%20(MCP)-blueviolet?style=flat-square" alt="MCP Protocol" />
  <img src="https://img.shields.io/badge/Architecture-Single%20Binary%20%2B%20embed.FS-0052CC?style=flat-square" alt="Single Binary" />
  <img src="https://img.shields.io/badge/Memory-~6.4MB-success?style=flat-square" alt="Memory" />
  <img src="https://img.shields.io/badge/License-Apache_2.0-blue?style=flat-square" alt="License" />
</p>

---

## 📖 项目简介 (Introduction)

**RelayMesh** 是一款专为 AI 辅助编程与自动化 Agent 打造的高性能、高可靠**人机协同交互与状态中继网关**。

在长周期开发与自动化任务中，AI Agent（如 Cursor、Claude Desktop、VS Code、Codex）常面临**上下文压缩丢失决策**、**等待人工确认时频繁超时报错 (`-32001`)**、**Web 界面简陋难用**、以及**公网部署缺乏零信任安全**等痛点。

RelayMesh 基于 **Go 1.25** 与 **Vue 3.5** 纯原生重构，支持 **原生 stdio 本地直启（随 AI 客户端自启退出、零配置开箱即用）** 与 **Streamable HTTP MCP** 双 Transport 架构，提供沉浸式桌面级 Web 控制台、毫秒级 SSE 实时事件流与单二进制内嵌托管，常驻内存仅 **~6.4MB**，兼顾本地极简免配置运行与公网高安全生产部署。

---

## ✨ 核心特性 (Key Features)

### ⚡ 0. 原生本地 stdio 直启与双 Transport 架构 (New in v1.2.0)
- **单一配置开箱即用**：无需克隆仓库、无需配置 Token、无需手动启动后台守护，Cursor / Claude 配置文件一条 `go run ...@latest mcp stdio` 即可即时运行；
- **双 Transport 共享内核**：stdio 与 HTTP 双 Transport 共享同一个状态存储、同一个 `mcp.Server` 实例，stdio 运行期间自动伴随本地 Web 控制台；
- **工作区绝对零污染**：stdio 模式下数据库与自签名证书自动存放于 OS 标准用户目录（Linux: `~/.local/share/relaymesh`；macOS: `Application Support`；Windows: `%LOCALAPPDATA%`），绝不向代码项目目录写入任何临时文件；
- **全生命周期平滑管理**：客户端关闭 `stdin` 时平滑退出，端口冲突自动 Fail Fast 友好提示，stdout 严格独占 MCP JSON-RPC 消息流。

### 🤖 1. 标准 Streamable HTTP MCP 协议中枢
- **双角色无缝支持**：
  - **人机交互角色 (Human Feedback)**：`interactive_feedback`、`continue_feedback_session`、`get_session_image`（新增：协议级原生图片看图与提取工具）、`get_session_history`、`list_sessions`；
  - **任务编排与执行角色 (Task Orchestrator)**：`configure_task`、`report_progress`（支持增量同步 `sync`、结构化报告 `report` 与非阻塞检查 `check_feedback`）。
- **根治客户端超时机制**：服务端精细化 Long-Polling（40s 挂起上限）配合单调递增因果游标，彻底告别 `-32001 Request timed out`。
- **秒回直取与提前暂存**：用户可在 AI 运行期间随时提前追加意见或暂存留言，AI 再次接入时 **0ms 毫秒级直取**。

### 💻 2. 沉浸式现代 Web 交互控制台
- **极简工程美学**：基于 Tailwind CSS v4 与 Radix Vue 打造，纯单页沉浸式工作区，支持 **跟随系统 💻 / 浅色 ☀️ / 深色 🌙** 3 态循环切换。
- **全通侧栏与工作流聚合**：统一以 `workflow_id` 进行多轮会话折叠聚合，支持全文检索与类型过滤，支持 PC 端侧边栏平滑折叠展开。
- **悬浮操作与底部避让**：毛玻璃悬浮反馈提交栏，支持高度自由拖拽与记忆、多行快捷预设标签与状态联动。

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
│  │                     Go 1.25 单二进制服务内核 (18775/18776)            │  │
│  │  - Streamable HTTP MCP Server    - 内存级反爆破限流器 (Lockout)         │  │
│  │  - SSE 实时事件广播 Broker         - 零依赖 TLS 证书引擎 (18776)         │  │
│  │  - RESTful 业务状态与草稿 API     - embed.FS 纯静态网页内嵌托管         │  │
│  └───────────────────────────────────┬───────────────────────────────────┘  │
│                                      │ 持久化读写                           │
│                                      ▼                                      │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                   纯 Go SQLite 数据库引擎 (无 CGO 依赖)               │  │
│  │    sessions  /  tasks  /  workflow_drafts  /  system_settings         │  │
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

Go 1.24+ 开发者无需手动克隆仓库或配置 Node 环境，直接通过 Go 命令行一键拉取并即时运行内嵌完整 Web 控制台的单二进制：

```bash
# 1. 直接运行最新版 HTTP 服务（自动下载、缓存并运行，类似 npx / uvx 模式）
go run github.com/teacat99/RelayMesh/cmd/relaymesh@latest

# 2. 或直接运行原生 stdio MCP 模式（为 Cursor / Claude 等 MCP Host 提供直启端点）
go run github.com/teacat99/RelayMesh/cmd/relaymesh@latest mcp stdio

# 3. 或全局安装到 $GOPATH/bin 作为系统 CLI 工具使用
go install github.com/teacat99/RelayMesh/cmd/relaymesh@latest
relaymesh            # 启动 HTTP/HTTPS 常驻服务与 Web 控制台
relaymesh mcp stdio  # 启动原生 stdio MCP 服务
```
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

---

### 模式三：Docker / Docker Compose 容器化部署（🐳 多架构支持，生产与常驻推荐）

官方 Docker 镜像发布于 Docker Hub（`teacat99/relaymesh:latest`），原生支持 `linux/amd64` 与 `linux/arm64`。

#### 1. 本地免密直连模式（本地开发与内网常驻预览推荐）
创建 `docker-compose.yml`：
```yaml
services:
  relaymesh:
    image: teacat99/relaymesh:latest
    container_name: relaymesh
    restart: unless-stopped
    ports:
      - "18775:18775"
      - "18776:18776"
    environment:
      - RELAYMESH_HOST=0.0.0.0
      - RELAYMESH_PORT=18775
      - RELAYMESH_HTTPS_PORT=18776
      - RELAYMESH_DB_PATH=/app/data/relaymesh.db
      - RELAYMESH_PROJECT_ID=default
      - RELAYMESH_ALLOW_NON_LOOPBACK=true
      - GIN_MODE=release
    volumes:
      - ./data:/app/data
```
启动容器：
```bash
docker compose up -d
```
- **HTTP 访问与 MCP 端点**：`http://localhost:18775/`
- **HTTPS 麦克风录音访问**：`https://localhost:18776/`

---

#### 2. 公网生产安全模式（云服务器与反向代理推荐）
创建 `docker-compose.prod.yml`：
```yaml
services:
  relaymesh:
    image: teacat99/relaymesh:latest
    container_name: relaymesh-prod
    restart: unless-stopped
    user: "0:0"  # 以 root 运行，避免 bind mount 权限问题（Alpine 最小镜像 + 单二进制，安全风险极低）
    ports:
      - "127.0.0.1:18775:18775"
    environment:
      - RELAYMESH_HOST=0.0.0.0
      - RELAYMESH_PORT=18775
      - RELAYMESH_HOST_NAME=prod-relayhub
      - RELAYMESH_DB_PATH=/app/data/relaymesh.db
      - RELAYMESH_PROJECT_ID=default
      # Web 控制台管理员账号与访问强密码
      - RELAYMESH_WEB_USERNAME=admin
      - RELAYMESH_WEB_PASSWORD=YourStrongWebPassword2026!
      - RELAYMESH_JWT_SECRET=YourRandomJWTSecretKeyAtLeast32CharsLong!
      # 全局 MCP 访问 Token
      - RELAYMESH_MCP_TOKEN=rmk_prod_secret_token_8888
      - RELAYMESH_ALLOW_NON_LOOPBACK=true
      - GIN_MODE=release
    volumes:
      - ./data:/app/data
```
启动生产容器：
```bash
docker compose -f docker-compose.prod.yml up -d
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
        "github.com/teacat99/RelayMesh/cmd/relaymesh@latest",
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

**公网 URL 鉴权与主机名自报模式（推荐，多主机协同防混淆）：**
```json
{
  "mcpServers": {
    "relaymesh": {
      "url": "https://relaymesh.yourdomain.com/mcp?token=rmk_prod_secret_token_8888&hostname=wsl"
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
        "Authorization": "Bearer rmk_prod_secret_token_8888"
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
        "github.com/teacat99/RelayMesh/cmd/relaymesh@latest",
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
      "url": "https://relaymesh.yourdomain.com/mcp?token=rmk_prod_secret_token_8888"
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
      "url": "https://relaymesh.yourdomain.com/mcp?token=rmk_prod_secret_token_8888"
    }
  }
}
```

---

## 🌐 Nginx / 1Panel 反向代理配置 (Reverse Proxy)

在公网生产环境部署时，建议通过 Nginx 或 1Panel 反向代理统一绑定域名与泛域名 SSL 证书。

### 方式一：1Panel / 宝塔等面板反向代理（推荐）

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

---

## ⚙️ 核心环境变量清单 (Environment Variables)

| 环境变量名 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `RELAYMESH_HOST` | 字符串 | `127.0.0.1` | HTTP 服务监听 IP（容器内使用 `0.0.0.0`） |
| `RELAYMESH_PORT` | 整数 | `18775` | HTTP 服务与 MCP 端口 |
| `RELAYMESH_HTTPS_PORT` | 整数 | `18776` | HTTPS 原生安全端口（用于浏览器麦克风录音） |
| `RELAYMESH_HOST_NAME` | 字符串 | 空 | 主机名标识（如 `wsl`, `macbook`, `dev-server`），用于多机环境区分 |
| `RELAYMESH_DB_PATH` | 字符串 | `data/relaymesh.db` | SQLite 数据库文件持久化路径 |
| `RELAYMESH_PROJECT_ID` | 字符串 | `default` | 默认项目隔离标识 |
| `RELAYMESH_WEB_USERNAME` | 字符串 | `admin` | Web 控制台管理员账号 |
| `RELAYMESH_WEB_PASSWORD` | 字符串 | 空 (免密) | Web 控制台密码（配置后强制开启 JWT 登录鉴权） |
| `RELAYMESH_JWT_SECRET` | 字符串 | 内置默认 | JWT 签名密钥（生产环境必须修改为随机长字符串） |
| `RELAYMESH_MCP_TOKEN` | 字符串 | 空 (免鉴权) | 全局 MCP 访问 Token（配置后 MCP 请求需携带 Token） |
| `RELAYMESH_TLS_ENABLED` | 布尔值 | `true` | 是否启用内置零依赖 HTTPS 自签名 TLS 引擎 |
| `RELAYMESH_ASR_API_URL` | 字符串 | MIMO 官方端点 | 语音识别 ASR 服务接口地址 |
| `RELAYMESH_ASR_API_KEY` | 字符串 | 空 | 语音识别 ASR 认证密钥 |

---

## 📦 Docker 镜像发布与 CI/CD 自动化 (Docker Publishing)

### 方式一：GitHub Actions 自动构建与发布（推荐）
项目已内置 `.github/workflows/docker-publish.yml`，当推送 Tag（如 `v1.0.0`）或合并至 `main` 分支时，将自动通过 GitHub Actions 执行多架构（`linux/amd64`, `linux/arm64`）构建，并推送到：
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
./scripts/publish-docker.sh v1.0.0
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

# 4. 查看当前版本
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
