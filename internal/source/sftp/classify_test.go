package sftp

import (
	"crypto/ed25519"
	"io"
	"os"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"tinysync/internal/source"
)

// testKey 构造一个用于 host key 回调的公钥（内容不重要，回调只比较
// fingerprint 字符串）。
func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pk, err := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, 32)))
	if err != nil {
		t.Fatalf("build test key: %v", err)
	}
	return pk
}

// adapter boundary 的 SFTP 协议错误分类：
//   - host key 不匹配 → permanent（重连不改变对端密钥）；
//   - pkg/sftp 客户端把 SSH_FX_NO_SUCH_FILE / SSH_FX_PERMISSION_DENIED
//     归一为 os.ErrNotExist / os.ErrPermission → permanent；
//   - 连接丢失（ErrSSHFxConnectionLost）与流中断（io.EOF）→ transient。
func TestSFTPErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"host key mismatch", fingerprintCallback("SHA256:bogus")("host", nil, testKey(t)), false},
		{"no such file normalized", os.ErrNotExist, false},
		{"permission denied normalized", os.ErrPermission, false},
		{"connection lost", sftp.ErrSSHFxConnectionLost, true},
		{"stream eof", io.EOF, true},
		{"generic failure", sftp.ErrSSHFxFailure, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("test case error is nil")
			}
			if got := source.IsRetryable(tc.err); got != tc.retryable {
				t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.retryable)
			}
		})
	}
}
