# v0.6.0 — Remote Browser, Local Files & Publishing

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本文件是 v0.6.0 的
实现契约：Remote 分页抽象、Local Browser 的 Job namespace 边界、
Publish Policy 模型、公开 Serving 语义、路径安全原语与 Web UI 结构均已
冻结，实现与本契约冲突时以本文件为准并先行修订本文件。

目标：建立统一的「远端文件访问 → 本地文件访问 → 显式发布」链条，
让用户从 Web UI 分页浏览远端（三协议）与本地（Job namespace）文件并
下载，把同步后的 managed 本地文件显式发布为受控 HTTP URL——同时保持
v0.5 已建立的协议抽象不被破坏。

```text
HTTP / WebUI
     ↓
Browser Service（internal/browser）        Publish Service（internal/publish）
     ↓                                            ↓
source.Service.OpenRemote()                filesafe（受限路径 + serving 共享实现）
     ↓                                            ↓
source.Remote                              Local Files（Job.LocalRoot canonical）
     ↓                                            ↑
WebDAV / S3 / SFTP                         Sync（既有链路，零改动）
```

依赖方向保持单向：

```text
api ───────→ browser ─────→ source
 │               │
 │               └───────→ syncjob
 │
 └─────────→ publish
                 │
                 └───────→ filesafe

source adapters ──→ source.Remote
```

Browser 复用 `source.Remote` 与 `source.Service.OpenRemote()`，不为
WebDAV / S3 / SFTP 再造三套路由或三个 browser；**Publish 不依赖任何
具体协议 adapter**。Remote Browser 不建立第二套文件协议。

## 范围

| 项目 | v0.6.0 |
| --- | --- |
| Remote Browser | list / stat / download（复用 source.Remote） |
| `Remote.List` 签名 | 改为分页：`ListOptions` + `FilePage` |
| S3 分页 | 协议原生（cursor ↔ ContinuationToken） |
| WebDAV / SFTP 分页 | 单层完整枚举后 adapter 边界切片分页 |
| Local Browser | 以 Job 为 namespace，只读 list / stat / download |
| managed 标记 | Local 条目携带 `managed` 布尔 |
| Publish | 单文件 Policy，CRUD + `/published/*path` serving |
| Range | Local download 与 Published serving 支持；Remote download 不做 |
| symlink | 显示不跟随；serving 层 EvalSymlinks + root confinement |
| 本地 delete / rename / write | **不做** |
| directory publish / index / listing | **不做** |
| selection persistence | **不做**（remote select 是纯 UI 行为） |
| 认证 | **不做**（v0.7） |
| pkg/sftp v1 → v2 迁移 | **不做**（不把 UI 阶段变成协议迁移阶段） |
| 可配置 cache policy | **不做**（第一版固定 `Cache-Control: no-store`） |

## Remote 分页契约

`Remote.List` 是 v0.6 最大的结构性变化：一层目录不再要求全部装入
单个 `[]FileInfo`。

```go
type ListOptions struct {
    Limit  int    // 默认 100，最大 500；<=0 取默认，>500 截断为 500
    Cursor string // adapter-owned opaque token；空串表示从头开始
}

type FilePage struct {
    Entries    []FileInfo
    NextCursor string // 空串 = EOF
}

type Remote interface {
    Stat(ctx context.Context, path string) (FileInfo, error)
    List(ctx context.Context, path string, opts ListOptions) (FilePage, error)
    Open(ctx context.Context, path string) (io.ReadCloser, error)
    Close() error
}
```

语义冻结：

```text
cursor 是 adapter-owned opaque token：调用方不解析、不改写、
        不假设其内部结构，只能原样回传
limit   默认 100；上限 500（REST 查询参数同规则，越界返回 400）
空 next_cursor = EOF；非空 = 还有下一页
Entries 保证为空时 NextCursor 必为空（不允许空页死循环）
```

各协议映射：

| 协议 | cursor | limit | 说明 |
| --- | --- | --- | --- |
| S3 | ContinuationToken | MaxKeys | 真正的 remote-native pagination |
| WebDAV | opaque offset token | 内存切片 | 单层 PROPFIND 全量后切片 |
| SFTP | opaque offset token | 内存切片 | `ReadDir` 全量后切片 |

WebDAV/SFTP 的已知限制（如实记录，不宣称协议级 cursor）：当前
`github.com/pkg/sftp` v1 的 `ReadDir` 与 WebDAV `Depth:1 PROPFIND`
都只提供整层目录读取，没有持久目录游标；adapter 在协议边界完成
一次单层枚举后按 limit 切片分页返回。**协议层的单层枚举仍是一次
全量读取，分页约束的是 adapter 输出与 REST 响应的条目数与内存
峰值**。为此迁移 pkg/sftp v2（`Dir.ReadDir(n)`）不在 v0.6 范围。

同步引擎同步改为分页循环，不感知协议差异：

```text
List(page)
   ↓ process entries
NextCursor != "" → continue
```

`internal/syncjob/scanner.go` 是唯一调用方改造点；三协议
`runCommonSyncScenario` E2E 必须完全不变。

## Local Browser：以 Job 为 namespace

不提供任意本地路径浏览。本地文件的唯一入口是 Job：

```text
job ID
  ↓
Job.LocalRoot（创建时已 canonicalize：abs + EvalSymlinks）
  ↓
logical path（/ 分隔、相对 LocalRoot）
  ↓
safeResolve(LocalRoot, path)   ← filesafe 统一实现
  ↓
filesystem
```

LocalRoot 既有的 ownership 规则（真实存在、canonicalize、Job 之间
及与 datadir 不重叠）全部自然复用。浏览器可以看到 **LocalRoot 下
真实存在的所有条目**，包括非 TinySync 管理的文件。

managed / unmanaged 定义：

```text
managed     该相对路径出现在本 Job 的 managed_files 记录中
            （state 任意；Copy relinquish 与 Mirror 删除会移除记录，
            对应文件随之变为 unmanaged）
unmanaged   目录中原有文件、relinquish 后保留的文件等其余条目
```

不能假定 LocalRoot 中所有文件都是 managed：Copy 模式 relinquish 后
本地文件保留、managed metadata 删除，是既有语义。

条目模型在公共字段外增加 `managed`：

```json
{
  "path": "/foo.txt",
  "name": "foo.txt",
  "kind": "file",
  "size": 1024,
  "modified_at": "2026-09-17T00:00:00Z",
  "managed": true
}
```

`kind` 取值：`file` / `directory` / `symlink` / `other`。

## symlink 策略

Local Browser 层（Lstat 视角，不跟随）：

```text
普通文件       可 stat / download / publish
普通目录       可进入
symlink        显示 kind=symlink；不可进入、不可 download、不可 publish
其他类型       显示 kind=other；不可操作
```

Serving 层（download 与 published 共用的防御纵深）：

```text
candidate
 ↓
EvalSymlinks（解析全部组件）
 ↓
filepath.Rel(root, resolved)
 ↓
必须仍在 root 内，且 resolved 是普通文件
```

两层各挡一类威胁：Browser 层拒绝「条目自身是 symlink」的操作请求
（无论指向 root 内还是外）；Serving 层防御「父目录组件中的 symlink」
逃逸。Path traversal（`..`、URL encoded、绝对路径注入）在 logical
path 校验层直接拒绝。写路径既有的 `rejectSymlinkComponents`
（逐级 Lstat，防 TOCTOU 覆盖）不变，与本阶段读路径策略并存。

## Publish Policy：单文件、canonical local path、与 Job 生命周期解耦

数据流不变：

```text
Remote → Sync → Local File → Publish Policy → HTTP
```

Schema（migration `0006_published_files.sql`，时间戳沿用项目
UnixMilli INTEGER 惯例）：

```sql
CREATE TABLE published_files (
    id          TEXT PRIMARY KEY,
    local_path  TEXT NOT NULL,
    public_path TEXT NOT NULL UNIQUE,
    enabled     INTEGER NOT NULL CHECK (enabled IN (0,1)),
    expires_at  INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
```

领域模型：

```go
type PublishedFile struct {
    ID         string
    LocalPath  string     // canonical 绝对路径（创建时 EvalSymlinks）
    PublicPath string     // /published 下的唯一公开路径
    Enabled    bool
    ExpiresAt  *time.Time // nil = 永不过期
    CreatedAt  time.Time
    UpdatedAt  time.Time
}
```

关键冻结点：

1. **API 不接受任意 `local_path`**。创建输入是
   `job_id + path`（LocalRoot 内逻辑路径）+ `public_path` +
   `enabled` + `expires_at`；只有 Service 能把它转换为 canonical
   `local_path`。API 无法借 Publish 浏览任意主机文件。
2. **创建校验链**（Service 内执行）：

   ```text
   Job.LocalRoot + logical path
     ↓ safeResolve（root confinement + symlink 拒绝）
     ↓ 必须是普通文件（Lstat，非 symlink / 非目录）
     ↓ managed == true（首次创建强制；不发布 Job 目录里的未知私人文件）
     ↓ canonical local_path（EvalSymlinks 后的绝对路径）
     ↓ published_files
   ```

3. **`local_path` immutable**。PATCH 只允许 `public_path` /
   `enabled` / `expires_at`；要换文件就新建 Policy。Job 后续修改
   LocalRoot 不会把既有 URL 静默指向一个新文件——不存在隐式
   retarget。
4. **生命周期与 Job 解耦**：Policy 持有 canonical 文件路径而非
   `job_id + rel_path` 长期绑定。Copy relinquish 后，只要本地文件
   还存在，显式创建过的 Publish 仍然有效；Mirror 真正删除文件后，
   Published URL 自然 404。
5. **只发布单文件**：不支持 publish directory、directory index、
   recursive publish、published 目录列举。`local_path` 指向目录的
   Policy 无法创建； serving 侧目录一律 404。

## REST API 契约

### Remote Files

```http
GET  /api/v1/sources/:id/files?path=/&limit=100&cursor=...
GET  /api/v1/sources/:id/files/stat?path=/foo.txt
GET  /api/v1/sources/:id/files/download?path=/foo.txt
```

列表响应：

```json
{
  "path": "/photos",
  "entries": [
    {
      "path": "/photos/2026",
      "name": "2026",
      "kind": "directory",
      "size": 0,
      "modified_at": null
    }
  ],
  "next_cursor": "..."
}
```

### Local Files

```http
GET  /api/v1/jobs/:id/files?path=/&limit=100&cursor=...
GET  /api/v1/jobs/:id/files/stat?path=/foo.txt
GET  /api/v1/jobs/:id/files/download?path=/foo.txt
HEAD /api/v1/jobs/:id/files/download?path=/foo.txt
```

Local 条目额外携带 `managed` 布尔（见上）。

### 错误映射（Remote 与 Local 一致）

```text
invalid path / limit 越界      400
source / job not found         404
file not found                 404
remote failure                 502
request cancelled              请求终止（客户端断开）
```

### Publish Policy

```http
GET    /api/v1/published-files
POST   /api/v1/published-files
PATCH  /api/v1/published-files/:id
DELETE /api/v1/published-files/:id
```

`PATCH` 只接受 `public_path` / `enabled` / `expires_at`；
`local_path` 与 `job_id` 不可变。`public_path` 冲突返回 `409`。

### Public Serving

```http
GET  /published/*path
HEAD /published/*path
```

- 必须是 Gin 显式注册的路由，**不得**落入 SPA `NoRoute` fallback
  （当前 router 会把普通未知路径交给 WebUI，需显式处理）。
- `disabled` / `expired` / `file missing` / `directory` 一律 `404`，
  不区分「存在但禁止」与「不存在」，减少资源信息泄露。
- `Range` 合法 → `206`；非法 → `416`；`HEAD` → headers only。
- 缓存第一版固定 `Cache-Control: no-store`，先保证语义正确；
  可配置 cache policy 不进入 v0.6。

## 文件 Serving 统一实现

新建共享包，Local download 与 Published HTTP 共用：

```text
internal/filesafe/
    path.go    ValidateLogicalPath / ResolveWithinRoot /
               ResolveRegularFile / NormalizePublicPath
    serve.go   MIME / Content-Disposition / Range / HEAD / Last-Modified
```

本地文件直接走 `http.ServeContent`（`*os.File` 是 `io.ReadSeeker`，
`Range` / `206 Partial Content` / `HEAD` / `If-Modified-Since` /
`Content-Length` 自然获得）。

Remote download 保持流式（`remote.Stat` 提供 Content-Length 与
Last-Modified（可得时）→ `remote.Open` → `io.Copy`），带
`Content-Type` 与 `Content-Disposition: attachment`。
**Remote download 不做 Range**：当前 `Remote.Open` 只有顺序
`io.ReadCloser`；Range 要求冻结在 Local / Publishing 上。

`source.ValidateLogicalPath` 保留为远端 logical path 的协议边界
校验；`filesafe.ValidateLogicalPath` 服务于本地/发布路径，两者
共享同一套基础规则（拒绝 dot segments、反斜杠、NUL、重复分隔符），
不出现两套互相矛盾的 path 语义。

## Web UI

新增独立 `Files` 页面，不动现有导航结构：

```text
Files
├── Remote      Source selector + breadcrumb + 分页 + download
├── Local       Job selector + breadcrumb + managed/unmanaged badge + download
└── Published   list + copy URL + enable/disable + expiry + delete
```

大目录渲染用 `@tanstack/react-virtual` 虚拟列表：只渲染 viewport
附近行；滚动接近末尾时以 `next_cursor` 拉取下一页；目录切换时
reset cursor / pages。不引入整套 MUI X DataGrid。

JobDialog 的 Remote Root 增加 `Browse...`（复用 RemoteFileBrowser
组件选择目录后直接 `setRemoteRoot(selected.path)`）。Files Local
视图复用既有 `POST /api/v1/jobs/:id/run` 触发同步，不新增 API。
Remote 多选（select）是纯 UI 行为，不建立任何后端 selection
persistence。

## 安全边界声明

当前 `ListenAddr()` 默认绑定全部网卡（`:<port>`），认证按 ROADMAP
在 v0.7 才实现。因此必须明确：

> v0.6 的 `private` 仅表示「没有通过 `/published/...` 公开发布」，
> **不表示** REST API 已经过身份认证。加入文件浏览与下载后，未认证
> API 直接具备读取文件内容的能力。

v0.6 不临时实现认证（避免与 v0.7 重叠）；README 与本文档要求在
可信网络或反向代理访问控制下运行，直到 v0.7 完成。

## 明确不做（v0.6 汇总）

```text
本地 delete / rename / write
directory publish / directory index / recursive publish / published listing
Remote download Range
selection persistence
认证与 Token（v0.7）
pkg/sftp v1 → v2 迁移
可配置 cache policy
浏览器端转码 / 编辑
```

## 完成标准

> 用户可以从 Web UI 分页浏览远端（WebDAV / S3 / SFTP 三协议一致）
> 与本地（以 Job.LocalRoot 为 namespace，含 managed 标记）文件并
> 下载；可以把同步后的 managed 本地文件显式发布为受控 HTTP URL
> （支持 Range / HEAD / 过期 / 禁用，路径逃逸与 symlink 逃逸被
> 拒绝）；发布生命周期与 Job 解耦且跨重启持久；三协议同步 E2E
> 零回归。
