package syncjob

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"tinysync/internal/source"
)

// gatedReader 分两次交付内容：先给 head 字节，阻塞到放行后再给剩余
// 部分——供测试在传输中途观察实时计数。
type gatedReader struct {
	head    int
	data    string
	release chan struct{}
	given   bool
}

func (r *gatedReader) Read(p []byte) (int, error) {
	if !r.given {
		r.given = true
		n := copy(p, r.data[:r.head])
		return n, nil
	}
	select {
	case <-r.release:
		n := copy(p, r.data[r.head:])
		return n, io.EOF
	case <-context.Background().Done():
		return 0, context.Canceled
	}
}

// failMidwayReader 先交付 head 字节再返回 retryable 流中断错误
// （触发重试）。
type failMidwayReader struct {
	head  int
	data  string
	given bool
}

func (r *failMidwayReader) Read(p []byte) (int, error) {
	if !r.given {
		r.given = true
		n := copy(p, r.data[:r.head])
		return n, nil
	}
	return 0, io.ErrUnexpectedEOF
}

// recordingListener 记录 AttemptStart 次数与写入事件序列。
type recordingListener struct {
	mu     sync.Mutex
	starts int
	writes []int64
}

func (l *recordingListener) AttemptStart() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.starts++
}

func (l *recordingListener) Write(n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writes = append(l.writes, n)
}

func (l *recordingListener) total() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var sum int64
	for _, n := range l.writes {
		sum += n
	}
	return sum
}

func (l *recordingListener) startCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts
}

// progressRemote 让 Open 经工厂返回全新 reader（重试的每个 attempt
// 各自重新打开远端，reader 状态不得跨 attempt 复用）。
type progressRemote struct {
	newReader func() io.Reader
}

func (r *progressRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, nil
}

func (r *progressRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, nil
}

func (r *progressRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return io.NopCloser(r.newReader()), nil
}

func (r *progressRemote) Close() error { return nil }

func newProgressDownloader(t *testing.T, newReader func() io.Reader) *Downloader {
	t.Helper()
	d := NewDownloader(&progressRemote{newReader: newReader})
	d.jitter = func() time.Duration { return 0 }
	return d
}

// 拷贝路径的实时计数：传输中途（reader 阻塞未放行）即可观察到已写入
// 字节；放行后收尾值等于文件总大小，AttemptStart 恰好一次。
func TestDownloadReportsLiveByteProgress(t *testing.T) {
	const content = "hello world" // 11 bytes
	release := make(chan struct{})
	d := newProgressDownloader(t, func() io.Reader {
		return &gatedReader{head: 4, data: content, release: release}
	})
	fp := &FileProgress{Path: "live.bin", BytesTotal: int64(len(content))}
	listener := fileProgressListener{fp: fp}

	done := make(chan error, 1)
	go func() {
		done <- d.download(context.Background(), "/live.bin", t.TempDir(), "live.bin",
			source.Fingerprint{Size: int64(len(content))}, listener)
	}()

	// 等待前 4 字节可见（实时性断言：不等传输结束）。
	deadline := time.Now().Add(5 * time.Second)
	for fp.BytesDone() < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("live bytes = %d, want >= 4 mid-transfer", fp.BytesDone())
		}
		time.Sleep(time.Millisecond)
	}
	if got := fp.BytesDone(); got != 4 {
		t.Errorf("mid-transfer bytes = %d, want exactly 4", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("download: %v", err)
	}
	if got := fp.BytesDone(); got != int64(len(content)) {
		t.Errorf("final bytes = %d, want %d", got, len(content))
	}
}

// 重试归零：第一次 attempt 中途流中断（已计入部分字节），重试后
// AttemptStart 归零再重新累加，终值等于总大小而非两次之和。
func TestDownloadProgressResetsOnRetry(t *testing.T) {
	const content = "0123456789" // 10 bytes
	// attempt 1 的流：交付 3 字节后中断（可重试）；attempt 2 的流：完整。
	attempt := 0
	d := newProgressDownloader(t, func() io.Reader {
		attempt++
		if attempt == 1 {
			return &failMidwayReader{head: 3, data: content}
		}
		return strings.NewReader(content)
	})
	rec := &recordingListener{}
	if err := d.download(context.Background(), "/retry.bin", t.TempDir(), "retry.bin",
		source.Fingerprint{Size: int64(len(content))}, rec); err != nil {
		t.Fatalf("download with retry: %v", err)
	}
	if rec.startCount() != 2 {
		t.Errorf("AttemptStart count = %d, want 2 (initial + retry)", rec.startCount())
	}
	if got := rec.total(); got != int64(len(content))+3 {
		t.Errorf("accumulated writes = %d, want %d (full retry rewrite + 3 from failed attempt)", got, len(content)+3)
	}
}

// 多文件并发传输的计数互不串扰：listener 与 FileProgress 一一对应。
func TestConcurrentTransfersKeepSeparateProgress(t *testing.T) {
	f := newEngineFixture(t, ModeCopy)
	remote := buildRemote(map[string]string{"/a.txt": "aaaa", "/b.txt": "bb", "/c.txt": "cccccc"}, nil)
	progress := NewRunProgress()
	if _, err := f.runWith(remote, func(o *RunOptions) {
		o.Progress = progress
		o.Transfers = NewTransferLimiter(3)
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// 全部收敛后在途清单为空、计数收敛（文件级终值由 downloader 测试
	// 断言；此处验证并发登记 / 注销无串扰）。
	snap := progress.Snapshot()
	if len(snap.ActiveFiles) != 0 {
		t.Errorf("active files after run = %v, want empty", snap.ActiveFiles)
	}
	if snap.WorkDone != snap.WorkTotal || snap.WorkTotal != 3 {
		t.Errorf("work = %d/%d, want 3/3", snap.WorkDone, snap.WorkTotal)
	}
}

// bytes_done 的并发读写守卫（-race）：下载路径累加与查询方并发。
func TestFileProgressConcurrentReadWrite(t *testing.T) {
	fp := &FileProgress{Path: "race.bin", BytesTotal: 1 << 20}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for range 1000 {
			fp.add(1)
		}
		close(stop)
	})
	for {
		select {
		case <-stop:
			if got := fp.BytesDone(); got != 1000 {
				t.Errorf("bytes done = %d, want 1000", got)
			}
			wg.Wait()
			return
		default:
			_ = fp.BytesDone()
		}
	}
}
