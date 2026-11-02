package job

import (
	"context"
	"encoding/json"
	"time"
)

// Executor 是领域任务执行器：Validate 校验并归一领域配置（创建与更新
// 共用），Run 执行一轮同步（由统一 Runner 调度，公共层不理解领域内部
// ——文件路径、OCI Manifest 等全部封装在实现内）。
// 应用启动时静态注册：map[JobKind]Executor；未注册的类型不可创建。
type Executor interface {
	// Validate 校验领域配置并返回归一后的落库形态（如 File 的
	// canonical LocalRoot 与归一 RemoteRoot）；返回 nil 表示保持原样。
	// jobID 在更新时为目标 Job ID、创建时为空，供需要跨 Job 检查的
	// 领域规则（如 File 的 LocalRoot 归属保护）跳过自身。
	Validate(ctx context.Context, jobID string, cfg json.RawMessage) (json.RawMessage, error)
	// Run 执行一轮同步：从建立远端连接到产出结果摘要。ctx 是本轮运行
	// 的取消根（用户停止 / Shutdown 会取消它）；返回的错误链包含
	// context.Canceled 即视为取消。结果摘要 marshal 为 result_json。
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// RunRequest 是统一 Runner 交给领域执行器的运行输入。
type RunRequest struct {
	// Job 是本轮运行的任务配置快照（Config 为领域 JSON）。
	Job Job
	// RunID 是本轮运行的持久化 ID，明细写入时携带。
	RunID string
	// Progress 接收运行期进度事件（阶段边界、工作量、在途单元）。
	Progress ProgressReporter
	// Items 接收运行明细；nil 表示不记录（只累计摘要）。
	Items ItemRecorder
	// Transfers 是本轮使用的进程级传输 limiter（Runner 单例的快照），
	// 领域内的传输并发（文件下载等）经它约束整个进程。
	Transfers *TransferLimiter
	// TransferTimeout 是单次 attempt 的传输超时；0 表示不启用。
	TransferTimeout time.Duration
}

// RunResult 是领域执行器返回的结果摘要（落库 result_json）。
type RunResult struct {
	Result json.RawMessage
}

// ItemRecorder 接收运行明细。返回错误视为本轮失败：历史明细缺失与
// 领域 metadata 缺失同等对待，不做静默降级。
type ItemRecorder interface {
	RecordItem(ctx context.Context, item RunItem) error
}

// ChangeAwareExecutor 由需要在配置变更 / 删除时维护领域状态的执行器
// 实现（如 File 的 managed metadata 重置与孤儿 partial 回收）。
type ChangeAwareExecutor interface {
	Executor
	// UpdateHooks 返回随 Job 更新同事务执行的领域钩子：before / after
	// 为变更前后快照，执行器自行判定领域映射是否变化（如 File 的
	// source / remote_root / local_root）。
	UpdateHooks(ctx context.Context, before, after Job) ([]TxHook, error)
	// AfterUpdate 在更新成功提交后调用：只做尽力而为的善后（失败只
	// 记日志），不得把已成功的变更报告为失败。
	AfterUpdate(ctx context.Context, before, after Job)
	// AfterDelete 在删除成功提交后调用，语义同 AfterUpdate。
	AfterDelete(ctx context.Context, job Job)
}
