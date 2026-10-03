package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"tinysync/internal/credential"
	credentialsqlite "tinysync/internal/credential/sqlite"
	"tinysync/internal/source"
	sourcesqlite "tinysync/internal/source/sqlite"
	"tinysync/internal/storage"
)

type fakeCheckRemote struct {
	fakeRemote
	fileRoot bool
	listErr  error
	closed   bool
	lastCtx  context.Context
}

func (r *fakeCheckRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	r.lastCtx = ctx
	if path != "/" {
		return source.FileInfo{}, errors.New("unexpected root path")
	}
	return source.FileInfo{Path: path, IsDir: !r.fileRoot}, r.statErr
}

func (r *fakeCheckRemote) List(ctx context.Context, path string, _ source.ListOptions) (source.FilePage, error) {
	if path != "/" || ctx != r.lastCtx {
		return source.FilePage{}, errors.New("unexpected root or context")
	}
	return source.FilePage{}, r.listErr
}

func (r *fakeCheckRemote) Close() error {
	r.closed = true
	return nil
}

func newSFTPCheckRouter(t *testing.T) (testRouter, *fakeInspectFactory) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatal(err)
	}
	factory := &fakeInspectFactory{remote: &fakeCheckRemote{}}
	repo := sourcesqlite.New(db)
	svc := source.NewService(repo, factory)
	creds := credential.NewService(credentialsqlite.New(db))
	svc.Credentials = creds
	return newTestAuth(t, db, Dependencies{Sources: svc, Credentials: creds, CredentialRefs: repo}), factory
}

const checkConfig = `{"host":"nas.example.com","port":2222,"username":"user","remote_root":"/srv/files","auth_method":"password"}`

const checkBody = `{"config":` + checkConfig + `,"credentials":{"password":"CHECK_SECRET"}}`

func TestSFTPCheckAPI(t *testing.T) {
	router, factory := newSFTPCheckRouter(t)
	rec := doJSON(t, router, "POST", "/api/v1/sources/check", checkBody)
	if rec.Code != http.StatusOK || decodeJSON(t, rec)["ok"] != true {
		t.Fatalf("check = %d, %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "CHECK_SECRET") {
		t.Fatal("check response contains secret")
	}
	if cfg := factory.lastSrc.Config.SFTP; cfg.RemoteRoot != "/srv/files" || cfg.Port != 2222 || factory.lastCreds.SFTP.Password != "CHECK_SECRET" {
		t.Fatal("check did not use proposed config and credentials")
	}
	remote := factory.remote.(*fakeCheckRemote)
	deadline, ok := remote.lastCtx.Deadline()
	if !remote.closed || !ok || time.Until(deadline) > 10*time.Second || remote.lastCtx.Err() == nil {
		t.Fatal("check must have a timeout, cancel context and close remote")
	}
	sources := decodeJSON(t, doJSON(t, router, "GET", "/api/v1/sources", ""))["sources"].([]any)
	if len(sources) != 0 {
		t.Fatal("check persisted a source")
	}
}

func TestSFTPCheckRemoteFailuresAPI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote *fakeCheckRemote
	}{
		{"missing directory", &fakeCheckRemote{fakeRemote: fakeRemote{statErr: errors.New("directory not found")}}},
		{"file root", &fakeCheckRemote{fileRoot: true}},
		{"unreadable directory", &fakeCheckRemote{listErr: errors.New("permission denied")}},
		{"canceled operation", &fakeCheckRemote{listErr: context.DeadlineExceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newSourceRouter(t, tc.remote)
			rec := doJSON(t, router, "POST", "/api/v1/sources/check", checkBody)
			body := decodeJSON(t, rec)
			if rec.Code != http.StatusOK || body["ok"] != false || body["error"] == "" || !tc.remote.closed {
				t.Fatalf("failed check = %d, %v; closed=%v", rec.Code, body, tc.remote.closed)
			}
		})
	}
	router := newSourceRouterWithFactory(t, fakeFactory{createErr: errors.New("authentication failed")})
	rec := doJSON(t, router, "POST", "/api/v1/sources/check", checkBody)
	if rec.Code != http.StatusOK || decodeJSON(t, rec)["ok"] != false {
		t.Fatalf("connection failure = %d, %s", rec.Code, rec.Body.String())
	}
}

func TestSFTPCheckValidationAPI(t *testing.T) {
	router, factory := newSFTPCheckRouter(t)
	for _, body := range []string{
		`{}`, `{"config":null}`, `{"config":{}}`,
		checkBody + `{}`,
		strings.Replace(checkBody, `"host":`, `"unknown":`, 1),
		strings.Replace(checkBody, `"port":2222`, `"port":65536`, 1),
		strings.Replace(checkBody, `/srv/files`, `relative/path`, 1),
		strings.Replace(checkBody, `"password":"CHECK_SECRET"`, `"token":"CHECK_SECRET"`, 1),
		strings.Replace(checkBody, `"password":"CHECK_SECRET"`, `"password":""`, 1),
		strings.Replace(checkBody, `"config":`, `"unknown":true,"config":`, 1),
	} {
		if rec := doJSON(t, router, "POST", "/api/v1/sources/check", body); rec.Code != http.StatusBadRequest {
			t.Errorf("invalid check status = %d, want 400", rec.Code)
		}
	}
	if factory.lastSrc.Type != "" {
		t.Fatal("invalid request reached remote factory")
	}
	rec := doJSON(t, router, "POST", "/api/v1/sources/check", `{"source_id":"missing","config":`+checkConfig+`}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing source status = %d, want 404", rec.Code)
	}
}

func TestSFTPCheckEditDoesNotPersistAPI(t *testing.T) {
	router, factory := newSFTPCheckRouter(t)
	rec := doJSON(t, router, "POST", "/api/v1/sources", `{"name":"NAS","type":"sftp","config":`+checkConfig+`,"credentials":{"password":"SAVED_SECRET"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	id := decodeJSON(t, rec)["id"].(string)
	before := doJSON(t, router, "GET", "/api/v1/sources/"+id, "").Body.String()
	config := strings.Replace(checkConfig, `/srv/files`, `/new/root`, 1)
	for _, tc := range []struct{ patch, want string }{
		{"", "SAVED_SECRET"},
		{`,"credentials":{"password":"NEW_SECRET"}`, "NEW_SECRET"},
	} {
		rec = doJSON(t, router, "POST", "/api/v1/sources/check", `{"source_id":"`+id+`","config":`+config+tc.patch+`}`)
		if rec.Code != http.StatusOK || decodeJSON(t, rec)["ok"] != true || factory.lastCreds.SFTP.Password != tc.want || factory.lastSrc.Config.SFTP.RemoteRoot != "/new/root" {
			t.Fatal("edit check did not apply temporary overrides")
		}
	}
	rec = doJSON(t, router, "POST", "/api/v1/sources/check", `{"source_id":"`+id+`","config":`+config+`,"credentials":{"password":""}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatal("explicitly cleared password reused stored secret")
	}
	if after := doJSON(t, router, "GET", "/api/v1/sources/"+id, "").Body.String(); after != before {
		t.Fatal("edit check changed stored source")
	}
}

func TestSFTPCheckCredentialReferenceAPI(t *testing.T) {
	router, factory := newSFTPCheckRouter(t)
	pem, _ := newKeyPEM(t)
	created := doJSON(t, router, "POST", "/api/v1/credentials", createBody("check key", pem))
	id := decodeJSON(t, created)["id"].(string)
	body := `{"config":{"host":"nas","username":"user","remote_root":"","auth_method":"private_key","credential_id":"` + id + `"}}`
	rec := doJSON(t, router, "POST", "/api/v1/sources/check", body)
	if rec.Code != http.StatusOK || decodeJSON(t, rec)["ok"] != true || factory.lastCreds.SFTP.PrivateKey != pem || factory.lastSrc.Config.SFTP.RemoteRoot != "" {
		t.Fatal("check did not resolve proposed credential reference or preserve Home root")
	}
	rec = doJSON(t, router, "POST", "/api/v1/sources/check", strings.Replace(body, id, "missing", 1))
	if rec.Code != http.StatusBadRequest {
		t.Fatal("missing credential reference was accepted")
	}
}

func TestSFTPCheckScopeAPI(t *testing.T) {
	router, _ := newSFTPCheckRouter(t)
	readToken, _ := createTokenViaAPI(t, router, `{"name":"reader","scopes":["read"]}`)
	if rec := doBearerJSON(t, router, "POST", "/api/v1/sources/check", readToken, checkBody); rec.Code != http.StatusForbidden {
		t.Errorf("read token status = %d, want 403", rec.Code)
	}
	if rec := doJSON(t, testRouter{Engine: router.Engine}, "POST", "/api/v1/sources/check", checkBody); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rec.Code)
	}
}
