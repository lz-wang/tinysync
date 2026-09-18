package syncjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"tinysync/internal/logging"
	"tinysync/internal/source"
)

// RunState 是运行的状态机取值。running → succeeded | failed；
// skipped 是调度触发但未执行（overlap / 容量不足）的持久化记录。
// idle 不是持久化状态，仅表示「从未运行」的查询占位。
type RunState string

// 运行状态机取值。运行记录持久化于 sync_runs，重启后仍可查询。
const (
	RunIdle      RunState = "idle"
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunSkipped   RunState = "skipped"
)

// 手动运行相关错误：API 层映射为 409 等状态码。
var (
	// ErrRunActive 表示同一 Job 已有运行在进行（手动触发返回 409）。
	ErrRunActive = errors.New("another sync run is active")
	// ErrConcurrencyLimit 表示全局并发已达 MaxConcurrentJobs 上限
	// （手动触发返回 409）。
	ErrConcurrencyLimit = errors.New("concurrency limit reached")
	// ErrJobDisabled 表示目标 Job 已禁用。
	ErrJobDisabled = errors.New("sync job is disabled")
	// ErrSourceDisabled 表示 Job 引用的 Source 已禁用。
	ErrSourceDisabled = errors.New("source is disabled")
	// ErrRunUnknown 表示查询的运行记录不存在。
	ErrRunUnknown = errors.New("run not found")
	// ErrShuttingDown 表示 Runner 正在关闭，拒绝启动新的运行
	// （手动触发返回 503）。
	ErrShuttingDown = errors.New("sync runner is shutting down")
	// ErrJobMutating 表示 Job 的配置变更正在进行（手动触发返回 409；
	// 调度触发视为瞬时状态，occurrence 不消费，稍后重试）。
	ErrJobMutating = errors.New("sync job is being modified")
)

// SourceGateway 是 Runner 访问 Source 领域的能力边界：读取 Source
// 配置与打开远端客户端。凭据查询与协议 dispatch 集中在 source.Service
// 内，Runner 不接触凭据明文与 RemoteFactory。
type SourceGateway interface {
	Get(ctx context.Context, id string) (source.Source, error)
	// OpenRemote 打开 Source 的远端客户端并返回其配置；调用方负责 Close。
	OpenRemote(ctx context.Context, id string) (source.Source, source.Remote, error)
}

// RunStatus 是一次运行的快照（API status 端点的数据形态）。
type RunStatus struct {
	RunID      string
	State      RunState
	StartedAt  time.Time
	FinishedAt time.Time
	Stats      RunStats
	// Error 是失败原因的人类可读描述；成功时为空。
	Error string
}

// activeRun 是进行中的一轮同步。
type activeRun struct {
	runID        string
	jobID        string
	trigger      RunTrigger
	scheduledFor *time.Time
	cancel       context.CancelFunc
	done         chan struct{}
	// transfers 是本轮使用的进程级传输 limiter（Runner 单例的快照）。
	transfers *TransferLimiter
	// startedAt 在 goroutine 结束时用于填充最终状态。
	startedAt time.Time
}

// occupant 标识 Job 协调位的占用者：执行链（Runner.start 到运行结束）
// 或配置变更链（API 的修改 / 删除）。两类占用互斥，使「读取 Job 配置」
// 与「声明运行中」成为原子操作，杜绝旧配置的运行与新配置写入交叉。
// 零值 occupantNone 表示无占用者（map 读取缺省值即无占用）。
type occupant int

const (
	occupantNone occupant = iota
	occupantRun
	occupantMutation
)

// Runner 是同步执行协调器：并发 Job 数受控（MaxConcurrentJobs）、同一
// Job 严格串行（手动触发冲突报错，调度触发记 skipped）、运行记录以
// history 为唯一事实来源（run row 在同步 goroutine 启动前落库）。
type Runner struct {
	repo    Repository
	managed ManagedRepository
	// sources 提供 Source 读取与远端客户端构造（含凭据与协议 dispatch）。
	sources SourceGateway
	// history 是运行记录的持久化仓库。
	history RunRepository
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time

	// MaxConcurrentJobs 是全进程同时运行的 Job 数上限；必须为正整数，
	// 由应用装配从运行配置注入。
	MaxConcurrentJobs int
	// MaxConcurrentTransfers 是全进程同时进行的远端文件下载上限，
	// 由所有 Job 的同步引擎经共享 transfer limiter 消费；必须为正整数。
	MaxConcurrentTransfers int
	// TransferTimeout 是单文件单次 attempt 的传输超时；0 表示不启用。
	// 由应用装配从运行配置注入。
	TransferTimeout time.Duration

	mu           sync.Mutex
	active       map[string]*activeRun // jobID → 进行中的运行
	occupancy    map[string]occupant   // jobID → 协调位占用者（run / mutation）
	transfers    *TransferLimiter      // 进程级传输上限（惰性创建的单例）
	shuttingDown bool                  // Shutdown 已进入：拒绝启动新运行
	wg           sync.WaitGroup
}

// NewRunner 构造 Runner：默认并发 Job 数 1（与 v0.3 行为一致）、
// 并发传输 4，应用装配可按运行配置覆盖。
func NewRunner(repo Repository, managed ManagedRepository, sources SourceGateway, history RunRepository) *Runner {
	return &Runner{
		repo:                   repo,
		managed:                managed,
		sources:                sources,
		history:                history,
		Now:                    func() time.Time { return time.Now().UTC() },
		MaxConcurrentJobs:      1,
		MaxConcurrentTransfers: 4,
		active:                 make(map[string]*activeRun),
		occupancy:              make(map[string]occupant),
	}
}

// Start 校验并异步启动一轮手动同步，立即返回 run ID。运行 context 独立于
// 调用方的 HTTP request context；同一 Job 已在运行或全局并发已满、Job 或
// Source 禁用时拒绝启动。
func (r *Runner) Start(ctx context.Context, jobID string) (string, error) {
	return r.start(ctx, jobID, TriggerManual, nil)
}

// StartScheduled 以调度触发启动一轮同步。occurrence 到期但同 Job 运行中
// 或全局并发已满时不排队：记录 status=skipped 的 run（error 记原因）并
// 消费该 occurrence，返回空 run ID 与 nil 错误；读取配置或落库失败返回
// 错误——occurrence 未被消费，调度器应重试窗口而不是视为已处理。
func (r *Runner) StartScheduled(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor time.Time) (string, error) {
	return r.start(ctx, jobID, trigger, &scheduledFor)
}

// start 是手动与调度触发的共同路径：原子占用 Job 协调位 → 读取并校验
// 配置 → 检查全局容量 → 同步落库 running 记录 → 启动 goroutine。
// Remote 创建在运行 goroutine 内进行：纳入 active 发布与 WaitGroup 之
// 后的真实 run 生命周期，创建失败记为 failed run，取消语义见 runCtx。
func (r *Runner) start(ctx context.Context, jobID string, trigger RunTrigger, scheduledFor *time.Time) (string, error) {
	// 先原子占用协调位再读取任何配置：API 的修改 / 删除同样必须拿到
	// 协调位才能执行，占用成功后读到的配置在其运行期间不会被变更，
	// 旧 mapping 的 metadata 推进与新配置写入不再可能交叉。
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
			// 记录 skipped 需要 Job 配置；读取或落库失败都视为
			// occurrence 未消费，交由调度器重试。
			job, err := r.repo.Get(ctx, jobID)
			if err != nil {
				return "", fmt.Errorf("load job %s to record skipped run: %w", jobID, err)
			}
			return "", r.recordSkipped(ctx, job, trigger, *scheduledFor, "previous run still active")
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

	job, err := r.repo.Get(ctx, jobID)
	if err != nil {
		return "", err
	}
	if !job.Enabled {
		return "", fmt.Errorf("%w: %s", ErrJobDisabled, jobID)
	}
	// Enabled 检查在读配置链路完成，禁用 Source 不产生到远端的拨号。
	src, err := r.sources.Get(ctx, job.SourceID)
	if err != nil {
		return "", err
	}
	if !src.Enabled {
		return "", fmt.Errorf("%w: %s", ErrSourceDisabled, job.SourceID)
	}

	maxConcurrent := r.MaxConcurrentJobs
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	r.mu.Lock()
	if len(r.active) >= maxConcurrent {
		r.mu.Unlock()
		if scheduledFor != nil {
			return "", r.recordSkipped(ctx, job, trigger, *scheduledFor, "concurrency limit reached")
		}
		return "", fmt.Errorf("%w: at most %d concurrent jobs", ErrConcurrencyLimit, maxConcurrent)
	}
	runID, err := newRunID()
	if err != nil {
		r.mu.Unlock()
		return "", err
	}
	now := r.Now()
	// runCtx 是这一轮 run 的唯一取消根：Remote 创建（含 SFTP 拨号）、
	// 有连接生命周期协议的关闭守护与引擎传输全部挂在其取消链上，
	// Shutdown 取消 runCtx 即可打断上述全部阶段——context 取消只能
	// 自父向子传播，取消根必须是 run 自身而不是其外的独立 context。
	// cancel 先于发布创建：active 一旦可见，Shutdown 就一定能取到
	// 取消函数，不存在 cancel 尚为 nil 的生命周期窗口；OpenRemote
	// 也已纳入该生命周期，Shutdown 与仍在拨号中的启动阶段没有竞态。
	// 运行 context 独立于调用方的 HTTP request context。
	runCtx, cancel := context.WithCancel(context.Background())
	run := &activeRun{
		runID:        runID,
		jobID:        jobID,
		trigger:      trigger,
		scheduledFor: scheduledFor,
		cancel:       cancel,
		done:         make(chan struct{}),
		transfers:    r.transferLimiter(),
		startedAt:    now,
	}
	r.active[jobID] = run
	starting = false
	// 与发布同一临界区递增 WaitGroup：Shutdown 读到该 run 时 wg 必已
	// 计入，Wait 不会在 goroutine 启动前先行返回。
	r.wg.Add(1)
	r.mu.Unlock()

	// run row 必须在 goroutine 启动前同步写入成功：不允许出现已经开始
	// 修改本地文件、却没有任何历史 run ID 的状态。调度触发经事务路径
	// 落库——once 的消费状态与 run row 原子写入，「run 存在 ⇔
	// occurrence 已消费」，不存在 Mark 失败导致重放的中间状态。失败
	// 则回收发布时预留的全部资源——active / 协调位 / WaitGroup 名额
	//（goroutine 尚未启动，Done 必须在此补齐，否则 wg 永不归零，
	// Shutdown 只能靠超时返回）。
	record := RunRecord{
		ID:           runID,
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
		cancel()
		return "", fmt.Errorf("persist run %s: %w", runID, err)
	}

	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.active, jobID)
			delete(r.occupancy, jobID)
			r.mu.Unlock()
			cancel()
			close(run.done)
		}()
		r.runOne(runCtx, job, run)
	}()
	return runID, nil
}

// releaseOccupancy 释放执行链在校验阶段占用的协调位。
func (r *Runner) releaseOccupancy(jobID string) {
	r.mu.Lock()
	delete(r.occupancy, jobID)
	r.mu.Unlock()
}

// transferLimiter 返回进程级共享的传输 limiter：首次调用按
// MaxConcurrentTransfers 构造（应用装配在 Start 之前注入运行配置），
// 之后所有 Job 共用同一实例，使下载并发上限约束整个进程。
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

// runOne 建立远端连接并运行同步引擎：Remote 创建属于运行期——创建
// 失败与运行失败同为一次 failed run（认证、host key、网络不通等在
// 历史中可查，而不是只在启动接口同步报错），创建阶段与传输阶段共用
// runCtx 一条取消链，Shutdown 取消运行即可打断拨号与阻塞中的读取。
func (r *Runner) runOne(ctx context.Context, job Job, run *activeRun) {
	_, remote, err := r.sources.OpenRemote(ctx, job.SourceID)
	if err != nil {
		r.finalize(ctx, run, RunStats{}, fmt.Errorf("create remote for source %s: %w", job.SourceID, err))
		return
	}
	// 运行结束释放 Remote 连接（有连接生命周期的协议如 SFTP 不遗留
	// 会话）；关闭失败不影响本轮终态。
	defer func() { _ = remote.Close() }()
	stats, runErr := Run(ctx, RunOptions{
		Remote:          remote,
		Job:             job,
		Managed:         r.managed,
		Items:           runItemRecorder{repo: r.history},
		RunID:           run.runID,
		Transfers:       run.transfers,
		TransferTimeout: r.TransferTimeout,
	})
	r.finalize(ctx, run, stats, runErr)
}

// finalize 把终态落库；落库失败只记日志，不改变本轮结果。终态落库与
// 历史回收不受运行取消影响：Shutdown 取消 runCtx 后 Engine 因取消返回，
// 此刻正是最需要把 failed 终态写库的时机——用已取消的 ctx 调真实
// SQLite 会直接失败，遗留 running 行只能等下次启动的 stale 恢复；
// 优雅关闭应在本进程内完成终态收敛。
func (r *Runner) finalize(ctx context.Context, run *activeRun, stats RunStats, runErr error) {
	finishedAt := r.Now()
	final := RunRecord{
		ID:           run.runID,
		JobID:        run.jobID,
		Trigger:      run.trigger,
		ScheduledFor: run.scheduledFor,
		State:        RunSucceeded,
		StartedAt:    run.startedAt,
		FinishedAt:   &finishedAt,
		Stats:        stats,
	}
	if runErr != nil {
		final.State = RunFailed
		final.Error = runErr.Error()
	}
	persistCtx := context.WithoutCancel(ctx)
	if err := r.history.Finalize(persistCtx, final); err != nil {
		logging.Errorf("finalize run %s: %v", run.runID, err)
	}
	r.pruneHistory(persistCtx)
}

// pruneHistory 在 run 终态落库后回收每 Job 的历史容量（保留最近
// RetentionRunsPerJob 条）。使用独立 context：运行取消（如 Shutdown）
// 后仍要完成清理，防止长期运行的 Job 无限膨胀。
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
func (r *Runner) recordSkipped(ctx context.Context, job Job, trigger RunTrigger, scheduledFor time.Time, reason string) error {
	id, err := newRunID()
	if err != nil {
		return fmt.Errorf("generate skipped run id for job %s: %w", job.ID, err)
	}
	now := r.Now()
	run := RunRecord{
		ID:           id,
		JobID:        job.ID,
		Trigger:      trigger,
		ScheduledFor: &scheduledFor,
		State:        RunSkipped,
		StartedAt:    now,
		FinishedAt:   &now,
		Error:        reason,
	}
	if err := r.history.PersistScheduledRun(ctx, run); err != nil {
		return fmt.Errorf("record skipped run for job %s: %w", job.ID, err)
	}
	r.pruneHistory(ctx)
	return nil
}

// BeginMutation 原子占用 Job 的协调位，与执行链（start → 运行结束）
// 互斥：Job 正在运行或正在启动时返回 ErrRunActive。配置修改 / 删除
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

// IsRunning 判断指定 Job 是否被执行链占用（含正在启动的窗口）。
func (r *Runner) IsRunning(jobID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.occupancy[jobID] == occupantRun
}

// GetStatus 返回 Job 的运行状态：进行中返回 running 快照，否则返回
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
	cancels := make([]context.CancelFunc, 0, len(r.active))
	for _, run := range r.active {
		cancels = append(cancels, run.cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
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

// NextRunAt 返回 Job 的下一次计划触发时间：manual 恒无；once 已消费后
// 无（未消费时返回配置时刻，即使已过期——调度器会立即补执行）；
// interval / cron 按 schedule 与当前时刻计算。第二个返回值为 false
// 表示没有下一次触发。
func (r *Runner) NextRunAt(ctx context.Context, job Job) (time.Time, bool, error) {
	var trigger RunTrigger
	switch job.Schedule.Type {
	case ScheduleOnce:
		trigger = TriggerOnce
	case ScheduleInterval:
		trigger = TriggerInterval
	case ScheduleCron:
		trigger = TriggerCron
	default:
		return time.Time{}, false, nil
	}
	next, ok := job.Schedule.NextRun(r.Now())
	if !ok {
		return time.Time{}, false, nil
	}
	if trigger == TriggerOnce {
		at, err := job.Schedule.OnceAt()
		if err != nil {
			return time.Time{}, false, nil
		}
		// once 消费状态在 Job 上（持久化 correctness state），不依赖
		// 运行历史存活。
		if onceConsumed(job, at) {
			return time.Time{}, false, nil
		}
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

// ListRunItems 分页查询 run 的文件级明细，total 为该 run 明细总数。
// 先确认 run 存在：明细为空的 run 与不存在的 run 必须可区分
// （不存在返回 ErrRunUnknown，与 /runs/:id 的资源语义一致）。
func (r *Runner) ListRunItems(ctx context.Context, runID string, limit, offset int) ([]RunItem, int, error) {
	if _, err := r.history.Get(ctx, runID); err != nil {
		return nil, 0, err
	}
	return r.history.Items(ctx, runID, limit, offset)
}

// runItemRecorder 把引擎的 ItemRecorder 适配到 RunRepository：
// 文件级明细直接追加到 sync_run_items。
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
		Stats:     rec.Stats,
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
