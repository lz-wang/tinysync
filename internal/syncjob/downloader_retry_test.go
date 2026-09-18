package syncjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tinysync/internal/source"
)

// countingRemote 统计 Open 次数，可按序列返回错误。
type countingRemote struct {
	contents map[string]io.ReadCloser
	errSeq   []error
	opens    atomic.Int64
}

func (r *countingRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (r *countingRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, errors.New("not implemented")
}

func (r *countingRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	n := r.opens.Add(1)
	if int(n) <= len(r.errSeq) {
		return nil, r.errSeq[n-1]
	}
	if rc, ok := r.contents[path]; ok {
		return rc, nil
	}
	return nil, fmt.Errorf("no such remote file %s", path)
}

func (r *countingRemote) Close() error { return nil }

// timeoutRemote 的 Open 阻塞至 ctx 结束：用于触发单次 attempt 超时。
// blockSeq 依次决定每次调用是否阻塞，序列耗尽后返回内容。
type timeoutRemote struct {
	contents map[string]io.ReadCloser
	blockSeq []bool
	opens    atomic.Int64
}

func (r *timeoutRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (r *timeoutRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, errors.New("not implemented")
}

func (r *timeoutRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	n := r.opens.Add(1)
	if int(n) <= len(r.blockSeq) && r.blockSeq[n-1] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if rc, ok := r.contents[path]; ok {
		return rc, nil
	}
	return nil, fmt.Errorf("no such remote file %s", path)
}

func (r *timeoutRemote) Close() error { return nil }

// permanent error 不做无意义重试：单次尝试后立即失败。
func TestDownloadDoesNotRetryPermanentFailure(t *testing.T) {
	root := t.TempDir()
	remote := &countingRemote{
		errSeq: []error{source.MarkPermanent(errors.New("401 unauthorized"))},
	}
	d := newTestDownloader(remote)

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 2})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Download = %v, want permanent error", err)
	}
	if got := remote.opens.Load(); got != 1 {
		t.Errorf("open attempts = %d, want 1 (permanent errors must not retry)", got)
	}
}

// size mismatch（确定性失败）同样不重试：下一轮 run 经 pending
// metadata 重新传输收敛。
func TestDownloadDoesNotRetrySizeMismatch(t *testing.T) {
	root := t.TempDir()
	remote := &countingRemote{
		contents: map[string]io.ReadCloser{
			"/docs/a.txt": io.NopCloser(strings.NewReader("short")),
		},
	}
	d := newTestDownloader(remote)

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 100})
	if err == nil {
		t.Fatal("Download size mismatch = nil, want error")
	}
	if got := remote.opens.Load(); got != 1 {
		t.Errorf("open attempts = %d, want 1", got)
	}
	assertNoTempFiles(t, root)
}

// 单次 attempt 超时可重试：第一次阻塞至超时，第二次成功落地。
func TestDownloadRetriesAfterAttemptTimeout(t *testing.T) {
	root := t.TempDir()
	remote := &timeoutRemote{
		contents: map[string]io.ReadCloser{
			"/docs/a.txt": io.NopCloser(strings.NewReader("after-timeout")),
		},
		blockSeq: []bool{true},
	}
	d := newTestDownloader(remote)
	d.timeout = 60 * time.Millisecond

	if err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 13}); err != nil {
		t.Fatalf("Download after attempt timeout: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "a.txt"))
	if err != nil || string(data) != "after-timeout" {
		t.Errorf("content = %q (%v), want after-timeout", data, err)
	}
	if got := remote.opens.Load(); got != 2 {
		t.Errorf("open attempts = %d, want 2", got)
	}
}

// attempt 持续超时：重试耗尽后失败，目标不落地、无临时文件残留。
func TestDownloadFailsAfterExhaustedTimeouts(t *testing.T) {
	root := t.TempDir()
	remote := &timeoutRemote{
		blockSeq: []bool{true, true, true},
	}
	d := newTestDownloader(remote)
	d.timeout = 40 * time.Millisecond

	err := d.Download(context.Background(), "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{})
	if err == nil {
		t.Fatal("Download with persistent timeout = nil, want error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want DeadlineExceeded", err)
	}
	if got := remote.opens.Load(); got != 3 {
		t.Errorf("open attempts = %d, want 3", got)
	}
	assertNoTempFiles(t, root)
}

// 默认退避序列：attempt 1 → 250ms、attempt 2 → 500ms（指数退避）。
func TestDefaultBackoffSequence(t *testing.T) {
	d := NewDownloader(&countingRemote{})
	cases := map[int]time.Duration{
		1: 250 * time.Millisecond,
		2: 500 * time.Millisecond,
		3: 1000 * time.Millisecond,
	}
	for attempt, want := range cases {
		if got := d.backoff(attempt); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

// 有界 jitter 注入：确定性序列参与重试等待（与零 backoff 组合后
// 通过 select 等待时间生效；这里只验证注入被消费、范围正确）。
func TestDefaultJitterBounded(t *testing.T) {
	for i := 0; i < 200; i++ {
		j := defaultJitter()
		if j < 0 || j >= maxJitter {
			t.Fatalf("jitter %v out of range [0, %v)", j, maxJitter)
		}
	}
}

// cancelProbeRemote 用 channel 同步 attempt 生命周期：第一次 Open
// 阻塞至 attempt 超时；第二次 Open 开始时发出信号并阻塞至 ctx 结束。
type cancelProbeRemote struct {
	contents map[string]io.ReadCloser
	opens    atomic.Int64
	// entered2 在第二次 Open 进入阻塞前关闭。
	entered2 chan struct{}
}

func (r *cancelProbeRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (r *cancelProbeRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, errors.New("not implemented")
}

func (r *cancelProbeRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	switch r.opens.Add(1) {
	case 1:
		<-ctx.Done() // attempt 自身超时
		return nil, ctx.Err()
	default:
		close(r.entered2)
		<-ctx.Done() // 阻塞至 run 取消或 attempt 超时
		return nil, ctx.Err()
	}
}

func (r *cancelProbeRemote) Close() error { return nil }

// run 级取消在第二次 attempt 进行中生效：立即返回 Canceled，不进入
// 第三次重试（确定性时序：channel 同步，不依赖 sleep）。
func TestDownloadStopsOnRunCancelDuringTimeoutRetry(t *testing.T) {
	root := t.TempDir()
	remote := &cancelProbeRemote{
		contents: map[string]io.ReadCloser{
			"/docs/a.txt": io.NopCloser(strings.NewReader("x")),
		},
		entered2: make(chan struct{}),
	}
	d := newTestDownloader(remote)
	// attempt1 经自身超时结束（40ms）；attempt2 的 deadline 在 80ms
	// 之后，test 在 entered2 信号后立刻取消 run——取消必然先于
	// attempt2 自身超时。
	d.timeout = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-remote.entered2 // 第二次 attempt 已在阻塞读取中
		cancel()
	}()
	err := d.Download(ctx, "/docs/a.txt", root, "docs/a.txt", source.Fingerprint{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want Canceled (run cancel wins)", err)
	}
	if got := remote.opens.Load(); got != 2 {
		t.Errorf("open attempts = %d, want 2 (no third retry after cancel)", got)
	}
}
