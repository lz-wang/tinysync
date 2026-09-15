# v0.2.0 — Persistence & Source Domain

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本页勾选项表示仓库中已有
实现与测试；发布状态以 tag、Release workflow 与发行资产验收为准。

目标：建立稳定的数据持久化层和协议无关的 Source 模型。

范围边界：v0.2.0 只做 Source 管理链路（SQLite → Repository → 应用服务 →
WebDAV 连接测试 → REST API → Web UI），不进入同步执行；Sync Job、Selector、
Copy / Mirror、下载、调度、S3 / SFTP、文件浏览、API Token 与 MCP 留给后续阶段。

## SQLite

技术选型（已实现）：

- driver：`modernc.org/sqlite`（v1.58.0），标准 `database/sql` + 手写 SQL，不引入 ORM；
- DSN 级 PRAGMA 基线：`foreign_keys=ON`、`journal_mode=WAL`、
  `busy_timeout=5000`、`synchronous=NORMAL`
  （`defensive` / `dqs` 在该 driver 当前构建中未注册，待支持后补入）；
- 单连接池运行（`SetMaxOpenConns(1)`），出现写密集场景再重新评估；
- WebDAV adapter 选型：`github.com/emersion/go-webdav`（原生 context-aware
  `Stat` / `ReadDir` / `Open`）。

- [x] 选择 pure-Go SQLite driver：`modernc.org/sqlite`。
- [x] 建立 `internal/storage`（`database.go` / `migrate.go` / `tx.go`）。
- [x] 数据库自动初始化（`storage.Open` + `storage.Migrate`，接入 `app.Run`）。
- [x] Schema migration/version 机制（内嵌 `migrations/*.sql` + `PRAGMA user_version`）。
- [x] 数据库备份与升级边界（升级前 `VACUUM INTO` 备份到 `<datadir>/backups/`，
      0700/0600 权限；更高版本数据库拒绝启动；迁移失败回滚）。
- [x] SQLite pragma 基线（DSN 级 per-connection 下发，测试断言生效值）。
- [x] Repository transaction helper（`storage.WithTx`）。

数据库文件为 `<datadir>/tinysync.db`，POSIX 权限 0600。schema 按领域阶段演进：
v0.2.0 只创建 `sources` 表，其余表由对应阶段 migration 补充：

```text
sync_jobs
managed_files
sync_runs
sync_run_items
api_tokens
published_files
```

## Source Domain

定义稳定身份模型（`internal/source/model.go`）：

```text
Source
├── ID（src_<128-bit random hex>，crypto/rand 生成）
├── Name（大小写不敏感唯一）
├── Type（当前仅 webdav）
├── Endpoint
├── Username
├── PasswordSet（布尔；领域对象不含密码明文）
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

建立协议无关接口（`internal/source/remote.go`），WebDAV 实现见
`internal/source/webdav/client.go`：

```go
type Source interface {
    Stat(ctx context.Context, path string) (FileInfo, error)
    List(ctx context.Context, path string) ([]FileInfo, error)
    Open(ctx context.Context, path string) (io.ReadCloser, error)
}
```

`Open` 暂不引入 `offset` 参数；range / resume 属于后续同步阶段，出现实际调用者再扩展接口。

约束：

- Remote logical path 统一使用 `/`。
- 不暴露协议特定对象给上层。
- v1 Source interface 只提供同步所需的 read-only 能力。
- 不加入 Upload / Delete / Rename / Move。
- HTTP 安全边界：15s 超时、最多 5 次重定向、拒绝跨 host 重定向与
  HTTPS→HTTP 降级；凭据走独立字段，拒绝内嵌 userinfo 的 endpoint。

## Repository

实现见 `internal/source/sqlite/repository.go`；普通读取路径不读取 password 列，
仅以 `(password != '')` 给出 PasswordSet，`GetPassword` 是唯一凭据读取入口。

- [x] Source Create。
- [x] Source Get。
- [x] Source List（按 name 大小写不敏感稳定排序）。
- [x] Source Update。
- [x] Source Delete（硬删除）。
- [x] 唯一 ID。
- [x] Credential 数据不通过普通 API 回显。

## 应用服务

`internal/source/service.go`：REST / Web UI / 后续 MCP 共用的应用服务层。
统一处理校验、ID 与时间戳生成、password 三态更新语义（缺省保留、空串清除、
非空替换）与连接测试（真正执行 WebDAV `PROPFIND` / `Stat("/")`，10s 超时；
连接失败返回结果而非错误）。

## REST API

已实现（`internal/api/source.go`）：

```text
GET    /api/v1/sources
POST   /api/v1/sources
GET    /api/v1/sources/:id
PATCH  /api/v1/sources/:id
DELETE /api/v1/sources/:id
POST   /api/v1/sources/:id/test
```

Connection Test 测试完成（含连接失败）返回 200 与 `ok` / `latency_ms`
（失败附 `error`）；仅 Source 不存在（404）、请求非法（400）、存储层故障（500）
走 REST 错误。API 表示层不含 `password` 字段，凭据状态只以 `password_set` 暴露。

## Web UI

实现见 `web/src`（`app/AppShell.tsx`、`pages/SourcesPage.tsx`、
`features/sources/SourceDialog.tsx`、`features/sources/DeleteSourceDialog.tsx`）；
引入 react-router-dom 与服务端 SPA deep-link fallback 对齐。

- [x] Sources 列表。
- [x] Source 创建。
- [x] Source 编辑（密码不回填；空白不发送、输入替换、Clear 清除）。
- [x] Source 删除确认（明确提示不删除远端文件）。
- [x] Connection Test（Testing → Success: <latency> ms / Failed: <error>）。
- [x] 错误状态展示。

## 完成标准

> 可以通过 Web UI / REST 创建一个 WebDAV Source，
> 持久化到 SQLite，并验证远端连接，但尚不执行同步。

本地验收已完成：Go 单测（storage / source / api 全覆盖）、原生 smoke
（真实 binary + SQLite + REST + 跨重启持久化）、浏览器人工验收（创建、刷新、
编辑、连接测试、删除、进程重启）。

发布验收已完成：`v0.2.0` tag 指向 a0859d3，Release workflow 成功
（2026-09-15，run 34977460579），三平台原生 smoke 与 `make ci` 通过，
六个发行档及 `checksums.txt` 已核对到位；WebDAV 镜像与 Pushover 通知
独立验收通过。
