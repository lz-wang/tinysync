# TinySync Roadmap

本文件是架构、开发阶段和进度的统一入口；详细方案按需进入 `docs/roadmap/`。
当前可用命令见 [README.md](README.md)，版本变更见 [CHANGELOG.md](CHANGELOG.md)，
协作规则见 [AGENTS.md](AGENTS.md)。

TinySync 面向 HomeLab，目标是将 WebDAV、S3、SFTP 等异构远端统一抽象为 Source，
按 Sync Job 将选定文件单向 Pull 到本地，通过 Web UI、REST API 与 MCP 管理和访问。
Pull 表示同步方向；Copy / Mirror 表示远端删除后的本地保留策略。

规划中的核心链条：

**Remote Source → Sync Job → Selector → Sync Engine → Local Files → Publish Policy / HTTP**。

Web UI、REST API、MCP 复用应用服务层；Source、Job 与 Publish Policy 分别建模。
其中 Remote Source 管理链路（持久化、WebDAV 只读访问与连接测试）、
WebDAV Pull Sync 同步链路（Sync Job → Selector → Sync Engine →
Local Files）、调度及同步历史链路（自动触发、持久化运行历史、
并发控制）、文件浏览及发布链路（Remote / Local 浏览、Publish
Policy、`/published/*path` 公开 serving）与认证及 API Token 链路
（单一 Local Admin、Web Session、scoped API Token、REST
default-deny）与 MCP 链路均已完成实现并发布。

## 当前状态与优先级

本地核对日期：2026-09-18。当前实现、测试和 workflow 支持以下判断。

- **已实现**：`serve`、`version` / `--version`、数据目录与端口配置、日志、HTTP 生命周期、health/version API、内嵌状态页及 fallback 构建；SQLite 持久化与 migration、多协议 Source 领域（typed config / credentials 模型、Remote Registry、WebDAV / S3 / SFTP 只读 adapter、协议无关 logical path 校验、remote identity 保护、应用服务）、Source CRUD 与连接测试 REST API（discriminated config）、Sources Web 管理界面；Sync Job 领域（模型、Selector、remote scanner、planner、原子下载、Copy/Mirror engine、手动运行 Runner、SQLite Repository）、Jobs REST API 与 Web 管理界面、端到端与跨重启持久化测试；调度与历史（Schedule 模型与持久化、Scheduler、多 Job Runner、有界并发传输、`sync_runs` / `sync_run_items` 持久化、runs REST API、History Web UI、并发配置）；文件访问与发布（Remote.List 分页抽象、filesafe 受限路径原语、browser Remote / Local 浏览服务、Job namespace 本地浏览与 managed 标记、published_files 持久化与迁移 `0006_published_files.sql`、发布 CRUD 与 `/published/*path` 公开 serving、Files Web UI）。
- **已有工程配置**：Git 版本注入、Go 测试、前端静态检查、Codecov、六平台构建、三平台原生 Smoke、Build/Release 分离及 WebDAV 镜像。
- **v0.1.0 已发布**：远端 `v0.1.0` Release workflow 成功（2026-09-14），六平台发行档案与 `checksums.txt` 已核对到位；WebDAV 镜像按发布规范需独立验收，不能由 GitHub Release 成功推导。
- **已发布**：认证与 API Token（单一 Local Admin、Argon2id 密码
  凭据与 CLI bootstrap、Web Session、scoped API Token、REST
  default-deny 与 CSRF 防护）。
- **已发布**：MCP 集成（`POST /mcp` Streamable HTTP、
  固定 `2026-07-28` sessionless、API Token Bearer、7 tools、
  managed 文件搜索、小型 UTF-8 resource、大文件经现有 HTTP）。
- **已完成实现**：v0.9 可靠性与运维（2026-09-18）。datadir 单实例
  独占锁（`tinysync.lock`，serve / db 命令互斥，native smoke 覆盖
  双实例拒绝）；数据库完整性原语与迁移前 corruption 守卫、
  `tinysync db check / backup / restore` 维护命令、干净关闭 WAL
  checkpoint 截断；远端错误 transient / permanent 分类（协议判断
  收敛在 adapter boundary）、只重试瞬时错误、指数退避 + 有界 jitter、
  `--transfer-timeout` 单文件单次尝试超时（默认 0 = 不启用）、启动
  期 crash 临时文件安全清理；本地映射 preflight（Windows 保留名 /
  非法字符、case-insensitive 冲突 fail-fast，先于任何本地变更）；
  filesafe 路径 fuzz（3 目标）与 downloader 文件系统故障注入；
  三协议统一 Remote contract suite（WebDAV / SFTP 随 make check，
  S3 随 MinIO integration gate，据此修复 S3 列目录含目录自身的
  契约违反）；可靠性 E2E（503 / 断连重试、401 不重试、真实
  app.Run 重启的 crash 收敛、幂等收敛）与规模场景（10k 目录、
  32 MiB 流式传输，TINYSYNC_HARDENING 门控）；请求 / 同步日志关联
  （X-Request-ID、access log、event=sync_run / sync_file_failed）；
  hardening 质量门禁（`make hardening` + 可复用 workflow，纳入
  Build 与 Release 链路）与性能基线（`make benchmark` 6 项基准）。
  本地 `make check` / `make build` / `make hardening` / 
  `make benchmark` 全绿。

v0.2.0 持久化与 Source 管理已发布：远端 Release workflow 成功（2026-09-15），
六平台发行档与 `checksums.txt` 核对到位，WebDAV 镜像与通知独立验收通过。
v0.3.0 WebDAV Pull Sync 已发布：第一条手动触发的 WebDAV → Local 单向
同步链路可用（Copy / Mirror、doublestar 选择器、原子下载、managed_files
删除授权、手动 Run 与实时状态）。tag `v0.3.0` 指向 751f2b7，Release
workflow run 34998126731 成功（2026-09-16），六平台发行档与
`checksums.txt` 核对到位，WebDAV 镜像与 Pushover 通知送达确认。
v0.4.0 调度与同步历史已完成实现（2026-09-16）：Schedule 模型与校验
（manual / once / interval / cron，once 补执行、interval 锚点相位、
cron 时区，missed 周期不补跑）、Scheduler、多 Job Runner（重叠跳过、
并发上限、run 先持久化）、有界并行传输与文件级历史、
`0003_scheduler_history.sql`、runs REST API 与 History Web UI、
并发运行配置；真实 WebDAV + SQLite + Scheduler 端到端覆盖自动同步、
补执行、容量跳过、跨重启历史与 retention。
v0.5.0 多协议 Source 抽象已完成实现（2026-09-16）：Source 模型
协议无关化（typed Config / Credentials / CredentialState，migration
`0005_source_configs.sql` 通用持久化与 WebDAV backfill）、Remote
生命周期（Close + context-aware 创建）与协议注册表（dispatch 只在
registry 边界，`internal/syncjob` 无协议分支）、S3 read-only adapter
（AWS SDK Go v2，显式 static credentials，BaseEndpoint / path-style /
分页 / folder marker / collision fail-fast）、SFTP read-only adapter
（password / private key、SHA256 host key pin、RealPath root
confinement、symlink fail-fast、取消关闭连接）、REST discriminated
config（严格解码、secret 三态、identity 修改保护泛化）、统一 logical
path 校验、Web UI 多协议动态表单；三协议统一场景 E2E 证明同一
Sync Engine 无协议分支，多协议 native smoke 通过。本地验证 `make
check` 全绿、`make build` 通过，本地真实 MinIO integration 场景实测
通过。远端 integration gate 曾因 GitHub-hosted runner 匿名拉取
Docker Hub 的 `minio/minio` 持续被拒而未运行即变红；改用官方 Quay
镜像并为可复用 workflow 增加显式 ref 输入（Build 检出 github.sha、
Release 检出发布 tag，保证被验证的 commit 就是被发布的 commit）后
全绿：真实 MinIO 测试实际执行并通过（run 35124990299）。v0.5.0 已
发布：tag `v0.5.0` 指向 ce23f03，Release workflow run 35163690638
成功（2026-09-17），六平台发行档与 `checksums.txt` 核对到位，
WebDAV 镜像与 Pushover 通知独立验收通过。
v0.6.0 文件浏览与发布已完成实现（2026-09-17）：Remote.List 分页
抽象（ListOptions / FilePage，cursor opaque、limit 默认 100 上限
500；S3 映射 ContinuationToken，WebDAV / SFTP 单层枚举后按排序
切片分页）、filesafe 受限路径原语（逻辑路径校验、root confinement、
symlink 防御、canonical 解析；source.ValidateLogicalPath 委托共享
基础规则）、browser 应用服务（Remote 经 source.Service.OpenRemote
统一入口，Local 以 Job.LocalRoot 为唯一 namespace 并携带
managed 标记）、发布领域（canonical local_path 创建后不可变、
managed 强制、生命周期与 Job 解耦、`0006_published_files.sql`
迁移与升级测试）、REST 端点（sources / jobs 的 files 浏览与下载、
published-files CRUD、`/published/*path` 公开 serving，disabled /
过期 / 缺失一律 404、Range / HEAD / no-store）、Files Web UI
（Remote / Local / Published 三视图、虚拟化列表与增量分页、
Remote Root 目录选择器）。三协议同步 E2E 零回归。发布前收尾：
filesafe 新增 `LstatWithinRoot` / `OpenCanonicalRegularFile` 加固
Local Stat 父目录 symlink 逃逸与 Published canonical 路径 serving
的 post-publish symlink 替换防御，Local 下载与公开 serving 收口到
共享 `ServeFileContent`；Remote Root 选择器绑定 Job Source 并消除
浏览器的 stale response；发布 API 严格 JSON 解码、远端下载对 0 字
节文件输出 `Content-Length: 0`。本地验证 `make check` / `make
build` 全绿，真实 MinIO integration 实测通过（含 ContinuationToken
多页 browser 分页），E2E 覆盖 confinement 与安全语义。
v0.6.0 文件浏览与发布已正式发布：tag `v0.6.0` 指向 ea66703，
Release workflow run 35228820113 成功（2026-09-17），六平台发行档
与 `checksums.txt` 核对到位，WebDAV 镜像与 Pushover 通知独立验收
通过。
v0.7.0 认证与 API Token 已完成实现（2026-09-18）：单一 Local
Admin（固定 `admin`，无 users / RBAC）、Argon2id（19 MiB / t=2 /
p=1）PHC 密码凭据、`auth set-password` CLI bootstrap（支持
`--password-stdin`，不提供 `--password` argv）、未初始化 serve
fail-fast、Web Session（256-bit 随机 + SHA-256 落库、7 天绝对
过期、HttpOnly / SameSite=Strict / HTTPS 下 Secure）、密码重置
同一事务撤销全部会话、scoped API Token（`ts_` + 256-bit 随机、
read / run / admin 且 admin ⇒ read + run、可选过期、幂等软撤销、
last_used 1 分钟写节流、raw token 仅创建响应返回一次）、REST
default-deny（Bearer 优先且不 fallback Cookie、Auth 缺失 fail
closed 500、URL 传参凭据 400）、`/published/*path` 保持公开、
Web 登录页与 API Tokens 管理页、`0007_authentication.sql` 迁移
及 v6→v7 升级测试、认证边界 E2E。发布前安全收尾：HEAD 本地
下载补齐 read scope（run ⇏ read）、登录落实 1024 字节密码上限与
登录请求体 4 KiB 独立上限（防密码 KDF DoS）、CSRF Origin 校验
收敛到 Web Session 凭据分支（Bearer API Token 不做 CSRF，与
实现契约对齐）、SQLite scope 解码复用领域校验（存储损坏 fail
closed），并新增受保护路由 scope 矩阵 E2E（与 Gin 注册表双向
核对，防平行路由漂移）。本地验证 `make check` 全绿；native
smoke 已从匿名启动模型迁移到认证生命周期（未初始化拒绝 →
bootstrap → default-deny → 登录 → 管理 API 携带会话 → 重启后
原 session 与数据持久化）并在 macOS 实测通过。
v0.7.0 认证与 API Token 已正式发布（2026-09-18）：tag `v0.7.0`
指向 45c2b64，Release workflow run 35289193790 成功——Validate /
Release checks / 三平台 native smoke / MinIO integration 全绿，
六平台发行档与 `checksums.txt` 核对到位，WebDAV 镜像
（Mirror release to WebDAV step）与 Pushover 通知独立验收通过。
v0.8.0 MCP 集成已完成实现（2026-09-18）：`internal/mcp` adapter
挂接 composition root，`POST /mcp` 使用官方 Go MCP SDK v1.8.0
Streamable HTTP（stateless、JSON 响应、请求体 1 MiB、
CrossOriginProtection 与 localhost DNS-rebinding 防护），认证链
RequireBearerToken → TokenVerifier → auth.Service 完全复用 API
Token（中间件不设全局 scope，per-tool authorization：read tools
与 resource 用 read、run_sync 用 run、run ⇏ read 保持）；7 个
tools（list_sources / list_jobs / get_job / run_sync /
get_sync_run / search_files / get_file_info）经应用服务查询，
MCP DTO 冻结 wire contract，secret 与 raw token 不进入返回；
browser.LocalService.SearchManaged 提供应用层 managed 文件搜索
（Job namespace、synced only、大小写不敏感子串、默认 50 上限
200、Stat 实时校验）；resource `tinysync://jobs/{job_id}/files/
{path}` 只承载 ≤256 KiB UTF-8 普通文件（LimitReader(max+1) 独立
兜底）；大文件复用现有 download 端点（relative URL、同一 Bearer
token、Range/HEAD 不回归）；cacheScope=private、ttlMs=0；
Runner 启动错误映射为可判定 tool error；无数据库 migration。
本地验证 `make check` / `make build` 全绿，native smoke 补充
认证 MCP 存活检查并在 macOS 实测通过（2026-07-28 sessionless
请求形态 + 旧版本头 400），MCP E2E 覆盖认证矩阵、授权矩阵、
run 生命周期（HTTP 请求结束不取消运行）、文件发现、resource
与 Range 下载。发布前静态评审收尾修复列表超大 offset 溢出、Tool
Annotation（`run_sync` 非幂等且 open-world、只读工具 closed-world）、
managed 搜索错误 / `truncated` 语义与未知 `/mcp/*` 的 SPA fallback。
本地 `make check`、`make build` 与发布 workflow 的 Release checks、MinIO
integration、三平台 native smoke 均通过。
v0.8.0 已正式发布：annotated tag `v0.8.0` 指向 `f4a8f31`，Release
workflow run `35300671765` 成功；六平台发行档与 `checksums.txt` 已按公开
资产 digest 核验，WebDAV mirror 与 Pushover 通知步骤均成功。

## 架构与边界

- 单一 Go + Gin 二进制，内嵌 React + MUI Web UI；全工程 `CGO_ENABLED=0`。
- 启动链路为 `main → cmd → app → api`：`main` 负责 signal context 和前端资源接入，`internal/cmd` 负责 CLI 解析，`internal/app` 是装配根，`internal/api` 负责 HTTP 路由与生命周期。新增业务放在 `internal/` 的对应领域包，入口只做装配。
- 版本号只来自 Git，由 Makefile 解析并经 `-ldflags` 注入 `internal/buildinfo.Version`；不引入 VERSION 文件、package version 或配置版本作为第二个发布版本来源。
- `-tags webui` 嵌入 `web/dist`，无 tag 时嵌入 `web/fallback`，保证无 Node 环境可完整编译、测试 Go 代码。
- 未知 `/api/v1/*` 必须返回 404，不得被 SPA fallback 吞掉；前后端 API 契约变更同步更新两端实现与测试。
- 当前运行配置为 `--datadir` / `--port` / `--max-concurrent-jobs` / `--max-concurrent-transfers`，环境变量为 `TINYSYNC_DATADIR` / `TINYSYNC_PORT` / `TINYSYNC_MAX_CONCURRENT_JOBS` / `TINYSYNC_MAX_CONCURRENT_TRANSFERS`；新增配置先明确用途。
- 以下为领域开发约束：本地文件系统保存文件内容；SQLite 只保存配置、状态、元数据和历史，采用 pure-Go driver。
- Source 与 Job 分离，一个 Source 可供多个 Job 复用；v1 只做单向 Copy / Mirror，不提供远端写操作。
- 默认保护本地数据：Mirror 仅删除 `managed_files` 明确归属于当前 Job 的文件，不用 `local_root - remote_listing` 删除未知文件；下载采用临时文件与原子替换，失败不破坏原文件。
- Web UI、REST API、MCP 共用应用服务层；优先简单、稳定、可恢复，不引入分布式架构和无需求支撑的复杂能力。

## 主线 10 个阶段

版本表示目标里程碑，不承诺发布日期。阶段细项、模型草案和完成标准见对应链接；
未标记为完成的后续版本均为规划，示例 API 与 schema 不属于当前命令契约。

| 版本 | 阶段方案 | 当前状态 | 完成标准摘要 |
| --- | --- | --- | --- |
| v0.1.0 | [工程与发布基线](docs/roadmap/01-engineering-baseline.md) | 已发布，镜像待独立验收 | 验证 tag、检查、三平台 Smoke、六平台发行档及 GitHub Release；记录镜像结果 |
| v0.2.0 | [持久化与 Source](docs/roadmap/02-persistence-and-sources.md) | 已完成 | Web UI / REST 创建 WebDAV Source，持久化并验证连接，尚不执行同步 |
| v0.3.0 | [WebDAV Pull Sync](docs/roadmap/03-webdav-pull-sync.md) | 已完成 | 手动执行 Copy / Mirror，支持选择器、原子下载与本地文件归属保护 |
| v0.4.0 | [调度与同步历史](docs/roadmap/04-scheduler-and-history.md) | 已完成 | 自动调度、重叠跳过、并发控制及运行/文件明细可追踪 |
| v0.5.0 | [S3 与 SFTP](docs/roadmap/05-s3-and-sftp.md) | 已完成实现 | 同一同步引擎支持三种协议，上层不依赖协议分支 |
| v0.6.0 | [文件浏览与发布](docs/roadmap/06-file-browser-and-publishing.md) | 已发布 | 只读远端浏览、本地下载与选择性 HTTP 发布，限制访问根目录 |
| v0.7.0 | [认证与 API Token](docs/roadmap/07-authentication-and-tokens.md) | 已发布 | 单一 Local Admin + Web Session + scoped API Token，REST default-deny 与 CSRF 防护 |
| v0.8.0 | [MCP 集成](docs/roadmap/08-mcp-integration.md) | 已发布 | 复用应用服务与鉴权，查询/运行任务，大文件经 HTTP 获取 |
| v0.9.0 | [可靠性与运维](docs/roadmap/09-hardening-and-operations.md) | 收尾中 | 恢复、安全、协议兼容、跨平台、性能及回归验证具备证据 |
| v1.0.0 | [单节点稳定版](docs/roadmap/10-stable-release.md) | 规划 | 多协议端到端同步、升级/恢复及完整质量门禁通过 |

## 非目标与后续候选

v1.0 不实现本地 → 远端上传、双向同步、自动冲突合并、远端 rename / move / delete、
多节点/分布式同步或调度、SQLite 存储文件内容，以及浏览器转码/编辑等复杂文件处理。

v1.0 后按真实需求考虑：

- 传输：断点续传、带宽限制、每 Source 并发、校验和验证策略。
- 自动化：通知集成、webhook、文件保留策略、Home Assistant 集成。
- 数据与运维：更丰富的检索/索引、加密凭证存储、配置导入导出、metrics endpoint / Prometheus。
- 客户端：CLI 远程客户端、面向移动端的 Web UI。

除非项目定位明确改变，仍不默认规划双向同步、分布式同步集群、远端文件编辑或通用网盘替代品。

## 如何维护进度

- 本页维护阶段状态、当前优先级与跨阶段边界；阶段文档维护模型、任务细项和验收标准，避免在 AGENTS.md 重复维护进度。
- 完成能力后同时更新阶段清单与本页摘要，附相关实现/测试路径及实际验收结果；只有配置或示例时不标为已完成。
- 本地验证、远端 CI、GitHub Release、WebDAV 镜像分别记录；发布以不可变 tag 和实际发行资产为依据，镜像失败不改变 GitHub Release 的权威地位。
- 用户可见交付同步进入 CHANGELOG；内部计划调整与文档整理不写入发布摘要。
