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

## [0.5.0] - 2026-09-16

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
