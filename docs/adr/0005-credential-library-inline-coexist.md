# 凭据库与内联凭据并存、SQLite 明文存储

多个 SFTP 源共用同一把 SSH 私钥时，现状要求每个源各贴一份 PEM：换钥要逐源重贴，源之间没有单一事实来源。我们决定引入**协议无关的凭据（Credential）实体**——一条命名的 secret 记录，独立于任何单个源，可被多个源同时引用；本次只实现 `ssh_key` 一种类型（私钥与其解密口令是不可分割的整体），表结构与 API 按协议无关设计，后续协议凭据可平滑挂入。**引用与内联并存且互斥**：`SFTPConfig` 新增 `credential_id`（非敏感，随 config JSON 整体替换语义更新），不变量为 `credential_id` 非空 ⟺ `auth_method=private_key`，且引用态下源自身不得再持有内联 private_key/passphrase——两条路谁也不静默胜出；存量内联私钥继续工作，不强制迁移，编辑态提供「提升为凭据」一键动作（后端把已存私钥+口令存为命名凭据并改写引用，私钥不必重新经手前端）。**凭据明文存 SQLite**，与现有全部协议 secret 同一信任模型：只给私钥上加密是半吊子安全（能读 DB 就能读其他 secret），真要加密应整体方案解决。保存凭据时**强制解析 PEM**（带口令时验证口令）并派生公钥 SHA256 指纹存储展示，坏钥在入口失败；公钥指纹只用于人前区分钥匙，与主机密钥指纹（校验连接目标）无关。**被引用的凭据阻止删除**（fail-closed，返回引用源清单，先解绑再删）；secret 更新走**整体替换**（提交完整新组，无三态），名称跟随 Source 规则（全局唯一、大小写不敏感、冲突 409）。REST 面为 `/api/v1/credentials` CRUD（`ScopeAdmin`，与源写路径一致；列表回显名称/指纹/带口令/引用计数，secret 永不回显），换绑走现有 PATCH sources 的 config 替换。WebUI 增加独立凭据管理页；SourceDialog 在 `auth_method=private_key` 时二选一（选凭据 / 粘贴一次性私钥）。MCP 不动：工具继续以 source_id 为入口，`GetCredentials` 解析时自动跟随引用。

## Considered Options

- 主口令派生密钥加密 / OS keyring：解锁 UX、口令找回、跨平台部署（CLI / 容器无 keyring）成本高，且与其他协议 secret 的明文现状不一致，造成「假安全」。
- 强制迁移（private_key 源必须引用凭据库）：破坏性变更，存量源要数据迁移、三态语义要重设计，收益只是模型纯净。
- credential_id 优先、内联 key 静默忽略：同名同钥两份副本漂移，出问题难排查。
- `auth_method` 扩第三个枚举值 `credential`：credential_id 本身已携带该语义，三态枚举冗余且稀释存量字段含义。
- secret 三态更新（沿用 sources）：「只改口令不换钥」场景极稀有，为它引入省略/清除/替换状态机不值。
- 凭据管理只藏在 SourceDialog 内嵌对话框：离开源视角无法重命名、清理引用。
- MCP 同步开凭据管理工具：v0.8 已封版的契约面被不必要地重开。

## Consequences

- migration 0012 新增 credentials 表（协议无关 secret JSON + 类型判别 + 指纹列）；sources 表结构不动（credential_id 在 config JSON 内）。
- `GetCredentials`（唯一 secret 读取路径）在 SFTP + 引用态下改为从凭据表合成 `SFTPCredentials`，远端客户端构造与同步引擎零改动。
- 引用态下源的 `CredentialState` 回显跟随凭据（`private_key_set` 反映凭据存在），UI 状态芯片沿用。
- 凭据换钥后所有引用源在下一次构造远端客户端时自动生效——没有逐源同步动作，这是共享引用的直接收益。
- 删除源自动解绑（引用计数减一），凭据不受影响；删除凭据需先无引用。
- API 面扩大一个资源；run-only token 与 MCP 侧无凭据管理能力，凭据管理只在 WebUI/REST 管理面。
- 静态加密已重新审视并**延期**：认证域 secret 本就不可逆（口令 PHC、session/token 只落 SHA-256 hash）；可回放 secret 的加密作为独立后续版本立项，方向为主密钥（env 或 dataDir key 文件）+ AES-256-GCM、全协议统一迁移（含存量备份的明文历史处置），届时本 ADR 的存储段落随之改写。本次不实施半吊子的部分加密。
