package source

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// ChecksumSHA256Prefix 是 Fingerprint.Checksum 携带内容摘要的算法前缀
// 分隔符（"sha256:<64 hex>"，契约见 docs/design/github-release.md §4/§5）。
const ChecksumSHA256Prefix = "sha256:"

// ParseChecksum 解析 Fingerprint.Checksum 携带的协议摘要：sha256 形式
// 返回十六进制摘要值；空串表示协议未提供摘要（返回空 hex）；声明了
// 其它算法、或 sha256 摘要长度/字符集非法即报错——声明了摘要就必须
// 可校验，宁可失败也不能静默跳过（fail-closed）。adapter 侧的快照
// 校验（githubrelease verify_sha256=required）与 syncjob Downloader 的
// 下载流校验共用本解析器，避免两处维护同一格式规则。
func ParseChecksum(checksum string) (string, error) {
	switch {
	case checksum == "":
		return "", nil
	case strings.HasPrefix(checksum, ChecksumSHA256Prefix):
		wantHex := strings.TrimPrefix(checksum, ChecksumSHA256Prefix)
		if len(wantHex) != sha256.Size*2 || !isHex(wantHex) {
			return "", fmt.Errorf("malformed %s digest %q", ChecksumSHA256Prefix, checksum)
		}
		return wantHex, nil
	default:
		// 未来协议可能携带其它算法；当前一律拒绝，避免「以为校验了
		// 实际没校验」的假安全。
		return "", fmt.Errorf("unsupported checksum format %q", checksum)
	}
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
