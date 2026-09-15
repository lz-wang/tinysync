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
