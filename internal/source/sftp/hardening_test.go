package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// ctx 取消：取消后 List / Stat / Open 立即失败；Create 阶段注入的
// 守护 goroutine 会关闭 SSH 连接，阻塞中的传输随之退出。
func TestSFTPCancellationClosesConnection(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "a.txt", "data")
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	ctx, cancel := context.WithCancel(context.Background())
	factory := NewFactory()
	r, err := factory.Create(ctx, source.Source{
		Name:   "test",
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &cfg},
	}, source.Credentials{SFTP: &source.SFTPCredentials{Password: testPassword}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 取消前可用。
	if _, err := r.List(ctx, "/", source.ListOptions{}); err != nil {
		t.Fatalf("List before cancel: %v", err)
	}

	cancel()
	if _, err := r.List(ctx, "/", source.ListOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("List after cancel = %v, want context.Canceled", err)
	}
	if _, err := r.Stat(ctx, "/a.txt"); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat after cancel = %v, want context.Canceled", err)
	}
	if _, err := r.Open(ctx, "/a.txt"); !errors.Is(err, context.Canceled) {
		t.Errorf("Open after cancel = %v, want context.Canceled", err)
	}
	// 显式 Close 幂等收尾；失败路径也不应 panic。
	_ = r.Close()
}

// remoteAbs 的 root escape 双重防御：越出给定 root 的映射一律拒绝。
func TestSFTPRootEscapeRejected(t *testing.T) {
	const root = "/srv/backups"
	cases := []struct {
		logical string
		want    string
		wantEr  bool
	}{
		{logical: "/", want: "/srv/backups"},
		{logical: "/a.txt", want: "/srv/backups/a.txt"},
		{logical: "/x/b.txt", want: "/srv/backups/x/b.txt"},
	}
	for _, tc := range cases {
		got, err := remoteAbs(root, tc.logical)
		if tc.wantEr {
			if err == nil {
				t.Errorf("remoteAbs(%q) = %q, want error", tc.logical, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("remoteAbs(%q) = %q, %v; want %q", tc.logical, got, err, tc.want)
		}
	}

	// root 为 "/srv/backups" 时不存在能逃逸的 clean logical path：
	// 该防御分支保护 root 解析形态变化（如 RealPath 结果异常）时
	// 不产生越界访问，本身对合法输入透明。
	abs, err := remoteAbs(root, "/ok")
	if err != nil || abs != "/srv/backups/ok" {
		t.Errorf("remoteAbs(/ok) = %q, %v; want /srv/backups/ok", abs, err)
	}
}

// 并发 Open：同一 Remote 上多个并发读取互不干扰
// （MaxConcurrentTransfers > 1 场景）。
func TestSFTPConcurrentOpens(t *testing.T) {
	root := t.TempDir()
	const files = 6
	for i := 0; i < files; i++ {
		seedFile(t, root, "f"+string(rune('a'+i))+".txt", strings.Repeat("x", i+1))
	}
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})

	var wg sync.WaitGroup
	errs := make(chan error, files)
	for i := 0; i < files; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "/f" + string(rune('a'+i)) + ".txt"
			rc, err := r.Open(context.Background(), name)
			if err != nil {
				errs <- err
				return
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				errs <- err
				return
			}
			if len(data) != i+1 {
				errs <- errors.New(name + ": wrong length")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent open: %v", err)
	}
}

// RealPath 收敛：配置 root 为 symlink 时按服务器解析结果为基准，
// 映射与列表仍然正确。
func TestSFTPRootResolution(t *testing.T) {
	base := t.TempDir()
	seedFile(t, base, "in-root.txt", "v")
	link := base + "-link"
	if err := os.Symlink(base, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, link, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	page, err := r.List(context.Background(), "/", source.ListOptions{})
	if err != nil {
		t.Fatalf("List via symlinked root: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Path != "/in-root.txt" {
		t.Fatalf("entries = %+v, want /in-root.txt resolved through symlinked root", page.Entries)
	}
}

// transfer timeout 契约（v0.9）：Open 成功后服务端停摆 body 响应，
// attempt context 超时必须实际中断阻塞中的 Read。SFTP v1 无请求级
// 取消、File.Close 因内部互斥量无法中断 pending Read，中断只能经
// 连接拆除实现；拆除后下一次 Open 惰性重连，Downloader 的重试语义
// 因此成立（单次超时不终结整轮 run）。
func TestSFTPTransferTimeoutInterruptsStalledBody(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("stall-body-", 400)
	seedFile(t, root, "big.txt", content)
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})

	// attempt 1：Open 成功，随后服务端停摆 → Read 阻塞 → attempt
	// 超时（300ms）经连接拆除中断 Read。
	attemptCtx, cancelAttempt := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelAttempt()
	rc, err := r.Open(attemptCtx, "/big.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts.stall.Store(true)

	type readResult struct {
		n   int
		err error
	}
	readDone := make(chan readResult, 1)
	start := time.Now()
	go func() {
		n, readErr := rc.Read(make([]byte, 32))
		readDone <- readResult{n: n, err: readErr}
	}()
	var res readResult
	select {
	case res = <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stalled read still blocked after attempt timeout; transfer timeout is not enforced on SFTP body reads")
	}
	elapsed := time.Since(start)
	_ = rc.Close()
	if res.err == nil {
		t.Fatal("stalled read returned data, want error after attempt timeout")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("stalled read unblocked after %v, want prompt interrupt by the 300ms attempt timeout", elapsed)
	}

	// attempt 2（Downloader 重试形态）：恢复服务端响应 → 惰性重连 →
	// 完整读取成功，证明单次超时不终结后续传输。
	ts.stall.Store(false)
	rc2, err := r.Open(context.Background(), "/big.txt")
	if err != nil {
		t.Fatalf("Open after connection teardown: %v", err)
	}
	defer rc2.Close()
	data, err := io.ReadAll(rc2)
	if err != nil {
		t.Fatalf("read after reconnect: %v", err)
	}
	if string(data) != content {
		t.Error("content mismatch after reconnect")
	}
}

// readAllVia 打开 logicalPath 并读取全部内容（每次调用独立 Open）。
func readAllVia(t *testing.T, r source.Remote, logicalPath string) string {
	t.Helper()
	rc, err := r.Open(context.Background(), logicalPath)
	if err != nil {
		t.Fatalf("Open %s: %v", logicalPath, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll %s: %v", logicalPath, err)
	}
	return string(data)
}

// 重连必须重新解析 remote root：路径映射与连接代际绑定，绝不允许
// 「旧 root 算出的绝对路径打在新连接上」——否则远端 root 解析结果
// 在重连间隙变化时会静默读旧目录，resolved root confinement 语义
// 也随之失效。进程内 server 的 RealPath 是词法解析（不解析
// symlink），因此以重连间隙改变 rootCfg 模拟真实服务器的 root
// 重定向：新代际必须按新 root 解析，绝不能读到旧 root 下的同名文件。
func TestSFTPReconnectResolvesCurrentRoot(t *testing.T) {
	base := t.TempDir()
	dirA := filepath.Join(base, "A")
	dirB := filepath.Join(base, "B")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	seedFile(t, dirA, "f.txt", "content-A")
	seedFile(t, dirB, "f.txt", "content-B")
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, dirA, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	tr := r.(*remote)

	// 首次连接解析到 A。
	if got := readAllVia(t, r, "/f.txt"); got != "content-A" {
		t.Fatalf("content via initial root = %q, want content-A", got)
	}

	// 拆除连接，并在重连间隙切换 root 解析目标（等价于真实服务器上
	// remote_root symlink 目标变化）。
	tr.teardown()
	tr.mu.Lock()
	tr.rootCfg = dirB
	tr.mu.Unlock()

	// 重连后必须按新 root 解析：读到 B 的内容，绝不能是旧 root 的 A。
	if got := readAllVia(t, r, "/f.txt"); got != "content-B" {
		t.Fatalf("content after reconnect = %q, want content-B (stale root leaked)", got)
	}
}

// 超时隔离的并发语义（v0.9 收尾验证）：停滞传输（A）触发共享连接
// 拆除时，同连接上**正在途读取**的健康传输（B）必然被连带中断——
// 按 transient 重试必须收敛，健康文件不得因邻居的超时而最终失败。
// 与仅持有空闲句柄不同，这里 B 在拆除发生时确定性地阻塞在 Read 上
// （服务端停摆吞掉 B 的下一个响应）。若本测试不稳定或失败，才需要
// 考虑 per-transfer 独立 SSH 连接；当前设计只验证行为。
func TestSFTPTimeoutIsolationUnderConcurrentTransfers(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "stalled.txt", strings.Repeat("A-content-", 500))
	// 足够大，保证 B 无法在停摆置位前读完（置位只发生在相邻语句间，
	// B 需要数千次调度才能完成 8 MiB）。
	bContent := strings.Repeat("B-content-", 819200) // ~8 MiB
	seedFile(t, root, "healthy.txt", bContent)
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})

	// 停滞流 A 先建立：Open 在停摆置位前完成（其后读取才会被吞掉
	// 响应阻塞），300ms attempt 超时负责拆除连接。
	attemptCtx, cancelA := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelA()
	rcA, err := r.Open(attemptCtx, "/stalled.txt")
	if err != nil {
		t.Fatalf("A Open: %v", err)
	}
	defer rcA.Close()

	// 健康流 B：同一连接上建立并真正开始持续传输。
	rcB, err := r.Open(context.Background(), "/healthy.txt")
	if err != nil {
		t.Fatalf("B Open: %v", err)
	}
	defer func() { _ = rcB.Close() }()
	bDone := make(chan error, 1)
	go func() {
		_, berr := io.Copy(io.Discard, rcB)
		bDone <- berr
	}()

	// 服务端停摆：B 的下一个读取响应被吞掉 → B 确定性阻塞在在途
	// Read 上；A 的读取同样阻塞，直至 300ms 超时经连接拆除中断。
	ts.stall.Store(true)
	readA := make(chan error, 1)
	go func() {
		_, aerr := rcA.Read(make([]byte, 64))
		readA <- aerr
	}()
	select {
	case aerr := <-readA:
		if aerr == nil {
			t.Fatal("A stalled read returned data, want timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("A stalled read not interrupted by attempt timeout")
	}

	// B 的在途流随共享连接拆除被连带中断（transient 语义）。
	if berr := <-bDone; berr == nil {
		t.Fatal("in-flight B stream survived teardown unharmed; expected collateral transient interruption")
	}

	// 按 Downloader 的重试形态重新打开后必须完整收敛，内容与传输前
	// 一致——健康文件不因邻居超时而最终失败。
	ts.stall.Store(false)
	if got := readAllVia(t, r, "/healthy.txt"); got != bContent {
		t.Errorf("B content after retry = %d bytes, want full %d bytes", len(got), len(bContent))
	}
}

// 真实 Downloader（默认 maxAttempts=3、指数退避 + 有界 jitter）的
// retry 收敛：A 的 attempt 超时拆除共享连接时，B 的在途 attempt 被
// 连带打断（transient），Downloader 消耗重试预算后必须收敛成功——
// 健康文件不因邻居的超时而最终失败。
func TestSFTPDownloaderConvergesThroughCollateralInterruption(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "stalled.txt", strings.Repeat("A-content-", 500))
	bContent := strings.Repeat("B-content-", 819200) // ~8 MiB
	seedFile(t, root, "healthy.txt", bContent)
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})

	// A：Open 在停摆前完成，读取由停摆阻塞，300ms attempt 超时负责
	// 拆除连接。
	attemptCtx, cancelA := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelA()
	rcA, err := r.Open(attemptCtx, "/stalled.txt")
	if err != nil {
		t.Fatalf("A Open: %v", err)
	}
	defer func() { _ = rcA.Close() }()

	// B：真实 Downloader 下载（默认重试参数）。等其在途（临时文件
	// 出现）再停摆，保证拆除发生在 B 的 attempt 进行中。
	localRoot := t.TempDir()
	dlDone := make(chan error, 1)
	go func() {
		d := syncjob.NewDownloader(r)
		dlDone <- d.Download(context.Background(), "/healthy.txt", localRoot, "healthy.txt",
			source.Fingerprint{Size: int64(len(bContent))})
	}()
	waitForInFlightTransfer(t, localRoot, 5*time.Second)

	ts.stall.Store(true)
	readA := make(chan error, 1)
	go func() {
		_, aerr := rcA.Read(make([]byte, 64))
		readA <- aerr
	}()
	select {
	case aerr := <-readA:
		if aerr == nil {
			t.Fatal("A stalled read returned data, want timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("A stalled read not interrupted by attempt timeout")
	}

	// 恢复服务端响应：B 被连带打断后经真实重试收敛。
	ts.stall.Store(false)
	if derr := <-dlDone; derr != nil {
		t.Fatalf("Downloader did not converge after collateral interruption: %v", derr)
	}
	got, err := os.ReadFile(filepath.Join(localRoot, "healthy.txt"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != bContent {
		t.Errorf("downloaded content = %d bytes, want %d bytes", len(got), len(bContent))
	}
}

// waitForInFlightTransfer 轮询等待 Downloader 的在途临时文件出现
// （命名与 syncjob.Downloader 的内部 tempPrefix 形态一致）。
func waitForInFlightTransfer(t *testing.T, localRoot string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(localRoot)
		if err != nil {
			t.Fatalf("read local root: %v", err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".tinysync-part-") {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("downloader temp file did not appear in time; B attempt never went in-flight")
}

// stale 代际的迟到超时不得拆除重连后的新连接：超时回调与其文件
// 打开时的会话快照绑定（teardownSession(expected)），当前会话已
// 更替时回调必须是 no-op——否则旧 attempt 的 watcher 会拆掉新代际，
// 新代际上的重试再被下一个 stale watcher 拆掉，形成连锁误伤。
func TestSFTPStaleTimeoutSparesNewGeneration(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "stale.txt", "stale-content")
	seedFile(t, root, "fresh.txt", "fresh-content")
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	tr := r.(*remote)

	// 代际 1：打开后强制拆除，文件成为携带 stale watcher 的遗留句柄
	//（真实时序里它的超时回调因调度延迟晚于重连到达）。
	staleCtx, cancelStale := context.WithCancel(context.Background())
	defer cancelStale()
	rcStale, err := r.Open(staleCtx, "/stale.txt")
	if err != nil {
		t.Fatalf("stale Open: %v", err)
	}
	defer func() { _ = rcStale.Close() }()
	tr.teardown()

	// 重连产生代际 2 并确认可用，记下当前会话指针。
	if got := readAllVia(t, r, "/fresh.txt"); got != "fresh-content" {
		t.Fatalf("fresh generation read = %q, want fresh-content", got)
	}
	current, _, err := tr.session(context.Background())
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	// stale 文件的 ctx 取消触发其超时回调：只允许拆代际 1（已不
	// 存在），绝不能拆掉当前会话。
	cancelStale()
	time.Sleep(100 * time.Millisecond)

	after, _, err := tr.session(context.Background())
	if err != nil {
		t.Fatalf("session after stale callback: %v", err)
	}
	if after != current {
		t.Fatal("current generation was torn down by a stale timeout callback")
	}
	// 当前会话仍真实可用（不是仅指针未变）。
	if got := readAllVia(t, r, "/fresh.txt"); got != "fresh-content" {
		t.Fatalf("read after stale callback = %q, want fresh-content", got)
	}
}

// Close 与 ctx 取消的竞争不得依赖 select 的随机就绪顺序：文件已
// Close 成功后迟到的 ctx 取消绝不能触发共享连接 teardown——否则
// MaxConcurrentTransfers > 1 时，单个已完成的传输会因唤醒顺序随机
// 连带中断其它在途传输。循环多轮放大「stop 与 ctx.Done 同时就绪」
// 的竞争窗口。
func TestCtxFileCloseWinsOverLateCancellation(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "f.txt", "content")
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	tr := r.(*remote)

	current, _, err := tr.session(context.Background())
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		rc, oerr := r.Open(ctx, "/f.txt")
		if oerr != nil {
			t.Fatalf("open %d: %v", i, oerr)
		}
		if _, rerr := io.ReadAll(rc); rerr != nil {
			t.Fatalf("read %d: %v", i, rerr)
		}
		if cerr := rc.Close(); cerr != nil {
			t.Fatalf("close %d: %v", i, cerr)
		}
		cancel() // Close 成功之后的取消：不得拆除连接
		time.Sleep(20 * time.Millisecond)
	}
	after, _, err := tr.session(context.Background())
	if err != nil {
		t.Fatalf("session after close/cancel loop: %v", err)
	}
	if after != current {
		t.Fatal("connection was torn down by a late cancellation after successful Close")
	}
}

// Close 与 ctx 取消同时就绪的竞争窗口（白盒）：上面的黑盒循环里
// Close 的文件关闭往返给了守护 goroutine 先经 stop 退出的调度机会，
// 打不进真正的竞争；这里用 markClosed 复刻 Close 的状态序列并在同
// 一 goroutine 上背靠背 cancel——守护 goroutine 醒来时 stop 与
// ctx.Done 同时就绪，select 的随机选择绝不允许改变结果（Close 赢
// → 回调不触发）。f 为 nil 即可：该路径不触碰文件。
func TestCtxFileCloseStateSuppressesRacingCancellation(t *testing.T) {
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		var fired atomic.Bool
		c := newCtxFile(ctx, nil, func() { fired.Store(true) })
		c.markClosed()
		cancel()
		time.Sleep(20 * time.Millisecond)
		if fired.Load() {
			t.Fatalf("iteration %d: teardown fired although Close won the state race", i)
		}
	}
}

// 反向语义仍须成立：未 Close 时 ctx 取消必须真实触发回调（守护
// 状态机没有被改写成永不拆除）。
func TestCtxFileTimeoutStillFiresWithoutClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var fired atomic.Bool
	// 守护 goroutine 持有 ctxFile，无需保留返回值。
	newCtxFile(ctx, nil, func() { fired.Store(true) })
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for !fired.Load() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !fired.Load() {
		t.Fatal("ctx cancellation did not fire teardown; timeout path is broken")
	}
}

// 并发操作 + 反复 teardown：无 panic、无死锁，任何成功的返回都必须
// 与当前 root 的真实内容一致（不存在跨代际的错误组合）；压测结束后
// 惰性重连仍然可用。go test -race 下同时验证 root 读写无数据竞争。
func TestSFTPConcurrentOpsWithTeardown(t *testing.T) {
	base := t.TempDir()
	seedFile(t, base, "x.txt", "content-X")
	seedFile(t, base, "y.txt", "content-Y")
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, base, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	tr := r.(*remote)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	// teardown 扰动方：周期性拆除连接，模拟并发 attempt 超时。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				tr.teardown()
			}
		}
	}()
	// 操作方：路径 → 期望内容。
	expect := map[string]string{"/x.txt": "content-X", "/y.txt": "content-Y"}
	for path, want := range expect {
		wg.Add(1)
		go func(path, want string) {
			defer wg.Done()
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				select {
				case <-stop:
					return
				default:
				}
				rc, err := r.Open(context.Background(), path)
				if err != nil {
					continue // 并发 teardown 下的连接失败可接受
				}
				data, err := io.ReadAll(rc)
				_ = rc.Close()
				if err != nil {
					continue // 在途读取被拆除打断可接受
				}
				if string(data) != want {
					panic(fmt.Sprintf("concurrent read of %s = %q, want %q (cross-generation state)", path, data, want))
				}
			}
		}(path, want)
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()

	// 压测结束后远端仍可用（惰性重连生效）。
	if got := readAllVia(t, r, "/x.txt"); got != "content-X" {
		t.Fatalf("post-hammer read = %q, want content-X", got)
	}
}

// —— 取消归一化（真实 SFTP 阻塞边界）——
//
// Runner 以 errors.Is(runErr, context.Canceled) 把用户停止收敛为
// canceled。ctx 取消路径拆除 SSH 连接后，阻塞中的真实 SFTP 操作
// 返回的是拆除回声（connection lost / EOF / use of closed network
// connection），不是 context 错误——以下测试锁死「调用返回时 ctx
// 已取消 ⇒ 返回 context.Canceled」的边界契约，覆盖握手、扫描与
// 传输体三个阻塞点。

// startStalledServer 返回已置位 stall 的服务：接受 TCP 与 SSH 握手
// 请求，但服务端 → 客户端的数据被吞掉。
func startStalledServer(tb testing.TB) *testServer {
	tb.Helper()
	ts := startTestServer(tb)
	ts.stall.Store(true)
	return ts
}

// waitCond 轮询等待条件成立（最长 2s）：阻塞测试的进入时序对齐，
// 不依赖 sleep 猜测。
func waitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met in time: %s", what)
}

// stalled handshake：握手响应被吞 → Create 阻塞在 NewClientConn →
// cancel 后必须返回 context.Canceled（而非 handshake EOF）。
func TestSFTPStalledHandshakeCancelReturnsContextError(t *testing.T) {
	ts := startStalledServer(t)
	cfg := sftpSourceConfig(ts, "/", source.SFTPAuthPassword)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type createResult struct {
		remote source.Remote
		err    error
	}
	done := make(chan createResult, 1)
	go func() {
		r, err := NewFactory().Create(ctx, source.Source{
			Name:   "test",
			Type:   source.TypeSFTP,
			Config: source.Config{SFTP: &cfg},
		}, source.Credentials{SFTP: &source.SFTPCredentials{Password: testPassword}})
		done <- createResult{remote: r, err: err}
	}()
	// Create 阻塞在握手（服务器日志确认请求已受理），取消触发
	// dial 的连接拆除，握手错误归一为 ctx 错误。
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("Create after cancel = %v, want context.Canceled", res.err)
		}
		if res.remote != nil {
			_ = res.remote.Close()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Create still blocked after cancel")
	}
}

// stalled scan：ReadDir 响应被吞 → ScanTree 阻塞 → cancel 后必须
// 返回 context.Canceled（而非 wrapOp 包裹的连接丢失错误）。连接以
// 可取消 ctx 创建（生产形态：Factory.Create 的取消守护负责拆除
// 连接，中断阻塞中的 ReadDir）。
func TestSFTPStalledScanCancelReturnsContextError(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "docs/a.txt", "data")
	ts := startTestServer(t)
	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := NewFactory().Create(ctx, source.Source{
		Name:   "test",
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &cfg},
	}, source.Credentials{SFTP: &source.SFTPCredentials{Password: testPassword}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	scanner, ok := r.(source.TreeScanner)
	if !ok {
		t.Fatal("remote does not implement TreeScanner")
	}

	// 预热：会话已建立，stall 只吞 ReadDir 响应。
	if _, err := r.Stat(context.Background(), "/docs"); err != nil {
		t.Fatalf("warmup Stat: %v", err)
	}
	ts.stall.Store(true)

	scanDone := make(chan error, 1)
	go func() {
		scanDone <- scanner.ScanTree(ctx, "/", func(source.FileInfo) error { return nil })
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-scanDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ScanTree after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ScanTree still blocked after cancel")
	}
}

// stalled body：读取响应被吞 → Read 阻塞 → cancel 后必须返回
// context.Canceled，且 Downloader 层把该轮传输的最终错误归一为
// ctx 错误（Runner 据此记 canceled 而非 failed）。
func TestSFTPStalledBodyReadCancelReturnsContextError(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("cancel-body-", 400)
	seedFile(t, root, "big.txt", content)
	ts := startTestServer(t)
	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factory := NewFactory()
	r, err := factory.Create(ctx, source.Source{
		Name:   "test",
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &cfg},
	}, source.Credentials{SFTP: &source.SFTPCredentials{Password: testPassword}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rc, err := r.Open(ctx, "/big.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts.stall.Store(true)

	readDone := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(rc)
		_ = rc.Close()
		readDone <- readErr
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-readDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stalled read after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled read still blocked after cancel")
	}

	// Downloader 视角：整个下载在取消后必须以 ctx 错误终止——
	// Runner.finalize 的 canceled 判定直接依赖这一点。
	d := syncjob.NewDownloader(r)
	dlDone := make(chan error, 1)
	go func() {
		dlDone <- d.Download(ctx, "/big.txt", t.TempDir(), "big.txt", source.Fingerprint{Size: int64(len(content))})
	}()
	select {
	case err := <-dlDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Download after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Download still blocked after cancel")
	}
}
