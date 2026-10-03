// Package smb 实现 source 的 SMB 只读 Remote：基于
// github.com/cloudsoda/go-smb2（纯 Go，rclone 的 SMB backend 同源），
// 只协商 SMB2/SMB3 + NTLMv2 用户名密码认证，不支持 SMB1 / Kerberos /
// DFS / guest。host + share + remote_root 定位远端 namespace，
// remote_root 映射 Source "/"，内部路径保持 POSIX 风格（native
// 反斜杠只在 adapter 边界出现）。所有 reparse point（symlink /
// junction / mount point）一律 fail-closed：不跟随、不静默跳过——
// 跳过会得到不完整的 remote snapshot，Mirror 可能据此误删本地
// managed 文件。
package smb

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"

	smb2 "github.com/cloudsoda/go-smb2"

	"tinysync/internal/source"
)

// Factory 实现 source.RemoteFactory，按 Source 配置建立 SMB 连接。
type Factory struct{}

// NewFactory 构造 SMB RemoteFactory。
func NewFactory() *Factory {
	return &Factory{}
}

// Type 实现 source.RemoteFactory：本 factory 服务 SMB 类型。
func (f *Factory) Type() source.Type {
	return source.TypeSMB
}

// conn 是一届 SMB 连接（tree-connected share）的最小能力面。抽象为
// 接口使 remote 的生命周期逻辑（惰性重连、代际 teardown、Close 终态）
// 可以在无真实 SMB 服务的单元测试中确定性地验证；生产实现 smb2Conn
// 包装 go-smb2 的 Session + Share。所有方法携带 ctx：go-smb2 经
// Share.WithContext 提供请求级取消，阻塞中的等待（含已打开文件的
// Read）随 ctx 取消立即返回 ctx 错误。
type conn interface {
	lstat(ctx context.Context, name string) (os.FileInfo, error)
	readDir(ctx context.Context, name string) ([]os.FileInfo, error)
	open(ctx context.Context, name string) (io.ReadCloser, error)
	mkdir(ctx context.Context, name string) error
	close() error
}

// smb2Conn 是 conn 的生产实现。session 与 share 必须成对持有：
// Close 先 Umount（tree disconnect）再 Logoff（session teardown）。
type smb2Conn struct {
	session *smb2.Session
	share   *smb2.Share
}

func (c *smb2Conn) lstat(ctx context.Context, name string) (os.FileInfo, error) {
	return c.share.WithContext(ctx).Lstat(name)
}

func (c *smb2Conn) readDir(ctx context.Context, name string) ([]os.FileInfo, error) {
	return c.share.WithContext(ctx).ReadDir(name)
}

func (c *smb2Conn) open(ctx context.Context, name string) (io.ReadCloser, error) {
	return c.share.WithContext(ctx).Open(name)
}

func (c *smb2Conn) mkdir(ctx context.Context, name string) error {
	return c.share.WithContext(ctx).Mkdir(name, 0o755)
}

// close 拆除 tree 与 session。显式用 background ctx：调用方 ctx 往往
// 已取消（run 结束 / 用户停止），带取消 ctx 的关闭请求会立即失败，
// 连接只等 TCP 超时回收。
func (c *smb2Conn) close() error {
	err := c.share.WithContext(context.Background()).Umount()
	if logoffErr := c.session.WithContext(context.Background()).Logoff(); err == nil {
		err = logoffErr
	}
	return err
}

// dial 建立 TCP 连接、协商 SMB2/3、NTLMv2 认证并 mount share。
// signing=required 时通过 Negotiator.RequireMessageSigning 强制
// 签名，服务器不支持即失败（fail-closed）；auto 跟随服务器协商。
func dial(ctx context.Context, cfg source.SMBConfig, password string) (conn, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     cfg.Username,
			Password: password,
			Domain:   cfg.Domain,
		},
		Negotiator: smb2.Negotiator{
			RequireMessageSigning: cfg.Signing == source.SMBSigningRequired,
		},
	}
	session, err := dialer.Dial(ctx, addr)
	if err != nil {
		return nil, normalizeCtxErr(ctx, classifyError(fmt.Errorf("smb dial %s: %w", addr, err)))
	}
	share, err := session.Mount(cfg.Share)
	if err != nil {
		_ = session.WithContext(context.Background()).Logoff()
		return nil, normalizeCtxErr(ctx, classifyError(fmt.Errorf("smb mount share %q on %s: %w", cfg.Share, addr, err)))
	}
	return &smb2Conn{session: session, share: share}, nil
}

// remote 是 source.Remote 的 SMB 实现。连接由 connect 惰性建立：
// transport 故障拆除当前代际（teardownSession）后，下一次操作经
// session 自动重连——同一轮 run 内 Downloader 的重试因此建立新
// SMB 连接，而不是反复使用坏连接。
type remote struct {
	cfg      source.SMBConfig
	password string
	// connect 建立新一届连接；生产路径是本包 dial，测试注入 fake。
	connect func(ctx context.Context) (conn, error)

	mu     sync.RWMutex
	conn   conn
	closed bool
}

// 编译期契约断言。
var (
	_ source.Remote           = (*remote)(nil)
	_ source.TreeScanner      = (*remote)(nil)
	_ source.DirectoryCreator = (*remote)(nil)
)

// Create 实现 source.RemoteFactory：校验类型与凭据，建立首条连接
// （ctx 可取消，阻塞中的 TCP dial / 协商 / NTLM 认证随取消退出）。
func (f *Factory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	if s.Type != source.TypeSMB || s.Config.SMB == nil {
		return nil, fmt.Errorf("%w: %q", source.ErrUnsupportedType, s.Type)
	}
	if credentials.SMB == nil || credentials.SMB.Password == "" {
		return nil, fmt.Errorf("%w: smb password is required (guest access is not supported)", source.ErrInvalid)
	}
	r := &remote{
		cfg:      *s.Config.SMB,
		password: credentials.SMB.Password,
		connect: func(ctx context.Context) (conn, error) {
			return dial(ctx, *s.Config.SMB, credentials.SMB.Password)
		},
	}
	r.mu.Lock()
	err := r.reconnectLocked(ctx)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return r, nil
}

// reconnectLocked 建立新一届连接；调用方持有写锁。
func (r *remote) reconnectLocked(ctx context.Context) error {
	c, err := r.connect(ctx)
	if err != nil {
		return err
	}
	r.conn = c
	return nil
}

// session 返回当前连接代际快照：无连接时惰性重连。closed 之后不再
// 重连（终态）。快照随后被并发 teardown 拆除时，其上的操作返回
// 连接丢失错误，由上层按 transient 重试（重试取得新一代快照）。
func (r *remote) session(ctx context.Context) (conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	if r.conn != nil {
		c := r.conn
		r.mu.RUnlock()
		return c, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("smb remote is closed")
	}
	if r.conn != nil {
		return r.conn, nil
	}
	if err := r.reconnectLocked(ctx); err != nil {
		return nil, err
	}
	return r.conn, nil
}

// teardownSession 拆除 expected 指向的连接代际：仅当当前连接仍是
// expected 时才关闭并清空引用，否则 no-op。transport 故障回调因此
// 与操作发生时的代际绑定——stale 代际（故障回报晚于重连到达）绝不
// 拆掉重连后的新连接。
func (r *remote) teardownSession(expected conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil || r.conn != expected {
		return
	}
	_ = r.conn.close()
	r.conn = nil
}

// maybeTeardown 在操作失败时按错误语义拆除所属代际：连接已坏
// （transport / session deleted）才拆除；认证、权限、不存在等
// 确定性失败不触碰连接——同连接上的其它操作（浏览、并发传输）不
// 因邻居的 permanent 失败被连带中断。
func (r *remote) maybeTeardown(c conn, err error) {
	if isSessionLost(err) {
		r.teardownSession(c)
	}
}

// root 返回配置的 canonical remote root（不可变字段无需加锁）。
func (r *remote) root() string {
	return r.cfg.RemoteRoot
}

// Stat 实现 source.Remote：Lstat 不跟随 reparse point（go-smb2 以
// FILE_OPEN_REPARSE_POINT 打开）。入口统一校验 logical path。
func (r *remote) Stat(ctx context.Context, logicalPath string) (source.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return source.FileInfo{}, err
	}
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return source.FileInfo{}, err
	}
	c, err := r.session(ctx)
	if err != nil {
		return source.FileInfo{}, err
	}
	native, err := remotePath(r.root(), logicalPath)
	if err != nil {
		return source.FileInfo{}, err
	}
	info, err := c.lstat(ctx, native)
	if err != nil {
		wrapped := normalizeCtxErr(ctx, wrapOp("stat", logicalPath, classifyError(err)))
		r.maybeTeardown(c, wrapped)
		return source.FileInfo{}, wrapped
	}
	return toFileInfo(logicalPath, info)
}

// List 实现 source.Remote：ReadDir 列一层，条目逐个安全校验后转换，
// 单层完整枚举再切片分页（SMB 没有持久目录游标，分页是 UI/API
// 语义；同步扫描走 ScanTree）。
func (r *remote) List(ctx context.Context, logicalDir string, opts source.ListOptions) (source.FilePage, error) {
	if err := ctx.Err(); err != nil {
		return source.FilePage{}, err
	}
	if err := source.ValidateLogicalPath(logicalDir); err != nil {
		return source.FilePage{}, err
	}
	c, err := r.session(ctx)
	if err != nil {
		return source.FilePage{}, err
	}
	native, err := remotePath(r.root(), logicalDir)
	if err != nil {
		return source.FilePage{}, err
	}
	entries, err := c.readDir(ctx, native)
	if err != nil {
		wrapped := normalizeCtxErr(ctx, wrapOp("list", logicalDir, classifyError(err)))
		r.maybeTeardown(c, wrapped)
		return source.FilePage{}, wrapped
	}
	all := make([]source.FileInfo, 0, len(entries))
	for _, entry := range entries {
		logical, err := toLogical(logicalDir, entry.Name())
		if err != nil {
			return source.FilePage{}, wrapOp("list", logicalDir, err)
		}
		fi, err := toFileInfo(logical, entry)
		if err != nil {
			return source.FilePage{}, err
		}
		all = append(all, fi)
	}
	return source.PageSlice(all, opts)
}

// Open 实现 source.Remote。返回的 *smb2.File 绑定打开时的 ctx：阻塞
// 中的 Read 等待服务器响应时，ctx 取消（用户停止 / attempt 超时）经
// go-smb2 的请求级取消立即返回 ctx 错误——不同于 SFTP，无需拆除
// 连接即可中断单次传输，同一连接上的其它在途传输不受影响。attempt
// 超时后句柄由服务器在 session 结束时回收（ctx 已取消时 Close 请求
// 必然失败，不视为泄漏事故）。
func (r *remote) Open(ctx context.Context, logicalPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return nil, err
	}
	c, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	native, err := remotePath(r.root(), logicalPath)
	if err != nil {
		return nil, err
	}
	f, err := c.open(ctx, native)
	if err != nil {
		wrapped := normalizeCtxErr(ctx, wrapOp("open", logicalPath, classifyError(err)))
		r.maybeTeardown(c, wrapped)
		return nil, wrapped
	}
	return f, nil
}

// Mkdir 实现 source.DirectoryCreator，在当前 Source root 下创建目录。
func (r *remote) Mkdir(ctx context.Context, logicalPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := source.ValidateLogicalPath(logicalPath); err != nil || logicalPath == "/" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: cannot create remote root", source.ErrInvalid)
	}
	c, err := r.session(ctx)
	if err != nil {
		return err
	}
	native, err := remotePath(r.root(), logicalPath)
	if err != nil {
		return err
	}
	if err := c.mkdir(ctx, native); err != nil {
		wrapped := normalizeCtxErr(ctx, wrapOp("mkdir", logicalPath, classifyError(err)))
		r.maybeTeardown(c, wrapped)
		return wrapped
	}
	return nil
}

// Close 实现 source.Remote：拆除连接并进入终态（session 不再重连），
// 幂等。
func (r *remote) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.conn == nil {
		return nil
	}
	err := r.conn.close()
	r.conn = nil
	return err
}

// toFileInfo 转换协议无关 FileInfo：任何 reparse point 一律拒绝
// （symlink / junction / mount point 不区分——跟随破坏 root
// confinement，静默跳过破坏 Mirror 删除安全语义，与 SFTP 的 symlink
// → 整轮 scan fail 保持一致）。SMB 不提供 ETag，Fingerprint 走
// Size + ModifiedAt。不依赖 receiver 状态：walkDirectory 注入 fake
// readDir 时复用同一规则。
func toFileInfo(logical string, info os.FileInfo) (source.FileInfo, error) {
	if st, ok := info.(*smb2.FileStat); ok && st.ReparsePointTag != 0 {
		return source.FileInfo{}, fmt.Errorf("%w: SMB reparse point %q is unsupported", source.ErrInvalid, logical)
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

// ScanTree 实现 source.TreeScanner：全树扫描 root 子树，文件与目录
// 都 visit（root 自身除外）。每个目录恰好一次 ReadDir——同步扫描
// 不经 List 的切片分页对同一目录重复枚举。遍历为显式栈式 DFS
// （不递归：超深目录树不占 goroutine 栈），栈内顺序不构成契约。
// 任何一层枚举失败、任何 reparse point、任何 visit 错误都整轮失败
// （绝不返回看起来成功的 incomplete snapshot——Mirror 的删除授权
// 依赖完整扫描），ctx 取消立即终止。
func (r *remote) ScanTree(ctx context.Context, root string, visit func(source.FileInfo) error) error {
	if err := source.ValidateLogicalPath(root); err != nil {
		return err
	}
	c, err := r.session(ctx)
	if err != nil {
		return err
	}
	sroot := r.root()
	readDir := func(ctx context.Context, logical string) ([]os.FileInfo, error) {
		native, err := remotePath(sroot, logical)
		if err != nil {
			return nil, err
		}
		entries, err := c.readDir(ctx, native)
		if err != nil {
			wrapped := normalizeCtxErr(ctx, wrapOp("scan", logical, classifyError(err)))
			r.maybeTeardown(c, wrapped)
			return nil, wrapped
		}
		return entries, nil
	}
	return walkDirectory(ctx, root, readDir, visit)
}

// walkDirectory 以显式栈枚举一个目录树：每层一次 readDir（生产路径
// 即一次 ReadDir / 一个协议请求），条目逐个转换并 visit，子目录入栈
// 后续展开。条目循环内逐条检查 ctx：单层枚举可能一次带回数万条目，
// 取消不能等到下一层 ReadDir 边界才生效。readDir 以参数注入：测试
// 传带计数器的 fake，把「每个目录恰好一次 ReadDir」「部分失败不产
// 出部分结果」变成确定性单元断言。
func walkDirectory(
	ctx context.Context,
	root string,
	readDir func(context.Context, string) ([]os.FileInfo, error),
	visit func(source.FileInfo) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := readDir(ctx, dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			child, err := toLogical(dir, entry.Name())
			if err != nil {
				return err
			}
			fi, err := toFileInfo(child, entry)
			if err != nil {
				return err
			}
			if err := visit(fi); err != nil {
				return err
			}
			if fi.IsDir {
				stack = append(stack, child)
			}
		}
	}
	return nil
}
