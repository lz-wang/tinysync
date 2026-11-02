package syncjob

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"tinysync/internal/filesafe"
	"tinysync/internal/job"
	"tinysync/internal/source"
)

// memCreds 是 SourceGateway 的内存实现（File 执行器测试专用）。
type memCreds struct {
	source source.Source
	remote source.Remote
	// openErr 非 nil 时 OpenRemote 失败（模拟拨号 / 认证故障）。
	openErr error
}

func (m *memCreds) Get(ctx context.Context, id string) (source.Source, error) {
	if m.source.ID != id {
		return source.Source{}, source.ErrNotFound
	}
	return m.source, nil
}

func (m *memCreds) OpenRemote(ctx context.Context, id string) (source.Source, source.Remote, error) {
	if m.source.ID != id {
		return source.Source{}, nil, source.ErrNotFound
	}
	if m.openErr != nil {
		return source.Source{}, nil, m.openErr
	}
	return m.source, m.remote, nil
}

// webdavSource 返回启用的 WebDAV 测试 Source（执行器入参形态，凭据
// 由 memCreds 桩接管，不真实拨号）。
func webdavSource() source.Source {
	return source.Source{
		ID: "src_a", Name: "nas", Type: source.TypeWebDAV,
		Config: source.Config{WebDAV: &source.WebDAVConfig{
			Endpoint: "https://dav.example.com/",
		}},
		Enabled: true,
	}
}

// fileExecRequest 构造统一 Runner 的调用形态：领域配置快照 + 可选
// 明细与共享 limiter。repo 仅 Validate / LocalRoots 路径触达，Run 直跑
// 传 nil 即可。
func fileExecRequest(t *testing.T, jobID, localRoot string, mode Mode, transfers *TransferLimiter, items ItemRecorder) job.RunRequest {
	t.Helper()
	cfg, err := MarshalFileConfig("src_a", "/", localRoot, mode, nil, nil)
	if err != nil {
		t.Fatalf("marshal file config: %v", err)
	}
	return job.RunRequest{
		Job:       job.Job{ID: jobID, Kind: job.KindFile, Config: cfg},
		RunID:     "run_" + jobID,
		Items:     items,
		Transfers: transfers,
	}
}

// runSync 用独立的 managed 与 LocalRoot 直跑一轮同步（模拟一个独立
// 任务的执行器调用），返回文件统计。
func runSync(ctx context.Context, t *testing.T, remote source.Remote, localRoot string, transfers *TransferLimiter) (RunStats, error) {
	t.Helper()
	exec := NewFileExecutor(&memCreds{source: webdavSource(), remote: remote}, nil, newInMemoryManaged(), t.TempDir())
	res, err := exec.Run(ctx, fileExecRequest(t, "job_"+filepath.Base(localRoot), localRoot, ModeCopy, transfers, nil))
	if err != nil {
		return RunStats{}, err
	}
	var stats RunStats
	if len(res.Result) > 0 {
		if uerr := json.Unmarshal(res.Result, &stats); uerr != nil {
			t.Fatalf("parse run result: %v", uerr)
		}
	}
	return stats, nil
}

// closeTrackingRemote 包装 engineRemote 并记录 Close 调用次数。
type closeTrackingRemote struct {
	*engineRemote
	mu    sync.Mutex
	times int
}

func (c *closeTrackingRemote) Close() error {
	c.mu.Lock()
	c.times++
	c.mu.Unlock()
	return nil
}

func (c *closeTrackingRemote) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.times
}

// 运行结束后 Remote 必须被释放：有连接生命周期的协议（如 SFTP）
// 不遗留会话。
func TestFileExecutorClosesRemoteAfterRun(t *testing.T) {
	remote := &closeTrackingRemote{
		engineRemote: buildRemote(map[string]string{"/a.txt": "v1"}, nil),
	}
	exec := NewFileExecutor(&memCreds{source: webdavSource(), remote: remote}, nil, newInMemoryManaged(), t.TempDir())
	root := t.TempDir()

	res, err := exec.Run(context.Background(), fileExecRequest(t, "job_close", root, ModeCopy, NewTransferLimiter(1), nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var stats RunStats
	if err := json.Unmarshal(res.Result, &stats); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if stats.FilesCreated != 1 {
		t.Errorf("stats = %+v, want 1 created", stats)
	}
	if n := remote.closeCount(); n != 1 {
		t.Fatalf("remote close count = %d, want 1", n)
	}
}

// mapping 校验在 OpenRemote 之前：变更后的非法映射（重叠 / symlink
// 替换 / root 缺失）直接失败，不建立连接，Mirror 也不删除既有文件。
func TestFileExecutorRejectsChangedLocalMappingBeforeOpen(t *testing.T) {
	for _, change := range []string{"overlap", "source_symlink", "target_symlink", "missing"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			root, err := filesafe.CanonicalExistingDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			target, err := filesafe.CanonicalExistingDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			creds := &memCreds{
				source: source.Source{
					ID: "src_a", Name: "nas", Type: source.TypeLocal, Enabled: true,
					Config: source.Config{Local: &source.LocalConfig{Root: root}},
				},
				openErr: errorsNew("OpenRemote should not be reached"),
			}
			managed := newInMemoryManaged()
			exec := NewFileExecutor(creds, nil, managed, t.TempDir())
			cfg, err := MarshalFileConfig("src_a", "/", target, ModeMirror, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := source.ValidateJobMapping(creds.source, "/", target); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "overlap":
				creds.source.Config.Local.Root = target
			case "source_symlink":
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, root); err != nil {
					t.Skip(err)
				}
			case "target_symlink":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root, target); err != nil {
					t.Skip(err)
				}
			case "missing":
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
			}
			survivor := filepath.Join(target, "keep.txt")
			if err := os.WriteFile(survivor, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := managed.Upsert(ctx, []ManagedFile{{JobID: "job_map", RemotePath: "/keep.txt", LocalRelPath: "keep.txt", State: StateSynced}}); err != nil {
				t.Fatal(err)
			}
			if _, err := exec.Run(ctx, job.RunRequest{Job: job.Job{ID: "job_map", Kind: job.KindFile, Config: cfg}}); err == nil ||
				!strings.Contains(err.Error(), "validate job mapping") {
				t.Fatalf("Run err = %v, want validate job mapping failure", err)
			}
			if _, err := os.Stat(survivor); err != nil {
				t.Fatalf("mirror deleted survivor: %v", err)
			}
			kept, err := managed.ListByJob(ctx, "job_map")
			if err != nil || len(kept) != 1 {
				t.Fatalf("metadata changed: %v %v", kept, err)
			}
		})
	}
}

// 禁用的 Source 属于 File 领域运行期检查（已决 R7）：执行器 Run 直接
// 失败并携带 ErrSourceDisabled，统一 Runner 不理解 Source。
func TestFileExecutorSourceDisabledFailsRun(t *testing.T) {
	creds := &memCreds{source: webdavSource(), remote: buildRemote(map[string]string{"/a.txt": "v1"}, nil)}
	creds.source.Enabled = false
	exec := NewFileExecutor(creds, nil, newInMemoryManaged(), t.TempDir())

	if _, err := exec.Run(context.Background(), fileExecRequest(t, "job_disabled", t.TempDir(), ModeCopy, NewTransferLimiter(1), nil)); !errors.Is(err, ErrSourceDisabled) {
		t.Fatalf("Run with disabled source = %v, want ErrSourceDisabled", err)
	}
}

// hangReader 的 Read 阻塞到连接关闭：不感知 ctx——模拟 *sftp.File.Read
// 这类不接受 context 的底层 I/O，只有连接被关闭才能让读取返回。
type hangReader struct {
	closed <-chan struct{}
}

func (r *hangReader) Read([]byte) (int, error) {
	<-r.closed
	return 0, errors.New("connection closed during read")
}

func (r *hangReader) Close() error { return nil }

// hangRemote 模拟有连接生命周期协议（SFTP）的传输模型：Open 返回阻塞
// reader，Close 关闭连接使阻塞中的读取返回。
type hangRemote struct {
	opened chan struct{} // Open 已被调用（读取已进入阻塞）
	closed chan struct{} // 连接已关闭
}

func (h *hangRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, nil
}

func (h *hangRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{Entries: []source.FileInfo{{
		Path:        "/big.bin",
		Fingerprint: source.Fingerprint{Size: 1 << 20},
	}}}, nil
}

func (h *hangRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	select {
	case <-h.opened:
	default:
		close(h.opened)
	}
	return &hangReader{closed: h.closed}, nil
}

func (h *hangRemote) Close() error {
	select {
	case <-h.closed:
	default:
		close(h.closed)
	}
	return nil
}

// closingGateway 是感知 run 生命周期的 SourceGateway：OpenRemote 记录
// 收到的 ctx，并可阻塞模拟拨号；ctx 取消时触发连接关闭——与 SFTP
// Factory 内「ctx 取消关闭连接」的守护行为一致。
type closingGateway struct {
	src    source.Source
	remote *hangRemote
	// dial 非 nil 时 OpenRemote 阻塞直到放行或 ctx 取消（模拟拨号）。
	dial    <-chan struct{}
	mu      sync.Mutex
	openCtx context.Context
}

func (g *closingGateway) Get(ctx context.Context, id string) (source.Source, error) {
	return g.src, nil
}

func (g *closingGateway) OpenRemote(ctx context.Context, id string) (source.Source, source.Remote, error) {
	g.mu.Lock()
	g.openCtx = ctx
	g.mu.Unlock()
	if g.dial != nil {
		select {
		case <-g.dial:
		case <-ctx.Done():
			return source.Source{}, nil, ctx.Err()
		}
	}
	// 模拟 Factory 的关闭守护：创建用的 ctx 取消即关闭连接。
	go func() {
		<-ctx.Done()
		_ = g.remote.Close()
	}()
	return g.src, g.remote, nil
}

// 回归：取消必须能打断阻塞中的远端读取。执行器的连接关闭守护在
// ctx 取消时关闭连接，使不感知 ctx 的底层读取返回——统一 Runner 的
// runCtx（Shutdown / 用户停止的取消根）在生产路径传入同一链条。
func TestFileExecutorCancelInterruptsBlockingTransfer(t *testing.T) {
	remote := &hangRemote{opened: make(chan struct{}), closed: make(chan struct{})}
	gw := &closingGateway{src: webdavSource(), remote: remote}
	exec := NewFileExecutor(gw, nil, newInMemoryManaged(), t.TempDir())
	runCtx, cancel := context.WithCancelCause(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := exec.Run(runCtx, fileExecRequest(t, "job_hang", t.TempDir(), ModeCopy, NewTransferLimiter(1), nil))
		done <- err
	}()
	// 确认引擎已进入阻塞读取，取消面对的是真实的传输中连接。
	select {
	case <-remote.opened:
	case <-time.After(5 * time.Second):
		t.Fatal("engine never started reading the remote file")
	}

	cancel(nil)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run after cancel = nil, want error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not interrupt the blocking transfer")
	}

	// 取消传播到 Remote 创建（关闭守护）用的 context 本身。
	gw.mu.Lock()
	openCtx := gw.openCtx
	gw.mu.Unlock()
	if openCtx == nil || openCtx.Err() == nil {
		t.Error("OpenRemote ctx was not cancelled")
	}
}

// 回归：拨号中的启动阶段也必须可被取消。Remote 创建在执行器 Run
// 内、共用同一 ctx 取消链；创建失败以错误返回（统一 Runner 落库为
// failed run），不存在游离于生命周期之外的启动窗口。
func TestFileExecutorCancelInterruptsPendingOpenRemote(t *testing.T) {
	remote := &hangRemote{closed: make(chan struct{})}
	gw := &closingGateway{
		src:    webdavSource(),
		remote: remote,
		dial:   make(chan struct{}),
	}
	exec := NewFileExecutor(gw, nil, newInMemoryManaged(), t.TempDir())
	runCtx, cancel := context.WithCancelCause(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := exec.Run(runCtx, fileExecRequest(t, "job_dial", t.TempDir(), ModeCopy, NewTransferLimiter(1), nil))
		done <- err
	}()

	cancel(nil)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "create remote") {
			t.Fatalf("Run after cancel = %v, want create remote error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not interrupt pending OpenRemote")
	}
}

// MaxConcurrentTransfers 的全进程语义：多个任务直跑共享同一 limiter
// 时，全部下载的并发峰值不超过容量——每个任务各持一份限额的叠加
// （任务数 × 上限）必须被排除。
func TestTransferLimitSharedAcrossConcurrentRuns(t *testing.T) {
	files := map[string]string{}
	for _, name := range []string{"/a1", "/a2", "/a3", "/a4", "/a5", "/b1", "/b2", "/b3", "/b4", "/b5"} {
		files[name] = "content-of" + name
	}
	probe := &probeRemote{
		engineRemote: buildRemote(files, nil),
		delay:        40 * time.Millisecond,
	}
	limiter := NewTransferLimiter(3)

	type outcome struct {
		stats RunStats
		err   error
	}
	done := make(chan outcome, 2)
	for _, root := range []string{t.TempDir(), t.TempDir()} {
		go func() {
			stats, err := runSync(context.Background(), t, probe, root, limiter)
			done <- outcome{stats: stats, err: err}
		}()
	}
	for range 2 {
		res := <-done
		if res.err != nil {
			t.Fatalf("concurrent run failed: %v", res.err)
		}
		if res.stats.FilesCreated != 10 {
			t.Errorf("stats = %+v, want 10 created per run", res.stats)
		}
	}
	if probe.maxSeen > 3 {
		t.Errorf("process-wide concurrent Open peak = %d, want <= 3", probe.maxSeen)
	}
	if probe.maxSeen < 2 {
		t.Errorf("concurrent Open peak = %d, want cross-run overlap observed (>= 2)", probe.maxSeen)
	}
}

// waitForPending 轮询等待指定远端文件被派发（managed 登记 pending）。
func waitForPending(t *testing.T, m *inMemoryManaged, remotePath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		_, ok := m.files[remotePath]
		m.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s was never dispatched (no pending metadata)", remotePath)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// 用户手动取消时，被中断的在途下载在明细中如实记 canceled；取消前已
// 收敛的文件保持 succeeded；未派发的文件不留明细（append-only 审计）。
func TestRunRecordsCanceledItemOnUserCancel(t *testing.T) {
	f := newEngineFixture(t, ModeCopy)
	runCtx, cancelRun := context.WithCancelCause(context.Background())
	defer cancelRun(nil)

	remote := buildRemote(map[string]string{"/a.txt": "v1", "/b.bin": "big", "/c.txt": "v1"}, nil)
	remote.overrides["/b.bin"] = &cancelAwareReader{rel: make(chan struct{}), cancelled: make(chan struct{})}
	// buildRemote 以 map 迭代构造 entries，顺序不定；显式排序固定派发
	// 顺序：a.txt（成功收敛）→ b.bin（阻塞在途）→ c.txt（永不派发）。
	entries := remote.entries["/"]
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	items := &memItems{}
	done := make(chan error, 1)
	go func() {
		_, err := Run(runCtx, RunOptions{
			Remote:  remote,
			Job:     f.job,
			Managed: f.managed,
			Items:   items,
			RunID:   "run_cx",
			// 串行传输：a.txt 收敛后 b.bin 才派发，保证断言确定性。
			Transfers: NewTransferLimiter(1),
		})
		done <- err
	}()
	// b.bin 已派发（blocking read 生效中）→ 模拟用户手动停止。
	waitForPending(t, f.managed, "/b.bin")
	cancelRun(ErrRunCanceled)

	if err := <-done; err == nil {
		t.Fatal("Run after user cancel = nil, want cancellation error")
	}
	got := items.all()
	var succeededA, canceledB int
	for _, item := range got {
		switch item.Target {
		case "a.txt":
			if item.Status != ItemSucceeded {
				t.Errorf("a.txt item = %+v, want succeeded (already converged)", item)
			}
			succeededA++
		case "b.bin":
			if item.Status != ItemCanceled {
				t.Errorf("b.bin item = %+v, want canceled", item)
			}
			canceledB++
		case "c.txt":
			t.Errorf("c.txt should have no item (never dispatched), got %+v", item)
		}
	}
	if succeededA != 1 || canceledB != 1 {
		t.Errorf("items = (%d a.txt, %d b.bin), want (1, 1); all=%+v", succeededA, canceledB, got)
	}
}
