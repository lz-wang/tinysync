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

每轮 run 传输开始前批量清理计划内各 target-id 的其它 partial
（远端更新后不留垃圾；按目录聚合、每目录一次枚举，绝不触碰其它
target 的 partial），独立调用的 Downloader 保留等价的单文件路径。

### 启动清理与 retention

| 文件 | 启动处理 |
|---|---|
| legacy `.tinysync-part-<12 hex>` | 立即删除（不可恢复） |
| `.tinysync-part-v1-*` | 保留，供 resume |
| v1 partial 超过 30 天 | 删除（orphan GC） |

`partialRetention = 30 天`不是协议语义：正常收敛由传输开始前的
partial 清理主动回收旧 fingerprint partial（run-level 批量操作，每
目录一次枚举），retention 只负责 Job 停用等仍被配置引用的孤儿。
Job 删除 / LocalRoot 变更会使旧 root 退出配置面——启动清理永远
枚举不到它，retention 对这类 mapping 变更孤儿实际无效，因此在
mutation 提交后立即回收旧 root 的传输中间文件
（`RemoveTransferTemps`）。

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
| smb | `*smb2.File.Seek`（断言 io.Seeker + Stat） | handle `Stat()` + expected（Lstat → Open 的同 size 替换 TOCTOU 由 handle 级校验关闭） |
| s3 | `GetObject Range=bytes=N-` + `If-Match: ETag`；无 ETag 退化 `If-Unmodified-Since`；两者皆无 → ErrResumeUnsupported | 412 → ErrRemoteChanged；所依赖的响应 validator（ETag / LastModified）必须存在且与快照一致——省略 → ErrResumeUnsupported，不一致 → ErrRemoteChanged；ContentRange 必须 `start==offset && total==expected.Size` |
| github_release | asset 下载 `Range`（穿过 302 至 CDN） | `fingerprintOf(asset)` 与 expected 显式比对 + asset ID / Version + SHA256 |
| http | `Range` + `If-Range`（仅 strong ETag）；无 strong ETag → ErrResumeUnsupported | 206 + Content-Range 严格校验；200 → Stat 复核 → unsupported/changed |
| webdav | 直接 HTTP GET `Range`（复用 webdav.HTTPClient 认证） | 同 http |

HTTP 家族只接受 strong ETag 作为 If-Range 身份断言（RFC 9110：普通
Last-Modified 默认是 weak validator，HTTP-date 只有秒精度，同一秒内
的 same-size 替换与未变更无法区分）。能力后果：HTTP 的 JSON 目录
索引 profile（nginx JSON / Caddy browse）不含 etag 字段，此类 Source
的快照天然无 strong ETag，续传自动降级完整下载；HTML profile 经
HEAD 捕获 ETag 后可续传。WebDAV 的 PROPFIND getetag 原生携带 ETag，
不受影响。

HTTP 家族的铁律：**Range 请求返回 200 时绝不能把 body 交给
Downloader append**（prefix + 完整文件 = 确定性损坏）；必须 206 且
`Content-Range` 起点等于请求 offset。

身份校验 fail-closed（安全不变量 5 的实现口径）：206 /
Content-Range 只能证明「这是该 URL 当前对象的 N..EOF」，无法证明
「这是生成 partial 时那个对象的 N..EOF」——所有协议必须先持有可
比对的身份信号（handle metadata、strong ETag、asset 身份；HTTP-date
秒精度的 Last-Modified 是 RFC 9110 weak validator，同一秒内的
same-size 替换不可识别，不作为 HTTP/WebDAV 的续传身份），无法证明
一致性时返回 ErrResumeUnsupported 降级完整下载，绝不以「区间大小
吻合」替代对象身份。same-size 替换是这些不变量的最小反例：只比较
Size 的校验在旧对象与新对象 Size 相等时必然漏判。

### offset 边界契约

`ResumableRemote.OpenFrom` 的 offset 语义跨协议统一（共享 helper
`source.CheckResumeOffset` / `source.EmptyResumeStream`）：

```text
offset < 0     → ErrInvalid
offset > size  → ErrRemoteChanged（远端已缩小）
offset == size → 空流（立即 EOF，不发注定 416 的 Range 请求）
其余           → 正常续传流程
```

契约套件按严格断言验收（空流不再容忍错误返回），覆盖 same-size
替换、beyond-end 与 negative offset 场景。

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
