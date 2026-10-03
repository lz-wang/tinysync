package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/source/remotetest"
)

// fakeCaddyServer 是进程内的 Caddy 形态文件服务：内存树 +
// Accept: application/json 的目录索引（与 Caddy file_server browse 的
// JSON 输出同构），供契约套件与 adapter 行为测试使用；测试装置直接
// 操作内存树，不经被测 Remote 造数据。
type fakeCaddyServer struct {
	mu    sync.Mutex
	files map[string]string
	dirs  map[string]bool
	mod   map[string]time.Time
	// listings 统计每个目录的 listing 请求次数（ScanTree「每目录恰
	// 一次」断言用）。
	listings map[string]int
	// failDir 命中时该目录 listing 返回 500（部分失败场景注入）。
	failDir string
	srv     *httptest.Server
}

func newFakeCaddyServer(t testing.TB) *fakeCaddyServer {
	t.Helper()
	f := &fakeCaddyServer{
		files:    map[string]string{},
		dirs:     map[string]bool{"/": true},
		mod:      map[string]time.Time{},
		listings: map[string]int{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// handle 以 Caddy browse 的 JSON 语义服务目录索引与文件。
func (f *fakeCaddyServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimSuffix(r.URL.Path, "/")
	if p == "" {
		p = "/"
	}
	if _, isFile := f.files[p]; isFile {
		f.serveFile(w, r, p)
		return
	}
	if !f.dirs[p] {
		http.NotFound(w, r)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/") {
		// 目录 canonical redirect（同源，验证 redirect 收敛可用）。
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
		return
	}
	f.listings[p]++
	if f.failDir == p {
		http.Error(w, "injected failure", http.StatusInternalServerError)
		return
	}
	prefix := "/"
	if p != "/" {
		prefix = p + "/"
	}
	entries := []caddyEntry{}
	for _, name := range sortedKeys(f.files, f.dirs, prefix) {
		child := prefix + name
		if f.dirs[child] {
			entries = append(entries, caddyEntry{
				Name: name, URL: name + "/", ModTime: f.modTime(child),
				Mode: 2147484141, IsDir: true,
			})
			continue
		}
		content := f.files[child]
		entries = append(entries, caddyEntry{
			Name: name, Size: int64(len(content)), URL: name,
			ModTime: f.modTime(child), Mode: 420,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

// serveFile 服务文件内容（GET）或元数据（HEAD）。
func (f *fakeCaddyServer) serveFile(w http.ResponseWriter, r *http.Request, p string) {
	content := f.files[p]
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Last-Modified", f.modTime(p).UTC().Format(http.TimeFormat))
	w.Header().Set("ETag", fmt.Sprintf(`"fake-%d"`, len(content)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write([]byte(content))
}

func (f *fakeCaddyServer) modTime(p string) time.Time {
	if t, ok := f.mod[p]; ok {
		return t
	}
	return time.Unix(1758900000, 0).UTC()
}

// write 写入文件并登记父目录。
func (f *fakeCaddyServer) write(logical, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureDirLocked(path_Dir(logical))
	f.files[logical] = content
	f.mod[logical] = time.Now().UTC().Truncate(time.Second)
}

// mkdir 创建目录（含父目录）。
func (f *fakeCaddyServer) mkdir(logical string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureDirLocked(logical)
}

func (f *fakeCaddyServer) ensureDirLocked(logical string) {
	if logical == "/" || logical == "" {
		return
	}
	f.dirs[logical] = true
	f.mod[logical] = time.Unix(1758900000, 0).UTC()
	f.ensureDirLocked(parentDir(logical))
}

// openRemote 经生产 Factory 构造指向本服务的 Client。
func (f *fakeCaddyServer) openRemote(listing source.HTTPListingMode) (source.Remote, error) {
	return NewFactory().Create(context.Background(), source.Source{
		Name:   "fake",
		Type:   source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{BaseURL: f.srv.URL + "/", ListingMode: listing}},
	}, source.Credentials{})
}

// sortedKeys 列出 prefix 直接子项名（去重排序）。
func sortedKeys(files map[string]string, dirs map[string]bool, prefix string) []string {
	seen := map[string]bool{}
	for p := range files {
		if strings.HasPrefix(p, prefix) && p != prefix {
			rest := strings.TrimPrefix(p, prefix)
			if rest != "" && !strings.Contains(rest, "/") {
				seen[rest] = true
			}
		}
	}
	for p := range dirs {
		if strings.HasPrefix(p, prefix) && p != prefix && p != "/" {
			rest := strings.TrimPrefix(p, prefix)
			if rest != "" && !strings.Contains(rest, "/") {
				seen[rest] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func path_Dir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "/"
}

func parentDir(p string) string { return path_Dir(p) }

// fakeHarness 适配 remotetest.Harness。
type fakeHarness struct{ f *fakeCaddyServer }

func (h fakeHarness) NewRemote(t *testing.T) source.Remote {
	t.Helper()
	r, err := h.f.openRemote(source.HTTPListingAuto)
	if err != nil {
		t.Fatalf("openRemote: %v", err)
	}
	return r
}

func (h fakeHarness) Write(t *testing.T, logical, content string) {
	t.Helper()
	h.f.write(logical, content)
}

func (h fakeHarness) Mkdir(t *testing.T, logical string) {
	t.Helper()
	h.f.mkdir(logical)
}

// 契约套件：Stat / List 分页 / Open（空文件、大文件、特殊文件名）/
// 缺失 / 非法 logical path / ctx 取消，与 WebDAV / S3 / SFTP / SMB
// 同一套断言。
func TestRemoteContract(t *testing.T) {
	remotetest.RunSuite(t, fakeHarness{newFakeCaddyServer(t)})
}
