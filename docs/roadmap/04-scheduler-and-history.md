# v0.4.0 — Scheduler & Sync History

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。本文件是 v0.4.0 的
实现契约：Schedule 模型、misfire 语义、overlap 与并发策略、schema、
Runner 演进、调度器实现、REST API 与 Web UI 的设计均已冻结，实现与本契约
冲突时以本文件为准并先行修订本文件。

目标：让同步任务可长期无人值守运行，并具有完整历史可观测性。

```text
Schedule Configuration
        ↓
Scheduler（trigger producer）
        ↓
Persistent Runner（execution coordinator）
        ↓
Bounded Concurrency
        ↓
Sync Engine
        ↓
Persistent Run / Run Items（authoritative run state）
        ↓
REST API
        ↓
Jobs + History Web UI
```

v0.3 的 `Runner = execution + transient state` 在本阶段拆分为：
Runner 只做执行协调，History Repository 是运行状态的唯一事实来源，
Scheduler 是触发生产者，Engine 是同步执行。到 v0.5.0 增加 S3 / SFTP 时，
新协议只需进入既有 `source.Remote → Engine` 边界，Scheduler、Runner、
History 与 Web History 都不感知协议差异。

明确不做（推迟到后续阶段）：queue、cancel_previous、Source 级并发
（v0.5 多协议阶段设计）、cron 秒域与 `@descriptors`、全量 unchanged 文件
审计、priority queue / 持久化 task queue / leader election / 分布式调度、
S3 / SFTP、发布、认证 / Token、MCP。

## Schedule 契约

| 项目 | 契约 |
| --- | --- |
| Job 总开关 | 沿用既有 `Job.Enabled`，不新增 `schedule.enabled` |
| Schedule 类型 | `manual / once / interval / cron` |
| 默认值 | 新建及升级既有 Job 均为 `manual`，v0.3 → v0.4 无行为变化 |
| once | RFC3339 绝对时间（如 `2026-09-20T03:00:00+08:00`），入库归一为 UTC RFC3339 |
| interval | Go duration 风格（如 `30m`、`6h`），最低 `1m` |
| interval 基准 | 持久化 `anchor_at`，防止服务重启后调度相位漂移 |
| cron | 标准 5-field cron，不支持 seconds、不支持 `@descriptors` |
| cron 时区 | Web UI 创建的 Cron 与单次指定时间一致，使用运行 TinySync 机器的本地时区；自动化 API 仍可选填 IANA timezone |

Schedule 与 Job 严格 1:1 且无独立生命周期，不建 `schedules` 表，直接扩展
`sync_jobs`。持久化为扁平列，API 不暴露互斥 nullable 字段，而是
discriminated object：

```json
{"schedule": {"type": "manual"}}
```

```json
{"schedule": {"type": "once", "at": "2026-09-20T03:00:00Z"}}
```

```json
{"schedule": {"type": "interval", "every": "30m"}}
```

```json
{"schedule": {"type": "cron", "expression": "0 3 * * *"}}
```

cron 解析只引入窄依赖 `github.com/robfig/cron/v3` 的
`cron.NewParser(Minute|Hour|Dom|Month|Dow)`，仅使用 parse + `Next()`；
不围绕 cron library 建立应用生命周期，TinySync 自己的 Scheduler 仍负责
Job、Runner、历史和并发策略。

## Misfire 语义

```text
missed cron / interval   → 不补跑服务离线期间错过的周期
missed once              → 服务恢复后仍未消费的 once 立即执行一次
```

调度器以内存游标划定处理窗口，天然不回看离线期间的历史周期。interval
的相位由持久化 anchor 推导（`anchor + n*interval`），重启只影响「从哪个
时刻继续」，不改变边界相位。游标只在成功读取并处理完全部 Job 后推进：
`repo.List` 失败、任一 occurrence 的消费检查或 run 落库失败时游标保持
不动，下一 tick 重扫同一窗口——内部瞬时失败不会吞掉 occurrence；已
成功持久化的 occurrence 由消费记录去重，重扫不会重复执行。确定性校验
失败（Job / Source 禁用、Job 已删除）按消费丢弃，避免游标被永久卡住。

once 的「已消费」判定不依赖可裁剪的运行历史：消费状态持久化在
`sync_jobs.once_consumed_for`（occurrence 的 Unix ms 时间戳；migration
`0004_once_consumption.sql`，升级时从存量 once run 回填）。调度触发
的 run 落库与 once 消费写入在同一个事务（`RunRepository.
PersistScheduledRun`），「run 存在 ⇔ occurrence 已消费」原子成立，
两次独立写之间的失败不会再造成 once 重放。interval / cron 的
occurrence 幂等（游标回退重扫去重）仍以
`sync_runs(job_id, trigger, scheduled_for)` 判定——它们的重复触发只是
幂等的多跑一轮，无 once 的单次语义约束。

Job 配置变更（PATCH / DELETE）与执行链经 per-Job 协调位原子互斥；
变更占用是毫秒级的瞬时状态，期间到期的调度触发不消费 occurrence
（返回瞬时错误，游标保持重试），变更完成后按新配置执行——一次顺手
改名不该永久吞掉一次 once。

## Overlap Policy

v1 默认 `skip`，不排队：

```text
同 Job overlap，自动调度    → 记录 status=skipped 的 run（error 记原因）
同 Job overlap，手动触发    → HTTP 409（保持 v0.3 行为）
全局并发满，自动调度        → 记录 skipped，reason=concurrency_limit
全局并发满，手动触发        → HTTP 409
配置变更占用中，自动调度    → 不消费：瞬时错误，稍后重试
配置变更占用中，手动触发    → HTTP 409（sync job is being modified）
```

## 并发模型

| 配置 | 默认值 | 含义 | 环境变量 |
| --- | ---: | --- | --- |
| `--max-concurrent-jobs` | `1` | 全进程同时运行的 Job 数上限（保持 v0.3 默认行为不突变） | `TINYSYNC_MAX_CONCURRENT_JOBS` |
| `--max-concurrent-transfers` | `4` | 全进程同时进行的远端文件下载上限 | `TINYSYNC_MAX_CONCURRENT_TRANSFERS` |

SQLite 固定单连接池：所有 metadata / history 写操作顺序进入一个连接。
因此传输改造为「远端下载 I/O 并行、SQLite 状态推进受控串行化」：

```text
逐条 mark pending（协调者串行写）
        ↓
bounded concurrent remote downloads（仅 I/O 并行）
        ↓
result channel
        ↓
single coordinator：managed synced + run item + stats（串行写）
```

只对实际开始传输的文件登记 pending（保持 v0.3「pending 强制重传仅覆盖
被中断传输」语义），失败后未派发的文件不登记。第一次传输失败后：

```text
cancel 剩余工作
        ↓
等待正在执行的 worker 收敛
        ↓
保留 pending 状态（下一轮强制重传）
        ↓
不执行 relinquish
        ↓
不执行 Mirror delete
        ↓
finalize run = failed
```

与 v0.3 已冻结的失败安全语义一致：传输失败后不执行后续 metadata 变更与
Mirror 删除，下一轮继续收敛。

## 数据库设计

migration `0003_scheduler_history.sql` 与 `0004_once_consumption.sql`：

`sync_jobs` 增加四列（全部带默认值，既有 Job 升级后即为 `manual`）：

```text
schedule_type       TEXT NOT NULL DEFAULT 'manual'
                    CHECK (schedule_type IN ('manual', 'once', 'interval', 'cron'))
schedule_value      TEXT NOT NULL DEFAULT ''
schedule_timezone   TEXT NOT NULL DEFAULT ''
schedule_anchor_at  INTEGER NULL          -- interval 相位基准，Unix ms
```

`0004` 增加 once 消费状态（调度器 correctness state，与可裁剪的
审计历史分离；升级时从存量 once run 回填最近一次 occurrence）：

```text
once_consumed_for   INTEGER NULL          -- once occurrence，Unix ms
                                          -- 非 once 调度 / 未执行的 once 为 NULL
                                          -- once 语义变更时由服务层清空
```

```text
sync_runs
├── id                 TEXT PK（run_<hex>）
├── job_id             TEXT FK sync_jobs(id) ON DELETE CASCADE
├── trigger_type       manual | once | interval | cron（trigger 为 SQL 保留字，列名 trigger_type）
├── scheduled_for      INTEGER NULL（occurrence 时间，Unix ms；手动运行为 NULL）
├── status             running | succeeded | failed | skipped
├── started_at         INTEGER NOT NULL
├── finished_at        INTEGER NULL
├── files_total        INTEGER NOT NULL DEFAULT 0
├── files_created      INTEGER NOT NULL DEFAULT 0
├── files_updated      INTEGER NOT NULL DEFAULT 0
├── files_deleted      INTEGER NOT NULL DEFAULT 0
├── files_skipped      INTEGER NOT NULL DEFAULT 0
├── bytes_transferred  INTEGER NOT NULL DEFAULT 0
└── error              TEXT NOT NULL DEFAULT ''
```

`scheduled_for` 区分「计划在 03:00 执行、实际 03:00:02 开始」，并作为
interval / cron occurrence 的幂等判定依据；once 的消费判定在
`sync_jobs.once_consumed_for`（见上）。`next run` 运行时计算，不持久化
为事实来源。

```text
sync_run_items
├── id      INTEGER PK AUTOINCREMENT
├── run_id  TEXT FK sync_runs(id) ON DELETE CASCADE
├── path    TEXT（相对 Job.LocalRoot，/ 分隔）
├── action  create | update | delete | relinquish
├── status  succeeded | failed | skipped
├── bytes   INTEGER NOT NULL DEFAULT 0
└── error   TEXT NOT NULL DEFAULT ''
```

索引：`sync_runs(job_id, started_at)`（per-Job 历史 / retention）、
`sync_runs(job_id, trigger_type, scheduled_for)`（interval / cron
occurrence 幂等去重）、`sync_run_items(run_id)`（明细读取 / 级联删除）。

**普通 unchanged 文件不创建 `sync_run_items`**，只累计 summary，
避免 SQLite 快速膨胀。以下情况才写 item：

```text
create succeeded
update succeeded
delete succeeded（Mirror managed 删除）
selector relinquish
local conflict → skipped（action=create / update，status=skipped）
download / update / delete → failed
```

一个有 100 万稳定文件、每天仅变化 20 个文件的 Job，不会每天写入
100 万行 history。若未来需要完整逐文件审计，再将 full audit 作为可选
策略，而不是默认行为。

### Retention

```text
每 Job 保留最近 500 runs
删除 run 时 items 级联删除（FK CASCADE）
```

prune 在 run finalize 之后执行，不阻塞运行路径的失败语义。

### 进程异常退出恢复

下次启动将遗留 `running` 记录收敛为 `failed`，
`error = previous process interrupted`，`finished_at` 取启动时刻。

## Runner 演进

```text
Runner
├── active map[jobID]*activeRun   （替换 v0.3 的 current *activeRun）
├── maxConcurrentJobs
├── maxConcurrentTransfers
├── history RunRepository
├── job Repository
├── managed ManagedRepository
└── source factory
```

触发路径：

```text
Trigger
   ↓
Runner.start()
   ↓
校验 Job / Source
   ↓
原子检查 same-job overlap + global capacity
   ↓
INSERT sync_runs(status=running)     ← 同步写入成功后才启动 goroutine
   ↓
启动 goroutine
   ↓
Engine
   ↓
UPDATE sync_runs(final state + stats)
   ↓
释放 active slot
   ↓
prune history
```

run row 必须在 goroutine 启动前同步写入成功——不允许出现已经开始修改
本地文件、却没有任何历史 run ID 的状态。

现有 `GET /jobs/:id/status` 不删除：数据源从内存 `Runner.last` 改为
持久化 history，接口基本兼容，但重启不再回到 idle。进行中的运行优先
返回 running 快照；否则返回最近一条持久化 run（含 `skipped`）；
从未运行为 idle。

## Scheduler 实现

新增 `internal/syncjob/schedule.go`（Schedule 模型、校验、Next 计算）与
`internal/syncjob/scheduler.go`（调度循环）。不引入通用 scheduler
framework。

```text
Load enabled jobs（schedule_type != manual）
      ↓
按内存游标计算窗口 (from, now] 内的 occurrence
      ↓
到期？
  ├─ no → continue
  └─ yes
       ↓
    Runner.StartScheduled(...)
       ↓
    executed / skipped
```

单 scheduler goroutine + 小周期 ticker（1 秒）：Job 数量小，SQLite 负载
可忽略；动态修改 Job 不需要 cron registration/unregistration；REST 与
未来 MCP 修改 Job 后无需额外通知 Scheduler；调度规则以 SQLite Job 配置
为事实来源；`tick()` 抽出后可用固定时间直接测试。

## REST API

保留现有 Job API，只增量扩展：

| API | v0.4 行为 |
| --- | --- |
| `POST /api/v1/jobs` | 支持 `schedule` |
| `PATCH /api/v1/jobs/:id` | 支持原子替换 `schedule`（nil 保留现有值） |
| `GET /api/v1/jobs/:id/status` | 改为持久化 latest run，并增加 `next_run_at` |
| `POST /api/v1/jobs/:id/run` | 保持手动运行语义（trigger=manual） |
| `GET /api/v1/runs` | 全局历史，支持 `job_id / status / limit / offset` |
| `GET /api/v1/runs/:id` | run summary（含 job_name） |
| `GET /api/v1/runs/:id/items` | 文件明细，分页 |

`sync_runs` 是独立资源，提供全局 `/runs` 而不是只设计 `/jobs/:id/runs`，
支撑跨 Job 的全局 History 视图。分页第一版用 `limit + offset`：
`limit` 默认 50、上限 200。

手动运行相关状态码：`202 started`、`404 job not found`、`409 disabled`、
`409 another run active`、`409 concurrency limit`。非法 schedule 返回 400。

## Web UI

`/jobs` 列表：

```text
Name / Source / Mode / Schedule / Last Run / Next Run / Run Now / Actions
```

移除 v0.3「任意 Job running → 全部 Run 按钮禁用」的全局单运行假设；
Run Now 只受本 Job 运行状态控制，全局容量冲突以错误提示呈现。
运行结束刷新持久 history，Last Run 不因刷新或重启消失。

`/history` 列表：

```text
Time / Job / Trigger / Status / Duration / Changes / Bytes
```

点击 row 进入 `/history/:runId`：run summary + 文件变化时间线
（时间线式历史视图，不绑定具体 UI 组件库，不为此引入 `@mui/lab`），
展示状态、trigger、统计、错误与文件级变更明细。

Job 编辑器集中承载 Source、Root、Mode、Selector、Enabled 与 Schedule，
schedule editor 直接加入既有对话框，不另建 Scheduler 管理页面。

## 实施顺序

14 个 commit，每个保持 `make check` 可通过，不提前暴露半完成的用户能力：

1. `docs: 完善 v0.4.0 调度与历史设计`（本文件）
2. `feat(storage): 增加调度与同步历史 schema`——`0003_scheduler_history.sql`、真实 v2→v3 migration 测试
3. `feat(syncjob): 增加 schedule 模型与校验`——`ScheduleType / Schedule`、manual/once/interval/cron 校验、cron parse（缺省机器本地时区）、interval anchor、`Next()` 计算
4. `feat(syncjob): 持久化 Job schedule`——SQLite Job repository 读写新字段、既有 Job 自动 manual、schedule 变更重设 interval anchor、不影响 mapping reset 事务语义
5. `feat(syncjob): 增加持久化 run history`——Run / RunItem 模型、RunRepository 接口与 SQLite 实现（Latest / List / Get / Items / Finalize / Prune）、启动时 stale-running recovery
6. `feat(syncjob): 支持受控的多 Job Runner`——`current` → `active map`、`MaxConcurrentJobs`、run 启动前持久化、same-job overlap 与全局容量拒绝、`StartScheduled` 与 skipped 记录、Wait / Shutdown 改造
7. `feat(syncjob): 增加并行传输与文件级历史`——`MaxConcurrentTransfers`、pending 逐条登记 + 有界下载 workers + 结果协调、RunItem 记录、失败取消与后置 relinquish / Mirror delete
8. `feat(syncjob): 增加自动调度器`——Scheduler / tick、once / interval / cron、不补跑、overlap / capacity → skipped、next run 计算、app 生命周期 Start / Stop
9. `feat(config): 增加同步并发配置`——`Config` 与 CLI / env 两个并发参数、正整数校验、默认 Jobs=1 / Transfers=4
10. `feat(api): 增加 schedule 与 sync history API`——Job DTO schedule、持久化 status + `next_run_at`、`/runs`、`/runs/:id`、`/runs/:id/items`、分页与过滤
11. `feat(web): 增加调度配置与持久化运行状态`——`api.ts` 类型、JobDialog schedule editor、Jobs 表 Schedule / Last / Next、移除全局 anyRunning 假设
12. `feat(web): 增加同步历史与运行详情`——`/history`、`/history/:runId`、AppShell 导航、时间线式明细
13. `test: 增加调度与历史端到端覆盖`——真实 SQLite + WebDAV + scheduler、跨 restart history、自动 sync、overlap、capacity、retention
14. `docs: 完成 v0.4.0 实现记录`——阶段 checklist、ROADMAP 状态、README、`CHANGELOG [Unreleased]`、验证证据

## 验收矩阵

| 场景 | 预期 |
| --- | --- |
| v0.3 DB 升级 | 所有既有 Job 自动成为 `manual`，不自动启动 |
| Manual run | 立即返回 run ID；运行历史持久化 |
| Restart | 上次 completed run 仍可查询 |
| Crash with running run | stale `running` 自动收敛为 failed |
| Once | 正常仅触发一次 |
| Once missed during downtime | 恢复后触发一次 |
| Interval | 按持久化 anchor 调度，重启不改变相位 |
| Cron | 缺省使用机器本地时区；API 指定 IANA 时区时按该时区，DST 行为交由 timezone / cron parser |
| Cron / interval downtime | 不补跑历史 missed occurrences |
| Same Job overlap | scheduled occurrence = skipped |
| Manual same-job overlap | 409 |
| Global capacity | 不超过 `MaxConcurrentJobs` |
| Transfer capacity | 全进程远端下载数不超过 `MaxConcurrentTransfers` |
| Transfer failure | run failed；已完成文件安全；Mirror delete 不执行 |
| Local conflict | 文件不覆盖；RunItem 记录 skipped |
| Mirror | 仍只能删除 `managed_files` 授权路径 |
| Retention | 每 Job 最多 500 runs，items 随 run 删除 |
| Job delete | history 与 managed metadata cascade，真实本地文件保留 |
| UI refresh | Last Run 不消失 |
| UI restart | Last Run 仍存在；Next Run 重新正确计算 |

## 交付清单

- [x] `0003_scheduler_history.sql`：`sync_jobs` schedule 列、`sync_runs`、`sync_run_items`、索引与 FK，真实 v2→v3 迁移与备份测试。
- [x] `0004_once_consumption.sql`：`sync_jobs.once_consumed_for` once 消费状态（含存量回填），真实 v3→v4 迁移专项测试。
- [x] Schedule 模型与校验：manual / once / interval / cron、Cron 缺省机器本地时区、interval anchor、Next 计算、非法输入拒绝。
- [x] SQLite Job repository 读写 schedule 字段；既有 Job 升级后自动 manual。
- [x] Run / RunItem 模型与 RunRepository（SQLite 实现）；启动时 stale-running recovery。
- [x] 多 Job Runner：active map、MaxConcurrentJobs、run 先持久化、same-job overlap 与容量控制、StartScheduled 与 skipped 记录。
- [x] 并行传输：MaxConcurrentTransfers、有界下载、单协调者串行状态推进、失败取消、文件级历史（unchanged 不写 item）。
- [x] 自动调度器：once / interval / cron、不补跑、once 恢复补执行、overlap / capacity → skipped、next run 计算。
- [x] 并发配置：CLI / env、默认 Jobs=1 / Transfers=4、正整数校验。
- [x] REST API：Job schedule、持久化 status + next_run_at、/runs 列表 / 详情 / 明细、分页与过滤。
- [x] Web UI：Jobs 表 Schedule / Last Run / Next Run、JobDialog schedule editor、/history 与 /history/:runId、移除全局单运行假设。
- [x] 端到端：真实 WebDAV + SQLite + scheduler，跨 restart、overlap、capacity、retention。
- [x] `make check` 全绿；`make build` 通过（Web 嵌入资源变更）。

## 实现记录

实现路径：

- 领域与调度：`internal/syncjob`（`schedule.go`、`run.go`、`scheduler.go`、
  重构后的 `runner.go` / `engine.go`）。
- 持久化：`internal/storage/migrations/0003_scheduler_history.sql` 与
  `0004_once_consumption.sql`、`internal/syncjob/sqlite/run_repository.go`
  （含 `PersistScheduledRun` 事务路径）、既有 `repository.go` 扩展
  schedule 四列与 once 消费列。
- 装配与配置：`internal/app/app.go`（stale 恢复、调度器生命周期、
  并发注入）、`internal/config` / `internal/cmd`（两个并发 flag 与 env）。
- REST API：`internal/api/job.go`（schedule DTO、next_run_at、
  /runs / /runs/:id / /runs/:id/items）。
- Web UI：`web/src/api.ts`、`web/src/features/jobs/JobDialog.tsx`、
  `web/src/pages/JobsPage.tsx`、`web/src/pages/HistoryPage.tsx`、
  `web/src/pages/RunDetailPage.tsx`、`web/src/features/history/shared.tsx`。
- 端到端：`internal/e2e/scheduler_history_test.go`（真实 x/net/webdav
  服务端 + SQLite + Scheduler：自动同步、once 补执行、容量跳过、
  跨重启历史与 stale 恢复、retention 级联）。

实现要点（与契约的对应关系）：

- cron 依赖为 `github.com/robfig/cron/v3` 的 5-field parser，仅
  parse + Next；表达式按运行机器的本地时区解释。
- 调度幂等：interval / cron 由内存游标窗口 (from, now] 保证不补跑，
  once 由 `sync_runs(job_id, trigger_type, scheduled_for)` 消费判定；
  `next_run_at` 运行时计算，不持久化。
- 传输阶段 pending 逐条登记（协调者串行写，仅覆盖实际派发的文件），
  下载按 `MaxConcurrentTransfers` 并行，synced 推进 / 明细 / 统计由
  单协调者串行写；失败后排空在途结果且不推进 synced（保留 pending），
  relinquish 与 Mirror delete 保持后置。
- unchanged 文件只累计 summary；create / update / delete / relinquish、
  本地冲突（skipped）与传输失败（failed）写 `sync_run_items`。

## 完成标准

> TinySync 可以作为常驻 HomeLab 服务自动运行 WebDAV 同步任务，
> 并完整追踪每次同步发生了什么。

本地验收已完成（2026-09-16）：`make check` 全绿（Go 全量测试、vet、
goimports-reviser、Biome、TypeScript）；`make build` 通过（webui 嵌入
路径）；`go test -race` 通过（syncjob 与 e2e）；验收矩阵中除「发布」
相关项外全部由单元测试与端到端测试覆盖，其中 restart / UI restart
（Last Run 不消失、Next Run 重新计算）经跨重启持久化测试验证。
发布门禁（`make ci`、原生 Smoke、Release workflow、发行档与镜像验收）
未开始，进入 v0.4.0 发布流程前需按[构建与发布](../guides/release.md)执行。
