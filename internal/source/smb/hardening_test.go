package smb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
)

// connQueue 让 connect 逐个发放连接：第 i 次 dial 返回第 i 个 fake，
// 模拟「坏连接拆除 → 重试建立新连接」的代际序列。
type connQueue struct {
	conns []*fakeConn
	next  atomic.Int64
}

func (q *connQueue) dial(ctx context.Context) (conn, error) {
	i := int(q.next.Add(1)) - 1
	if i >= len(q.conns) {
		return nil, fmt.Errorf("unexpected dial #%d", i+1)
	}
	return q.conns[i], nil
}

func (q *connQueue) dials() int { return int(q.next.Load()) }

// transport 故障后的重连链路：操作遇到 transport 错误 → 所属代际被拆
// 除 → 下一次操作经 session 重连取得新代际并成功——Downloader 的
// 重试因此真正建立新 SMB 连接，而不是反复使用坏连接（硬性验收 #7）。
func TestTransportFailureReconnects(t *testing.T) {
	c1, c2 := newFakeConn(), newFakeConn()
	for _, c := range []*fakeConn{c1, c2} {
		c.seed("", fakeEntry{name: "a.txt", size: 4})
		c.writeFile(`a.txt`, "data")
	}
	q := &connQueue{conns: []*fakeConn{c1, c2}}
	r := newFakeRemote(t, q.dial)
	ctx := context.Background()

	// 代际 1 正常工作。
	if _, err := r.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("Stat on generation 1: %v", err)
	}
	// 连接死亡：下一次操作失败并拆除代际 1。
	c1.kill()
	if _, err := r.Stat(ctx, "/a.txt"); err == nil {
		t.Fatal("Stat on dead connection = nil, want error")
	}
	if c1.closeCount != 1 {
		t.Fatalf("generation 1 closeCount = %d, want 1 (torn down)", c1.closeCount)
	}
	// 重试形态：下一次操作重连（代际 2）并成功。
	if _, err := r.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("Stat after reconnect: %v", err)
	}
	if q.dials() != 2 {
		t.Errorf("dials = %d, want 2", q.dials())
	}
	if c1.closeCount != 1 {
		t.Errorf("generation 1 closeCount = %d after reconnect, want stable 1", c1.closeCount)
	}
	// 下载路径同样恢复。
	rc, err := r.Open(ctx, "/a.txt")
	if err != nil {
		t.Fatalf("Open after reconnect: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(data) != "data" {
		t.Errorf("read after reconnect = %q, %v", data, err)
	}
}

// permanent 错误（认证 / 权限 / 不存在）不拆除连接：同连接上的其它
// 操作不受邻居的确定性失败连带中断。
func TestPermanentFailureSparesConnection(t *testing.T) {
	c := newFakeConn()
	c.seed("", fakeEntry{name: "a.txt", size: 4})
	c.writeFile(`a.txt`, "data")
	q := &connQueue{conns: []*fakeConn{c}}
	r := newFakeRemote(t, q.dial)
	ctx := context.Background()

	// 不存在：permanent 语义（IsRetryable false），不拆连接。
	if _, err := r.Stat(ctx, "/missing.txt"); err == nil {
		t.Fatal("Stat missing = nil")
	}
	if c.closeCount != 0 {
		t.Fatalf("closeCount = %d after not-exist, want 0", c.closeCount)
	}
	// 同一连接继续可用。
	if _, err := r.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("Stat after neighbor not-exist: %v", err)
	}
	if q.dials() != 1 {
		t.Errorf("dials = %d, want 1 (no reconnect on permanent failure)", q.dials())
	}
}

// stale 代际的迟到 teardown 不得拆除重连后的新连接：故障回报与其
// 操作发生时的代际绑定（teardownSession(expected)），当前连接已更替
// 时必须是 no-op。
func TestStaleTeardownSparesNewGeneration(t *testing.T) {
	c1, c2 := newFakeConn(), newFakeConn()
	for _, c := range []*fakeConn{c1, c2} {
		c.seed("", fakeEntry{name: "a.txt", size: 4})
		c.writeFile(`a.txt`, "data")
	}
	q := &connQueue{conns: []*fakeConn{c1, c2}}
	r := newFakeRemote(t, q.dial)
	ctx := context.Background()

	// 建立代际 1 后死亡；显式模拟「迟到的故障回报」。
	if _, err := r.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("warmup Stat on generation 1: %v", err)
	}
	r.mu.RLock()
	gen1 := r.conn
	r.mu.RUnlock()
	if gen1 == nil || gen1 != conn(c1) {
		t.Fatal("generation 1 not established")
	}
	c1.kill()
	if _, err := r.Stat(ctx, "/a.txt"); err == nil {
		t.Fatal("Stat on dead generation = nil")
	}
	// 此时连接已拆；重连产生代际 2。
	if _, err := r.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("Stat on generation 2: %v", err)
	}
	// 迟到的 stale teardown（针对代际 1）：no-op，不碰代际 2。
	r.teardownSession(gen1)
	if _, err := r.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("Stat after stale teardown: %v", err)
	}
	if c2.closeCount != 0 {
		t.Errorf("generation 2 closeCount = %d, want 0", c2.closeCount)
	}
}

// Close 幂等且为终态：多次 Close 不报错；closed 后 session 不再重连。
func TestCloseIdempotentAndFinal(t *testing.T) {
	c := newFakeConn()
	c.seed("", fakeEntry{name: "a.txt", size: 4})
	q := &connQueue{conns: []*fakeConn{c}}
	r := newFakeRemote(t, q.dial)
	ctx := context.Background()

	// 建立连接后再 Close：连接被真实拆除。
	if _, err := r.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("warmup Stat: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if c.closeCount != 1 {
		t.Errorf("closeCount = %d, want 1 (idempotent)", c.closeCount)
	}
	// 终态：连接已清空，再 Close 是 no-op；操作返回 closed 错误且不
	// 触发重连。
	if err := r.Close(); err != nil || c.closeCount != 1 {
		t.Errorf("third Close = %v, closeCount = %d", err, c.closeCount)
	}
	if _, err := r.Stat(context.Background(), "/a.txt"); err == nil || q.dials() != 1 {
		t.Errorf("Stat after Close = %v, dials = %d; want error without reconnect", err, q.dials())
	}
	// 无连接状态下 Close 同样幂等。
	r.mu.Lock()
	r.conn = nil
	r.mu.Unlock()
	if err := r.Close(); err != nil {
		t.Errorf("Close with nil conn = %v, want nil", err)
	}
}

// 连接建立失败：session 返回错误，连接保持空；下一次调用重试 dial
// （服务恢复后自愈）。
func TestConnectFailureRetriedLazily(t *testing.T) {
	var dials atomic.Int64
	var healthy atomic.Bool
	r := newFakeRemote(t, func(ctx context.Context) (conn, error) {
		dials.Add(1)
		if !healthy.Load() {
			return nil, &smb2.TransportError{Err: errors.New("connection refused")}
		}
		c := newFakeConn()
		c.seed("", fakeEntry{name: "a.txt", size: 4})
		return c, nil
	})
	ctx := context.Background()

	if _, err := r.Stat(ctx, "/a.txt"); err == nil {
		t.Fatal("Stat with failing dial = nil, want error")
	}
	healthy.Store(true)
	if _, err := r.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("Stat after service recovery: %v", err)
	}
	if dials.Load() != 2 {
		t.Errorf("dials = %d, want 2", dials.Load())
	}
}

// 并发操作 + 反复 teardown：无 panic、无死锁，任何成功的返回都必须
// 内容正确（不存在跨代际的错误组合）；压测结束后惰性重连仍可用。
// go test -race 下同时验证锁的正确性。
func TestConcurrentOpsWithTeardown(t *testing.T) {
	dirs, files := walkFixture()
	mkConn := func() *fakeConn {
		c := newFakeConn()
		for dir, entries := range dirs {
			c.seed(dir, entries...)
		}
		for native, content := range files {
			c.writeFile(native, content)
		}
		return c
	}
	current := &atomic.Pointer[fakeConn]{}
	first := mkConn()
	current.Store(first)
	r := newFakeRemote(t, func(ctx context.Context) (conn, error) {
		return current.Load(), nil
	})
	ctx := context.Background()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	// teardown 扰动方：周期性拆除连接并换代（模拟 transport 故障 +
	// 重连），与操作方并发。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				old := current.Load()
				fresh := mkConn()
				current.Store(fresh)
				old.kill()
				r.mu.RLock()
				cur := r.conn
				r.mu.RUnlock()
				if cur == conn(old) {
					r.teardownSession(old)
				}
			}
		}
	}()
	// 操作方：读取并校验内容。
	expect := map[string]string{"/a.txt": "v1", "/docs/b.txt": "b1"}
	for path, want := range expect {
		wg.Add(1)
		go func(path, want string) {
			defer wg.Done()
			deadline := time.Now().Add(400 * time.Millisecond)
			for time.Now().Before(deadline) {
				select {
				case <-stop:
					return
				default:
				}
				rc, err := r.Open(ctx, path)
				if err != nil {
					continue // 并发 teardown 下的连接失败可接受
				}
				data, err := io.ReadAll(rc)
				_ = rc.Close()
				if err != nil {
					continue // 在途读取被拆除打断可接受
				}
				if string(data) != want {
					panic(fmt.Sprintf("concurrent read of %s = %q, want %q", path, data, want))
				}
			}
		}(path, want)
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()

	// 压测结束后远端仍可用（惰性重连生效）。
	rc, err := r.Open(ctx, "/a.txt")
	if err != nil {
		t.Fatalf("post-hammer open: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(data) != "v1" {
		t.Errorf("post-hammer read = %q, %v", data, err)
	}
}
