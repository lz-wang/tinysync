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
其中 Remote Source 管理链路（持久化、WebDAV 只读访问与连接测试）与
WebDAV Pull Sync 手动同步链路（Sync Job → Selector → Sync Engine →
Local Files）已实现；调度、持久化历史、发布、认证与 MCP 尚未实现。

## 当前状态与优先级

本地核对日期：2026-09-15。当前实现、测试和 workflow 支持以下判断。

- **已实现**：`serve`、`version` / `--version`、数据目录与端口配置、日志、HTTP 生命周期、health/version API、内嵌状态页及 fallback 构建；SQLite 持久化与 migration、Source 领域（模型、SQLite Repository、WebDAV 只读 adapter、应用服务）、Source CRUD 与连接测试 REST API、Sources Web 管理界面；Sync Job 领域（模型、Selector、remote scanner、planner、原子下载、Copy/Mirror engine、手动运行 Runner、SQLite Repository）、Jobs REST API 与 Web 管理界面、端到端与跨重启持久化测试。
- **已有工程配置**：Git 版本注入、Go 测试、前端静态检查、Codecov、六平台构建、三平台原生 Smoke、Build/Release 分离及 WebDAV 镜像。
- **v0.1.0 已发布**：远端 `v0.1.0` Release workflow 成功（2026-09-14），六平台发行档案与 `checksums.txt` 已核对到位；WebDAV 镜像按发布规范需独立验收，不能由 GitHub Release 成功推导。
- **尚未实现**：调度与历史、文件浏览/发布、认证与 Token、MCP。

v0.2.0 持久化与 Source 管理已发布：远端 Release workflow 成功（2026-09-15），
六平台发行档与 `checksums.txt` 核对到位，WebDAV 镜像与通知独立验收通过。
v0.3.0 WebDAV Pull Sync 已完成实现并通过本地与浏览器验收：
第一条手动触发的 WebDAV → Local 单向同步链路可用（Copy / Mirror、
doublestar 选择器、原子下载、managed_files 删除授权、手动 Run 与实时状态）。
当前状态为**实现完成，发布待验收**：待 `make ci`、远端三平台 smoke 与
六平台构建通过后打 `v0.3.0` tag 进入发布验收。

## 架构与边界

- 单一 Go + Gin 二进制，内嵌 React + MUI Web UI；全工程 `CGO_ENABLED=0`。
- 启动链路为 `main → cmd → app → api`：`main` 负责 signal context 和前端资源接入，`internal/cmd` 负责 CLI 解析，`internal/app` 是装配根，`internal/api` 负责 HTTP 路由与生命周期。新增业务放在 `internal/` 的对应领域包，入口只做装配。
- 版本号只来自 Git，由 Makefile 解析并经 `-ldflags` 注入 `internal/buildinfo.Version`；不引入 VERSION 文件、package version 或配置版本作为第二个发布版本来源。
- `-tags webui` 嵌入 `web/dist`，无 tag 时嵌入 `web/fallback`，保证无 Node 环境可完整编译、测试 Go 代码。
- 未知 `/api/v1/*` 必须返回 404，不得被 SPA fallback 吞掉；前后端 API 契约变更同步更新两端实现与测试。
- 当前运行配置仅 `--datadir` / `--port`，环境变量为 `TINYSYNC_DATADIR` / `TINYSYNC_PORT`；新增配置先明确用途。
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
| v0.3.0 | [WebDAV Pull Sync](docs/roadmap/03-webdav-pull-sync.md) | 实现完成，发布待验收 | 手动执行 Copy / Mirror，支持选择器、原子下载与本地文件归属保护 |
| v0.4.0 | [调度与同步历史](docs/roadmap/04-scheduler-and-history.md) | 规划 | 自动调度、重叠跳过、并发控制及运行/文件明细可追踪 |
| v0.5.0 | [S3 与 SFTP](docs/roadmap/05-s3-and-sftp.md) | 规划 | 同一同步引擎支持三种协议，上层不依赖协议分支 |
| v0.6.0 | [文件浏览与发布](docs/roadmap/06-file-browser-and-publishing.md) | 规划 | 只读远端浏览、本地下载与选择性 HTTP 发布，限制访问根目录 |
| v0.7.0 | [认证与 API Token](docs/roadmap/07-authentication-and-tokens.md) | 规划 | Web session 与 API Token 分离，支持 scope、过期和撤销 |
| v0.8.0 | [MCP 集成](docs/roadmap/08-mcp-integration.md) | 规划 | 复用应用服务与鉴权，查询/运行任务，大文件经 HTTP 获取 |
| v0.9.0 | [可靠性与运维](docs/roadmap/09-hardening-and-operations.md) | 规划 | 恢复、安全、协议兼容、跨平台、性能及回归验证具备证据 |
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
