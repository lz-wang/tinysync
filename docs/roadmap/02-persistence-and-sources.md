# v0.2.0 — Persistence & Source Domain

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

目标：建立稳定的数据持久化层和协议无关的 Source 模型。

范围边界：v0.2.0 只做 Source 管理链路（SQLite → Repository → 应用服务 →
WebDAV 连接测试 → REST API → Web UI），不进入同步执行；Sync Job、Selector、
Copy / Mirror、下载、调度、S3 / SFTP、文件浏览、API Token 与 MCP 留给后续阶段。

## SQLite

技术选型（已确定）：

- driver：`modernc.org/sqlite`，标准 `database/sql` + 手写 SQL，不引入 ORM；
- DSN 级 PRAGMA 基线：`foreign_keys=ON`、`journal_mode=WAL`、
  `busy_timeout=5000`、`synchronous=NORMAL`、`defensive=ON`、`dqs=OFF`；
- 暂以单连接池运行（`SetMaxOpenConns(1)`），出现写密集场景再重新评估；
- WebDAV adapter 选型：`github.com/emersion/go-webdav`（原生 context-aware
  `Stat` / `ReadDir` / `Open`）。

- [ ] 选择 pure-Go SQLite driver。
- [ ] 建立 `internal/storage`。
- [ ] 数据库自动初始化。
- [ ] Schema migration/version 机制。
- [ ] 数据库备份与升级边界。
- [ ] SQLite pragma 基线。
- [ ] Repository transaction helper。

Schema migration 采用内嵌 SQL + `PRAGMA user_version` 顺序执行，不引入
Goose / Atlas / migrate：新库自动初始化；旧库向前迁移，迁移前用
`VACUUM INTO` 备份；更高版本数据库拒绝启动；迁移失败回滚。

只创建 v0.2.0 所需的 `sources` 表；其余表随对应领域阶段由 migration 补充，
避免预先设计过多 schema：

```text
sync_jobs
managed_files
sync_runs
sync_run_items
api_tokens
published_files
```

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
    Open(ctx context.Context, path string) (io.ReadCloser, error)
}
```

`Open` 暂不引入 `offset` 参数；range / resume 属于后续同步阶段，出现实际调用者再扩展接口。

约束：

- Remote logical path 统一使用 `/`。
- 不暴露协议特定对象给上层。
- v1 Source interface 只提供同步所需的 read-only 能力。
- 不加入 Upload / Delete / Rename / Move。

## Repository

- [ ] Source Create。
- [ ] Source Get。
- [ ] Source List。
- [ ] Source Update。
- [ ] Source Delete。
- [ ] 唯一 ID。
- [ ] Credential 数据不通过普通 API 回显。

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

Connection Test 真正执行 WebDAV `PROPFIND`（`Stat("/")`），超时 10s；
测试完成（含连接失败）返回 200 与 `ok` / `latency_ms`（失败附 `error`）；
仅 Source 不存在（404）、请求非法（400）、存储层故障（500）走 REST 错误。
PATCH 的 `password` 语义固定为：字段缺省保留现有密码，空串清除，非空替换。

## Web UI

- [ ] Sources 列表。
- [ ] Source 创建。
- [ ] Source 编辑。
- [ ] Source 删除确认。
- [ ] Connection Test。
- [ ] 错误状态展示。

## 完成标准

> 可以通过 Web UI / REST 创建一个 WebDAV Source，
> 持久化到 SQLite，并验证远端连接，但尚不执行同步。
