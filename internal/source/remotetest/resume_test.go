package remotetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"tinysync/internal/source"
)

// resumeSuiteHarness 是套件自身的语义自测装置：内存 fake Remote，
// 按契约实现 ResumableRemote（offset 流 + 指纹校验），验证
// RunResumeSuite 对正确实现的断言通过、对违约实现的断言失败。
type resumeSuiteHarness struct {
	mu       sync.Mutex
	files    map[string][]byte
	mtime    map[string]time.Time
	newRemote func(*testing.T) source.Remote // 注入违约实现；nil 走合规实现
	remotes  int
}

func newResumeSuiteHarness() *resumeSuiteHarness {
	return &resumeSuiteHarness{
		files: make(map[string][]byte),
		mtime: make(map[string]time.Time),
	}
}

func (h *resumeSuiteHarness) NewRemote(t *testing.T) source.Remote {
	h.remotes++
	if h.newRemote != nil {
		return h.newRemote(t)
	}
	return &resumeFakeRemote{h: h}
}

func (h *resumeSuiteHarness) Write(t *testing.T, logical string, content string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[logical] = []byte(content)
	// 每次覆盖推进 mtime，避免同精度时间戳导致指纹不漂移。
	h.mtime[logical] = time.Now().Add(time.Duration(h.remotes) * time.Second)
}

func (h *resumeSuiteHarness) Mkdir(t *testing.T, logical string) {}

// resumeFakeRemote 是契约合规的最小 Remote + ResumableRemote。
type resumeFakeRemote struct {
	h *resumeSuiteHarness
}

func (r *resumeFakeRemote) Stat(_ context.Context, path string) (source.FileInfo, error) {
	if err := source.ValidateLogicalPath(path); err != nil {
		return source.FileInfo{}, err
	}
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	data, ok := r.h.files[path]
	if !ok {
		return source.FileInfo{}, errors.New("not found")
	}
	return source.FileInfo{
		Path:  path,
		IsDir: false,
		Fingerprint: source.Fingerprint{
			Size:       int64(len(data)),
			ModifiedAt: r.h.mtime[path],
		},
	}, nil
}

func (r *resumeFakeRemote) List(_ context.Context, dir string, _ source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, nil
}

func (r *resumeFakeRemote) Open(_ context.Context, path string) (io.ReadCloser, error) {
	if err := source.ValidateLogicalPath(path); err != nil {
		return nil, err
	}
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	data, ok := r.h.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// OpenFrom 按契约实现：指纹（Size + ModifiedAt）不一致 →
// ErrRemoteChanged；offset 越过对象末尾 → ErrRemoteChanged。
func (r *resumeFakeRemote) OpenFrom(_ context.Context, path string, offset int64, expected source.Fingerprint) (io.ReadCloser, error) {
	if err := source.ValidateLogicalPath(path); err != nil {
		return nil, err
	}
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	data, ok := r.h.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	fp := source.Fingerprint{Size: int64(len(data)), ModifiedAt: r.h.mtime[path]}
	if fp.Size != expected.Size || !fp.ModifiedAt.Equal(expected.ModifiedAt) {
		return nil, source.ErrRemoteChanged
	}
	if offset > fp.Size {
		return nil, source.ErrRemoteChanged
	}
	return io.NopCloser(bytes.NewReader(data[offset:])), nil
}

func (r *resumeFakeRemote) Close() error { return nil }

// 套件对合规实现整体通过。
func TestResumeSuiteCompliantRemote(t *testing.T) {
	RunResumeSuite(t, newResumeSuiteHarness())
}

// noResumeHarness 装配只暴露 Remote 能力面的实现：套件跳过而不是失败。
type noResumeHarness struct{ *resumeSuiteHarness }

func (h *noResumeHarness) NewRemote(t *testing.T) source.Remote {
	return struct{ source.Remote }{h.resumeSuiteHarness.NewRemote(t)}
}

func TestResumeSuiteSkipsWithoutCapability(t *testing.T) {
	h := &noResumeHarness{newResumeSuiteHarness()}
	RunResumeSuite(t, h)
	if h.remotes == 0 {
		t.Fatal("suite never constructed a remote")
	}
}

// 哨兵错误语义：ErrRemoteChanged 是确定性失败，重试无意义。
func TestRemoteChangedNotRetryable(t *testing.T) {
	if source.IsRetryable(source.ErrRemoteChanged) {
		t.Fatal("ErrRemoteChanged is retryable, want permanent")
	}
	wrapped := fmt.Errorf("transfer %s: %w", "/a/b", source.ErrRemoteChanged)
	if source.IsRetryable(wrapped) {
		t.Fatal("wrapped ErrRemoteChanged is retryable, want permanent")
	}
}
