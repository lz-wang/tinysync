# ADR 0007：SMB Source

日期：2026-10-03
状态：已接受

## 背景

TinySync 的第五种 Source 类型：家庭与小型办公环境大量 NAS（Samba /
Windows 共享）以 SMB/CIFS 提供文件访问。既有架构
`Source → RemoteFactory → Remote → Scanner → Planner → Downloader`
的协议边界已经稳定，SMB 不需要新的同步引擎或任务模型，只作为第五个
adapter 接入 `RemoteRegistry`——同步业务层（Runner / Scanner / Planner /
Downloader / Browser）不出现任何 SMB 分支。

## 决策

### 协议与认证

- **客户端库**：`github.com/cloudsoda/go-smb2`（纯 Go，rclone 的 SMB
  backend 同源），锁定具体 commit、不追踪 main。满足
  `CGO_ENABLED=0` 单二进制、跨平台、不依赖系统 `mount.cifs` 的既有
  硬约束。
- **只支持 SMB2/SMB3 + NTLMv2 用户名密码**。不做 SMB1、Kerberos、
  DFS、guest。不支持 guest 是显式决策：匿名访问将来需要显式的
  `auth_method = guest`，绝不根据 username / password 空值隐式推断。
- **signing 默认 required**（`SMBSigningPolicy = required | auto`，枚举
  而非 bool——bool 零值无法区分「未提供」与「明确关闭」）。不提供
  disabled：auto 已足够兼容家庭 LAN 旧 NAS，降低客户端要求必须是
  用户的主动选择。
- **第一版不做 SMB encryption 配置**：dialect / signing / encryption /
  server capability 之间存在组合关系，先保持 Source 配置简单；
  SMB3 encryption required 作为独立的安全议题另行设计。

### 路径模型

`host + share + remote_root` 三段定位远端 namespace：host 是裸主机名 /
IP（拒绝 `smb://` 前缀、`\\server\share` UNC 形态与附带路径），share
是单段名称（允许 Unicode 与 `$` 隐藏 share），remote_root 是 share 内
的 **POSIX 风格绝对路径**。tinysync 内部（selector、scanner、browser、
planner、API）只出现 `/photos/2026/a.jpg` 形式的 logical path，SMB
反斜杠 native path 只在 adapter 边界内转换；中间拼接一律用 `path`
包而非 `filepath`（Windows build 下 `filepath.Join` 会改变路径语义，
与 SFTP adapter 同一约定）。share 内 native 路径是相对路径（go-smb2
拒绝前导 `\`），share root 表示为空串。

### 安全 fail-closed

- **任何 reparse point（symlink / junction / mount point）一律拒绝**：
  不跟随、不静默跳过。跟随破坏 root confinement；静默跳过会让
  Planner 认为相关 managed files 已从远端删除，触发本地大规模删除。
  判定覆盖两个信号（真实 Samba 实测）：QUERY_DIRECTORY 响应的
  `ReparsePointTag`（ReadDir 路径）与 CREATE 响应
  `FileAttributes` 中的 `FILE_ATTRIBUTE_REPARSE_POINT`（Lstat 路径，
  Samba 不携带 tag）。
- **部分扫描等于失败**：ScanTree 中任何一层目录枚举失败（含
  ACCESS_DENIED）都整轮失败，绝不返回看起来成功的 incomplete
  snapshot——Mirror 的删除授权依赖完整快照。
- **远端身份保护**：`host + port + share + remote_root + username +
  domain` 是 remote identity，被 Job 引用后不可变更（409）；signing
  只影响协商强度、password 轮换只影响访问，均允许。

### 可靠性

- **ctx 取消真正中断网络 I/O**：所有操作经 `share.WithContext(ctx)`，
  go-smb2 提供请求级取消——阻塞中的 Read（含已打开文件的传输）随
  ctx 取消立即返回 ctx 错误。这与 SFTP（协议无请求级取消，只能靠
  拆连接）不同：单次 attempt 超时不中断同连接上的其它在途传输。
- **transport 故障后重连**：session deleted / transport 错误 → 拆除
  所属连接代际（与操作时代际绑定，stale 回调不伤新代际）→ 下一次
  操作惰性重连。Downloader 的重试因此建立新 SMB 连接，而不是反复
  使用坏连接。
- **Close 幂等且有界**：优雅拆除（Umount + Logoff）走带超时
  （5s）的派生 ctx——对端停摆时优雅拆除的响应永远等不到，Close
  位于 Runner 的 defer 路径，绝不能无限阻塞（真实 Samba 集成测试
  发现的死锁形态）。

### 凭据与持久化

- **不接凭据库（v1）**：SMB password 走 Source 自身的
  `credentials_json`，生命周期与 WebDAV password 完全一致（明文只
  在持久化与远端客户端构造路径，普通 API 永不回显）。凭据库目前
  只服务 SFTP SSH key；把它扩展成通用 username/password 凭据是独立
  的架构议题，不与协议功能捆绑。
- **无 SQLite migration**：`sources.type` 即判别符，config /
  credentials 走既有扁平 JSON；credential state SQL 表达式增加
  `smb.password_set`。

### 测试与集成

- 单元测试经最小 `conn` 接口注入内存 fake：路径映射、reparse 拒绝、
  错误分类矩阵、重连 / teardown / Close 幂等 / 并发扰动（-race）。
- **真实 Samba 集成测试**（CI 独立 job，`servercontainers/samba`
  容器）：真实 TCP + SMB2/3 negotiate + NTLMv2 + 真实文件系统，不
  mock SMB RPC。覆盖认证失败、浏览、Copy/Mirror 协议矩阵、reparse
  fail-closed（Mirror 不删除）、TCP 停摆后的 ctx 取消、连接断开
  后的重连。协议矩阵（`TestSyncAcrossProtocols`）新增 smb fixture
  （env-gated）。

## 已知限制与降级约束

- 创建 SMB Source 后**不支持无损降级**到不认识 SMB 的旧版本
  （`decodeConfig` 对 `type=smb` 返回 ErrUnsupportedType，Source
  列表读取失败）。降级前应先删除 SMB Source——不为降级做静默忽略，
  与项目 fail-closed 风格一致；该约束写入 Release note。
- Samba 对 POSIX symlink 默认服务器侧跟随（呈现为目标属性），
  SMB 客户端无从判定；fail-closed 契约覆盖的是 SMB reparse point
  （Windows 语义的 symlink / junction，含经 SMB 协议创建的 native
  symlink）。
- 部署要求 TCP 445 出站可达（`port` 可配置）。

## 后果

- 同步业务层零 SMB 特判；SMB 差异只存在于 Source domain 的 typed
  config/credentials、持久化/API 编解码与 `internal/source/smb`。
- 下一阶段可独立评估：通用 username/password 凭据模型（SMB /
  WebDAV / 未来 FTP / HTTP Basic 共用）与 SMB3 encryption required
  安全策略。
