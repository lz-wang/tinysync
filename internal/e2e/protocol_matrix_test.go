package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
	s3adapter "tinysync/internal/source/s3"
	sftpadapter "tinysync/internal/source/sftp"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/source/webdav"
	"tinysync/internal/syncjob"
	jobsqlite "tinysync/internal/syncjob/sqlite"
)

// 协议矩阵：三种协议经过同一个 Remote → Scanner → Selector → Planner
// → Engine → Local Files 链路运行完全相同的同步场景。scenario 内禁止
// 出现任何协议分支——协议差异全部封装在本文件的 fixture 中。
// S3 fixture 为进程内协议模拟（真实 S3 服务由 integration gate /
// MinIO 验证），WebDAV 与 SFTP 走真实协议栈。

// matrixRemote 是一个协议 fixture：可编程的远端数据面 + 「打开一个
// 新的 Remote 连接」。与生产语义一致：Runner 每轮运行经 OpenRemote
// 获得独立连接，用毕 Close；fixture 不得共享单个 Remote 实例
// （有连接生命周期的协议在首轮结束后会被正确关闭）。
type matrixRemote struct {
	name       string
	put        func(t *testing.T, logical, content string)
	remove     func(t *testing.T, logical string)
	openRemote func() (source.Remote, error)
}

// protocolCase 是每个协议的 fixture 构造入口。
type protocolCase struct {
	name    string
	fixture func(t *testing.T) matrixRemote
}

func protocolFixtures() []protocolCase {
	return []protocolCase{
		{name: "webdav", fixture: newWebDAVFixture},
		{name: "s3", fixture: newS3Fixture},
		{name: "sftp", fixture: newSFTPFixture},
	}
}

// --- WebDAV fixture：真实 HTTP/WebDAV 协议栈（x/net/webdav server）。

type webdavFixture struct {
	fs  xnetdav.FileSystem
	srv *davServer
}

func newWebDAVFixture(t *testing.T) matrixRemote {
	dav := startDAV(t)
	// MemFS 需要显式创建目录。
	if err := dav.fs.Mkdir(context.Background(), "/docs", 0o755); err != nil {
		t.Fatalf("dav mkdir /docs: %v", err)
	}
	factory := webdav.NewFactory()
	r, err := factory.Create(context.Background(), source.Source{
		Name: "matrix",
		Type: source.TypeWebDAV,
		Config: source.Config{WebDAV: &source.WebDAVConfig{
			Endpoint: dav.srv.URL,
		}},
	}, source.Credentials{})
	if err != nil {
		t.Fatalf("webdav factory: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return matrixRemote{
		name: "webdav",
		put: func(t *testing.T, logical, content string) {
			dav.writeFile(t, logical, content)
		},
		remove: dav.remove,
		openRemote: func() (source.Remote, error) {
			return factory.Create(context.Background(), source.Source{
				Name: "matrix",
				Type: source.TypeWebDAV,
				Config: source.Config{WebDAV: &source.WebDAVConfig{
					Endpoint: dav.srv.URL,
				}},
			}, source.Credentials{})
		},
	}
}

// --- S3 fixture：进程内协议模拟（实现 adapter 的最小能力面）。

type s3Fixture struct {
	objects map[string]string
}

func (f *s3Fixture) key(logical string) string {
	return strings.TrimPrefix(logical, "/")
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func awsString(s string) *string { return &s }

func awsInt64(n int64) *int64 { return &n }

func nopReader(content string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(content))
}

func (f *s3Fixture) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	prefix := deref(params.Prefix)
	delim := deref(params.Delimiter)
	var contents []types.Object
	dirs := map[string]bool{}
	for key := range f.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(key, prefix)
		if delim != "" && strings.Contains(rest, delim) {
			idx := strings.Index(rest, delim)
			dirs[rest[:idx+len(delim)]] = true
			continue
		}
		keyCopy := key
		mod := time.Unix(1757879400, 0).UTC()
		contents = append(contents, types.Object{
			Key:          &keyCopy,
			Size:         awsInt64(int64(len(f.objects[key]))),
			ETag:         awsString(`"` + key + `"`),
			LastModified: &mod,
		})
	}
	var common []types.CommonPrefix
	for d := range dirs {
		p := prefix + d
		common = append(common, types.CommonPrefix{Prefix: &p})
	}
	// 与真实 S3 一致：keys + common prefixes 统一字典序分页；
	// MaxKeys / ContinuationToken 生效（token 为 opaque offset），
	// 无 MaxKeys 时单页全量返回（同步场景行为不变）。
	items := make([]string, 0, len(contents)+len(common))
	for _, obj := range contents {
		items = append(items, deref(obj.Key))
	}
	for _, cp := range common {
		items = append(items, deref(cp.Prefix))
	}
	sort.Strings(items)

	start := 0
	if token := deref(params.ContinuationToken); token != "" {
		if _, err := fmt.Sscanf(token, "offset-%d", &start); err != nil {
			return nil, &matrixNotFound{}
		}
	}
	limit := len(items)
	if params.MaxKeys != nil && *params.MaxKeys > 0 && int(*params.MaxKeys) < limit {
		limit = int(*params.MaxKeys)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	pageKeys := map[string]bool{}
	for _, key := range items[start:end] {
		pageKeys[key] = true
	}

	pageContents := make([]types.Object, 0, len(contents))
	for _, obj := range contents {
		if pageKeys[deref(obj.Key)] {
			pageContents = append(pageContents, obj)
		}
	}
	pageCommon := make([]types.CommonPrefix, 0, len(common))
	for _, cp := range common {
		if pageKeys[deref(cp.Prefix)] {
			pageCommon = append(pageCommon, cp)
		}
	}
	out := &s3.ListObjectsV2Output{
		Contents:       pageContents,
		CommonPrefixes: pageCommon,
		MaxKeys:        params.MaxKeys,
	}
	if end < len(items) {
		truncated := true
		out.IsTruncated = &truncated
		next := fmt.Sprintf("offset-%d", end)
		out.NextContinuationToken = &next
	}
	return out, nil
}

func (f *s3Fixture) HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	content, ok := f.objects[deref(params.Key)]
	if !ok {
		return nil, &matrixNotFound{}
	}
	mod := time.Unix(1757879400, 0).UTC()
	return &s3.HeadObjectOutput{
		ContentLength: awsInt64(int64(len(content))),
		LastModified:  &mod,
		ETag:          awsString(`"` + deref(params.Key) + `"`),
	}, nil
}

func (f *s3Fixture) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	content, ok := f.objects[deref(params.Key)]
	if !ok {
		return nil, &matrixNotFound{}
	}
	return &s3.GetObjectOutput{Body: nopReader(content)}, nil
}

type matrixNotFound struct{}

func (e *matrixNotFound) Error() string { return "NoSuchKey" }

func (e *matrixNotFound) HTTPStatusCode() int { return 404 }

func newS3Fixture(t *testing.T) matrixRemote {
	f := &s3Fixture{objects: map[string]string{}}
	return matrixRemote{
		name: "s3",
		put: func(t *testing.T, logical, content string) {
			f.objects[f.key(logical)] = content
		},
		remove: func(t *testing.T, logical string) {
			delete(f.objects, f.key(logical))
		},
		openRemote: func() (source.Remote, error) {
			return s3adapter.NewRemoteWithAPI(f, "matrix-bucket", ""), nil
		},
	}
}

// --- SFTP fixture：真实 SSH/SFTP 协议栈（进程内 server + 真实目录）。

func newSFTPFixture(t *testing.T) matrixRemote {
	root := t.TempDir()
	ts := startMatrixSFTP(t)
	cfg := source.SFTPConfig{
		Host:               matrixHost(ts.addr()),
		Port:               matrixPort(ts.addr()),
		Username:           matrixUser,
		RemoteRoot:         root,
		AuthMethod:         source.SFTPAuthPassword,
		HostKeyFingerprint: ts.fingerprint,
	}
	factory := sftpadapter.NewFactory()
	return matrixRemote{
		name: "sftp",
		put: func(t *testing.T, logical, content string) {
			matrixSeed(t, root, logical, content)
		},
		remove: func(t *testing.T, logical string) {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(logical, "/")))); err != nil && !os.IsNotExist(err) {
				t.Fatalf("sftp remove %s: %v", logical, err)
			}
		},
		openRemote: func() (source.Remote, error) {
			return factory.Create(context.Background(), source.Source{
				Name:   "matrix",
				Type:   source.TypeSFTP,
				Config: source.Config{SFTP: &cfg},
			}, source.Credentials{SFTP: &source.SFTPCredentials{Password: matrixPassword}})
		},
	}
}

// --- 共享 scenario。

// matrixEnv 是一台协议无关的同步环境：真实 SQLite + Runner。
type matrixEnv struct {
	dataDir string
	jobRepo *jobsqlite.Repository
	managed *jobsqlite.ManagedRepository
	runner  *syncjob.Runner
	runs    *jobsqlite.RunRepository
}

func newMatrixEnv(t *testing.T, fixture matrixRemote) *matrixEnv {
	t.Helper()
	dataDir := t.TempDir()
	db := openDB(t, dataDir)
	t.Cleanup(func() { _ = db.Close() })
	// Job 表的 FK 引用 Source：经真实 Source repository 落一行矩阵
	// Source（协议无关——Runner 只消费 gateway 提供的 Remote）。
	srcRepo := sourcesqlite.New(db)
	now := time.Unix(1757879400, 0).UTC()
	if err := srcRepo.Create(context.Background(), source.Source{
		ID:              "src_matrix",
		Name:            "matrix",
		Type:            source.TypeWebDAV,
		Config:          source.Config{WebDAV: &source.WebDAVConfig{Endpoint: "https://matrix.invalid/dav"}},
		CredentialState: source.CredentialState{WebDAV: &source.WebDAVCredentialState{}},
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, source.Credentials{}); err != nil {
		t.Fatalf("create matrix source row: %v", err)
	}
	gw := matrixGateway{src: source.Source{
		ID: "src_matrix", Name: "matrix", Type: source.TypeWebDAV, Enabled: true,
	}, openRemote: fixture.openRemote}
	env := &matrixEnv{
		dataDir: dataDir,
		jobRepo: jobsqlite.NewRepository(db),
		managed: jobsqlite.NewManagedRepository(db),
		runs:    jobsqlite.NewRunRepository(db),
	}
	env.runner = syncjob.NewRunner(env.jobRepo, env.managed, gw, env.runs)
	return env
}

func newMatrixJob(t *testing.T, e *matrixEnv, localRoot, mode string) syncjob.Job {
	t.Helper()
	id, err := syncjob.NewID()
	if err != nil {
		t.Fatalf("new job id: %v", err)
	}
	now := time.Unix(1757879400, 0).UTC()
	job := syncjob.Job{
		ID: id, Name: "job-" + id, SourceID: "src_matrix",
		RemoteRoot: "/", LocalRoot: localRoot,
		Mode: syncjob.Mode(mode), Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.jobRepo.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return job
}

func runAndWait(t *testing.T, e *matrixEnv, jobID string) syncjob.RunStats {
	t.Helper()
	runID, err := e.runner.Start(context.Background(), jobID)
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
	return status.Stats
}

// TestSyncAcrossProtocols 是 v0.5 的核心验收：三协议同场景全通过，
// scenario 内零协议分支。
func TestSyncAcrossProtocols(t *testing.T) {
	for _, tc := range protocolFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			runCommonSyncScenario(t, tc.fixture(t))
		})
	}
}

// runCommonSyncScenario 覆盖：initial pull、unchanged、update、
// selector、Copy remove 保留、Mirror remove 删除、history 记录。
// 本函数与协议实现零耦合。
func runCommonSyncScenario(t *testing.T, remote matrixRemote) {
	ctx := context.Background()
	e := newMatrixEnv(t, remote)
	localRoot := t.TempDir()

	// 远端初始数据：根文件 + 子目录文件。
	remote.put(t, "/a.txt", "v1")
	remote.put(t, "/docs/b.txt", "b1")
	remote.put(t, "/docs/photo.jpg", "jpeg")

	job := newMatrixJob(t, e, localRoot, "mirror")

	// 1. initial pull。
	stats := runAndWait(t, e, job.ID)
	if stats.FilesCreated != 3 || stats.BytesTransferred != int64(len("v1")+len("b1")+len("jpeg")) {
		t.Fatalf("initial stats = %+v, want 3 created", stats)
	}
	assertLocalFile(t, localRoot, "a.txt", "v1")
	assertLocalFile(t, localRoot, filepath.Join("docs", "b.txt"), "b1")

	// 2. unchanged：再跑一轮无变化。
	stats = runAndWait(t, e, job.ID)
	if stats.FilesCreated != 0 || stats.FilesUpdated != 0 || stats.FilesDeleted != 0 {
		t.Fatalf("unchanged stats = %+v, want zero changes", stats)
	}

	// 3. update：远端内容变化 → update。
	remote.put(t, "/a.txt", "v2-longer-content")
	stats = runAndWait(t, e, job.ID)
	if stats.FilesUpdated != 1 {
		t.Fatalf("update stats = %+v, want 1 updated", stats)
	}
	assertLocalFile(t, localRoot, "a.txt", "v2-longer-content")

	// 4. selector：include 只同步匹配文件。
	selectorRoot := t.TempDir()
	selectorJob := newMatrixJobWithSelector(t, e, selectorRoot, "copy", []string{"docs/*.txt"}, nil)
	stats = runAndWait(t, e, selectorJob.ID)
	if stats.FilesCreated != 1 {
		t.Fatalf("selector stats = %+v, want 1 created (docs/b.txt only)", stats)
	}
	assertLocalFile(t, selectorRoot, filepath.Join("docs", "b.txt"), "b1")
	assertNoLocalFile(t, selectorRoot, "a.txt")

	// 5. Copy remove：远端删除后 Copy 保留本地。
	remote.remove(t, "/docs/photo.jpg")
	copyStats := runAndWait(t, e, selectorJob.ID)
	if copyStats.FilesDeleted != 0 {
		t.Fatalf("copy stats = %+v, want 0 deleted", copyStats)
	}

	// 6. Mirror remove：远端删除 b.txt 与 photo.jpg 后 Mirror 删除两个
	// 本地 managed 文件；a.txt unchanged 计入 skipped。
	remote.remove(t, "/docs/b.txt")
	stats = runAndWait(t, e, job.ID)
	if stats.FilesDeleted != 2 || stats.FilesSkipped != 1 {
		t.Fatalf("mirror stats = %+v, want 2 deleted + 1 unchanged-skipped", stats)
	}
	assertNoLocalFile(t, localRoot, filepath.Join("docs", "b.txt"))

	// 7. history：run / run items 记录该轮运行。
	runs, total, err := e.runs.List(ctx, syncjob.RunFilter{JobID: job.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if total < 4 {
		t.Fatalf("run history total = %d, want >= 4 (initial/unchanged/update/remove)", total)
	}
	last := runs[0]
	if last.State != syncjob.RunSucceeded {
		t.Fatalf("latest run = %s, want succeeded", last.State)
	}
	items, _, err := e.runs.Items(ctx, last.ID, 10, 0)
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	foundDelete := false
	for _, item := range items {
		if item.Action == syncjob.ItemDelete && item.Status == syncjob.ItemSucceeded &&
			strings.HasSuffix(item.Path, "b.txt") {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("items = %+v, want successful delete of b.txt", items)
	}
}

func newMatrixJobWithSelector(t *testing.T, e *matrixEnv, localRoot, mode string, include, exclude []string) syncjob.Job {
	t.Helper()
	job := newMatrixJob(t, e, localRoot, mode)
	job.Include = include
	job.Exclude = exclude
	if err := e.jobRepo.Update(context.Background(), job); err != nil {
		t.Fatalf("update job selector: %v", err)
	}
	return job
}

func assertLocalFile(t *testing.T, root, rel, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read local %s: %v", rel, err)
	}
	if string(data) != want {
		t.Fatalf("local %s = %q, want %q", rel, data, want)
	}
}

func assertNoLocalFile(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Fatalf("local %s should not exist (err = %v)", rel, err)
	}
}

// --- SFTP in-process server（矩阵专用最小装配）。

const (
	matrixUser     = "tinysync"
	matrixPassword = "matrix-password"
)

// matrixSFTPServer 是进程内 SSH/SFTP 服务：真实握手 + pkg/sftp server。
type matrixSFTPServer struct {
	listener    net.Listener
	fingerprint string
	acceptDone  chan struct{}
}

func startMatrixSFTP(t *testing.T) *matrixSFTPServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if conn.User() == matrixUser && string(password) == matrixPassword {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("rejected %s", conn.User())
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ts := &matrixSFTPServer{
		listener:    listener,
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		acceptDone:  make(chan struct{}),
	}
	go ts.serve(config)
	t.Cleanup(ts.close)
	return ts
}

func (ts *matrixSFTPServer) addr() string { return ts.listener.Addr().String() }

func (ts *matrixSFTPServer) serve(config *ssh.ServerConfig) {
	defer close(ts.acceptDone)
	for {
		conn, err := ts.listener.Accept()
		if err != nil {
			return
		}
		go ts.handleConn(conn, config)
	}
}

func (ts *matrixSFTPServer) handleConn(conn net.Conn, config *ssh.ServerConfig) {
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

func (ts *matrixSFTPServer) close() {
	_ = ts.listener.Close()
	<-ts.acceptDone
}

func matrixHost(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

func matrixPort(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 22
	}
	n := 22
	fmt.Sscanf(p, "%d", &n)
	return n
}

// matrixGateway 是 syncjob.SourceGateway 的矩阵实现：协议矩阵不经
// Source service，每次 OpenRemote 经 fixture 建立独立连接。
type matrixGateway struct {
	src        source.Source
	openRemote func() (source.Remote, error)
}

func (g matrixGateway) Get(ctx context.Context, id string) (source.Source, error) {
	return g.src, nil
}

func (g matrixGateway) OpenRemote(ctx context.Context, id string) (source.Source, source.Remote, error) {
	r, err := g.openRemote()
	if err != nil {
		return source.Source{}, nil, err
	}
	return g.src, r, nil
}

func matrixSeed(t *testing.T, root, logical, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(logical, "/")))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", logical, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", logical, err)
	}
}
