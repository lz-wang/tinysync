package api

import (
	"net/http"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// typedFakeFactory 把 fakeFactory 的类型覆盖为指定协议（connection
// test 经 registry 按 Type dispatch）。
type typedFakeFactory struct {
	fakeFactory
	typ source.Type
}

func (f typedFakeFactory) Type() source.Type { return f.typ }

// newHTTPSourceRouter 构造 http factory 的路由（connection test 用）。
func newHTTPSourceRouter(t *testing.T, remote source.Remote) testRouter {
	t.Helper()
	return newSourceRouterWithFactory(t, typedFakeFactory{fakeFactory{remote: remote}, source.TypeHTTP})
}

// createHTTPSourceViaAPI 创建 basic 认证的 http Source，返回 ID。
func createHTTPSourceViaAPI(t *testing.T, router testRouter, name string) string {
	t.Helper()
	body := `{
		"name": "` + name + `", "type": "http",
		"config": {"base_url": "https://mirror.example.com/releases", "listing_mode": "auto", "auth_method": "basic", "username": "tinysync"},
		"credentials": {"password": "HTTP_SECRET"}
	}`
	rec := doJSON(t, router, "POST", "/api/v1/sources", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create http source status = %d, body = %s", rec.Code, rec.Body.String())
	}
	id, _ := decodeJSON(t, rec)["id"].(string)
	if id == "" {
		t.Fatalf("created source has no id: %s", rec.Body.String())
	}
	return id
}

// http 类型的 REST 生命周期：省略 auth_method 归一为 none、不接受
// password（400）；显式 basic 创建 → 归一化回显（base_url 补尾斜杠、
// listing_mode/auth_method 默认值、caddy_file_limit 默认 10000）→
// PATCH 轮换 / 清除 password（三态）→ connection test → DELETE。
// secret 明文全程不出现在响应中。
func TestHTTPSourceLifecycleAPI(t *testing.T) {
	router := newHTTPSourceRouter(t, fakeRemote{})

	// 省略 auth_method → 归一为 none；none 不接受任何 secret。
	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Mirror Anonymous", "type": "http",
		"config": {"base_url": "https://mirror.example.com/releases"},
		"credentials": {"password": "HTTP_SECRET"},
		"enabled": true
	}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not accept credentials") {
		t.Fatalf("POST anonymous-with-secret status = %d, body = %s, want 400 none rejects credentials", rec.Code, rec.Body.String())
	}

	// 显式 basic 创建。
	rec = doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Mirror HTTP 2", "type": "http",
		"config": {"base_url": "https://mirror.example.com/releases", "auth_method": "basic", "username": "tinysync"},
		"credentials": {"password": "HTTP_SECRET2"}
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST basic status = %d, body = %s", rec.Code, rec.Body.String())
	}
	basic := decodeJSON(t, rec)
	config, _ := basic["config"].(map[string]any)
	if config["base_url"] != "https://mirror.example.com/releases/" {
		t.Errorf("base_url = %v, want trailing slash normalized", config["base_url"])
	}
	if config["listing_mode"] != "auto" || config["auth_method"] != "basic" {
		t.Errorf("config enums = %v/%v, want auto/basic", config["listing_mode"], config["auth_method"])
	}
	if config["caddy_file_limit"] != float64(10000) {
		t.Errorf("caddy_file_limit = %v, want default 10000", config["caddy_file_limit"])
	}
	state, _ := basic["credential_state"].(map[string]any)
	httpState, _ := state["http"].(map[string]any)
	if httpState == nil || httpState["password_set"] != true || httpState["bearer_token_set"] != false {
		t.Errorf("credential_state = %v, want http password_set true / token false", basic["credential_state"])
	}
	id, _ := basic["id"].(string)
	if id == "" {
		t.Fatalf("created source has no id: %s", rec.Body.String())
	}

	// PATCH：caddy_file_limit 调整（非身份字段）+ listing_mode 显式化
	//（身份字段，未被 Job 引用时允许）。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"config": {"base_url": "https://mirror.example.com/releases/", "listing_mode": "caddy", "auth_method": "basic", "username": "tinysync", "caddy_file_limit": 100000}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	updated := decodeJSON(t, rec)
	config, _ = updated["config"].(map[string]any)
	if config["listing_mode"] != "caddy" || config["caddy_file_limit"] != float64(100000) {
		t.Errorf("patched config = %v, want caddy + 100000", config)
	}
	// password 未随 config PATCH 丢失。
	state, _ = updated["credential_state"].(map[string]any)
	httpState, _ = state["http"].(map[string]any)
	if httpState == nil || httpState["password_set"] != true {
		t.Error("password_set = false after config-only PATCH, want true")
	}

	// PATCH：轮换 password 成功；清除 password（basic 唯一 secret）
	// 被拒绝——那会保存一个下次打开 Remote 必然失败的源（存储不变
	// 量：basic 必须持有 password）。彻底移除 secret 走 auth_method=none。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"credentials": {"password": "ROTATED_HTTP"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH rotate status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"credentials": {"password": ""}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "password is required") {
		t.Fatalf("PATCH clear status = %d, body = %s, want 400 password is required", rec.Code, rec.Body.String())
	}
	// 切换 auth_method=none：存量 password 被清除，源进入一致的
	// 无认证形态。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"config": {"base_url": "https://mirror.example.com/releases/", "listing_mode": "caddy", "auth_method": "none"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH to none status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cleared := decodeJSON(t, rec)
	state, _ = cleared["credential_state"].(map[string]any)
	httpState, _ = state["http"].(map[string]any)
	if httpState == nil || httpState["password_set"] != false {
		t.Error("password_set = true after switching to none, want false")
	}

	// connection test：http factory → fakeRemote.Stat("/") 成功 → ok。
	rec = doJSON(t, router, "POST", "/api/v1/sources/"+id+"/test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("test status = %d, body = %s", rec.Code, rec.Body.String())
	}
	testResult := decodeJSON(t, rec)
	if testResult["ok"] != true {
		t.Errorf("test ok = %v, want true (%v)", testResult["ok"], testResult)
	}

	// DELETE。
	rec = doJSON(t, router, "DELETE", "/api/v1/sources/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
}

// http 的严格解码与校验：未知 config 字段、非法枚举、query URL、
// %2F URL、basic 缺 username / password、none 带凭据、config 单选
// 污染、凭据错组在 400 暴露。
func TestHTTPSourceValidationAPI(t *testing.T) {
	router := newHTTPSourceRouter(t, fakeRemote{})

	for name, body := range map[string]string{
		"bad listing mode": `{
			"name": "Bad Mode", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "listing_mode": "apache"}
		}`,
		"bad auth method": `{
			"name": "Bad Auth", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "auth_method": "digest"}
		}`,
		"query in base url": `{
			"name": "Query URL", "type": "http",
			"config": {"base_url": "https://mirror.example.com/files/?token=x"}
		}`,
		"userinfo in base url": `{
			"name": "Userinfo URL", "type": "http",
			"config": {"base_url": "https://user:pass@mirror.example.com/"}
		}`,
		"encoded separator": `{
			"name": "Encoded Slash", "type": "http",
			"config": {"base_url": "https://mirror.example.com/a%2Fb/"}
		}`,
		"dot segment": `{
			"name": "Dot Segment", "type": "http",
			"config": {"base_url": "https://mirror.example.com/a/../b/"}
		}`,
		"basic without username": `{
			"name": "No Username", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "auth_method": "basic"}
		}`,
		"basic without password": `{
			"name": "No Password", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "auth_method": "basic", "username": "u"}
		}`,
		"none with password": `{
			"name": "None With Secret", "type": "http",
			"config": {"base_url": "https://mirror.example.com/"},
			"credentials": {"password": "p"}
		}`,
		"bearer with password": `{
			"name": "Bearer With Password", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "auth_method": "bearer"},
			"credentials": {"password": "p"}
		}`,
		"bearer missing token": `{
			"name": "Bearer No Token", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "auth_method": "bearer"}
		}`,
		"unknown config field": `{
			"name": "Unknown Field", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "remote_root": "/x"}
		}`,
		"config union contamination": `{
			"name": "Contaminated", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "endpoint": "https://x/dav"}
		}`,
		"credentials wrong group": `{
			"name": "Wrong Creds", "type": "http",
			"config": {"base_url": "https://mirror.example.com/", "auth_method": "basic", "username": "u"},
			"credentials": {"secret_key": "k"}
		}`,
	} {
		rec := doJSON(t, router, "POST", "/api/v1/sources", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400, body = %s", name, rec.Code, rec.Body.String())
		}
	}
}

// bearer 认证的生命周期：token 创建 / 轮换 / 清除（bearer_token_set
// 独立三态），username 在归一化中被清空。
func TestHTTPBearerLifecycleAPI(t *testing.T) {
	router := newHTTPSourceRouter(t, fakeRemote{})

	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Bearer Mirror", "type": "http",
		"config": {"base_url": "https://mirror.example.com/", "auth_method": "bearer", "username": "should-be-cleared"},
		"credentials": {"bearer_token": "TOKEN_SECRET"}
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)
	config, _ := created["config"].(map[string]any)
	if config["username"] != nil && config["username"] != "" {
		t.Errorf("username = %v, want cleared for bearer", config["username"])
	}
	state, _ := created["credential_state"].(map[string]any)
	httpState, _ := state["http"].(map[string]any)
	if httpState == nil || httpState["bearer_token_set"] != true {
		t.Errorf("credential_state = %v, want bearer_token_set true", created["credential_state"])
	}

	// 清除 token（bearer 唯一 secret）被拒绝：bearer 必须持有 token，
	// 彻底移除 secret 走 auth_method=none。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id, `{"credentials": {"bearer_token": ""}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bearer token is required") {
		t.Fatalf("PATCH clear token status = %d, body = %s, want 400 bearer token is required", rec.Code, rec.Body.String())
	}
	// 同请求携带两种 secret：只保留当前认证方式（bearer）的 token。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"credentials": {"password": "DORMANT", "bearer_token": "ROTATED_TOKEN"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH dual secrets status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rotated := decodeJSON(t, rec)
	state, _ = rotated["credential_state"].(map[string]any)
	httpState, _ = state["http"].(map[string]any)
	if httpState == nil || httpState["bearer_token_set"] != true || httpState["password_set"] != false {
		t.Errorf("credential_state = %v, want token set / password cleared", rotated["credential_state"])
	}
}

// 被 Job 引用的 http Source：remote identity（base_url / listing_mode /
// auth_method / username）变更 409；caddy_file_limit（安全参数）与
// secret 轮换不受引用保护限制。
func TestHTTPSourceIdentityGuardAPI(t *testing.T) {
	router := newJobRouter(t, fakeJobRemote{})
	sourceID := createHTTPSourceViaAPI(t, router, "Referenced HTTP")
	payload, _ := jobPayload(t, "HTTP Holder", sourceID, "mirror", true)
	rec := doJSON(t, router, "POST", "/api/v1/jobs", payload)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create job status = %d, body = %s", rec.Code, rec.Body.String())
	}

	identityChanges := map[string]string{
		"base_url":     `{"config": {"base_url": "https://other.example.com/releases/", "auth_method": "basic", "username": "tinysync"}}`,
		"listing mode": `{"config": {"base_url": "https://mirror.example.com/releases/", "listing_mode": "nginx", "auth_method": "basic", "username": "tinysync"}}`,
		"username":     `{"config": {"base_url": "https://mirror.example.com/releases/", "auth_method": "basic", "username": "other"}}`,
		"auth method":  `{"config": {"base_url": "https://mirror.example.com/releases/", "auth_method": "none"}}`,
	}
	for name, body := range identityChanges {
		rec := doJSON(t, router, "PATCH", "/api/v1/sources/"+sourceID, body)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s change on referenced source = %d %s, want 409", name, rec.Code, rec.Body.String())
		}
	}

	// 同值 PATCH（幂等）允许。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+sourceID,
		`{"config": {"base_url": "https://mirror.example.com/releases/", "auth_method": "basic", "username": "tinysync"}}`)
	if rec.Code != http.StatusOK {
		t.Errorf("same-identity PATCH = %d %s, want 200", rec.Code, rec.Body.String())
	}
	// caddy_file_limit 是安全参数，允许修改。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+sourceID,
		`{"config": {"base_url": "https://mirror.example.com/releases/", "auth_method": "basic", "username": "tinysync", "caddy_file_limit": 100000}}`)
	if rec.Code != http.StatusOK {
		t.Errorf("caddy_file_limit PATCH = %d %s, want 200", rec.Code, rec.Body.String())
	}
	// secret rotation 始终允许。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+sourceID, `{"credentials": {"password": "ROTATED_HTTP"}}`)
	if rec.Code != http.StatusOK {
		t.Errorf("secret rotation = %d %s, want 200", rec.Code, rec.Body.String())
	}
	// 改名允许。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+sourceID, `{"name": "Renamed HTTP"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("name PATCH = %d %s, want 200", rec.Code, rec.Body.String())
	}
}
