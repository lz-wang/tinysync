// Package sftp 实现 source 的 SFTP 只读 Remote：基于 pkg/sftp 与
// golang.org/x/crypto/ssh。认证方式显式声明（password | private_key），
// host key 以 SHA256 fingerprint 严格 pin（拒绝 insecure 模式），
// remote_root 映射 Source "/"；发现 symlink 立即失败（不跳过、不
// 跟随），保证 remote snapshot 完整性——Mirror 的删除授权依赖完整
// 扫描。
package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"tinysync/internal/source"
)

// dialTimeout 是 SSH TCP 连接建立的最长时间。
const dialTimeout = 15 * time.Second

// handshakeTimeout 是 SSH 握手（含认证）的最长时间。
const handshakeTimeout = 15 * time.Second

// Factory 实现 source.RemoteFactory，按 Source 配置建立 SSH/SFTP 连接。
type Factory struct{}

// NewFactory 构造 SFTP RemoteFactory。
func NewFactory() *Factory {
	return &Factory{}
}

// Type 实现 source.RemoteFactory：本 factory 服务 SFTP 类型。
func (f *Factory) Type() source.Type {
	return source.TypeSFTP
}

// Create 实现 source.RemoteFactory：按 auth_method 组装认证，host key
// 以 SHA256 fingerprint 严格比较，SSH 连接建立支持 ctx 取消。
func (f *Factory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	if s.Type != source.TypeSFTP || s.Config.SFTP == nil {
		return nil, fmt.Errorf("%w: %q", source.ErrUnsupportedType, s.Type)
	}
	if credentials.SFTP == nil {
		return nil, fmt.Errorf("%w: sftp credentials are required", source.ErrInvalid)
	}
	cfg := *s.Config.SFTP
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(port))

	auth, err := authMethod(cfg, *credentials.SFTP)
	if err != nil {
		return nil, err
	}
	sshConfig := &ssh.ClientConfig{
		User: cfg.Username,
		Auth: []ssh.AuthMethod{auth},
		// Host key 必须 pin：fingerprint 不匹配直接失败，绝不提供
		// InsecureIgnoreHostKey 之类的旁路。
		HostKeyCallback: fingerprintCallback(cfg.HostKeyFingerprint),
		Timeout:         dialTimeout,
	}

	r := &remote{
		addr:    addr,
		sshCfg:  sshConfig,
		rootCfg: path.Clean(cfg.RemoteRoot),
	}
	r.mu.Lock()
	err = r.connect(ctx)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// ctx 取消（运行取消 / Shutdown）关闭 SSH 连接：阻塞中的 SFTP
	// 读取随连接关闭立即退出。ctx 为 background（Done 返回 nil）时
	// 不起 goroutine，避免泄漏。
	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			_ = r.Close()
		}()
	}
	return r, nil
}

// authMethod 按 auth_method 显式构造认证；不根据字段非空隐式推断。
func authMethod(cfg source.SFTPConfig, creds source.SFTPCredentials) (ssh.AuthMethod, error) {
	switch cfg.AuthMethod {
	case source.SFTPAuthPassword:
		if creds.Password == "" {
			return nil, fmt.Errorf("%w: sftp password is required for auth_method=password", source.ErrInvalid)
		}
		return ssh.Password(creds.Password), nil
	case source.SFTPAuthPrivateKey:
		if creds.PrivateKey == "" {
			return nil, fmt.Errorf("%w: sftp private_key is required for auth_method=private_key", source.ErrInvalid)
		}
		var signer ssh.Signer
		var err error
		if creds.PrivateKeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(
				[]byte(creds.PrivateKey), []byte(creds.PrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(creds.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("%w: parse sftp private key: %v", source.ErrInvalid, err)
		}
		return ssh.PublicKeys(signer), nil
	default:
		return nil, fmt.Errorf("%w: unsupported sftp auth_method %q", source.ErrInvalid, cfg.AuthMethod)
	}
}

// fingerprintCallback 返回严格比较 SHA256 fingerprint 的 HostKeyCallback；
// expected 必须是 SHA256:<base64> 形式（校验层保证）。host key 不匹配
// 标记为 permanent：重连不会改变对端密钥，重试无意义。
func fingerprintCallback(expected string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if got != expected {
			return source.MarkPermanent(fmt.Errorf("host key fingerprint mismatch for %s: got %s, want %s", hostname, got, expected))
		}
		return nil
	}
}

// dial 建立 SSH 连接：TCP dial 与握手分别限时，ctx 取消立即中断
// （阻塞中的 dial / 握手随 runCtx 取消退出）。
func dial(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else if handshakeTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	}
	// 握手阻塞期间 ctx 取消直接关闭连接（SetDeadline 只能覆盖
	// 带 deadline 的取消）。
	handshakeDone := make(chan struct{})
	defer close(handshakeDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-handshakeDone:
		}
	}()
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// 握手完成后恢复：SFTP 会话与数据传输不受握手期限约束。
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// remote 是 source.Remote 的 SFTP 实现，持有 SSH 连接与会话。
// 连接可被 attempt 超时拆除（见 ctxFile），操作经 session 惰性重连；
// 同一轮 run 的多次传输因此不因单次超时整体失败。
type remote struct {
	// 连接参数：连接被超时拆除后惰性重连所需。
	addr    string
	sshCfg  *ssh.ClientConfig
	rootCfg string

	mu     sync.RWMutex
	ssh    *ssh.Client
	sftp   *sftp.Client
	root   string
	closed bool
}

// 编译期断言。
var _ source.Remote = (*remote)(nil)

// connect 建立 SSH 连接、SFTP 会话并解析真实 root；调用方持有写锁。
func (r *remote) connect(ctx context.Context) error {
	client, err := dial(ctx, r.addr, r.sshCfg)
	if err != nil {
		return fmt.Errorf("sftp dial %s: %w", r.addr, err)
	}
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("sftp open session on %s: %w", r.addr, err)
	}
	// RealPath 解析服务器侧真实 root：配置路径可能经 symlink / 挂载
	// 呈现，root confinement 以解析后的真实路径为基准。
	root, err := sftpClient.RealPath(r.rootCfg)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("resolve sftp remote root %s: %w", r.rootCfg, err)
	}
	if !path.IsAbs(root) {
		_ = client.Close()
		return fmt.Errorf("%w: sftp remote root %s resolved to non-absolute %s", source.ErrInvalid, r.rootCfg, root)
	}
	r.ssh = client
	r.sftp = sftpClient
	r.root = root
	return nil
}

// session 返回当前 SFTP 会话与其解析 root 的代际快照：连接尚未建立
// 或已被超时拆除时惰性重连（RealPath 重新解析，与首次创建一致）。
// client 与 root 必须成对使用——路径映射基于快照 root，操作落在同
// 一快照的连接上，杜绝「旧 root 路径打在新连接上」的跨代际组合；
// 快照随后被并发 teardown 拆除时，操作返回连接丢失错误，由上层按
// transient 重试（重试会取得新一代快照）。closed 之后不再重连。
func (r *remote) session(ctx context.Context) (*sftp.Client, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	r.mu.RLock()
	if r.sftp != nil {
		c, root := r.sftp, r.root
		r.mu.RUnlock()
		return c, root, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, "", fmt.Errorf("sftp remote is closed")
	}
	if r.sftp != nil {
		return r.sftp, r.root, nil
	}
	if err := r.connect(ctx); err != nil {
		return nil, "", err
	}
	return r.sftp, r.root, nil
}

// teardown 关闭当前 SSH/SFTP 连接并清空引用：attempt 超时或 run 取消
// 时触发（见 ctxFile），阻塞中的 SFTP 读取随连接关闭立即返回错误。
// 连接之后经 session 惰性重建；Close 才是终态。
//
// 必须先断 SSH 连接再关 SFTP 会话：sftp.Client.Close 会等待 recv
// 循环退出，而对端停摆时只有底层连接关闭才能让 recv 循环退出；
// 顺序颠倒会永久阻塞在 sftp 的 WaitGroup 上。
func (r *remote) teardown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sftp == nil {
		return
	}
	_ = r.ssh.Close()
	_ = r.sftp.Close()
	r.sftp = nil
	r.ssh = nil
}

// remoteAbs 把 Source-relative logical path 映射为远端绝对路径
// （全部 path 语义，不用 filepath），并双重防御 root escape：
// logical path 经 ValidateLogicalPath 保证 clean，映射结果仍显式
// 验证落在给定 root 之内。root 必须来自 session 快照——路径映射与
// 连接代际绑定，重连切换 root 后不会出现旧 root 路径打在新连接上
// 的组合，r.root 也因此只在锁内读写。
func remoteAbs(root, logicalPath string) (string, error) {
	cleaned := path.Clean(logicalPath)
	abs := root
	if cleaned != "/" {
		abs = root + cleaned
	}
	if abs != root && !strings.HasPrefix(abs, root+"/") {
		return "", fmt.Errorf("%w: sftp path %q escapes remote root %q", source.ErrInvalid, logicalPath, root)
	}
	return abs, nil
}

// toLogical 把远端条目名转为 Source-relative logical path 并统一过
// ValidateLogicalPath。
func (r *remote) toLogical(dirLogical, name string) (string, error) {
	if dirLogical == "/" {
		dirLogical = ""
	}
	logical := "/" + strings.TrimPrefix(path.Join(dirLogical, name), "/")
	if err := source.ValidateLogicalPath(logical); err != nil {
		return "", err
	}
	return logical, nil
}

// Stat 实现 source.Remote：Lstat 不跟随 symlink。入口统一校验
// logical path（ErrInvalid fail-fast）。
func (r *remote) Stat(ctx context.Context, logicalPath string) (source.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return source.FileInfo{}, err
	}
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return source.FileInfo{}, err
	}
	c, root, err := r.session(ctx)
	if err != nil {
		return source.FileInfo{}, err
	}
	abs, err := remoteAbs(root, logicalPath)
	if err != nil {
		return source.FileInfo{}, err
	}
	info, err := c.Lstat(abs)
	if err != nil {
		return source.FileInfo{}, wrapOp("stat", logicalPath, err)
	}
	return r.toFileInfo(logicalPath, info)
}

// List 实现 source.Remote：ReadDir 列一层；发现 symlink 整体失败——
// 简单 skip 会得到不完整的 remote snapshot，Mirror 可能据此误删
// 本地 managed 文件。SFTP v1 的 ReadDir 没有持久目录游标：单层
// 完整枚举后切片分页，cursor 为 opaque offset token（分页约束返回
// 条目数；协议层单次请求仍是整层目录，v0.6 契约已记录该限制）。
func (r *remote) List(ctx context.Context, logicalDir string, opts source.ListOptions) (source.FilePage, error) {
	if err := ctx.Err(); err != nil {
		return source.FilePage{}, err
	}
	if err := source.ValidateLogicalPath(logicalDir); err != nil {
		return source.FilePage{}, err
	}
	c, root, err := r.session(ctx)
	if err != nil {
		return source.FilePage{}, err
	}
	abs, err := remoteAbs(root, logicalDir)
	if err != nil {
		return source.FilePage{}, err
	}
	entries, err := c.ReadDir(abs)
	if err != nil {
		return source.FilePage{}, wrapOp("list", logicalDir, err)
	}
	all := make([]source.FileInfo, 0, len(entries))
	for _, entry := range entries {
		logical, err := r.toLogical(logicalDir, entry.Name())
		if err != nil {
			return source.FilePage{}, wrapOp("list", logicalDir, err)
		}
		fi, err := r.toFileInfo(logical, entry)
		if err != nil {
			return source.FilePage{}, err
		}
		all = append(all, fi)
	}
	return source.PageSlice(all, opts)
}

// toFileInfo 转换协议无关 FileInfo：symlink 拒绝；SFTP 不提供 ETag，
// Fingerprint 走 Size + ModifiedAt（planner 既有降级路径）。
func (r *remote) toFileInfo(logical string, info fs.FileInfo) (source.FileInfo, error) {
	if info.Mode()&fs.ModeSymlink != 0 {
		return source.FileInfo{}, fmt.Errorf("%w: %s is a symlink; sftp sources do not follow or skip symlinks", source.ErrInvalid, logical)
	}
	return source.FileInfo{
		Path:  logical,
		IsDir: info.IsDir(),
		Fingerprint: source.Fingerprint{
			Size:       info.Size(),
			ModifiedAt: info.ModTime(),
		},
	}, nil
}

// Open 实现 source.Remote。入口统一校验 logical path。返回的
// ReadCloser 绑定 ctx：ctx 取消 / 超时经连接拆除中断阻塞中的读取。
func (r *remote) Open(ctx context.Context, logicalPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return nil, err
	}
	c, root, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	abs, err := remoteAbs(root, logicalPath)
	if err != nil {
		return nil, err
	}
	f, err := c.Open(abs)
	if err != nil {
		return nil, wrapOp("open", logicalPath, err)
	}
	return newCtxFile(ctx, f, r.teardown), nil
}

// Close 实现 source.Remote：关闭连接并进入终态（不再重连），阻塞中
// 的读取随连接关闭退出。幂等。与 teardown 相同，先断 SSH 连接再关
// SFTP 会话，避免对端停摆时永久阻塞在 sftp 的 WaitGroup 上；此后
// SFTP 会话的关闭错误只是连接已拆的回声（channel close 对已死 TCP
// 必然失败），不携带额外信息，忽略之。
func (r *remote) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.sftp == nil {
		return nil
	}
	sshErr := r.ssh.Close()
	_ = r.sftp.Close()
	r.sftp = nil
	r.ssh = nil
	return sshErr
}

// ctxFile 把打开的 SFTP 文件绑定到调用方 context。SFTP v1 协议没有
// 请求级取消，且 pkg/sftp 的 File.Read 全程持有内部互斥量——从另
// 一 goroutine Close 文件无法中断阻塞中的读取；唯一可靠的中断手段
// 是关闭底层连接。ctx 取消（Downloader 的 attempt 超时或 run 取消）
// 触发 teardown 拆除共享连接，阻塞中的 Read 立即返回连接丢失错误；
// Downloader 按 transient 重试，下一次操作经 session 惰性重连。
type ctxFile struct {
	f        *sftp.File
	teardown func()
	stop     chan struct{}
	stopOnce sync.Once
}

// 编译期断言。
var _ io.ReadCloser = (*ctxFile)(nil)

// newCtxFile 包装已打开的文件；ctx 无 Done（background）时不启动
// 守护 goroutine，行为与裸 *sftp.File 一致。
func newCtxFile(ctx context.Context, f *sftp.File, teardown func()) *ctxFile {
	c := &ctxFile{f: f, teardown: teardown, stop: make(chan struct{})}
	if ctx.Done() == nil {
		return c
	}
	go func() {
		select {
		case <-ctx.Done():
			c.teardown()
		case <-c.stop:
		}
	}()
	return c
}

func (c *ctxFile) Read(p []byte) (int, error) {
	return c.f.Read(p)
}

// Close 停止守护并关闭文件；与 ctx 触发的 teardown 竞争安全。
func (c *ctxFile) Close() error {
	c.stopOnce.Do(func() { close(c.stop) })
	return c.f.Close()
}

// wrapOp 为底层错误补充操作与路径上下文；ctx 超时/取消经 %w 保持
// 可判定。
func wrapOp(op, logicalPath string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("sftp %s %s: %w", op, logicalPath, err)
}
