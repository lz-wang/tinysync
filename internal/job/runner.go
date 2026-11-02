package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"tinysync/internal/logging"
)

// 运行协调相关错误：API 层映射为 409 / 503 等状态码。
var (
	// ErrRunActive 表示同一任务已有运行在进行（手动触发返回 409）。
	ErrRunActive = errors.New("another sync run is active")
	// ErrConcurrencyLimit 表示全局并发已达 MaxConcurrentJobs 上限
	//（手动触发返回 409）。
	ErrConcurrencyLimit = errors.New("concurrency limit reached")
	// ErrJobDisabled 表示目标任务已禁用。
	ErrJobDisabled = errors.New("sync job is disabled")
	// ErrShuttingDown 表示 Runner 正在关闭，拒绝启动新的运行
	//（手动触发返回 503）。
	ErrShuttingDown = errors.New("sync runner is shutting down")
	// ErrJobMutating 表示任务的配置变更正在进行（手动触发返回 409；
	// 调度触发视为瞬时状态，occurrence 不消费，稍后重试）。
	ErrJobMutating = errors.New("sync job is being modified")
)

// RunStatus 是一次运行的快照（API status 端点的数据形态）。
type RunStatus struct {
	RunID      string
	State      RunState
	StartedAt  time.Time
	FinishedAt time.Time
	Result     []byte
	// Error 是失败原因的人类可读描述；成功时为空。
	Error string
}

// activeRun 是进行中的一轮运行。
type activeRun struct {
	runID        string
	jobID        string
	jobKind      JobKind
	trigger      RunTrigger
	scheduledFor *time.Time
	// cancel 是本轮唯一的取消根。CancelCauseFunc 使「用户手动停止」
	//（cancel(ErrRunCanceled)）与「服务 Shutdown」（cancel(nil)，cause
	// 退化为 context.Canceled）在 finalize 中可区分。
	cancel context.CancelCauseFunc
	// progress 是本轮的进程内实时进度（transient）：Runner 与领域
	// 执行器经 ProgressReporter 共同推进；运行结束随 activeRun 销毁。
	progress *RunProgress
	// transfers 是本轮使用的进程级传输 limiter（Runner 单例的快照，
	// 发布时在 r.mu 临界区内取出——惰性创建只发生一次，全部运行共享
	// 同一实例）。
	transfers *TransferLimiter
	done      chan struct{}
	// startedAt 在 goroutine 结束时用于填充最终状态。
	startedAt time.Time
}

// occupant 标识任务协调位的占用者：执行链（Runner.start 到运行结束）
// 或配置变更链（API 的修改 / 删除）。两类占用互斥，使「读取任务配置」
// 与「声明运行中」成为原子操作，杜绝旧配置的运行与新配置写入交叉。
// 零值 occupantNone 表示无占用者（map 读取缺省值即无占用）。
type occupant int

const (
	occupantNone occupant = iota
	occupantRun
	occupantMutation
)

// Runner 是统一运行协调器：并发任务数受控（MaxConcurrentJobs）、
// 同一任务严格串行（手动触发冲突报错，调度触发记 skipped）、按 kind
// 分发到领域执行器（Executor.Run）、运行记录以 history 为唯一事实
// 来源（run row 在运行 goroutine 启动前落库）。公共层不理解文件
// 路径或 OCI Manifest——领域内部全部封装在各执行器内。
type Runner struct {
	repo    Repository
	history RunRepository
	// executors 是按类型静态注册的执行器表。
	executors map[JobKind]Executor
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time

	// MaxConcurrentJobs 是全进程同时运行的任务数上限；必须为正整数，
	// 由应用装配从运行配置注入（默认 2）。
	MaxConcurrentJobs int
	// MaxConcurrentTransfers 是全进程同时进行的传输上限，由各领域
	// 执行器经共享 TransferLimiter 消费；必须为正整数。
	MaxConcurrentTransfers int
	// TransferTimeout 是单文件单次 attempt 的传输超时；0 表示不启用。
	// 由应用装配从运行配置注入。
	TransferTimeout time.Duration

	// CompletionHook 是运行终态成功落库后的旁路消费者（完成通知等）。
	// nil 表示不发布；实现必须立即返回，任何下游耗时不得拖住终态收口
	//（ADR 0006）。
	CompletionHook RunCompletionHook

	mu           sync.Mutex
	active       map[string]*activeRun // jobID → 进行中的运行
	occupancy    map[string]occupant   // jobID → 协调位占用者（run / mutation）
	transfers    *TransferLimiter      // 进程级传输上限（惰性创建的单例）
	shuttingDown bool                  // Shutdown 已进入：拒绝启动新运行
	wg           sync.WaitGroup
}

// NewRunner 构造 Runner：默认并发任务数 2（计划约定）、并发传输 4，
// 应用装配可按运行配置覆盖。
func NewRunner(repo Repository, history RunRepository, executors map[JobKind]Executor) *Runner {
	return &Runner{
		repo:                   repo,
		history:                history,
		executors:              executors,
		Now:                    func() time.Time { return time.Now().UTC() },
		MaxConcurrentJobs:      2,
		MaxConcurrentTransfers: 4,
		active:                 make(map[string]*activeRun),
		occupancy:              make(map[string]occupant),
	}
}

// Start 校验并异步启动一轮手动运行，立即返回 run ID。运行 context 独立于
// 调用方的 HTTP request context；同一任务已在运行或全局并发已满、任务
// 禁用时拒绝启动。
func (r *Runner) Start(ctx context.Context, jobID string) (string, error) {
	return r.StartWith(ctx, jobID, StartOptions{})
}

// StartOptions 是手动运行的附加输入。ConfigOverride 是领域专用的
// 「本次运行配置快照改写」（如 oci 的 src_images 子集触发）：在协调位
// 占用后的配置读取路径内应用，仍走统一快照语义——不落库、运行期间
// 不受并发配置变更影响；返回错误视为启动失败（400 语义由调用方映射）。
type StartOptions struct {
	// ConfigOverride 为 nil 表示以落库配置运行；非 nil 时以返回值替换
	// 本轮配置快照（不得返回 nil）。
	ConfigOverride func(Job) (json.RawMessage, error)
}

// StartWith 按 StartOptions 启动一轮手动运行（Start 的扩展形态）。
func (r *Runner) StartWith(ctx context.Context, jobID string, opts StartOptions) (string, error) {
	if opts.ConfigOverride == nil {
		return r.start(ctx, jobID, TriggerManual, nil, nil)
	}
	return r.start(ctx, jobID, TriggerManual, nil, opts.ConfigOverride)
}

// StartScheduled 以调度触发启动一轮运行。occurrence 到期但同任务运行中
// 时不排队：记录 status=skipped 的 run（error 记原因）并消费该
// occurrence，返回空 run ID 与 nil 错误。全局并发已满时不记 skipped：
// 返回 ErrConcurrencyLimit（瞬时错误），调度器保持游标在后续周期重试
// 该到期任务。读取配置或落库失败返回错误——occurrence 未被消费，
// 调度器应重试窗口而不是视为已处理。
func (r *Runner) StartScheduled(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor time.Time) (string, error) {
	return r.start(ctx, jobID, trigger, &scheduledFor, nil)
}

// start 是手动与调度触发的共同路径：原子占用任务协调位 → 读取并校验
// 配置（含可选的本次运行配置快照改写）→ 检查全局容量 → 同步落库
// running 记录 → 启动 goroutine 分发到领域执行器。
func (r *Runner) start(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor *time.Time, override func(Job) (json.RawMessage, error)) (string, error) {
	// 先原子占用协调位再读取任何配置：API 的修改 / 删除同样必须拿到
	// 协调位才能执行，占用成功后读到的配置在其运行期间不会被变更。
	r.mu.Lock()
	if r.shuttingDown {
		r.mu.Unlock()
		return "", ErrShuttingDown
	}
	occ, busy := r.occupancy[jobID]
	if busy {
		r.mu.Unlock()
		if scheduledFor != nil {
			if occ == occupantMutation {
				// 配置变更只是毫秒级的短暂互斥，不属于 overlap 策略：
				// 记 skipped 会永久消费 occurrence（once 尤其不该被
				// 一次顺手改名吞掉）。返回瞬时错误让调度器保持游标
				// 重试，变更完成后按新配置执行。
				return "", ErrJobMutating
			}
			// 同任务运行中：记 skipped（occurrence 已消费）。
			j, err := r.repo.Get(ctx, jobID)
			if err != nil {
				return "", fmt.Errorf("load job %s to record skipped run: %w", jobID, err)
			}
			return "", r.recordSkipped(ctx, j, trigger, *scheduledFor, "previous run still active")
		}
		if occ == occupantMutation {
			return "", ErrJobMutating
		}
		return "", fmt.Errorf("%w: job %s is running", ErrRunActive, jobID)
	}
	r.occupancy[jobID] = occupantRun
	r.mu.Unlock()
	// 校验失败路径统一由此释放；run 发布成功后改由运行 goroutine 接管。
	starting := true
	defer func() {
		if starting {
			r.releaseOccupancy(jobID)
		}
	}()

	j, err := r.repo.Get(ctx, jobID)
	if err != nil {
		return "", err
	}
	if !j.Enabled {
		return "", fmt.Errorf("%w: %s", ErrJobDisabled, jobID)
	}
	// 本次运行的配置快照改写（如 oci 的 src_images 子集触发）：在协调位
	// 占用后的读取路径内应用，快照语义与普通运行一致——不落库。
	if override != nil {
		overridden, oerr := override(j)
		if oerr != nil {
			return "", oerr
		}
		if overridden != nil {
			j.Config = overridden
		}
	}
	exec, err := r.executor(j.Kind)
	if err != nil {
		return "", err
	}

	maxConcurrent := r.MaxConcurrentJobs
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	r.mu.Lock()
	if len(r.active) >= maxConcurrent {
		r.mu.Unlock()
		if scheduledFor != nil {
			// 并发已满：不消费 occurrence，交由调度器在后续周期重试。
			return "", ErrConcurrencyLimit
		}
		return "", fmt.Errorf("%w: at most %d concurrent jobs", ErrConcurrencyLimit, maxConcurrent)
	}
	runID, err := newRunID()
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	now := r.Now()
	// runCtx 是这一轮 run 的唯一取消根：领域执行器的拨号、扫描与传输
	// 全部挂在其取消链上，Shutdown 或用户手动停止取消 runCtx 即可打断
	// 上述全部阶段——context 取消只能自父向子传播，取消根必须是 run
	// 自身而不是其外的独立 context。cancel 先于发布创建：active 一旦
	// 可见，Shutdown 与 Runner.Cancel 就一定能取到取消函数，不存在
	// cancel 尚为 nil 的生命周期窗口。运行 context 独立于调用方的
	// HTTP request context。
	runCtx, cancel := context.WithCancelCause(context.Background())
	run := &activeRun{
		runID:        runID,
		jobID:        jobID,
		jobKind:      j.Kind,
		trigger:      trigger,
		scheduledFor: scheduledFor,
		cancel:       cancel,
		progress:     NewRunProgress(),
		transfers:    r.transferLimiter(),
		done:         make(chan struct{}),
		startedAt:    now,
	}
	r.active[jobID] = run
	starting = false
	// 与发布同一临界区递增 WaitGroup：Shutdown 读到该 run 时 wg 必已
	// 计入，Wait 不会在 goroutine 启动前先行返回。
	r.wg.Add(1)
	r.mu.Unlock()

	// run row 必须在 goroutine 启动前同步写入成功：不允许出现已经开始
	// 修改外部状态、却没有任何历史 run ID 的状态。调度触发经事务路径
	// 落库——once 的消费状态与 run row 原子写入，「run 存在 ⇔
	// occurrence 已消费」，不存在 Mark 失败导致重放的中间状态。失败
	// 则回收发布时预留的全部资源——active / 协调位 / WaitGroup 名额
	//（goroutine 尚未启动，Done 必须在此补齐，否则 wg 永不归零，
	// Shutdown 只能靠超时返回）。
	record := RunRecord{
		ID:           runID,
		JobKind:      j.Kind,
		JobID:        jobID,
		Trigger:      trigger,
		ScheduledFor: scheduledFor,
		State:        RunRunning,
		StartedAt:    now,
	}
	if trigger == TriggerManual {
		err = r.history.Insert(ctx, record)
	} else {
		err = r.history.PersistScheduledRun(ctx, record)
	}
	if err != nil {
		r.mu.Lock()
		delete(r.active, jobID)
		delete(r.occupancy, jobID)
		r.mu.Unlock()
		r.wg.Done()
		// goroutine 尚未启动，取消仅释放资源；cause 无意义。
		cancel(nil)
		return "", fmt.Errorf("persist run %s: %w", runID, err)
	}

	// run start 事件：终态事件只发生在结束时，进程硬崩溃后日志里没有
	// 「这个 run 曾经启动」的痕迹。run row 落库成功即输出 running，
	// 与终态事件构成同一 run_id 的完整时间线；字段布局与终态事件一致。
	logging.Infof("event=sync_run job_id=%s run_id=%s status=%s duration_ms=0 result={} error=%q",
		jobID, runID, RunRunning, "")

	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.active, jobID)
			delete(r.occupancy, jobID)
			r.mu.Unlock()
			// 运行已结束的兜底释放，cause 无意义；nil 不覆盖既有 cause。
			cancel(nil)
			close(run.done)
		}()
		r.runOne(runCtx, j, exec, run)
	}()
	return runID, nil
}

// executor 返回类型的执行器；未注册返回 ErrKindUnsupported。
func (r *Runner) executor(kind JobKind) (Executor, error) {
	exec, ok := r.executors[kind]
	if !ok {
		return nil, fmt.Errorf("%w: kind %q has no executor", ErrKindUnsupported, kind)
	}
	return exec, nil
}

// releaseOccupancy 释放执行链在校验阶段占用的协调位。
func (r *Runner) releaseOccupancy(jobID string) {
	r.mu.Lock()
	delete(r.occupancy, jobID)
	r.mu.Unlock()
}

// transferLimiter 返回进程级共享的传输 limiter：首次调用按
// MaxConcurrentTransfers 构造（应用装配在 Start 之前注入运行配置），
// 之后所有任务共用同一实例，使传输并发上限约束整个进程。
// 调用方必须持有 r.mu。
func (r *Runner) transferLimiter() *TransferLimiter {
	if r.transfers == nil {
		limit := r.MaxConcurrentTransfers
		if limit < 1 {
			limit = 1
		}
		r.transfers = NewTransferLimiter(limit)
	}
	return r.transfers
}

// runOne 分发到领域执行器执行一轮同步：拨号、扫描、传输全部在执行器
// 内（连接阶段进度由 Runner 先行登记），结果摘要由执行器返回。本轮的
// 传输 limiter 是发布时预留的进程级单例快照。
func (r *Runner) runOne(ctx context.Context, j Job, exec Executor, run *activeRun) {
	// connecting 阶段覆盖执行器内的拨号；执行器接管后推进到
	// scanning 及之后。
	run.progress.SetPhase(RunPhaseConnecting)
	outcome, runErr := exec.Run(ctx, RunRequest{
		Job:             j,
		RunID:           run.runID,
		Progress:        run.progress,
		Items:           runItemRecorder{repo: r.history},
		Transfers:       run.transfers,
		TransferTimeout: r.TransferTimeout,
	})
	r.finalize(ctx, run, j, outcome.Result, runErr)
}

// finalize 把终态落库；落库失败只记日志，不改变本轮结果。终态落库与
// 历史回收不受运行取消影响：Shutdown 取消 runCtx 后执行器因取消返回，
// 此刻正是最需要把 failed 终态写库的时机——用已取消的 ctx 调真实
// SQLite 会直接失败，遗留 running 行只能等下次启动的 stale 恢复；
// 优雅关闭应在本进程内完成终态收敛。终态同时输出 event=sync_run
// 结构化事件（result 为领域结果摘要 JSON）。
func (r *Runner) finalize(ctx context.Context, run *activeRun, j Job, result []byte, runErr error) {
	finishedAt := r.Now()
	final := RunRecord{
		ID:           run.runID,
		JobKind:      run.jobKind,
		JobID:        run.jobID,
		Trigger:      run.trigger,
		ScheduledFor: run.scheduledFor,
		State:        RunSucceeded,
		StartedAt:    run.startedAt,
		FinishedAt:   &finishedAt,
		Result:       result,
	}
	if runErr != nil {
		final.State = RunFailed
		final.Error = runErr.Error()
		// 用户手动停止且取消正是失败原因：终态记 canceled 而非 failed。
		// 判据是双重的——runErr 的错误链必须是取消（排除「取消到达前
		// 运行已因真实错误失败」的情形），且取消原因必须是用户手动
		// 停止（Shutdown 的 cause 为 context.Canceled，保持 failed）。
		if errors.Is(runErr, context.Canceled) &&
			errors.Is(context.Cause(ctx), ErrRunCanceled) {
			final.State = RunCanceled
			final.Error = ErrRunCanceled.Error()
		}
	}
	logging.Infof("event=sync_run job_id=%s run_id=%s status=%s duration_ms=%d result=%s error=%q",
		run.jobID, run.runID, final.State,
		finishedAt.Sub(run.startedAt).Milliseconds(), resultString(final.Result), final.Error)
	persistCtx := context.WithoutCancel(ctx)
	finalizeErr := r.history.Finalize(persistCtx, final)
	if finalizeErr != nil {
		logging.Errorf("finalize run %s: %v", run.runID, finalizeErr)
	}
	r.pruneHistory(persistCtx)
	// 完成事件只在终态成功落库后发布：落库失败时数据库仍显示
	// running，此刻发出完成通知会造成两边事实不一致——宁可漏发
	//（ADR 0006）。事件自带 JobName 快照，消费者无需回读可能已变更
	// 的任务配置。失败明细（R31）是事件的旁路负载：查询失败只记日志，
	// 不影响已落库的终态与事件本身的发布。
	if finalizeErr != nil || r.CompletionHook == nil {
		return
	}
	completion := RunCompletion{
		RunID:      final.ID,
		JobID:      final.JobID,
		JobKind:    final.JobKind,
		JobName:    j.Name,
		Trigger:    final.Trigger,
		State:      final.State,
		StartedAt:  final.StartedAt,
		FinishedAt: finishedAt,
		Result:     final.Result,
		Error:      final.Error,
	}
	if failed, total, err := r.history.FailedItems(persistCtx, final.ID, FailedItemLimit); err != nil {
		logging.Errorf("load failed items of run %s: %v", final.ID, err)
	} else if len(failed) > 0 {
		completion.FailedItems = make([]FailedItem, 0, len(failed))
		for _, item := range failed {
			completion.FailedItems = append(completion.FailedItems, FailedItem{
				Target: item.Target,
				Detail: item.Detail,
				Error:  item.Error,
			})
		}
		completion.FailedItemsTotal = total
	}
	r.CompletionHook.OnRunCompleted(completion)
}

// resultString 把结果 JSON 转为日志友好的字符串；空结果为 {}。
func resultString(result []byte) string {
	if len(result) == 0 {
		return "{}"
	}
	return string(result)
}

// pruneHistory 在 run 终态落库后回收每 Job 的历史容量（保留最近
// RetentionRunsPerJob 条）。使用独立 context：运行取消（如 Shutdown）
// 后仍要完成清理，防止长期运行的任务无限膨胀。
func (r *Runner) pruneHistory(ctx context.Context) {
	if err := r.history.PruneRetention(context.WithoutCancel(ctx), RetentionRunsPerJob); err != nil {
		logging.Errorf("prune run history: %v", err)
	}
}

// recordSkipped 记录调度触发的 skipped run：occurrence 已消费，
// 不排队、不执行；终态经事务路径落库（once 的消费状态与 skipped
// 记录原子写入），之后与正常执行走同一条容量回收路径。持久化失败
// 时返回错误——occurrence 未被成功消费，调用方（调度器）据此重试
// 整个窗口而不是把它当作已处理。
func (r *Runner) recordSkipped(ctx context.Context, j Job, trigger RunTrigger, scheduledFor time.Time, reason string) error {
	id, err := newRunID()
	if err != nil {
		return fmt.Errorf("generate skipped run id for job %s: %w", j.ID, err)
	}
	now := r.Now()
	run := RunRecord{
		ID:           id,
		JobKind:      j.Kind,
		JobID:        j.ID,
		Trigger:      trigger,
		ScheduledFor: &scheduledFor,
		State:        RunSkipped,
		StartedAt:    now,
		FinishedAt:   &now,
		Error:        reason,
	}
	if err := r.history.PersistScheduledRun(ctx, run); err != nil {
		return fmt.Errorf("record skipped run for job %s: %w", j.ID, err)
	}
	logging.Infof("event=sync_run job_id=%s run_id=%s status=%s duration_ms=0 result={} error=%q",
		j.ID, id, RunSkipped, reason)
	r.pruneHistory(ctx)
	return nil
}

// BeginMutation 原子占用任务的协调位，与执行链（start → 运行结束）
// 互斥：任务正在运行或正在启动时返回 ErrRunActive。配置修改 / 删除
// 在占用成功后才执行，从根上排除「IsRunning 检查通过后、Service 落库
// 前」运行恰好启动的 TOCTOU 窗口；占用方完成后必须调用 EndMutation。
func (r *Runner) BeginMutation(jobID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.occupancy[jobID]; busy {
		return fmt.Errorf("%w: job %s is running", ErrRunActive, jobID)
	}
	r.occupancy[jobID] = occupantMutation
	return nil
}

// EndMutation 释放 BeginMutation 占用的协调位；多次调用安全。
func (r *Runner) EndMutation(jobID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if occ, busy := r.occupancy[jobID]; busy && occ == occupantMutation {
		delete(r.occupancy, jobID)
	}
}

// IsRunning 判断指定任务是否被执行链占用（含正在启动的窗口）。
func (r *Runner) IsRunning(jobID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.occupancy[jobID] == occupantRun
}

// GetStatus 返回任务的运行状态：进行中返回 running 快照，否则返回
// 最近一条持久化 run（含 skipped）；从未运行为 idle。重启后历史仍在。
func (r *Runner) GetStatus(ctx context.Context, jobID string) (RunStatus, error) {
	r.mu.Lock()
	run := r.active[jobID]
	r.mu.Unlock()
	if run != nil {
		return RunStatus{
			RunID:     run.runID,
			State:     RunRunning,
			StartedAt: run.startedAt,
		}, nil
	}
	rec, err := r.history.Latest(ctx, jobID)
	if errors.Is(err, ErrRunUnknown) {
		return RunStatus{State: RunIdle}, nil
	}
	if err != nil {
		return RunStatus{}, err
	}
	return runRecordToStatus(rec), nil
}

// Wait 等待指定 run 结束并返回最终状态；run 未知或已结束则查询
// 持久化历史。
func (r *Runner) Wait(ctx context.Context, runID string) (RunStatus, error) {
	run := r.findActive(runID)
	if run != nil {
		select {
		case <-run.done:
		case <-ctx.Done():
			return RunStatus{}, ctx.Err()
		}
	}
	rec, err := r.history.Get(ctx, runID)
	if err != nil {
		return RunStatus{}, err
	}
	return runRecordToStatus(rec), nil
}

// Shutdown 拒绝启动新运行，取消全部在途运行并等待退出；ctx 超时
// 则返回 ctx 错误。多次调用安全。
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.shuttingDown = true
	cancels := make([]context.CancelCauseFunc, 0, len(r.active))
	for _, run := range r.active {
		cancels = append(cancels, run.cancel)
	}
	r.mu.Unlock()
	// cancel(nil)：cause 退化为 context.Canceled——Shutdown 不是用户
	// 主动行为，finalize 据此保持 failed 终态，与启动恢复的 stale
	// running 收敛语义一致。
	for _, cancel := range cancels {
		cancel(nil)
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancel 手动取消一轮进行中的运行：以 ErrRunCanceled 为原因触发 runCtx
// 取消链（拨号、扫描、传输全部中断），终态由 finalize 收敛为 canceled。
// run 已不在进行中（不存在或已终态）返回 ErrRunNotActive——按 run ID
// 而非任务取消，杜绝「看到的 run 已结束、迟到的停止误伤下一轮」的
// 竞态。仍 active 的重复取消幂等（cancel 为 no-op，返回 nil）。
func (r *Runner) Cancel(runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.active {
		if run.runID == runID {
			run.cancel(ErrRunCanceled)
			return nil
		}
	}
	return ErrRunNotActive
}

// Progress 返回进行中 run 的实时进度快照；run 不在进行中（不存在、
// 已终态或运行已结束但尚未移出 active）返回 false——调用方以持久化
// 历史为准渲染终态。
func (r *Runner) Progress(runID string) (RunProgressSnapshot, bool) {
	run := r.findActive(runID)
	if run == nil {
		return RunProgressSnapshot{}, false
	}
	return run.progress.Snapshot(), true
}

// findActive 按 run ID 查找进行中的运行。
func (r *Runner) findActive(runID string) *activeRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.active {
		if run.runID == runID {
			return run
		}
	}
	return nil
}

// NextRunAt 返回任务的下一次计划触发时间：manual 恒无；once 已消费后
// 无（未消费时返回配置时刻，即使已过期——调度器会立即补执行）；
// interval / cron 按 schedule 与当前时刻计算。第二个返回值为 false
// 表示没有下一次触发。
func (r *Runner) NextRunAt(ctx context.Context, j Job) (time.Time, bool, error) {
	switch j.Schedule.Type {
	case ScheduleOnce, ScheduleInterval, ScheduleCron:
	default:
		return time.Time{}, false, nil
	}
	next, ok := j.Schedule.NextRun(r.Now())
	if !ok {
		return time.Time{}, false, nil
	}
	if j.Schedule.Type == ScheduleOnce && OnceConsumed(j) {
		return time.Time{}, false, nil
	}
	return next, true, nil
}

// ListRuns 按过滤与分页查询持久化运行历史，total 为过滤后总数。
func (r *Runner) ListRuns(ctx context.Context, filter RunFilter) ([]RunRecord, int, error) {
	return r.history.List(ctx, filter)
}

// GetRun 按 ID 查询运行摘要；不存在时返回 ErrRunUnknown。
func (r *Runner) GetRun(ctx context.Context, runID string) (RunRecord, error) {
	return r.history.Get(ctx, runID)
}

// ListRunItems 分页查询 run 的明细，total 为该 run 明细总数。
// 先确认 run 存在：明细为空的 run 与不存在的 run 必须可区分
// （不存在返回 ErrRunUnknown，与 /runs/:id 的资源语义一致）。
func (r *Runner) ListRunItems(ctx context.Context, runID string, limit, offset int) ([]RunItem, int, error) {
	if _, err := r.history.Get(ctx, runID); err != nil {
		return nil, 0, err
	}
	return r.history.Items(ctx, runID, limit, offset)
}

// runItemRecorder 把执行器的明细输出适配到 RunRepository。
type runItemRecorder struct {
	repo RunRepository
}

// RecordItem 实现 ItemRecorder。
func (a runItemRecorder) RecordItem(ctx context.Context, item RunItem) error {
	return a.repo.AppendItem(ctx, item)
}

// runRecordToStatus 把持久化 run 转为状态快照。
func runRecordToStatus(rec RunRecord) RunStatus {
	st := RunStatus{
		RunID:     rec.ID,
		State:     rec.State,
		StartedAt: rec.StartedAt,
		Result:    rec.Result,
		Error:     rec.Error,
	}
	if rec.FinishedAt != nil {
		st.FinishedAt = *rec.FinishedAt
	}
	return st
}

// newRunID 生成 run_<128-bit random hex> 形式的运行 ID。
func newRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return "run_" + hex.EncodeToString(buf), nil
}
