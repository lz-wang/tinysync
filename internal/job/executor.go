package job

import (
	"context"
	"encoding/json"
)

// Executor 是领域任务执行器：Validate 校验并归一领域配置（创建与更新
// 共用），Run 执行一轮同步（由统一 Runner 调度，公共层不理解领域内部）。
// 应用启动时静态注册：map[JobKind]Executor；未注册的类型不可创建。
type Executor interface {
	// Validate 校验领域配置并返回归一后的落库形态（如 File 的
	// canonical LocalRoot 与归一 RemoteRoot）；返回 nil 表示保持原样。
	// jobID 在更新时为目标 Job ID、创建时为空，供需要跨 Job 检查的
	// 领域规则（如 File 的 LocalRoot 归属保护）跳过自身。
	Validate(ctx context.Context, jobID string, cfg json.RawMessage) (json.RawMessage, error)
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
