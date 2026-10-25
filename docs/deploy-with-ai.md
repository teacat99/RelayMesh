# 让 AI 助手帮你部署 RelayMesh

这份文档有两种读法：

- **你是使用者**：直接看「一、复制提示词」，把提示词交给你的 AI 助手（Cursor、Claude Code、Codex、Cline 等能在终端里执行命令的助手都行）。它会先做只读的环境检查，给你一份部署计划，**等你点头之后**才动手，部署完逐项验证，最后告诉你访问地址、口令放在哪里、怎么回滚。
- **你是 AI 助手**：从「二、执行协议」开始读，按顺序执行，每一步都做验证，不要跳步。

> 命令都在 Linux 上实际跑过：Docker Compose 的安装 / 升级 / 备份 / 回滚 / 恢复、一键安装脚本、systemd 常驻、Caddy 反向代理、stdio 直启。macOS 与 Windows 与 Linux 的差异在对应步骤旁单独标出；本指南的命令一律是 POSIX shell 写法。

---

## 一、复制提示词（给使用者）

把下面整段复制给你的 AI 助手，改掉方括号里的内容（不确定的就留着，AI 会来问你）：

```text
请帮我部署 RelayMesh（AI 与人类协同的中继网关）。请先完整阅读这份部署指南，再开始动手：
https://raw.githubusercontent.com/teacat99/RelayMesh/main/docs/deploy-with-ai.md

我的情况（没写的项，请按指南里的默认值，或者来问我）：
- 部署在哪里：[这台电脑 / 远程服务器（SSH：user@host）/ NAS]
- 谁来访问：[只有这台机器 / 局域网里的手机和同事 / 公网域名（例如 https://relay.example.com）]
- 要不要用 Web 终端连接我的其他主机：[不要 / 要]

要求：
1. 先做只读的环境检查，把部署计划发给我，我确认之后再执行；
2. 口令和令牌自动生成并保存到文件里，不要贴在对话里；
3. 不要杀掉任何已有进程、不要覆盖已有数据目录；端口被占用就换端口；
4. 部署完成后告诉我：访问地址、口令保存位置、验证结果、升级与回滚的命令。
```

---

## 二、执行协议（给 AI 助手）

### 0. 硬规则（先读完再动手）

1. **先问、先计划、再执行。** 收集信息（第 1 节）→ 只读检查（第 2 节）→ 把计划发给用户 → **得到明确的「可以」之后**，才执行会改变系统的命令（安装软件、写系统目录、开放端口、启动服务）。
2. **口令与令牌不进对话。** 一律随机生成，写进 `.env`（Docker）或 `/etc/relaymesh/relaymesh.env`（systemd），文件权限 `600`。不要把口令、令牌打印出来、写进日志、提交进 Git；交付时只告诉用户文件在哪里。用 `curl -v`、`set -x`、回显配置文件之类会泄露它们的做法也要避开。
3. **口令必设；没设口令就绝不对外。** Compose 模板不写 `RELAYMESH_PUBLISH_BIND` 时**默认监听所有网卡**（同一网络里的人都能打开登录页，云服务器上就是整个互联网），所以：Web 密码与 MCP 令牌一律先生成好（路径 A 的 A2 已经包含）；监听范围按第 1 节问题 2 的答案**显式写进 `.env`**——「只有这台机器」写 `127.0.0.1`，「局域网」写 `0.0.0.0`；「免登录 + 所有网卡」是绝对不能交付的组合。要给公网用，必须放在 HTTPS 反向代理后面（第 7 节）并写 `127.0.0.1`，**不要**把 18775 / 18776 直接暴露到公网。
4. **不碰已有的东西。** 端口被占用就换端口，不要杀别人的进程；目标目录里已经有 `data/` 或 `.env` 时先问用户，不要覆盖；不要修改防火墙 / 安全组，需要开放端口时告诉用户该开哪个端口。
5. **每步做验证。** 失败了先看第 11 节的速查表，不要原样重试同一条命令；两次修不好就停下来向用户汇报现象和日志。
6. **破坏性操作必须用户明确要求。** 卸载、删除数据目录、`docker system prune`、重置密码、覆盖备份……一律先问。
7. **文档没覆盖的情况不要猜。** 去读 [README](../README.md) 对应章节；仍不确定就问用户。

### 1. 收集信息（最多问这 4 个问题；用户已经说过的不要重复问）

| # | 问题 | 没有答案时的默认值 |
|---|---|---|
| 1 | 部署在哪里：这台电脑，还是远程服务器（需要 SSH 信息）？ | 这台电脑 |
| 2 | 谁来访问：只有这台机器 / 局域网（手机、同事）/ 公网域名？（决定监听范围：前者与公网 → `127.0.0.1`，局域网 → `0.0.0.0`） | 只有这台机器 |
| 3 | 用什么 AI 客户端连接（Cursor / Claude Desktop / Cline / 其他）？ | 用户当前正在用的这个 |
| 4 | 要不要用 Web 终端连接其他主机（需要设置 Web 密码，之后再装 Agent）？ | 不要，以后可以再加 |

### 2. 前置检查（全是只读命令，可以直接执行）

```bash
uname -sm                                    # 系统与架构：Linux x86_64 / aarch64，Darwin arm64 / x86_64
docker --version && docker compose version   # 有 Docker 就优先走路径 A
go version                                   # 路径 C / D 的 go run 方式需要 Go 1.26+
curl --version | head -1
openssl version                              # 生成随机口令用；没有的话见第 11 节
df -h . | tail -1                            # 至少留 200 MB
ss -ltn | grep -E ':(18775|18776)\b' || echo "18775 / 18776 空闲"   # macOS 没有 ss：lsof -iTCP:18775 -sTCP:LISTEN
```

要点：

- 18775 / 18776 被占用：Docker 路径在 `.env` 里改 `RELAYMESH_PUBLISH_HTTP_PORT` / `RELAYMESH_PUBLISH_HTTPS_PORT`；二进制路径设置 `RELAYMESH_PORT` / `RELAYMESH_HTTPS_PORT`。**不要杀占用端口的进程。**
- `docker ps` 报 `permission denied`：当前用户不在 `docker` 组。告诉用户，征得同意后用 `sudo` 或把用户加进 `docker` 组，不要自行改权限。
- 把检查结果和打算走的路径整理成一份简短的计划发给用户，等确认。

### 3. 选路径

| 条件 | 路径 |
|---|---|
| 要常驻（服务器 / NAS / 一直开着的电脑），有 Docker | **A. Docker Compose（推荐）** |
| 要常驻，Linux，没有 Docker | **B. 单二进制 + systemd** |
| 只在这台电脑上配合 Cursor / Claude Desktop 用 | **C. 本机直启（stdio）** |
| 只想临时看一眼 | **D. 前台临时运行** |

macOS / Windows 要常驻：优先用 Docker Desktop 走路径 A（Windows 请在 WSL2 终端里操作）。原生 Windows 想直接运行，请从 [Releases](https://github.com/teacat99/RelayMesh/releases) 下载 `relaymesh-windows-amd64.exe` 在 PowerShell 里前台运行。

---

### 4. 路径 A：Docker Compose（推荐）

```bash
# A1 目录与 compose 文件（目录可以按用户要求换；已存在 .env 或 data/ 时先问用户）
mkdir -p ~/relaymesh && cd ~/relaymesh
curl -fsSLO https://raw.githubusercontent.com/teacat99/RelayMesh/main/docker-compose.yml

# A2 生成口令与令牌（命令里不会打印它们），写进 .env
# 监听范围按第 1 节问题 2 的答案选：只有这台机器 / 公网域名 → 127.0.0.1；局域网（手机、同事）→ 0.0.0.0
BIND=127.0.0.1
cat > .env <<EOF
RELAYMESH_WEB_USERNAME=admin
RELAYMESH_WEB_PASSWORD=$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)
RELAYMESH_MCP_TOKEN=rmk_$(openssl rand -hex 24)
RELAYMESH_PUBLISH_BIND=$BIND
EOF
chmod 600 .env

# A3 启动（首次要拉取约 37 MB 的镜像）
docker compose up -d
docker compose ps        # 十几秒后 STATUS 应为 Up … (healthy)
```

说明：

- `RELAYMESH_PUBLISH_BIND` 决定谁能连上：`127.0.0.1` = 只有这台机器；`0.0.0.0` = 同一网络里的人都能打开登录页（模板里不写这一行时的默认值也是 `0.0.0.0`，所以上面显式写了它）。用户要局域网 / 手机访问：口令与令牌已经生成好了，把 `BIND` 改成 `0.0.0.0` 再执行，并告诉用户「现在同一网络里的人都能打开登录页」。公网请走第 7 节。
- 端口冲突：在 `.env` 里追加 `RELAYMESH_PUBLISH_HTTP_PORT=18785`、`RELAYMESH_PUBLISH_HTTPS_PORT=18786`（数字自选），然后 `docker compose up -d`。
- 只想先试用、不要口令：删掉 `.env` 里的 `RELAYMESH_WEB_PASSWORD` 与 `RELAYMESH_MCP_TOKEN` 两行，同时**必须**让 `RELAYMESH_PUBLISH_BIND=127.0.0.1`（不写这一行，模板默认是所有网卡——那就是全网可达的免登录中枢）。**免登录只适合仅本机访问**；Web 终端功能要求设置密码。
- 旧版 `docker-compose`（带连字符的 v1）请把命令里的 `docker compose` 换成 `docker-compose`。
- 然后做第 8 节的验证。

### 5. 路径 B：单二进制 + systemd（Linux）

```bash
# B1 安装二进制（root 装到 /usr/local/bin/relaymesh；下载后会校验 SHA256，不匹配即中止）
curl -fsSL https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.sh | sh
relaymesh version        # 期望：RelayMesh v<版本号>
```

> 安装脚本想固定版本：`… | sh -s -- v<版本号>`。普通用户运行会装到 `~/.local/bin/relaymesh`，但 systemd 单元里的程序必须放在 `/usr/local/bin` 这样的系统目录（下面用 `DynamicUser`，访问不到 `/home`、`/tmp`），所以走本路径请用 root。

```bash
# B2 环境文件（root 执行；口令与令牌同样不打印）
mkdir -p /etc/relaymesh
cat > /etc/relaymesh/relaymesh.env <<EOF
RELAYMESH_HOST=127.0.0.1
RELAYMESH_PORT=18775
RELAYMESH_HTTPS_PORT=18776
RELAYMESH_DB_PATH=/var/lib/relaymesh/relaymesh.db
RELAYMESH_TLS_CERT=/var/lib/relaymesh/certs/cert.pem
RELAYMESH_TLS_KEY=/var/lib/relaymesh/certs/key.pem
RELAYMESH_OPEN_BROWSER=false
RELAYMESH_WEB_USERNAME=admin
RELAYMESH_WEB_PASSWORD=$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)
RELAYMESH_MCP_TOKEN=rmk_$(openssl rand -hex 24)
EOF
chmod 600 /etc/relaymesh/relaymesh.env

# B3 systemd 单元
cat > /etc/systemd/system/relaymesh.service <<'EOF'
[Unit]
Description=RelayMesh - AI 协同中枢
Documentation=https://github.com/teacat99/RelayMesh
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
DynamicUser=yes
StateDirectory=relaymesh
WorkingDirectory=/var/lib/relaymesh
EnvironmentFile=/etc/relaymesh/relaymesh.env
ExecStart=/usr/local/bin/relaymesh
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

# B4 启动并设为开机自启
systemctl daemon-reload
systemctl enable --now relaymesh
systemctl is-active relaymesh            # 期望：active
journalctl -u relaymesh -n 20 --no-pager # 看到 “HTTP server listening on http://127.0.0.1:18775” 即成功
```

说明：

- `RELAYMESH_HOST=127.0.0.1` 只允许本机访问；要给局域网用，先确认口令与令牌已设置，再改成 `0.0.0.0` 并 `systemctl restart relaymesh`。
- `DynamicUser` + `StateDirectory`：systemd 自动创建一个低权限用户，并管理 `/var/lib/relaymesh`（实际位于 `/var/lib/private/relaymesh`）。数据库、自签名证书都放在这里；**证书路径必须写在环境文件里**，这样重启后证书指纹不变（Agent 用指纹校验服务端）。
- 崩溃会在 3 秒后自动拉起；日志用 `journalctl -u relaymesh` 看。
- 然后做第 8 节的验证，验证命令里的 `ENV_FILE` 换成 `/etc/relaymesh/relaymesh.env`。

### 6. 路径 C 与 D：本机直启 / 前台临时运行

**路径 C：配合 Cursor / Claude Desktop（stdio，随客户端启动和退出，不需要常驻服务）**

有 Go 1.26+：

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

没有 Go：先装二进制（B1 的安装命令；普通用户会装到 `~/.local/bin/relaymesh`，macOS 的这个目录默认不在 `PATH` 里），然后把 `command` 写成它的**绝对路径**：

```json
{
  "mcpServers": {
    "relaymesh": {
      "command": "/绝对路径/relaymesh",
      "args": ["mcp", "stdio"]
    }
  }
}
```

配置文件：Cursor 是 `~/.cursor/mcp.json`（或项目里的 `.cursor/mcp.json`），Claude Desktop 是 `claude_desktop_config.json`。已有配置时**合并**进 `mcpServers`，不要覆盖别的服务器。写完让用户重启客户端。

行为：stdio 运行期间会自动在 `http://127.0.0.1:18775/` 提供 Web 控制台；开多个窗口时，第二个及之后的窗口会自动接入第一个窗口里的进程（会话汇总在同一个控制台，主进程所在窗口关闭后其余窗口自动接管，要求同一个系统用户启动、共用数据目录），只有端口被常驻服务或别的程序占用时才会报错退出，并在报错里说明怎么办；单窗口时网页侧栏默认收起；数据放在系统用户目录（Linux `~/.local/share/relaymesh`，macOS `~/Library/Application Support/relaymesh`，Windows `%LOCALAPPDATA%\relaymesh`），不会往项目目录写任何文件。`go run` 第一次要下载依赖并编译，可能要几十秒到几分钟。

**路径 D：前台临时运行（只想看一眼）**

```bash
mkdir -p ~/relaymesh-try && cd ~/relaymesh-try
relaymesh                # 或：go run github.com/teacat99/RelayMesh/v2/cmd/relaymesh@latest
# 浏览器打开 http://127.0.0.1:18775/ ；Ctrl+C 退出
```

注意：服务模式的数据默认放在**当前工作目录**下的 `data/`（数据库 + `data/certs/` 证书），换个目录再启动就是一份全新的空数据。要固定位置请设置绝对路径的 `RELAYMESH_DB_PATH`。桌面环境下会自动打开浏览器，不想要就设 `RELAYMESH_OPEN_BROWSER=false`。

---

### 7. 公网访问：放在 HTTPS 反向代理后面

原则：服务只监听本机（路径 A 在 `.env` 里写 `RELAYMESH_PUBLISH_BIND=127.0.0.1`，即 A2 里的 `BIND`——模板默认是所有网卡，不写就会暴露；路径 B 保持 `RELAYMESH_HOST=127.0.0.1`），由同一台机器上的反向代理终止 TLS 并转发。前置条件：域名的 DNS 已指向这台服务器，80 / 443 端口已对外开放（开放防火墙由用户操作）。

**Caddy（最省事：自动申请并续期证书，自动处理 SSE 与 WebSocket）**

```caddyfile
relay.example.com {
	reverse_proxy 127.0.0.1:18775 {
		flush_interval -1
	}
}
```

**Nginx / 1Panel / 宝塔**：配置较长，请直接使用 [README「反向代理配置」](../README.md#-nginx--1panel-反向代理配置-reverse-proxy) 里的完整示例。三个关键点不能漏：`proxy_buffering off`（SSE 实时流）、`proxy_read_timeout 3600s`（MCP 长轮询）、`Upgrade` / `Connection` 头（Web 终端的 WebSocket）。

配好之后：

- 浏览器访问 `https://relay.example.com/`，验证里的 `BASE` 换成 `https://relay.example.com`；
- MCP 客户端地址是 `https://relay.example.com/mcp`，令牌放在请求头 `Authorization: Bearer <令牌>`；
- 反向代理与 RelayMesh 在同一台机器、或在私网内时，不需要额外设置；经公网 CDN 或其他主机上的反代访问时，请按 README 设置 `RELAYMESH_TRUSTED_PROXIES`，否则登录防暴力破解会把所有人当成同一个 IP；
- 要使用 OAuth 连接器（ChatGPT / Claude 等），还需要设置 `RELAYMESH_PUBLIC_URL=https://relay.example.com`，见 [OAuth 文档](oauth.md)；新客户端第一次接入前，要在网页「设置 → OAuth 授权」里开启总开关，并打开「接入窗口」（默认限时 10 分钟，窗口之外客户端无法自动注册）。

---

### 8. 验证清单（部署后必做，全部通过才算完成）

把下面脚本保存为 `verify.sh`。它只读取 `.env` 里的口令与令牌用于校验，**不会打印它们**。

```bash
#!/usr/bin/env bash
# 用法：BASE=http://127.0.0.1:18775 ENV_FILE=./.env bash verify.sh
# 路径 B：ENV_FILE=/etc/relaymesh/relaymesh.env；经反向代理：BASE=https://relay.example.com
BASE=${BASE:-http://127.0.0.1:18775}
ENV_FILE=${ENV_FILE:-./.env}
val() { sed -n "s/^$1=//p" "$ENV_FILE" 2>/dev/null | tail -1; }
TOKEN=$(val RELAYMESH_MCP_TOKEN); PASSWORD=$(val RELAYMESH_WEB_PASSWORD); WEB_USER=$(val RELAYMESH_WEB_USERNAME); WEB_USER=${WEB_USER:-admin}
ok()  { printf '✔ %s\n' "$1"; }
bad() { printf '✘ %s\n' "$1"; FAIL=1; }
code() { curl -s -m 8 -o /dev/null -w '%{http_code}' "$@"; }
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"verify","version":"0"}}}'
mcp() { code -X POST "$BASE/mcp" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d "$INIT" "$@"; }

[ "$(code "$BASE/api/v1/auth/status")" = 200 ] && ok "状态接口可访问" || bad "状态接口不可访问（服务没起来，或地址 / 端口不对）"
[ "$(code "$BASE/")" = 200 ] && ok "Web 首页可访问" || bad "Web 首页不可访问"
if [ -n "$PASSWORD" ]; then
  [ "$(code "$BASE/api/v1/sessions")" = 401 ] && ok "未登录读取会话被拒绝（401）" || bad "未登录也能读取会话：Web 密码没有生效"
  c=$(code -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' -d "{\"username\":\"$WEB_USER\",\"password\":\"$PASSWORD\"}")
  [ "$c" = 200 ] && ok "用 .env 里的账号口令可以登录" || bad "登录失败（HTTP $c）"
else
  echo "… 未设置 Web 密码：免登录模式（只适合仅本机访问）"
fi
if [ -n "$TOKEN" ]; then
  [ "$(mcp)" = 401 ] && ok "MCP 不带令牌被拒绝（401）" || bad "MCP 不带令牌也能访问：令牌没有生效"
  [ "$(mcp -H "Authorization: Bearer $TOKEN")" = 200 ] && ok "MCP 带令牌可以握手（200）" || bad "MCP 带令牌仍然失败"
else
  [ "$(mcp)" = 200 ] && ok "MCP 免鉴权可以握手（未设置令牌）" || bad "MCP 握手失败"
fi
exit ${FAIL:-0}
```

再补三项手工检查：

| 检查 | 命令 | 期望 |
|---|---|---|
| 服务状态 | `docker compose ps` / `systemctl is-active relaymesh` | `healthy` / `active` |
| 监听范围 | `ss -ltn \| grep -E ':(18775\|18776)\b'` | 与 `.env` 里的 `RELAYMESH_PUBLISH_BIND` 一致：`127.0.0.1` 时只绑定本机；`0.0.0.0` 时是所有网卡——此时 `verify.sh` 必须显示口令已生效（登录要求密码、MCP 要求令牌） |
| 数据落盘 | `ls data/`（路径 A）/ `ls /var/lib/private/relaymesh`（路径 B） | 能看到 `relaymesh.db` 与 `certs/` |

再做一次「重启后仍可登录」：`docker compose restart`（或 `systemctl restart relaymesh`）后重新跑 `verify.sh`，全部 ✔ 说明数据与登录密钥都持久化了。

### 9. 连接 AI 客户端

HTTP 方式（路径 A / B）：把令牌**用脚本**写进客户端配置，不要在对话里回显。下面以 Cursor 为例，会合并到已有的 `~/.cursor/mcp.json` 而不是覆盖：

```bash
# RM_URL：AI 客户端与 RelayMesh 在同一台机器用 127.0.0.1；不在同一台时换成服务器地址（局域网 http://<IP>:18775/mcp，公网 https://域名/mcp）
RM_URL=http://127.0.0.1:18775/mcp \
RM_TOKEN=$(sed -n 's/^RELAYMESH_MCP_TOKEN=//p' ~/relaymesh/.env) \
python3 - <<'PY'
import json, os
p = os.path.expanduser('~/.cursor/mcp.json')
cfg = json.load(open(p)) if os.path.exists(p) else {}
cfg.setdefault('mcpServers', {})['relaymesh'] = {
    'url': os.environ['RM_URL'],
    'headers': {'Authorization': 'Bearer ' + os.environ['RM_TOKEN']},
}
json.dump(cfg, open(p, 'w'), indent=2, ensure_ascii=False)
PY
```

Claude Desktop、Cline / Roo Code 的写法见 [README「AI 客户端配置」](../README.md#-ai-客户端配置-ai-client-setup)。配置后让用户重启客户端，在 MCP 面板里应能看到 `relaymesh` 并列出 `interactive_feedback` 等工具。

### 10. 可选：接入远程终端 Agent

只有用户在第 1 节选了「要」才做。前置条件：Web 登录密码已设置（本指南默认已设置）。

1. 让用户登录 Web，进入「终端 → 接入主机」，开启「发现窗口」；页面会按目标系统生成安装命令（含服务端地址、证书校验方式和只在窗口开启期间有效的安装票据）。
2. 把页面生成的命令在目标主机上执行。**所有 `relaymesh-agent` 命令都不要碰这台机器上已经在运行的 Agent**：先 `relaymesh-agent status` 看有没有已安装的，有就先问用户。
3. Agent 上线后会出现在待处理列表。**核对机器码（`RM-XXXX-…`）并点「启用」必须由用户本人在 Web 上完成，AI 不要代点。**
4. 更多细节：[README「远程终端 Agent」一节](../README.md)、[终端运维文档](terminal-operations.md)。

### 11. 常见错误速查

| 现象 | 原因 | 处理 |
|---|---|---|
| `bind: address already in use` / `port is already allocated` | 18775 / 18776 被别的程序占用 | 换端口（路径 A：`RELAYMESH_PUBLISH_HTTP_PORT` / `RELAYMESH_PUBLISH_HTTPS_PORT`；路径 B / D：`RELAYMESH_PORT` / `RELAYMESH_HTTPS_PORT`）；不要杀别人的进程 |
| `permission denied while trying to connect to the Docker daemon socket` | 当前用户不在 `docker` 组 | 征得用户同意后用 `sudo`，或把用户加入 `docker` 组并重新登录 |
| `docker: 'compose' is not a docker command` | 没有 Compose v2 | 安装 `docker-compose-plugin`，或改用 `docker-compose` |
| 本机能打开，手机 / 别的电脑打不开 | 监听被写成了 `127.0.0.1`（路径 A：`.env` 里的 `RELAYMESH_PUBLISH_BIND`；路径 B 的二进制默认就是 `127.0.0.1`，由 `RELAYMESH_HOST` 决定），或者防火墙 / 云安全组没放行端口 | 确认口令与令牌已设置后改成 `0.0.0.0`（路径 B 同理改 `RELAYMESH_HOST`），并让用户放行防火墙端口 |
| 浏览器访问 `https://<IP>:18776` 提示证书不受信任 | 内置的是自签名证书，这是正常现象 | 点「继续访问」即可（麦克风录音需要 HTTPS）；想要受信任的证书就走第 7 节。MCP 客户端请用 `http://…:18775/mcp` |
| MCP 客户端报 `401` | 没带令牌或令牌不对 | 请求头 `Authorization: Bearer <令牌>`，或地址后加 `?token=<令牌>`；令牌在 `.env` / 环境文件里 |
| MCP 报 `-32001 Request timed out`，或经反向代理后网页不再实时更新 | 反向代理缓冲了 SSE、或超时太短 | Nginx：`proxy_buffering off` + `proxy_read_timeout 3600s`；Caddy：`flush_interval -1` |
| 重启后会话、草稿都没了 | 数据没有持久化：Docker 没挂载 `./data`；二进制每次在不同目录启动（默认写当前目录的 `data/`） | 路径 A 确认 `./data` 挂载；路径 B / D 设置绝对路径的 `RELAYMESH_DB_PATH` |
| 忘了密码 / 想重置 | — | 只想恢复成 `.env` 里的密码：`docker compose exec relaymesh /app/relaymesh reset-auth`（二进制：`relaymesh reset-auth`），会清除网页上改过的账号密码。想换新密码：先改 `.env` 并 `docker compose up -d`（让容器读到新值），**再**执行这条命令，顺序反了会恢复成旧密码。需要用户明确要求，且不要把新密码贴进对话 |
| 生成口令时 `openssl: command not found` | 机器上没有 openssl | 改用 `head -c 24 /dev/urandom \| base64 \| tr -dc 'A-Za-z0-9' \| head -c 20`；令牌用 `rmk_$(head -c 24 /dev/urandom \| od -An -tx1 \| tr -d ' \n')` |
| 装完后输入 `relaymesh` 提示找不到命令 | 装到了 `~/.local/bin`，它不在 `PATH` 里 | 用绝对路径运行，或把 `~/.local/bin` 加进 `PATH` |
| `go run …@latest` 很久没有输出 | 第一次要下载依赖并编译 | 等待；有缓存之后几秒即可 |

### 12. 升级、备份、回滚、卸载

**路径 A（Docker Compose）**

```bash
# 升级到最新版（数据库迁移自动完成）
docker compose pull && docker compose up -d

# 备份：先停服务，保证数据库文件一致
docker compose down && tar czf relaymesh-data-$(date +%F).tgz data && docker compose up -d

# 回滚到某个版本：在 .env 里写 RELAYMESH_TAG=<旧版本号>（Docker Hub 上的标签不带 v，例如 2.0.0；想回到升级前的 1.x 就写那个版本号），然后
docker compose up -d
# 想回到最新版：删掉 .env 里的 RELAYMESH_TAG 这一行，再 docker compose pull && docker compose up -d

# 从备份恢复（先确认备份文件存在且能读）
tar tzf relaymesh-data-<日期>.tgz > /dev/null && docker compose down && rm -rf data && tar xzf relaymesh-data-<日期>.tgz && docker compose up -d
```

服务端数据库迁移只增不改：旧版本可以直接打开新版本升级过的数据库，往返升级与回滚数据完整；新版本才有的功能所产生的数据，旧版本可能不显示，升回去就能看到。

**路径 B（systemd）**

```bash
# 升级：重新执行安装脚本替换二进制，然后重启（可固定版本：| sh -s -- v<版本号>）
curl -fsSL https://raw.githubusercontent.com/teacat99/RelayMesh/main/install.sh | sh
systemctl restart relaymesh

# 备份 / 恢复（实际数据在 /var/lib/private/relaymesh）
systemctl stop relaymesh && tar czf relaymesh-data-$(date +%F).tgz -C /var/lib/private relaymesh && systemctl start relaymesh
tar tzf relaymesh-data-<日期>.tgz > /dev/null && systemctl stop relaymesh && rm -rf /var/lib/private/relaymesh && tar xzf relaymesh-data-<日期>.tgz -C /var/lib/private && systemctl start relaymesh
```

**卸载（会删除数据，必须先让用户明确确认；建议先做一次备份）**

```bash
# 路径 A
docker compose down && cd .. && rm -rf relaymesh
# 路径 B
systemctl disable --now relaymesh && rm -f /etc/systemd/system/relaymesh.service /usr/local/bin/relaymesh && rm -rf /etc/relaymesh /var/lib/private/relaymesh && systemctl daemon-reload
```

### 13. 交付：向用户汇报什么

部署完成后，用下面的格式向用户汇报（口令与令牌**不要**写进汇报）：

```text
✅ RelayMesh 部署完成
- 部署方式：Docker Compose（目录 ~/relaymesh）/ systemd 单二进制
- 访问地址：Web 控制台 http://<地址>:18775/（账号 admin）；MCP 端点 http://<地址>:18775/mcp
- 谁能访问：只有这台机器（127.0.0.1）/ 同一网络里的人（0.0.0.0，已设置口令）；没有开放到公网
- 口令与令牌：保存在 ~/relaymesh/.env（权限 600），我没有在对话里显示；需要时请自己打开这个文件
- 验证结果：verify.sh 全部 ✔（逐项列出），重启后仍可登录
- 已配置的 AI 客户端：Cursor（~/.cursor/mcp.json），请重启客户端
- 运维命令：升级 / 备份 / 回滚 / 卸载见本指南第 12 节（已为你整理成可直接复制的命令）
- 还没做、需要你决定的：公网域名与 HTTPS、是否接入远程终端 Agent、防火墙放行
```
