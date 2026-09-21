# 运行取消与实时进度的领域模型

WebUI 需要三项运行期能力：手动停止运行中任务、任务总体进度（`25 / 75`）、单文件传输进度（`14.5 MB / 46.0 MB`）。底层取消链（runCtx 贯穿拨号 / 扫描 / 传输）已经存在，缺的是领域语义与对外接口。我们决定：**`canceled` 状态专指用户手动停止**（经 `Runner.Cancel(runID)` 以 `ErrRunCanceled` 为 cancel cause 触发）；服务 Shutdown、超时等非用户意愿的中断保持 `failed` 终态，与启动恢复把遗留 running 收敛为 failed 的既有语义一致。取消按 run ID 而非 Job 定位——迟到的「停止 Job」不能误伤下一轮运行。**历史统计（`RunStats`，SQLite）与运行进度（`RunProgress`，内存 transient）彻底分离**：进度分母是 BuildPlan+Preflight 后确定的计划工作项数（跳过 + 下载 + 更新 + 释放 + 删除），不用「远端扫描到的文件数」（include 过滤、Mirror 删除都会使两者偏离）；单文件字节进度在协议无关的 Downloader `io.Copy` 层以 atomic 计数统计（重试时归零当前 attempt），不产生任何高频 SQLite 写。`sync_run_items` 保持 append-only 审计：取消时在途文件记 `canceled` 明细，已收敛的保持 `succeeded`，未派发的不留明细。对外只扩展 `GET /runs/:id`（运行中附 `progress` 字段，终态省略）与 `POST /runs/:id/cancel`，前端以 500ms～1s 轮询消费。

## Considered Options

- 取消统一记 failed（仅加 HTTP handler 不加状态）：用户主动停止与真实失败在历史中不可区分，语义噪音（`failed: context canceled`）正是要修复的对象。
- Shutdown 也记 canceled：服务重启打断与用户停止混在一起，筛选统计不可分；非用户意愿的中断与 stale 恢复的 failed 语义冲突。
- 以 `RunStats.FilesTotal` 充当进度分母：实现最省，但分母在扫描期即固定，与实际计划工作量不是同一概念，进度可能虚高或超 100%。
- 进度写入 SQLite（`sync_run_items` 改 mutable、按 chunk UPDATE）：把干净的 append-only 历史变成高频可变状态，write amplification 严重。
- SSE / WebSocket 推送：为几个进度数字引入长连接生命周期管理，HomeLab 场景下轮询已足够；`RunProgress` 模型不绑定传输方式，未来可平移到 SSE。

## Consequences

- `sync_runs.status` / `sync_run_items.status` 的 CHECK 需 migration 0011 重建两张表（SQLite 不能修改 CHECK 约束）。
- 用户点停止到终态落库之间有短暂延迟（取消链收敛），API 返回 202 而非同步确认；期间重复取消幂等。
- 进度快照只在进程内存中存在：进程重启后运行中进度消失（stale running 已被启动恢复收敛为 failed，不存在「有 running 无进度」的稳定窗口）。
- 跳过与释放这类瞬时工作项会让进度条在传输开始前快速推进；扫描 / 计划阶段工作量未知，前端必须以 indeterminate 进度条表达，不得伪造百分比。
