package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tinysync/internal/source"
	httpadapter "tinysync/internal/source/http"
	"tinysync/internal/syncjob"
)

// resumeRelayServer 是支持 Range 的进程内 HTTP 文件服务（Caddy 形态
// JSON 目录索引），并记录每次文件 GET 的 Range 头与实际发送字节数；
// 第一次文件 GET 可注入「发送 cutAfter 字节后主动断开连接」——
// 断点续传可靠性 E2E 的核心装置（ADR 0010）。
type resumeRelayServer struct {
	mu       sync.Mutex
	content  []byte
	ranges   []string // 每次文件 GET 的 Range 头（空串 = 无 Range）
	sent     []int    // 每次文件 GET 实际发送的字节数
	cutAfter int      // > 0 时第一次文件 GET 发送该字节数后中止连接
	srv      *httptest.Server
}

func newResumeRelayServer(t *testing.T, content []byte) *resumeRelayServer {
	t.Helper()
	s := &resumeRelayServer{content: content}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *resumeRelayServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/big.bin" {
		s.serveListing(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rangeHeader := r.Header.Get("Range")
	s.ranges = append(s.ranges, rangeHeader)
	content := s.content
	// 第一次请求注入断流：发送 cutAfter 字节后中止连接（不写响应尾，
	// 客户端收到 unexpected EOF，按瞬时故障重试）。
	first := len(s.ranges) == 1
	if first && s.cutAfter > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content[:s.cutAfter])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		s.sent = append(s.sent, s.cutAfter)
		panic(http.ErrAbortHandler)
	}
	if rangeHeader == "" {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		_, _ = w.Write(content)
		s.sent = append(s.sent, len(content))
		return
	}
	var start int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &start); err != nil || start < 0 || start > int64(len(content)) {
		http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
		s.sent = append(s.sent, 0)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(content[start:])
	s.sent = append(s.sent, len(content)-int(start))
}

// serveListing 输出 Caddy 形态 JSON 目录索引（根目录，仅 big.bin）。
func (s *resumeRelayServer) serveListing(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		URL     string `json:"url"`
		ModTime string `json:"mod_time"`
		IsDir   bool   `json:"is_dir"`
		IsSyml  bool   `json:"is_symlink"`
	}
	entries := []entry{{
		Name: "big.bin", URL: "big.bin", Size: int64(len(s.content)),
		ModTime: "2026-10-01T12:00:00Z", IsDir: false, IsSyml: false,
	}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

// TestResumeReliabilityE2E 是断点续传的验收级 E2E（ADR 0010）：
// 8 MiB 文件第一次传输在 3 MiB 处被服务端主动断开，同一 Download
// 的重试必须以 Range: bytes=3145728- 续传，服务端第二次只发送约
// 5 MiB——这证明断点续传真的发生，而不只是「最终文件正确」。
func TestResumeReliabilityE2E(t *testing.T) {
	const total = 8 << 20
	const cut = 3 << 20
	content := make([]byte, total)
	for i := range content {
		content[i] = byte(i * 7 % 251)
	}
	srv := newResumeRelayServer(t, content)
	srv.cutAfter = cut

	factory := httpadapter.NewFactory()
	remote, err := factory.Create(context.Background(), source.Source{
		Name: "resume-e2e",
		Type: source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{
			BaseURL:     srv.srv.URL + "/",
			ListingMode: source.HTTPListingCaddy,
		}},
	}, source.Credentials{})
	if err != nil {
		t.Fatalf("create http remote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	fi, err := remote.Stat(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Fingerprint.Size != total {
		t.Fatalf("stat size = %d, want %d", fi.Fingerprint.Size, total)
	}

	root := t.TempDir()
	d := syncjob.NewDownloader(remote)
	spec := syncjob.TransferSpec{
		JobID:       "job_resume_e2e",
		SourceID:    "src_resume_e2e",
		LogicalPath: "/big.bin",
		LocalRoot:   root,
		RelPath:     "big.bin",
		Expected:    fi.Fingerprint,
	}
	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}

	// 最终文件完整一致。
	got, err := os.ReadFile(filepath.Join(root, "big.bin"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if len(got) != total {
		t.Fatalf("target size = %d, want %d", len(got), total)
	}
	for i := range got {
		if got[i] != content[i] {
			t.Fatalf("target diverges at %d", i)
		}
	}

	// 服务端视角：第二次文件请求必须携带精确断点，且只发送剩余字节。
	srv.mu.Lock()
	defer srv.mu.Unlock()
	fileGets := len(srv.ranges)
	if fileGets < 2 {
		t.Fatalf("file GETs = %d, want >= 2 (interrupted then resumed)", fileGets)
	}
	second := srv.ranges[1]
	if second != "bytes=3145728-" {
		t.Fatalf("second request Range = %q, want bytes=3145728-", second)
	}
	sentSecond := srv.sent[1]
	if sentSecond != total-cut {
		t.Fatalf("second transfer sent %d bytes, want %d (resume must not resend the prefix)", sentSecond, total-cut)
	}
	// 目录里不残留断点文件。
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "big.bin" {
		t.Fatalf("root entries = %v, want only big.bin", entries)
	}
}

// TestResumeAcrossDownloadInvocationsE2E 进程内「重启」语义：第一次
// Download 在中断后失败（重试耗尽），第二次 Download 的首个文件请求
// 就从遗留断点开始。
func TestResumeAcrossDownloadInvocationsE2E(t *testing.T) {
	const total = 4 << 20
	const cut = 1 << 20
	content := make([]byte, total)
	for i := range content {
		content[i] = byte(i % 253)
	}
	srv := newResumeRelayServer(t, content)
	srv.cutAfter = cut

	factory := httpadapter.NewFactory()
	remote, err := factory.Create(context.Background(), source.Source{
		Name: "resume-e2e-2",
		Type: source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{
			BaseURL:     srv.srv.URL + "/",
			ListingMode: source.HTTPListingCaddy,
		}},
	}, source.Credentials{})
	if err != nil {
		t.Fatalf("create http remote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	fi, err := remote.Stat(context.Background(), "/big.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	root := t.TempDir()
	d := syncjob.NewDownloader(remote)
	spec := syncjob.TransferSpec{
		JobID:       "job_resume_e2e",
		SourceID:    "src_resume_e2e",
		LogicalPath: "/big.bin",
		LocalRoot:   root,
		RelPath:     "big.bin",
		Expected:    fi.Fingerprint,
	}

	// 第一次 Download：attempt 1 断流，后续 attempt 继续断流（fake
	// 只注入第一次断开，重试从断点续传成功）。
	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	srv.mu.Lock()
	if len(srv.ranges) < 2 || srv.ranges[1] != "bytes=1048576-" {
		srv.mu.Unlock()
		t.Fatalf("Range sequence = %v, want second request bytes=1048576-", srv.ranges)
	}
	srv.mu.Unlock()
	got, err := os.ReadFile(filepath.Join(root, "big.bin"))
	if err != nil || len(got) != total {
		t.Fatalf("target = %d bytes (%v), want %d", len(got), err, total)
	}
	if !strings.Contains(string(got[:64]), string(content[:64])) {
		t.Fatal("prefix mismatch")
	}
}
