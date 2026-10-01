package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// pushoverEndpoint 是 Pushover Messages API 的生产地址。
const pushoverEndpoint = "https://api.pushover.net/1/messages.json"

// pushoverHTTPTimeout 是单次 Pushover 请求的兜底超时；调用方还会为
// 发送器包裹独立 context 超时（ADR 0006：每个发送器单独超时）。
const pushoverHTTPTimeout = 10 * time.Second

// PushoverSender 经 Pushover Messages API 发送通知。第一版只带
// token / user / title / message 四个字段，不涉及 priority / device /
// sound 等高级功能。Token 与 UserKey 只进入请求体，绝不进入错误消息
// 或日志。
type PushoverSender struct {
	Token   string
	UserKey string
	// Endpoint 覆盖 API 地址，测试注入 httptest.Server；空为生产地址。
	Endpoint string
	// Client 注入 HTTP 客户端；nil 时按超时构造。
	Client *http.Client
}

// Send 实现 Sender：构造 form 请求 → 检查 HTTP 状态 → 检查 Pushover
// API 业务结果（status=1）。任何失败以 error 返回，由调用方记结构化
// 日志；错误消息不含 token / user key。
func (s *PushoverSender) Send(ctx context.Context, message Message) error {
	form := url.Values{}
	form.Set("token", s.Token)
	form.Set("user", s.UserKey)
	form.Set("title", message.Title)
	form.Set("message", message.Body)

	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = pushoverEndpoint
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: pushoverHTTPTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build pushover request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("pushover request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read pushover response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pushover http status %d", resp.StatusCode)
	}
	var result struct {
		Status  int      `json:"status"`
		Errors  []string `json:"errors"`
		Request string   `json:"request"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode pushover response: %w", err)
	}
	if result.Status != 1 {
		// errors 是 Pushover 的字段级校验文案，不含 secret 值。
		if len(result.Errors) > 0 {
			return fmt.Errorf("pushover rejected message: %s", strings.Join(result.Errors, "; "))
		}
		return fmt.Errorf("pushover rejected message (status %d)", result.Status)
	}
	return nil
}
