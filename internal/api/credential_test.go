package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tinysync/internal/credential"
	"tinysync/internal/credential/keytest"
	credentialsqlite "tinysync/internal/credential/sqlite"
	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

// newCredentialRouter 构造挂载真实凭据服务的路由与数据库句柄（引用
// 索引复用 Source 仓库，与生产装配一致）。
func newCredentialRouter(t *testing.T) (testRouter, *sql.DB) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	srcRepo := sourcesqlite.New(db)
	svc := credential.NewService(credentialsqlite.New(db))
	router := newTestAuth(t, db, Dependencies{Credentials: svc, CredentialRefs: srcRepo})
	return router, db
}

// newKeyPEM 生成一把钥匙 PEM，并返回其中段片段作为泄漏标记（随机
// 钥匙的任何片段都不应出现在响应里）。
func newKeyPEM(t *testing.T) (string, string) {
	t.Helper()
	pem, err := keytest.UnencryptedEd25519()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	mid := len(pem) / 2
	return pem, pem[mid : mid+24]
}

// jq 是 JSON 字符串字面量包装（Go %q 产生合法 JSON 字符串）。
func jq(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// createBody 构造创建凭据的请求体。
func createBody(name, pem string) string {
	return `{"name":` + jq(name) + `,"type":"ssh_key","secret":{"private_key":` + jq(pem) + `}}`
}

// doBearerJSON 以 Bearer token 发送 JSON 请求（不附带 cookie）。
func doBearerJSON(t *testing.T, router testRouter, method, path, rawToken, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if rawToken != "" {
		req.Header.Set("Authorization", "Bearer "+rawToken)
	}
	rec := httptest.NewRecorder()
	router.Engine.ServeHTTP(rec, req)
	return rec
}

// 完整生命周期：创建 → 读取 → 列表 → 改名 → 换钥 → 删除。
// 全程 secret 明文不出现在任何响应中。
func TestCredentialLifecycleAPI(t *testing.T) {
	router, _ := newCredentialRouter(t)
	pem1, marker1 := newKeyPEM(t)
	pem2, marker2 := newKeyPEM(t)

	// POST 创建。
	rec := doJSON(t, router, "POST", "/api/v1/credentials", createBody("NAS 钥匙", pem1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	id, _ := body["id"].(string)
	if id == "" || !strings.HasPrefix(id, "crd_") {
		t.Fatalf("id = %v, want crd_ prefix", body["id"])
	}
	if body["type"] != "ssh_key" {
		t.Errorf("type = %v, want ssh_key", body["type"])
	}
	if fp, _ := body["fingerprint"].(string); !strings.HasPrefix(fp, "SHA256:") {
		t.Errorf("fingerprint = %v, want SHA256: prefix", body["fingerprint"])
	}
	if body["has_passphrase"] != false || body["referenced_by"] != float64(0) {
		t.Errorf("flags = %v / %v, want false / 0", body["has_passphrase"], body["referenced_by"])
	}
	if strings.Contains(rec.Body.String(), marker1) {
		t.Fatalf("create response leaks key material")
	}

	// GET 单个。
	rec = doJSON(t, router, "GET", "/api/v1/credentials/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}

	// 列表：引用计数为 0，不含 secret。
	rec = doJSON(t, router, "GET", "/api/v1/credentials", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), marker1) {
		t.Fatalf("list response leaks key material")
	}

	// PATCH 只改名：指纹不变。
	rec = doJSON(t, router, "PATCH", "/api/v1/credentials/"+id, `{"name":"改名后的钥匙"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body = decodeJSON(t, rec); body["name"] != "改名后的钥匙" {
		t.Errorf("name = %v, want renamed", body["name"])
	}

	// PATCH 换钥：指纹更新为新钥的指纹。
	rec = doJSON(t, router, "PATCH", "/api/v1/credentials/"+id,
		`{"secret":{"private_key":`+jq(pem2)+`}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rekey status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), marker2) {
		t.Fatalf("rekey response leaks key material")
	}
	rekeyBody := decodeJSON(t, rec)
	newFP, _ := rekeyBody["fingerprint"].(string)
	if !strings.HasPrefix(newFP, "SHA256:") {
		t.Errorf("fingerprint after rekey = %v", rekeyBody["fingerprint"])
	}

	// DELETE → 204 → GET 404。
	rec = doJSON(t, router, "DELETE", "/api/v1/credentials/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec = doJSON(t, router, "GET", "/api/v1/credentials/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", rec.Code)
	}
}

// 创建校验：坏 PEM、缺 secret、未知类型、未知字段，一律 400。
func TestCredentialCreateValidationAPI(t *testing.T) {
	router, _ := newCredentialRouter(t)

	rec := doJSON(t, router, "POST", "/api/v1/credentials", createBody("坏钥", "not a pem"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "PEM") {
		t.Errorf("bad pem: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, router, "POST", "/api/v1/credentials", `{"name":"x","type":"ssh_key"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing secret: status = %d", rec.Code)
	}

	rec = doJSON(t, router, "POST", "/api/v1/credentials",
		`{"name":"x","type":"webdav","secret":{"private_key":"junk"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown type: status = %d", rec.Code)
	}

	rec = doJSON(t, router, "POST", "/api/v1/credentials",
		`{"name":"x","type":"ssh_key","bogus":1,"secret":{"private_key":"junk"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: status = %d", rec.Code)
	}
}

// 重名冲突 409（大小写不敏感）。
func TestCredentialNameConflictAPI(t *testing.T) {
	router, _ := newCredentialRouter(t)
	pem, _ := newKeyPEM(t)

	if rec := doJSON(t, router, "POST", "/api/v1/credentials", createBody("NAS", pem)); rec.Code != http.StatusCreated {
		t.Fatalf("first create status = %d", rec.Code)
	}
	rec := doJSON(t, router, "POST", "/api/v1/credentials", createBody("nas", pem))
	if rec.Code != http.StatusConflict {
		t.Errorf("conflict status = %d, want 409", rec.Code)
	}
}

// 加密钥匙：缺口令与错口令 400，正确口令创建成功并回显 has_passphrase。
func TestCredentialEncryptedKeyAPI(t *testing.T) {
	router, _ := newCredentialRouter(t)
	encPEM, err := keytest.EncryptedEd25519("correct-pass")
	if err != nil {
		t.Fatalf("generate encrypted key: %v", err)
	}

	rec := doJSON(t, router, "POST", "/api/v1/credentials", createBody("enc", encPEM))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "passphrase is required") {
		t.Errorf("missing passphrase: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := `{"name":"enc","type":"ssh_key","secret":{"private_key":` + jq(encPEM) + `,"private_key_passphrase":"wrong"}}`
	rec = doJSON(t, router, "POST", "/api/v1/credentials", body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "incorrect private key passphrase") {
		t.Errorf("wrong passphrase: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body = `{"name":"enc","type":"ssh_key","secret":{"private_key":` + jq(encPEM) + `,"private_key_passphrase":"correct-pass"}}`
	rec = doJSON(t, router, "POST", "/api/v1/credentials", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("correct passphrase: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := decodeJSON(t, rec); body["has_passphrase"] != true {
		t.Errorf("has_passphrase = %v, want true", body["has_passphrase"])
	}
}

// 删除被引用凭据：409 + 引用源清单；无引用凭据删除 204；列表回显
// 引用计数。
func TestCredentialDeleteInUseAPI(t *testing.T) {
	router, db := newCredentialRouter(t)
	pem, _ := newKeyPEM(t)

	rec := doJSON(t, router, "POST", "/api/v1/credentials", createBody("被引用", pem))
	created := decodeJSON(t, rec)
	id, _ := created["id"].(string)

	// 直接落一行引用该凭据的 sftp 源（引用态由 config JSON 承载，
	// 领域字段在后续票接入）。
	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO sources (id, name, type, endpoint, username, password, config_json, credentials_json, enabled, created_at, updated_at)
		 VALUES ('src_ref', 'NAS 源', 'sftp', '', '', '', json_object('credential_id', ?), '{}', 1, ?, ?)`,
		id, now, now,
	); err != nil {
		t.Fatalf("insert referencing source: %v", err)
	}

	// 列表回显引用计数（按 id 定位目标条目）。
	rec = doJSON(t, router, "GET", "/api/v1/credentials", "")
	list := decodeJSON(t, rec)
	entries, _ := list["credentials"].([]any)
	if len(entries) != 1 {
		t.Fatalf("credentials = %v, want 1 entry", list["credentials"])
	}
	entry, _ := entries[0].(map[string]any)
	if entry["referenced_by"] != float64(1) {
		t.Errorf("referenced_by = %v, want 1", entry["referenced_by"])
	}

	rec = doJSON(t, router, "DELETE", "/api/v1/credentials/"+id, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete in-use status = %d, want 409", rec.Code)
	}
	body := decodeJSON(t, rec)
	sources, _ := body["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources = %v, want 1 entry", body["sources"])
	}
	ref, _ := sources[0].(map[string]any)
	if ref["id"] != "src_ref" || ref["name"] != "NAS 源" {
		t.Errorf("ref = %v, want src_ref / NAS 源", ref)
	}

	// 无引用凭据删除 204。
	rec = doJSON(t, router, "POST", "/api/v1/credentials", createBody("无引用", pem))
	freeID, _ := decodeJSON(t, rec)["id"].(string)
	rec = doJSON(t, router, "DELETE", "/api/v1/credentials/"+freeID, "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete free status = %d, want 204", rec.Code)
	}
}

// 权限矩阵：read scope 的 Bearer token 与匿名请求对凭据端点一律拒绝。
func TestCredentialScopeAPI(t *testing.T) {
	router, _ := newCredentialRouter(t)
	readRaw, _ := createTokenViaAPI(t, router, `{"name": "reader", "scopes": ["read"]}`)

	if rec := doBearerJSON(t, router, "GET", "/api/v1/credentials", readRaw, ""); rec.Code != http.StatusForbidden {
		t.Errorf("read token list status = %d, want 403", rec.Code)
	}
	if rec := doBearerJSON(t, router, "POST", "/api/v1/credentials", readRaw,
		`{"name":"x","type":"ssh_key","secret":{"private_key":"junk"}}`); rec.Code != http.StatusForbidden {
		t.Errorf("read token create status = %d, want 403", rec.Code)
	}

	rec := doJSON(t, testRouter{Engine: router.Engine}, "GET", "/api/v1/credentials", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rec.Code)
	}
}

// 源凭据引用的 API 语义：存在性校验 400、互斥 400、引用态回显跟随
// 凭据、解绑回退（票 #3）。
func TestSourceCredentialReferenceAPI(t *testing.T) {
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	srcRepo := sourcesqlite.New(db)
	credSvc := credential.NewService(credentialsqlite.New(db))
	srcSvc := source.NewService(srcRepo, fakeFactory{remote: fakeRemote{}})
	srcSvc.Credentials = credSvc
	router := newTestAuth(t, db, Dependencies{Sources: srcSvc, Credentials: credSvc, CredentialRefs: srcRepo})

	// 建凭据。
	pem, _ := newKeyPEM(t)
	rec := doJSON(t, router, "POST", "/api/v1/credentials", createBody("NAS 钥匙", pem))
	credID, _ := decodeJSON(t, rec)["id"].(string)

	sftpCfg := func(credentialID string) string {
		return `"host":"nas.example.com","port":22,"username":"tinysync","remote_root":"/","auth_method":"private_key"` +
			`,"host_key_fingerprint":"","credential_id":` + jq(credentialID)
	}

	// 引用不存在的凭据 → 400。
	rec = doJSON(t, router, "POST", "/api/v1/sources", `{"name":"坏引用","type":"sftp","config":{`+sftpCfg("crd_missing")+`}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not exist") {
		t.Errorf("missing reference: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 互斥：引用 + 内联私钥 → 400。
	rec = doJSON(t, router, "POST", "/api/v1/sources",
		`{"name":"互斥","type":"sftp","config":{`+sftpCfg(credID)+`},"credentials":{"private_key":"junk"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "mutually exclusive") {
		t.Errorf("mutex: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 正常引用创建 → 201，回显 state 跟随凭据。
	rec = doJSON(t, router, "POST", "/api/v1/sources", `{"name":"NAS","type":"sftp","config":{`+sftpCfg(credID)+`}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reference create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	srcID, _ := body["id"].(string)
	state, _ := body["credential_state"].(map[string]any)
	sftpState, _ := state["sftp"].(map[string]any)
	if sftpState == nil || sftpState["private_key_set"] != true {
		t.Errorf("credential_state = %v, want sftp private_key_set true", state)
	}

	// 解绑（config 缺省 credential_id）→ state 回退 false。
	rec = doJSON(t, router, "PATCH", "/api/v1/sources/"+srcID,
		`{"config":{"host":"nas.example.com","port":22,"username":"tinysync","remote_root":"/","auth_method":"private_key","host_key_fingerprint":"","credential_id":""}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unbind: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	state, _ = decodeJSON(t, rec)["credential_state"].(map[string]any)
	sftpState, _ = state["sftp"].(map[string]any)
	if sftpState == nil || sftpState["private_key_set"] != false {
		t.Errorf("state after unbind = %v, want private_key_set false", state)
	}
}

// 提升为凭据：内联私钥源一键转存为凭据并改写引用（票 #6）。
func TestSourcePromoteCredentialAPI(t *testing.T) {
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	srcRepo := sourcesqlite.New(db)
	credSvc := credential.NewService(credentialsqlite.New(db))
	srcSvc := source.NewService(srcRepo, fakeFactory{remote: fakeRemote{}})
	srcSvc.Credentials = credSvc
	router := newTestAuth(t, db, Dependencies{Sources: srcSvc, Credentials: credSvc, CredentialRefs: srcRepo})

	keyPEM, _ := newKeyPEM(t)

	// 建内联私钥源（引用态之外，满足提升资格）。
	rec := doJSON(t, router, "POST", "/api/v1/sources",
		`{"name":"内联源","type":"sftp","config":{"host":"nas.example.com","port":22,"username":"tinysync","remote_root":"/","auth_method":"private_key","host_key_fingerprint":"","credential_id":""},"credentials":{"private_key":`+jq(keyPEM)+`}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inline source: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	srcID, _ := decodeJSON(t, rec)["id"].(string)

	// 提升：200，凭据与引用一并返回。
	rec = doJSON(t, router, "POST", "/api/v1/sources/"+srcID+"/promote-credential",
		`{"name":"提升的钥匙"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	credentialOut, _ := body["credential"].(map[string]any)
	if credentialOut == nil || !strings.HasPrefix(credentialOut["fingerprint"].(string), "SHA256:") {
		t.Errorf("credential = %v, want fingerprint", body["credential"])
	}
	sourceOut, _ := body["source"].(map[string]any)
	config, _ := sourceOut["config"].(map[string]any)
	if config["credential_id"] == "" || config["credential_id"] == nil {
		t.Errorf("source config credential_id = %v, want set", config["credential_id"])
	}

	// 提升后源不再具备资格：再次提升 400。
	rec = doJSON(t, router, "POST", "/api/v1/sources/"+srcID+"/promote-credential",
		`{"name":"再来一次"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("second promote: status = %d, want 400", rec.Code)
	}

	// 名称冲突经凭据唯一性 409：建第二个内联源提升同名。
	rec = doJSON(t, router, "POST", "/api/v1/sources",
		`{"name":"内联源二","type":"sftp","config":{"host":"nas2.example.com","port":22,"username":"tinysync","remote_root":"/","auth_method":"private_key","host_key_fingerprint":"","credential_id":""},"credentials":{"private_key":`+jq(keyPEM)+`}}`)
	srcID2, _ := decodeJSON(t, rec)["id"].(string)
	rec = doJSON(t, router, "POST", "/api/v1/sources/"+srcID2+"/promote-credential",
		`{"name":"提升的钥匙"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("conflict promote: status = %d, want 409", rec.Code)
	}

	// password 方式源：资格不合格 400。
	rec = doJSON(t, router, "POST", "/api/v1/sources",
		`{"name":"密码源","type":"sftp","config":{"host":"nas3.example.com","port":22,"username":"tinysync","remote_root":"/","auth_method":"password","host_key_fingerprint":"","credential_id":""},"credentials":{"password":"pw"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create password source: status = %d", rec.Code)
	}
	pwID, _ := decodeJSON(t, rec)["id"].(string)
	rec = doJSON(t, router, "POST", "/api/v1/sources/"+pwID+"/promote-credential",
		`{"name":"不该成功"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("password promote: status = %d, want 400", rec.Code)
	}
}
