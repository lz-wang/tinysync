package notification

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/syncjob"
)

// Message 是一个渠道无关的通知：标题 + 纯文本正文。Pushover 与邮件
// 共用同一 formatter 产出，保证两个渠道的统计口径永远一致（ADR 0006）。
type Message struct {
	Title string
	Body  string
}

// Sender 是通知渠道的最小接口。实现负责自己的网络细节与超时；失败以
// error 返回，由调用方记结构化日志——发送失败绝不影响 run 终态。
type Sender interface {
	Send(ctx context.Context, message Message) error
}

// FormatRun 把运行完成事件格式化为通知。正文口径：
//   - 「文件变更」= 新增 + 更新 + 删除；relinquish 不改变本地文件，
//     不计入变更；
//   - BytesTransferred 统计成功同步文件的实际 payload 大小，不含重试
//     与协议开销，文案用「同步数据量」而非「网络流量」。
func FormatRun(run syncjob.RunCompletion) Message {
	stateText, titleState := describeState(run.State)
	title := fmt.Sprintf("TinySync · %s · %s", run.JobName, titleState)

	var b strings.Builder
	fmt.Fprintf(&b, "任务：%s\n", run.JobName)
	fmt.Fprintf(&b, "结果：%s\n", stateText)
	fmt.Fprintf(&b, "触发：%s\n", describeTrigger(run.Trigger))
	fmt.Fprintf(&b, "耗时：%s\n", formatDuration(run.FinishedAt.Sub(run.StartedAt)))
	b.WriteString("\n")
	s := run.Stats
	fmt.Fprintf(&b, "文件总数：%d\n", s.FilesTotal)
	fmt.Fprintf(&b, "文件变更：%d\n", s.FilesCreated+s.FilesUpdated+s.FilesDeleted)
	fmt.Fprintf(&b, "新增：%d\n", s.FilesCreated)
	fmt.Fprintf(&b, "更新：%d\n", s.FilesUpdated)
	fmt.Fprintf(&b, "删除：%d\n", s.FilesDeleted)
	fmt.Fprintf(&b, "跳过：%d\n", s.FilesSkipped)
	b.WriteString("\n")
	fmt.Fprintf(&b, "同步数据量：%s\n", formatBytes(s.BytesTransferred))
	fmt.Fprintf(&b, "Run ID：%s", run.RunID)
	if run.Error != "" {
		fmt.Fprintf(&b, "\n\n错误：\n%s", run.Error)
	}
	return Message{Title: title, Body: b.String()}
}

// FormatTest 是「发送测试通知」的消息：与运行通知同一 formatter 产出，
// 用于验证渠道配置，不含任何运行数据。
func FormatTest() Message {
	return Message{
		Title: "TinySync · 测试通知",
		Body:  "这是一条 TinySync 测试通知。收到它说明当前渠道配置可以正常发送。",
	}
}

// describeState 把运行终态映射为结果文案：正文与标题共用同一口径。
// skipped 不是完成事件（第一版不通知），防御性兜底为原文。
func describeState(state syncjob.RunState) (body, title string) {
	switch state {
	case syncjob.RunSucceeded:
		return "成功", "同步成功"
	case syncjob.RunFailed:
		return "失败", "同步失败"
	case syncjob.RunCanceled:
		return "已取消", "已取消"
	default:
		return string(state), string(state)
	}
}

// describeTrigger 把触发方式映射为中文文案。
func describeTrigger(trigger syncjob.RunTrigger) string {
	switch trigger {
	case syncjob.TriggerManual:
		return "手动"
	case syncjob.TriggerOnce:
		return "一次性"
	case syncjob.TriggerInterval:
		return "间隔"
	case syncjob.TriggerCron:
		return "Cron"
	default:
		return string(trigger)
	}
}

// formatDuration 输出人类易读的耗时（秒级取整）。
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

// formatBytes 输出二进制单位的同步数据量：B / KiB / MiB / GiB / TiB，
// 保留一位小数（B 为整数）。
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	labels := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(n)
	i := 0
	for value >= unit && i < len(labels)-1 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", value, labels[i])
}
