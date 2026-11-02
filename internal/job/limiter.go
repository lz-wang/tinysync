package job

import "context"

// TransferLimiter 是全进程并发传输的上限：由 Runner 持有单例并注入
// 每轮运行的执行器，保证 MaxConcurrentTransfers 约束的是整个进程
// 同时进行的传输总和，而不是每个任务各自的独立额度。
type TransferLimiter struct {
	sem chan struct{}
}

// NewTransferLimiter 构造上限为 maxConcurrent 的传输 limiter；
// 小于 1 时收敛为 1（无并发的下限仍需成立）。
func NewTransferLimiter(maxConcurrent int) *TransferLimiter {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &TransferLimiter{sem: make(chan struct{}, maxConcurrent)}
}

// Capacity 返回允许的最大并发传输数。
func (l *TransferLimiter) Capacity() int {
	return cap(l.sem)
}

// Acquire 占用一个传输名额；ctx 先取消时返回 ctx 错误且不占用名额。
func (l *TransferLimiter) Acquire(ctx context.Context) error {
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release 释放一个传输名额；必须与已成功的 Acquire 一一配对。
func (l *TransferLimiter) Release() {
	<-l.sem
}
