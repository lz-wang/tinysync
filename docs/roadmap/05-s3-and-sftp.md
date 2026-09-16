# v0.5.0 — Multi-Protocol Source Abstraction (S3 & SFTP)

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本文件是 v0.5.0 的
实现契约：Source 模型、Remote 生命周期、协议注册、SQLite migration、
REST API、logical path 校验、S3 / SFTP adapter 语义与 Web UI 的设计均已
冻结，实现与本契约冲突时以本文件为准并先行修订本文件。

目标：完成 WebDAV-shaped Source 模型向真正多协议 Source 模型的迁移，
实现 S3 与 SFTP read-only adapter，证明现有 Sync Engine、Scheduler 与
History 在没有协议分支的情况下原样运行于 WebDAV、S3 和 SFTP。

```text
Source（typed config + credentials）
        ↓
Remote Registry（webdav / s3 / sftp 各注册一个 factory）
        ↓
source.Remote（Stat / List / Open，协议无关）
        ↓
Scanner → Selector → Planner → Engine   ← 零协议分支
        ↓
Local Files + managed_files + Run History
```

v0.3 已冻结的基础在本阶段继续成立：任何协议都必须映射到
Source-relative logical path，`"/"` 永远表示 Source root；planner 按
`Version → Checksum → ETag+Size → Size+ModifiedAt` 判定变化，ETag 是
opaque token。Sync Engine 只依赖 `Remote.List / Open`，本阶段不改引擎。

## 范围

| 项目 | v0.5.0 |
| --- | --- |
| WebDAV | 保持现有行为，零回归 |
| S3 | 完整 `Stat / List / Open` + 同步 |
| SFTP | 完整 `Stat / List / Open` + 同步 |
| Copy / Mirror | 三种协议一致 |
| Selector | 三种协议一致 |
| Scheduler | 三种协议复用现有 Runner |
| History | 三种协议复用现有历史 |
| Range / Resume | **不做** |
| Capability API | **暂不做**（保留为未来设计原则） |
| S3 multipart upload | 不涉及，只读 |
| S3 VersionID | 能低成本获取则填充，否则留空 |
| SFTP symlink | 不跟随，发现即拒绝（fail-fast） |
| SFTP TOFU | 不做 |
| SFTP insecure host key | 不做（禁止 `ssh.InsecureIgnoreHostKey`） |
| AWS ambient credential chain | 不做（Source 完全由自身配置决定） |
| Remote write | 不做 |
| Source 类型转换 | 不做，Type 创建后不可变 |

不增加 `Capabilities()` / `OpenRange(...)`：当前没有调用者，v0.3 已把
Range/resume 明确推迟；继续维持最小接口 `Stat / List / Open`，等真正
实现 resume、HTTP Range 或远端浏览器需要随机访问时再引入 capability。

## Source 模型

`Source` 从 WebDAV 扁平字段改为 tagged/discriminated configuration：

```go
type Type string

const (
    TypeWebDAV Type = "webdav"
    TypeS3     Type = "s3"
    TypeSFTP   Type = "sftp"
)

type Source struct {
    ID              string
    Name            string
    Type            Type
    Config          Config            // 非敏感协议配置
    CredentialState CredentialState   // 各 secret 是否已设置（布尔集合）
    Enabled         bool
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type Config struct {
    WebDAV *WebDAVConfig
    S3     *S3Config
    SFTP   *SFTPConfig
}
```

一致性约束（校验层强制）：

```text
Type=webdav → 只能存在 WebDAV config
Type=s3     → 只能存在 S3 config
Type=sftp   → 只能存在 SFTP config
```

### 各协议配置

| Type | 非敏感 Config | Secret Credentials |
| --- | --- | --- |
| WebDAV | `endpoint`, `username` | `password` |
| S3 | `endpoint`(optional), `region`, `bucket`, `prefix`(optional), `path_style`, `access_key` | `secret_key` |
| SFTP | `host`, `port`(default 22), `username`, `remote_root`, `auth_method`(`password` \| `private_key`), `host_key_fingerprint` | `password` 或 `private_key` + `private_key_passphrase`(optional) |

- `access_key` 不是 secret，进入 Config；`secret_key` 与 SFTP 私钥是
  secret，完全不进入普通 Source 对象与 API 响应。
- SFTP 的 `auth_method` 显式声明，不根据哪个字段非空隐式推断；校验、
  UI 与 PATCH 语义都以此为单一事实来源。
- 领域对象与输入结构分层：`Source` 永不携带 secret 明文，secret 只经
  `Credentials` 输入结构与 Repository 凭据查询流转。

## Logical Path 校验

新增统一入口 `source.ValidateLogicalPath()`，任何 adapter 返回的
`FileInfo.Path` 都必须经过这一层（adapter 出口校验 + scanner 二次校验）：

```text
"/"                    OK
"/a/b.txt"             OK

relative/path          reject
/a/../b                reject（dot segments）
/a/./b                 reject（dot segments）
/a//b                  reject（重复分隔符）
/a\b                   reject（反斜杠，避免跨平台歧义）
含 NUL                 reject
目录尾随 "/"           reject
```

尤其 `\` 必须拒绝：S3 key 允许 `foo\bar.txt`，进入 `filepath` 后在
Unix 与 Windows 上语义不同。校验失败视为该次 List/Stat 失败。

## Remote 生命周期

`Remote` 增加显式生命周期与 context-aware 构造：

```go
type Remote interface {
    Stat(ctx context.Context, path string) (FileInfo, error)
    List(ctx context.Context, path string) ([]FileInfo, error)
    Open(ctx context.Context, path string) (io.ReadCloser, error)
    Close() error
}

type RemoteFactory interface {
    Create(ctx context.Context, source Source, credentials Credentials) (Remote, error)
}
```

生命周期链路：

```text
Runner runCtx
   ↓
SourceService.OpenRemote(runCtx)
   ↓
adapter dial（WebDAV 无连接 / S3 HTTP client / SFTP SSH session）
   ↓
runCtx cancel 或运行结束
   ↓
Remote.Close() 释放连接
```

- Connection Test 使用现有 10 秒 timeout context 创建 Remote 并
  `defer remote.Close()`（当前实现创建后无 close）。
- WebDAV 的 `Close` 是显式空操作（HTTP 无持久会话）；SFTP 的 `Close`
  关闭 SSH/SFTP 连接。
- factory 创建支持 ctx 取消：SFTP dial 阻塞期间取消 runCtx 能退出。

## Remote Registry 与依赖方向

协议 dispatch 只发生在注册表边界一处，业务层禁止 `switch source.Type`：

```go
remotes := source.NewRemoteRegistry(
    webdav.NewFactory(),
    s3.NewFactory(),
    sftp.NewFactory(),
)
sources := source.NewService(repo, remotes)
```

- Source Service 承担 credentials → factory 的集中创建，对外暴露
  `OpenRemote(ctx, id)`；Runner、Connection Test、未来的 MCP / browser
  都复用该入口，不再各自拿凭据调 factory。
- Runner 移除 `GetPassword` 与 `RemoteFactory` 直接依赖，改为依赖
  `source.RemoteOpener`（`OpenRemote` 能力接口）。

```text
syncjob
   │
   ▼
source.Remote / RemoteOpener
   ▲
   │
Remote Registry
 ┌─┼─────────┐
 ▼ ▼         ▼
DAV S3      SFTP
```

## SQLite Migration 0005

`sources` 表从 WebDAV 固定列演进为通用持久化，不追加十几个 sparse
列（`region` / `bucket` / `host` / `port` ... 会让表退化为
protocol-specific sparse table）：

```text
config_json       TEXT NOT NULL DEFAULT '{}'
credentials_json  TEXT NOT NULL DEFAULT '{}'
```

Migration `0005_source_configs.sql`：

```text
旧 endpoint/username/password
    ↓ 一次性 backfill（存量 WebDAV Source）
config_json / credentials_json
    ↓
清空旧值
    ↓
Repository 之后只读写 JSON 字段
```

- backfill 形态：WebDAV 的 `config_json` 为
  `{"endpoint":"...","username":"..."}`，`credentials_json` 为
  `{"password":"..."}`。
- 旧 `endpoint / username / password` 列在 migration 中清空为 `''`，
  作为 schema tombstone 保留（v0.9 hardening 或 v1 schema compact 时
  再重建表）。**不允许新旧字段同时长期作为事实来源。**
- 不在 v0.5 重建整个 `sources` 表：FK 链路（`sync_jobs → sources`）
  牵动面大，收益低于风险，不符合「简单、稳定、可恢复」原则。
- Repository 提供 typed encode/decode；`GetPassword` 移除，凭据统一
  走 `GetCredentials`（返回完整 `Credentials`），普通读取路径永不
  返回 `credentials_json`。

## REST API

趁尚未 1.0，Source REST 一次性收敛为多协议契约：

创建请求（POST /api/v1/sources）：

```json
{
  "name": "backup-s3",
  "type": "s3",
  "enabled": true,
  "config": {
    "endpoint": "https://s3.example.com",
    "region": "us-east-1",
    "bucket": "backup",
    "prefix": "tinysync",
    "path_style": true,
    "access_key": "AKID..."
  },
  "credentials": {
    "secret_key": "..."
  }
}
```

响应：

```json
{
  "id": "src_...",
  "name": "backup-s3",
  "type": "s3",
  "config": { "...": "..." },
  "credential_state": {
    "password_set": false,
    "secret_key_set": true,
    "private_key_set": false,
    "private_key_passphrase_set": false
  },
  "enabled": true,
  "created_at": "...",
  "updated_at": "..."
}
```

- **任何 secret 都不回显。**
- 请求体拒绝未知字段与 type/config 不匹配（400）。
- `type` 创建后不可修改：PATCH 请求携带 `type` 且与现有值不同 → 400。

PATCH 语义（比 nested partial-patch 简单）：

```text
config 缺省             → 保留
config 出现             → 整个 protocol config 替换

secret 字段缺省          → 保留
secret = ""             → 清除
secret = non-empty      → 替换
```

Source 修改保护从 endpoint 泛化为 remote identity
（见下节）：被 Job 引用时 identity 变更 409，secret rotation 保持允许。

## Remote Identity 修改保护

当前「被 Job 引用时不允许修改 endpoint」的安全策略推广为：

```go
RemoteIdentityEqual(old, new Source) bool
```

| 协议 | Remote Identity 字段 |
| --- | --- |
| WebDAV | `endpoint` + `username` |
| S3 | `endpoint` + `region` + `bucket` + `prefix` + `path_style` |
| SFTP | `host` + `port` + `username` + `remote_root` + `host_key_fingerprint` |

identity 字段在 Source 被 Job 引用时禁止修改（409）；`name`、`enabled`
与全部 secret（password / secret_key / private_key rotation）保持允许，
否则正常凭据轮换无法操作。更换远端的正确路径仍是
新建 Source → Job 切换 SourceID（触发原子 metadata 重置）。

## S3 Adapter

依赖采用 **AWS SDK for Go v2**（不自行实现 S3 HTTP 协议，不优先绑定
MinIO client）。SDK 支持 `BaseEndpoint` 定制 endpoint 与
`UsePathStyle`，覆盖自建 S3 / MinIO 场景。

配置：

```text
endpoint      optional（"" 表示 AWS 默认 endpoint）
region        required
bucket        required
prefix        optional（Source root 在 bucket 内的子前缀）
path_style    bool
access_key    required
secret_key    required
```

凭据只用显式 static credentials；禁止宿主机
`~/.aws/credentials`、EC2/ECS metadata、environment default chain
成为 Source 的隐式凭据来源——Source 身份必须完全由自身配置决定。

路径映射：

```text
bucket = backup, prefix = homes/lzwang

Source "/"             ↔ homes/lzwang/
Source "/docs/a.pdf"   ↔ homes/lzwang/docs/a.pdf
```

`List("/docs")` 使用 `ListObjectsV2` 且
`Prefix = <prefix>/docs/`、`Delimiter = "/"`，从 `Contents` 与
`CommonPrefixes` 合成文件与一级目录，保持现有 recursive
`ScanRemote` 完全不变。**必须支持 paginator**，不假设单页 1000 object。

Fingerprint 第一版：

```text
Size       ← object size
ModifiedAt ← LastModified
ETag       ← ETag（opaque，绝不当 MD5 用）
Checksum   ← 空
Version    ← 空
```

不为填充 Version 对每个对象额外 `HeadObject` /
`ListObjectVersions`——现有 planner 已能正确降级到 ETag+Size。

S3 namespace 特例：

- folder marker（零字节、key 以 `/` 结尾的 `foo/`）表示目录，不得
  变成普通零字节文件。
- 同一 logical path 同时出现文件与目录
  （`a` 与 `a/b.txt` 并存）时，**整个 List 返回错误**（fail whole
  scan）：本地 filesystem 无法无损表达该 namespace，Mirror 在完整
  扫描失败时本来就不删除，fail-fast 安全。

## SFTP Adapter

依赖：`github.com/pkg/sftp`（v1 稳定系列）+ `golang.org/x/crypto/ssh`。

配置：

```text
host                  required
port                  默认 22
username              required
remote_root           required，必须 absolute
auth_method           password | private_key（显式声明）
host_key_fingerprint  required，SHA256:... 形式
```

认证：

```text
password       → credentials.password
private_key    → credentials.private_key（PEM）+ private_key_passphrase（可选）
```

Host key 必须验证：保存 `SHA256:xxxxx` fingerprint，握手 callback 内用
`ssh.FingerprintSHA256(key)` 严格比较；不提供
`ssh.InsecureIgnoreHostKey()`，v0.5 不实现 TOFU。

Source root 映射（全部用 `path`，不用 `filepath`）：

```text
remote_root = /srv/backups

SFTP /srv/backups           → Source "/"
SFTP /srv/backups/a.txt     → Source "/a.txt"
SFTP /srv/backups/x/b.txt   → Source "/x/b.txt"
```

Symlink 边界：**v0.5 全部禁止**。发现 symlink（文件或目录）时该次
List/Stat 返回错误 → remote scan failed → Mirror 不执行 delete。
不允许简单 skip：skip 会得到不完整的 remote snapshot，Mirror 可能
把仍存在的 managed file 误判为远端消失。用 `Lstat`（不 follow）识别
条目类型，配合 `RealPath` 加强 root confinement，禁止 `..` 或 symlink
逃逸 remote_root。

## Web UI

- 创建 Source：Type 选择器（WebDAV / S3 / SFTP）+ 按类型动态表单。
- 编辑 Source：Type readonly（不支持原地转换）。
- Sources 列表列：`Name / Type / Location / Credential / Enabled /
  Connection / Actions`；Location 按协议摘要：

```text
WebDAV  https://nas/dav/
S3      s3.example.com / backup / photos
SFTP    nas.example.com:22 /srv/backup
```

- secret 字段（password / secret key / private key）保持既有语义：
  绝不回填、显示已设置状态、Replace、Clear。

## 协议矩阵（核心验收）

E2E 构建统一 protocol test matrix，`runCommonSyncScenario` 内**禁止**
任何协议分支，协议差异全部封装在 test fixture / adapter 中：

```go
func protocolFactories() []protocolCase {
    return []protocolCase{
        {name: "webdav", ...},
        {name: "s3", ...},
        {name: "sftp", ...},
    }
}

func TestSyncAcrossProtocols(t *testing.T) {
    for _, tc := range protocolFactories() {
        t.Run(tc.name, func(t *testing.T) {
            runCommonSyncScenario(t, tc)
        })
    }
}
```

统一 scenario：initial pull、unchanged、update、selector、
Copy remove、Mirror remove、history。要证明的是三协议经过同一个
`Remote → Scanner → Selector → Planner → Engine → Local Files`
链路，而不是「项目里出现了 s3 和 sftp 两个包」。

## 实施顺序

18 个 commit，每个保持 `make check` 可通过，不提前暴露半完成的
用户能力：

1. `docs: 固化 v0.5.0 多协议 Source 契约`（本文件）
2. `refactor(source): 增加 Remote 生命周期与 context-aware factory`——`Remote.Close()`、`RemoteFactory.Create(ctx, ...)`、WebDAV 适配、TestConnection defer Close
3. `refactor(source): 集中 Remote 创建与协议注册`——Remote registry、`Service.OpenRemote`、Runner 移除 `GetPassword` / factory 依赖、app 只装配一个 registry
4. `refactor(source): 建立多协议配置与凭据模型`——`TypeS3 / TypeSFTP`、三类 Config / Credentials / CredentialState、type-config 一致性校验、Type immutable
5. `feat(storage): 迁移多协议 Source 配置与凭据`——`0005_source_configs.sql`、v4→v5 WebDAV backfill、清空 legacy 列、Repository typed encode/decode、移除 `GetPassword`
6. `refactor(api): 改造 Source API 为 discriminated config`——`config` / `credentials` / `credential_state`、按 type 严格解码、PATCH secret 三态、拒绝未知字段、Type 不可修改
7. `feat(source): 统一 logical path 校验`——`ValidateLogicalPath`、scanner 对 adapter 返回值二次校验
8. `feat(s3): 实现 S3 read-only remote adapter`——AWS SDK Go v2、显式 static credentials、BaseEndpoint、path-style、pagination、logical path 转换
9. `test(s3): 加固 S3 namespace 与 fingerprint 语义`——folder marker、CommonPrefixes、file/dir collision、ETag opaque、context cancellation
10. `feat(sftp): 实现 SFTP read-only remote adapter`——`pkg/sftp` + `x/crypto/ssh`、password / private key、host key fingerprint、remote root
11. `fix(sftp): 加固 root confinement 与取消语义`——`RealPath / Lstat`、symlink fail-fast、禁止 root escape、factory ctx 取消
12. `fix(source): 泛化 Source remote identity 修改保护`——`RemoteIdentityEqual`、identity 变更在有 Job 引用时 409、secret rotation 保持允许
13. `feat(web): 支持 WebDAV S3 SFTP Source 配置`——type selector、动态表单、Type readonly、secret replace/clear、Location 摘要
14. `test(e2e): 验证三协议共用同一 Sync Engine`——统一 protocol matrix
15. `ci: 增加真实 S3 SFTP integration gate`——MinIO / SFTP 容器的 integration workflow
16. `test: 更新多协议 native smoke`——smoke 覆盖新 schema 与 API
17. `docs: 完成 v0.5.0 实现记录`——README 示例、checklist、ROADMAP 状态、CHANGELOG
18. `chore(release): prepare v0.5.0`——发布前最终收口，不混入新功能

## 验收矩阵

| 验收项 | 必须满足 |
| --- | --- |
| Source CRUD | WebDAV / S3 / SFTP 均可通过 REST + Web UI 创建、编辑、删除、测试 |
| Persistence | 三种协议跨重启完整恢复；secret 永不进入普通 API |
| Migration | v0.4 数据库无损升级；原 WebDAV Source 自动迁移 |
| WebDAV | 现有行为零回归 |
| S3 | prefix、pagination、folder marker、path-style、custom endpoint 正常 |
| SFTP | password / private-key、host-key pinning、root confinement 正常 |
| Security | SFTP symlink 不跟随；无 `InsecureIgnoreHostKey` |
| Engine | `internal/syncjob` 不出现 S3/SFTP/WebDAV 分支 |
| Copy / Mirror | 三协议一致，incomplete scan 永不 delete |
| Selector | 三协议一致 |
| Scheduler | 自动调度三协议都走现有 Runner |
| History | run / run-items 正常记录三协议运行 |
| Concurrency | S3 / SFTP 在 `MaxConcurrentTransfers > 1` 下验证 |
| Cancellation | 取消运行能终止三协议传输并释放连接 |
| CI | `make check`、build、native smoke、protocol integration 全绿 |
| Release | 六平台产物 / checksums、GitHub Release、WebDAV 镜像独立验收 |

## 完成标准

> 同一个 Sync Engine 无需协议分支即可同步 WebDAV、S3 和 SFTP；
> Source 配置、凭据、持久化、REST、Web UI、连接生命周期与 logical
> path 均具备协议扩展能力。

上述验收矩阵为可验证条件；发布相关项（Release workflow、发行资产、
镜像验收）在 tag `v0.5.0` 后按[构建与发布](../guides/release.md)执行，
不属于本阶段代码 commit。
