package s3

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	awshttp "github.com/aws/smithy-go/transport/http"

	"tinysync/internal/source"
)

// adapter boundary 的 S3 协议错误分类：408 / 429 / 5xx transient，
// 其余全部 4xx（含 NoSuchKey / 401 / 403 / 404 / 400 / 409 / 412 等）
// permanent——客户端错误重连不会改变结果；无状态码可判定的错误
// 交给通用规则兜底。
func TestClassifyS3Error(t *testing.T) {
	statusErr := func(code int) error {
		return &smithy.OperationError{
			ServiceID:     "S3",
			OperationName: "GetObject",
			Err: &awshttp.ResponseError{
				Response: &awshttp.Response{Response: &http.Response{StatusCode: code}},
				Err:      errors.New(http.StatusText(code)),
			},
		}
	}

	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"no such key", &types.NoSuchKey{}, false},
		{"not found", &types.NotFound{}, false},
		{"401", statusErr(401), false},
		{"403 access denied", statusErr(403), false},
		{"404", statusErr(404), false},
		{"408 request timeout", statusErr(408), true},
		{"429 slow down", statusErr(429), true},
		{"500 internal error", statusErr(500), true},
		{"503 service unavailable", statusErr(503), true},
		{"400 bad request", statusErr(400), false},
		{"409 conflict", statusErr(409), false},
		{"412 precondition failed", statusErr(412), false},
		// 无状态码可判定的错误：通用规则兜底（默认 transient）。
		{"generic wrapped", fmt.Errorf("serialization failed: %w", errors.New("boom")), true},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classified := classifyS3Error(tc.err)
			if classified == nil {
				if tc.err == nil {
					return
				}
				t.Fatal("classifyS3Error returned nil for non-nil error")
			}
			if got := source.IsRetryable(classified); got != tc.retryable {
				t.Errorf("IsRetryable(%v) = %v, want %v", classified, got, tc.retryable)
			}
		})
	}
}
