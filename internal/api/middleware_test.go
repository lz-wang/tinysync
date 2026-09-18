package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"

	"tinysync/internal/logging"
)

// 服务器生成的 request ID：形如 req_<32 hex>、随请求变化、不采纳
// 客户端提供的值，并回写 X-Request-ID 响应头。
func TestRequestIDGenerated(t *testing.T) {
	router := NewRouter(fstest.MapFS{}, Dependencies{})
	pattern := regexp.MustCompile(`^req_[0-9a-f]{32}$`)

	ids := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("X-Request-ID", "client-supplied-id")
		router.ServeHTTP(w, req)

		id := w.Header().Get("X-Request-ID")
		if !pattern.MatchString(id) {
			t.Fatalf("X-Request-ID = %q, want req_<32 hex>", id)
		}
		if id == "client-supplied-id" {
			t.Fatal("client-supplied request id was trusted")
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] {
		t.Errorf("request ids identical across requests: %s", ids[0])
	}
}

// access log 字段与脱敏契约：event/request_id/method/path/status/
// duration_ms/bytes 全部落盘；query string、Authorization、Cookie
// 与客户端伪造的 request id 不进入日志。
func TestAccessLogFieldsAndRedaction(t *testing.T) {
	dataDir := t.TempDir()
	logging.Init(dataDir)
	t.Cleanup(func() { logging.Init("") })

	router := NewRouter(fstest.MapFS{}, Dependencies{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health?token=super-secret-query", nil)
	req.Header.Set("Authorization", "Bearer super-secret-bearer")
	req.Header.Set("Cookie", "session=super-secret-cookie")
	router.ServeHTTP(w, req)

	_ = logging.Sync()
	data, err := os.ReadFile(filepath.Join(dataDir, "logs", "tinysync.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	log := string(data)

	requestID := w.Header().Get("X-Request-ID")
	for _, want := range []string{
		"event=request",
		"request_id=" + requestID,
		"method=GET",
		"path=/api/v1/health",
		"status=200",
		"duration_ms=",
		"bytes=",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("access log missing %q:\n%s", want, log)
		}
	}
	for _, forbidden := range []string{
		"super-secret-query",
		"super-secret-bearer",
		"super-secret-cookie",
		"client-supplied-id",
		"token=",
	} {
		if strings.Contains(log, forbidden) {
			t.Errorf("access log leaks %q:\n%s", forbidden, log)
		}
	}
}

// panic 请求经 Recovery 收敛为 500，access log 仍留下记录（defer
// 保证）。注册一条必然 panic 的受保护路由之外路径不可行，这里直接
// 构造 engine 复用中间件链验证。
func TestAccessLogRecordsPanicAs500(t *testing.T) {
	dataDir := t.TempDir()
	logging.Init(dataDir)
	t.Cleanup(func() { logging.Init("") })

	router := NewRouter(fstest.MapFS{}, Dependencies{})
	router.GET("/api/v1/__boom", func(c *gin.Context) {
		panic("boom-for-test")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/__boom", nil)
	// /api/v1/* 未匹配路径会先撞上 404？显式注册的路由优先；认证
	// 中间件只挂在 protected group，不会拦截这里。
	router.ServeHTTP(w, req)

	_ = logging.Sync()
	data, err := os.ReadFile(filepath.Join(dataDir, "logs", "tinysync.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, "path=/api/v1/__boom") || !strings.Contains(log, "status=500") {
		t.Errorf("access log missing panic record:\n%s", log)
	}
}
