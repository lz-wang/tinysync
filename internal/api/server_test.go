package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"tinysync/internal/buildinfo"
)

// GET /api/v1/health 返回 {"status":"ok"}。
func TestHealthEndpoint(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	NewRouter().ServeHTTP(rec, req)

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
	NewRouter().ServeHTTP(rec, req)

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
		NewRouter().ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
}

// 非 GET 方法返回 405。
func TestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/health", nil)
	NewRouter().ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Fatalf("POST /api/v1/health status = %d, want 405", rec.Code)
	}
}
