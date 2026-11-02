package notification

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"tinysync/internal/job"
	"tinysync/internal/syncjob"
)

// completedRun 构造格式化测试的基准事件，子测试按需改写。
func completedRun() job.RunCompletion {
	start := time.Unix(1760000000, 0).UTC()
	stats := syncjob.RunStats{
		FilesTotal:       75,
		FilesCreated:     20,
		FilesUpdated:     5,
		FilesDeleted:     0,
		FilesSkipped:     50,
		BytesTransferred: 14.5 * 1024 * 1024,
	}
	result, _ := json.Marshal(stats)
	return job.RunCompletion{
		RunID:     "run_abc123",
		JobID:     "job_1",
		JobKind:   job.KindFile,
		JobName:   "photos",
		Trigger:   job.TriggerCron,
		State:     job.RunSucceeded,
		StartedAt: start,
		// 92 秒后结束：耗时显示 1m32s。
		FinishedAt: start.Add(92 * time.Second),
		Result:     result,
	}
}

func TestFormatRunSucceeded(t *testing.T) {
	message := FormatRun(completedRun())
	if message.Title != "[tinysync][文件同步] photos" {
		t.Fatalf("Title = %q", message.Title)
	}
	for _, want := range []string{
		"任务：photos",
		"结果：成功",
		"触发：Cron",
		"耗时：1m32s",
		"文件总数：75",
		"文件变更：25",
		"新增：20",
		"更新：5",
		"删除：0",
		"跳过：50",
		"同步数据量：14.5 MiB",
		"Run ID：run_abc123",
	} {
		if !strings.Contains(message.Body, want) {
			t.Errorf("Body 缺少 %q；实际：\n%s", want, message.Body)
		}
	}
	if strings.Contains(message.Body, "错误") {
		t.Error("成功通知不应包含错误段落")
	}
}

func TestFormatRunFailed(t *testing.T) {
	run := completedRun()
	run.State = job.RunFailed
	run.Error = "remote scan failed: 404 not found"
	message := FormatRun(run)
	if message.Title != "[tinysync][文件同步] photos" {
		t.Fatalf("Title = %q", message.Title)
	}
	if !strings.Contains(message.Body, "结果：失败") {
		t.Errorf("Body 缺少失败结果；实际：\n%s", message.Body)
	}
	if !strings.Contains(message.Body, "错误：\nremote scan failed: 404 not found") {
		t.Errorf("Body 缺少错误段落；实际：\n%s", message.Body)
	}
}

func TestFormatRunCanceled(t *testing.T) {
	run := completedRun()
	run.State = job.RunCanceled
	message := FormatRun(run)
	if message.Title != "[tinysync][文件同步] photos" {
		t.Fatalf("Title = %q", message.Title)
	}
	if !strings.Contains(message.Body, "结果：已取消") {
		t.Errorf("Body 缺少取消结果；实际：\n%s", message.Body)
	}
}

// ociRun 构造 oci 完成事件的基准：镜像同步任务名 cache，92 秒耗时与
// file 基准一致；子测试按需改写 Result 与 FailedItems。
func ociRun() job.RunCompletion {
	run := completedRun()
	run.JobKind = job.KindOCI
	run.JobName = "cache"
	run.Result = json.RawMessage(`{"status":"success","total_images":4,"success_count":3,"skipped_count":1,"failure_count":0,"transferred_bytes":1536}`)
	return run
}

// ociFailedItem 构造一条失败明细（Detail 为执行器 itemDetail 的领域
// 字段形态，target 已由公共层拆出）。
func ociFailedItem(target, src, platform, errText string) job.FailedItem {
	detail := map[string]any{
		"src":      src,
		"registry": "mirror.local",
	}
	if platform != "" || errText != "" {
		detail["platform_results"] = []map[string]string{{
			"platform": platform,
			"status":   "failed",
			"error":    errText,
		}}
	}
	raw, _ := json.Marshal(detail)
	return job.FailedItem{Target: target, Detail: raw, Error: errText}
}

// oci 成功：计数行齐全、无失败明细段，结果文案取领域状态。
func TestFormatRunOCISucceeded(t *testing.T) {
	message := FormatRun(ociRun())
	if message.Title != "[tinysync][镜像同步] cache" {
		t.Fatalf("Title = %q", message.Title)
	}
	for _, want := range []string{
		"任务：cache",
		"结果：成功",
		"触发：Cron",
		"耗时：1m32s",
		"总计：4",
		"成功：3",
		"跳过：1",
		"失败：0",
		"传输量：1.5 KiB",
		"Run ID：run_abc123",
	} {
		if !strings.Contains(message.Body, want) {
			t.Errorf("Body 缺少 %q；实际：\n%s", want, message.Body)
		}
	}
	if strings.Contains(message.Body, "失败明细") {
		t.Error("成功通知不应包含失败明细段")
	}
}

// oci 部分成功：领域状态推导出「部分成功」，失败明细行渲染
// 「源 → 目标 ｜ 平台 ｜ 错误」。
func TestFormatRunOCIPartialSuccess(t *testing.T) {
	run := ociRun()
	run.State = job.RunFailed
	run.Error = "1 of 4 image copies failed"
	run.Result = json.RawMessage(`{"status":"partial_success","total_images":4,"success_count":3,"skipped_count":0,"failure_count":1,"transferred_bytes":1024}`)
	run.FailedItems = []job.FailedItem{
		ociFailedItem("mirror.local/library/cache:1.27", "docker.io/library/cache:1.27", "linux/arm64", "failed to write image: connection reset"),
	}
	run.FailedItemsTotal = 1
	message := FormatRun(run)
	if !strings.Contains(message.Body, "结果：部分成功") {
		t.Errorf("Body 缺少部分成功结果；实际：\n%s", message.Body)
	}
	if !strings.Contains(message.Body, "失败明细：\ndocker.io/library/cache:1.27 → mirror.local/library/cache:1.27 ｜ linux/arm64 ｜ failed to write image: connection reset\n") {
		t.Errorf("Body 失败明细行不符合「源 → 目标 ｜ 平台 ｜ 错误」；实际：\n%s", message.Body)
	}
	if strings.Contains(message.Body, "另有") {
		t.Error("失败明细未截断时不应出现截断标注")
	}
	if strings.Contains(message.Body, "文件总数") {
		t.Error("oci 通知不应渲染文件统计")
	}
}

// oci 全部失败：结果文案为失败，逐条列出失败明细。
func TestFormatRunOCIAllFailed(t *testing.T) {
	run := ociRun()
	run.State = job.RunFailed
	run.Result = json.RawMessage(`{"status":"failure","total_images":2,"success_count":0,"skipped_count":0,"failure_count":3}`)
	run.FailedItems = []job.FailedItem{
		ociFailedItem("mirror.local/a:1", "docker.io/a:1", "linux/amd64", "denied"),
		ociFailedItem("mirror.local/b:1", "docker.io/b:1", "", ""),
		ociFailedItem("mirror.local/c:1", "docker.io/c:1", "linux/arm/v7", ""),
	}
	run.FailedItemsTotal = 3
	message := FormatRun(run)
	if !strings.Contains(message.Body, "结果：失败") {
		t.Errorf("Body 缺少失败结果；实际：\n%s", message.Body)
	}
	for _, want := range []string{
		"失败明细：",
		"docker.io/a:1 → mirror.local/a:1 ｜ linux/amd64 ｜ denied",
		// 无平台信息 / 明细级错误缺失时以 "-" 占位或回退平台错误。
		"docker.io/b:1 → mirror.local/b:1 ｜ - ｜ -",
		"docker.io/c:1 → mirror.local/c:1 ｜ linux/arm/v7 ｜ -",
	} {
		if !strings.Contains(message.Body, want) {
			t.Errorf("Body 缺少 %q；实际：\n%s", want, message.Body)
		}
	}
}

// oci 截断标注：携带条数达到上限且总数更多时渲染「另有 N 条失败未列
// 出」；恰好 20 条全部携带（上限边界、无更多失败）时不渲染。
func TestFormatRunOCIFailedItemsTruncation(t *testing.T) {
	run := ociRun()
	run.State = job.RunFailed
	run.Result = json.RawMessage(`{"status":"partial_success","total_images":25,"success_count":0,"skipped_count":0,"failure_count":25}`)
	items := make([]job.FailedItem, 0, job.FailedItemLimit)
	for i := 0; i < job.FailedItemLimit; i++ {
		target := fmt.Sprintf("mirror.local/img%d:1", i)
		items = append(items, ociFailedItem(target, "docker.io/img"+fmt.Sprint(i)+":1", "linux/amd64", "boom"))
	}
	run.FailedItems = items
	run.FailedItemsTotal = 25
	message := FormatRun(run)
	if !strings.Contains(message.Body, "…另有 5 条失败未列出\n") {
		t.Errorf("Body 缺少截断标注；实际：\n%s", message.Body)
	}

	// 上限边界：恰好 20 条失败全部携带（total == 携带条数），无标注。
	run.FailedItemsTotal = job.FailedItemLimit
	message = FormatRun(run)
	if strings.Contains(message.Body, "另有") {
		t.Errorf("恰好携带全部失败时不应渲染截断标注；实际：\n%s", message.Body)
	}
}

// oci 结果文案的推导次序：status 缺失时按计数推导（部分成功 = 成功与
// 失败混合）；status 与计数都不可用时退回 run 终态文案。
func TestFormatRunOCIStatusDerivation(t *testing.T) {
	tests := []struct {
		name   string
		result string
		state  job.RunState
		want   string
	}{
		{"status 缺失按计数推导部分成功", `{"total_images":3,"success_count":1,"failure_count":2}`, job.RunFailed, "部分成功"},
		{"status 缺失全失败按计数推导", `{"total_images":2,"failure_count":2}`, job.RunFailed, "失败"},
		{"status 与计数都缺失退回终态", `{}`, job.RunFailed, "失败"},
		{"status 与计数都缺失成功", `{}`, job.RunSucceeded, "成功"},
		{"cancelled 状态", `{"status":"cancelled"}`, job.RunCanceled, "已取消"},
		{"skipped（全部已存在）视为成功", `{"status":"skipped","skipped_count":3}`, job.RunSucceeded, "成功"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := ociRun()
			run.State = tt.state
			run.Result = json.RawMessage(tt.result)
			message := FormatRun(run)
			if want := "结果：" + tt.want; !strings.Contains(message.Body, want) {
				t.Errorf("Body 缺少 %q；实际：\n%s", want, message.Body)
			}
		})
	}
}

// file 域正文整份 golden：钉死六行统计 + 空行 + 同步数据量 + Run ID 的
// 精确拼接（含空行位置——「同步数据量」独立于统计行之后的空行，Run ID
// 紧随其后），布局漂移（strings.Contains 逐行断言不敏感）在此拦截。
func TestFormatRunFileBodyGolden(t *testing.T) {
	message := FormatRun(completedRun())
	want := "任务：photos\n" +
		"结果：成功\n" +
		"触发：Cron\n" +
		"耗时：1m32s\n" +
		"\n" +
		"文件总数：75\n" +
		"文件变更：25\n" +
		"新增：20\n" +
		"更新：5\n" +
		"删除：0\n" +
		"跳过：50\n" +
		"\n" +
		"同步数据量：14.5 MiB\n" +
		"Run ID：run_abc123"
	if message.Body != want {
		t.Fatalf("file 正文布局漂移：\n实际：\n%q\n期望：\n%q", message.Body, want)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{14.5 * 1024 * 1024, "14.5 MiB"},
		{3 * 1024 * 1024 * 1024, "3.0 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TiB"},
		// 超出 TiB 的值封顶在 TiB 尺度（5 TiB），不再进位。
		{5 * 1024 * 1024 * 1024 * 1024, "5.0 TiB"},
	}
	for _, tt := range tests {
		if got := formatBytes(tt.n); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestFormatTest(t *testing.T) {
	message := FormatTest()
	if message.Title == "" || message.Body == "" {
		t.Fatalf("FormatTest() = %+v, want non-empty", message)
	}
}
