# v0.9.0 — Hardening & Operations

总体进度与当前优先级见 [ROADMAP.md](../../ROADMAP.md)。以下为 v0.9.0 冻结的
可靠性契约：Baseline 记录当前已存在的机制，Goals 是本阶段要交付并可验收的
行为，Non-goals 明确排除到 v1.0 之后。模型、命令与字段示例用于设计讨论，
随各 commit 落地逐步成为真实契约，未落地前不代表可用能力。

目标：使 TinySync 在进程异常退出、网络瞬断、数据库损坏恢复、文件系统差异、
长期运行和大规模数据条件下，行为可预测、失败安全且具备可复现的验证证据。

## 1. Process / DataDir Safety

### Existing baseline

- [x] stale running run records converge to failed on startup（`FailStaleRunning`）
- [x] graceful shutdown persists terminal run state（finalize 使用 `context.WithoutCancel`）
- [x] Runner / Scheduler / API 的优雅关闭顺序与在途运行取消

### Goals

- [x] 同一 datadir 只允许一个 TinySync 进程持有
      `<datadir>/tinysync.lock`（exclusive lock，serve 全生命周期持有）
- [x] lock acquisition fail-fast：第二个实例立即失败退出（exit != 0），
      错误信息包含 datadir 路径，不包含任何 secret
- [x] 锁通过 OS 级 file lock 实现（POSIX flock / Windows LockFileEx），
      不以「文件存在」判定；crash 后由 OS 自动释放，lock 文件本身可保留
- [x] pure Go / `CGO_ENABLED=0`，POSIX 与 Windows 均支持
- [x] `tinysync db check|backup|restore` 复用同一把 datadir lock

```text
CLI parse datadir
      │
      ▼
Acquire <datadir>/tinysync.lock
      │
      ├── fail → another TinySync process owns datadir
      │
      ▼
Open DB → Migrate → Start Runner/Scheduler/API
      │
      ▼
Shutdown → Release lock
```

## 2. Transfer Reliability

### Existing baseline

- [x] 基础重试（最多 3 次尝试，100ms / 200ms 指数退避）
- [x] 失败传输清理临时文件（`.tinysync-part-*`），不触碰已有目标
- [x] pending 先行登记：传输中断的文件下一轮强制重传
- [x] context 取消立即停止传输

### Goals

- [x] 重试分类：只重试 transient error；协议错误判断只能在 adapter
      boundary 内完成（`internal/syncjob` 不出现协议分支）
- [x] permanent error（401 / 403 / 404、invalid path、host-key mismatch、
      invalid credentials、本地 permission / ENOSPC、path safety violation、
      size mismatch）不做无意义重试
- [x] transient error（connection reset、临时网络故障、超时、HTTP
      408 / 429 / 5xx、S3 / SFTP 瞬时故障、远端流中断）按策略重试
- [x] context cancellation / timeout 立即停止 retry
- [x] 重试策略：max attempts = 3；attempt 2 → ~250ms、attempt 3 → ~500ms，
      指数退避 + 有界 jitter，jitter 可注入保证测试确定性
- [x] Transfer timeout 可配置（`--transfer-timeout` /
      `TINYSYNC_TRANSFER_TIMEOUT`，默认 0 = 不启用，保持既有行为）；
      超时作用于单文件单次 attempt，不影响整轮 run 的其它控制语义
- [x] crash 遗留的 transfer 临时文件在启动 Runner / Scheduler 之前
      安全清理：枚举已配置 Job 的 LocalRoot、WalkDir 不跟随 symlink、
      只删除匹配内部临时前缀的普通文件，记录
      `stale_temp_files_removed=N`；不删除其它隐藏文件

## 3. SQLite Recovery & Maintenance

### Existing baseline

- [x] `foreign_keys=ON` + `busy_timeout=5000` + WAL +
      `synchronous=NORMAL` + 单连接池
- [x] migration 前自动 `VACUUM INTO` 一致性备份
- [x] 更高版本 schema 拒绝启动（提示升级二进制）

### Goals

- [x] 数据库完整性原语：`Check`（quick_check / foreign_key_check /
      user_version）、`Backup`（VACUUM INTO）、`Inspect`（按路径打开）
- [x] `quick_check != ok`、外键违规、schema 比 binary 新 → 显式失败；
      不自动「修复」数据库，不静默忽略 corruption
- [x] Migrate 之前先做 integrity check：已损坏的数据库不再继续 migration
- [x] operator 命令：`tinysync db check` / `tinysync db backup` /
      `tinysync db restore --from <file> --force`，全部持有 datadir lock
- [x] backup 默认输出 `<datadir>/backups/tinysync-manual-<时间戳>-<随机>.db`
      （一致性 snapshot，不是裸复制主库文件），POSIX 权限 0600
- [x] restore 流程：validate source backup（quick_check / foreign_key_check /
      user_version ≤ supported）→ 备份当前 DB（pre-restore safety
      backup）→ 同目录 staging 文件 + fsync → 替换数据库 → 清理遗留
      -wal / -shm → reopen + integrity check
- [x] backup schema 较旧：restore 成功，下次 serve 正常向前 migration；
      backup schema 较新：拒绝恢复（不做 downgrade migration）
- [x] serve 运行期间 restore 因 datadir lock 被拒绝
- [x] clean shutdown 顺序收口：停止触发 → 停 Scheduler → cancel/wait
      Runner → finalize run state → 停 HTTP → `PRAGMA
      wal_checkpoint(TRUNCATE)` → Close DB → release lock
- [x] checkpoint failure 记录日志并影响 graceful shutdown 结果；
      绝不尝试删除 WAL 文件
- [x] unclean shutdown 依赖 SQLite WAL recovery 保证正确性，不依赖
      checkpoint

## 4. Filesystem Portability & Safety

### Existing baseline

- [x] logical path 校验（filesafe）、root confinement、symlink 防御、
      canonical 解析
- [x] PreflightLocal：download 目标被未知本地文件 / 目录 / symlink
      占用时跳过，永不覆盖
- [x] Mirror 删除前校验父目录 symlink

### Goals

- [x] 本地映射 preflight 位于「完整 remote scan → selector → build plan →
      filesystem compatibility preflight → 任何本地 mutation」链上，
      preflight 不全通过则整轮失败、零本地变更
- [x] Windows 保留名与非法字符（CON / PRN / AUX / NUL / COM1..COM9 /
      LPT1..LPT9；`< > : " | ? *`；尾随点、尾随空格、控制字符）在
      mutation 前 fail-fast；这是 local mapping 限制，不污染
      `source.ValidateLogicalPath()` 的远端协议模型
- [x] case collision：检测 LocalRoot 实际大小写敏感性；case-insensitive
      文件系统上本轮计划内出现 `Foo.txt` / `foo.txt` 冲突时整体失败，
      不允许后下载覆盖先下载（平台相关结果）
- [x] path fuzz（FuzzValidateLogicalPath / FuzzResolveWithinRoot /
      FuzzLocalMapping）无 panic、无 root 逃逸、不把 traversal 归一成
      合法路径、不接受 NUL、无绝对路径注入
- [x] downloader 文件系统故障注入（permission denied / ENOSPC / rename
      failure / fsync failure / create temp failure）：经小型
      file-operation abstraction 注入，不依赖 chmod 000（Windows 与
      root CI 下不可靠）；失败必须证明——已有目标完好、临时文件清理、
      managed metadata 未错误推进 synced、Mirror delete 阶段未执行

## 5. Protocol Fault & Contract Testing

### Existing baseline

- [x] WebDAV / S3 / SFTP 共用 `source.Remote`，协议 dispatch 只在
      RemoteRegistry
- [x] S3 以真实 MinIO 为强制兼容参考实现（integration gate）
- [x] WebDAV / SFTP 有 in-process 真实协议栈测试

### Goals

- [x] 统一 Remote contract suite（`internal/source/remotetest`）：Stat
      root / Stat file / List / pagination / Open / empty file / large
      file / not found / invalid logical path / context cancellation /
      special filename / root confinement 在三个 adapter 上以同一套
      契约验证
- [x] transient network fault 场景：首次 503 重试成功、中途断连重试
      成功、401 不重试、timeout 安全失败
- [x] crash 场景：running 记录、pending managed metadata、遗留临时文件
      经 restart 后收敛（running → failed、temp 清理、下轮 run 收敛）
- [x] idempotency：同一 remote snapshot 连续 run 零变更（Copy 与
      Mirror 都覆盖）
- [x] large directory：≥ 10,000 remote files 的 scan / selector /
      planner / pagination / browser 场景
- [x] large transfer：32–64 MiB 生成流验证真实 streaming 而非整体读入
      内存

## 6. Operational Observability

### Existing baseline

- [x] 单一 zap logger（stderr INFO+ / 文件 DEBUG+，文本单行），
      Gin Logger 刻意禁用（不产生第二份 access log 事实来源）

### Goals

- [x] 全局 Gin middleware：RequestID + AccessLog + Recovery
- [x] Request ID 由服务器生成（`req_<128-bit random>`），响应头返回
      `X-Request-ID`；不无条件信任客户端提供的 request id
- [x] access log 至少包含：

```text
event=request request_id method path status duration_ms bytes
```

- [x] 不记录 Authorization、Cookie、request body、secret；默认不记录
      query string
- [x] sync run 事件（run 开始 / 结束）：

```text
event=sync_run job_id run_id source_id status duration_ms bytes
files_created files_updated files_deleted error
```

- [x] 文件级失败附加 `path`；成功的单文件不逐条 INFO（10k 文件不产生
      10k 日志）
- [x] 应用日志与 access log 保持一个 logger 体系，不引入 Gin Logger +
      zap access logger 两个事实来源
- [x] secret / token / password 不进入日志

## 7. Performance Baseline & v1.0 Quality Gate

### Goals

- [x] `make hardening`：reliability E2E、large directory、database
      restore、filesystem failure、short fuzz runs
- [x] `make benchmark`：BenchmarkPlan10K、BenchmarkManagedSearch100K、
      BenchmarkSQLiteManaged100K、BenchmarkSmallFileSync、
      BenchmarkLargeFileTransfer、BenchmarkRemotePagination10K，
      输出 ns/op、B/op、allocs/op，先建立 baseline
- [x] benchmark 只作为优化依据，不使用不稳定的 wall-clock CI threshold
- [x] 10k listing / 100k managed metadata 基准已记录；只有实测不可接受
      才追加独立优化 commit（如 repository 内查询），否则不提前优化
- [x] hardening CI：`.github/workflows/hardening.yml`（可复用、ref
      输入）。各质量 gate 在 prepare 之后并行执行，不是严格顺序链：
      Build 上 build 与 native smoke 在 test 通过后运行，notify 汇总
      test / integration / hardening / build / smoke 全部结果；
      Release 上 publish 需要 test / integration / hardening / smoke
      全部通过——hardening 失败无法发布

## Benchmark baseline（记录于 2026-09-18）

环境：darwin/arm64（Apple Silicon，本机），Go 1.26，`make benchmark`
输出。基线只作优化对比依据；100k managed metadata 与 10k listing
实测结果不构成优化压力，不提前引入 cache / index / FTS。

```text
BenchmarkPlan10K                 ~3.9 ms/op   ~12.3 MB/op   ~20k allocs/op
BenchmarkSmallFileSync           ~101 µs/op
BenchmarkLargeFileTransfer       ~12.9 ms/op（32 MiB，~2.6 GB/s）
BenchmarkSQLiteManaged100K       ~146 ms/op   ~167 MB/op    ~1.6M allocs/op
BenchmarkManagedSearch100K       ~162 ms/op   ~171 MB/op    ~1.5M allocs/op
BenchmarkRemotePagination10K     ~2.8 s/op    ~2.77 GB/op   ~46.5M allocs/op
```

## 明确排除（推迟到 v1.0 之后）

```text
断点续传
带宽限制
per-source concurrency
Prometheus / metrics endpoint
通知 / webhook
凭据加密
远端上传
双向同步
分布式执行
通用文件索引 / FTS
新的 Web UI 大功能
多云厂商 S3 compatibility matrix
```

## 与 v1.0 的边界

v0.9 是最后一个真正修改运行时语义的阶段：可靠性模型、恢复模型、跨平台
模型与质量门禁在此冻结；v1.0 以全链路验收、upgrade verification、
regression fixes、文档与发布为核心，不再大规模设计新机制。

## Upgrade impact

- 新增 `--transfer-timeout`（默认 0）与 `TINYSYNC_TRANSFER_TIMEOUT`：
  默认行为与 v0.8 兼容。
- 新增 datadir 单实例约束：第二个 `serve` 实例启动失败属于预期行为。
- `tinysync db` 子命令新增；无 schema migration。
- 无破坏性 API / Web UI 变更。

## 完成标准

恢复、安全、跨平台和长期运行场景均有可复现的验证结果，升级与备份恢复
路径已验证，关键领域回归测试与性能基准能够支撑 v1.0 验收。验收清单：

- [x] 同一 datadir 不允许两个 TinySync serve 实例同时运行
- [x] crash 后 stale running run 自动收敛
- [x] crash 遗留 transfer temp file 自动安全清理
- [x] transient remote error 可以重试
- [x] permanent error 不发生无意义重试
- [x] context cancellation / timeout 立即停止 retry
- [x] transfer timeout 可配置且默认保持兼容行为
- [x] SQLite quick_check / foreign_key_check 可执行
- [x] migration 前 corruption 不继续迁移
- [x] operator 可以生成一致性数据库备份
- [x] operator 可以离线恢复备份
- [x] restore 前自动生成当前数据库 safety backup
- [x] newer schema backup 拒绝恢复
- [x] older schema restore 后可以正常向前迁移
- [x] clean shutdown WAL checkpoint 正常
- [x] unclean shutdown WAL recovery 正常
- [x] path fuzz 无 panic / escape
- [x] symlink confinement 不回归
- [x] Windows invalid filename fail-fast
- [x] case-insensitive filesystem collision fail-fast
- [x] permission / ENOSPC / rename failure 不破坏旧文件
- [x] WebDAV / S3 / SFTP 通过统一 Remote contract（S3 契约随 MinIO
      integration gate 执行，本地 MinIO 实测通过；分页跟随用例以
      limit=2 避开参考服务器 MaxKeys=1 的续页丢失缺陷，见 s3 List 注释）
- [x] transient network fault E2E 通过
- [x] 10k directory scenario 通过
- [x] large streaming transfer 通过
- [x] repeated sync 收敛且幂等
- [x] 每个管理请求有 request_id
- [x] access log 有 method/status/duration/bytes
- [x] sync run log 有 job_id/run_id/source_id/status/duration/bytes
- [x] secret/token/password 不进入日志
- [x] benchmark baseline 已记录（100k managed metadata / 10k listing /
      large transfer）
- [x] `make check` / `make build` / `make integration` / `make hardening`
- [ ] 三平台 native smoke 与六平台 build 通过（随发布流程在远端 CI 执行）
- [ ] Release assets + checksums 核对；WebDAV 镜像独立验收
