# v0.3.0 — WebDAV Pull Sync

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本文件是 v0.3.0 的
实现契约：路径空间、Fingerprint、schema、删除授权、同步算法、Selector、
LocalRoot 边界、原子下载、手动运行状态、REST API 与 Web UI 的设计均已冻结，
实现与本契约冲突时以本文件为准并先行修订本文件。

目标：完成第一条真正可使用的、安全的、手动触发的 WebDAV → Local 单向同步链路。

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

明确不做（推迟到后续阶段）：cron / interval 调度、持久化同步历史
（`sync_runs` / `sync_run_items` 属于 v0.4）、并行传输、并行 Job、
resume / Range 下载、S3、SFTP、远端写操作、远端文件浏览器、本地文件浏览器、
HTTP 发布、认证 / Token、MCP。v0.3 坚持单机、简单、可恢复、强本地数据保护。

## 核心契约一：Source logical path 统一路径空间

无论实际 endpoint 前缀是什么（如 `https://nas.example.com/dav/user/`），
上层永远只看到 Source-relative logical path：

```text
Server path:  /dav/user/docs/report.pdf
Source path:  /docs/report.pdf
```

`"/"` 永远表示 Source root，而不是服务器 root。RemoteRoot、Selector、
`managed_files.remote_path`、Mirror 比较全部基于这套语义，
三种协议 adapter 不得引入不同路径基准。

adapter 必须完成双向转换并拒绝越界：

```text
request logical path  → endpoint-relative WebDAV path
WebDAV response href  → Source-relative logical path（拒绝越出 endpoint root）
```

v0.2 的 `source.FileInfo.Path` 声明为绝对逻辑路径，但 WebDAV adapter 的
`toFileInfo()` 仍直接透传底层 href 解析结果；Connection Test 不消费该字段，
v0.3 的 List 消费，因此必须首先修正，并补充 endpoint 前缀、Unicode、
percent-encoding 与恶意 href 的测试。

## 核心契约二：Fingerprint 协议无关模型

```go
type Fingerprint struct {
    Size       int64
    ModifiedAt time.Time
    ETag       string
    Checksum   string
    Version    string
}

type FileInfo struct {
    Path        string
    IsDir       bool
    Fingerprint Fingerprint
}
```

WebDAV v0.3 填 `Size` / `ModifiedAt` / `ETag`，`Checksum` / `Version` 留空；
未来 S3 使用 Version，其他协议可使用 checksum。

变更判定优先级（高 → 低）：

```text
Version
   ↓
Checksum
   ↓
ETag + Size
   ↓
Size + ModifiedAt
   ↓
无法判断 → 视为 changed
```

ETag 永远是 opaque token，绝不假定它是 MD5。

## 数据库设计

v0.3 只新增两张表；不创建 `sync_runs` / `sync_run_items`（属 v0.4）。

`0002_sync_jobs.sql`：

```text
sync_jobs
├── id                  TEXT PK
├── name                TEXT COLLATE NOCASE UNIQUE
├── source_id           TEXT FK sources(id) RESTRICT
├── remote_root         TEXT
├── local_root          TEXT
├── mode                copy | mirror
├── include_patterns    JSON TEXT
├── exclude_patterns    JSON TEXT
├── enabled             INTEGER
├── created_at          INTEGER
└── updated_at          INTEGER
```

```text
managed_files
├── job_id              TEXT FK sync_jobs(id) CASCADE
├── remote_path         TEXT
├── local_rel_path      TEXT
├── state               pending | synced
├── remote_size         INTEGER
├── remote_mtime_ns     INTEGER NULL
├── remote_etag         TEXT
├── remote_checksum     TEXT
├── remote_version      TEXT
├── local_size          INTEGER NULL
├── local_mtime_ns      INTEGER NULL
├── updated_at          INTEGER
├── PK(job_id, remote_path)
└── UNIQUE(job_id, local_rel_path)
```

路径存储约定：

```text
remote_path:     /docs/report.pdf   （Source-relative，/ 开头）
local_rel_path:  docs/report.pdf    （相对 LocalRoot，统一 / 分隔）
```

不存绝对本地路径；绝对路径由 `Job.LocalRoot + local_rel_path` 实时安全解析。

## Managed Files 安全语义（删除授权）

`managed_files` 是唯一的删除授权来源。

### Copy

远端删除：

```text
managed metadata → 删除
local file       → 保留
```

该文件从此变成普通本地文件，TinySync 不再拥有它。

### Mirror

远端删除：

```text
只有 managed_files 中属于本 Job 的文件
        ↓
允许删除 local file
        ↓
再删除 managed metadata
```

绝对禁止 `walk(local_root) - remote_files` 差集删除。

### Selector 变更导致的 relinquish

「原来 include、已同步、现在 exclude」的文件不能按 Mirror remote-delete
处理——文件仍在远端，只是不再被选择。正确行为与 Copy 的远端删除一致：

```text
managed metadata → 删除
local file       → 保留
```

因此 scanner 必须同时产出 `all remote files` 与 `selected remote files`
两个集合，以区分 remote deleted 与 still remote but excluded。

## 同步执行算法

固定顺序：

```text
1. Resolve Job / Source
2. 完整扫描 RemoteRoot
3. Selector
4. 读取 managed_files
5. 构造完整 Sync Plan
6. 本地路径与冲突 preflight
7. 执行 create/update
8. 所有传输成功
9. 执行 metadata relinquish
10. Copy/Mirror remote-delete
11. 完成
```

强制安全规则：

> 只要 remote scan 没有完整成功，Mirror 不执行任何 delete。
> 只要本轮任意 create/update 失败，本轮也不执行后续 Mirror delete。

失败保持可恢复状态，下一次 Run 继续收敛；避免「一半下载成功 + 旧文件被删」。

## Selector

使用 `github.com/bmatcuk/doublestar/v4`（v4.10.0）。`Match` 以 `/` 分隔，
匹配对象始终是相对 RemoteRoot 的路径（`RemoteRoot=/photos` 时远端文件
`/photos/2026/a.jpg` 的 selector input 为 `2026/a.jpg`）。

```text
支持语法：*  **  ?  []
include == []  → include all
exclude == []  → exclude none
exclude        → 永远优先于 include
```

第一版不做 selector directory pruning：即使 `exclude = ["tmp/**"]`，
scanner 仍完整遍历再过滤文件，避免 glob 推导错误导致 Mirror 误判远端消失。

## LocalRoot 安全边界

`LocalRoot` 校验：必须 absolute、已存在、是 directory，经
`filepath.Abs` / `Clean` / `EvalSymlinks` 归一。

禁止重叠：

```text
Job A local_root == Job B local_root        ×
Job A local_root 是 Job B 的父/子目录        ×
local_root 与 DataDir 互相包含              ×
```

每个 remote path 映射本地路径必须经过：

```text
remote relative path
        ↓ filepath.FromSlash
Join(LocalRoot)
        ↓ filepath.Rel(LocalRoot, target)
确认不以 .. 逃逸
```

并用 `Lstat` 检查已有路径组件，禁止经本地 symlink 跳出 LocalRoot。
抵御并发 symlink 替换的完整 `openat` 模型属 v0.9 hardening，v0.3 不做。

## Atomic Download

```text
remote Open
    ↓ target-dir/.tinysync-part-<random>
io.Copy
    ↓ size verify
file.Sync
    ↓ Close
Rename/replace target
```

临时文件与 target 必须同目录（不允许系统 `/tmp`，避免跨 filesystem）。
普通失败、context cancellation、retry 前必须 remove temp，不得先删除旧 target。

跨平台精确定义：Linux/macOS 为 same-filesystem atomic rename/replace；
Windows 为 same-directory replace semantics，不夸大为 OS 保证的严格原子操作。

## WebDAV HTTP timeout

v0.2 的 `http.Client{Timeout: 15s}` 覆盖整个 response body 生命周期，
大文件下载必然失败。v0.3 改为：

```text
Client.Timeout = 0
Transport 控制：Dial timeout / TLS handshake timeout / ResponseHeaderTimeout / Idle connections
```

Connection Test 原有的 `context.WithTimeout(..., 10s)` 保留；
真正的下载由 Run context + retry 控制生命周期。

## Manual Run（内存状态）

```text
POST /api/v1/jobs/:id/run     → 202 Accepted
GET  /api/v1/jobs/:id/status
```

状态机：`idle → running → succeeded | failed`。运行记录
（run_id、started_at、finished_at、files_total/created/updated/deleted/skipped、
bytes_transferred、error）只存内存，进程重启后 status 回到 idle——
持久化历史明确属于 v0.4。

并发策略：v0.3 全局固定只允许一个同步运行，`running Job A + Run Job B → 409`；
不 queue、不 parallel。MaxConcurrentJobs / Overlap Policy 属 v0.4。

## REST API

```text
GET    /api/v1/jobs
POST   /api/v1/jobs
GET    /api/v1/jobs/:id
PATCH  /api/v1/jobs/:id
DELETE /api/v1/jobs/:id
POST   /api/v1/jobs/:id/run
GET    /api/v1/jobs/:id/status
```

Run 状态码：`202 started`、`404 job not found`、`409 job disabled`、
`409 source disabled`、`409 another run active`。

Source 被 Job 引用时 `DELETE /api/v1/sources/:id → 409`，
数据库层以 `FOREIGN KEY source_id REFERENCES sources(id) ON DELETE RESTRICT`
约束兜底，不产生 500。Job 删除只清 managed metadata，真实本地文件永远保留。

## Web UI

新增 `/jobs` 页面。配置字段：Name、Source、Remote Root、Local Root、
Mode（Copy / Mirror）、Include、Exclude、Enabled。

Remote Root v0.3 只做路径文本输入（`/`、`/photos`、`/backup/docs`），
不做远端文件浏览器（属 v0.6）。Patterns 用 multiline 文本（一行一个）。

运行区：Run Now / Running... / Succeeded / Failed；运行中每 1～2 秒
polling `GET /jobs/:id/status`，不引入 WebSocket / SSE。

## 实施顺序

19 个 commit，每个保持测试通过：

1. `docs: 固化 v0.3.0 WebDAV Pull Sync 契约`（本文件）
2. `fix(storage): 加固 SQLite DSN 与迁移备份`——SQLite URI 正确构造、特殊 datadir、备份名高精度/随机后缀、migration retry 测试
3. `feat(storage): 增加 Sync Job 与 managed files schema`——`0002_sync_jobs.sql`、真实 v1→v2 migration/backup 测试（v0.2.0 库含 Source + password 升级后完整保留，备份可重开且 user_version==1）
4. `feat(source): 固化远端 logical path 与 fingerprint`——Fingerprint、ETag、href→Source logical path、拒绝 endpoint root escape、List 去掉 self
5. `fix(webdav): 调整同步下载 HTTP timeout`——Transport 级 timeout 替代 15s 整体超时
6. `feat(syncjob): 建立 Sync Job 领域模型`
7. `feat(syncjob): 实现 SQLite Job repository`
8. `feat(syncjob): 增加 Job 应用服务与 LocalRoot ownership`
9. `feat(syncjob): 增加 include exclude selector`
10. `feat(syncjob): 增加路径安全与 remote scanner`
11. `feat(syncjob): 增加同步 planner`
12. `feat(syncjob): 增加原子 downloader 与 retry`
13. `feat(syncjob): 实现 Copy Mirror sync engine`
14. `feat(syncjob): 增加手动运行与内存状态`
15. `feat(api): 增加 Sync Job API`
16. `test: 增加 WebDAV Pull 端到端与持久化 smoke`
17. `feat(web): 增加 Jobs 管理界面`
18. `feat(web): 增加 Run Now 与实时状态`
19. `docs: 完成 v0.3.0 实现记录`

随后单独 `chore(release): prepare v0.3.0` 进入发布门禁
（`make ci` / `make build` / native smoke → 远端 workflow 全绿 → tag v0.3.0 →
Release 验收 → `docs: 记录 v0.3.0 发布验收并推进 v0.4.0`）。

## 核心同步矩阵（engine tests 核心）

| 场景 | Copy | Mirror |
| --- | --- | --- |
| remote new | download | download |
| remote changed | update | update |
| remote unchanged | skip | skip |
| local managed file missing | repair | repair |
| remote deleted | keep local + unmanage | delete managed local |
| selector now excludes | keep local + unmanage | keep local + unmanage |
| unknown local file | never overwrite | never overwrite/delete |
| remote scan failed | abort | abort, **zero delete** |
| transfer failed | fail | fail, **zero later delete** |
| context cancelled | temp cleanup | temp cleanup |
| local-root escape | reject | reject |
| symlink escape | reject | reject |

## 实现记录

实现路径：

- 领域与引擎：`internal/syncjob`（`model.go`、`selector.go`、`scanner.go`、
  `planner.go`、`downloader.go`、`engine.go`、`runner.go`、`service.go`）。
- 持久化：`internal/storage/migrations/0002_sync_jobs.sql`、
  `internal/syncjob/sqlite/repository.go`。
- REST API：`internal/api/job.go`（CRUD + run + status）、`internal/api/source.go`
  （Source 删除保护）；装配与优雅关闭见 `internal/app/app.go`。
- Web UI：`web/src/pages/JobsPage.tsx`、`web/src/features/jobs/JobDialog.tsx`、
  `web/src/features/jobs/DeleteJobDialog.tsx`。
- 端到端与 smoke：`internal/e2e/webdav_pull_test.go`（真实 x/net/webdav
  服务端 + SQLite + 临时本地目录）、`scripts/smoke.sh`（Job binary smoke）。

交付清单：

- [x] Source logical path 统一路径空间（href 双向转换、拒绝越界、List 去自）。
- [x] Fingerprint 协议无关模型（WebDAV 填 Size / ModifiedAt / ETag）。
- [x] `0002_sync_jobs.sql`：`sync_jobs` + `managed_files`，真实 v1→v2 迁移与备份测试。
- [x] Sync Job 领域模型、SQLite Repository（FK RESTRICT / CASCADE、name NOCASE UNIQUE）。
- [x] Job 应用服务：LocalRoot 归一与归属保护（Job 间、DataDir 重叠拒绝）、
      mapping 变更安全释放 metadata。
- [x] Selector：doublestar v4、include all 缺省、exclude 优先、不做目录剪枝。
- [x] Remote scanner：递归完整快照、RemoteRoot-relative 映射、symlink/escape 防护。
- [x] Planner 与 preflight：未知本地文件冲突记为跳过，永不覆盖；确定性排序。
- [x] 原子下载：同目录临时文件、大小校验、Sync/Close/Rename、瞬时错误退避重试、取消清理。
- [x] Sync Engine：核心同步矩阵全覆盖（含扫描失败零删除、传输失败禁删、
      Copy 保留 / Mirror managed-only 删除、relinquish 语义）。
- [x] 手动运行：异步 Run、全局单运行（409）、run ID 与内存状态机、
      root context、Shutdown 取消并等待退出。
- [x] REST API：CRUD + run（202/404/409）+ status；Source 被 Job 引用时删除 409。
- [x] Web UI：`/jobs` 列表与编辑器（Source 选择、模式、multiline patterns）、
      Run Now、1.5s 轮询实时状态、统计与错误展示。
- [x] 端到端与持久化 smoke：跨重启 Job / managed 保留、状态回 idle、
      重启后继续收敛；smoke.sh 覆盖 Job binary 链路。

## 完成标准

> 完成第一条真正可使用的、安全的、手动触发的 WebDAV → Local 单向同步链路：
> 手动执行 Copy / Mirror，支持选择器、原子下载与本地文件归属保护。

本地验收已完成：`make check` 全绿（Go 全量测试、vet、Biome、TypeScript）；
`make build` 通过；核心同步矩阵 engine tests、真实 WebDAV + SQLite 端到端、
跨重启持久化、原生 smoke（含 Job binary 链路）全部通过；浏览器人工验收
（创建 Job、Run Now、Succeeded 统计、Failed 错误展示、运行状态跨刷新保持）。

发布验收待执行：`make ci` / 远端三平台 smoke / 六平台构建 / `v0.3.0` tag /
Release 资产核对。当前状态：**实现完成，发布待验收**。

## 端到端验收

不依赖 mock：`httptest` WebDAV server + real SQLite + `t.TempDir` local root +
Source + SyncJob → Run。序列：a.txt=v1 同步成功 → 再 Run skipped →
远端改 v2 → updated → 远端删除后 Copy 保留本地；Mirror Job 删除 managed
文件且手动创建的 unknown.txt 保留。

## 完成标准

> 可以配置一个 WebDAV Source + Sync Job，
> 手动将匹配文件稳定、安全地同步到本地目录。
