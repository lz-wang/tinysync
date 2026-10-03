package api

import (
	"net/http"
	"strings"
	"testing"
)

// smb 类型的 REST 生命周期：创建（password 凭据）→ 读取（config 非敏感
// 回显 + credential_state.smb.password_set，默认值归一后回显）→
// PATCH 轮换 / 清除 password（三态）→ DELETE。secret 明文全程不出
// 现在响应中。
func TestSMBSourceLifecycleAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	// port / remote_root / signing 省略：归一为 445 / "/" / required 后回显。
	rec := doJSON(t, router, "POST", "/api/v1/sources", `{
		"name": "NAS SMB",
		"type": "smb",
		"config": {"host": "nas.example.com", "share": "backup", "username": "tinysync"},
		"credentials": {"password": "SMB_SECRET"},
		"enabled": true
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	if strings.Contains(rec.Body.String(), "SMB_SECRET") {
		t.Errorf("response leaks password: %s", rec.Body.String())
	}
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created source has no id: %s", rec.Body.String())
	}
	state, _ := created["credential_state"].(map[string]any)
	smbState, _ := state["smb"].(map[string]any)
	if smbState == nil || smbState["password_set"] != true {
		t.Errorf("credential_state = %v, want smb password_set true", created["credential_state"])
	}
	config, _ := created["config"].(map[string]any)
	if config["port"] != float64(445) || config["remote_root"] != "/" || config["signing"] != "required" {
		t.Errorf("config = %v, want normalized defaults (445 / / / required)", config)
	}

	// PATCH：轮换密码（非空替换）+ signing 放宽（非身份字段，允许）。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"config": {"host": "nas.example.com", "port": 445, "share": "backup", "remote_root": "/photos", "username": "tinysync", "domain": "WORKGROUP", "signing": "auto"}, "credentials": {"password": "SMB_NEW"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertNoSecret(t, rec)
	if strings.Contains(rec.Body.String(), "SMB_NEW") {
		t.Errorf("response leaks rotated password: %s", rec.Body.String())
	}
	updated := decodeJSON(t, rec)
	config, _ = updated["config"].(map[string]any)
	if config["signing"] != "auto" || config["remote_root"] != "/photos" || config["domain"] != "WORKGROUP" {
		t.Errorf("patched config = %v, want signing auto + /photos + WORKGROUP", config)
	}
	state, _ = updated["credential_state"].(map[string]any)
	smbState, _ = state["smb"].(map[string]any)
	if smbState == nil || smbState["password_set"] != true {
		t.Error("password_set = false after rotate, want true")
	}

	// PATCH：空串清除密码 → password_set false（清除合法：结果状态由
	// 业务决定，连接测试会拒绝无密码的 Source）。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+id,
		`{"credentials": {"password": ""}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH clear status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cleared := decodeJSON(t, rec)
	state, _ = cleared["credential_state"].(map[string]any)
	smbState, _ = state["smb"].(map[string]any)
	if smbState == nil || smbState["password_set"] != false {
		t.Error("password_set = true after clear, want false")
	}

	// DELETE。
	rec = doJSON(t, router, "DELETE", "/api/v1/sources/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
}

// smb 的严格解码与校验：未知 config 字段、非法 signing、host 带
// scheme、缺 password、config 单选污染在 400 暴露。
func TestSMBSourceValidationAPI(t *testing.T) {
	router := newSourceRouter(t, fakeRemote{})

	for name, body := range map[string]string{
		"bad signing": `{
			"name": "Bad Signing", "type": "smb",
			"config": {"host": "nas", "share": "backup", "username": "u", "signing": "disabled"},
			"credentials": {"password": "p"}
		}`,
		"host with scheme": `{
			"name": "Scheme Host", "type": "smb",
			"config": {"host": "smb://nas.example.com", "share": "backup", "username": "u"},
			"credentials": {"password": "p"}
		}`,
		"host unc form": `{
			"name": "UNC Host", "type": "smb",
			"config": {"host": "\\\\nas\\backup", "share": "backup", "username": "u"},
			"credentials": {"password": "p"}
		}`,
		"share with slash": `{
			"name": "Slash Share", "type": "smb",
			"config": {"host": "nas", "share": "backup/photos", "username": "u"},
			"credentials": {"password": "p"}
		}`,
		"missing password": `{
			"name": "No Password", "type": "smb",
			"config": {"host": "nas", "share": "backup", "username": "u"}
		}`,
		"unknown config field": `{
			"name": "Unknown Field", "type": "smb",
			"config": {"host": "nas", "share": "backup", "username": "u", "encryption": "required"},
			"credentials": {"password": "p"}
		}`,
		"config union contamination": `{
			"name": "Contaminated", "type": "smb",
			"config": {"host": "nas", "share": "backup", "username": "u", "endpoint": "https://x/"},
			"credentials": {"password": "p"}
		}`,
		"credentials wrong group": `{
			"name": "Wrong Creds", "type": "smb",
			"config": {"host": "nas", "share": "backup", "username": "u"},
			"credentials": {"secret_key": "k"}
		}`,
	} {
		rec := doJSON(t, router, "POST", "/api/v1/sources", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400, body = %s", name, rec.Code, rec.Body.String())
		}
	}
}
