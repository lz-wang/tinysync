package api

import (
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"tinysync/internal/buildinfo"
)

// testWebFS 模拟 Vite 构建产物：SPA 入口 + 带内容 hash 的 assets。
func testWebFS() fs.FS {
	return fstest.MapFS{
		"index.html":            &fstest.MapFile{Data: []byte("<!doctype html><html><head><title>TinySync</title></head><body><div id=\"root\"></div></body></html>")},
		"assets/app-abc123.js":  &fstest.MapFile{Data: []byte("console.log('tinysync')")},
		"assets/app-abc123.css": &fstest.MapFile{Data: []byte("body{}")},
	}
}

// GET /api/v1/health 返回 {"status":"ok"}。
func TestHealthEndpoint(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	NewRouter(testWebFS(), Dependencies{}).ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q, want application/json; charset=utf-8", contentType)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status = %q, want ok", body["status"])
	}
}

// GET /api/v1/version 返回 buildinfo.Version，打通版本链路。
func TestVersionEndpoint(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/version", nil)
	NewRouter(testWebFS(), Dependencies{}).ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Version != buildinfo.Version {
		t.Fatalf("version = %q, want %q (buildinfo.Version)", body.Version, buildinfo.Version)
	}
}

// 未知 API 路径必须 404，不能被 SPA fallback 吞掉。
func TestUnknownAPIPathReturns404(t *testing.T) {
	for _, path := range []string{
		"/api/v1/not-found",
		"/api/v1/sources",
		"/api/v1/health/extra",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		NewRouter(testWebFS(), Dependencies{}).ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
}

// 非 GET 方法返回 405，并按 RFC 7231 携带 Allow 头（Gin
// HandleMethodNotAllowed 行为）。
func TestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/health", nil)
	NewRouter(testWebFS(), Dependencies{}).ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Fatalf("POST /api/v1/health status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET" {
		t.Fatalf("allow header = %q, want GET", allow)
	}
}

// SPA fallback：根路径与前端路由深链接都回退 index.html（不缓存）。
func TestSPAFallbackServesIndex(t *testing.T) {
	for _, path := range []string{"/", "/sources", "/jobs/42/edit"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		NewRouter(testWebFS(), Dependencies{}).ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("GET %s status = %d, want 200", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<title>TinySync</title>") {
			t.Errorf("GET %s body has no <title>TinySync</title>", path)
		}
		if cache := rec.Header().Get("Cache-Control"); cache != "no-store" {
			t.Errorf("GET %s cache-control = %q, want no-store", path, cache)
		}
	}
}

// 嵌入的静态资源按内容服务，assets 带 immutable 缓存。
func TestStaticAssetServed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/assets/app-abc123.js", nil)
	NewRouter(testWebFS(), Dependencies{}).ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "console.log('tinysync')" {
		t.Fatalf("body = %q, want asset content", got)
	}
	if cache := rec.Header().Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
		t.Fatalf("cache-control = %q, want immutable", cache)
	}
}

// 缺失的静态资源（带扩展名或 /assets/ 前缀）404，不误回 index.html。
func TestMissingAssetReturns404(t *testing.T) {
	for _, path := range []string{"/assets/missing.js", "/missing.txt"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		NewRouter(testWebFS(), Dependencies{}).ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
}
