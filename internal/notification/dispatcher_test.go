package notification

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"tinysync/internal/syncjob"
)

// fakeSender 记录发送的消息，可按脚本失败。
type fakeSender struct {
	mu       sync.Mutex
	messages []Message
	err      error
}

func (f *fakeSender) Send(ctx context.Context, message Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, message)
	return f.err
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

// dispatcherEnv 装配带假发送工厂的 Dispatcher。
type dispatcherEnv struct {
	service  *Service
	repo     *fakeRepo
	pushover *fakeSender
	email    *fakeSender
	dispatch *Dispatcher
}

func newDispatcherEnv(t *testing.T, settings Settings) *dispatcherEnv {
	t.Helper()
	repo := &fakeRepo{settings: settings}
	env := &dispatcherEnv{
		service:  NewService(repo),
		repo:     repo,
		pushover: &fakeSender{},
		email:    &fakeSender{},
	}
	env.dispatch = NewDispatcher(env.service)
	env.dispatch.pushoverFactory = func(PushoverSettings) Sender { return env.pushover }
	env.dispatch.emailFactory = func(EmailSettings) Sender { return env.email }
	env.dispatch.Start()
	t.Cleanup(func() {
		_ = env.dispatch.Shutdown(context.Background())
	})
	return env
}

// enabledSettings 两渠道全启用。
func enabledSettings() Settings {
	settings := validSettings()
	settings.Pushover.Enabled = true
	settings.Email.Enabled = true
	return settings
}

func completedEvent() syncjob.RunCompletion {
	run := completedRun()
	return syncjob.RunCompletion{
		RunID:      run.RunID,
		JobID:      run.JobID,
		JobName:    run.JobName,
		SourceID:   run.SourceID,
		Trigger:    run.Trigger,
		State:      run.State,
		StartedAt:  run.StartedAt,
		FinishedAt: run.FinishedAt,
		Stats:      run.Stats,
		Error:      run.Error,
	}
}

// waitFor 轮询等待条件满足（事件分发在后台 worker 中异步完成）。
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within deadline")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestDispatcherSendsToEnabledChannels(t *testing.T) {
	env := newDispatcherEnv(t, enabledSettings())
	env.dispatch.OnRunCompleted(completedEvent())

	waitFor(t, func() bool { return env.pushover.count() == 1 && env.email.count() == 1 })
	// 共享 formatter：两个渠道收到同一消息。
	if env.pushover.messages[0] != env.email.messages[0] {
		t.Fatalf("两渠道消息不一致：%+v vs %+v", env.pushover.messages[0], env.email.messages[0])
	}
	if env.pushover.messages[0].Title == "" {
		t.Fatal("消息缺少标题")
	}
}

func TestDispatcherOnlyEnabledChannel(t *testing.T) {
	settings := enabledSettings()
	settings.Pushover.Enabled = false
	env := newDispatcherEnv(t, settings)
	env.dispatch.OnRunCompleted(completedEvent())

	waitFor(t, func() bool { return env.email.count() == 1 })
	if env.pushover.count() != 0 {
		t.Fatalf("pushover 发送 %d 条，want 0（未启用）", env.pushover.count())
	}
}

func TestDispatcherNoChannelsEnabled(t *testing.T) {
	env := newDispatcherEnv(t, Settings{})
	env.dispatch.OnRunCompleted(completedEvent())

	// 未启用任何渠道：静默丢弃。短暂等待后确认无发送。
	time.Sleep(50 * time.Millisecond)
	if env.pushover.count()+env.email.count() != 0 {
		t.Fatal("未启用渠道不应发送")
	}
}

func TestDispatcherSenderFailureDoesNotPanicOrBlock(t *testing.T) {
	env := newDispatcherEnv(t, enabledSettings())
	env.pushover.err = errors.New("pushover down")
	env.dispatch.OnRunCompleted(completedEvent())

	// pushover 失败不影响 email 发出。
	waitFor(t, func() bool { return env.email.count() == 1 })
	if env.pushover.count() != 1 {
		t.Fatalf("失败的发送器也应被调用一次，got %d", env.pushover.count())
	}
}

func TestDispatcherIgnoresSkippedCompletion(t *testing.T) {
	env := newDispatcherEnv(t, enabledSettings())
	event := completedEvent()
	event.State = syncjob.RunSkipped
	env.dispatch.OnRunCompleted(event)

	time.Sleep(50 * time.Millisecond)
	if env.pushover.count()+env.email.count() != 0 {
		t.Fatal("skipped 事件不应发送通知")
	}
}

func TestDispatcherShutdownDrainsQueue(t *testing.T) {
	repo := &fakeRepo{settings: enabledSettings()}
	pushover := &fakeSender{}
	service := NewService(repo)
	dispatch := NewDispatcher(service)
	dispatch.pushoverFactory = func(PushoverSettings) Sender { return pushover }
	dispatch.emailFactory = func(EmailSettings) Sender { return pushover }
	dispatch.Start()

	// Shutdown 与 worker 处理并发：排空后全部发出。
	const total = 8
	for i := 0; i < total; i++ {
		dispatch.OnRunCompleted(completedEvent())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dispatch.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := pushover.count(); got != total*2 {
		t.Fatalf("drain 后发送 %d 条（pushover+email × %d run），want %d", got, total, total*2)
	}
	// Shutdown 后再投递：静默丢弃，不 panic。
	dispatch.OnRunCompleted(completedEvent())
	if got := pushover.count(); got != total*2 {
		t.Fatalf("Shutdown 后仍发送：%d", got)
	}
}

func TestDispatcherOnRunCompletedDoesNotBlockWhenFull(t *testing.T) {
	repo := &fakeRepo{settings: enabledSettings()}
	sender := &fakeSender{}
	service := NewService(repo)
	dispatch := NewDispatcher(service)
	dispatch.pushoverFactory = func(PushoverSettings) Sender { return sender }
	dispatch.emailFactory = func(EmailSettings) Sender { return sender }
	// 不 Start：队列无人消费，必然填满，OnRunCompleted 仍须立即返回。
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < queueCapacity+10; i++ {
			dispatch.OnRunCompleted(completedEvent())
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("OnRunCompleted 在队列满时阻塞")
	}
	// 未 Start 的 Shutdown 不等待 worker，立即返回（队列内容丢弃）。
	if err := dispatch.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := sender.count(); got != 0 {
		t.Fatalf("未 Start 的 dispatcher 不应发送，got %d", got)
	}
}

// Shutdown 的 drain 超时触发后必须强制取消 worker 并等它真正退出：
// Shutdown 返回后绝不再访问 Service / SQLite / 网络。慢 sender 以
// ctx.Done 为唯一退出条件，验证取消信号确实穿透到发送路径。
func TestDispatcherShutdownCancelsStuckWorker(t *testing.T) {
	sender := &ctxBlockSender{
		entered:  make(chan struct{}),
		released: make(chan struct{}),
	}
	// 只启用 pushover：ctxBlockSender 的 close 标记只能触发一次。
	settings := enabledSettings()
	settings.Email.Enabled = false
	service := NewService(&fakeRepo{settings: settings})
	dispatch := NewDispatcher(service,
		WithPushoverSender(func(PushoverSettings) Sender { return sender }),
		WithEmailSender(func(EmailSettings) Sender { return sender }),
	)
	dispatch.Start()
	dispatch.OnRunCompleted(completedEvent())

	// 等 sender 进入阻塞。
	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sender 未开始发送")
	}

	// 已超时的 ctx：立即走强制取消路径。
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)
	if err := dispatch.Shutdown(expired); err == nil {
		t.Fatal("Shutdown with expired ctx = nil, want deadline error")
	}
	// worker 被 cancel：sender 的 Send 因 ctx 取消返回。
	select {
	case <-sender.released:
	case <-time.After(5 * time.Second):
		t.Fatal("worker cancel 未穿透到 sender，Shutdown 返回后仍在访问网络")
	}
}

// ctxBlockSender 阻塞直到其 ctx 取消，标记 entered 与 released 时刻。
type ctxBlockSender struct {
	entered  chan struct{}
	released chan struct{}
}

func (s *ctxBlockSender) Send(ctx context.Context, message Message) error {
	close(s.entered)
	<-ctx.Done()
	close(s.released)
	return ctx.Err()
}

func TestDispatcherSendTest(t *testing.T) {
	env := newDispatcherEnv(t, enabledSettings())
	if err := env.dispatch.SendTest(context.Background(), "pushover"); err != nil {
		t.Fatalf("SendTest pushover: %v", err)
	}
	if err := env.dispatch.SendTest(context.Background(), "email"); err != nil {
		t.Fatalf("SendTest email: %v", err)
	}
	waitFor(t, func() bool { return env.pushover.count() == 1 && env.email.count() == 1 })
	if !containsTitle(env.pushover.messages, "TinySync · 测试通知") {
		t.Fatalf("测试消息标题错误：%+v", env.pushover.messages)
	}

	// 未启用：ErrNotConfigured。
	disabled := enabledSettings()
	disabled.Pushover.Enabled = false
	env.repo.settings = disabled
	if err := env.dispatch.SendTest(context.Background(), "pushover"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SendTest 未启用渠道 = %v, want ErrNotConfigured", err)
	}
	// 未知渠道：invalid。
	if err := env.dispatch.SendTest(context.Background(), "sms"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SendTest 未知渠道 = %v, want ErrInvalid", err)
	}
}

func containsTitle(messages []Message, title string) bool {
	for _, m := range messages {
		if m.Title == title {
			return true
		}
	}
	return false
}
