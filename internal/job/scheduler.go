package job

import (
	"context"
	"errors"
	"sync"
	"time"

	"tinysync/internal/logging"
)

// schedulerTickInterval 是调度器检查周期。HomeLab 的任务数量很小，
// 单 goroutine + 秒级 ticker 的负载可忽略：动态修改任务无需注册/注销
// 机制，调度规则始终以 SQLite 任务配置为事实来源。
const schedulerTickInterval = time.Second

// Scheduler 周期检查启用任务的 schedule 并触发到期运行。不引入通用
// scheduler framework：Runner、历史与并发策略仍由 TinySync 自己协调。
type Scheduler struct {
	repo    Repository
	runner  *Runner
	history RunRepository
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time

	mu sync.Mutex
	// cursor 是上次 tick 处理到的时刻，初始化为启动时刻：窗口外
	//（含离线期间错过）的周期不回看，即 cron / interval 不补跑。
	cursor time.Time
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewScheduler 构造 Scheduler。
func NewScheduler(repo Repository, runner *Runner, history RunRepository) *Scheduler {
	return &Scheduler{
		repo:    repo,
		runner:  runner,
		history: history,
		Now:     func() time.Time { return time.Now().UTC() },
	}
}

// Start 启动调度循环；ctx 取消或 Stop 被调用后退出。
func (s *Scheduler) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mu.Lock()
	s.cursor = s.Now()
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(schedulerTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.tick(ctx)
			}
		}
	}()
}

// Stop 停止调度循环并等待退出；多次调用安全。
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// tick 处理内存游标窗口 (from, now] 内到期的 occurrence：命中即经
// Runner.StartScheduled 触发（同任务重叠由 Runner 记 skipped；全局
// 并发已满返回瞬时错误，游标不推进、后续周期重试），窗口外不回看。
// 抽出为独立方法以便固定时钟直接测试。
//
// 游标只在成功读取并处理完全部任务后推进：List 失败、处理中断或任一
// occurrence 的检查 / 持久化失败时保持不动，下一个 tick 重扫同一窗口，
// 不会因一次内部失败吞掉 occurrence；已成功启动 / skip 的 occurrence
// 由持久化消费记录（once 读 Job、interval / cron 读历史）挡住重复。
func (s *Scheduler) tick(ctx context.Context) {
	s.mu.Lock()
	from := s.cursor
	s.mu.Unlock()
	now := s.Now()

	jobs, err := s.repo.List(ctx)
	if err != nil {
		logging.Errorf("scheduler list jobs: %v", err)
		return
	}
	tickComplete := true
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		if !j.Enabled {
			continue
		}
		var trigger RunTrigger
		switch j.Schedule.Type {
		case ScheduleOnce:
			trigger = TriggerOnce
		case ScheduleInterval:
			trigger = TriggerInterval
		case ScheduleCron:
			trigger = TriggerCron
		default: // manual 或未知类型不调度
			continue
		}
		occurrence, due := j.Schedule.DueOccurrence(from, now)
		if !due {
			continue
		}
		if trigger == TriggerOnce {
			// once 的消费状态在任务行上（jobs.once_consumed_at，
			// 持久化 correctness state）：错过仍补执行一次，且不依赖
			// 可被 retention 裁剪的运行历史存活。
			if OnceConsumed(j) {
				continue
			}
		} else {
			// interval / cron 以持久化历史（含 skipped）判定 occurrence
			// 消费：游标因内部失败回退重扫时不重复触发同一 occurrence。
			consumed, err := s.history.HasRunFor(ctx, j.ID, trigger, occurrence)
			if err != nil {
				logging.Errorf("scheduler check occurrence consumption for job %s: %v", j.ID, err)
				tickComplete = false
				continue
			}
			if consumed {
				continue
			}
		}
		if _, err := s.runner.StartScheduled(ctx, j.ID, trigger, occurrence); err != nil {
			logging.Errorf("scheduled run for job %s: %v", j.ID, err)
			// 禁用等确定性校验失败重试无意义，occurrence 随本轮丢弃；
			// 并发已满（ErrConcurrencyLimit）与配置变更占用
			//（ErrJobMutating）是瞬时状态——游标不推进，下一 tick
			// 重试整个窗口（并发已满时计划约定「保留到期任务后续
			// 周期重试」）；其余（配置读取、run 落库等）同样视为
			// 瞬时故障。
			if !errors.Is(err, ErrJobDisabled) &&
				!errors.Is(err, ErrNotFound) {
				tickComplete = false
			}
		}
	}
	if !tickComplete {
		return
	}
	s.mu.Lock()
	s.cursor = now
	s.mu.Unlock()
}
