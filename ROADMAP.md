# TinySync Roadmap

TinySync 是一个面向 HomeLab 的轻量级远程文件同步服务。

核心目标：

> 将 WebDAV、S3、SFTP 等异构远端文件源统一抽象为 Source，
> 按 Sync Job 将选定文件单向 Pull / Mirror 到本地文件系统，
> 并通过 Web UI、REST API 与 MCP 提供统一管理和访问能力。

## 设计原则

* 单一 Go 二进制，内嵌 React Web UI。
* 后端使用 Go + Gin。
* 全工程 `CGO_ENABLED=0`。
* 本地文件系统保存实际同步文件。
* SQLite 只保存配置、状态、元数据与历史记录，不保存文件内容。
* Remote Source 与 Sync Job 分离，一个 Source 可被多个 Job 复用。
* v1 只实现远端 → 本地的单向 Pull / Mirror。
* 默认保护本地数据，Mirror 只能删除由当前 Job 管理的文件。
* Web UI、REST API、MCP 共用同一应用服务层，不重复实现业务逻辑。
* 优先保证简单、稳定、可恢复，不引入分布式架构和不必要的企业级能力。

## 非目标

v1.0 之前明确不实现：

* 本地 → 远端上传同步。
* 双向同步。
* 文件冲突自动合并。
* 远端文件 rename / move / delete 管理。
* 多节点或分布式同步。
* 分布式调度。
* 文件内容存入 SQLite。
* 浏览器端转码、编辑等复杂文件处理能力。

---

# 版本进度

| 版本     | 阶段                          | 状态                  |
| ------ | --------------------------- | ------------------- |
| v0.1.0 | 工程与发布基线                     | 🟡 Ready to release |
| v0.2.0 | Persistence & Source Domain | ⬜ Planned           |
| v0.3.0 | WebDAV Pull Sync            | ⬜ Planned           |
| v0.4.0 | Scheduler & Sync History    | ⬜ Planned           |
| v0.5.0 | S3 & SFTP Sources           | ⬜ Planned           |
| v0.6.0 | File Browser & Publishing   | ⬜ Planned           |
| v0.7.0 | Authentication & API Tokens | ⬜ Planned           |
| v0.8.0 | MCP Integration             | ⬜ Planned           |
| v0.9.0 | Hardening & Operations      | ⬜ Planned           |
| v1.0.0 | Stable Single-Node Release  | ⬜ Planned           |

状态含义：

* ✅ Completed
* 🟡 Ready / In progress
* ⬜ Planned

---

# v0.1.0 — 工程与发布基线

目标：建立后续领域开发依赖的稳定工程底座。

## Runtime

* [x] Go 1.26 工程。
* [x] `main -> cmd -> app -> api` 分层。
* [x] Gin HTTP server。
* [x] `serve` 命令。
* [x] `version` / `--version`。
* [x] `--datadir` / `TINYSYNC_DATADIR`。
* [x] `--port` / `TINYSYNC_PORT`。
* [x] SIGINT / SIGTERM 优雅关闭。
* [x] zap + lumberjack 日志。
* [x] 全工程禁止 CGO。

## HTTP

* [x] `GET /api/v1/health`。
* [x] `GET /api/v1/version`。
* [x] API 404 与 SPA fallback 正确隔离。
* [x] Method Not Allowed 处理。
* [x] 静态资源缓存策略。
* [x] HTTP server timeout 基线。

## Web UI

* [x] React。
* [x] TypeScript。
* [x] MUI。
* [x] Vite。
* [x] Biome。
* [x] 服务状态与版本展示。
* [x] WebUI 内嵌 Go binary。
* [x] `webui` build tag / fallback 构建机制。

## Engineering

* [x] Git 驱动版本号。
* [x] Keep a Changelog。
* [x] Semantic Versioning。
* [x] Makefile 统一工程入口。
* [x] Go tests。
* [x] Codecov。
* [x] Linux / macOS / Windows native smoke。
* [x] Linux / macOS / Windows × amd64 / arm64 cross-build。
* [x] Build / Release GitHub Actions 分离。
* [x] 开发版 WebDAV 镜像。
* [x] 正式版 WebDAV 镜像机制。
* [x] 移除 GitHub Actions Artifact 依赖。
* [ ] 发布并验证 `v0.1.0` Release workflow。

完成标准：

```text
v0.1.0 tag
  → release validation
  → make ci
  → 3-platform native smoke
  → 6-platform dist
  → checksums.txt
  → GitHub Release
  → WebDAV mirror
  → Pushover
```

---

# v0.2.0 — Persistence & Source Domain

目标：建立稳定的数据持久化层和协议无关的 Source 模型。

## SQLite

* [ ] 选择 pure-Go SQLite driver。
* [ ] 建立 `internal/storage`。
* [ ] 数据库自动初始化。
* [ ] Schema migration/version 机制。
* [ ] 数据库备份与升级边界。
* [ ] SQLite pragma 基线。
* [ ] Repository transaction helper。

建议初始表：

```text
sources
sync_jobs
managed_files
sync_runs
sync_run_items
api_tokens
published_files
```

本阶段只实际启用 Source 所需表，其余表可随对应领域阶段创建，避免预先设计过多 schema。

## Source Domain

定义稳定身份模型：

```text
Source
├── ID
├── Name
├── Type
├── Endpoint
├── Credential reference/config
├── Enabled
├── CreatedAt
└── UpdatedAt
```

协议类型第一阶段：

```text
webdav
```

后续扩展：

```text
s3
sftp
```

## Source Interface

建立协议无关接口，例如：

```go
type Source interface {
    Stat(ctx context.Context, path string) (FileInfo, error)
    List(ctx context.Context, path string) ([]FileInfo, error)
    Open(ctx context.Context, path string, offset int64) (io.ReadCloser, error)
}
```

约束：

* Remote logical path 统一使用 `/`。
* 不暴露协议特定对象给上层。
* v1 Source interface 只提供同步所需的 read-only 能力。
* 不加入 Upload / Delete / Rename / Move。

## Repository

* [ ] Source Create。
* [ ] Source Get。
* [ ] Source List。
* [ ] Source Update。
* [ ] Source Delete。
* [ ] 唯一 ID。
* [ ] Credential 数据不通过普通 API 回显。

## REST API

建议：

```text
GET    /api/v1/sources
POST   /api/v1/sources
GET    /api/v1/sources/:id
PATCH  /api/v1/sources/:id
DELETE /api/v1/sources/:id
POST   /api/v1/sources/:id/test
```

## Web UI

* [ ] Sources 列表。
* [ ] Source 创建。
* [ ] Source 编辑。
* [ ] Source 删除确认。
* [ ] Connection Test。
* [ ] 错误状态展示。

完成标准：

> 可以通过 Web UI / REST 创建一个 WebDAV Source，
> 持久化到 SQLite，并验证远端连接，但尚不执行同步。

---

# v0.3.0 — WebDAV Pull Sync

目标：完成 TinySync 第一条真正可用的端到端同步链路。

```text
WebDAV
   ↓
Source
   ↓
Sync Job
   ↓
Selector
   ↓
Sync Engine
   ↓
Local filesystem
```

## Sync Job Model

建议模型：

```text
SyncJob
├── ID
├── Name
├── SourceID
├── RemoteRoot
├── LocalRoot
├── Mode
├── Include
├── Exclude
├── Enabled
├── CreatedAt
└── UpdatedAt
```

Source 与 Job 必须保持分离。

## Sync Mode

第一版固定两类：

### Copy

```text
remote create  → local create
remote update  → local update
remote delete  → local keep
```

### Mirror

```text
remote create  → local create
remote update  → local update
remote delete  → local delete
```

Mirror 安全规则：

> 只能删除 `managed_files` 明确记录为由当前 Job 管理的文件。

禁止根据：

```text
local_root - remote_listing
```

直接删除未知本地文件。

## Selector

支持：

```text
include
exclude
```

第一版语法：

```text
*
**
?
[]
```

单文件选择等价于精确 include pattern。

建议：

```text
exclude > include
```

即 exclusion 优先。

## Fingerprint

定义协议无关：

```go
type Fingerprint struct {
    Size       int64
    ModifiedAt time.Time
    ETag       string
    Checksum   string
    Version    string
}
```

同步引擎不要假定：

```text
ETag == MD5
```

## Atomic Download

从第一版强制：

```text
remote
  ↓
target.tinysync-part
  ↓
optional verify
  ↓
atomic rename
  ↓
target
```

要求：

* [ ] 临时文件清理。
* [ ] 下载失败不破坏原文件。
* [ ] Context cancellation。
* [ ] 请求 timeout。
* [ ] 基础 retry。
* [ ] 防 path traversal。
* [ ] 防 local root escape。

暂不实现 resume。

## Local Root Ownership

v1：

* [ ] 不允许两个 Job 使用重叠的 local root。
* [ ] Job 删除时明确处理 managed metadata。
* [ ] 不默认删除已经同步到本地的真实文件。

## Manual Run

REST：

```text
POST /api/v1/jobs/:id/run
```

第一版只需要支持手动执行。

## Web UI

* [ ] Jobs 列表。
* [ ] Job Editor。
* [ ] Source picker。
* [ ] Remote root selector。
* [ ] Local root 配置。
* [ ] Copy / Mirror mode。
* [ ] Include / Exclude。
* [ ] Run Now。
* [ ] 当前运行状态。

完成标准：

> 可以配置一个 WebDAV Source + Sync Job，
> 手动将匹配文件稳定、安全地同步到本地目录。

---

# v0.4.0 — Scheduler & Sync History

目标：让同步任务可长期无人值守运行，并具有完整历史可观测性。

## Scheduler

支持：

```text
manual
once
interval
cron
```

配置：

```text
timezone
enabled
schedule
```

## Overlap Policy

v1 默认：

```text
skip
```

即：

> 同一个 Job 上一次运行未完成时，新调度跳过。

未来可考虑：

```text
queue
cancel_previous
```

但不作为初版能力。

## Concurrency

全局：

```text
MaxConcurrentJobs
MaxConcurrentTransfers
```

需要明确：

* Job 并发限制。
* 单 Job 文件下载并发限制。
* Source 连接压力控制。

## Sync Run

```text
sync_runs
├── id
├── job_id
├── trigger
├── status
├── started_at
├── finished_at
├── files_total
├── files_created
├── files_updated
├── files_deleted
├── files_skipped
├── bytes_transferred
└── error
```

## Sync Run Item

必要时保存文件级事件：

```text
sync_run_items
├── run_id
├── path
├── action
├── status
├── bytes
└── error
```

需要控制历史容量，避免 SQLite 无限增长。

## Web UI

* [ ] Scheduler editor。
* [ ] 上次运行。
* [ ] 下次运行。
* [ ] Sync history 列表。
* [ ] MUI Timeline。
* [ ] Run detail。
* [ ] 文件级变更明细。
* [ ] 错误信息。

完成标准：

> TinySync 可以作为常驻 HomeLab 服务自动运行 WebDAV 同步任务，
> 并完整追踪每次同步发生了什么。

---

# v0.5.0 — S3 & SFTP Sources

目标：验证 Source abstraction 能稳定承载多协议。

## S3

支持：

* [ ] endpoint。
* [ ] region。
* [ ] bucket。
* [ ] access key / secret key。
* [ ] path style。
* [ ] prefix/root。
* [ ] List。
* [ ] Stat。
* [ ] Open/Range。
* [ ] ETag / version metadata。

注意：

> S3 ETag 不保证等于 MD5，multipart upload 等场景必须按 opaque fingerprint 处理。

## SFTP

支持：

* [ ] host。
* [ ] port。
* [ ] username。
* [ ] password。
* [ ] private key。
* [ ] host key verification。
* [ ] remote root。
* [ ] Stat。
* [ ] List。
* [ ] Open。

## Capability

如果协议能力开始出现差异，增加显式 capability，而不是在业务层：

```go
switch source.Type
```

例如：

```text
range read
checksum
version
mtime
```

完成标准：

> 同一个 Sync Engine 无需协议分支即可同步 WebDAV、S3 和 SFTP。

---

# v0.6.0 — Remote Browser, Local Files & Publishing

目标：补齐日常文件管理和安全访问能力。

## Remote Browser

只读：

```text
browse
stat
select
download
trigger sync
```

明确不支持：

```text
remote upload
remote delete
remote rename
remote move
```

## Virtualized Browser

针对大目录：

* [ ] 分页/游标。
* [ ] 前端虚拟列表。
* [ ] lazy directory loading。
* [ ] 大目录不一次性加载全部条目。

## Local File Browser

* [ ] 浏览同步根目录。
* [ ] 文件信息。
* [ ] 下载。
* [ ] Range。
* [ ] HEAD。
* [ ] MIME。
* [ ] Content-Disposition。

是否支持本地删除/rename，应独立评估，不默认进入第一版。

## Publishing

Publish 与 Sync Job 分离：

```text
Remote
  ↓
Sync
  ↓
Local File
  ↓
Publish Policy
  ↓
HTTP
```

模型：

```text
published_files
├── id
├── local_path
├── public_path
├── enabled
├── expires_at
└── ...
```

安全要求：

* [ ] 默认 private。
* [ ] path traversal protection。
* [ ] symlink escape protection。
* [ ] root confinement。
* [ ] Range。
* [ ] HEAD。
* [ ] MIME。
* [ ] cache policy。

完成标准：

> 用户可以从 Web UI 浏览远端和本地文件，并选择性将同步后的本地文件通过 HTTP 暴露。

---

# v0.7.0 — Authentication & API Tokens

目标：建立 Web 管理端和程序化 API 的安全边界。

## Web UI Authentication

Web UI 与 API token 分离。

考虑：

```text
session / cookie
```

不要用长期 API token 直接充当浏览器 session。

## API Token

体验类似 LLM provider：

创建时：

```text
显示 raw token 一次
```

数据库只保存：

```text
SHA-256(random high-entropy token)
```

API token 是高熵 secret，不使用慢速 password KDF。

模型：

```text
api_tokens
├── id
├── name
├── prefix
├── token_hash
├── scopes
├── created_at
├── expires_at
├── last_used_at
└── revoked_at
```

## Scopes

第一阶段可以：

```text
read
run
admin
```

后续细化：

```text
sources:read
jobs:read
jobs:run
files:read
...
```

## Web UI

* [ ] Token list。
* [ ] Create。
* [ ] Raw token one-time display。
* [ ] Revoke。
* [ ] Expiration。
* [ ] Last used。
* [ ] Scope selection。

完成标准：

> REST API 可以安全暴露给自动化脚本和本地服务，而不共享 WebUI 登录凭据。

---

# v0.8.0 — MCP Integration

目标：让 Agent / LLM 可以通过 MCP 管理 TinySync 和读取同步文件信息。

原则：

> MCP 是现有 application service 的 adapter，不建立第二套业务实现。

使用官方 Go MCP SDK，并采用 Streamable HTTP。

## Tools

建议：

```text
list_sources
list_jobs
get_job
run_sync
get_sync_run
search_files
get_file_info
```

涉及修改配置的工具应谨慎开放。

## Resources

适合小型文本内容或元数据：

```text
tinysync://files/...
```

大型 binary 不通过 MCP base64 搬运。

正确方式：

```text
MCP
 → metadata / download URL
 → HTTP Range download
```

## Authorization

MCP 复用 API Token / scope 模型。

完成标准：

> Agent 可以发现同步源、查询任务、触发同步、检索本地文件信息，并通过 HTTP 获取大文件。

---

# v0.9.0 — Hardening & Operations

目标：在 v1.0 前集中处理恢复、安全和长期运行问题。

## Sync Reliability

* [ ] Retry policy。
* [ ] Backoff。
* [ ] Transfer timeout。
* [ ] Partial file cleanup。
* [ ] Crash recovery。
* [ ] stale running state recovery。
* [ ] orphan managed file metadata cleanup。
* [ ] idempotent sync。

## Database

* [ ] Migration upgrade testing。
* [ ] Backup。
* [ ] Restore。
* [ ] corruption handling。
* [ ] WAL/checkpoint strategy。
* [ ] graceful shutdown consistency。

## Filesystem

* [ ] Path traversal fuzz/test。
* [ ] symlink escape。
* [ ] case sensitivity。
* [ ] filename compatibility。
* [ ] Windows path handling。
* [ ] permission errors。
* [ ] disk-full behavior。
* [ ] atomic rename boundary。

## Protocol

* [ ] WebDAV interoperability tests。
* [ ] S3-compatible providers。
* [ ] SFTP server differences。
* [ ] transient network failure。
* [ ] large directory behavior。
* [ ] large file behavior。

## Observability

至少形成：

```text
request_id
job_id
run_id
source_id
path
duration
bytes
status
error
```

保持应用日志与 access log 边界清晰。

## Performance

建立基准：

* [ ] 10k files listing。
* [ ] 100k managed metadata。
* [ ] 大文件 transfer。
* [ ] 小文件批量 sync。
* [ ] WebUI large list。
* [ ] SQLite query latency。

不要在有数据前提前复杂优化。

## Test

逐步增加：

* [ ] Repository unit tests。
* [ ] Source contract tests。
* [ ] Sync engine tests。
* [ ] Integration tests。
* [ ] Filesystem safety tests。
* [ ] Scheduler tests。
* [ ] API tests。
* [ ] release smoke。

核心领域代码应成为 coverage 重点。

---

# v1.0.0 — Stable Single-Node Release

v1.0 的含义：

> TinySync 已经是一个稳定、可升级、可恢复的单节点多协议远端文件 Pull/Mirror 服务。

## Required Features

### Sources

* [ ] WebDAV。
* [ ] S3。
* [ ] SFTP。

### Sync

* [ ] Copy。
* [ ] Mirror。
* [ ] include/exclude selectors。
* [ ] atomic download。
* [ ] managed-file ownership。
* [ ] concurrency control。
* [ ] retry / timeout。
* [ ] manual / once / interval / cron。

### Management

* [ ] Web UI。
* [ ] REST API。
* [ ] Remote Browser。
* [ ] Local File Browser。
* [ ] Sync history。
* [ ] API tokens。
* [ ] Publishing。
* [ ] MCP。

### Operations

* [ ] SQLite migrations。
* [ ] Upgrade path。
* [ ] Backup / restore guidance。
* [ ] Six-platform release。
* [ ] GitHub Release。
* [ ] WebDAV mirror。
* [ ] Changelog。
* [ ] Codecov。
* [ ] Cross-platform smoke。

## v1.0 Quality Gate

发布 v1.0 前必须确认：

```text
clean install
upgrade from previous release
database migration
WebDAV end-to-end sync
S3 end-to-end sync
SFTP end-to-end sync
Copy semantics
Mirror safety
scheduler restart recovery
large file transfer
large directory browsing
token authorization
published file confinement
MCP basic workflow
Linux/macOS/Windows smoke
```

---

# Post-v1.0 Candidates

以下能力不进入 v1.0 核心范围，根据真实使用需求决定：

* resumable download。
* bandwidth limit。
* per-source concurrency。
* notification integrations。
* webhook。
* file retention policy。
* richer search/index。
* checksum verification policies。
* encrypted credential store。
* import/export configuration。
* metrics endpoint。
* Prometheus。
* Home Assistant integration。
* CLI remote client。
* mobile-oriented Web UI。

仍不默认规划：

```text
bidirectional sync
distributed sync cluster
remote file editing
generic cloud-drive replacement
```

除非未来项目定位发生明确变化。

---

# Current Focus

当前优先级：

1. 发布并验证 `v0.1.0`。
2. 建立 pure-Go SQLite persistence。
3. 建立 Source domain/interface。
4. 实现 WebDAV Source。
5. 实现 Source Repository + REST API + Web UI。
6. 开始第一个 WebDAV manual pull Sync Job。

下一阶段不应继续扩展工程骨架；从 `v0.2.0` 开始，工作重心正式转向 TinySync 的领域能力。

这份 Roadmap 的一个关键点是：**v0.2.0 不急着同时实现全部数据库表和三个协议，而是先把 SQLite + Source abstraction + WebDAV Source 做扎实；v0.3.0 再引入真正的 Sync Engine。** 这样可以避免 Source、Job、Scheduler、History、Auth 一次性同时建模导致早期架构过度设计。
