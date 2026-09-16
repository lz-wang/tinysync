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

	client, err := dial(ctx, addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("sftp dial %s: %w", addr, err)
	}
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("sftp open session on %s: %w", addr, err)
	}
	return &remote{
		ssh:  client,
		sftp: sftpClient,
		root: path.Clean(cfg.RemoteRoot),
	}, nil
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
// expected 必须是 SHA256:<base64> 形式（校验层保证）。
func fingerprintCallback(expected string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if got != expected {
			return fmt.Errorf("host key fingerprint mismatch for %s: got %s, want %s", hostname, got, expected)
		}
		return nil
	}
}

// dial 建立 SSH 连接：TCP dial 与握手分别限时，ctx 取消立即中断
// （阻塞中的 dial / 握手随 runCtx 取消退出）。
func dial(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
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
type remote struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	// root 是远端绝对路径的 Source root（已 path.Clean）。
	root string
}

// 编译期断言。
var _ source.Remote = (*remote)(nil)

// remoteAbs 把 Source-relative logical path 映射为远端绝对路径
// （全部 path 语义，不用 filepath）。
func (r *remote) remoteAbs(logicalPath string) string {
	cleaned := path.Clean(logicalPath)
	if cleaned == "/" {
		return r.root
	}
	return r.root + cleaned
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

// Stat 实现 source.Remote：Lstat 不跟随 symlink。
func (r *remote) Stat(ctx context.Context, logicalPath string) (source.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return source.FileInfo{}, err
	}
	info, err := r.sftp.Lstat(r.remoteAbs(logicalPath))
	if err != nil {
		return source.FileInfo{}, wrapOp("stat", logicalPath, err)
	}
	return r.toFileInfo(logicalPath, info)
}

// List 实现 source.Remote：ReadDir 列一层；发现 symlink 整体失败——
// 简单 skip 会得到不完整的 remote snapshot，Mirror 可能据此误删
// 本地 managed 文件。
func (r *remote) List(ctx context.Context, logicalDir string) ([]source.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := r.sftp.ReadDir(r.remoteAbs(logicalDir))
	if err != nil {
		return nil, wrapOp("list", logicalDir, err)
	}
	out := make([]source.FileInfo, 0, len(entries))
	for _, entry := range entries {
		logical, err := r.toLogical(logicalDir, entry.Name())
		if err != nil {
			return nil, wrapOp("list", logicalDir, err)
		}
		fi, err := r.toFileInfo(logical, entry)
		if err != nil {
			return nil, err
		}
		out = append(out, fi)
	}
	return out, nil
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

// Open 实现 source.Remote。
func (r *remote) Open(ctx context.Context, logicalPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := r.sftp.Open(r.remoteAbs(logicalPath))
	if err != nil {
		return nil, wrapOp("open", logicalPath, err)
	}
	return f, nil
}

// Close 实现 source.Remote：关闭 SFTP 会话与 SSH 连接，阻塞中的
// 读取随连接关闭退出。
func (r *remote) Close() error {
	sftpErr := r.sftp.Close()
	if err := r.ssh.Close(); err != nil && sftpErr == nil {
		sftpErr = err
	}
	return sftpErr
}

// wrapOp 为底层错误补充操作与路径上下文；ctx 超时/取消经 %w 保持
// 可判定。
func wrapOp(op, logicalPath string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("sftp %s %s: %w", op, logicalPath, err)
}
