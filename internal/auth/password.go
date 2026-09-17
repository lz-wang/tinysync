package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// 密码策略与 Argon2id 参数。参数为 OWASP 当前最低推荐；在目标机器
// benchmark 后可适度提高，变更需同步修订实现契约文档。
const (
	// minPasswordChars 是密码最小字符数。
	minPasswordChars = 12
	// maxPasswordBytes 是密码最大输入字节数，避免超长输入造成
	// 密码 KDF DoS。
	maxPasswordBytes = 1024

	// argon2MemoryKiB 是 Argon2id 内存参数（19456 KiB = 19 MiB）。
	argon2MemoryKiB = 19 * 1024
	// argon2Iterations 是 Argon2id 迭代次数。
	argon2Iterations = 2
	// argon2Parallelism 是 Argon2id 并行度。
	argon2Parallelism = 1
	// argon2KeyLen 是派生密钥长度（字节）。
	argon2KeyLen = 32
	// argon2SaltLen 是随机 salt 长度（字节）。
	argon2SaltLen = 16
)

// phcAlgo 是本实现生成与接受的 PHC 算法标识。
const phcAlgo = "argon2id"

// ErrPasswordHash 表示 PHC hash 格式损坏或参数不被支持。
var ErrPasswordHash = errors.New("auth: malformed password hash")

// ValidatePassword 校验密码策略：至少 12 字符且不超过 1024 字节。
func ValidatePassword(password string) error {
	if chars := utf8.RuneCountInString(password); chars < minPasswordChars {
		return fmt.Errorf("%w: password must be at least %d characters", ErrInvalidInput, minPasswordChars)
	}
	if len(password) > maxPasswordBytes {
		return fmt.Errorf("%w: password must be at most %d bytes", ErrInvalidInput, maxPasswordBytes)
	}
	return nil
}

// HashPassword 校验密码策略并生成 PHC 风格完整编码的 Argon2id
// hash，如 $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>。salt 来自
// crypto/rand。
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2MemoryKiB, argon2Parallelism, argon2KeyLen)
	return encodePHC(salt, key), nil
}

// VerifyPassword 校验明文密码与 PHC hash 是否匹配。hash 格式损坏
// 或参数不支持返回 ErrPasswordHash——调用方按凭据错误处理，绝不
// panic。hash 自带参数参与重派生，参数未来提高后存量 hash 仍可校验。
func VerifyPassword(password, phc string) (bool, error) {
	salt, want, memory, iterations, parallelism, err := decodePHC(phc)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, nil
	}
	return true, nil
}

// encodePHC 把 salt 与派生密钥编码为 PHC 字符串；base64 使用无填充
// 标准 alphabet（PHC 规范）。
func encodePHC(salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		phcAlgo, argon2.Version, argon2MemoryKiB, argon2Iterations, argon2Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

// decodePHC 解析 PHC 字符串并提取 salt、密钥与参数。
func decodePHC(phc string) (salt, key []byte, memory, iterations uint32, parallelism uint8, err error) {
	bad := func() ([]byte, []byte, uint32, uint32, uint8, error) {
		return nil, nil, 0, 0, 0, fmt.Errorf("%w: %q", ErrPasswordHash, phc)
	}
	parts := strings.Split(phc, "$")
	// 空串 + algo + version + params + salt + hash 共 6 段。
	if len(parts) != 6 || parts[0] != "" || parts[1] != phcAlgo {
		return bad()
	}
	if !strings.HasPrefix(parts[2], "v=") {
		return bad()
	}
	version, verr := strconv.Atoi(strings.TrimPrefix(parts[2], "v="))
	if verr != nil || version != argon2.Version {
		return bad()
	}
	memory, iterations, parallelism, perr := parsePHCParams(parts[3])
	if perr != nil {
		return bad()
	}
	b64 := base64.RawStdEncoding
	salt, serr := b64.DecodeString(parts[4])
	if serr != nil || len(salt) == 0 {
		return bad()
	}
	key, kerr := b64.DecodeString(parts[5])
	if kerr != nil || len(key) == 0 {
		return bad()
	}
	return salt, key, memory, iterations, parallelism, nil
}

// parsePHCParams 解析 m=,t=,p= 参数段；缺失或非法参数报错。
func parsePHCParams(raw string) (memory, iterations uint32, parallelism uint8, err error) {
	for part := range strings.SplitSeq(raw, ",") {
		name, value, found := strings.Cut(part, "=")
		if !found || value == "" {
			return 0, 0, 0, fmt.Errorf("malformed param %q", part)
		}
		switch name {
		case "m":
			if memory = parseUint32(value); memory == 0 {
				return 0, 0, 0, fmt.Errorf("malformed m %q", value)
			}
		case "t":
			if iterations = parseUint32(value); iterations == 0 {
				return 0, 0, 0, fmt.Errorf("malformed t %q", value)
			}
		case "p":
			p, e := strconv.ParseUint(value, 10, 8)
			if e != nil || p == 0 {
				return 0, 0, 0, fmt.Errorf("malformed p %q", value)
			}
			parallelism = uint8(p)
		default:
			return 0, 0, 0, fmt.Errorf("unknown param %q", name)
		}
	}
	if memory == 0 || iterations == 0 || parallelism == 0 {
		return 0, 0, 0, errors.New("missing param")
	}
	return memory, iterations, parallelism, nil
}

// parseUint32 解析十进制 uint32，失败返回 0。
func parseUint32(raw string) uint32 {
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}
