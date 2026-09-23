// SFTP 凭据引用端到端：凭据库 → 引用态 Source → 真实 Runner 同步，
// 全链路走真实 SSH 公钥认证。覆盖：引用态同步成功、凭据换钥对全部
// 引用源自动生效、解绑回内联钥继续同步（票 #3）。
package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"tinysync/internal/credential"
	credentialsqlite "tinysync/internal/credential/sqlite"
	"tinysync/internal/source"
	sftpadapter "tinysync/internal/source/sftp"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// refSFTPServer 是公钥认证的进程内 SSH/SFTP 服务：只接受 authorized
// 公钥（可编程替换，模拟服务器侧换钥）。
type refSFTPServer struct {
	listener    net.Listener
	mu          sync.Mutex
	authorized  ssh.PublicKey
	fingerprint string
	acceptDone  chan struct{}
	root        string
}

func startRefSFTP(t *testing.T, initial ssh.PublicKey) *refSFTPServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	ts := &refSFTPServer{
		authorized:  initial,
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		acceptDone:  make(chan struct{}),
		root:        t.TempDir(),
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			ts.mu.Lock()
			defer ts.mu.Unlock()
			if ts.authorized != nil && keysEqual(key, ts.authorized) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("public key rejected for %s", conn.User())
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ts.listener = listener
	go ts.serve(config)
	t.Cleanup(func() { _ = listener.Close(); <-ts.acceptDone })
	return ts
}

func keysEqual(a, b ssh.PublicKey) bool {
	return string(a.Marshal()) == string(b.Marshal()) && a.Type() == b.Type()
}

func (ts *refSFTPServer) setAuthorized(key ssh.PublicKey) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.authorized = key
}

func (ts *refSFTPServer) addr() string { return ts.listener.Addr().String() }

func (ts *refSFTPServer) serve(config *ssh.ServerConfig) {
	defer close(ts.acceptDone)
	for {
		conn, err := ts.listener.Accept()
		if err != nil {
			return
		}
		go ts.handleConn(conn, config)
	}
}

func (ts *refSFTPServer) handleConn(conn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
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
		_ = server.Serve()
		_ = server.Close()
	}
}

// refEnv 是引用 e2e 的真实装配：真实 SQLite + 凭据服务 + Source 服务
// （带 resolver）+ 真实 Runner（经 Source 服务构造远端客户端）。
type refEnv struct {
	sources     *source.Service
	credentials *credential.Service
	runner      *syncjob.Runner
	jobRepo     *jobsqlite.Repository
	server      *refSFTPServer
	sourceID    string
}

func newRefEnv(t *testing.T, authorized ssh.PublicKey) *refEnv {
	t.Helper()
	dataDir := t.TempDir()
	db := openDB(t, dataDir)
	credentials := credential.NewService(credentialsqlite.New(db))
	sources := source.NewService(sourcesqlite.New(db), sftpadapter.NewFactory())
	sources.Credentials = credentials
	jobRepo := jobsqlite.NewRepository(db)
	runner := syncjob.NewRunner(jobRepo, jobsqlite.NewManagedRepository(db), sources, jobsqlite.NewRunRepository(db))
	return &refEnv{
		sources:     sources,
		credentials: credentials,
		runner:      runner,
		jobRepo:     jobRepo,
		server:      startRefSFTP(t, authorized),
	}
}

// newClientKey 生成 ed25519 客户端钥匙：私钥 openssh PEM + 服务器侧公钥。
func newClientKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "tinysync e2e")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), sshPub
}

// createReferencingSource 经真实服务建引用态 SFTP 源。
func (e *refEnv) createReferencingSource(t *testing.T, credentialID string) {
	t.Helper()
	host, port := splitHostPort(e.server.addr())
	src, err := e.sources.Create(context.Background(), source.CreateInput{
		Name:    "引用源",
		Type:    source.TypeSFTP,
		Enabled: true,
		Config: source.Config{SFTP: &source.SFTPConfig{
			Host:               host,
			Port:               port,
			Username:           "tinysync",
			RemoteRoot:         e.server.root,
			AuthMethod:         source.SFTPAuthPrivateKey,
			HostKeyFingerprint: e.server.fingerprint,
			CredentialID:       credentialID,
		}},
	})
	if err != nil {
		t.Fatalf("create referencing source: %v", err)
	}
	e.sourceID = src.ID
}

// runJob 建任务并跑一轮，断言成功。
func (e *refEnv) runJob(t *testing.T, localRoot string) {
	t.Helper()
	id, err := syncjob.NewID()
	if err != nil {
		t.Fatalf("new job id: %v", err)
	}
	now := time.Now().UTC()
	job := syncjob.Job{
		ID: id, Name: "job-" + id, SourceID: e.sourceID,
		RemoteRoot: "/", LocalRoot: localRoot,
		Mode: syncjob.ModeCopy, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.jobRepo.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := e.runner.Start(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	status, err := e.runner.Wait(context.Background(), runID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if status.State != syncjob.RunSucceeded {
		t.Fatalf("run state = %s (%s), want succeeded", status.State, status.Error)
	}
}

// TestSFTPCredentialReferenceEndToEnd：引用态真实同步 → 凭据换钥自动
// 生效（服务器同时拒收旧钥）→ 解绑回内联钥继续同步。
func TestSFTPCredentialReferenceEndToEnd(t *testing.T) {
	ctx := context.Background()
	pem1, pub1 := newClientKey(t)

	e := newRefEnv(t, pub1)
	key1, err := e.credentials.Create(ctx, credential.CreateInput{
		Name:   "NAS 钥匙",
		Type:   credential.TypeSSHKey,
		Secret: credential.Secret{PrivateKey: pem1},
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	e.createReferencingSource(t, key1.ID)

	// 首轮：引用态公钥认证同步成功。
	writeRemoteFileE2E(t, e.server.root, "docs/a.txt", "alpha")
	localA := t.TempDir()
	e.runJob(t, localA)
	assertLocalFile(t, localA, "docs/a.txt", "alpha")

	// 换钥：凭据换新钥 + 服务器只认新钥 → 下轮对引用源自动生效。
	pem2, pub2 := newClientKey(t)
	if _, err := e.credentials.Update(ctx, key1.ID, credential.UpdateInput{
		Secret: &credential.Secret{PrivateKey: pem2},
	}); err != nil {
		t.Fatalf("rekey credential: %v", err)
	}
	e.server.setAuthorized(pub2)
	writeRemoteFileE2E(t, e.server.root, "docs/b.txt", "beta")
	localB := t.TempDir()
	e.runJob(t, localB)
	assertLocalFile(t, localB, "docs/b.txt", "beta")

	// 解绑回内联钥：config 清空 credential_id，内联写入 pem2。
	src, err := e.sources.Get(ctx, e.sourceID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	sftpCfg := *src.Config.SFTP
	sftpCfg.CredentialID = ""
	if _, err := e.sources.Update(ctx, e.sourceID, source.UpdateInput{
		Config: &source.Config{SFTP: &sftpCfg},
		Credentials: &source.CredentialsUpdate{SFTP: &source.SFTPCredentialsUpdate{
			PrivateKey: &pem2,
		}},
	}); err != nil {
		t.Fatalf("unbind to inline key: %v", err)
	}
	writeRemoteFileE2E(t, e.server.root, "docs/c.txt", "gamma")
	localC := t.TempDir()
	e.runJob(t, localC)
	assertLocalFile(t, localC, "docs/c.txt", "gamma")
}

// writeRemoteFileE2E 直接落文件到 SFTP 服务根目录（进程内 server 以
// 真实文件系统为数据面）。
func writeRemoteFileE2E(t *testing.T, root, logical, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(logical, "/")))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", target, err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}
}

func splitHostPort(addr string) (string, int) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 22
	}
	n := 22
	_, _ = fmt.Sscanf(p, "%d", &n)
	return h, n
}
