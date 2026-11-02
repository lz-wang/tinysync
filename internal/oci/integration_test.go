// Package oci_test 承载 OCI 领域的集成测试：真实引擎 + 内存 Registry v2
// （go-containerregistry/pkg/registry）+ 真实 sqlite 存储，不 mock 引擎
// 内部。覆盖 oci-sync 一直缺失的端到端缺口（P0）：单平台复制成功 →
// 二次运行 digest 跳过 → 断言 RunItem / result_json。
package oci_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	registrytest "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"tinysync/internal/job"
	"tinysync/internal/oci"
	ocisqlite "tinysync/internal/oci/sqlite"
	"tinysync/internal/storage"
)

// progressRecorder 记录执行器回报的进度事件（SetWorkTotal / BeginPair /
// EndPair / PairDone），断言 UnitProgress 语义的移植形态。
type progressRecorder struct {
	workTotal   int64
	beginUnits  []string
	endUnits    []string
	workDoneSum int64
	phases      []job.RunPhase
}

func (p *progressRecorder) SetPhase(phase job.RunPhase) { p.phases = append(p.phases, phase) }

func (p *progressRecorder) SetWorkTotal(total int64) { p.workTotal = total }

func (p *progressRecorder) AddWorkDone(n int64) { p.workDoneSum += n }

func (p *progressRecorder) BeginUnit(target string, action job.RunItemAction, totalBytes int64) *job.UnitProgress {
	p.beginUnits = append(p.beginUnits, target)
	return &job.UnitProgress{Target: target, Action: action, BytesTotal: totalBytes}
}

func (p *progressRecorder) EndUnit(target string) { p.endUnits = append(p.endUnits, target) }

// itemRecorder 收集执行器产出的运行明细。
type itemRecorder struct {
	items []job.RunItem
}

func (r *itemRecorder) RecordItem(_ context.Context, item job.RunItem) error {
	r.items = append(r.items, item)
	return nil
}

// integrationEnv 聚合集成测试环境：内存 registry + sqlite 服务 + 执行器。
type integrationEnv struct {
	host     string // registry 主机（host:port，plain HTTP）
	svc      *oci.Service
	executor *oci.OCIExecutor
}

// newIntegrationEnv 启动内存 Registry v2 并装配真实存储链路。strip
// 让目标路径剥离源前缀（避免 host:port 出现在仓库路径里——registry
// 仓库路径不允许冒号）。
func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	reg := registrytest.New(registrytest.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(reg)
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")

	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Ensure(context.Background(), db); err != nil {
		t.Fatalf("storage.Ensure: %v", err)
	}
	svc := oci.NewService(ocisqlite.New(db), nil)
	if _, err := svc.Create(context.Background(), oci.CreateInput{
		Name:            "local",
		Kind:            oci.KindPrimary,
		Host:            host,
		PlainHTTP:       true,
		StripRegistries: []string{host + "/upstream"},
	}); err != nil {
		t.Fatalf("create registry row: %v", err)
	}
	return &integrationEnv{
		host:     host,
		svc:      svc,
		executor: oci.NewOCIExecutor(svc, nil),
	}
}

// seedImage 向内存 registry 写入一个单平台随机镜像，返回摘要。
func (e *integrationEnv) seedImage(t *testing.T, ref string) string {
	t.Helper()
	img, err := random.Image(256, 2)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	tag, err := name.NewTag(ref, name.Insecure)
	if err != nil {
		t.Fatalf("NewTag(%s): %v", ref, err)
	}
	if err := remote.Write(tag, img); err != nil {
		t.Fatalf("remote.Write(%s): %v", ref, err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}
	return digest.String()
}

// runOnce 以给定配置执行一轮镜像同步。
func (e *integrationEnv) runOnce(t *testing.T, runID string, cfg json.RawMessage) (job.RunResult, error, *progressRecorder, *itemRecorder) {
	t.Helper()
	progress := &progressRecorder{}
	items := &itemRecorder{}
	result, err := e.executor.Run(context.Background(), job.RunRequest{
		Job:      job.Job{ID: "job_it", Name: "it", Kind: job.KindOCI, Enabled: true, Config: cfg},
		RunID:    runID,
		Progress: progress,
		Items:    items,
	})
	return result, err, progress, items
}

// 集成主链路：单平台镜像复制成功（RunItem / result_json / 进度）→
// 二次运行全图 HEAD digest 相同跳过（skipped）。
func TestIntegrationCopyThenDigestSkip(t *testing.T) {
	env := newIntegrationEnv(t)
	srcRef := env.host + "/upstream/app:1.0"
	dstRef := env.host + "/app:1.0"
	wantDigest := env.seedImage(t, srcRef)

	cfg := json.RawMessage(`{"src_images":["` + srcRef + `"],"dst_servers":["local"]}`)
	normalized, err := env.executor.Validate(context.Background(), "", cfg)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// 第一次运行：复制成功。
	result, runErr, progress, items := env.runOnce(t, "run_1", normalized)
	if runErr != nil {
		t.Fatalf("first run: %v", runErr)
	}
	var summary struct {
		Status           string `json:"status"`
		TotalImages      int    `json:"total_images"`
		SuccessCount     int    `json:"success_count"`
		SkippedCount     int    `json:"skipped_count"`
		FailureCount     int    `json:"failure_count"`
		TransferredBytes int64  `json:"transferred_bytes"`
	}
	if err := json.Unmarshal(result.Result, &summary); err != nil {
		t.Fatalf("unmarshal result_json: %v (%s)", err, result.Result)
	}
	if summary.Status != "success" || summary.TotalImages != 1 || summary.SuccessCount != 1 ||
		summary.SkippedCount != 0 || summary.FailureCount != 0 || summary.TransferredBytes == 0 {
		t.Fatalf("first run summary = %+v (%s)", summary, result.Result)
	}
	if len(items.items) != 1 {
		t.Fatalf("first run items = %d, want 1", len(items.items))
	}
	first := items.items[0]
	if first.RunID != "run_1" || first.Target != dstRef || first.Action != "copy" || first.Status != job.ItemSucceeded {
		t.Fatalf("first item = %+v, want copy/succeeded targeting %s", first, dstRef)
	}
	var detail struct {
		Src      string `json:"src"`
		Registry string `json:"registry"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(first.Detail, &detail); err != nil {
		t.Fatalf("unmarshal detail: %v (%s)", err, first.Detail)
	}
	if detail.Src != srcRef || detail.Registry != env.host || detail.Status != "success" {
		t.Fatalf("first detail = %+v (%s)", detail, first.Detail)
	}
	// 进度语义：工作总量 = 镜像数 × 目标数；在途单元以目标完整引用登记
	// 与注销；收敛计入完成数。
	if progress.workTotal != 1 || len(progress.beginUnits) != 1 || progress.beginUnits[0] != dstRef ||
		len(progress.endUnits) != 1 || progress.workDoneSum != 1 {
		t.Fatalf("first run progress = %+v", progress)
	}

	// 目标确实存在且 digest 与源一致。
	got, err := remote.Head(mustTag(t, dstRef))
	if err != nil {
		t.Fatalf("remote.Head(dst): %v", err)
	}
	if got.Digest.String() != wantDigest {
		t.Fatalf("dst digest = %s, want %s", got.Digest.String(), wantDigest)
	}

	// 第二次运行：全图 HEAD digest 相同 → skipped。
	result2, runErr2, progress2, items2 := env.runOnce(t, "run_2", normalized)
	if runErr2 != nil {
		t.Fatalf("second run: %v", runErr2)
	}
	var summary2 struct {
		Status       string `json:"status"`
		SuccessCount int    `json:"success_count"`
		SkippedCount int    `json:"skipped_count"`
		FailureCount int    `json:"failure_count"`
	}
	if err := json.Unmarshal(result2.Result, &summary2); err != nil {
		t.Fatalf("unmarshal second result_json: %v (%s)", err, result2.Result)
	}
	if summary2.Status != "skipped" || summary2.SuccessCount != 0 || summary2.SkippedCount != 1 || summary2.FailureCount != 0 {
		t.Fatalf("second run summary = %+v (%s)", summary2, result2.Result)
	}
	if len(items2.items) != 1 {
		t.Fatalf("second run items = %d, want 1", len(items2.items))
	}
	second := items2.items[0]
	if second.Target != dstRef || second.Action != "skip" || second.Status != job.ItemSkipped {
		t.Fatalf("second item = %+v, want skip/skipped", second)
	}
	if progress2.workDoneSum != 1 || len(progress2.beginUnits) != 1 {
		t.Fatalf("second run progress = %+v", progress2)
	}
}

// 目标在配置校验后被删除（dst_servers 运行期失配）时按对失败：
// result_json 计 failure，RunItem 记 failed，run 级错误摘要可查。
func TestIntegrationUnknownDestinationFails(t *testing.T) {
	env := newIntegrationEnv(t)
	srcRef := env.host + "/upstream/app:2.0"
	env.seedImage(t, srcRef)

	// 临时目标：Validate 通过后删除，模拟运行期配置失配。
	if _, err := env.svc.Create(context.Background(), oci.CreateInput{
		Name: "temp", Kind: oci.KindPrimary, Host: env.host, PlainHTTP: true,
	}); err != nil {
		t.Fatalf("create temp registry: %v", err)
	}
	cfg := json.RawMessage(`{"src_images":["` + srcRef + `"],"dst_servers":["temp"]}`)
	normalized, err := env.executor.Validate(context.Background(), "", cfg)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	registries, err := env.svc.List(context.Background())
	if err != nil {
		t.Fatalf("list registries: %v", err)
	}
	for _, row := range registries {
		if row.Name == "temp" {
			if err := env.svc.Delete(context.Background(), row.ID); err != nil {
				t.Fatalf("delete temp registry: %v", err)
			}
		}
	}

	result, runErr, _, items := env.runOnce(t, "run_x", normalized)
	if runErr == nil || !strings.Contains(runErr.Error(), "1 of 1") {
		t.Fatalf("run error = %v, want failure summary", runErr)
	}
	var summary struct {
		Status       string `json:"status"`
		FailureCount int    `json:"failure_count"`
	}
	if err := json.Unmarshal(result.Result, &summary); err != nil {
		t.Fatalf("unmarshal result_json: %v", err)
	}
	if summary.Status != "failure" || summary.FailureCount != 1 {
		t.Fatalf("summary = %+v (%s)", summary, result.Result)
	}
	if len(items.items) != 1 || items.items[0].Status != job.ItemFailed || items.items[0].Error == "" {
		t.Fatalf("items = %+v, want one failed item with error", items.items)
	}
}

// mustTag 解析 tag 引用（Insecure：内存 registry 走 plain HTTP）。
func mustTag(t *testing.T, ref string) name.Tag {
	t.Helper()
	tag, err := name.NewTag(ref, name.Insecure)
	if err != nil {
		t.Fatalf("NewTag(%s): %v", ref, err)
	}
	return tag
}
