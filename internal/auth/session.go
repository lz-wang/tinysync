package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// Web Session 契约常量。
const (
	// sessionTokenBytes 是 raw session token 的熵字节数（256 bit）。
	sessionTokenBytes = 32
	// SessionTTL 是会话绝对过期时间：7 天，无 sliding、无 remember me。
	SessionTTL = 7 * 24 * time.Hour
	// sessionIDBytes 是会话 ID 随机部分的字节数。
	sessionIDBytes = 16
)

// sessionIDPrefix 是会话 ID 前缀。
const sessionIDPrefix = "ses_"

// newSessionToken 生成 raw session token（32 字节 crypto/rand →
// base64url）与其 SHA-256 hash；数据库只存 hash。
func newSessionToken() (raw string, hash []byte, err error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("read session token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, sum[:], nil
}

// HashSessionToken 计算 raw session token 的 SHA-256 hash（cookie
// 值 → 存储键的唯一步骤）。
func HashSessionToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// newSessionID 生成 ses_<hex> 会话 ID。
func newSessionID() (string, error) {
	buf := make([]byte, sessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read session id: %w", err)
	}
	return sessionIDPrefix + hex.EncodeToString(buf), nil
}
