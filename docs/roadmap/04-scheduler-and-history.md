# v0.4.0 — Scheduler & Sync History

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

目标：让同步任务可长期无人值守运行，并具有完整历史可观测性。

## Scheduler

支持：

```text
manual
once
interval
cron
```

配置：

```text
timezone
enabled
schedule
```

## Overlap Policy

v1 默认：

```text
skip
```

即：

> 同一个 Job 上一次运行未完成时，新调度跳过。

未来可考虑：

```text
queue
cancel_previous
```

但不作为初版能力。

## Concurrency

全局：

```text
MaxConcurrentJobs
MaxConcurrentTransfers
```

需要明确：

- Job 并发限制。
- 单 Job 文件下载并发限制。
- Source 连接压力控制。

## Sync Run

```text
sync_runs
├── id
├── job_id
├── trigger
├── status
├── started_at
├── finished_at
├── files_total
├── files_created
├── files_updated
├── files_deleted
├── files_skipped
├── bytes_transferred
└── error
```

## Sync Run Item

必要时保存文件级事件：

```text
sync_run_items
├── run_id
├── path
├── action
├── status
├── bytes
└── error
```

需要控制历史容量，避免 SQLite 无限增长。

## Web UI

- [ ] Scheduler editor。
- [ ] 上次运行。
- [ ] 下次运行。
- [ ] Sync history 列表。
- [ ] MUI Timeline。
- [ ] Run detail。
- [ ] 文件级变更明细。
- [ ] 错误信息。

## 完成标准

> TinySync 可以作为常驻 HomeLab 服务自动运行 WebDAV 同步任务，
> 并完整追踪每次同步发生了什么。
