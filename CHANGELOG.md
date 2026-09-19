# 更新日志

本文件记录 TinySync 每次正式版本包含的用户可见变化，发布内容应与对应的
GitHub Release 摘要一致。

格式遵循 [Keep a Changelog 1.1.0](https://keepachangelog.com/zh-CN/1.1.0/)，
版本遵循[语义化版本 2.0.0](https://semver.org/lang/zh-CN/)。正式版本格式为
`x.y.z`；开发版本格式为 `dev-<commit日期>-<commit7>`。

开发期间向 `[Unreleased]` 写入；正式发布时将 `[Unreleased]` 改为
`[x.y.z] - yyyy-mm-dd`。Release workflow 只读取对应版本段落，
绝不根据 Git commit message 自动生成 Release Notes。

每个版本只使用以下分类：

- `新增`：面向用户的新特性。
- `修复`：面向用户的故障修复。
- `移除`：面向用户的特性或能力移除。

内部重构、测试、构建和文档维护不进入发布摘要，除非它们直接交付新特性或修复用户可见故障。

## [Unreleased]

### 新增

- Web UI 重构为可折叠侧边栏工作区，新增亮色、暗色与默认自动主题；用户设置支持头像、在线改密，并将 scoped API Token 管理移至独立设置入口。
- Web UI 移除各菜单页的重复标题与说明，收紧为操作、标签页和必要状态组件；页面、表单与提示统一提供中文文案。
- 创建或编辑同步源、同步任务时，表单面板固定为页面宽度的 60% 与页面高度的 80%，内容在面板内滚动。
- 首次 `serve` 启动自动生成高熵管理员密码并仅在本机输出一次；后续认证继续使用 HttpOnly Web Session，密码变更会立即废弃全部已有会话。

## [0.9.0] - 2026-09-19

### 新增

- 新增数据目录单实例约束：`serve` 在整个运行生命周期持有
  `<datadir>/tinysync.lock` 独占锁（POSIX flock / Windows LockFileEx），
  同一数据目录的第二个实例启动立即失败并说明占用情况，进程异常退出
  后由操作系统自动释放锁；不同数据目录的实例可并存。
- 新增数据库维护命令：`tinysync db check`（quick_check / 外键 /
  schema 版本完整性报告）、`tinysync db backup`（VACUUM INTO 一致性
  快照写入 `<datadir>/backups/`，POSIX 权限 0600）与
  `tinysync db restore --from <file> --force`（离线恢复：先校验备份、
  自动生成当前库 safety backup——「健康」（可打开且通过完整性检查）
  的库备份失败即在任何替换前中止，打不开或可打开但真实损坏（页级
  损坏、外键违规）的库告警并继续救灾、staging + fsync 后原子替换并
  清理遗留 WAL）。当前库完整性检查的错误分类化：仅真实损坏走救灾
  路径；schema 比本二进制新或 context 取消 / I/O 等运维失败一律在
  任何替换前中止——旧 binary 不允许覆盖健康的更高版本数据库。三个
  命令与 `serve` 互斥执行（同一把
  datadir 锁）；schema
  比当前二进制新的备份拒绝恢复，较旧的备份恢复后由下次 `serve` 正常
  向前迁移。数据库迁移前的自动备份行为不变。
- 数据库迁移（含升级前）现在先执行完整性检查：quick_check 或外键
  校验失败的数据库拒绝继续迁移，避免在损坏数据上继续写 schema 版本；
  优雅关闭顺序收口为「停止调度 → 运行终态落库 → 排空请求 → WAL
  checkpoint 截断 → 关闭数据库 → 释放锁」，干净退出后不再遗留膨胀的
  WAL 文件。
- 传输重试策略硬化：现在只重试瞬时错误（网络抖动、408 / 429 / 5xx、
  连接中断等）；认证失败、404、其余 4xx 客户端错误（400 / 405 /
  409 / 410 / 412 等，HTTP 与 S3 协议一致）、host key 不匹配、权限
  不足、磁盘空间耗尽、内容大小不一致等确定性失败不再做无意义重试，
  由下一轮运行继续收敛。重试退避改为指数 + 有界抖动（约 250ms /
  500ms），并新增
  `--transfer-timeout` / `TINYSYNC_TRANSFER_TIMEOUT` 配置单个文件单次
  传输尝试的超时（默认 0 = 不启用，行为与之前兼容；超时的尝试会
  重试，不影响整轮运行的取消语义）。超时对所有协议真实生效：SFTP
  无请求级取消，超时经拆除底层连接中断阻塞中的读取，后续操作自动
  重连（路径映射与超时拆除均与所在连接代际绑定：重连后重新解析
  remote root，不出现旧 root 路径打在新连接上的组合；迟到的旧
  attempt 超时回调不拆除重连后的新连接，已正常 Close 的文件也不因
  迟到的 context 取消触发连接拆除），单次超时不终结整轮运行。
- 进程异常退出遗留的下载临时文件现在在启动时自动安全清理：只扫描
  已配置 Job 的同步根目录、不跟随 symlink、只删除与内部临时文件名
  形态（`.tinysync-part-` + 12 位十六进制）严格匹配的普通文件，
  同前缀的合法用户文件与其它文件（含隐藏文件）一律不动。
- 同步新增本地映射 preflight：在任何本地变更之前校验本轮计划与
  实际文件系统的兼容性——Windows 平台拒绝保留设备名（CON / NUL /
  COM1..COM9 / LPT1..COM9 等，可带扩展名）、非法字符、控制字符与
  尾随点 / 空格；大小写不敏感的文件系统上，远端同时存在仅大小写
  不同的路径（如 `Foo.txt` 与 `foo.txt`）时整轮同步在变更前失败，
  不再出现后下载覆盖先下载的平台相关结果。
- 新增请求与同步链路日志关联：每个 HTTP 请求（含 MCP）由服务器
  生成 `req_<随机>` 请求标识并回写 `X-Request-ID` 响应头，access
  log 统一记录 event=request、request_id、method、path、status、
  耗时与响应字节数（不记录 query string、请求体与认证头）；每轮
  同步在启动（run 记录落库成功）与结束时各输出一条 event=sync_run
  事件（status=running 与终态，job_id / run_id / source_id / status /
  耗时 / 字节 / 文件变更统计 / 失败原因），文件级失败附带 path，
  进程硬崩溃后日志仍留有 run 启动痕迹。日志保持单一 zap 文本体系，
  panic 请求同样留痕。

## [0.8.0] - 2026-09-18

### 新增

- 新增 MCP Streamable HTTP 端点 `POST /mcp`（官方 Go MCP SDK、
  固定 `2026-07-28` 协议、stateless、JSON 响应、请求体上限
  1 MiB）：Agent / LLM 可查询与执行 TinySync，完全复用现有
  API Token 认证（`Authorization: Bearer`，Web Session 无效）与
  `read` / `run` / `admin` scope，跨源与 DNS rebinding 防护启用。
- 新增 7 个 MCP tools：`list_sources` / `list_jobs` / `get_job`
  （只读发现，secret 永不返回）、`run_sync`（只接受已配置的
  `job_id`，异步返回 `run_id`，标注 destructive-capable）、
  `get_sync_run`（状态 / 统计 / 失败原因）、`search_files` 与
  `get_file_info`（本地同步文件发现）；不提供任何配置修改类
  tool。
- 新增本地同步文件搜索：以 Job 为命名空间、从 `managed_files`
  检索已同步文件（大小写不敏感子串匹配，默认 50 条、上限
  200 条，带截断标记），返回前经 filesafe 边界获取当前实际文件
  状态；本地已删除的记录不返回。
- 新增小型文本 MCP resource：`tinysync://jobs/{job_id}/files/{path}`
  仅承载 ≤ 256 KiB 的 UTF-8 普通文件（读取上限独立兜底，目录 /
  symlink / binary 一律拒绝），缓存策略为 `cacheScope=private`、
  `ttlMs=0`。
- 大文件不经 MCP 搬运：`get_file_info` 返回 relative
  `download_url`，客户端携带同一 Bearer token 走现有
  `/api/v1/jobs/:id/files/download`（Range / HEAD 行为不变），
  不产生匿名临时链接。

### 修复

- 修复 MCP `list_sources` 与 `list_jobs` 在超大合法 offset 下的整数
  溢出：现在稳定返回空页，不会因 slice bounds panic 变为 500；同时
  修正 Tool Annotation，`run_sync` 明确为非幂等、会与预配置远端交互，
  只读工具明确为闭合世界。
- 修复 `search_files` 将本地权限、I/O 等访问失败误报为「没有匹配文件」
  的语义丢失；现在只忽略已经不存在的 managed 文件，且仅存在第
  `limit + 1` 条实际可返回结果时设置 `truncated`。
- 修复未注册的 `/mcp/*` 路径会被 Web SPA fallback 返回 HTML 的路由
  边界问题，现统一返回 404。

## [0.7.0] - 2026-09-18

### 新增

- 新增管理员认证基线：`tinysync auth set-password` 用于初始化或
  重置管理员密码（支持 `--password-stdin` 自动化输入），重置会
  立即废弃全部已有 Web Session；`serve` 在管理员密码未初始化时
  拒绝启动。登录接口与密码设置执行同一密码长度策略（最长 1024
  字节），登录请求体另设独立大小上限，防止超长输入拖垮密码散列。
- REST API 引入认证：除 health / version / 登录与 `/published`
  公开文件外，所有 `/api/v1` 端点默认拒绝匿名访问（401）；Web
  端通过登录建立 HttpOnly Session Cookie（7 天绝对过期、
  SameSite=Strict、HTTPS 下自动 Secure），新增会话查询与登出
  接口（`/api/v1/auth/login|session|logout`），跨源变更请求与
  URL 传参凭据一律拒绝。
- 新增 API Token：管理端点 `GET/POST /api/v1/api-tokens` 与
  `POST /api/v1/api-tokens/:id/revoke`，支持 `read` / `run` /
  `admin` 三种 scope（admin 蕴含 read + run）、可选过期时刻、
  幂等撤销与 last_used 记录；raw token 仅创建响应返回一次，
  之后不可查询。所有端点仅接受 `Authorization: Bearer` 认证，
  按 scope 返回 401 / 403。
- Web 界面新增登录页与 API Tokens 管理页：登录后进入应用，
  顶栏可登出；Token 页支持创建（scope 选择与可选过期时刻）、
  raw token 一次性展示与复制、状态（Active / Expired / Revoked）、
  最近使用时间与撤销确认；会话过期自动跳转登录页。

## [0.6.0] - 2026-09-17

### 新增

- 支持远端与本地文件浏览：Remote 视图可分页浏览 WebDAV / S3 / SFTP
  三种协议的远端目录（统一逻辑路径与分页游标，S3 使用协议原生
  分页），Local 视图以 Job 为入口浏览同步根目录并区分
  managed / unmanaged 文件；大目录采用虚拟列表与增量分页渲染。
- 支持文件下载：远端文件流式下载，本地文件下载支持 Range 断点
  请求、HEAD 与正确的 MIME / Content-Disposition。
- 支持 HTTP 发布策略：把同步后的 managed 本地文件显式发布为
  `/published/...` 公开 URL，支持启用 / 禁用、过期时刻、路径冲突
  保护与跨重启持久；禁用、过期或文件删除后 URL 统一返回 404，
  不泄露存在性。发布目标只能是受 Job 管理的普通文件，API 不接受
  任意本地路径。
- Web 界面新增 Files 页面：Remote / Local / Published 三个视图，
  支持目录导航、分页加载、下载、一键触发同步、远端目录选择器
  （Job 配置的 Remote Root 可直接浏览选取）与发布策略管理。

### 修复

- 修复本地文件浏览的元信息查询可经父目录 symlink 越出同步根目录、
  泄露根目录之外文件的元信息（存在性、类型、大小、修改时间）的
  问题；symlink 现在只能作为路径终点显示，不能作为中间节点逃逸。
- 修复发布文件在策略创建之后被替换为 symlink 时，公开 URL 会跟随
  symlink 读取同步根目录之外文件的问题：公开服务以创建时固化的
  canonical 路径为身份，每次响应前复验全链解析结果，任意组件变成
  symlink 即与「不存在」同形返回 404。
- 远端下载对 0 字节文件现在输出 `Content-Length: 0`：此前 0 与
  「长度未知」共用同一值导致响应头缺失，客户端无法预知空文件大小。
- 发布策略的创建与更新接口改用严格 JSON 解码：未知字段（包括不
  存在的 `local_path`）与尾随数据一律返回 400，与 Source API 契约
  一致，不再静默忽略拼写错误的字段。

## [0.5.0] - 2026-09-17

### 新增

- Source 支持多协议：在 WebDAV 之外新增 S3 与 SFTP 只读 Source，
  三种协议共用同一个同步引擎、调度与运行历史。S3 支持自建服务
  （显式 endpoint、path-style、bucket 内 prefix、分页列举、目录
  占位对象），凭据只用 Source 自身的 access key / secret key；
  SFTP 支持 password 与 private key（含 passphrase）两种显式认证
  方式，host key 以 SHA256 fingerprint 严格校验，remote root 之外
  的访问与 symlink 一律拒绝。
- Source 配置按协议分为非敏感 `config` 与 secret `credentials`，
  API 响应只回显 `credential_state` 布尔集合，secret 永不回显；
  被同步任务引用的 Source 现在按协议完整保护 remote identity
  （如 SFTP 的 host / port / remote root / host key fingerprint、
  WebDAV 的 username），secret 轮换不受影响。
- 跨协议统一 logical path 校验：包含反斜杠、dot segments、重复
  分隔符或 NUL 的远端路径在协议边界整体失败，不再进入本地文件
  映射（Mirror 在不完整扫描下不会删除）。
- Web 界面支持创建 / 编辑三种协议的 Source：协议类型选择、按类型
  动态表单、编辑时类型只读、secret 三态（保留 / 替换 / 清除）与
  按协议的远端位置摘要。
- v0.4 数据库升级时自动完成 schema 迁移：存量 WebDAV Source 的
  endpoint / username / password 迁移到新的通用配置与凭据存储，
  迁移前自动备份数据库，升级后同步行为不变。

### 修复

- 优雅关闭现在能立即中断 SFTP 等有连接生命周期协议的传输：远端
  连接的创建、使用与释放统一挂在同一运行取消链上，此前阻塞中的
  读取可能无法被取消、关闭挂起直到传输自然结束。
- 远端连接建立失败（认证、host key 校验、网络不通等）现在记录为
  一轮失败的运行历史，而不是在触发运行的接口上同步报错。
- 连接测试的失败语义恢复既有 REST 契约：SFTP 等协议在连接建立
  阶段的失败（拨号、认证、host key 校验、超时）现在返回 200 与
  `ok=false`，而不是误报为服务器内部错误（500）。

## [0.4.0] - 2026-09-16

### 新增

- 支持 Job 自动调度：once（指定时刻，离线错过恢复后补执行一次）、
  interval（固定周期，按持久化锚点保持相位，重启不漂移且不补跑离线
  期间错过的周期）与 cron（标准 5-field 表达式，支持 IANA 时区）。
  调度触发与手动运行遵循同等安全规则；同一 Job 运行重叠时调度自动
  跳过并记录原因，不排队。
- 同步运行历史持久化：每轮运行（含手动、调度与被跳过的运行）及
  文件级变更明细落库，进程重启后仍可查询；异常退出遗留的运行在
  下次启动自动收敛为失败；每 Job 保留最近 500 轮运行，超出部分连同
  文件明细自动清理。
- 提供同步历史 REST API：`GET /api/v1/runs`（按 Job 与状态过滤、
  分页）、`GET /api/v1/runs/:id` 与 `GET /api/v1/runs/:id/items`；
  `GET /api/v1/jobs/:id/status` 改读持久化历史并附带 `next_run_at`，
  重启后不再回到 idle。Job 的创建与更新接口支持 schedule 配置。
- Web 界面新增 History 页面：全局运行历史列表与运行详情（含文件
  变化时间线）；Jobs 页面新增 Schedule / Last Run / Next Run 列，
  Job 编辑器支持配置调度；手动运行不再受其他 Job 运行影响，全局
  并发冲突以错误提示呈现。
- 支持通过 `--max-concurrent-jobs` / `--max-concurrent-transfers`
  （环境变量 `TINYSYNC_MAX_CONCURRENT_JOBS` /
  `TINYSYNC_MAX_CONCURRENT_TRANSFERS`）配置同时运行的同步 Job 数
  （默认 1）与同时进行的远端文件下载上限（默认 4）；远端文件下载
  在上限内并行，本地状态推进保持串行一致。

### 修复

- 修正 `--max-concurrent-transfers` 的并发语义：下载上限现在约束整个
  进程所有 Sync Job 的远端下载总和，不再按 Job 各自为政叠加（此前
  多个 Job 并行时实际并发可达「Job 数 × 上限」）。
- 关闭 Job 执行与配置变更之间的竞态：修改 / 删除与运行启动经协调位
  原子互斥，排除「检查时未运行、变更落库前恰好启动」的窗口，杜绝
  旧配置的运行写入新配置的 mapping 元数据。
- 任一文件传输失败后立即取消其余在途下载：不再继续下载完整文件
  （节省远端流量、加快失败收敛），失败后也不再改动本地文件。
- 被跳过的调度运行同样纳入每 Job 500 轮的历史容量回收：Job 长时间
  运行期间频繁产生的 skipped 记录不再无限堆积。
- 调度器内部读取故障不再吞掉到期的调度触发：读取失败时保持推进
  游标不动，恢复后补上本轮触发，且同一触发不会因重扫被重复执行。
- Job 的 schedule 接口按 discriminated union 严格校验：不属于所选
  类型的互斥字段、缺失的必填字段与未知类型现在返回 400，不再被
  静默丢弃或归一。
- 对 interval 调度发送同值更新不再重置触发相位：语义未变的 PATCH
  （如改名附带原样 schedule）保留原有锚点，触发时刻不漂移。
- `GET /api/v1/runs/:id/items` 对不存在的 run 现在返回 404，与
  `GET /api/v1/runs/:id` 的语义一致，不再返回空列表。
- Web 编辑器不再丢失 once 调度的秒精度：打开编辑器保留完整时刻，
  未改动保存不再产生无意义的调度更新（interval 触发相位不漂移）。
- 修复 once 调度「只执行一次」保证与运行历史清理的长期冲突：once
  消费状态改存于 Job 自身的持久化字段，运行历史被按上限裁剪后，
  已执行的 once 不会因历史行消失而被重新触发；修改 once 触发时刻
  后仍允许新时刻正常执行。
- 调度触发在单次内部故障下不再丢失：读取消费状态或落库运行记录
  失败时本轮不推进调度游标，恢复后自动补上该次触发，且不会因重试
  产生重复执行。
- 优雅关闭时运行终态可靠落库：正常退出（如收到停止信号）取消在途
  运行后，其失败终态立即写入运行历史，不再依赖下次启动的恢复逻辑
  才能从「进行中」收敛；关闭期间新的运行请求以 503 拒绝。
- 修复启动登记失败后的内部资源泄漏：该失败路径此前会遗留一个未
  释放的关闭等待名额，导致之后的进程优雅关闭无法及时完成。
- once 调度的执行与消费判定现在原子落库：消除「运行已登记但消费
  状态写入失败」窗口下同一 once 被反复触发的可能。
- 配置变更期间到期的调度触发不再被永久跳过：变更只是短暂互斥，
  现在会稍后重试并正常执行，不再以「已跳过」消费该次触发。

## [0.3.0] - 2026-09-16

### 新增

- 支持 WebDAV 单向同步 Job：手动触发 Copy / Mirror 同步，include / exclude
  过滤（doublestar 语法），原子下载（临时文件 + 替换，失败不破坏原文件），
  本地文件归属保护——Mirror 只删除远端已消失且归本 Job 管理的本地文件，
  Copy 则始终保留。
- 提供 Sync Job 的 REST API：创建、查询、更新、删除及手动运行与状态查询。
- Web 界面新增 Jobs 管理页面，支持配置同步方向、模式与过滤规则，并可
  一键手动运行、轮询查看运行进度、统计与失败原因；运行期间全局运行按钮禁用。
- 被 Sync Job 引用的 Source 现以 `409` 拒绝删除，不再依赖数据库错误。

### 修复

- 修复同步传输中断后，该文件在下一轮可能被误跳过、本地长期停留在
  旧版本的问题；中断的传输现在必定在后续运行中重试直至收敛。
- 远端无法提供可比对指纹（无 ETag 且修改时间缺失）时，现在按已变更
  处理并重新传输，同尺寸的内容变更不再被漏掉。
- Mirror 删除前校验本地路径组件：managed 文件的父目录为 symlink 时
  拒绝删除，杜绝经 symlink 删除本地根目录之外文件的可能。
- 修复本地目录重叠检查在根路径（如 `/`）下漏判的问题。
- 修复非根 RemoteRoot 之下远端返回 Source 根路径时可能越界扫描的问题。
- 同步运行期间以 `409` 拒绝对该 Job 的修改与删除；Web 界面同步禁用
  对应按钮，避免传输与配置变更交叉产生状态竞争。
- 创建 / 更新 Job 时即时校验 include / exclude pattern，非法 pattern
  直接拒绝，不再留到首次运行时才失败。
- 被 Sync Job 引用的 Source 现以 `409` 拒绝修改 endpoint，防止更换
  远端后 Mirror 把既有本地文件误判为远端消失而删除；更换远端请新建
  Source 后切换 Job。

### 移除

## [0.2.0] - 2026-09-15

### 新增

- 支持持久化管理 WebDAV Source。
- 提供 WebDAV Source 的 REST API 与 Web 管理界面。
- 支持验证 WebDAV Source 连接状态。

## [0.1.0] - 2026-09-14

### 新增

- 提供 TinySync 初始可运行服务骨架。
- 提供内嵌 Web UI。
- 提供服务健康状态与版本查询 API。
- 支持 Linux、macOS、Windows 的 amd64/arm64 构建。
