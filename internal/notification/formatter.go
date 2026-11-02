package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tinysync/internal/job"
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

// FormatRun 把运行完成事件格式化为通知：共享骨架（任务/结果/触发/耗时
// + Run ID + 错误段）按 JobKind 分支渲染领域统计段。标题统一为
// 「[tinysync][领域段] 任务名」（R33）——运行状态不再进标题，由正文
// 的「结果」行承载。
func FormatRun(run job.RunCompletion) Message {
	title := fmt.Sprintf("[tinysync][%s] %s", kindLabel(run.JobKind), run.JobName)

	var b strings.Builder
	fmt.Fprintf(&b, "任务：%s\n", run.JobName)
	fmt.Fprintf(&b, "结果：%s\n", resultText(run))
	fmt.Fprintf(&b, "触发：%s\n", describeTrigger(run.Trigger))
	fmt.Fprintf(&b, "耗时：%s\n", formatDuration(run.FinishedAt.Sub(run.StartedAt)))
	b.WriteString("\n")
	// 领域段自带首尾布局（file 域与统一前逐字节一致，含空行位置）；
	// Run ID 紧随其后，不加额外空行。
	writeDomainSummary(&b, run)
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

// kindLabel 把任务类型映射为标题中的领域段文案（R33）。
func kindLabel(kind job.JobKind) string {
	switch kind {
	case job.KindFile:
		return "文件同步"
	case job.KindOCI:
		return "镜像同步"
	default:
		return string(kind)
	}
}

// resultText 把终态映射为「结果」行文案：默认按 run 终态；oci 领域
// 优先从 result_json 的领域状态推导（partial_success 等比 run 终态
// 更细，R29），status 缺失时在 ociStatusText 内按计数、run 终态逐级
// 回退。
func resultText(run job.RunCompletion) string {
	if run.JobKind == job.KindOCI {
		return ociStatusText(decodeOCISummary(run.Result), run.State)
	}
	return describeState(run.State)
}

// describeState 把运行终态映射为「结果」行文案。skipped 不是完成事件
// （第一版不通知），防御性兜底为原文。
func describeState(state job.RunState) string {
	switch state {
	case job.RunSucceeded:
		return "成功"
	case job.RunFailed:
		return "失败"
	case job.RunCanceled:
		return "已取消"
	default:
		return string(state)
	}
}

// writeDomainSummary 渲染领域结果摘要段：file 域保留统一前的文件统计
// 文案原样；oci 域为镜像复制摘要（R29）。未识别的类型按 file 域解释
// （与统一前行为一致）。
func writeDomainSummary(b *strings.Builder, run job.RunCompletion) {
	if run.JobKind == job.KindOCI {
		writeOCISummary(b, run)
		return
	}
	writeFileSummary(b, run)
}

// writeFileSummary 渲染 file 域统计段：行集合、顺序与空行位置与统一前
// 完全一致——六行统计、一个空行、再「同步数据量」（Run ID 紧随其后，
// 中间无空行，亦与统一前一致）。文案口径：
//   - 「文件变更」= 新增 + 更新 + 删除；relinquish 不改变本地文件，
//     不计入变更；
//   - BytesTransferred 统计成功同步文件的实际 payload 大小，不含重试
//     与协议开销，文案用「同步数据量」而非「网络流量」。
func writeFileSummary(b *strings.Builder, run job.RunCompletion) {
	var s syncjob.RunStats
	if len(run.Result) > 0 {
		_ = json.Unmarshal(run.Result, &s)
	}
	fmt.Fprintf(b, "文件总数：%d\n", s.FilesTotal)
	fmt.Fprintf(b, "文件变更：%d\n", s.FilesCreated+s.FilesUpdated+s.FilesDeleted)
	fmt.Fprintf(b, "新增：%d\n", s.FilesCreated)
	fmt.Fprintf(b, "更新：%d\n", s.FilesUpdated)
	fmt.Fprintf(b, "删除：%d\n", s.FilesDeleted)
	fmt.Fprintf(b, "跳过：%d\n", s.FilesSkipped)
	b.WriteString("\n")
	fmt.Fprintf(b, "同步数据量：%s\n", formatBytes(s.BytesTransferred))
}

// ociRunSummary 是 oci result_json 的领域形态（通知侧解码，与执行器的
// runResultSummary 同构；duration_ms 由共享骨架的 StartedAt/FinishedAt
// 耗时承载，正文不重复渲染）。
type ociRunSummary struct {
	Status           string `json:"status"`
	TotalImages      int    `json:"total_images"`
	SuccessCount     int    `json:"success_count"`
	SkippedCount     int    `json:"skipped_count"`
	FailureCount     int    `json:"failure_count"`
	TransferredBytes int64  `json:"transferred_bytes"`
}

// decodeOCISummary 解码 oci 结果摘要；无法解析时返回零值。
func decodeOCISummary(result json.RawMessage) ociRunSummary {
	var s ociRunSummary
	if len(result) > 0 {
		_ = json.Unmarshal(result, &s)
	}
	return s
}

// ociStatusText 从 result_json.status / 计数推导 oci 运行的结果文案
// （R29）：skipped（全部镜像已存在，无事可做）与成功同义；status 缺失
// （任务级错误先行失败、无摘要）或未知时按计数推导——部分成功 =
// 成功与失败混合；再退回 run 终态文案。
func ociStatusText(s ociRunSummary, state job.RunState) string {
	switch s.Status {
	case "success", "skipped":
		return "成功"
	case "partial_success":
		return "部分成功"
	case "failure":
		return "失败"
	case "cancelled":
		return "已取消"
	}
	switch {
	case s.FailureCount > 0 && s.SuccessCount > 0:
		return "部分成功"
	case s.FailureCount > 0:
		return "失败"
	}
	return describeState(state)
}

// writeOCISummary 渲染 oci 域统计段（R29）：计数行 + 失败明细行
// （最多 FailedItemLimit 条，超出渲染截断标注；成功不列明细）。段尾
// 以空行与 Run ID 行分隔。
func writeOCISummary(b *strings.Builder, run job.RunCompletion) {
	s := decodeOCISummary(run.Result)
	fmt.Fprintf(b, "总计：%d\n", s.TotalImages)
	fmt.Fprintf(b, "成功：%d\n", s.SuccessCount)
	fmt.Fprintf(b, "跳过：%d\n", s.SkippedCount)
	fmt.Fprintf(b, "失败：%d\n", s.FailureCount)
	fmt.Fprintf(b, "传输量：%s\n", formatBytes(s.TransferredBytes))
	writeOCIFailedItems(b, run)
	b.WriteString("\n")
}

// writeOCIFailedItems 渲染失败明细行「源 → 目标 ｜ 平台 ｜ 错误」，
// 行数以完成事件携带的 FailedItems（≤ job.FailedItemLimit）为上限；
// 恰好用尽限额且没有更多失败（FailedItemsTotal == 携带条数）时不渲染
// 截断标注。
func writeOCIFailedItems(b *strings.Builder, run job.RunCompletion) {
	if len(run.FailedItems) == 0 {
		return
	}
	b.WriteString("失败明细：\n")
	for _, item := range run.FailedItems {
		src, platform, errText := ociFailedItemFields(item)
		fmt.Fprintf(b, "%s → %s ｜ %s ｜ %s\n", src, item.Target, platform, errText)
	}
	if rest := run.FailedItemsTotal - len(run.FailedItems); rest > 0 {
		fmt.Fprintf(b, "…另有 %d 条失败未列出\n", rest)
	}
}

// ociItemDetail 是 oci 失败明细 Detail 原始 JSON 的领域形态（通知侧
// 解码，与执行器的 itemDetail 同构）。
type ociItemDetail struct {
	Src             string `json:"src"`
	PlatformResults []struct {
		Platform string `json:"platform"`
		Status   string `json:"status"`
		Error    string `json:"error"`
	} `json:"platform_results"`
}

// ociFailedItemFields 从失败明细解码 oci 领域字段：源镜像引用、失败
// 平台（platform_results 中 status=failed 的平台，逗号连接）与错误
// 文案（明细级 Error 优先，缺失时取首个失败平台的错误）。无平台信息
// 或错误文案时以 "-" 占位，保持行格式稳定；通知内容不含任何凭据。
func ociFailedItemFields(item job.FailedItem) (src, platform, errText string) {
	var d ociItemDetail
	if len(item.Detail) > 0 {
		_ = json.Unmarshal(item.Detail, &d)
	}
	var failedPlatforms []string
	for _, p := range d.PlatformResults {
		if p.Status == "failed" {
			failedPlatforms = append(failedPlatforms, p.Platform)
		}
	}
	if src = d.Src; src == "" {
		src = "-"
	}
	if platform = strings.Join(failedPlatforms, ", "); platform == "" {
		platform = "-"
	}
	errText = item.Error
	if errText == "" {
		for _, p := range d.PlatformResults {
			if p.Status == "failed" && p.Error != "" {
				errText = p.Error
				break
			}
		}
	}
	if errText == "" {
		errText = "-"
	}
	return src, platform, errText
}

// describeTrigger 把触发方式映射为中文文案。
func describeTrigger(trigger job.RunTrigger) string {
	switch trigger {
	case job.TriggerManual:
		return "手动"
	case job.TriggerOnce:
		return "一次性"
	case job.TriggerInterval:
		return "间隔"
	case job.TriggerCron:
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
