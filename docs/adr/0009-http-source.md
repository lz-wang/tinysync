# ADR 0009：HTTP Web Server Source

日期：2026-10-03
状态：已接受

## 背景

第六种 Source 类型：大量镜像站与内网文件服务以普通 HTTP 文件服务
（nginx `autoindex`、Caddy `file_server browse`、miniserve）提供只读
目录下载。它们不是新的文件访问协议，而是 **HTTP 上不同的目录索引
表现形式**。既有架构 `Source → RemoteFactory → Remote → Scanner →
Planner → Downloader` 的协议边界保持不变，HTTP 作为第六个 adapter
接入 `RemoteRegistry`——同步业务层（Runner / Scanner / Planner /
Downloader / Browser）不出现任何 HTTP 分支。

## 决策

### 单一协议类型 + listing profile

只新增一个协议类型 `TypeHTTP = "http"`。nginx / Caddy / miniserve
的差异收敛为 adapter 内部的 **listing profile**：

```text
Source Type = http
        │
        ▼
 internal/source/http
        ├── listing profile: auto | nginx | caddy | miniserve
        ├── listing parser（差异只停留在目录发现层）
        └── transport / auth / redirect / URL / metadata / 下载共用
```

不设 `TypeNginx` / `TypeCaddy` / `TypeMiniserve`：后续支持 Apache
`mod_autoindex`、Python `http.server`、lighttpd 只增加 parser/profile，
不扩充 Source Type。

### 路径模型：BaseURL 即 Source root

配置只有 `BaseURL`（如 `https://mirror.example.com/releases/`），
该 URL 本身定义 Source 的 `/`——HTTP 文件服务的 URL path 是
namespace 的一部分，Job 已有自己的 `RemoteRoot`，HTTP 再设
`remote_root` 会形成重复抽象。canonical form 冻结为：
scheme 限 http/https、host 必填、禁止 userinfo / query / fragment、
path clean、统一补尾 `/`；认证必须走 Credentials，listing query 由
adapter 自己控制（如 miniserve 的 `?raw=true`）。

### listing profile 与 metadata 策略

| profile | 首选 listing | 精确 Size | ModifiedAt | Symlink |
|---|---|---|---|---|
| nginx | JSON autoindex（`autoindex_format json`） | JSON | JSON | 不可靠识别 |
| nginx HTML fallback | HTML DOM | HEAD | HEAD | 不可靠 |
| caddy | `Accept: application/json` | JSON | RFC3339 | JSON `is_symlink` |
| miniserve | `?raw=true` HTML table | HEAD | HEAD | 部分可识别 |

- **绝不解析 HTML 页面显示的 Size**：nginx `autoindex_exact_size`
  只影响 HTML，页面值为人类可读近似；miniserve 默认
  `--size-display human`。HTML 模式一律经 HEAD（fallback
  `GET Range: bytes=0-0` 的 `Content-Range`）取精确值——Downloader
  会把 `written != expected.Size` 判为永久失败，近似值会让同步
  永远失败。
- **`auto` 探测不依赖 `Server:` header**（reverse proxy / CDN /
  隐藏版本都会让它失效），而是按响应形态判定：JSON 字段集
  （`is_dir`+`is_symlink`+`mod_time` → caddy；`type`+`mtime`+`size`
  → nginx）或 HTML 结构标记（miniserve row class / nginx autoindex
  DOM）。无法识别的 HTML 明确失败（`unsupported HTTP directory
  listing`），绝不加入 generic HTML crawler 作为静默 fallback——
  普通网页的 `<a href>` 不是目录索引。
- **每次 listing 必须独立证明结构完整**：`auto` 下裸 `[]` 无法区分 nginx / Caddy 与普通 JSON API，返回永久错误并提示显式配置；仅显式 nginx / caddy 接受空数组。非空 JSON 所有条目必须满足同一 profile 的必填字段，Caddy 要求 `name` / `size` / `url` / `mod_time` / `is_dir` / `is_symlink` 存在且非 null。miniserve 由 DOM 中的条目行与带类型锚点，或同一 table 的完整 `thead` 三列表头与 `tbody` 识别；parser 独立复核结构，未知 `entry-type-*`、行 / 锚点 / href 类型冲突均整轮失败。
- **可识别 symlink 一律拒绝**（Caddy JSON 的 `is_symlink`）：
  Caddy 官方明确 file server root 不是文件系统 sandbox，root 内
  symlink 仍可能指向 root 外；与 Local / SMB 的 fail-closed 风格
  一致。

### Caddy `file_limit` 完整性保护（fail-closed）

Caddy `file_server browse` 默认 `file_limit 10000`，超出后**只显示
前 N 个条目**。若把截断 listing 当完整 snapshot，Mirror 会把剩余
managed 文件误判为远端删除而错误删除本地文件——不可接受。因此：

- `HTTPConfig.CaddyFileLimit` 持久化该上限（0 归一为 10000）；
- caddy listing 条目数达到该值时**整轮 ScanTree 失败**（Planner
  不执行、Mirror 不删除任何文件），错误为 permanent；
- 用户改服务器 `file_limit` 时必须同步改 TinySync 配置；UI 必须
  说明该约束。

### Fingerprint 策略

| profile | Fingerprint |
|---|---|
| nginx JSON / caddy JSON | Size + ModifiedAt |
| nginx HTML / miniserve | Size + ModifiedAt + ETag（HEAD 有则用） |

不为 JSON 模式逐文件补 HEAD 换 ETag：10 万文件 = 10 万无意义
请求；`Size + ModifiedAt` 已满足既有 Planner 契约。

### 传输安全

- **禁用压缩 representation**：所有请求带
  `Accept-Encoding: identity` 且 transport `DisableCompression: true`；
  收到非 identity 的 `Content-Encoding` 直接失败——压缩 representation
  会破坏 Downloader 的字节数校验语义。
- **URL confinement**：logical path 逐 segment `url.PathEscape`
  拼接，HTML listing 的 `<a href>` 只作为发现 entry 的输入，解码后
  验证 same origin、仍在 BaseURL subtree、无 `..` / `%2F` / `%5C` /
  NUL，最终 URL 一律按 logical path 重建。
- **Redirect 收敛**：最多 5 跳、same origin only（含 scheme 不降级
  https→http）、最终 path 仍须位于 BaseURL subtree——Basic / Bearer
  credential 绝不被重定向到其它服务器。
- **metadata fallback**：HEAD 不可用（405 / 无 Content-Length）时
  以 `GET Range: bytes=0-0` 的 `Content-Range` 取精确 size；HEAD 与
  Range 都不可用且 listing 无精确 size 时**ScanTree 失败**，绝不能
  以 `Size = 0` 继续同步。

### 认证

第一版只支持 `none / basic / bearer`（覆盖 nginx `auth_basic`、
Caddy `basic_auth`、miniserve auth、反向代理 Bearer gateway）。不做
任意 Header / Cookie / OAuth2 / OIDC / client certificate——那会把
HTTP Source 变成通用 HTTP Client 配置系统。basic 要求 username +
password；bearer 与 none 归一时清空 username。

### 远端身份与持久化

- **remote identity**：`base_url + listing_mode + auth_method +
  username`——base_url / username 改变远端 namespace 或 ACL 可见
  的目录树。被 Job 引用后禁止变更（409）。`password` /
  `bearer_token` 是 credential rotation，可更新；`caddy_file_limit`
  是扫描安全参数而非身份，允许修改。
- **无 SQLite migration**：`sources.type = http` 走既有
  `config_json` / `credentials_json` 扁平 JSON（`sqlite/encode.go`
  增加 case），与 SMB / Local 相同。

### 测试与集成

- `httptest.Server` 覆盖绝大多数场景（三 profile fixture、URL
  安全矩阵、压缩 representation、redirect 收敛、HEAD→Range fallback、
  认证、Caddy file_limit fail-closed、错误分类），接入
  `remotetest.RunSuite` 与协议矩阵。
- 真实服务集成（CI 独立 job）：nginx / Caddy / miniserve 容器，
  固定 TinySync 对真实输出的兼容性；重点验证 Copy / Mirror、认证、
  取消、不完整 listing 不删除。

## 已知限制与降级约束

- 创建 HTTP Source 后**不支持无损降级**到不认识该类型的旧版本
  （`decodeConfig` 对 `type=http` 返回 ErrUnsupportedType）。降级前
  应先删除 HTTP Source；约束写入 Release note。
- HTTP 文件服务在 TinySync 中只视为只读源：不实现
  `DirectoryCreator`，即使 miniserve 可开上传 / WebDAV（需要写
  能力时应使用既有 WebDAV Source）。
- nginx HTML 模式下 symlink 无法可靠识别，存在服务器侧 symlink
  被 follow 的固有风险（与 Samba POSIX symlink 同类限制）。

## 后果

- 同步业务层零 HTTP 特判；三种 Web Server 的差异只存在于 listing
  parser / detector 一层。
- 为未来 Apache autoindex / 镜像站 / 软件仓库探测保留了
  `HTTP transport → listing detector → specialized parser` 的两层
  演进路径；Caddy `file_limit` 的完整性保护是
  `SnapshotCompleteness` 通用约束的第一个实例化。
