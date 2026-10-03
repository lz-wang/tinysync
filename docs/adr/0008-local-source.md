# ADR 0008：本地文件 Source

日期：2026-10-03
状态：已接受

## 背景

HomeLab 除网络存储外，也需要把运行主机上的目录单向复制到另一个目录。
既有 `Source → RemoteFactory → Remote → Scanner → Planner → Downloader`
已经提供协议隔离边界。增加本机来源无需另建同步引擎，也不应绕过原子下载、
managed metadata、重试、进度、取消、历史与通知链路。

## 决策

### 一等 Source adapter

增加第六种类型 `local`，非敏感配置只有 `LocalConfig.Root`。
`internal/source/local` 实现 `Remote`、`TreeScanner` 和 `DirectoryCreator`，
经生产 `RemoteRegistry` 装配。没有连接状态，`Close` 是幂等空操作。
同步扫描、规划、下载与执行核心保持协议无关，不增加 `TypeLocal` 分支。
内部 `Remote` 等历史名称保留；用户界面使用「源端文件」「源端路径」。

仍然只做 Source → Job 本地目标的单向同步，不提供双向冲突解决。
不同 Job 可构成分级复制流水线，但第一版不检测全局依赖图或跨 Job 循环。

### 两种路径与稳定身份

- `LocalConfig.Root`、`Job.LocalRoot` 是 native OS 文件系统路径。
- `Job.RemoteRoot`、`source.FileInfo.Path` 是 Source-relative、以 `/` 分隔
  的绝对逻辑路径，`/` 表示 Source 根；原生分隔符只在 adapter 边界转换。
- `PrepareConfig` 先检查 config 严格单选，再归一化字段；Local root 经
  trim、Abs、Clean、EvalSymlinks、Stat（必须为已存在目录）转换为 canonical
  absolute path，持久化最终路径，不保存输入别名。
- API 身份比较和 Create/Update 共用准备后的配置。Local identity 就是
  canonical root；等价路径更新不误报冲突，被 Job 引用后更换 root 返回 409。
  更换来源需创建新 Source 后切换 Job.SourceID，沿用 managed metadata 原子重置。

### 普通文件树与完整快照

只接受普通文件与目录。每次访问逐组件 `Lstat`，检查包括持久化 root 的父组件；
任何 symlink（含指向根内的链接）、FIFO、socket、设备或其他特殊文件均拒绝。
根目录输入别名可在配置准备时解析；已保存的 canonical 路径后来被 symlink
替换则拒绝，不重新跟随它到别处。

- `List` 完整枚举单层目录，再使用 `source.PageSlice` 分页。
- `ScanTree` 使用显式 stack DFS，每层读取一次，不退回递归分页 List。
  根自身不 visit；目录和普通文件都 visit。任意目录读取失败、危险条目、
  context 取消或 visitor 错误均使整次扫描失败，绝不把不完整结果授权给 Mirror。
- `Open` 仅打开普通文件，打开后重验句柄类型及文件身份；每次读取前检查 context，
  让本地复制复用 Downloader 的取消与单次传输超时。
- `Mkdir` 验证逻辑路径和无 symlink 的已有父目录后，用 `os.Mkdir(..., 0755)`
  只创建一级目录，不用 `MkdirAll` 解析未加固输入。

第一版不提供文件系统 snapshot。逐组件检查与后续 I/O 不是一次原子文件系统事务，
不承诺在外部进程并发替换路径组件时获得一致时间点视图；配置之后已存在的危险
路径变化会在运行前和 adapter 访问时重验并拒绝。

### 单 Job 自重叠保护

比较实际源子树 `Resolve(LocalConfig.Root, Job.RemoteRoot)` 与 `Job.LocalRoot`，
相同目录、源包含目标、目标包含源全部拒绝。不能只比较 Source 根：例如根为
`/data`、源端路径 `/photos`、目标 `/data/videos` 时，两棵 sibling 子树允许同步。

`source.ValidateJobMapping` 集中维护协议差异；Job Create 在路径准备后校验，
Update 按合并后的 SourceID / RemoteRoot / LocalRoot 校验一次，任何字段更新都
无法跳过。Runner 每轮在读取 Source 后、OpenRemote 前再校验；失败记 failed run，
不进入扫描，不改 managed metadata，也不执行 Mirror 删除。

`filesafe.CanonicalExistingDir`、`PathsOverlap` 与 `ResolveNoSymlink` 为共享原语，
路径重叠规则兼容文件系统根路径及 Windows 大小写语义。

### 无凭据、无迁移、无新依赖

Local 无 `LocalCredentials` 或 `LocalCredentialState`。创建请求省略
`credentials`；显式发送（包括空对象或 null）一律 400。更新同样拒绝。
SQLite 使用现有 type 判别符与 config JSON，credentials JSON 和回显状态均为 `{}`。
不新增 schema migration，不增加第三方依赖，全工程维持无 CGO。

Web UI 的源根目录选择复用 Job 使用的主机目录浏览/建目录能力。
Source CRUD、目录访问测试、文件浏览/下载/建目录与 MCP `list_sources`
通过既有服务入口工作，不新增协议专属 API。

### 性能与宿主机边界

指纹使用 size + mtime，不在扫描时计算 checksum。同大小且 mtime 相同的内容变化
可能无法识别；checksum 模式需要独立设计。传输继续走 `os.File → Downloader →
临时文件 → atomic rename`，并发由 `--max-concurrent-transfers` 控制。
不增加 hardlink、reflink、sendfile、copy_file_range、clonefile 或 snapshot 优化。

Local 使用宿主机文件系统 API，不识别 ext4/APFS/NTFS 等文件系统类型，
不自动连接 SMB/NFS/SFTP、不管理挂载生命周期。用户自行挂载的目录仍由操作系统
提供访问，TinySync 不额外提供对应协议级保证。

## 验证与后果

- Local 接入公共 `remotetest.RunSuite`、`TestSyncAcrossProtocols/local` 和
  `TestRemoteBrowserAcrossProtocols/local`；公共同步 scenario 无 Local 分支。
- 额外覆盖实际子树重叠矩阵、三种 mapping 字段更新、运行前目录变化、局部扫描
  失败、symlink、FIFO/socket/device、权限失败、建目录约束和 32 MiB 复制取消。
  Unix 特殊节点与权限测试在 Unix 平台运行，不能替代 Windows 运行验收。
- 提供 1000 个文件的扫描基准并接入 `make benchmark`，仅用于性能观察，不作时间门禁。
- 数据库保存 `type=local` 后，不应降级到不认识该类型的旧 TinySync；旧版的严格
  decoder 会拒绝读取该 Source。需要降级时先备份并删除 Local Source 及其引用 Job，
  或恢复添加该类型之前的数据库备份。
