package webdav

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
	"tinysync/internal/source/remotetest"
)

// webdavContractHarness 把 x/net/webdav MemFS 真实协议栈接到
// remotetest 套件：httptest HTTP 服务 + 生产 Factory 构造 adapter。
// fs 在每次 NewRemote 时重建；套件单协程顺序执行（NewRemote 后
// 紧跟 seed 与断言），状态无需加锁。
type webdavContractHarness struct {
	fs xnetdav.FileSystem
}

// NewRemote 启动全新 MemFS 后端的服务并经生产 Factory 构造 Remote。
func (h *webdavContractHarness) NewRemote(t *testing.T) source.Remote {
	h.fs = xnetdav.NewMemFS()
	handler := &xnetdav.Handler{FileSystem: h.fs, LockSystem: xnetdav.NewMemLS()}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	f := NewFactory()
	remote, err := f.Create(context.Background(), source.Source{
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: srv.URL},
		},
	}, source.Credentials{})
	if err != nil {
		t.Fatalf("create webdav remote: %v", err)
	}
	return remote
}

// Write 经 MemFS 直接写入（测试装置不经被测 adapter）；逐级创建父目录。
func (h *webdavContractHarness) Write(t *testing.T, logical, content string) {
	t.Helper()
	ctx := context.Background()
	for i := 1; i < len(logical); i++ {
		if logical[i] == '/' {
			_ = h.fs.Mkdir(ctx, logical[:i], 0o755) // 已存在视为成功
		}
	}
	f, err := h.fs.OpenFile(ctx, logical, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("dav write %s: %v", logical, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("dav write %s: %v", logical, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("dav close %s: %v", logical, err)
	}
}

// Mkdir 经 MemFS 创建目录。
func (h *webdavContractHarness) Mkdir(t *testing.T, logical string) {
	t.Helper()
	if err := h.fs.Mkdir(context.Background(), logical, 0o755); err != nil {
		t.Fatalf("dav mkdir %s: %v", logical, err)
	}
}

// TestRemoteContractSuite 以统一契约套件验证 WebDAV adapter。
func TestRemoteContractSuite(t *testing.T) {
	remotetest.RunSuite(t, &webdavContractHarness{})
}
