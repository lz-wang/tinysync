package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// LastUsedThrottle 是 last_used_at 的写入节流阈值：仓库实现仅当
// 当前值为空或早于 now-threshold 时写入，高频 API 调用不退化为
// 每请求一次 SQLite 元数据写。
const LastUsedThrottle = time.Minute

// API Token 契约常量。
const (
	// apiTokenSecretBytes 是 secret 部分的熵字节数（256 bit，高于
	// OWASP 对 token 随机性的最低要求）。
	apiTokenSecretBytes = 32
	// TokenPrefix 是 API Token 的固定前缀。
	TokenPrefix = "ts_"
	// tokenDisplayLen 是展示用前缀长度（ts_ + 8 字符 secret）。
	tokenDisplayLen = len(TokenPrefix) + 8
	// tokenIDBytes 是 token ID 随机部分的字节数。
	tokenIDBytes = 16
)

// tokenIDPrefix 是 API Token ID 前缀。
const tokenIDPrefix = "tok_"

// maxTokenNameLen 是 token 名称长度上限（字符）。
const maxTokenNameLen = 100

// newAPITokenSecret 生成 raw token（ts_<base64url>）与其 SHA-256
// hash；raw 只在创建时返回一次，数据库只存 hash。
func newAPITokenSecret() (raw string, hash []byte, err error) {
	buf := make([]byte, apiTokenSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("read api token secret: %w", err)
	}
	raw = TokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, sum[:], nil
}

// HashAPIToken 计算 raw token 的 SHA-256 hash（认证查找键）。
func HashAPIToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// DisplayPrefix 返回 raw token 的展示前缀：数据库 prefix 列与 List
// API 只暴露这段，不含完整 secret。
func DisplayPrefix(raw string) string {
	if utf8.RuneCountInString(raw) <= tokenDisplayLen {
		return raw
	}
	return raw[:tokenDisplayLen]
}

// newTokenID 生成 tok_<hex> 的 token ID。
func newTokenID() (string, error) {
	buf := make([]byte, tokenIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read api token id: %w", err)
	}
	return tokenIDPrefix + hex.EncodeToString(buf), nil
}

// ValidateTokenName 校验 token 名称：去空白后 1..100 字符。
func ValidateTokenName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: token name must not be empty", ErrInvalidInput)
	}
	if utf8.RuneCountInString(trimmed) > maxTokenNameLen {
		return fmt.Errorf("%w: token name must be at most %d characters", ErrInvalidInput, maxTokenNameLen)
	}
	return nil
}
