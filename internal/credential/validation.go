package credential

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxNameLength 是凭据 name 的长度上限（按 Unicode 字符计）。
const maxNameLength = 128

// ValidateName 校验凭据名称：非空且不超过长度上限。规则与 Source
// name 完全一致，用户无需学习第二套约定；身份由独立 ID 承担，名称
// 只是给人看的标签。调用方负责 trim。
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return fmt.Errorf("%w: name exceeds %d characters", ErrInvalid, maxNameLength)
	}
	return nil
}
