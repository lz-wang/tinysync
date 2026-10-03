package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"

	"tinysync/internal/source"
	smbadapter "tinysync/internal/source/smb"
	"tinysync/internal/syncjob"
)

// SMB integration：真实 Samba 容器（CI 由 integration job 注入；本地
// 可对任意 SMB2/3 服务运行）。设置 TINYSYNC_IT_SMB_HOST 后下列测试
// 才运行，未设置时跳过，不影响 make check。真实 TCP + 真实 SMB
// negotiate + 真实 NTLMv2 认证 + 真实 Samba 文件系统——不 mock SMB
// RPC。
const (
	itSMBHost     = "TINYSYNC_IT_SMB_HOST"
	itSMBPort     = "TINYSYNC_IT_SMB_PORT"
	itSMBShare    = "TINYSYNC_IT_SMB_SHARE"
	itSMBUsername = "TINYSYNC_IT_SMB_USERNAME"
	itSMBPassword = "TINYSYNC_IT_SMB_PASSWORD"
	itSMBDomain   = "TINYSYNC_IT_SMB_DOMAIN"
)

// smbITConfig 是一次集成运行的环境参数。
type smbITConfig struct {
	host     string
	port     int
	share    string
	username string
	password string
	domain   string
}

// smbITLoad 读取环境变量；HOST 未设置时跳过。
func smbITLoad(t *testing.T) smbITConfig {
	t.Helper()
	host := os.Getenv(itSMBHost)
	if host == "" {
		t.Skipf("set %s to run the real-Samba integration scenario", itSMBHost)
	}
	port := 445
	if raw := os.Getenv(itSMBPort); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 65535 {
			t.Fatalf("%s = %q is not a valid port", itSMBPort, raw)
		}
		port = n
	}
	share := os.Getenv(itSMBShare)
	if share == "" {
		share = "tinysync"
	}
	username := os.Getenv(itSMBUsername)
	if username == "" {
		username = "tinysync"
	}
	password := os.Getenv(itSMBPassword)
	if password == "" {
		t.Fatalf("%s is required", itSMBPassword)
	}
	return smbITConfig{
		host: host, port: port, share: share,
		username: username, password: password, domain: os.Getenv(itSMBDomain),
	}
}

// addr 返回 host:port。
func (c smbITConfig) addr() string {
	return net.JoinHostPort(c.host, strconv.Itoa(c.port))
}

// sourceConfig 构造指向 isolationRoot 的 canonical SMBConfig（signing
// 默认 required，真实服务器必须支持签名）。
func (c smbITConfig) sourceConfig(isolationRoot string) source.SMBConfig {
	return source.SMBConfig{
		Host:       c.host,
		Port:       c.port,
		Share:      c.share,
		RemoteRoot: isolationRoot,
		Username:   c.username,
		Domain:     c.domain,
		Signing:    source.SMBSigningRequired,
	}
}

// smbITAdmin 建立独立的 admin 连接（测试数据装置，不经被测 adapter
// 的任何代码路径）。
func smbITAdmin(t *testing.T, cfg smbITConfig) *smb2.Share {
	t.Helper()
	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User: cfg.username, Password: cfg.password, Domain: cfg.domain,
		},
		Negotiator: smb2.Negotiator{RequireMessageSigning: true},
	}
	session, err := dialer.Dial(context.Background(), cfg.addr())
	if err != nil {
		t.Fatalf("admin dial: %v", err)
	}
	share, err := session.Mount(cfg.share)
	if err != nil {
		_ = session.Logoff()
		t.Fatalf("admin mount %q: %v", cfg.share, err)
	}
	t.Cleanup(func() {
		_ = share.Umount()
		_ = session.Logoff()
	})
	return share
}

// smbITSpace 是一个带时间戳的隔离目录：put / remove 直接操作 admin
// share 造数据（不经被测 adapter），remoteRoot 供 adapter 的
// RemoteRoot 使用，native 把 logical 路径转为隔离目录内的 share
// native 路径。结束时尽力清空目录。
type smbITSpace struct {
	put        func(t *testing.T, logical, content string)
	remove     func(t *testing.T, logical string)
	remoteRoot string
	native     func(logical string) string
}

// smbITNewSpace 建立隔离目录与装置。
func smbITNewSpace(t *testing.T, cfg smbITConfig) smbITSpace {
	t.Helper()
	admin := smbITAdmin(t, cfg)
	root := fmt.Sprintf("it-%d", time.Now().UnixNano())
	if err := admin.Mkdir(root, 0o755); err != nil {
		t.Fatalf("admin mkdir isolation root: %v", err)
	}
	native := func(logical string) string {
		return root + `\` + strings.ReplaceAll(strings.TrimPrefix(path.Clean(logical), "/"), "/", `\`)
	}
	put := func(t *testing.T, logical, content string) {
		t.Helper()
		n := native(logical)
		// 父目录在 logical 空间计算后转 native（path 包不认识 '\'）。
		if parent := path.Dir(path.Clean(logical)); parent != "/" && parent != "." {
			_ = admin.MkdirAll(native(parent), 0o755)
		}
		f, err := admin.Create(n)
		if err != nil {
			t.Fatalf("admin create %s: %v", n, err)
		}
		defer f.Close()
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("admin write %s: %v", n, err)
		}
	}
	remove := func(t *testing.T, logical string) {
		t.Helper()
		if err := admin.Remove(native(logical)); err != nil {
			t.Fatalf("admin remove %s: %v", logical, err)
		}
	}
	t.Cleanup(func() {
		_ = admin.RemoveAll(root)
	})
	// adapter 侧的 RemoteRoot 是 POSIX 绝对 logical 路径；native 前缀
	// 由 put / remove / native 闭包内部处理。
	return smbITSpace{put: put, remove: remove, remoteRoot: "/" + root, native: native}
}

// TestIntegrationSMB 用真实 Samba 运行与协议矩阵完全相同的同步场景：
// initial pull、unchanged、update、selector、Copy remove 保留、Mirror
// remove 删除、history（覆盖验收 #8：SMB 完整通过现有协议矩阵语义）。
func TestIntegrationSMB(t *testing.T) {
	cfg := smbITLoad(t)
	space := smbITNewSpace(t, cfg)
	factory := smbadapter.NewFactory()
	srcCfg := cfg.sourceConfig(space.remoteRoot)
	creds := source.Credentials{SMB: &source.SMBCredentials{Password: cfg.password}}
	runCommonSyncScenario(t, matrixRemote{
		name:   "smb-real",
		put:    space.put,
		remove: space.remove,
		openRemote: func() (source.Remote, error) {
			return factory.Create(context.Background(), source.Source{
				Name:   "integration-smb",
				Type:   source.TypeSMB,
				Config: source.Config{SMB: &srcCfg},
			}, creds)
		},
	})
}

// TestIntegrationSMBAuthFailures：错误密码与不存在 share 都是确定性
// 失败——permanent 分类让 Downloader / Runner 不做无意义重试。
func TestIntegrationSMBAuthFailures(t *testing.T) {
	cfg := smbITLoad(t)
	factory := smbadapter.NewFactory()
	ctx := context.Background()

	cases := []struct {
		name string
		cfg  source.SMBConfig
		pw   string
	}{
		{"wrong password", func() source.SMBConfig {
			c := cfg.sourceConfig("/")
			return c
		}(), "definitely-wrong-password"},
		{"no such share", func() source.SMBConfig {
			c := cfg.sourceConfig("/")
			c.Share = "no-such-share-404"
			return c
		}(), cfg.password},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := factory.Create(ctx, source.Source{
				Name: "integration-smb-auth", Type: source.TypeSMB,
				Config: source.Config{SMB: &tc.cfg},
			}, source.Credentials{SMB: &source.SMBCredentials{Password: tc.pw}})
			if err == nil {
				t.Fatal("Create = nil, want error")
			}
			if source.IsRetryable(err) {
				t.Errorf("Create error %v is retryable, want permanent", err)
			}
		})
	}
}

// TestIntegrationSMBBrowse：远端浏览语义——Stat root / 文件、Unicode
// 与空格文件名、空目录、深层目录、隐藏文件、List 分页、Mkdir。
func TestIntegrationSMBBrowse(t *testing.T) {
	cfg := smbITLoad(t)
	space := smbITNewSpace(t, cfg)
	ctx := context.Background()

	space.put(t, "/hello.txt", "hello samba")
	space.put(t, "/empty.txt", "")
	space.put(t, "/特别 名字 +1.txt", "unicode")
	space.put(t, "/.hidden", "hidden-dot-file")
	space.put(t, "/deep/a/b/c/leaf.txt", "deep-leaf")
	// 空目录经 admin 建。
	admin := smbITAdmin(t, cfg)
	if err := admin.MkdirAll(space.native("/empty-dir"), 0o755); err != nil {
		t.Fatalf("mkdir empty-dir: %v", err)
	}

	factory := smbadapter.NewFactory()
	srcCfg := cfg.sourceConfig(space.remoteRoot)
	r, err := factory.Create(ctx, source.Source{
		Name: "integration-smb-browse", Type: source.TypeSMB,
		Config: source.Config{SMB: &srcCfg},
	}, source.Credentials{SMB: &source.SMBCredentials{Password: cfg.password}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Stat root。
	fi, err := r.Stat(ctx, "/")
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if !fi.IsDir || fi.Path != "/" {
		t.Errorf("root = %+v, want dir /", fi)
	}

	// Stat 文件（Unicode / 空格 / 隐藏文件全链路）。
	for name, want := range map[string]int{
		"/hello.txt":           len("hello samba"),
		"/特别 名字 +1.txt":        len("unicode"),
		"/.hidden":             len("hidden-dot-file"),
		"/deep/a/b/c/leaf.txt": len("deep-leaf"),
	} {
		fi, err := r.Stat(ctx, name)
		if err != nil {
			t.Errorf("Stat %q: %v", name, err)
			continue
		}
		if fi.IsDir || fi.Fingerprint.Size != int64(want) {
			t.Errorf("Stat %q = %+v, want file size %d", name, fi, want)
		}
	}

	// Open roundtrip。
	rc, err := r.Open(ctx, "/特别 名字 +1.txt")
	if err != nil {
		t.Fatalf("Open unicode name: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(data) != "unicode" {
		t.Errorf("unicode read = %q, %v", data, err)
	}

	// List root：分页跟随 cursor 到 EOF，不重复不丢失（root 含目录）。
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := r.List(ctx, "/", source.ListOptions{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("List page: %v", err)
		}
		for _, e := range page.Entries {
			if seen[e.Path] {
				t.Errorf("duplicate entry %q", e.Path)
			}
			seen[e.Path] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	for _, want := range []string{"/hello.txt", "/empty.txt", "/特别 名字 +1.txt", "/.hidden", "/deep", "/empty-dir"} {
		if !seen[want] {
			t.Errorf("entry %q missing from paged listing (got %v)", want, seen)
		}
	}

	// Mkdir 后立即可见。
	if err := r.(source.DirectoryCreator).Mkdir(ctx, "/made-by-tinysync"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := r.Stat(ctx, "/made-by-tinysync"); err != nil {
		t.Errorf("Stat after Mkdir: %v", err)
	}

	// ScanTree 深层目录完整可达。
	leaves := map[string]bool{}
	err = r.(source.TreeScanner).ScanTree(ctx, "/", func(fi source.FileInfo) error {
		if !fi.IsDir {
			leaves[fi.Path] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if !leaves["/deep/a/b/c/leaf.txt"] {
		t.Errorf("deep leaf missing from scan: %v", leaves)
	}
}

// TestIntegrationSMBReparse：Windows 语义的 native symlink（reparse
// point）fail-closed——Stat / List / ScanTree 拒绝，绝不跟随或静默
// 跳过；Mirror 在扫描失败后不执行任何删除。
func TestIntegrationSMBReparse(t *testing.T) {
	cfg := smbITLoad(t)
	space := smbITNewSpace(t, cfg)
	ctx := context.Background()

	space.put(t, "/keep.txt", "keep-me")
	admin := smbITAdmin(t, cfg)
	// 经 SMB 协议创建 native symlink（Windows 工具创建 reparse point
	// 的等价物；Samba 对 POSIX symlink 会服务器侧跟随，不在该契约内）。
	if err := admin.Symlink(space.native("/keep.txt"), space.native("/self-link")); err != nil {
		t.Fatalf("admin create native symlink: %v", err)
	}

	factory := smbadapter.NewFactory()
	srcCfg := cfg.sourceConfig(space.remoteRoot)
	r, err := factory.Create(ctx, source.Source{
		Name: "integration-smb-reparse", Type: source.TypeSMB,
		Config: source.Config{SMB: &srcCfg},
	}, source.Credentials{SMB: &source.SMBCredentials{Password: cfg.password}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = r.Close() }()

	// Stat reparse point：拒绝。
	if _, err := r.Stat(ctx, "/self-link"); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("Stat reparse = %v, want ErrInvalid", err)
	}
	// List 包含 reparse：拒绝。
	if _, err := r.List(ctx, "/", source.ListOptions{}); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("List with reparse = %v, want ErrInvalid", err)
	}
	// ScanTree：整轮失败。
	scanner := r.(source.TreeScanner)
	err = scanner.ScanTree(ctx, "/", func(source.FileInfo) error { return nil })
	if !errors.Is(err, source.ErrInvalid) {
		t.Errorf("ScanTree with reparse = %v, want ErrInvalid", err)
	}

	// Mirror 语义：扫描失败的 run 不产出快照、不删除本地 managed
	// 文件。先正常同步一轮建立 managed 集，再注入 symlink 跑第二轮，
	// 断言本地文件仍在（扫描失败 → run 失败 → 不删除）。
	e := newMatrixEnv(t, matrixRemote{
		name:   "smb-reparse",
		put:    space.put,
		remove: space.remove,
		openRemote: func() (source.Remote, error) {
			return factory.Create(context.Background(), source.Source{
				Name: "integration-smb-reparse", Type: source.TypeSMB,
				Config: source.Config{SMB: &srcCfg},
			}, source.Credentials{SMB: &source.SMBCredentials{Password: cfg.password}})
		},
	})
	localRoot := t.TempDir()
	job := newMatrixJob(t, e, localRoot, "mirror")
	// 先删掉 symlink 建立基线，再重新注入。
	if err := admin.Remove(space.native("/self-link")); err != nil {
		t.Fatalf("remove symlink for baseline: %v", err)
	}
	runAndWait(t, e, job.ID)
	assertLocalFile(t, localRoot, "keep.txt", "keep-me")

	// 重新注入 reparse point 后 Mirror 必须失败，本地文件不被误删。
	if err := admin.Symlink(space.native("/keep.txt"), space.native("/self-link")); err != nil {
		t.Fatalf("re-create native symlink: %v", err)
	}
	runID, err := e.runner.Start(ctx, job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, err := e.runner.Wait(ctx, runID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if status.State != syncjob.RunFailed {
		t.Fatalf("run state = %s, want failed (scan fail-closed)", status.State)
	}
	assertLocalFile(t, localRoot, "keep.txt", "keep-me")
}

// —— TCP 代理：模拟服务端停摆与连接断开 ——

// stallProxy 是一次性客户端方向的 TCP 代理：正常阶段双向转发；
// freeze 后吞掉 服务端→客户端 方向的数据（模拟无响应停摆）；
// sever 主动关闭当前连接（模拟连接被 reset）。
type stallProxy struct {
	ln     net.Listener
	target string

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	frozen  atomic.Bool
	severed atomic.Bool
}

func newStallProxy(t *testing.T, target string) *stallProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &stallProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	t.Cleanup(func() {
		_ = ln.Close()
		p.severAll()
	})
	go p.serve()
	return p
}

func (p *stallProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		p.conns[client] = struct{}{}
		p.mu.Unlock()
		go p.handle(client)
	}
}

func (p *stallProxy) handle(client net.Conn) {
	defer func() { _ = client.Close() }()
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		return
	}
	p.mu.Lock()
	p.conns[upstream] = struct{}{}
	p.mu.Unlock()
	defer func() {
		_ = upstream.Close()
		p.mu.Lock()
		delete(p.conns, upstream)
		p.mu.Unlock()
	}()

	done := make(chan struct{}, 2)
	// client → upstream 恒转发。
	go func() {
		_, _ = io.Copy(upstream, client)
		done <- struct{}{}
	}()
	// upstream → client：frozen 时丢弃（吞掉响应模拟停摆）。
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := upstream.Read(buf)
			if p.frozen.Load() {
				continue // 数据被吞：客户端永远等不到响应
			}
			if n > 0 {
				if _, werr := client.Write(buf[:n]); werr != nil {
					done <- struct{}{}
					return
				}
			}
			if rerr != nil {
				done <- struct{}{}
				return
			}
		}
	}()
	<-done
}

// severAll 主动关闭全部连接（模拟 TCP reset）。
func (p *stallProxy) severAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.conns = map[net.Conn]struct{}{}
}

// addr 返回代理监听地址。
func (p *stallProxy) addr() string { return p.ln.Addr().String() }

// TestIntegrationSMBCancelInterruptsStalledRead：服务端停摆后
// attempt ctx 取消必须真正中断阻塞中的 SMB Read（go-smb2 请求级取消
// 在真实 TCP 上生效——硬性验收 #6），且错误可判定为 ctx 错误。
func TestIntegrationSMBCancelInterruptsStalledRead(t *testing.T) {
	cfg := smbITLoad(t)
	space := smbITNewSpace(t, cfg)
	ctx := context.Background()
	space.put(t, "/big.txt", strings.Repeat("stall-body-", 400))

	proxy := newStallProxy(t, cfg.addr())
	proxiedCfg := cfg.sourceConfig(space.remoteRoot)
	host, portStr, _ := net.SplitHostPort(proxy.addr())
	port, _ := strconv.Atoi(portStr)
	proxiedCfg.Host, proxiedCfg.Port = host, port

	factory := smbadapter.NewFactory()
	r, err := factory.Create(ctx, source.Source{
		Name: "integration-smb-stall", Type: source.TypeSMB,
		Config: source.Config{SMB: &proxiedCfg},
	}, source.Credentials{SMB: &source.SMBCredentials{Password: cfg.password}})
	if err != nil {
		t.Fatalf("Create via proxy: %v", err)
	}
	defer func() { _ = r.Close() }()

	// 停摆前可用。
	if _, err := r.Stat(ctx, "/big.txt"); err != nil {
		t.Fatalf("Stat before stall: %v", err)
	}
	// 打开文件（FILE_OPEN 走 proxy 正常转发），随后冻结响应方向。
	attemptCtx, cancelAttempt := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelAttempt()
	rc, err := r.Open(attemptCtx, "/big.txt")
	if err != nil {
		t.Fatalf("Open before stall: %v", err)
	}
	proxy.frozen.Store(true)

	readDone := make(chan error, 1)
	go func() {
		_, readErr := rc.Read(make([]byte, 64))
		readDone <- readErr
	}()
	select {
	case readErr := <-readDone:
		if readErr == nil {
			t.Fatal("stalled read returned data, want error after attempt timeout")
		}
		if !errors.Is(readErr, context.DeadlineExceeded) {
			t.Errorf("stalled read error = %v, want context.DeadlineExceeded", readErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stalled read still blocked after attempt timeout; ctx cancellation does not interrupt real SMB I/O")
	}
	_ = rc.Close()
}

// TestIntegrationSMBReconnectAfterSever：连接被 reset 后操作返回
// transient 错误并拆除所属代际，下一次操作（Downloader 重试形态）
// 自动建立新 session 并完整收敛（硬性验收 #7）。
func TestIntegrationSMBReconnectAfterSever(t *testing.T) {
	cfg := smbITLoad(t)
	space := smbITNewSpace(t, cfg)
	ctx := context.Background()
	content := strings.Repeat("sever-body-", 500)
	space.put(t, "/big.txt", content)

	proxy := newStallProxy(t, cfg.addr())
	proxiedCfg := cfg.sourceConfig(space.remoteRoot)
	host, portStr, _ := net.SplitHostPort(proxy.addr())
	port, _ := strconv.Atoi(portStr)
	proxiedCfg.Host, proxiedCfg.Port = host, port

	factory := smbadapter.NewFactory()
	r, err := factory.Create(ctx, source.Source{
		Name: "integration-smb-sever", Type: source.TypeSMB,
		Config: source.Config{SMB: &proxiedCfg},
	}, source.Credentials{SMB: &source.SMBCredentials{Password: cfg.password}})
	if err != nil {
		t.Fatalf("Create via proxy: %v", err)
	}
	defer func() { _ = r.Close() }()

	if _, err := r.Stat(ctx, "/big.txt"); err != nil {
		t.Fatalf("Stat before sever: %v", err)
	}

	// 服务端断开当前连接：下一次操作得到 transport 错误（transient）。
	proxy.severAll()
	_, err = r.Stat(ctx, "/big.txt")
	if err == nil {
		t.Fatal("Stat after sever = nil, want transport error")
	}
	if !source.IsRetryable(err) {
		t.Errorf("sever error %v is not retryable, want transient", err)
	}

	// Downloader 重试形态：下一次操作建立新连接（proxy 接受新连接）。
	rc, err := r.Open(ctx, "/big.txt")
	if err != nil {
		t.Fatalf("Open after sever (should reconnect): %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read after reconnect: %v", err)
	}
	if string(data) != content {
		t.Errorf("content after reconnect = %d bytes, want %d", len(data), len(content))
	}
}
