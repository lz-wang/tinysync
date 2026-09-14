# v0.5.0 — S3 & SFTP Sources

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

目标：验证 Source abstraction 能稳定承载多协议。

## S3

支持：

- [ ] endpoint。
- [ ] region。
- [ ] bucket。
- [ ] access key / secret key。
- [ ] path style。
- [ ] prefix/root。
- [ ] List。
- [ ] Stat。
- [ ] Open/Range。
- [ ] ETag / version metadata。

注意：

> S3 ETag 不保证等于 MD5，multipart upload 等场景必须按 opaque fingerprint 处理。

## SFTP

支持：

- [ ] host。
- [ ] port。
- [ ] username。
- [ ] password。
- [ ] private key。
- [ ] host key verification。
- [ ] remote root。
- [ ] Stat。
- [ ] List。
- [ ] Open。

## Capability

如果协议能力开始出现差异，增加显式 capability，而不是在业务层：

```go
switch source.Type
```

例如：

```text
range read
checksum
version
mtime
```

## 完成标准

> 同一个 Sync Engine 无需协议分支即可同步 WebDAV、S3 和 SFTP。
