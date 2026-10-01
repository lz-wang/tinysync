package notification

import (
	"context"
	"sync"
	"time"

	"tinysync/internal/logging"
	"tinysync/internal/syncjob"
)

// queueCapacity 是完成事件的内存队列容量。HomeLab 的 run 频率远低于
// 此，溢出意味着下游长时间不可用——丢弃并记日志，不阻塞 finalize。
const queueCapacity = 64

// sendTimeout 是单条通知内单个发送器的独立超时（ADR 0006）。
const sendTimeout = 10 * time.Second

// Dispatcher 消费 syncjob.RunCompletion：实现 syncjob.RunCompletionHook，
// OnRunCompleted 只 enqueue 立即返回；单 worker 逐条热读取配置并经
// 已启用的渠道发送。worker 持有独立生命周期（不绑定应用主 ctx），
// 由 Shutdown 排空回收——SIGTERM 到达时最后几个 run 的通知不会被
// 主 ctx 的取消提前丢弃。不做 durable outbox：任务历史才是权威事实
// （ADR 0006）。
type Dispatcher struct {
	service *Service
	queue   chan syncjob.RunCompletion

	// pushoverFactory / emailFactory 按当次读取的配置构造发送器；
	// nil 用生产构造（PushoverSender / EmailSender）。测试注入假实现
	// 验证分发逻辑，不访问外网。
	pushoverFactory func(PushoverSettings) Sender
	emailFactory    func(EmailSettings) Sender

	mu      sync.Mutex
	closed  bool
	started bool
	done    chan struct{}
}

// NewDispatcher 构造 Dispatcher；Start 前只是惰性对象。
func NewDispatcher(service *Service) *Dispatcher {
	return &Dispatcher{
		service: service,
		queue:   make(chan syncjob.RunCompletion, queueCapacity),
		done:    make(chan struct{}),
	}
}

// Start 启动后台 worker。只应调用一次。
func (d *Dispatcher) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started {
		return
	}
	d.started = true
	go func() {
		defer close(d.done)
		for run := range d.queue {
			d.dispatch(run)
		}
	}()
}

// OnRunCompleted 实现 syncjob.RunCompletionHook：enqueue 立即返回，
// 队列满或已关闭时丢弃并记日志——通知是辅助提醒，绝不让发送路径
// 拖住 Runner 的终态收口。
func (d *Dispatcher) OnRunCompleted(run syncjob.RunCompletion) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	select {
	case d.queue <- run:
	default:
		logging.Warnf("event=notification run_id=%s status=dropped reason=queue_full", run.RunID)
	}
}

// Shutdown 停止接收新事件，排空队列中已有的事件后返回；ctx 超时返回
// ctx 错误，未发出的事件随进程退出丢弃。调用方（应用装配）保证这发生
// 在 Runner.Shutdown 之后——不再有新事件产生，drain 是有限集。
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.queue)
	}
	// 从未 Start：没有 worker 会关闭 done，这里直接标记完成（队列
	// 中未分发的事件随 Shutdown 丢弃，与「已 Start 但超时」同语义）。
	started := d.started
	d.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// dispatch 处理一条完成事件：热读取当前配置 → 共享 formatter 格式化
// 一次 → 逐个已启用渠道发送。任何失败只记结构化日志（绝不输出
// secret），继续处理下一条。
func (d *Dispatcher) dispatch(run syncjob.RunCompletion) {
	if run.State != syncjob.RunSucceeded && run.State != syncjob.RunFailed && run.State != syncjob.RunCanceled {
		// 防御：skipped / running 不是完成事件，不发通知。
		return
	}
	settings, err := d.service.Settings(context.Background())
	if err != nil {
		logging.Errorf("event=notification run_id=%s status=failed reason=load_settings error=%q", run.RunID, err)
		return
	}
	if !settings.Pushover.Enabled && !settings.Email.Enabled {
		return
	}
	message := FormatRun(run)
	if settings.Pushover.Enabled {
		d.send("pushover", run.RunID, d.pushoverSender(settings.Pushover), message)
	}
	if settings.Email.Enabled {
		d.send("email", run.RunID, d.emailSender(settings.Email), message)
	}
}

// send 经单个渠道发送一条消息：独立超时，成功与失败都留结构化痕迹。
func (d *Dispatcher) send(channel, runID string, sender Sender, message Message) {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	if err := sender.Send(ctx, message); err != nil {
		logging.Errorf("event=notification channel=%s run_id=%s status=failed error=%q", channel, runID, err)
		return
	}
	logging.Infof("event=notification channel=%s run_id=%s status=sent", channel, runID)
}

// pushoverSender 按配置构造 Pushover 发送器（工厂可注入）。
func (d *Dispatcher) pushoverSender(settings PushoverSettings) Sender {
	if d.pushoverFactory != nil {
		return d.pushoverFactory(settings)
	}
	return &PushoverSender{Token: settings.Token, UserKey: settings.UserKey}
}

// emailSender 按配置构造 SMTP 发送器（工厂可注入）。
func (d *Dispatcher) emailSender(settings EmailSettings) Sender {
	if d.emailFactory != nil {
		return d.emailFactory(settings)
	}
	return &EmailSender{Settings: settings}
}

// SendTest 用已保存的配置发送一条测试通知（API 测试端点入口）。
// 返回错误供 HTTP 层回显；渠道未启用或配置不完整返回 ErrNotConfigured。
// 测试消息与运行消息同一 formatter 产出，验证的是真实发送路径。
func (d *Dispatcher) SendTest(ctx context.Context, channel string) error {
	settings, err := d.service.Settings(ctx)
	if err != nil {
		return err
	}
	message := FormatTest()
	var sender Sender
	switch channel {
	case "pushover":
		if !settings.Pushover.Enabled || !settings.PushoverConfigured() {
			return ErrNotConfigured
		}
		sender = d.pushoverSender(settings.Pushover)
	case "email":
		if !settings.Email.Enabled || !settings.EmailConfigured() {
			return ErrNotConfigured
		}
		sender = d.emailSender(settings.Email)
	default:
		return &InvalidError{Field: "channel", Reason: "unknown channel"}
	}
	return sender.Send(ctx, message)
}
