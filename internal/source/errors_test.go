package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"syscall"
	"testing"
)

// IsRetryable 是重试策略的单一判定点：按优先级验证分类规则。
func TestIsRetryable(t *testing.T) {
	netTimeout := &timeoutNetError{}
	connReset := &net.OpError{Op: "read", Err: syscall.ECONNRESET}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"wrapped context canceled", fmt.Errorf("op: %w", context.Canceled), false},
		// DeadlineExceeded 可能来自单次 attempt 的传输超时：可重试；
		// run 级取消由调用方在重试循环内先检查自身 ctx。
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"marked transient", MarkTransient(errors.New("503 service unavailable")), true},
		{"wrapped marked transient", fmt.Errorf("s3 open /x: %w", MarkTransient(errors.New("slow down"))), true},
		{"marked permanent", MarkPermanent(errors.New("401 unauthorized")), false},
		{"wrapped marked permanent", fmt.Errorf("webdav stat /x: %w", MarkPermanent(errors.New("404"))), false},
		{"permanent wins over later transient", MarkTransient(MarkPermanent(errors.New("403"))), false},
		{"local permission", fs.ErrPermission, false},
		{"wrapped local permission", fmt.Errorf("create temp: %w", os.ErrPermission), false},
		{"not exist", fs.ErrNotExist, false},
		{"enospc", syscall.ENOSPC, false},
		{"io eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"net timeout", netTimeout, true},
		{"conn reset", connReset, true},
		{"conn refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true},
		{"unknown error defaults transient", errors.New("mystery failure"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// MarkTransient / MarkPermanent 幂等：重复标记不产生嵌套包装。
func TestMarkIdempotent(t *testing.T) {
	base := errors.New("boom")
	once := MarkTransient(base)
	twice := MarkTransient(once)
	if once != twice {
		t.Error("MarkTransient should be idempotent")
	}

	perm := MarkPermanent(base)
	permTwice := MarkPermanent(perm)
	if perm != permTwice {
		t.Error("MarkPermanent should be idempotent")
	}

	if MarkTransient(nil) != nil || MarkPermanent(nil) != nil {
		t.Error("marking nil should return nil")
	}
}

// timeoutNetError 实现 net.Error 且 Timeout() 为 true。
type timeoutNetError struct{}

func (e *timeoutNetError) Error() string { return "i/o timeout" }

func (e *timeoutNetError) Timeout() bool { return true }

func (e *timeoutNetError) Temporary() bool { return true }

// compile-time 断言：确保测试类型满足接口。
var _ net.Error = (*timeoutNetError)(nil)
