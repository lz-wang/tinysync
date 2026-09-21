package sftp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"tinysync/internal/source"
)

// testServer 是进程内 SSH/SFTP 服务：真实 SSH 握手 + pkg/sftp server
// + 真实临时目录，用于端到端验证 adapter 的认证、host key pin 与
// 路径映射。
type testServer struct {
	hostKeyFingerprint string
	listener           net.Listener
	clientKeyPEM       string        // 供 private_key 认证使用
	clientKeySigner    ssh.Signer    // 供服务端 PublicKeyCallback 比对
	acceptDone         chan struct{} // accept 循环退出（Close 后）
	// stall 置位后吞掉服务端 → 客户端的响应数据：模拟服务端停摆
	// （收到请求但不回应），用于验证读取阻塞被 attempt 超时中断。
	stall atomic.Bool
}

const (
	testUser     = "tinysync"
	testPassword = "p@ssw0rd-中文"
	testPassphr  = "key-passphrase"
)

// startTestServer 启动进程内 SSH/SFTP 服务。pkg/sftp server 按请求的
// 绝对路径读写真实文件系统，测试用 remote_root 指向临时目录。
// 接受 testing.TB：单元测试与 benchmark（10k 目录数据集）共用。
func startTestServer(tb testing.TB) *testServer {
	tb.Helper()

	// 服务端 host key。
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		tb.Fatalf("host signer: %v", err)
	}

	// 客户端私钥（private_key 认证测试用）。
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatalf("generate client key: %v", err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		tb.Fatalf("client signer: %v", err)
	}
	encryptedBlock, err := ssh.MarshalPrivateKeyWithPassphrase(clientPriv, "tinysync-test", []byte(testPassphr))
	if err != nil {
		tb.Fatalf("marshal private key: %v", err)
	}
	clientKeyPEM := string(pem.EncodeToMemory(encryptedBlock))

	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if conn.User() == testUser && string(password) == testPassword {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("password rejected for %s", conn.User())
		},
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if conn.User() == testUser && string(key.Marshal()) == string(clientSigner.PublicKey().Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("public key rejected for %s", conn.User())
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}
	ts := &testServer{
		hostKeyFingerprint: ssh.FingerprintSHA256(hostSigner.PublicKey()),
		listener:           listener,
		clientKeyPEM:       clientKeyPEM,
		clientKeySigner:    clientSigner,
		acceptDone:         make(chan struct{}),
	}
	go ts.serve(config)
	tb.Cleanup(ts.close)
	return ts
}

// serve 接受连接：SSH 握手后在 session channel 上启动 sftp subsystem。
func (ts *testServer) serve(config *ssh.ServerConfig) {
	defer close(ts.acceptDone)
	for {
		conn, err := ts.listener.Accept()
		if err != nil {
			return
		}
		go ts.handleConn(conn, config)
	}
}

// stallableConn 在 stall 置位时吞掉写方向（服务端 → 客户端）的数据：
// 请求仍被服务端处理，但响应永不到达客户端，客户端读取阻塞。
func (c *stallableConn) Write(p []byte) (int, error) {
	if c.stall.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

type stallableConn struct {
	net.Conn
	stall *atomic.Bool
}

func (ts *testServer) handleConn(conn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(&stallableConn{Conn: conn, stall: &ts.stall}, config)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, newChannel.ChannelType())
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go func(requests <-chan *ssh.Request) {
			for req := range requests {
				if req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp" {
					req.Reply(true, nil)
					continue
				}
				req.Reply(false, nil)
			}
		}(requests)
		server, err := sftp.NewServer(channel)
		if err != nil {
			_ = channel.Close()
			continue
		}
		// Serve 返回错误（含连接关闭的 io.EOF）即收尾。
		_ = server.Serve()
		_ = server.Close()
	}
}

func (ts *testServer) close() {
	_ = ts.listener.Close()
	<-ts.acceptDone
}

// addr 返回服务地址。
func (ts *testServer) addr() string {
	return ts.listener.Addr().String()
}

// seedFile 在 root 下创建带内容的文件（自动建父目录）。
func seedFile(tb testing.TB, root, rel, content string) {
	tb.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		tb.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		tb.Fatalf("write %s: %v", rel, err)
	}
}

// sftpSourceConfig 返回指向测试服务的 SFTP Source 配置。
func sftpSourceConfig(ts *testServer, root string, auth source.SFTPAuthMethod) source.SFTPConfig {
	return source.SFTPConfig{
		Host:               hostOf(ts.addr()),
		Port:               portOf(ts.addr()),
		Username:           testUser,
		RemoteRoot:         root,
		AuthMethod:         auth,
		HostKeyFingerprint: ts.hostKeyFingerprint,
	}
}

func hostOf(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 22
	}
	n := 0
	fmt.Sscanf(p, "%d", &n)
	return n
}

// newSFTPFactoryRemote 用真实 Factory 连接测试服务。
func newSFTPFactoryRemote(tb testing.TB, ts *testServer, cfg source.SFTPConfig, creds source.Credentials) source.Remote {
	tb.Helper()
	factory := NewFactory()
	r, err := factory.Create(context.Background(), source.Source{
		Name:   "test",
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &cfg},
	}, creds)
	if err != nil {
		tb.Fatalf("Factory.Create: %v", err)
	}
	tb.Cleanup(func() { _ = r.Close() })
	return r
}

// password 认证端到端：List / Stat / Open 走真实 SSH + SFTP 通道。
func TestSFTPPasswordAuthRoundTrip(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "docs/report.txt", "sftp-content")
	seedFile(t, root, "photos/a.jpg", "jpeg")
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	ctx := context.Background()

	page, err := r.List(ctx, "/", source.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]bool{}
	for _, e := range page.Entries {
		got[e.Path+"/"+fmt.Sprint(e.IsDir)] = true
	}
	if !got["/docs/true"] || !got["/photos/true"] {
		t.Errorf("entries = %v, want /docs and /photos dirs", got)
	}

	fi, err := r.Stat(ctx, "/docs/report.txt")
	if err != nil || fi.IsDir || fi.Fingerprint.Size != int64(len("sftp-content")) {
		t.Fatalf("Stat = %+v, %v; want file with size", fi, err)
	}

	rc, err := r.Open(ctx, "/docs/report.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil || string(data) != "sftp-content" {
		t.Fatalf("read = %q, %v; want sftp-content", data, err)
	}
}

// private key 认证（含 passphrase）端到端。
func TestSFTPPrivateKeyAuth(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "data.bin", "xyz")
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPrivateKey)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		PrivateKey:           ts.clientKeyPEM,
		PrivateKeyPassphrase: testPassphr,
	}})
	page, err := r.List(context.Background(), "/", source.ListOptions{})
	if err != nil {
		t.Fatalf("List via private key: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Path != "/data.bin" {
		t.Fatalf("entries = %+v, want /data.bin", page.Entries)
	}
}

// host key fingerprint 不匹配时连接失败——绝不静默接受未知主机。
func TestSFTPHostKeyMismatchRejected(t *testing.T) {
	root := t.TempDir()
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	cfg.HostKeyFingerprint = "SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k"
	factory := NewFactory()
	_, err := factory.Create(context.Background(), source.Source{
		Name:   "test",
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &cfg},
	}, source.Credentials{SFTP: &source.SFTPCredentials{Password: testPassword}})
	if err == nil {
		t.Fatal("Create with wrong fingerprint = nil, want error")
	}
	if !containsAny(err.Error(), "mismatch", "fingerprint") {
		t.Errorf("error = %v, want fingerprint mismatch description", err)
	}
}

// 未配置 fingerprint 时，用户显式选择跳过主机密钥校验，仍可建立连接。
func TestSFTPHostKeyVerificationOptional(t *testing.T) {
	root := t.TempDir()
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	cfg.HostKeyFingerprint = ""
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	if _, err := r.List(context.Background(), "/", source.ListOptions{}); err != nil {
		t.Fatalf("List without host key verification: %v", err)
	}
}

// 密码错误认证失败。
func TestSFTPBadPasswordRejected(t *testing.T) {
	root := t.TempDir()
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	factory := NewFactory()
	_, err := factory.Create(context.Background(), source.Source{
		Name:   "test",
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &cfg},
	}, source.Credentials{SFTP: &source.SFTPCredentials{Password: "wrong"}})
	if err == nil {
		t.Fatal("Create with wrong password = nil, want error")
	}
}

// private key 与 passphrase 不匹配时报解析错误。
func TestSFTPBadPassphraseRejected(t *testing.T) {
	root := t.TempDir()
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPrivateKey)
	factory := NewFactory()
	_, err := factory.Create(context.Background(), source.Source{
		Name:   "test",
		Type:   source.TypeSFTP,
		Config: source.Config{SFTP: &cfg},
	}, source.Credentials{SFTP: &source.SFTPCredentials{
		PrivateKey:           ts.clientKeyPEM,
		PrivateKeyPassphrase: "wrong-passphrase",
	}})
	if err == nil {
		t.Fatal("Create with wrong passphrase = nil, want error")
	}
	if !errors.Is(err, source.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid (parse failure)", err)
	}
}

// symlink 拒绝：List 中出现 symlink 条目整体失败，不跳过、不跟随。
func TestSFTPSymlinkRejected(t *testing.T) {
	root := t.TempDir()
	seedFile(t, root, "real.txt", "ok")
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ts := startTestServer(t)

	cfg := sftpSourceConfig(ts, root, source.SFTPAuthPassword)
	r := newSFTPFactoryRemote(t, ts, cfg, source.Credentials{SFTP: &source.SFTPCredentials{
		Password: testPassword,
	}})
	_, err := r.List(context.Background(), "/", source.ListOptions{})
	if err == nil {
		t.Fatal("List with symlink = nil, want error")
	}
	if !errors.Is(err, source.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
