# GitHub Release Source 设计

状态：已冻结（v0.11 实施基准）。本文冻结 GitHub Release Source 的可观察行为：版本选择、逻辑路径、指纹与删除语义。路径与指纹契约一旦有 managed 文件落地即不可无迁移变更，修改本文档的编码或排序规则必须同步提供数据迁移。

## 1. 目标与边界

将一个 GitHub 仓库的 Releases 转换为统一的只读远端目录树，交给现有同步引擎（Selector / BuildPlan / Downloader / Runner）执行扫描、过滤、下载、校验、历史与进度。新增协议类型 `github_release`，独立实现于 `internal/source/githubrelease`，经 `RemoteRegistry` 注册，业务层不出现协议分支。

第一阶段明确不做（与实施边界一致，后续作为独立扩展）：

- GitHub 自动生成的 Source ZIP/TAR、Actions Artifact、Packages、无 Release 的裸 Git Tag；
- GitHub Enterprise Server / 自定义 API Host（第一版固定 `https://api.github.com`）；
- HTTP Range 断点续传（重试整文件重下）；
- Webhook 自动触发、发布稳定等待窗口、版本保留策略。

## 2. 版本选择（release policy）

策略保存在 Source 配置 `release_policy`，同一仓库需要不同策略时创建多个 Source。

| 策略 | 取值 | 行为 |
| --- | --- | --- |
| 最新稳定版（默认） | `latest` | `GET /repos/{owner}/{repo}/releases/latest`，恰一个版本。GitHub 按自身规则选择非 prerelease、非 draft 的 Release，不假定其发布时间最近或 tag 版本号最大 |
| 指定 Tag | `tag` | `GET /repos/{owner}/{repo}/releases/tags/{tag}`，精确匹配。404 属确定性失败（permanent） |
| 最近 N 个版本 | `recent` | 完整分页枚举 `GET /repos/{owner}/{repo}/releases` 后按 `published_at` 降序排序取前 N；不依赖 API 排序契约，不截断首页 |
| 全部版本 | `all` | 完整分页枚举同上，取全部入选版本 |

规则（全部策略共用）：

- 草稿（draft）一律不参与同步——草稿对无写权限的匿名请求不可见，且随时可能消失；已认证请求也主动排除。
- 预发布（prerelease）默认排除；`include_prereleases: true` 时纳入。`latest` 策略不受该开关影响（GitHub 的 latest 端点本身不返回 prerelease）。
- 无 `published_at` 的 Release（理论上是 draft 的属性，防御性处理）按不可选处理。
- 扫描规模上限 `maxReleases = 1000`：完整枚举结果超过上限时扫描整体失败，绝不允许截断后进入 Mirror 删除授权。
- `recent` 的 N（`recent_count`）必须为正整数（1–1000）。

## 3. 逻辑路径契约

### 3.1 目录结构

```
/                                    ← Source root（Job remote_root 固定推荐 "/"）
/v1.2.0__123456789/                  ← 版本目录 = encode(tag) + "__" + release_id
/v1.2.0__123456789/app-linux-amd64.tar.gz
/v1.2.0__123456789/SHA256SUMS
/v1.1.0__987654321/...
```

- 始终保留版本目录：不同版本的同名 Asset 不合并，Mirror 的版本轮换语义依赖目录边界。
- 版本目录名 = `encode(tag) + "__" + <release_id（GitHub 数字 ID 十进制）>`。Release ID 是 GitHub 不可变主键，兜底一切冲突（tag 大小写冲突、编码碰撞、tag 改名重建）。
- Asset 文件名使用 GitHub 返回的 `name` 原样（GitHub 保证 asset name 非空且唯一于同一 Release）；落地前仍必须通过 `source.ValidateLogicalPath`，不合法即扫描整体失败。

### 3.2 Tag 编码（percent-encoding，已冻结）

- 保留字符集：`A-Za-z0-9`、`-`、`.`、`_`；其余每一字节（UTF-8 按字节）编码为 `%XX` 大写十六进制。`%` 自身编码为 `%25`，编码无歧义且可逆。
- `.` 开头的目录名风险（`.`、`..`、隐藏目录）由两道既有防线覆盖：tag 首字符为 `.` 时编码后仍以 `.` 开头，因此额外规则——**保留字符集内的 `.` 仅允许出现在非首位置，tag 首字符若为 `.` 一并 percent-encode**（`.` → `%2E`）；`..` 目录分量与 dot segments 由 `ValidateLogicalPath` 统一拒绝。
- 解码可逆：目录名剥掉最后一个 `__<digits>` 后缀后 percent-decode 即还原 tag。`__<digits>` 后缀切分取**最后一个** `__` 且其后全为数字，tag 本身含 `__<digits>` 结尾时由 Release ID 的存在性区分（还原仅用于展示，不用于寻址，寻址始终携带完整目录名）。
- WebUI 展示用解码后的 tag（`v1.2.0`），不展示内部 ID。

### 3.3 大小写不敏感文件系统

`V1.0__111/` 与 `v1.0__222/` 在 macOS/Windows 上不冲突（ID 不同）；同一目录内 asset 同名不同大小写同理——GitHub 同一 Release 内 asset name 唯一（不区分大小写地唯一），扫描阶段对同一版本目录内的 asset name 做大小写折叠冲突检测，冲突即整体失败，不落地歧义路径。

## 4. 指纹与增量同步

GitHub Asset 字段 → TinySync `Fingerprint` 的映射（冻结）：

| GitHub 字段 | Fingerprint 字段 | 说明 |
| --- | --- | --- |
| `size` | `Size` | 严格校验下载字节数 |
| `updated_at` | `ModifiedAt` | RFC3339 解析为 UTC |
| `digest`（`sha256:<hex>`） | `Checksum` | 原样存储含 `sha256:` 前缀；缺失时为空串 |
| `id` + `updated_at` + `digest` 组合 | `Version` | 格式 `<assetID>:<updated_at RFC3339>:<digest 或空>` |

- `Version` 包含内容身份信息：同一 Release 下重新上传同名 Asset（新 asset ID / 新 updated_at）即产生新 Version，`fingerprintChanged` 的 Version 优先级直接命中，无需改造增量模型。
- 下载始终使用扫描快照确定的 asset ID（`GET /releases/assets/{asset_id}` + `Accept: application/octet-stream`），不按 tag+文件名重新解析。扫描后 Asset 被删除 → 下载 404 permanent 失败，已有本地文件与管理状态保持不变。
- 空串 `Version` 不会出现（asset ID 恒非零）；`fingerprintChanged` 对「一方 Version 为空」判已变，兼容旧协议混合场景不受影响（GitHub Source 的 managed 记录恒有 Version）。

## 5. SHA-256 校验

`verify_sha256` 配置两种取值，默认 `if_available`：

- `if_available`：GitHub 提供 `digest` 时，Downloader 流式计算 SHA-256 并强制比较；未提供时仅沿用既有字节数校验。
- `required`：扫描阶段即检查所有入选 Asset 均带有效 `sha256:<64 hex>` digest，任一缺失/格式非法 → 扫描整体失败（在下载开始前拦截）。

校验在 Downloader 的临时文件拷贝路径内流式完成（`io.Copy` 循环内喂 hash），校验失败属确定性失败（不重试、不原子替换、不触碰已有目标文件），下一轮 run 经 pending 语义重新传输收敛。永久失败信息中包含期望与实际摘要。

## 6. 删除语义（Copy / Mirror 与版本轮换）

- Mirror：新版本进入选择范围使最旧版本退出范围时，退出版本的 managed 文件成为删除授权候选（既有 `BuildPlan` 的 remote-gone 语义，目录级整体消失）；selector/include 排除的文件永远走 Relinquish 不删除。
- Copy：旧版本退出范围后本地文件保留（Relinquish 释放管理授权，不删文件）。
- WebUI 必须在 GitHub Release Source 的 Job 选择 Mirror 时明示版本清理行为（commit 08 落地）。
- 完整快照契约不变：Release 枚举分页失败、速率限制（403/429）、权限异常、超限、条目冲突，任何一处失败都使整轮扫描失败，`ScanRemote` 不返回部分结果，Mirror 不产生任何删除。

## 7. HTTP 客户端与速率限制

- 仅标准库 `net/http`，固定 API Host `https://api.github.com`；Token（匿名时无）只发送到该 Host。
- 下载走 asset API，GitHub 返回 200 数据流或 302 → CDN。重定向策略：仅允许 https；跨 host 重定向剥离 `Authorization` 头；重定向目标 host 必须以 `github.com` 或 `githubusercontent.com` 结尾（后缀按 DNS label 边界匹配），否则拒绝（permanent）。
- 错误分类对齐 adapter boundary 约定：408/429/5xx、网络传输错误 → transient；401/403/404/422 等其余 4xx → permanent。403/429 读取 `Retry-After` / `X-RateLimit-Reset`，限流期间不做无界重试（重试仍由上层 Downloader 的有限 attempts 控制，adapter 不自旋等待）。
- ETag 条件请求缓存（per Remote 实例内存缓存）：Release 列表与 per-Release Asset 元数据按 ETag / `If-None-Match` 缓存，304 命中复用。缓存不完整（任一页失败）不产出快照——fail-closed，绝不以空目录结果触发 Mirror 删除。
- 元数据请求（Release/Asset 枚举）在该 Remote 内串行执行；Asset 下载体走现有全局传输并发控制。

## 8. 凭据与身份保护

- Token 支持匿名（公开仓库）与 Fine-grained PAT（私有仓库需 Contents: read）。以 `Authorization: Bearer <token>` 发送。
- 存储：`credentials_json` 复用现有扁平 JSON（新增 `token` 键），API 不回传明文，编辑表单沿用 SecretField 三态（不填保留 / Clear 清除 / 输入替换）。
- 被 Job 引用时的远端身份字段（修改返回 409，复用 `RemoteIdentityEqual`）：`repository`、`release_policy`、`tag`、`recent_count`、`include_prereleases`。非身份字段：`name`、`enabled`、`verify_sha256`、Token 轮换。改变版本选择策略应新建 Source 并经 Job 映射切换，同时重置对应 managed metadata（现有流程）。

## 9. REST / MCP 表面

- 现有 Source CRUD、`POST /sources/:id/test`、Job / Run / 文件浏览接口全部兼容，`github_release` 作为第四类型自然接入。
- 新增 `POST /api/v1/sources/inspect`（admin scope，不持久化）：请求携带完整表单配置 + 临时 Token，或 `source_id`（服务端读取已存凭据）；响应为连接状态、仓库名称、可见 Release 概览（版本、发布时间、prerelease 标记、Asset 数量与总大小、digest 可用性）。预览数据供「测试并预览」，不下载制品。
- MCP `list_sources` DTO 按 `github_release` 分发同一份非敏感配置，不泄露 Token。

## 10. 验收清单

1. 公开仓库匿名、私有仓库 Token 访问符合预期；
2. latest / tag / recent / all 四种模式行为正确；
3. >100 条 Release 或 Asset 完整分页（Link header 逐页）；
4. tag 特殊字符、大小写冲突安全处理；
5. 同名 Asset 重新上传后经 Version 变化识别并重新下载；
6. SHA-256 不匹配时不覆盖原文件；
7. 302 重定向不泄露 Token，非法目标被拒绝；
8. 分页失败 / 403 / 429 时不触发 Mirror 删除；
9. Copy 保留旧版本，Mirror 只删除授权 managed 文件；
10. 运行取消可中断 GitHub API 请求与下载；
11. WebUI 创建、编辑、测试、预览、glob 过滤通过；
12. 现有三协议与全部现有 API 回归通过。
