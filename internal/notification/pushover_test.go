package notification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pushoverFixture 记录测试服务器收到的一次请求。
type pushoverFixture struct {
	contentType string
	title       string
	body        string
	token       string
	userKey     string
}

// startPushoverServer 启动记录请求并按脚本回应的测试服务器。
func startPushoverServer(t *testing.T, respond func() (int, string)) (*httptest.Server, *pushoverFixture) {
	t.Helper()
	fixture := &pushoverFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		fixture.contentType = r.Header.Get("Content-Type")
		fixture.title = r.PostFormValue("title")
		fixture.body = r.PostFormValue("message")
		fixture.token = r.PostFormValue("token")
		fixture.userKey = r.PostFormValue("user")
		status, payload := respond()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return server, fixture
}

func TestPushoverSendSuccess(t *testing.T) {
	server, fixture := startPushoverServer(t, func() (int, string) {
		return http.StatusOK, `{"status":1,"request":"req-1"}`
	})
	sender := &PushoverSender{
		Token:    "token-1",
		UserKey:  "userkey-1",
		Endpoint: server.URL,
	}
	message := Message{Title: "TinySync · photos · 同步成功", Body: "任务：photos"}
	if err := sender.Send(context.Background(), message); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fixture.title != message.Title || fixture.body != message.Body {
		t.Fatalf("收到 title=%q body=%q，与发送内容不一致", fixture.title, fixture.body)
	}
	if fixture.token != "token-1" || fixture.userKey != "userkey-1" {
		t.Fatalf("收到 token=%q user=%q", fixture.token, fixture.userKey)
	}
	if !strings.HasPrefix(fixture.contentType, "application/x-www-form-urlencoded") {
		t.Fatalf("Content-Type = %q", fixture.contentType)
	}
}

func TestPushoverSendAPIRejects(t *testing.T) {
	server, fixture := startPushoverServer(t, func() (int, string) {
		return http.StatusOK, `{"status":0,"errors":["user identifier is not a valid user key"]}`
	})
	sender := &PushoverSender{Token: "secret-token-value", UserKey: "secret-user-value", Endpoint: server.URL}
	err := sender.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("Send with API rejection = nil, want error")
	}
	// 错误消息回显 Pushover 的校验文案，但绝不包含 secret 值。
	if strings.Contains(err.Error(), "secret-token-value") || strings.Contains(err.Error(), "secret-user-value") {
		t.Fatalf("错误消息泄漏 secret：%v", err)
	}
	if !strings.Contains(err.Error(), "user identifier") {
		t.Fatalf("错误消息缺少 API 校验文案：%v", err)
	}
	if fixture.token != "secret-token-value" {
		t.Fatalf("secret 未随请求发送：token=%q", fixture.token)
	}
}

func TestPushoverSendHTTPError(t *testing.T) {
	server, _ := startPushoverServer(t, func() (int, string) {
		return http.StatusInternalServerError, `{"status":0}`
	})
	sender := &PushoverSender{Token: "t", UserKey: "u", Endpoint: server.URL}
	err := sender.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("Send with http 500 = %v, want status error", err)
	}
}

func TestPushoverSendContextCanceled(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	t.Cleanup(func() {
		close(block)
		server.Close()
	})
	sender := &PushoverSender{Token: "t", UserKey: "u", Endpoint: server.URL}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sender.Send(ctx, Message{Title: "t", Body: "b"}); err == nil {
		t.Fatal("Send with canceled ctx = nil, want error")
	}
}

// 超长内容按官方上限 rune 截断并附标记：同步失败的错误信息可能很长，
// 恰好是最需要通知的时刻，不能因超长被整条拒绝。
func TestPushoverSendTruncatesLongContent(t *testing.T) {
	server, fixture := startPushoverServer(t, func() (int, string) {
		return http.StatusOK, `{"status":1,"request":"req-1"}`
	})
	sender := &PushoverSender{Token: "t", UserKey: "u", Endpoint: server.URL}

	longTitle := strings.Repeat("标题", 200)   // 400 runes > 250
	longBody := strings.Repeat("正文内容。", 300) // 1500 runes > 1024
	if err := sender.Send(context.Background(), Message{Title: longTitle, Body: longBody}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := len([]rune(fixture.title)); got != pushoverTitleLimit {
		t.Fatalf("title runes = %d, want %d", got, pushoverTitleLimit)
	}
	if !strings.HasSuffix(fixture.title, truncateMarker) {
		t.Fatalf("title 未附截断标记：%q", fixture.title[len(fixture.title)-40:])
	}
	if got := len([]rune(fixture.body)); got != pushoverMessageLimit {
		t.Fatalf("body runes = %d, want %d", got, pushoverMessageLimit)
	}
	if !strings.HasSuffix(fixture.body, truncateMarker) {
		t.Fatalf("body 未附截断标记：%q", fixture.body[len(fixture.body)-40:])
	}

	// 短内容原样发送。
	short := Message{Title: "短标题", Body: "短正文"}
	if err := sender.Send(context.Background(), short); err != nil {
		t.Fatalf("Send short: %v", err)
	}
	if fixture.title != short.Title || fixture.body != short.Body {
		t.Fatalf("短内容被意外修改：title=%q body=%q", fixture.title, fixture.body)
	}
}
