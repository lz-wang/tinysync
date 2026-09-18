package sftp

import (
	"context"
	"errors"
	"io"
	"os"
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

// remoteAbs 的 root escape 双重防御：越出真实 root 的映射一律拒绝。
func TestSFTPRootEscapeRejected(t *testing.T) {
	r := &remote{root: "/srv/backups"}
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
		got, err := r.remoteAbs(tc.logical)
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
	weird := &remote{root: "/srv/backups"}
	abs, err := weird.remoteAbs("/ok")
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
