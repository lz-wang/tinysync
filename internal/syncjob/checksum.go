package syncjob

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// checksumPrefix 是 GitHub Release Asset digest 的算法前缀分隔符。
// Fingerprint.Checksum 存储协议原样摘要（如 "sha256:<hex>"，契约见
// docs/design/github-release.md §4/§5）。
const checksumPrefix = "sha256:"

// checksumVerifier 是下载流式校验器：consume 每次写入后喂入摘要，
// verify 在传输完成后与期望十六进制值比较。
type checksumVerifier struct {
	hasher  hash.Hash
	wantHex string
}

// newChecksumVerifier 按 Fingerprint.Checksum 构造校验器；空串表示
// 协议未提供摘要，返回 nil（仅做既有字节数校验）。声明了不支持的
// 算法一律报错——声明了摘要就必须校验，宁可失败也不能静默跳过
// （fail-closed，契约见设计文档 §5）。
func newChecksumVerifier(checksum string) (*checksumVerifier, error) {
	switch {
	case checksum == "":
		return nil, nil
	case strings.HasPrefix(checksum, checksumPrefix):
		wantHex := strings.TrimPrefix(checksum, checksumPrefix)
		if len(wantHex) != sha256.Size*2 || !isHex(wantHex) {
			return nil, fmt.Errorf("malformed %s digest %q", checksumPrefix, checksum)
		}
		return &checksumVerifier{hasher: sha256.New(), wantHex: wantHex}, nil
	default:
		// 未来协议可能携带其它算法；当前一律拒绝，避免「以为校验了
		// 实际没校验」的假安全。
		return nil, fmt.Errorf("unsupported checksum format %q", checksum)
	}
}

// verify 在传输完成后比较摘要；不匹配返回错误（调用方按确定性失败
// 处理：不重试、不原子替换）。
func (v *checksumVerifier) verify(target string) error {
	if v == nil {
		return nil
	}
	got := hex.EncodeToString(v.hasher.Sum(nil))
	if got != v.wantHex {
		return fmt.Errorf("checksum mismatch for %s: got sha256:%s, want %s", target, got, v.wantHex)
	}
	return nil
}

// isHex 判断字符串是否全为十六进制字符。
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
