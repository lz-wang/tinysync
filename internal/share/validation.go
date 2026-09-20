package share

import (
	"crypto/rand"
	"fmt"
	"regexp"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
)

const (
	// maxSlugLength 是自定义共享名称（即 slug）的长度上限。
	maxSlugLength = 64
	// randomSlugSize 是随机 slug 的字符数（base62）。
	randomSlugSize = 10
)

// slugPattern 是自定义共享名称的字符集：URL 安全、不含点（避免与
// SPA 静态资源按扩展名判定的分支冲突）、字母或数字开头。
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// slugAlphabet 是随机 slug 的 base62 字母表。
const slugAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// ValidateSlug 校验自定义共享名称（即 slug）。
func ValidateSlug(name string) error {
	if name == "" {
		return fmt.Errorf("share name is empty")
	}
	if len(name) > maxSlugLength {
		return fmt.Errorf("share name %q exceeds %d characters", name, maxSlugLength)
	}
	if !slugPattern.MatchString(name) {
		return fmt.Errorf("share name %q must start with a letter or digit and contain only letters, digits, '-' and '_'", name)
	}
	return nil
}

// RandomSlug 生成 10 字符 base62 随机 slug。取模映射存在轻微分布
// 偏差（256 mod 62），对 slug 而言无安全影响——唯一性由数据库
// UNIQUE 约束兜底。
func RandomSlug() (string, error) {
	buf := make([]byte, randomSlugSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random slug: %w", err)
	}
	out := make([]byte, randomSlugSize)
	for i, b := range buf {
		out[i] = slugAlphabet[int(b)%len(slugAlphabet)]
	}
	return string(out), nil
}

// ValidateCreateInput 校验创建输入的纯格式部分（不涉及文件系统）：
// JobID 必填、目标为合法逻辑路径（"/" 即整个本地根）、自定义名称
// 提供时须是合法 slug。
func ValidateCreateInput(input CreateInput) error {
	if input.JobID == "" {
		return fmt.Errorf("%w: job id is required", source.ErrInvalid)
	}
	if err := filesafe.ValidateLogicalPath(input.Path); err != nil {
		return fmt.Errorf("%w: %v", source.ErrInvalid, err)
	}
	if input.Name != "" {
		if err := ValidateSlug(input.Name); err != nil {
			return fmt.Errorf("%w: %v", source.ErrInvalid, err)
		}
	}
	return nil
}

// ValidateUpdateInput 校验更新输入：Name 提供且非空串时须是合法
// slug；设过期与清过期互斥。
func ValidateUpdateInput(input UpdateInput) error {
	if input.Name != nil && *input.Name != "" {
		if err := ValidateSlug(*input.Name); err != nil {
			return fmt.Errorf("%w: %v", source.ErrInvalid, err)
		}
	}
	if input.ExpiresAt != nil && input.ClearExpires {
		return fmt.Errorf("%w: expires_at and clear_expires are mutually exclusive", source.ErrInvalid)
	}
	return nil
}
