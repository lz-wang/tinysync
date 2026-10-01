package notification

import (
	"strings"
	"testing"
	"time"

	"tinysync/internal/syncjob"
)

// completedRun 构造格式化测试的基准事件，子测试按需改写。
func completedRun() syncjob.RunCompletion {
	start := time.Unix(1760000000, 0).UTC()
	return syncjob.RunCompletion{
		RunID:     "run_abc123",
		JobID:     "job_1",
		JobName:   "photos",
		SourceID:  "src_1",
		Trigger:   syncjob.TriggerCron,
		State:     syncjob.RunSucceeded,
		StartedAt: start,
		// 92 秒后结束：耗时显示 1m32s。
		FinishedAt: start.Add(92 * time.Second),
		Stats: syncjob.RunStats{
			FilesTotal:       75,
			FilesCreated:     20,
			FilesUpdated:     5,
			FilesDeleted:     0,
			FilesSkipped:     50,
			BytesTransferred: 14.5 * 1024 * 1024,
		},
	}
}

func TestFormatRunSucceeded(t *testing.T) {
	message := FormatRun(completedRun())
	if message.Title != "TinySync · photos · 同步成功" {
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
	run.State = syncjob.RunFailed
	run.Error = "remote scan failed: 404 not found"
	message := FormatRun(run)
	if message.Title != "TinySync · photos · 同步失败" {
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
	run.State = syncjob.RunCanceled
	message := FormatRun(run)
	if message.Title != "TinySync · photos · 已取消" {
		t.Fatalf("Title = %q", message.Title)
	}
	if !strings.Contains(message.Body, "结果：已取消") {
		t.Errorf("Body 缺少取消结果；实际：\n%s", message.Body)
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
