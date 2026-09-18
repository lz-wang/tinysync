# v0.8.0 — MCP Integration

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本文件是 v0.8.0 的
实现契约：协议版本、transport、endpoint、认证边界、scope 映射、tools、
resources、文件搜索语义、大文件下载、缓存策略、错误语义与升级影响均已
冻结，实现与本契约冲突时以本文件为准并先行修订本文件。

目标：**为现有 Application Services 增加一个受 API Token 保护的 MCP
Adapter**。MCP 提供 TinySync 的查询、同步触发、运行状态查询、本地同步
文件发现与小型文本读取；不通过 MCP 修改 Source / Job / Publish 配置，
不建立新的业务实现、文件索引或认证体系。

```text
MCP != REST wrapper
MCP != 调 REST API
MCP != 第二套 business logic

MCP → Application Service
REST → Application Service
```

## Goals

- Agent / LLM 可以发现同步源、查询任务、触发同步、检索同步文件并读取
  小型文本内容。
- 完全复用 v0.7 认证体系：`auth.Service`、`read` / `run` / `admin`
  scope，不引入第二套权限模型。
- 完全复用现有应用服务：`source.Service`、`syncjob.Service`、
  `syncjob.Runner`、`browser.LocalService`；MCP 只是又一个 transport。

## Non-goals

以下内容明确排除在 v0.8 之外，实现中不得顺手引入：

```text
Source create/update/delete
Job create/update/delete
Publish policy mutation
API Token 管理
远端文件内容读取
MCP OAuth / OIDC
MCP Tasks extension
MCP Apps
Prompts
Sampling
Elicitation
Subscriptions
server → client requests
全文索引 / SQLite FTS
文件 watcher
新增数据库 migration
预签名公开下载 URL
MCP Web UI 配置页
```

尤其不提供 `create_job` / `update_job` / `delete_job`：`run_sync` 只能
执行已经由管理员配置并审核过的 Job，配置变更属控制面操作，继续由
REST / Web UI 的 admin 权限管理。

## Architecture

```text
Web UI
   │
REST/Gin ───────────────┐
                        │
Agent / LLM             │
   │                    │
Streamable HTTP         │
   │                    │
MCP Adapter ────────────┤
                        ▼
              Application Services
              ├── auth.Service
              ├── source.Service
              ├── syncjob.Service
              ├── syncjob.Runner
              ├── browser.LocalService
              └── publish.Service
```

- 新增 `internal/mcp` 包：只在该包 import MCP SDK；`internal/api` 只
  认识 `http.Handler`（`api.Dependencies.MCP`），不知道 SDK 的存在。
- 装配在 `internal/app` composition root 完成，与 REST 共用同一批
  应用服务实例。
- MCP 不接触数据库细节；查询走 `managed_files` 现有 repository 契约
  （`ListByJob`），认证走 `auth.Service`，运行走 `Runner`。
- **不新增 MCP 专属数据库 schema**：搜索使用现有 `managed_files`、
  认证使用现有 `api_tokens`、运行使用现有 `sync_runs`。实现中若发现
  需要 MCP 专属表，视为架构偏离。

## SDK 与协议版本

- 官方 Go SDK：`github.com/modelcontextprotocol/go-sdk v1.8.0`。
- 协议版本：**`2026-07-28` only**。服务端固定
  `SupportedProtocolVersions: []string{"2026-07-28"}`；旧版本协商一律
  失败，不引入兼容层。

## Transport 与 Endpoint

```text
Endpoint:   POST /mcp
Transport:  Streamable HTTP
Stateless:  true（不建立 TinySync 自有 MCP session persistence）
JSONResponse: true
```

- `StreamableHTTPOptions{Stateless: true, JSONResponse: true,
  PropagateRequestCancellation: true, MaxRequestBodyBytes: 1 << 20}`。
  请求体超过 1 MiB 返回 413。
- 显式启用 Go 标准库 `http.NewCrossOriginProtection()` 包裹 handler
  （SDK 的 options 字段已 deprecated，不依赖其默认值）；SDK 的
  localhost DNS-rebinding protection 保持默认启用。
- 不为旧 transport 引入 stateful session：`2026-07-28` 已转向
  sessionless 模型，TinySync 没有 server→client sampling、elicitation
  或长期 SSE subscription 需求。

## Authentication

```text
Authorization: Bearer ts_xxx
        │
        ▼
MCP SDK RequireBearerToken
        │
TokenVerifier
        │
auth.Service.AuthenticateAPIToken()
        │
Principal
        │
per-tool auth.Authorize()
```

- `/mcp` **只接受 API Token Bearer credential**；Web Session Cookie
  一律 401（即使浏览器已登录）。MCP 始终要求显式 Bearer API Token，
  与 v0.7「machine credential 只有一个入口」的设计一致。
- TokenVerifier 包装 `auth.Service.AuthenticateAPIToken`：不存在 /
  已撤销 / 已过期统一映射为 SDK `auth.ErrInvalidToken`（→ HTTP 401）。
- `RequireBearerTokenOptions` **不设置全局 `Scopes`**（否则 run-only
  token 连 `/mcp` 都进不去），并设置 `AllowMissingExpiration: true`
  （TinySync token 可永不过期，过期判定在 verifier 内完成）。
- 认证成功后将 principal scopes 写入 `TokenInfo.Scopes`，供 handler
  内 per-tool 授权使用。

## Authorization matrix

HTTP middleware 只负责 authentication；**tool handler 根据工具做
authorization**（`auth.Authorize(principal, scope)`）：

| MCP 能力          | Scope  |
| ---------------- | ------ |
| `list_sources`   | `read` |
| `list_jobs`      | `read` |
| `get_job`        | `read` |
| `get_sync_run`   | `read` |
| `search_files`   | `read` |
| `get_file_info`  | `read` |
| Resource read    | `read` |
| `run_sync`       | `run`  |

冻结语义保持：`admin ⇒ read + run`；`run ⇏ read`；`read ⇏ run`。
v0.8 没有 admin-only tool。

## Tools

| Tool            | 输入                         | 主要输出                           | Scope |
| --------------- | -------------------------- | ------------------------------ | ----- |
| `list_sources`  | `limit`, `offset`          | Source 摘要                      | read  |
| `list_jobs`     | `limit`, `offset`          | Job 摘要                         | read  |
| `get_job`       | `job_id`                   | Job 完整只读配置                  | read  |
| `run_sync`      | `job_id`                   | `run_id`                       | run   |
| `get_sync_run`  | `run_id`                   | state/stats/error/timestamps   | read  |
| `search_files`  | `job_id`, `query`, `limit` | matching local files           | read  |
| `get_file_info` | `job_id`, `path`           | metadata/resource/download URL | read  |

- 输入采用强类型 struct（经 SDK JSON Schema 校验），分页参数
  `limit` 默认 100、上限 500（越界报 tool error，不静默截断）。
- **不直接输出 domain object**：MCP DTO 层冻结 wire contract；Source
  DTO 只回显非敏感 config 与 credential_state 布尔集合，secret 与
  raw token 永不出现在任何 MCP 返回中。
- `run_sync` 只接受 `job_id`，不允许调用方覆盖 mode / source /
  remote_root / local_root / include / exclude。工具描述明确标注为
  side-effect / destructive-capable（mirror Job 可能删除「已由该 Job
  管理、但远端已不存在」的本地文件）。
- `run_sync` 调用链严格为 `Runner.Start(jobID) → run_id`（异步启动、
  立即返回）；`get_sync_run` 走 `Runner.GetRun(runID)`。运行 context
  与 MCP HTTP 请求 context 解耦——HTTP 请求结束不取消已成功启动的
  run。不需要 MCP Tasks extension。

### run_sync 错误语义

Runner 的既有错误必须转换为稳定、可供 Agent 判断的 MCP tool error，
不得统一映射为 internal error：

| Runner 错误                | Tool error 语义                        |
| ------------------------- | -------------------------------------- |
| Job not found             | `job not found`（确定性失败）           |
| Job disabled              | `job is disabled`（确定性失败）         |
| Source disabled           | `source is disabled`（确定性失败）      |
| ErrRunActive              | `job already running`（瞬时，可重试）    |
| ErrConcurrencyLimit       | `concurrency limit reached`（瞬时）     |
| ErrJobMutating            | `job is being modified`（瞬时）         |
| ErrShuttingDown           | `server is shutting down`（服务不可用）  |
| 其他（存储故障等）          | internal error（不泄露内部细节）         |

## File search semantics

`search_files` 基于 **managed_files**，不是通用主机文件搜索。不引入
SQLite FTS、新索引表、后台索引器或文件 watcher；也不在 MCP handler
里 `filepath.Walk`。

应用层新增 `browser.LocalService.SearchManaged`（不依赖 MCP，REST
未来可复用）：

```text
namespace      = Job（jobs.Get 校验后取 LocalRoot）
source         = managed_files（ListByJob）
state          = StateSynced only
match target   = LocalRelPath
match          = case-insensitive substring
default limit  = 50
max limit      = 200
```

返回前对匹配记录再走 `LocalService.Stat()` 获得当前实际文件状态
（filesafe confinement 后的 Entry），**不信任数据库中的旧
size/mtime**；本地已删除的文件不返回。结果携带 `Truncated` 标记。

```text
managed_files → candidate path → LocalService.Stat → actual Entry
```

对调用方已明确知道的具体路径，`get_file_info` 仍按现有
`LocalService.Stat()` 语义访问（managed 标记照常返回）。达到 10 万级
managed_files 后是否改用 SQLite 查询，留待 v0.9 性能基准后再决定。

## Resources

URI 模板显式携带 Job namespace：

```text
tinysync://jobs/{job_id}/files/{path}
例：tinysync://jobs/job_abc/files/docs/readme.md
```

对应 `Job → LocalRoot → logical path` 模型，无「属于哪个 LocalRoot」
的歧义。读取经 `LocalService.Open()`（filesafe 边界：父目录 symlink
逃逸防御、拒绝 symlink、拒绝目录）。

Resource 只负责小型文本，固定约束：

```text
regular file only
UTF-8 only
max = 256 KiB
```

- 读取不只信 `Stat().Size`：`io.LimitReader(file, maxResourceSize+1)`
  防止 stat/read 之间文件被替换超限。
- binary（含 UTF-8 BOM/无效序列检测失败）、超限、目录、symlink、
  special file 一律拒绝，提示调用方走 `get_file_info` → `download_url`。
- 空文件（0 字节）合法返回空文本。

## Large file download

大文件继续走现有 HTTP，**不新增 `/mcp/download`，不复制下载实现**：

```http
GET  /api/v1/jobs/:id/files/download?path=/foo.bin
HEAD /api/v1/jobs/:id/files/download?path=/foo.bin
```

`get_file_info` 返回 relative URL（client 相对 MCP endpoint origin
解析），不生成 absolute URL，不引入 public base URL 配置：

```json
{
  "job_id": "job_abc",
  "path": "/movie.mkv",
  "kind": "file",
  "size": 123456789,
  "managed": true,
  "resource_uri": "tinysync://jobs/job_abc/files/movie.mkv",
  "download_url": "/api/v1/jobs/job_abc/files/download?path=%2Fmovie.mkv",
  "download_auth": "bearer"
}
```

下载时继续携带同一 `Authorization: Bearer ts_xxx`（`read` scope）；
不产生匿名临时 URL / pre-signed URL。现有 Range → 206、非法 Range →
416、HEAD、Content-Disposition 行为不回归。

## Cache policy

`tools/list`、`resources/list`、`resources/read` 等带 `ttlMs` /
`cacheScope` 的响应：TinySync 的结果是认证后的私有 HomeLab 数据，
通过 `ServerOptions.SetCacheable` 显式设置：

```text
cacheScope = private
ttlMs      = 0
```

第一版不做客户端缓存优化；API 稳定后再考虑给 `tools/list` 更长 TTL。

## Error semantics

- 领域错误（not found / invalid / conflict）映射为确定性 tool error
  文案（见 run_sync 错误语义表），Agent 可据此重试或放弃。
- 授权失败返回权限拒绝 tool error，与「资源不存在」可区分。
- 内部错误（存储故障等）统一 `internal error`，不向 MCP 客户端泄露
  文件系统路径、SQL 或栈信息。
- context 取消（请求断开）原样传递，不包装成业务错误。

## Security

- Cross-origin protection 显式启用（`http.NewCrossOriginProtection()`）；
  localhost DNS-rebinding protection 保持 SDK 默认启用。
- `/mcp` 认证失败一律 401；无匿名降级路径。Auth service 缺失时
  fail closed，不注册 MCP handler。
- secret、raw token、token hash、credential 明文不出现在任何 MCP
  tool / resource 返回中；E2E 以 grep 断言。
- MCP 请求体上限 1 MiB；资源读取上限 256 KiB；搜索/列表均有上限，
  无无界返回。
- 不提供任何 mutation tool；配置面变更不经 MCP 可达。

## Testing

- 单元：transport 认证矩阵（no auth / invalid / revoked / expired /
  cookie-only → 401；valid → 接受；旧协议 → unsupported；> 1 MiB →
  413）、tool 授权矩阵、搜索语义、resource 边界（256 KiB ± 1、
  binary、symlink、`../`、Stat 与实际读取不一致）。
- E2E（`internal/e2e/mcp_test.go`）：真实 Gin router + SQLite +
  auth.Service + Runner + WebDAV fixture + 官方 MCP client，覆盖
  authentication、authorization、run、search、resource、HTTP download
  全链路（含 Range → 206）。
- native smoke：锁定 `anonymous /mcp → 401`、`Bearer /mcp → 有效 MCP
  响应` 两条存活检查；协议细节由 Go E2E 承担。
- 既有 REST / Web UI / Scheduler / Browser / Publish E2E 零回归。

## Upgrade impact

- **无数据库 migration**（v6 → v7 schema 不变）。
- 无新运行配置、无新环境变量；`/mcp` 随 `serve` 启动。
- 升级后 MCP 立即可用：用现有 API Token（read / run / read+run / admin）
  作 Bearer credential 连接 `POST /mcp`。
- 降级安全：v0.7 二进制上 `/mcp` 不存在（404 fallback），不影响
  既有客户端。

## 完成标准

1. `POST /mcp` 使用官方 Go MCP SDK 和 Streamable HTTP，支持并固定
   `2026-07-28`。
2. MCP transport 为 stateless，不建立 TinySync 自有 MCP session
   persistence。
3. `/mcp` 只接受 API Token Bearer credential；Web Session Cookie
   无效。
4. revoked / expired / invalid token 均无法访问 MCP。
5. MCP 完全复用 `auth.Service` 和 `read` / `run` / `admin` scope，
   不引入第二套权限模型。
6. `list_sources` / `list_jobs` / `get_job` 只通过 Application
   Service 查询，secret 永不进入返回结果。
7. `run_sync` 只接受现有 `job_id`，不允许调用方覆盖同步配置。
8. `run_sync` 使用 `run`；运行状态查询使用 `read`；`run ⇏ read`
   保持成立。
9. `search_files` 以 Job 为 namespace，从 `managed_files` 检索已
   同步文件，不扫描任意主机目录。
10. `get_file_info` 复用 `LocalService.Stat()` 和现有 filesafe 边界。
11. `tinysync://jobs/{job_id}/files/...` 只能读取受 Job.LocalRoot
    限制的普通文件。
12. MCP inline resource 仅支持 ≤256 KiB UTF-8 文本，并具有独立实际
    读取上限（`LimitReader(max+1)`）。
13. binary / large file 不经 MCP base64 搬运。
14. 大文件复用现有 `/api/v1/jobs/:id/files/download`，Range / HEAD
    行为不回归。
15. MCP 返回的 download URL 继续要求同一 `read` Bearer token，不产生
    匿名临时 URL。
16. MCP cacheable responses 使用 `cacheScope=private`。
17. Cross-origin protection 与 SDK localhost DNS-rebinding protection
    保持启用。
18. 不提供 Source / Job / Publish mutation MCP tools。
19. 不新增 MCP 专属数据库 schema。
20. MCP E2E 完整覆盖 authentication、authorization、run、search、
    resource、HTTP download。
21. 既有 REST / Web UI / Scheduler / Browser / Publish E2E 零回归。
22. `make check`、`make build`、integration 和三平台 native smoke
    全绿。
23. 正式 Release、六平台资产、checksum、WebDAV mirror 按现有发布
    流程验收。
