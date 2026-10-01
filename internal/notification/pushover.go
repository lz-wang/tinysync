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

// Pushover 官方长度上限（UTF-8 字符计）。
const (
	pushoverTitleLimit   = 250
	pushoverMessageLimit = 1024
)

// truncateMarker 是截断时附加的结尾标记，读者可区分「原文到此为止」
// 与「被截断」。
const truncateMarker = "…（内容已截断）"

// truncateRunes 按 rune 截断到 limit（含标记在内不超过 limit），中文
// 不会被切成半个 UTF-8 序列。Email 不截断——本函数只在 Pushover
// channel 内使用。
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	marker := []rune(truncateMarker)
	cut := limit - len(marker)
	if cut < 0 {
		cut = 0
	}
	return string(runes[:cut]) + truncateMarker
}

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
// API 业务结果（status=1）。title / message 先按官方上限做 rune 感知
// 截断——同步失败的错误信息可能很长，恰好是最需要通知的时刻，不能
// 因超长被 Pushover 整条拒绝。任何失败以 error 返回，由调用方记结构
// 化日志；错误消息不含 token / user key。
func (s *PushoverSender) Send(ctx context.Context, message Message) error {
	form := url.Values{}
	form.Set("token", s.Token)
	form.Set("user", s.UserKey)
	form.Set("title", truncateRunes(message.Title, pushoverTitleLimit))
	form.Set("message", truncateRunes(message.Body, pushoverMessageLimit))

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
