# OAuth 2.1 授权服务器（ExecMesh）

RelayMesh Hub 内置 OAuth 2.1 授权服务器与受保护资源元数据，供 ChatGPT、Claude、Claude Code 等 MCP 客户端通过授权码 + PKCE 连接 `/mcp`，无需手动粘贴长期令牌。

## 环境变量

| 变量 | 说明 |
|---|---|
| `RELAYMESH_PUBLIC_URL` | **推荐在生产设置。** 对外根 URL（如 `https://relaymesh.example.com`），用于 `issuer` 与受保护资源 URL。 |
| `RELAYMESH_OAUTH=off` | **硬开关**：关闭全部 `/.well-known/*` 与 `/oauth/*` 端点（返回 JSON 404）；`/mcp` 行为与未启用 OAuth 时一致（不注入 `WWW-Authenticate`）。 |

### 对外地址优先级

1. 环境变量 `RELAYMESH_PUBLIC_URL`
2. 设置页 **OAuth 策略 → 对外地址**（`oauth.public_url`，保存时校验为 https 源地址，环回主机允许 http）
3. 以上皆无：从请求的 `Host` / `X-Forwarded-Host` / `X-Forwarded-Proto` 推导；此时 OAuth 元数据与授权相关响应带 `Cache-Control: no-store` 与 `Vary: Host, X-Forwarded-Host, X-Forwarded-Proto`，Web 会提示「未设置对外地址」。

### OAuth 授权服务器总开关

设置页 **启用 OAuth 授权服务器** 默认 **关闭**（`RELAYMESH_OAUTH=off` 优先级更高）。开启后才暴露元数据与 `/oauth/*`；DCR 默认允许但被总开关管辖。

**生产部署请设置 `RELAYMESH_PUBLIC_URL` 或设置页对外地址**，避免依赖可被客户端影响的请求头。

授权码仅存内存，Hub 重启后未兑换的码失效；同一授权码第二次兑换会作废整条授权（审计 `oauth_code_replay`，不含码本身）。

**关闭总开关 = 立刻断开**：开关关闭（或 `RELAYMESH_OAUTH=off`）时，已签发的 `rmo_` 访问令牌也会被 `/mcp` 拒绝（401）；授权记录与隐藏凭据保留，重新开启后恢复。要永久回收某个应用，请在设置页「撤销」。

修改 OAuth 策略（总开关、对外地址、DCR、受信来源）与批准授权一样需要**终端解锁**（再次验证 Web 密码），并写入审计 `oauth_policy_changed`（只记开关状态，不记地址本身）；拒绝授权不需要解锁。

### 生产首次开启 OAuth 的检查清单

1. 设置 `RELAYMESH_PUBLIC_URL=https://<你的对外域名>`（或在设置页填对外地址），与反向代理一致；不要依赖请求头推导。
2. 设置 `RELAYMESH_TRUSTED_PROXIES` 为反向代理所在网段，使登录、OAuth 与 hook 安装下载的按 IP 限速读到真实客户端 IP。
3. 确认 MCP 本身已启用鉴权（`RELAYMESH_MCP_TOKEN` 或库内令牌）；未配置任何令牌的 Hub 会让 `/mcp` 对任意 Bearer 开放，OAuth 无从谈起。
4. 登录 Web → 设置 → OAuth 策略：先填对外地址，再打开总开关；按需开启 DCR、配置自定义客户端域名。
5. 验证：元数据返回 200；错误的 `redirect_uri` 得到 400 错误页（而不是 302）；无令牌的 `POST /mcp` 返回 `401` 且带 `WWW-Authenticate`；用一个真实客户端走一遍授权并在设置页看到授权记录，再撤销确认令牌立即失效。

## 发现端点

- `GET /.well-known/oauth-protected-resource` — 与下者相同 JSON（资源 URL 为 `{ORIGIN}/mcp`）
- `GET /.well-known/oauth-protected-resource/mcp` — RFC 9728 路径插入式资源元数据（`resource` 字段为 `{ORIGIN}/mcp`）
- `GET /.well-known/oauth-authorization-server` — RFC 8414 授权服务器元数据（`issuer` 为 `RELAYMESH_PUBLIC_URL` 或反代推导的根 URL，无路径）

未提供 OpenID Provider 能力：**不**暴露 `/.well-known/openid-configuration`（该路径返回 JSON `404`，不会被 SPA 接住）。若 `issuer` 带路径前缀的 OIDC 插入式变体不适用本 Hub（单根 issuer、资源固定为 `/mcp`）。

未携带有效令牌的 `POST /mcp` 返回 `401`，并带 `WWW-Authenticate: Bearer resource_metadata="…"`。

## 范围

首发仅开放 ExecMesh 范围：`exec`（终端执行）、`exec:files`（文件）。不开放 `relay`（治理类工具）。

OAuth 访问令牌为 `rmo_` 前缀的不透明串，服务端只存 SHA-256；每次用户批准会创建一条**隐藏 MCP 凭据**，复用终端总闸、主机名单、模式上限、限额、撤销语义（与删除令牌相同，会结束进行中的命令）。

OAuth 令牌**不能**调用 REST / SSE（返回 `403 mcp_token_forbidden`）。

## 用户批准

浏览器访问 `GET /oauth/authorize` 后进入 Web 授权页 `/oauth/consent`：需 Web 登录，授予终端能力前需终端解锁；显示客户端名、重定向主机名与范围；对不在策略白名单或仅回环重定向的客户端会显著警告。

## 管理

设置 → MCP 与安全 → **OAuth 授权的应用** / **OAuth 策略**：查看与撤销授权、配置 DCR、客户端来源白名单、令牌有效期。

## 客户端注册

- **CIMD**：`client_id` 为 HTTPS 元数据文档 URL（抓取有 SSRF 防护：仅 HTTPS、拒绝内网/回环/链路本地、不跟随重定向、64 KiB / 3 秒上限）。
- **DCR**：`POST /oauth/register`（可策略关闭；有速率与数量上限）。

刷新令牌每次轮换；重放旧刷新令牌在宽限期外会作废整条授权。

可选的 ChatGPT mTLS 客户端证书校验**未实现**。
