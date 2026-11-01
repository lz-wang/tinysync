package syncjob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tinysync/internal/job"
	"tinysync/internal/logging"
	"tinysync/internal/source"
)

// initLogFileLogging 把全局 logger 指向临时目录的日志文件，返回
// 读取函数；测试结束重置为 stderr-only，避免污染其它测试。
func initLogFileLogging(t *testing.T) func() string {
	t.Helper()
	dataDir := t.TempDir()
	logging.Init(dataDir)
	t.Cleanup(func() { logging.Init("") })
	return func() string {
		_ = logging.Sync()
		data, err := os.ReadFile(filepath.Join(dataDir, "logs", "tinysync.log"))
		if err != nil {
			t.Fatalf("read log file: %v", err)
		}
		return string(data)
	}
}

// logJob 构造 finalize 直调所需的统一任务快照。
func logJob(id, name string) job.Job {
	return job.Job{ID: id, Name: name, Kind: job.KindFile}
}

// run 终态输出 event=sync_run 结构化事件：job_id / run_id /
// status / duration_ms / result / error。
func TestFinalizeEmitsSyncRunEvent(t *testing.T) {
	readLog := initLogFileLogging(t)

	r := NewRunner(newMemJobRepo(), newInMemoryManaged(), &memCreds{}, newMemRunRepo())
	run := &activeRun{
		runID:     "run_abc123",
		jobID:     "job_a",
		startedAt: time.Now().Add(-1500 * time.Millisecond),
	}
	r.finalize(context.Background(), run, logJob("job_a", "photos"),
		[]byte(`{"files_created":1,"bytes_transferred":42}`), errors.New("boom reason"))

	log := readLog()
	for _, want := range []string{
		"event=sync_run",
		"job_id=job_a",
		"run_id=run_abc123",
		"status=failed",
		`result={"files_created":1,"bytes_transferred":42}`,
		`error="boom reason"`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("sync_run log missing %q:\n%s", want, log)
		}
	}
}

// 成功终态同样输出事件（status=succeeded），且不含 error 内容。
func TestFinalizeEmitsSuccessEvent(t *testing.T) {
	readLog := initLogFileLogging(t)

	r := NewRunner(newMemJobRepo(), newInMemoryManaged(), &memCreds{}, newMemRunRepo())
	run := &activeRun{
		runID:     "run_ok42",
		jobID:     "job_ok",
		startedAt: time.Now().Add(-100 * time.Millisecond),
	}
	r.finalize(context.Background(), run, logJob("job_ok", "docs"), []byte(`{"files_updated":2,"bytes_transferred":7}`), nil)

	log := readLog()
	for _, want := range []string{"event=sync_run", "run_id=run_ok42", "status=succeeded", `"bytes_transferred":7`} {
		if !strings.Contains(log, want) {
			t.Errorf("sync_run log missing %q:\n%s", want, log)
		}
	}
}

// run row 落库成功即输出 status=running 事件：终态事件只发生在结束
// 时，进程硬崩溃后日志里没有「这个 run 曾经启动」的痕迹；running
// 与终态构成完整时间线，同一 run_id 两条 event=sync_run。
func TestStartEmitsRunningEvent(t *testing.T) {
	readLog := initLogFileLogging(t)

	repo := newMemJobRepo()
	cfg, err := MarshalFileConfig("src_start", "/", t.TempDir(), ModeCopy, nil, nil)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	j := job.Job{ID: "job_start", Name: "j", Kind: job.KindFile, Enabled: true, Config: cfg}
	if err := repo.Create(context.Background(), j); err != nil {
		t.Fatalf("create job: %v", err)
	}
	creds := &memCreds{
		source: source.Source{ID: "src_start", Type: source.TypeWebDAV, Enabled: true},
		remote: buildRemote(map[string]string{}, nil),
	}
	r := NewRunner(repo, newInMemoryManaged(), creds, newMemRunRepo())

	runID, err := r.start(context.Background(), "job_start", TriggerManual, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := r.Wait(context.Background(), runID); err != nil {
		t.Fatalf("wait: %v", err)
	}

	log := readLog()
	if !strings.Contains(log, "status=running") {
		t.Errorf("sync_run log missing start event status=running:\n%s", log)
	}
	if got := strings.Count(log, "event=sync_run"); got != 2 {
		t.Errorf("event=sync_run count = %d, want 2 (running + terminal):\n%s", got, log)
	}
}
