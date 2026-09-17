# v0.7.0 — Authentication & API Tokens

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本文件是 v0.7.0 的
实现契约：Local Admin 模型、密码存储、Web Session、CSRF、API Token、
scope 语义、路由权限矩阵、migration 与升级行为均已冻结，实现与本契约
冲突时以本文件为准并先行修订本文件。

目标：为 Web 管理端和程序化 API 建立统一安全边界——

```text
Local Admin
    ↓ password login
Web Session ──────────────┐
                          ↓
                      Principal
                          ↓
                    Authorization
                          ↓
REST / Web UI → Application Services

API Token ────────────────┘
```

v0.7 的核心不是「Token 页面」，而是建立统一的
`Principal → AuthZ → Application Service` 边界。Web Session 与
API Token 只是两种 credential；v0.8 MCP 到来时只需再接入一个
transport，不需要重新设计认证体系。

依赖方向保持单向：

```text
api ──────────→ auth.Service
                  ↓
                sqlite

MCP(v0.8) ────→ auth.Service
```

认证逻辑不写进 Gin middleware；middleware 只做 credential 提取与
principal 传递。v0.8 应复用 authentication service，而不是 HTTP
implementation。

## 范围

| 项目 | v0.7.0 |
| --- | --- |
| 管理身份 | 单一固定 `admin`（single-admin service） |
| 管理员密码 | Argon2id，CLI `tinysync auth set-password` bootstrap / reset |
| Web Session | HttpOnly Cookie，7 天绝对 TTL，无 sliding / remember me |
| Session API | login / session / logout |
| API Token | create / list / revoke，scope + 过期 + `last_used_at` |
| Scope | `read` / `run` / `admin`，`admin ⇒ read + run` |
| REST 默认策略 | 全面 default-deny，显式 public 白名单 |
| CSRF | `SameSite=Strict` + Origin 校验 |
| Web UI | 登录页 + Token 管理页 + Logout |
| CORS | **不启用**（跨域 API 不是目标） |
| `/published/*path` | **保持显式 public，不变** |

明确不做（以后有明确需求再增加）：

```text
多用户 / 用户注册 / RBAC
OAuth / OIDC / JWT / MFA
Refresh Token / API Token rotation protocol
Token resource-level ACL
LDAP / SSO / TLS termination
password recovery email
session 管理列表 / audit log
细粒度 scope（sources:read、jobs:run 等，等 v0.8 有真实
调用面后再决定，避免形成无法稳定兼容的权限 API）
```

## Local Admin 模型

不引入通用 `users` 表。TinySync v1 主线没有多用户需求，现有
Source / Job / File 都没有 owner 概念；通用用户体系只会提前引入
roles / memberships / ownership 等无需求支撑的复杂度。

管理员身份逻辑上固定为 `subject = "admin"`，不需要 username 配置。
数据库使用单例行：

```sql
CREATE TABLE admin_credentials (
    singleton     INTEGER PRIMARY KEY CHECK (singleton = 1),
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
```

登录请求体只有密码：

```json
{ "password": "..." }
```

## 管理员密码

密码与 API Token 采用不同存储策略：人类密码用慢速 KDF，高熵随机
secret 用 SHA-256。

| 项目 | 契约 |
| --- | --- |
| 算法 | Argon2id（`golang.org/x/crypto`，已是直接依赖，无新增依赖） |
| 初始参数 | `memory = 19 MiB`、`iterations = 2`、`parallelism = 1`（OWASP 最低推荐；目标机器 benchmark 后可适度提高） |
| salt | `crypto/rand` |
| 存储 | PHC 风格完整编码值，如 `$argon2id$v=19$m=19456,t=2,p=1$...$...` |
| 最小长度 | 12 字符 |
| 最大输入 | 1024 字节（避免超长输入造成密码 KDF DoS） |

## Bootstrap：CLI，不做匿名 Web Setup

**不提供「首次访问网页创建管理员」的匿名 setup endpoint**。否则
HomeLab 场景（LAN / VPS / reverse proxy 后）存在 first-user race：
首次启动后任何网络客户端都能调用 setup。

新增 CLI：

```bash
tinysync auth set-password --datadir /path/to/data
```

- 默认从 TTY 安全读取（不回显）并要求输入确认。
- 自动化用 `--password-stdin`：

```bash
printf '%s\n' "$PASSWORD" | tinysync auth set-password --datadir /data --password-stdin
```

- **不提供 `--password xxx`**：避免密码进入 shell history / process argv。

`set-password` 同时承担首次 bootstrap、遗忘密码后的 operator reset
与密码 rotation：不存在 admin 则创建，存在则替换密码。密码更新与
`DELETE FROM web_sessions` 处于同一 transaction——重置密码立即废弃
全部已有 Web Session。

## 启动安全模型

> 没有初始化管理员密码时，`tinysync serve` 直接启动失败：
>
> ```text
> authentication is not initialized;
> run `tinysync auth set-password --datadir ...`
> ```

不保留 `--no-auth` / `TINYSYNC_DISABLE_AUTH` / anonymous mode 等绕过
路径。v0.6 → v0.7 是一次明确的 operational migration：

```text
stop v0.6 → install v0.7 → tinysync auth set-password --datadir ...
→ start v0.7 → Web login → 为脚本创建 API Token
```

该步骤写入 README 与 release notes。

## Web Session

```sql
CREATE TABLE web_sessions (
    id           TEXT PRIMARY KEY,
    session_hash BLOB NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);
```

- 浏览器持有：`32-byte crypto/rand → base64url`。
- 数据库只存 `SHA-256(raw_session_token)`（session token 是高熵
  secret，不是人类密码，SHA-256 安全合理）。
- Session ID 不进入 `localStorage` / `sessionStorage`。

Cookie：

```text
tinysync_session=<random>
HttpOnly
SameSite=Strict
Path=/
Secure   ← 仅 HTTPS 请求（request.TLS != nil 或
           X-Forwarded-Proto == https）时设置
```

生命周期（v0.7 固定）：

```text
absolute TTL = 7 days
sliding expiration = no
remember me = no

login        → 创建 session
logout       → 删除当前 session
expired      → 401 + 删除过期 session
set-password → 删除全部 session
```

## CSRF

Cookie authentication 引入后不可省略。组合：

```text
SameSite=Strict（主防线）
+
Origin validation（纵深防御）
```

对 `POST` / `PUT` / `PATCH` / `DELETE`：请求携带 `Origin` 头时，
`Origin.Host` 必须等于 `Request.Host`，否则 403。登录 endpoint 同样
检查。Bearer API Token 不做 CSRF 检查——Authorization header 不会
被浏览器自动附带。继续不启用 CORS。

## API Token

```sql
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    token_hash   BLOB NOT NULL UNIQUE,
    scopes_json  TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,
    last_used_at INTEGER,
    revoked_at   INTEGER
);
```

Token 格式：

```text
crypto/rand 32 bytes（256 bit 熵）
        ↓ base64url
ts_<secret>        例如 ts_lf4kV...
```

数据库：`prefix = ts_ 前 8 字符`（展示用）、
`token_hash = SHA-256(full raw token)`。**Raw token 只在 create
response 返回一次**；日志、数据库、后续 GET API 绝不出现它。

生命周期语义：

```text
revoke  幂等（active → revoked、revoked → revoked 均 204）
        只软撤销，不 DELETE 记录（保留 name / prefix / created_at /
        last_used_at / revoked_at 作为最低限度生命周期证据）
scope / expiration  immutable；需要变更 → revoke + create new
last_used_at        Repository 节流更新（阈值 1 分钟），高频 API
                    调用不退化为每请求一次 SQLite 写
```

## Scope 语义

沿用 `read` / `run` / `admin` 三种，语义冻结：

| Scope | 权限 |
| --- | --- |
| `read` | 查询 Source / Job / Run / File / Published metadata，并下载文件 |
| `run` | 手动触发 Job |
| `admin` | 所有权限：read + run + 配置修改 + Token 管理 |

```text
admin ⇒ read + run
run ⇏ read
read ⇏ run
```

`run` 不含 `read` 允许创建只会触发已知 Job ID 的自动化 token。
scope 规范化：排序、去重、拒绝未知值、拒绝空集合。

## HTTP 权限矩阵

Router 改造为 public / protected 两层（`/published/*path` 单独保持
public）：

| API | 权限 |
| --- | --- |
| `GET /api/v1/health` | public |
| `GET /api/v1/version` | public |
| `POST /api/v1/auth/login` | public + Origin check |
| `GET /api/v1/auth/session` | Web Session |
| `POST /api/v1/auth/logout` | Web Session |
| `GET /api/v1/sources*`（含远端文件 list / stat / download） | `read` |
| Source create / update / delete / test | `admin` |
| `GET /api/v1/jobs*`（含本地文件 list / stat / download） | `read` |
| Job create / update / delete | `admin` |
| `POST /api/v1/jobs/:id/run` | `run` |
| `GET /api/v1/runs*` | `read` |
| `GET /api/v1/published-files` | `read` |
| Publish create / update / delete | `admin` |
| `GET /api/v1/api-tokens` / `POST /api/v1/api-tokens` / `POST /api/v1/api-tokens/:id/revoke` | `admin` |
| `/published/*path` | **public，不变** |

Source `test` 归入 `admin`：它会用已有 secret 主动访问远端网络。

## Auth middleware

认证顺序固定：

```text
Authorization header 存在？
   ├─ yes → 只能是 Bearer Token
   │         valid → API principal
   │         invalid → 401（绝不 fallback 到 Web Session）
   └─ no  → Web Session cookie
             valid → admin principal（scopes = read+run+admin）
             invalid / absent → 401
```

关键规则：

> 显式提供了无效 Bearer Token 的请求，即使同时持有有效 Web Session
> 也必须 401——避免浏览器中错误 Authorization header 造成权限来源
> 混淆。

只支持 `Authorization: Bearer <token>`；认证信息不放 URL。禁止
`?token=` / `?api_key=` / `X-API-Key` / cookie api token 等替代入口，
v0.8 MCP 因此只有一个 machine credential 入口。

`Dependencies.Auth == nil` 必须 fail closed（500），绝不能解释为
「关闭认证」的匿名模式。

## Auth 服务层

```text
internal/auth/
├── model.go            Principal / Scope / Token / Session / errors
├── password.go         Argon2id encode / verify + 长度限制
├── token.go            token 生成 / SHA-256 / scope 规范化
├── session.go          session 生成 / TTL
├── service.go          认证应用服务
└── sqlite/
    └── repository.go   SQLite 实现
```

服务接口（供 api / CLI / 未来 MCP 复用）：

```go
SetAdminPassword(...)      // 原子：替换 hash + 清空 session
AdminConfigured(...)
Login(...) / Logout(...) / AuthenticateSession(...)
CreateAPIToken(...) / ListAPITokens(...) / RevokeAPIToken(...)
AuthenticateAPIToken(...)
Authorize(principal, scope) // admin ⇒ read + run
```

Composition root（`internal/app/app.go`）在 migration 后构造
`auth.Service`，注入 `api.Dependencies{Auth: ...}`；`serve` 在
`AdminConfigured() == false` 时启动失败。

## SQLite migration

`0007_authentication.sql`，包含 `admin_credentials` / `web_sessions` /
`api_tokens` 三表，并补 migration upgrade test：

```text
0006 database（含 Source / Job / Run / Published 数据）
     ↓ run migrations
0007 tables / constraints correct
     ↓
既有业务记录完整无损
```

## REST 契约

### Web Session API

```http
POST /api/v1/auth/login     {"password": "..."}
→ 200 {"expires_at": "RFC3339"} + Set-Cookie

GET  /api/v1/auth/session   → 200 {"authenticated": true, "expires_at": "..."}
POST /api/v1/auth/logout    → 204 + 清除 Cookie
```

登录失败（密码错误 / admin 未配置 / 凭据不存在）统一返回
`401 {"error": "invalid credentials"}`，不区分具体原因。auth 相关
响应带 `Cache-Control: no-store`。

### API Token REST

```http
POST /api/v1/api-tokens     {"name": "...", "scopes": ["read","run"], "expires_at": "RFC3339 可选"}
→ 201 {"api_token": {...}, "raw_token": "ts_..."}   ← raw 唯一一次

GET  /api/v1/api-tokens     → 200（绝不返回 raw_token / token_hash）
POST /api/v1/api-tokens/:id/revoke → 204 幂等
```

## Web UI

```text
App
├── /login                  LoginPage（Password + Login；无注册/忘记密码入口，
│                           提示 operator 用 `tinysync auth set-password`）
└── RequireAuth
    └── AppShell
        ├── / /sources /jobs /files /history（不变）
        └── /tokens         TokensPage + CreateTokenDialog + RawTokenDialog
```

- 前端 API client 收敛为统一 `apiFetch()`：`credentials: 'same-origin'`、
  JSON / 204 处理、后端错误提取、401 → `UnauthorizedError`；
  AuthProvider 捕获 401 后清空 session 状态并跳转 `/login`。
- Token 绝不存入前端 storage；Web UI 只用 Session Cookie。
- Raw token modal 关闭即清空 React state，无法再次打开查看。
- Token 状态展示：Active / Expired / Revoked；List / Create /
  scope 选择 / 过期 / copy / revoke / last used。

## 测试策略

不能只测 happy path，至少覆盖：

| 场景 | 结果 |
| --- | --- |
| health / version / SPA / login assets without auth | 200 |
| protected API without auth | 401 |
| valid session | allowed |
| expired session / logout 后的 session | 401 |
| password reset | 全部旧 session 失效 |
| invalid bearer + valid cookie | **401，不 fallback** |
| read token → GET | allowed |
| read token → POST run | 403 |
| run token → run | allowed |
| run token → GET sources | 403 |
| admin token → 全部 protected API | allowed |
| expired / revoked token | 401 |
| token missing scope | 403 |
| token 出现在 query | rejected |
| raw token 出现在数据库 / 日志 / List API | absent |
| cross-origin session mutation | rejected |
| `/published/*path` | auth-independent |
| 既有 Source / Job / Scheduler / History / Browser / Publish E2E | zero regression |
| restart 后 admin / token / revocation / expiration 状态 | 正确恢复 |

测试不允许让 `Auth == nil` 变成「关闭认证」；统一 test helper
（构造已初始化 admin 的 router、签发 session / admin token、
携带凭据发请求）并批量迁移既有 API / E2E 测试。

## v0.6 → v0.7 升级

1. stop v0.6 服务。
2. 安装 v0.7 二进制。
3. `tinysync auth set-password --datadir <datadir>`（migration 0007
   在此过程中自动完成，升级前自动生成备份）。
4. `tinysync serve` 启动；未初始化密码则拒绝启动。
5. Web 登录；为既有脚本逐一创建 API Token（v0.6 的匿名脚本访问
   从 v0.7 起必须携带 Bearer Token）。

`/published/*path` 公开语义不受升级影响。

## v0.8 MCP 复用边界

MCP 接入 authentication service（`AuthenticateAPIToken` /
`Authorize`），不接 Gin middleware。是否需要 `read/run/admin` 之外
的 capability、是否需要限定单个 Job 的 token，等 v0.8 有真实调用面
后再决定；TinySync 长期是否坚持 single-admin 亦同。

## 完成标准

1. 未认证客户端无法访问任何管理 API。
2. Web UI 使用 HttpOnly Session Cookie；不用 API Token 充当浏览器
   session。
3. API Token 只通过 `Authorization: Bearer` 使用。
4. Password 使用 Argon2id；Session / API Token 是随机高熵 secret +
   SHA-256 存储。
5. 数据库、日志、List API 中均不存在 raw API token。
6. `read` / `run` / `admin` 权限边界有自动化测试。
7. expired / revoked token 立即失效。
8. password reset 立即使全部 Web Session 失效。
9. session mutation 具备 CSRF 防护。
10. `/published/*path` 继续保持显式 public 语义。
11. Source / Job / Scheduler / History / Browser / Publish 的既有
    E2E 全部通过。
12. Restart 后 admin、token、revocation、expiration 等状态正确恢复。
13. `make check` / `make build` / integration 全绿。
14. v0.6 用户存在明确且可重复的升级流程。
