package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
	httpadapter "tinysync/internal/source/http"
)

// HTTP integration：真实 nginx / Caddy / miniserve 容器（CI 由
// integration job 注入；本地可对任意 HTTP 文件服务运行）。设置
// TINYSYNC_IT_HTTP_BASE_URL 后下列测试才运行，未设置时跳过，不影响
// make check。真实 TCP + 真实目录索引输出 + 真实文件服务——不 mock
// HTTP 响应；重点验证 TinySync 对三种真实 listing 输出的兼容性。
const (
	itHTTPBaseURL   = "TINYSYNC_IT_HTTP_BASE_URL"
	itHTTPRoot      = "TINYSYNC_IT_HTTP_ROOT"
	itHTTPListing   = "TINYSYNC_IT_HTTP_LISTING"
	itHTTPUsername  = "TINYSYNC_IT_HTTP_USERNAME"
	itHTTPPassword  = "TINYSYNC_IT_HTTP_PASSWORD"
	itHTTPFileLimit = "TINYSYNC_IT_HTTP_FILE_LIMIT"
)

// httpITConfig 是一次集成运行的环境参数。
type httpITConfig struct {
	baseURL  string
	root     string
	listing  source.HTTPListingMode
	username string
	password string
	// fileLimit > 0 时服务器配置了 browse file_limit（fail-closed
	// 场景注入）。
	fileLimit int
}

// httpITLoad 读取环境变量；BASE_URL 未设置时跳过。
func httpITLoad(t *testing.T) httpITConfig {
	t.Helper()
	baseURL := os.Getenv(itHTTPBaseURL)
	if baseURL == "" {
		t.Skipf("set %s to run the real-HTTP-server integration scenario", itHTTPBaseURL)
	}
	root := os.Getenv(itHTTPRoot)
	if root == "" {
		t.Fatalf("%s is required (host path of the served directory)", itHTTPRoot)
	}
	listing := source.HTTPListingMode(os.Getenv(itHTTPListing))
	switch listing {
	case "":
		listing = source.HTTPListingAuto
	case source.HTTPListingAuto, source.HTTPListingNginx, source.HTTPListingCaddy, source.HTTPListingMiniserve:
	default:
		t.Fatalf("%s = %q is not a supported listing mode", itHTTPListing, listing)
	}
	limit := 0
	if raw := os.Getenv(itHTTPFileLimit); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			t.Fatalf("%s = %q is not a positive integer", itHTTPFileLimit, raw)
		}
		limit = n
	}
	return httpITConfig{
		baseURL:   strings.TrimRight(baseURL, "/") + "/",
		root:      root,
		listing:   listing,
		username:  os.Getenv(itHTTPUsername),
		password:  os.Getenv(itHTTPPassword),
		fileLimit: limit,
	}
}

// sourceConfig 构造 canonical HTTPConfig。
func (c httpITConfig) sourceConfig() source.HTTPConfig {
	cfg := source.HTTPConfig{
		BaseURL:        c.baseURL,
		ListingMode:    c.listing,
		AuthMethod:     source.HTTPAuthNone,
		CaddyFileLimit: source.DefaultCaddyFileLimit,
	}
	if c.fileLimit > 0 {
		cfg.CaddyFileLimit = c.fileLimit
	}
	if c.username != "" {
		cfg.AuthMethod = source.HTTPAuthBasic
		cfg.Username = c.username
	}
	return cfg
}

// credentials 构造认证凭据。
func (c httpITConfig) credentials() source.Credentials {
	if c.username == "" {
		return source.Credentials{}
	}
	return source.Credentials{HTTP: &source.HTTPCredentials{Password: c.password}}
}

// isolationRoot 建立本轮运行专属的隔离子树（服务器根下的子目录），
// 避免与容器内其他内容互相干扰。
func (c httpITConfig) isolationRoot(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(c.root, fmt.Sprintf("it-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir isolation root: %v", err)
	}
	return dir
}

// openRemote 经生产 Factory 建立指向 baseURL（或隔离子树）的 Client。
func (c httpITConfig) openRemote(root string) (source.Remote, error) {
	cfg := c.sourceConfig()
	if root != "" {
		rel := strings.TrimLeft(strings.TrimPrefix(filepath.ToSlash(root), c.root), "/")
		if rel != "" {
			cfg.BaseURL = strings.TrimRight(c.baseURL, "/") + "/" + rel + "/"
		}
	}
	return httpadapter.NewFactory().Create(context.Background(), source.Source{
		Name:   "it-http",
		Type:   source.TypeHTTP,
		Config: source.Config{HTTP: &cfg},
	}, c.credentials())
}

// put 写入隔离树内文件（父目录自动创建）。
func (c httpITConfig) put(root, logical, content string) error {
	p := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(logical, "/")))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// TestIntegrationHTTPRemote 对真实服务器执行 Remote 契约：根目录
// Stat / auto 探测 / 单层 List / 全树 ScanTree 与种子一致 / 文件
// Open roundtrip；basic 认证（提供用户名时）含错误口令的负向断言。
func TestIntegrationHTTPRemote(t *testing.T) {
	cfg := httpITLoad(t)
	root := cfg.isolationRoot(t)
	seed := map[string]string{
		"/hello.txt":     "hello integration",
		"/empty.txt":     "",
		"/dir/inner.txt": "inner",
	}
	for logical, content := range seed {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, strings.TrimPrefix(logical, "/"))), 0o755); err != nil {
			t.Fatalf("mkdir seed parent: %v", err)
		}
		if err := cfg.put(root, logical, content); err != nil {
			t.Fatalf("seed %s: %v", logical, err)
		}
	}

	remote, err := cfg.openRemote(root)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = remote.Close() }()

	// Stat 根目录（auto 模式在此完成探测——端点可达且是受支持的
	// 目录索引）。
	fi, err := remote.Stat(context.Background(), "/")
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if !fi.IsDir {
		t.Fatal("root IsDir = false")
	}

	// Stat 文件：精确 size。
	fi, err = remote.Stat(context.Background(), "/hello.txt")
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if fi.Fingerprint.Size != int64(len("hello integration")) {
		t.Errorf("size = %d, want exact %d (listing=%s)", fi.Fingerprint.Size, len("hello integration"), cfg.listing)
	}

	// List 根目录包含全部种子条目。
	page, err := remote.List(context.Background(), "/", source.ListOptions{Limit: source.MaxListLimit})
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range page.Entries {
		seen[e.Path] = true
	}
	for _, want := range []string{"/hello.txt", "/empty.txt", "/dir"} {
		if !seen[want] {
			t.Errorf("entry %q missing from listing (listing=%s): %v", want, cfg.listing, seen)
		}
	}

	// ScanTree 全量枚举（文件 + 目录），Open roundtrip。
	scanErr := remote.(source.TreeScanner).ScanTree(context.Background(), "/", func(fi source.FileInfo) error {
		if !fi.IsDir && fi.Fingerprint.Size == 0 && fi.Path != "/empty.txt" {
			return fmt.Errorf("file %q has zero size", fi.Path)
		}
		if fi.Path == "/dir/inner.txt" {
			rc, err := remote.Open(context.Background(), fi.Path)
			if err != nil {
				return err
			}
			defer func() { _ = rc.Close() }()
			buf := make([]byte, 64)
			n, _ := rc.Read(buf)
			if string(buf[:n]) != "inner" {
				return fmt.Errorf("inner.txt content = %q", buf[:n])
			}
		}
		return nil
	})
	if scanErr != nil {
		t.Fatalf("ScanTree (listing=%s): %v", cfg.listing, scanErr)
	}

	// basic 认证负向：错误口令 → permanent 失败（凭据不泄露、不重试）。
	if cfg.username != "" {
		bad := cfg
		bad.password = "wrong-password"
		badRemote, err := bad.openRemote(root)
		if err == nil {
			_, statErr := badRemote.Stat(context.Background(), "/")
			_ = badRemote.Close()
			if statErr == nil {
				t.Fatal("wrong password accepted, want 401 failure")
			}
			if source.IsRetryable(statErr) {
				t.Errorf("wrong password should be permanent, got %v", statErr)
			}
		} else if source.IsRetryable(err) {
			t.Errorf("wrong password Create should be permanent, got %v", err)
		}
	}
}

// TestIntegrationHTTPFileLimitFailClosed：服务器配置 browse
// file_limit 且单目录条目达到该值时，ScanTree 整体失败——绝不把
// 截断 listing 当完整快照授权 Mirror 删除。仅在提供
// TINYSYNC_IT_HTTP_FILE_LIMIT 时运行（需要服务器侧小 file_limit）。
func TestIntegrationHTTPFileLimitFailClosed(t *testing.T) {
	cfg := httpITLoad(t)
	if cfg.fileLimit == 0 {
		t.Skipf("set %s to run the file_limit fail-closed scenario", itHTTPFileLimit)
	}
	root := cfg.isolationRoot(t)
	// 达到上限：恰好 fileLimit 个条目（达到即触发，无法证明没有更多）。
	for i := 0; i < cfg.fileLimit; i++ {
		if err := cfg.put(root, fmt.Sprintf("/f-%04d.txt", i), "x"); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	remote, err := cfg.openRemote(root)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = remote.Close() }()
	visited := 0
	err = remote.(source.TreeScanner).ScanTree(context.Background(), "/", func(source.FileInfo) error {
		visited++
		return nil
	})
	if err == nil {
		t.Fatal("ScanTree with reached file_limit = nil, want failure")
	}
	if !strings.Contains(err.Error(), "file_limit") {
		t.Errorf("error = %v, want file_limit detail", err)
	}
	if source.IsRetryable(err) {
		t.Error("file_limit failure should be permanent")
	}
	if visited != 0 {
		t.Errorf("visited %d entries, want 0 (fail before yielding entries)", visited)
	}
}

// newHTTPRealFixture 构造接真实 HTTP 文件服务的矩阵 fixture；与生产
// 语义一致，每次 openRemote 经 Factory 建立独立 Client。未设置
// TINYSYNC_IT_HTTP_BASE_URL 时矩阵条目跳过，其余协议照常运行。
//
// put / remove 后经服务器自身视角轮询确认变更可见：macOS Docker
// 的 virtiofs 属性缓存可能让容器内 stat 短暂看到旧 size / mtime，
// 不等待会让 update 场景在本地开发机上假失败（CI Linux 无此现象，
// 轮询首轮即通过）。
func newHTTPRealFixture(t *testing.T) matrixRemote {
	cfg := httpITLoad(t)
	root := cfg.isolationRoot(t)
	settle := func(t *testing.T, logical, content string, removed bool) {
		t.Helper()
		dir := path_Dir(logical)
		name := strings.TrimSuffix(path_Base(logical), "/")
		deadline := time.Now().Add(10 * time.Second)
		for {
			entries, err := cfg.rawList(root, dir)
			if errors.Is(err, errNotJSONListing) {
				// HTML listing（miniserve）无法用裸 JSON 观察——退化为
				// 对文件本身 HEAD 轮询（put：Content-Length 达到期望；
				// remove：404）。
				if cfg.headSettled(root, logical, len(content), removed) {
					return
				}
				if time.Now().After(deadline) {
					return
				}
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if err == nil {
				present := containsEntry(entries, name)
				if removed && !present {
					return
				}
				if !removed && present {
					for _, e := range entries {
						if (e.Name == name || e.Name == name+"/") && !e.IsDir && e.Size == int64(len(content)) {
							return
						}
					}
				}
			}
			if time.Now().After(deadline) {
				return // 超时放行：由后续同步断言给出真实失败。
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	return matrixRemote{
		name: "http_real",
		put: func(t *testing.T, logical, content string) {
			if err := cfg.put(root, logical, content); err != nil {
				t.Fatalf("http put %s: %v", logical, err)
			}
			settle(t, logical, content, false)
		},
		remove: func(t *testing.T, logical string) {
			p := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(logical, "/")))
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("http remove %s: %v", logical, err)
			}
			settle(t, logical, "", true)
		},
		openRemote: func() (source.Remote, error) {
			return cfg.openRemote(root)
		},
	}
}

// containsEntry 报告条目（可带尾斜杠）是否在列表中。
func containsEntry(entries []rawHTTPEntry, name string) bool {
	for _, e := range entries {
		if e.Name == name || e.Name == name+"/" {
			return true
		}
	}
	return false
}

// rawHTTPEntry 是裸 listing 轮询的最小条目形态。
type rawHTTPEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
}

// errNotJSONListing 标识裸轮询拿到非 JSON listing（miniserve HTML）。
var errNotJSONListing = errors.New("listing is not JSON")

// rawList 直接 GET listing（不经被测 adapter）以服务器视角枚举 dir。
func (c httpITConfig) rawList(root, logicalDir string) ([]rawHTTPEntry, error) {
	rel := strings.TrimLeft(strings.TrimPrefix(filepath.ToSlash(root), c.root), "/")
	if rel != "" {
		rel += "/"
	}
	if logicalDir != "/" {
		rel += strings.TrimPrefix(logicalDir, "/") + "/"
	}
	rel = strings.ReplaceAll(rel, " ", "%20")
	urlStr := strings.TrimRight(c.baseURL, "/") + "/" + rel
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// nginx JSON 与 Caddy JSON 同为顶层数组；miniserve（HTML）场景
	// 无法用裸 JSON 观察返回专用错误，调用方立即放行。
	var entries []rawHTTPEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, errNotJSONListing
	}
	return entries, nil
}

// path_Dir / path_Base 是 path.Dir / path.Base 的本地别名（避免与
// 文件路径 helper 混淆）。
func path_Dir(p string) string {
	if i := strings.LastIndexByte(strings.TrimSuffix(p, "/"), '/'); i > 0 {
		return p[:i]
	}
	return "/"
}

// headSettled 以 HEAD 文件的方式观察变更是否已被服务器看到（HTML
// listing 服务器的 settle 兜底）。
func (c httpITConfig) headSettled(root, logical string, wantSize int, removed bool) bool {
	rel := strings.TrimLeft(strings.TrimPrefix(filepath.ToSlash(root), c.root), "/")
	if rel != "" {
		rel += "/"
	}
	rel += strings.TrimPrefix(logical, "/")
	urlStr := strings.TrimRight(c.baseURL, "/") + "/" + rel
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, urlStr, nil)
	if err != nil {
		return false
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if removed {
		return resp.StatusCode == http.StatusNotFound
	}
	return resp.StatusCode == http.StatusOK && resp.ContentLength == int64(wantSize)
}

func path_Base(p string) string {
	return p[strings.LastIndexByte(p, '/')+1:]
}
