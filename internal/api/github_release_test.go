package api

import (
	"net/http"
	"strings"
	"testing"
)

// github_release 类型的 REST 生命周期：创建（token 凭据）→ 读取
// （config 非敏感回显 + credential_state.token_set）→ PATCH 轮换 /
// 清除 token → DELETE。secret 明文全程不出现在响应中。
func TestGitHubReleaseSourceLifecycleAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Gitea Releases",
		"type": "github_release",
		"config": {
			"repository": "gitea/gitea",
			"release_policy": "recent",
			"recent_count": 3,
			"include_prereleases": false,
			"verify_sha256": "if_available"
		},
		"credentials": {"token": "GH_TOKEN_SECRET"},
		"enabled": true
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	if strings.Contains(rec.Body.String(), "GH_TOKEN_SECRET") {
		t.Errorf("response leaks token: %s", rec.Body.String())
	}
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created source has no id: %s", rec.Body.String())
	}
	state, _ := created["credential_state"].(map[string]any)
	ghState, _ := state["github_release"].(map[string]any)
	if ghState == nil || ghState["token_set"] != true {
		t.Errorf("credential_state = %v, want github_release token_set true", created["credential_state"])
	}
	config, _ := created["config"].(map[string]any)
	if config["repository"] != "gitea/gitea" || config["release_policy"] != "recent" {
		t.Errorf("config = %v, want github_release echo", config)
	}

	// PATCH：轮换 token（非空替换）+ 校验策略调整（非身份字段）。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"config": {"repository": "gitea/gitea", "release_policy": "recent", "recent_count": 3, "include_prereleases": false, "verify_sha256": "required"}, "credentials": {"token": "GH_TOKEN_NEW"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	if strings.Contains(rec.Body.String(), "GH_TOKEN_NEW") {
		t.Errorf("response leaks rotated token: %s", rec.Body.String())
	}
	updated := decodeJSON(t, rec)
	config, _ = updated["config"].(map[string]any)
	if config["verify_sha256"] != "required" {
		t.Errorf("patched verify_sha256 = %v, want required", config["verify_sha256"])
	}
	state, _ = updated["credential_state"].(map[string]any)
	ghState, _ = state["github_release"].(map[string]any)
	if ghState == nil || ghState["token_set"] != true {
		t.Error("token_set = false after rotate, want true")
	}

	// PATCH：空串清除 token。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"credentials": {"token": ""}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH clear status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cleared := decodeJSON(t, rec)
	state, _ = cleared["credential_state"].(map[string]any)
	ghState, _ = state["github_release"].(map[string]any)
	if ghState == nil || ghState["token_set"] != false {
		t.Error("token_set = true after clear, want false")
	}

	// DELETE。
	rec = doJSON(t, router, "DELETE", "/api/v1/sources/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", rec.Code)
	}
}

// github_release 的严格解码：未知 config 字段与非法 policy 在 400
// 暴露；token 明文绝不回显。
func TestGitHubReleaseSourceValidationAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Bad Policy",
		"type": "github_release",
		"config": {"repository": "gitea/gitea", "release_policy": "weekly"},
		"enabled": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad policy status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Unknown Field",
		"type": "github_release",
		"config": {"repository": "gitea/gitea", "api_host": "https://example.com"},
		"enabled": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "Bad Repo",
		"type": "github_release",
		"config": {"repository": "not-a-repo"},
		"enabled": true
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad repository status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
