# v0.6.0 — Remote Browser, Local Files & Publishing

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下均为规划；模型、接口和路由示例用于设计讨论，不代表当前可用契约。

目标：补齐日常文件管理和安全访问能力。

## Remote Browser

只读：

```text
browse
stat
select
download
trigger sync
```

明确不支持：

```text
remote upload
remote delete
remote rename
remote move
```

## Virtualized Browser

针对大目录：

- [ ] 分页/游标。
- [ ] 前端虚拟列表。
- [ ] lazy directory loading。
- [ ] 大目录不一次性加载全部条目。

## Local File Browser

- [ ] 浏览同步根目录。
- [ ] 文件信息。
- [ ] 下载。
- [ ] Range。
- [ ] HEAD。
- [ ] MIME。
- [ ] Content-Disposition。

是否支持本地删除/rename，应独立评估，不默认进入第一版。

## Publishing

Publish 与 Sync Job 分离：

```text
Remote
  ↓
Sync
  ↓
Local File
  ↓
Publish Policy
  ↓
HTTP
```

模型：

```text
published_files
├── id
├── local_path
├── public_path
├── enabled
├── expires_at
└── ...
```

安全要求：

- [ ] 默认 private。
- [ ] path traversal protection。
- [ ] symlink escape protection。
- [ ] root confinement。
- [ ] Range。
- [ ] HEAD。
- [ ] MIME。
- [ ] cache policy。

## 完成标准

> 用户可以从 Web UI 浏览远端和本地文件，并选择性将同步后的本地文件通过 HTTP 暴露。
