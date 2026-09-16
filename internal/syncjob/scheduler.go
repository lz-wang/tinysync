package syncjob

import (
	"context"
	"sync"
	"time"

	"tinysync/internal/logging"
)

// schedulerTickInterval 是调度器检查周期。HomeLab 的 Job 数量很小，
// 单 goroutine + 秒级 ticker 的负载可忽略：动态修改 Job 无需注册/注销
// 机制，调度规则始终以 SQLite Job 配置为事实来源。
const schedulerTickInterval = time.Second

// Scheduler 周期检查启用 Job 的 schedule 并触发到期运行。不引入通用
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
// Runner.StartScheduled 触发（overlap / 容量不足时由 Runner 记 skipped），
// 窗口外不回看。抽出为独立方法以便固定时钟直接测试。
//
// 游标只在成功读取并处理完全部 Job 后推进：List 失败或处理中断时保持
// 不动，下一个 tick 重扫同一窗口，不会因一次内部失败吞掉 occurrence；
// 重复扫描由持久化的 occurrence 消费记录挡住（见下）。
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
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		if !job.Enabled {
			continue
		}
		var trigger RunTrigger
		switch job.Schedule.Type {
		case ScheduleOnce:
			trigger = TriggerOnce
		case ScheduleInterval:
			trigger = TriggerInterval
		case ScheduleCron:
			trigger = TriggerCron
		default: // manual 或未知类型不调度
			continue
		}
		occurrence, due := job.Schedule.DueOccurrence(from, now)
		if !due {
			continue
		}
		if trigger == TriggerOnce {
			// once 的消费状态在 Job 上（sync_jobs.once_consumed_for，
			// 持久化 correctness state）：错过仍补执行一次，且不依赖
			// 可被 retention 裁剪的运行历史存活。
			if onceConsumed(job, occurrence) {
				continue
			}
		} else {
			// interval / cron 以持久化历史（含 skipped）判定 occurrence
			// 消费：游标因内部失败回退重扫时不重复触发同一 occurrence。
			consumed, err := s.history.HasRunFor(ctx, job.ID, trigger, occurrence)
			if err != nil {
				logging.Errorf("scheduler check occurrence consumption for job %s: %v", job.ID, err)
				continue
			}
			if consumed {
				continue
			}
		}
		if _, err := s.runner.StartScheduled(ctx, job.ID, trigger, occurrence); err != nil {
			// 禁用等校验失败只记日志：不阻塞后续 Job。
			logging.Errorf("scheduled run for job %s: %v", job.ID, err)
		}
	}
	s.mu.Lock()
	s.cursor = now
	s.mu.Unlock()
}
