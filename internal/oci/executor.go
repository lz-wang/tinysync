package oci

import (
	"context"
	"encoding/json"
	"fmt"

	"tinysync/internal/job"
	"tinysync/internal/oci/image"
	"tinysync/internal/oci/registry"
	"tinysync/internal/oci/task"
)

// OCI 执行器产出的 RunItem action 取值（公共层只把它们当作领域定义的
// 字符串）。
const (
	ItemActionCopy RunItemAction = "copy"
	ItemActionSkip RunItemAction = "skip"
)

// RunItemAction 是 oci 领域的运行明细动作（job.RunItemAction 的别名，
// 公共层不做统一枚举约束）。
type RunItemAction = job.RunItemAction

// RegistryProvider 提供引擎的 Registry 配置快照（由 Service 实现，
// 从 registries 表构建）。
type RegistryProvider interface {
	Resolver(ctx context.Context) (registry.Resolver, error)
}

// OCIExecutor 是 oci 任务的领域执行器（job.Executor）：Validate 校验并
// 归一 config_json（src_images 展开 + dst_servers 存在性 + platforms
// 归一化），Run 扇出执行镜像复制（每 run 一个 CopySession、固定 3 并发
// worker 池、per (image,dst) CopyImage）并把结果映射为统一 RunItem 与
// result_json。
type OCIExecutor struct {
	registries RegistryProvider
	tasks      *task.TaskExecutor
}

// NewOCIExecutor 构造执行器；logger 为引擎日志接口（生产装配注入
// tinysync logging 适配），nil 时用静默实现（测试）。
func NewOCIExecutor(registries RegistryProvider, logger registry.Logger) *OCIExecutor {
	if logger == nil {
		logger = nopLogger{}
	}
	return &OCIExecutor{
		registries: registries,
		tasks:      task.NewTaskExecutor(logger),
	}
}

// Validate 实现 job.Executor（创建与更新共用）：parseSrcImages 展开 +
// name.NewTag 校验 + dst_servers 必须存在于 registries（primary）+
// platforms 归一化；返回归一化 config 落库（src_images 为展开后的
// repo:tag 列表、platforms 补全 os / 去空白）。
func (e *OCIExecutor) Validate(ctx context.Context, jobID string, cfg json.RawMessage) (json.RawMessage, error) {
	taskCfg, err := ParseTaskConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err := ValidateSrcImages(taskCfg.SrcImages); err != nil {
		return nil, err
	}
	expanded, err := ExpandSrcImages(taskCfg.SrcImages)
	if err != nil {
		return nil, err
	}
	if len(expanded) == 0 {
		return nil, fmt.Errorf("%w: src_images is required", job.ErrInvalid)
	}
	if len(taskCfg.DstServers) == 0 {
		return nil, fmt.Errorf("%w: dst_servers is required", job.ErrInvalid)
	}
	platforms, err := NormalizePlatforms(taskCfg.Platforms)
	if err != nil {
		return nil, err
	}

	// dst_servers 必须是已配置的 primary Registry（mirror 行不是同步目标）。
	res, err := e.registries.Resolver(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(taskCfg.DstServers))
	for i, dst := range taskCfg.DstServers {
		if len(dst) > MaxDstServerLength {
			return nil, fmt.Errorf("%w: dst_servers[%d] exceeds maximum length of %d characters", job.ErrInvalid, i, MaxDstServerLength)
		}
		if seen[dst] {
			return nil, fmt.Errorf("%w: dst_servers[%d] duplicates %q", job.ErrInvalid, i, dst)
		}
		seen[dst] = true
		if res.RegistryByName(dst) == nil {
			return nil, fmt.Errorf("%w: dst_servers[%d] references unknown primary registry %q", job.ErrInvalid, i, dst)
		}
	}

	normalized := task.TaskConfig{
		SrcImages:  expanded,
		DstServers: taskCfg.DstServers,
		Platforms:  platforms,
	}
	return MarshalTaskConfig(normalized)
}

// itemDetail 是 RunItem.Detail 的形态（R16 同构：领域扩展字段）。
// platform 级结果经 platform_results 保留可分（含 platform_skip）。
type itemDetail struct {
	Src              string                     `json:"src"`
	Registry         string                     `json:"registry,omitempty"`
	Status           string                     `json:"status"`
	DurationMs       int64                      `json:"duration_ms"`
	TransferredBytes int64                      `json:"transferred_bytes,omitempty"`
	Error            string                     `json:"error,omitempty"`
	PlatformResults  []image.PlatformCopyResult `json:"platform_results,omitempty"`
}

// runResultSummary 是 result_json 的形态（通知 formatter 的输入，
// commit 7 消费）：platform_skip 在 run 级并入 skipped_count。
type runResultSummary struct {
	Status           string `json:"status"`
	TotalImages      int    `json:"total_images"`
	SuccessCount     int    `json:"success_count"`
	SkippedCount     int    `json:"skipped_count"`
	FailureCount     int    `json:"failure_count"`
	TransferredBytes int64  `json:"transferred_bytes,omitempty"`
	DurationMs       int64  `json:"duration_ms"`
}

// ociProgress 把 task.ProgressSink 适配到统一 Runner 的
// job.ProgressReporter：SetWorkTotal(len(images)×len(dst))；每个
// (image, dst) 对 BeginUnit（target=目标镜像完整引用，「running 起点也
// 回报一次」）/ EndUnit；收敛对 AddWorkDone（失败不递增）。
type ociProgress struct {
	reporter job.ProgressReporter
}

func (p ociProgress) SetWorkTotal(total int) { p.reporter.SetWorkTotal(int64(total)) }

func (p ociProgress) BeginPair(target string) {
	p.reporter.BeginUnit(target, ItemActionCopy, 0)
}

func (p ociProgress) EndPair(target string) { p.reporter.EndUnit(target) }

func (p ociProgress) PairDone() { p.reporter.AddWorkDone(1) }

// Run 实现 job.Executor：一轮镜像同步。取消约定与 oci-sync 一致——引擎
// 只看 ctx，run 级 cancelled 由本层以 ctx.Err() 判定并传入 FinalStatus，
// 再以携带 context.Canceled 的错误链返回（统一 Runner 据此收敛 canceled
// 终态）；failure / partial_success 映射为 run 级失败（错误摘要可查），
// 全部跳过 / 成功映射为成功。
func (e *OCIExecutor) Run(ctx context.Context, req job.RunRequest) (job.RunResult, error) {
	taskCfg, err := ParseTaskConfig(req.Job.Config)
	if err != nil {
		return job.RunResult{}, err
	}
	res, err := e.registries.Resolver(ctx)
	if err != nil {
		return job.RunResult{}, fmt.Errorf("load registries: %w", err)
	}

	progress := job.NullProgress(req.Progress)
	// connecting 由统一 Runner 先行登记；工作量在计划完成后确定，
	// 扇出复制即进入 transferring。
	progress.SetPhase(job.RunPhaseTransferring)
	result, execErr := e.tasks.ExecuteTask(ctx, res, taskCfg, ociProgress{reporter: progress})

	// 明细：每个 (image, dst) 对一行（含 skipped——digest 跳过与
	// platform_skip 是有意义的按镜像状态，GET /images 依赖）。
	if req.Items != nil && execErr == nil {
		progress.SetPhase(job.RunPhaseFinalizing)
		for _, copy := range result.Results {
			if err := req.Items.RecordItem(ctx, copyToItem(req.RunID, copy)); err != nil {
				return job.RunResult{}, fmt.Errorf("record oci run item: %w", err)
			}
		}
	}

	// 终态：cancelled > failure > partial_success > skipped > success。
	cancelled := ctx.Err() != nil
	finalStatus := task.FinalStatus(result, cancelled, execErr)
	result.Status = finalStatus

	summary := runResultSummary{
		Status:           finalStatus,
		TotalImages:      result.TotalImages,
		SuccessCount:     result.SuccessCount,
		SkippedCount:     result.SkippedCount,
		FailureCount:     result.FailureCount,
		TransferredBytes: result.TransferredBytes,
		DurationMs:       result.EndTime.Sub(result.StartTime).Milliseconds(),
	}
	resultJSON, merr := job.MarshalResult(summary)
	if merr != nil {
		return job.RunResult{Result: resultJSON}, fmt.Errorf("marshal oci run result: %w", merr)
	}

	var runErr error
	switch {
	case execErr != nil:
		// 配置 / 展开失败等任务级错误。
		runErr = execErr
	case cancelled:
		runErr = fmt.Errorf("image sync interrupted: %w", ctx.Err())
	case finalStatus == "failure" || finalStatus == "partial_success":
		runErr = fmt.Errorf("%d of %d image copies failed", result.FailureCount, result.TotalImages)
	}
	return job.RunResult{Result: resultJSON}, runErr
}

// copyToItem 把单个复制结果映射为统一 RunItem：Target 为目标镜像完整
// 引用，Status 收敛到公共枚举（platform_skip 并入 skipped），领域状态
// （success / skipped / platform_skip / failed）保留在 detail。
func copyToItem(runID string, copy task.CopyResult) job.RunItem {
	status := job.ItemSucceeded
	action := ItemActionCopy
	detailStatus := "success"
	switch {
	case !copy.Success:
		status = job.ItemFailed
		detailStatus = "failed"
	case copy.PlatformSkip:
		status = job.ItemSkipped
		action = ItemActionSkip
		detailStatus = "platform_skip"
	case copy.Skipped:
		status = job.ItemSkipped
		action = ItemActionSkip
		detailStatus = "skipped"
	}
	detail := itemDetail{
		Src:              copy.Source,
		Registry:         copy.DstRegistry,
		Status:           detailStatus,
		DurationMs:       copy.Duration,
		TransferredBytes: copy.TransferredBytes,
		Error:            copy.Error,
		PlatformResults:  copy.PlatformResults,
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		detailJSON = []byte("{}")
	}
	return job.RunItem{
		RunID:  runID,
		Target: copy.Destination,
		Action: action,
		Status: status,
		Bytes:  copy.TransferredBytes,
		Error:  copy.Error,
		Detail: detailJSON,
	}
}

// 编译期接口断言。
var _ job.Executor = (*OCIExecutor)(nil)
