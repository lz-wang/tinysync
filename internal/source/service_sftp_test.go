// Service 连接测试的 SFTP 真实协议栈验证：dial、认证与 host key 校验
// 全部发生在 Factory.Create 内，这些失败必须归入 OK=false 的测试结论
// （REST 200），而不是作为操作失败传播（REST 5xx）。最小 SSH server
// 只需覆盖到认证阶段——失败即发生在 Create 内，不进入 SFTP 会话。
package source_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"

	"tinysync/internal/source"
	"tinysync/internal/source/sftp"
	"tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// newTestServiceWithFactory 用真实临时 SQLite 仓库与指定 factory 构造
// Service（factory 决定协议 dispatch，这里注入真实 SFTP factory）。
func newTestServiceWithFactory(t *testing.T, factory source.RemoteFactory) *source.Service {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	return source.NewService(sqlite.New(db), factory)
}

// sshAuthServer 是最小 SSH server：接受连接完成握手，认证按
// acceptPassword 拒绝或通过；通过后立即关闭——连接测试只关心
// Create 阶段的成败。
type sshAuthServer struct {
	listener           net.Listener
	hostKeyFingerprint string
}

func startSSHAuthServer(t *testing.T, acceptPassword bool) *sshAuthServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if acceptPassword {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("password rejected for %s", conn.User())
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ts := &sshAuthServer{
		listener:           listener,
		hostKeyFingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// 认证失败时 NewServerConn 返回错误；测试只到握手阶段，
			// 无论成败随即关闭连接。
			_, _, _, err = ssh.NewServerConn(conn, config)
			_ = err
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return ts
}

func (ts *sshAuthServer) addr() string { return ts.listener.Addr().String() }

func (ts *sshAuthServer) host() string {
	h, _, err := net.SplitHostPort(ts.addr())
	if err != nil {
		return ts.addr()
	}
	return h
}

func (ts *sshAuthServer) port() int {
	_, p, err := net.SplitHostPort(ts.addr())
	if err != nil {
		return 22
	}
	n := 0
	fmt.Sscanf(p, "%d", &n)
	return n
}

// sftpTestSource 返回指向给定地址的 SFTP Source（密码认证）。
func sftpTestSource(host string, port int, fingerprint, password string) source.CreateInput {
	cfg := source.SFTPConfig{
		Host:               host,
		Port:               port,
		Username:           "tinysync",
		RemoteRoot:         "/",
		AuthMethod:         source.SFTPAuthPassword,
		HostKeyFingerprint: fingerprint,
	}
	return source.CreateInput{
		Name:        "nas-sftp",
		Type:        source.TypeSFTP,
		Config:      source.Config{SFTP: &cfg},
		Credentials: source.Credentials{SFTP: &source.SFTPCredentials{Password: password}},
		Enabled:     true,
	}
}

// mustCreate 创建 Source 供测试使用。
func mustCreate(t *testing.T, svc *source.Service, input source.CreateInput) string {
	t.Helper()
	created, err := svc.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return created.ID
}

// 错误密码：认证失败发生在 Factory.Create 内，测试结论是 OK=false。
func TestConnectionSFTPBadPassword(t *testing.T) {
	ts := startSSHAuthServer(t, false)
	svc := newTestServiceWithFactory(t, sftp.NewFactory())
	id := mustCreate(t, svc, sftpTestSource(ts.host(), ts.port(), ts.hostKeyFingerprint, "wrong-password"))

	result, err := svc.TestConnection(context.Background(), id)
	if err != nil {
		t.Fatalf("TestConnection returned error: %v", err)
	}
	if result.OK {
		t.Error("result.OK = true, want false")
	}
	if result.Error == "" {
		t.Error("result.Error empty, want auth failure reason")
	}
}

// host key 指纹不匹配：校验发生在 Factory.Create 内，测试结论是
// OK=false。
func TestConnectionSFTPHostKeyMismatch(t *testing.T) {
	ts := startSSHAuthServer(t, true)
	svc := newTestServiceWithFactory(t, sftp.NewFactory())
	// 正确密码 + 一个不属于该服务器的指纹（由独立密钥生成）。
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate unrelated key: %v", err)
	}
	otherSigner, err := ssh.NewSignerFromKey(otherPriv)
	if err != nil {
		t.Fatalf("unrelated signer: %v", err)
	}
	id := mustCreate(t, svc, sftpTestSource(
		ts.host(), ts.port(), ssh.FingerprintSHA256(otherSigner.PublicKey()), "right-password"))

	result, err := svc.TestConnection(context.Background(), id)
	if err != nil {
		t.Fatalf("TestConnection returned error: %v", err)
	}
	if result.OK {
		t.Error("result.OK = true, want false")
	}
	if result.Error == "" {
		t.Error("result.Error empty, want host key mismatch reason")
	}
}

// 连接被拒绝（无监听端口）：dial 失败发生在 Factory.Create 内，测试
// 结论是 OK=false。
func TestConnectionSFTPRefused(t *testing.T) {
	svc := newTestServiceWithFactory(t, sftp.NewFactory())
	// 先监听拿到端口再关闭：保证是不可达的本机地址。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, portRaw, _ := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	port := 0
	fmt.Sscanf(portRaw, "%d", &port)
	// 指纹本身必须合法（base64）：由独立密钥生成。
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate unrelated key: %v", err)
	}
	otherSigner, err := ssh.NewSignerFromKey(otherPriv)
	if err != nil {
		t.Fatalf("unrelated signer: %v", err)
	}

	id := mustCreate(t, svc, sftpTestSource(
		host, port, ssh.FingerprintSHA256(otherSigner.PublicKey()), "any-password"))

	result, err := svc.TestConnection(context.Background(), id)
	if err != nil {
		t.Fatalf("TestConnection returned error: %v", err)
	}
	if result.OK {
		t.Error("result.OK = true, want false")
	}
	if result.Error == "" {
		t.Error("result.Error empty, want dial failure reason")
	}
}
