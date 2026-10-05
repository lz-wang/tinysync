package webdav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	xnetdav "golang.org/x/net/webdav"

	"tinysync/internal/source"
	"tinysync/internal/source/remotetest"
)

// rangeCapableWebDAV 包装 x/net/webdav：PROPFIND / MKCOL 走原
// handler，GET / HEAD 改由标准库 http.ServeContent 服务——Range /
// If-Range / 206 / Content-Range 语义与 nginx dav / Apache / rclone
// 等支持 Range 的真实 WebDAV 服务一致（x/net/webdav 自身不支持
// Range，该形态由 Downloader 的 unsupported 降级路径覆盖）。
func rangeCapableWebDAV(fs xnetdav.FileSystem) http.Handler {
	inner := &xnetdav.Handler{FileSystem: fs, LockSystem: xnetdav.NewMemLS()}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			inner.ServeHTTP(w, r)
			return
		}
		f, err := fs.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			http.Error(w, "stat failed", http.StatusInternalServerError)
			return
		}
		if info.IsDir() {
			_ = f.Close()
			http.Error(w, "is a directory", http.StatusBadRequest)
			return
		}
		if rs, ok := f.(io.ReadSeeker); ok {
			defer func() { _ = f.Close() }()
			// GET 响应携带与 PROPFIND getetag 一致的 ETag
			//（x/net/webdav findETag 的生成规则）：真实服务两者一致，
			// If-Range 的匹配语义依赖这一点。
			w.Header().Set("ETag", fmt.Sprintf(`"%x%x"`, info.ModTime().UnixNano(), info.Size()))
			http.ServeContent(w, r, info.Name(), info.ModTime(), rs)
			return
		}
		data, readErr := io.ReadAll(f)
		_ = f.Close()
		if readErr != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
	})
}

// webdavResumeHarness 与 contract harness 共用 MemFS 数据装置，但
// HTTP 栈支持 Range。
type webdavResumeHarness struct {
	inner webdavContractHarness
}

func (h *webdavResumeHarness) NewRemote(t *testing.T) source.Remote {
	h.inner.fs = xnetdav.NewMemFS()
	srv := httptest.NewServer(rangeCapableWebDAV(h.inner.fs))
	t.Cleanup(srv.Close)
	remote, err := NewFactory().Create(context.Background(), source.Source{
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

func (h *webdavResumeHarness) Write(t *testing.T, logical, content string) {
	h.inner.Write(t, logical, content)
}

func (h *webdavResumeHarness) Mkdir(t *testing.T, logical string) {
	h.inner.Mkdir(t, logical)
}

// TestRemoteResumeContractSuite：Range-capable 后端上的断点续传契约。
func TestRemoteResumeContractSuite(t *testing.T) {
	remotetest.RunResumeSuite(t, &webdavResumeHarness{})
}

// 不支持 Range 的 WebDAV 服务（GET 剥离 Range 头返回 200 全量）：
// OpenFrom 经 Stat 复核确认指纹未变后降级 ErrResumeUnsupported，
// 绝不把完整 body 冒充 offset 流。
func TestOpenFromFallsBackWhenServerIgnoresRange(t *testing.T) {
	h := &webdavResumeHarness{inner: webdavContractHarness{fs: xnetdav.NewMemFS()}}
	// 包装：剥掉 GET 的 Range 头，模拟 range-less 服务器。
	rangeless := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			r = r.Clone(r.Context())
			r.Header.Del("Range")
		}
		rangeCapableWebDAV(h.inner.fs).ServeHTTP(w, r)
	})
	srv := httptest.NewServer(rangeless)
	t.Cleanup(srv.Close)
	remote, err := NewFactory().Create(context.Background(), source.Source{
		Type:   source.TypeWebDAV,
		Config: source.Config{WebDAV: &source.WebDAVConfig{Endpoint: srv.URL}},
	}, source.Credentials{})
	if err != nil {
		t.Fatalf("create remote: %v", err)
	}
	rr, ok := remote.(source.ResumableRemote)
	if !ok {
		t.Fatal("webdav remote does not implement ResumableRemote")
	}
	h.Write(t, "/resume.bin", strings.Repeat("x", 100))
	fi, err := remote.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	_, err = rr.OpenFrom(context.Background(), "/resume.bin", 10, fi.Fingerprint)
	if !errors.Is(err, source.ErrResumeUnsupported) {
		t.Fatalf("OpenFrom on range-less server = %v, want ErrResumeUnsupported", err)
	}
}

// 416（offset 越过当前资源末尾）映射为 ErrRemoteChanged：远端缩小。
func TestOpenFromRangeNotSatisfiable(t *testing.T) {
	h := &webdavResumeHarness{}
	r := h.NewRemote(t)
	defer func() { _ = r.Close() }()
	rr := r.(source.ResumableRemote)
	h.Write(t, "/resume.bin", strings.Repeat("x", 100))
	fi, err := r.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	_, err = rr.OpenFrom(context.Background(), "/resume.bin", 200, fi.Fingerprint)
	if !errors.Is(err, source.ErrRemoteChanged) {
		t.Fatalf("OpenFrom(200) on 100-byte object = %v, want ErrRemoteChanged", err)
	}
}

// 认证经由 webdav.HTTPClient 包装传递：OpenFrom 的直接 GET 同样携带
// Basic 认证（不另写一套认证）。
func TestOpenFromSendsBasicAuth(t *testing.T) {
	h := &webdavResumeHarness{inner: webdavContractHarness{fs: xnetdav.NewMemFS()}}
	var gotAuth string
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		rangeCapableWebDAV(h.inner.fs).ServeHTTP(w, r)
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)
	remote, err := NewFactory().Create(context.Background(), source.Source{
		Type: source.TypeWebDAV,
		Config: source.Config{
			WebDAV: &source.WebDAVConfig{Endpoint: srv.URL, Username: "alice"},
		},
	}, source.Credentials{WebDAV: &source.WebDAVCredentials{Password: "secret"}})
	if err != nil {
		t.Fatalf("create remote: %v", err)
	}
	rr := remote.(source.ResumableRemote)
	h.Write(t, "/resume.bin", strings.Repeat("y", 50))
	fi, err := remote.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	rc, err := rr.OpenFrom(context.Background(), "/resume.bin", 5, fi.Fingerprint)
	if err != nil {
		t.Fatalf("OpenFrom: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || len(data) != 45 {
		t.Fatalf("suffix = %d bytes (%v), want 45", len(data), err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("Authorization header = %q, want Basic auth", gotAuth)
	}
}

// offset=0 走既有 Open 路径（无 Range 的完整 GET）。
func TestOpenFromZeroOffsetUsesOpen(t *testing.T) {
	h := &webdavResumeHarness{}
	r := h.NewRemote(t)
	defer func() { _ = r.Close() }()
	rr := r.(source.ResumableRemote)
	h.Write(t, "/resume.bin", strings.Repeat("z", 30))
	fi, err := r.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	rc, err := rr.OpenFrom(context.Background(), "/resume.bin", 0, fi.Fingerprint)
	if err != nil {
		t.Fatalf("OpenFrom(0): %v", err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if len(data) != 30 {
		t.Fatalf("full read = %d bytes, want 30", len(data))
	}
}

// 入口校验：负 offset 与非法路径。
func TestOpenFromInvalidInput(t *testing.T) {
	h := &webdavResumeHarness{}
	r := h.NewRemote(t)
	defer func() { _ = r.Close() }()
	rr := r.(source.ResumableRemote)
	if _, err := rr.OpenFrom(context.Background(), "/resume.bin", -1, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
		t.Fatalf("OpenFrom(-1) = %v, want ErrInvalid", err)
	}
	if _, err := rr.OpenFrom(context.Background(), "relative", 0, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
		t.Fatalf("OpenFrom(relative) = %v, want ErrInvalid", err)
	}
}

// If-Range 选择与引号归一：strong ETag 补引号直传；weak ETag 与仅有
// Last-Modified 的快照返回空串（RFC 9110 weak validator——秒精度的
// HTTP-date 无法识别同一秒内的 same-size 替换，调用方降级完整下载，
// 不再退化为 HTTP-date）。
func TestIfRangeValueSelection(t *testing.T) {
	mod := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		fp   source.Fingerprint
		want string
	}{
		{source.Fingerprint{ETag: `"abc"`}, `"abc"`},
		{source.Fingerprint{ETag: `abc`}, `"abc"`},
		{source.Fingerprint{ETag: `W/"abc"`, ModifiedAt: mod}, ""},
		{source.Fingerprint{ETag: "", ModifiedAt: mod}, ""},
		{source.Fingerprint{}, ""},
	}
	for _, tc := range cases {
		if got := webdavIfRangeValue(tc.fp); got != tc.want {
			t.Errorf("webdavIfRangeValue(%+v) = %q, want %q", tc.fp, got, tc.want)
		}
	}
}

// 只有 Last-Modified 的快照（PROPFIND 无 getetag 的服务）在 same-size
// 替换后：即使 Size / mtime 在秒精度内不可区分，缺少 strong ETag 一律
// 拒绝续传——绝不凭 HTTP-date 把旧 prefix 与可能替换过的对象拼接。
func TestOpenFromRefusesLastModifiedOnlySameSecondReplacement(t *testing.T) {
	h := &webdavResumeHarness{}
	r := h.NewRemote(t)
	defer func() { _ = r.Close() }()
	rr := r.(source.ResumableRemote)
	h.Write(t, "/resume.bin", strings.Repeat("A", 100))
	fi, err := r.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// 快照剥掉 ETag，只保留 Last-Modified 身份信号。
	fp := source.Fingerprint{Size: fi.Fingerprint.Size, ModifiedAt: fi.Fingerprint.ModifiedAt}
	h.Write(t, "/resume.bin", strings.Repeat("B", 100))
	_, err = rr.OpenFrom(context.Background(), "/resume.bin", 40, fp)
	if !errors.Is(err, source.ErrResumeUnsupported) {
		t.Fatalf("OpenFrom with Last-Modified-only fingerprint = %v, want ErrResumeUnsupported", err)
	}
}
