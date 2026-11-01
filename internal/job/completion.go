package job

import (
	"encoding/json"
	"time"
)

// RunCompletion 是一轮运行终态成功落库后发布的完成事件快照（ADR 0006）。
// 事件自带 JobName 快照与领域结果，旁路消费者（完成通知）无需回读可能
// 已经变更的 Job 配置。只有真实执行过的运行才产生事件：succeeded /
// failed / canceled；skipped 是调度层未执行同步的记录，不属于「任务完成」。
type RunCompletion struct {
	RunID   string
	JobID   string
	JobKind JobKind
	JobName string
	Trigger RunTrigger

	State      RunState
	StartedAt  time.Time
	FinishedAt time.Time
	// Result 是领域结果摘要的原始 JSON（file 为文件统计、oci 为镜像
	// 复制摘要）；skipped / 未产生摘要的运行为 nil。
	Result json.RawMessage
	// Error 是失败原因的人类可读描述；成功时为空。
	Error string
}

// RunCompletionHook 是运行完成的旁路消费者。实现必须立即返回：
// 运行的事实已经落库，任何下游耗时（网络发送等）不得拖住 Runner 的
// 终态收口与 activeRun 释放；需要异步处理的实现自行排队。
type RunCompletionHook interface {
	OnRunCompleted(RunCompletion)
}
