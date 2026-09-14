# v0.9.0 — Hardening & Operations

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

目标：在 v1.0 前集中处理恢复、安全和长期运行问题。

## Sync Reliability

- [ ] Retry policy。
- [ ] Backoff。
- [ ] Transfer timeout。
- [ ] Partial file cleanup。
- [ ] Crash recovery。
- [ ] stale running state recovery。
- [ ] orphan managed file metadata cleanup。
- [ ] idempotent sync。

## Database

- [ ] Migration upgrade testing。
- [ ] Backup。
- [ ] Restore。
- [ ] corruption handling。
- [ ] WAL/checkpoint strategy。
- [ ] graceful shutdown consistency。

## Filesystem

- [ ] Path traversal fuzz/test。
- [ ] symlink escape。
- [ ] case sensitivity。
- [ ] filename compatibility。
- [ ] Windows path handling。
- [ ] permission errors。
- [ ] disk-full behavior。
- [ ] atomic rename boundary。

## Protocol

- [ ] WebDAV interoperability tests。
- [ ] S3-compatible providers。
- [ ] SFTP server differences。
- [ ] transient network failure。
- [ ] large directory behavior。
- [ ] large file behavior。

## Observability

至少形成：

```text
request_id
job_id
run_id
source_id
path
duration
bytes
status
error
```

保持应用日志与 access log 边界清晰。

## Performance

建立基准：

- [ ] 10k files listing。
- [ ] 100k managed metadata。
- [ ] 大文件 transfer。
- [ ] 小文件批量 sync。
- [ ] WebUI large list。
- [ ] SQLite query latency。

不要在有数据前提前复杂优化。

## Test

逐步增加：

- [ ] Repository unit tests。
- [ ] Source contract tests。
- [ ] Sync engine tests。
- [ ] Integration tests。
- [ ] Filesystem safety tests。
- [ ] Scheduler tests。
- [ ] API tests。
- [ ] release smoke。

核心领域代码应成为 coverage 重点。

## 完成标准

恢复、安全、跨平台和长期运行场景均有可复现的验证结果，升级与备份恢复路径已验证，
关键领域回归测试与性能基准能够支撑 v1.0 验收。
