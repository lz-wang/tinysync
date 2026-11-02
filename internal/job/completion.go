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
	// FailedItems 是本轮 status=failed 的运行明细快照（写入序，最多
	// FailedItemLimit 条）。Target / Error 是公共层语义，领域扩展字段
	// 保留在 Detail 原始 JSON 中——由消费者按 JobKind 自行解码，公共层
	// 不解释领域字段。
	FailedItems []FailedItem
	// FailedItemsTotal 是本轮失败明细总数（可能超过 FailedItems 携带的
	// 条数）；消费者据此渲染「另有 N 条失败未列出」之类的截断标注。
	FailedItemsTotal int
}

// FailedItemLimit 是完成事件携带失败明细的查询上限：通知正文只渲染
// 这么多行，更多失败由消费者按 FailedItemsTotal 渲染截断标注。
const FailedItemLimit = 20

// FailedItem 是完成事件携带的一条失败明细（run items 中 status=failed
// 的行）。Target 是统一的展示目标（各领域自定义），Detail 是领域扩展
// 字段的原始 JSON，Error 是失败原因。
type FailedItem struct {
	Target string
	Detail json.RawMessage
	Error  string
}

// RunCompletionHook 是运行完成的旁路消费者。实现必须立即返回：
// 运行的事实已经落库，任何下游耗时（网络发送等）不得拖住 Runner 的
// 终态收口与 activeRun 释放；需要异步处理的实现自行排队。
type RunCompletionHook interface {
	OnRunCompleted(RunCompletion)
}
