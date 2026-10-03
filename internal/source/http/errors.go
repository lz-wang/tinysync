package http

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"tinysync/internal/source"
)

// 远端错误分类（ADR 0009）：协议错误判断只在 adapter boundary 内完成，
// 上层只问 source.IsRetryable，不出现 HTTP 分支。
//
//	400 / 401 / 403 / 404 / 409 / 410 → permanent
//	405（HEAD）→ metadata fallback 决策，不直接分类
//	408 / 429 / 5xx → transient
//	TLS 证书错误 / DNS 不存在 → permanent
//	DNS 临时 / 连接重置 / 传输层 → transient
//	ctx 取消 → 原样透传（调用方生命周期控制）

// classifyResponseError 把非 2xx 响应转换为已分类错误。
func classifyResponseError(op string, resp *http.Response) error {
	err := fmt.Errorf("http %s: %s %s: %s", op, resp.Request.Method, resp.Request.URL.Redacted(), resp.Status)
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusGone:
		return source.MarkPermanent(err)
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return source.MarkTransient(err)
	default:
		if resp.StatusCode >= 500 {
			return source.MarkTransient(err)
		}
		// 其余 4xx（含 405 由调用方在 fallback 决策后走到这里）是
		// 确定性失败。
		return source.MarkPermanent(err)
	}
}

// redirectError 标记 redirect 收敛策略的拒绝：同源 / 子树约束是
// 确定性失败（permanent），不做无意义重试。
type redirectError struct{ err error }

func (e *redirectError) Error() string { return e.err.Error() }
func (e *redirectError) Unwrap() error { return e.err }

// classifyTransportError 分类传输层错误：ctx 取消原样透传；TLS 证书
// 校验、DNS 不存在与 redirect 越界是配置级确定性失败；其余（连接
// 重置、DNS 临时、超时、EOF）按瞬时处理——与 source.IsRetryable 的
// 保守默认一致。
func classifyTransportError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var certErr x509.CertificateInvalidError
	var authErr x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var constraintErr x509.ConstraintViolationError
	if errors.As(err, &certErr) || errors.As(err, &authErr) ||
		errors.As(err, &hostErr) || errors.As(err, &constraintErr) {
		return source.MarkPermanent(fmt.Errorf("http %s: %w", op, err))
	}
	var redirErr *redirectError
	if errors.As(err, &redirErr) {
		return source.MarkPermanent(fmt.Errorf("http %s: %w", op, err))
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return source.MarkPermanent(fmt.Errorf("http %s: %w", op, err))
	}
	return source.MarkTransient(fmt.Errorf("http %s: %w", op, err))
}

// unsupportedListingError 返回「端点可达但没有可识别目录索引」的
// permanent 错误——HTTP 200 但页面是普通网站 / index.html 时，
// TestConnection 与扫描都必须明确失败，不静默降级为空目录。
func unsupportedListingError(detail string) error {
	return source.MarkPermanent(fmt.Errorf(
		"HTTP endpoint is reachable, but no supported directory listing was detected%s", detail))
}

// normalizeHTTPErr 把 http.Client 返回的 *url.Error 中的 ctx 取消
// 剥出来（调用方以 errors.Is(err, context.Canceled) 判定取消语义）。
func normalizeHTTPErr(err error) error {
	if err == nil {
		return nil
	}
	if uerr := new(url.Error); errors.As(err, &uerr) {
		if inner := uerr.Unwrap(); inner != nil {
			if errors.Is(inner, context.Canceled) || errors.Is(inner, context.DeadlineExceeded) {
				return inner
			}
		}
	}
	return err
}
