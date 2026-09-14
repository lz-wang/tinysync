# v0.3.0 — WebDAV Pull Sync

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

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

- [ ] 临时文件清理。
- [ ] 下载失败不破坏原文件。
- [ ] Context cancellation。
- [ ] 请求 timeout。
- [ ] 基础 retry。
- [ ] 防 path traversal。
- [ ] 防 local root escape。

暂不实现 resume。

## Local Root Ownership

v1：

- [ ] 不允许两个 Job 使用重叠的 local root。
- [ ] Job 删除时明确处理 managed metadata。
- [ ] 不默认删除已经同步到本地的真实文件。

## Manual Run

REST：

```text
POST /api/v1/jobs/:id/run
```

第一版只需要支持手动执行。

## Web UI

- [ ] Jobs 列表。
- [ ] Job Editor。
- [ ] Source picker。
- [ ] Remote root selector。
- [ ] Local root 配置。
- [ ] Copy / Mirror mode。
- [ ] Include / Exclude。
- [ ] Run Now。
- [ ] 当前运行状态。

## 完成标准

> 可以配置一个 WebDAV Source + Sync Job，
> 手动将匹配文件稳定、安全地同步到本地目录。
