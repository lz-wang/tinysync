# ADR 0010：可断点续传的文件传输

日期：2026-10-04
状态：已接受

## 背景

HomeLab 场景中大文件（系统镜像、备份归档、发行版 ISO）的单次传输
经常跨越数小时甚至数天。当前 Downloader 的任何一次 attempt 失败都
从 offset 0 重来，且三层机制联合阻止了续传：

```text
1. Remote.Open() 不支持 offset；
2. Downloader 失败即删除 partial；
3. 服务启动时 RemoveStaleTempFiles 又删除 crash 遗留 partial。
```

一个 100 GiB 文件在 93 GiB 处断网，意味着 93 GiB 的重复下载。本 ADR
把「断点续传」定义为同步引擎的统一能力（v0.16.0），而不是给各
Source 增加独立的传输逻辑。

## 决策

### 可选的 ResumableRemote 能力，不修改强制接口

沿用 `TreeScanner` / `DirectoryCreator` 的 optional capability 模式：

```go
type ResumableRemote interface {
    OpenFrom(ctx context.Context, path string, offset int64,
        expected Fingerprint) (io.ReadCloser, error)
}
```

实现方必须保证：

1. 返回数据的第一个字节就是 `offset`；
2. 数据仍对应 `expected` 所代表的远端对象（身份不一致时返回
   `ErrRemoteChanged`，绝不返回另一个对象的字节流）；
3. 不允许服务器忽略 Range 后把完整文件冒充 offset stream；
4. 无法安全续传时返回 `ErrResumeUnsupported`。

HTTP / WebDAV 是否支持 Range 是**服务器运行时能力**，不能按 Source
类型静态判断。因此不设 `SupportsResume() bool`，WebUI 也不增加
「Enable Resume」配置项——断点续传默认自动发生、自动降级。

两个公共错误的语义严格区分：

```text
ErrResumeUnsupported                        ErrRemoteChanged
        ↓                                          ↓
删除 / 截断 partial                       禁止拼接 partial
        ↓                                          ↓
自动退化为完整下载                          当前 run 失败
        ↓                                          ↓
不算任务失败                               下一 run 重新 scan 对齐
```

Range 不被支持绝不能把同步任务判失败。

### 不增加 SQLite checkpoint 表

断点位置天然存在于 partial file 自身长度：partial 67 MiB、远端
100 MiB，则 resume offset = 67 MiB。`managed_files.state = pending`
已经表达「文件尚未完成同步」。不引入
`transfer_checkpoints / partial_transfers / download_sessions` 表，
避免「网络写 → 进度回调 → SQLite update」的高频 WAL 写，维持
「传输进度只放内存」的既有设计。

### deterministic partial file

随机后缀临时文件（`.tinysync-part-<12 hex>`）改为确定性命名：

```text
.tinysync-part-v1-<target-id>-<remote-id>

target-id = SHA256(jobID ‖ localRelPath)[:16]
remote-id = SHA256(sourceID ‖ logicalPath ‖ Size ‖ ModifiedAt
                   ‖ ETag ‖ Checksum ‖ Version)[:16]
```

字段以长度前缀二进制编码进入哈希（不 `Sprintf("%v", fp)`），保证
Fingerprint 未来增删字段时 identity 不漂移。同一文件同一远端版本
跨 run 得到相同 partial 路径；远端任一身份字段变化即产生新
remote-id，旧 partial 永不被错误复用。

Downloader 开始传输前清理同一 target-id 的其它 partial（远端更新
后不留垃圾），但绝不触碰其它 target 的 partial。

### 启动清理与 retention

| 文件 | 启动处理 |
|---|---|
| legacy `.tinysync-part-<12 hex>` | 立即删除（不可恢复） |
| `.tinysync-part-v1-*` | 保留，供 resume |
| v1 partial 超过 30 天 | 删除（orphan GC） |

`partialRetention = 30 天`不是协议语义：正常收敛由 Downloader 主动
清理旧 fingerprint partial，retention 只负责 Job 停用 / 删除 /
LocalRoot 变更等孤儿。

### 失败后的 partial 保留策略

partial 中已写入的数据仍是有效 prefix 时**保留**：网络中断、
timeout、context canceled（用户手动停止）、SFTP/SMB 会话中断、
HTTP 5xx、S3 transient、本地 ENOSPC。100 GiB 传到 93 GiB 磁盘满，
扩容后下一轮直接 93 → 100 GiB。

**必须废弃** partial：`ErrRemoteChanged`、checksum mismatch、
partial size > expected.Size、错误 Content-Range / HTTP 206 起点错误、
partial 不是 regular file、identity 不一致。废弃即删除并按语义
收敛（unsupported → 完整重传；changed → 本轮失败）。

### checksum 覆盖「旧 prefix + 新 suffix」

最终 SHA-256 必须是 `SHA256(existing prefix + downloaded suffix)`。
进程重启后第一轮打开 partial 时从磁盘把 `[0, offset)` 喂入 hasher，
同一 Downloader invocation 内的重试保留内存 hash state（只有进程
重启才重新 hash partial prefix）。写入侧由 verifiedWriter 保证
hash 状态严格对应实际落盘字节——短写（ENOSPC 写 12 KiB / 32 KiB）
时 hash 只吃 12 KiB，杜绝 partial size 与内存 hash state 漂移。

### 进度适配

`TransferListener.AttemptStart(offset)` 携带断点：46 MiB 文件从
14.5 MiB 续传时 WebUI 第一眼就是 `14.5 MiB / 46 MiB`。API 增加
`resume_from` 字段（仅展示，无配置项）。`RunStats.BytesTransferred`
语义不变：成功同步文件的 payload 大小（「同步数据量」），不是实际
网络流量；100 GiB 从 90 GiB 续传成功后仍记 100 GiB。未来网络统计
另立 `NetworkBytesDownloaded`，不改旧字段。

### 各协议支持策略

| Source | 续传方式 | 身份保护 |
|---|---|---|
| local | 打开 handle 后 `Seek(offset)` | handle `Stat()` 与 expected 比较 |
| sftp | `*sftp.File.Seek` | `File.Stat()` + expected |
| smb | `*smb2.File.Seek`（断言 io.Seeker） | Lstat + expected |
| s3 | `GetObject Range=bytes=N-` + `If-Match: ETag` | 412 → ErrRemoteChanged；ContentRange 必须 `start==offset && total==expected.Size` |
| github_release | asset 下载 `Range`（穿过 302 至 CDN） | asset ID / Version + SHA256 |
| http | `Range` + `If-Range`（strong ETag 或 Last-Modified） | 206 + Content-Range 严格校验；200 → Stat 复核 → unsupported/changed |
| webdav | 直接 HTTP GET `Range`（复用 webdav.HTTPClient 认证） | 同 http |

HTTP 家族的铁律：**Range 请求返回 200 时绝不能把 body 交给
Downloader append**（prefix + 完整文件 = 确定性损坏）；必须 206 且
`Content-Range` 起点等于请求 offset。

## 安全不变量

断点续传不破坏以下既有属性：

```text
1. partial 永远不是 managed file
2. partial 永远不参与 Mirror delete 授权
3. partial 完成前永远不能替换 target
4. Range 响应必须证明从 requested offset 开始
5. remote identity 变化后禁止 prefix + suffix 拼接
6. 最终 size 必须 == snapshot Size
7. 有 checksum 时必须验证完整文件 checksum
8. 不支持 resume 的服务透明退化为完整下载，不算失败
9. cancel 不删除可恢复 partial
10. 成功后仍只有一次 rename 使 target 生效
```

落地模型保持 `download → verify → fsync → close → rename` 的原子
替换不变；完整扫描、pending 状态、checksum、Mirror 删除授权全部
不需要改变。

## 后果

- 新增 FTP / NFS 等 Source 时只要协议支持随机读取，实现
  `ResumableRemote` 即获得断点续传，同步引擎零改动。
- 遗留的开放问题（后续独立决策）：大文件并行分段下载（单流续传的
  扩展）、S3 / GitHub 强对象身份下持久化 SHA-256 中间状态以避免
  重启后重新 hash 大 partial、上传方向复用同一套 transfer state
  抽象（S3 Multipart Upload 等）。
