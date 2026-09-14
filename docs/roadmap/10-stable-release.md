# v1.0.0 — Stable Single-Node Release

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

v1.0 的含义：

> TinySync 已经是一个稳定、可升级、可恢复的单节点多协议远端文件 Pull/Mirror 服务。

## Required Features

### Sources

- [ ] WebDAV。
- [ ] S3。
- [ ] SFTP。

### Sync

- [ ] Copy。
- [ ] Mirror。
- [ ] include/exclude selectors。
- [ ] atomic download。
- [ ] managed-file ownership。
- [ ] concurrency control。
- [ ] retry / timeout。
- [ ] manual / once / interval / cron。

### Management

- [ ] Web UI。
- [ ] REST API。
- [ ] Remote Browser。
- [ ] Local File Browser。
- [ ] Sync history。
- [ ] API tokens。
- [ ] Publishing。
- [ ] MCP。

### Operations

- [ ] SQLite migrations。
- [ ] Upgrade path。
- [ ] Backup / restore guidance。
- [ ] Six-platform release。
- [ ] GitHub Release。
- [ ] WebDAV mirror。
- [ ] Changelog。
- [ ] Codecov。
- [ ] Cross-platform smoke。

## v1.0 Quality Gate

发布 v1.0 前必须确认：

```text
clean install
upgrade from previous release
database migration
WebDAV end-to-end sync
S3 end-to-end sync
SFTP end-to-end sync
Copy semantics
Mirror safety
scheduler restart recovery
large file transfer
large directory browsing
token authorization
published file confinement
MCP basic workflow
Linux/macOS/Windows smoke
```
