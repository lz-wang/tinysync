# v0.2.0 — Persistence & Source Domain

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

目标：建立稳定的数据持久化层和协议无关的 Source 模型。

## SQLite

- [ ] 选择 pure-Go SQLite driver。
- [ ] 建立 `internal/storage`。
- [ ] 数据库自动初始化。
- [ ] Schema migration/version 机制。
- [ ] 数据库备份与升级边界。
- [ ] SQLite pragma 基线。
- [ ] Repository transaction helper。

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
