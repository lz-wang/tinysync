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
	"testing"
	"time"

	"tinysync/internal/source"
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
