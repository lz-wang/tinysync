package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"tinysync/internal/auth"
	authsqlite "tinysync/internal/auth/sqlite"
	"tinysync/internal/notification"
	notificationsqlite "tinysync/internal/notification/sqlite"
	"tinysync/internal/storage"
)

// captureSender 记录测试端点触发的发送（api 包本地假实现）。
type captureSender struct {
	mu       sync.Mutex
	messages []notification.Message
}

func (s *captureSender) Send(ctx context.Context, message notification.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	return nil
}

func (s *captureSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

// newNotificationRouter 构造挂载真实通知服务与 dispatcher 的路由
// （发送工厂注入假发送器，测试不访问外网）。
func newNotificationRouter(t *testing.T) (testRouter, *captureSender) {
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
	svc := notification.NewService(notificationsqlite.New(db))
	sender := &captureSender{}
	dispatcher := notification.NewDispatcher(svc,
		notification.WithPushoverSender(func(notification.PushoverSettings) notification.Sender { return sender }),
		notification.WithEmailSender(func(notification.EmailSettings) notification.Sender { return sender }),
	)
	dispatcher.Start()
	t.Cleanup(func() { _ = dispatcher.Shutdown(context.Background()) })
	router := newTestAuth(t, db, Dependencies{Notifications: svc, NotificationDispatch: dispatcher})
	return router, sender
}

// saveFullSettings 落一份两渠道全启用的完整配置。
func saveFullSettings(t *testing.T, router testRouter) {
	t.Helper()
	body := `{"pushover":{"enabled":true,"token":"secret-token-123","user_key":"secret-userkey-456"},
		"email":{"enabled":true,"host":"smtp.example.com","port":465,"security":"tls",
		"username":"tinysync@example.com","password":"secret-pass-789",
		"from":"tinysync@example.com","to":["me@example.com"]}}`
	rec := doJSON(t, router, http.MethodPatch, "/api/v1/notifications/settings", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH settings = %d: %s", rec.Code, rec.Body.String())
	}
}

// assertNoNotificationSecret 检查响应体不含任何通知 secret 明文。
func assertNoNotificationSecret(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"secret-token-123", "secret-userkey-456", "secret-pass-789", "rotated-token-000"} {
		if strings.Contains(body, secret) {
			t.Fatalf("响应泄漏 secret %q：%s", secret, body)
		}
	}
}

// GET 回显：非 secret 字段原样，secret 折叠为 configured 布尔。
func TestNotificationSettingsGetNeverLeaksSecrets(t *testing.T) {
	router, _ := newNotificationRouter(t)
	saveFullSettings(t, router)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/notifications/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET settings = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoNotificationSecret(t, body)
	for _, want := range []string{
		`"enabled":true`,
		`"token_configured":true`,
		`"user_key_configured":true`,
		`"password_configured":true`,
		`"host":"smtp.example.com"`,
		`"port":465`,
		`"security":"tls"`,
		`"from":"tinysync@example.com"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET settings 缺少 %q：%s", want, body)
		}
	}
}

// PATCH 保留 / 替换 / 删除三态：secret 留空保持原值，clear 删除，
// 响应同样不回显。
func TestNotificationSettingsPatchSemantics(t *testing.T) {
	router, _ := newNotificationRouter(t)
	saveFullSettings(t, router)

	// 只改 host，不带任何 secret 字段：全部保留。
	rec := doJSON(t, router, http.MethodPatch, "/api/v1/notifications/settings",
		`{"email":{"host":"smtp2.example.com"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH host = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertNoNotificationSecret(t, body)
	if !strings.Contains(body, `"token_configured":true`) || !strings.Contains(body, `"password_configured":true`) {
		t.Fatalf("未提供 secret 字段时 configured 状态应保持 true：%s", body)
	}

	// 替换 secret。
	rec = doJSON(t, router, http.MethodPatch, "/api/v1/notifications/settings",
		`{"pushover":{"token":"rotated-token-000"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH token = %d: %s", rec.Code, rec.Body.String())
	}
	assertNoNotificationSecret(t, rec.Body.String())

	// clear 删除。
	rec = doJSON(t, router, http.MethodPatch, "/api/v1/notifications/settings",
		`{"email":{"clear_password":true}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH clear_password = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"password_configured":false`) {
		t.Fatalf("clear 后 password_configured 应为 false：%s", rec.Body.String())
	}

	// 校验失败：400 携带 field / reason。
	rec = doJSON(t, router, http.MethodPatch, "/api/v1/notifications/settings",
		`{"email":{"port":99999}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH invalid port = %d, want 400：%s", rec.Code, rec.Body.String())
	}
	var errBody struct {
		Field  string `json:"field"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Field != "email.port" {
		t.Fatalf("error field = %q, want email.port", errBody.Field)
	}
}

// 测试端点用已保存的配置发送；未配置时 400。
func TestNotificationTestEndpoints(t *testing.T) {
	router, sender := newNotificationRouter(t)

	// 未配置直接测试：400。
	rec := doJSON(t, router, http.MethodPost, "/api/v1/notifications/test/pushover", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("test pushover before config = %d, want 400", rec.Code)
	}

	saveFullSettings(t, router)
	for _, path := range []string{"/api/v1/notifications/test/pushover", "/api/v1/notifications/test/email"} {
		rec := doJSON(t, router, http.MethodPost, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("%s 响应应携带 ok=true：%s", path, rec.Body.String())
		}
	}
	if sender.count() != 2 {
		t.Fatalf("测试发送 %d 条，want 2（pushover + email 共用假 sender）", sender.count())
	}
}

// 通知端点是管理面：read scope token 不可见。
func TestNotificationSettingsRequiresAdmin(t *testing.T) {
	dataDir := t.TempDir()
	db, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.Migrate(context.Background(), db, dataDir); err != nil {
		t.Fatalf("storage.Migrate: %v", err)
	}
	svc := notification.NewService(notificationsqlite.New(db))
	router := newTestAuth(t, db, Dependencies{Notifications: svc})

	// 签发一个 read-only token，以 Bearer 访问应被拒。
	authSvc := auth.NewService(authsqlite.NewRepository(db))
	_, raw, err := authSvc.CreateAPIToken(context.Background(), auth.CreateAPITokenInput{
		Name:   "reader",
		Scopes: []auth.Scope{auth.ScopeRead},
	})
	if err != nil {
		t.Fatalf("create read token: %v", err)
	}
	rec := doBearerJSON(t, router, http.MethodGet, "/api/v1/notifications/settings", raw, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read token GET settings = %d, want 403", rec.Code)
	}
}
