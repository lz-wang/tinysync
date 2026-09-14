package source

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// maxNameLength 是 Source name 的长度上限（按 Unicode 字符计）。
const maxNameLength = 128

// ValidateType 校验协议类型，当前仅支持 webdav。
func ValidateType(t Type) error {
	switch t {
	case TypeWebDAV:
		return nil
	case "":
		return fmt.Errorf("%w: type is required", ErrInvalid)
	default:
		return fmt.Errorf("%w: unsupported source type %q", ErrInvalid, t)
	}
}

// ValidateName 校验 name：去除首尾空白后非空，且不超过长度上限。
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return fmt.Errorf("%w: name exceeds %d characters", ErrInvalid, maxNameLength)
	}
	return nil
}

// ValidateEndpoint 校验 endpoint：
//   - scheme 必须为 http / https；
//   - host 必填；
//   - 凭据必须走独立字段，拒绝 https://user:pass@host/ 形式；
//   - 不允许 fragment。
func ValidateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: parse endpoint %q: %v", ErrInvalid, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: endpoint scheme must be http or https, got %q", ErrInvalid, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: endpoint host is required", ErrInvalid)
	}
	if u.User != nil {
		return fmt.Errorf("%w: endpoint must not embed credentials; use separate username and password", ErrInvalid)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%w: endpoint must not contain a fragment", ErrInvalid)
	}
	return nil
}

// ValidateCreateInput 校验创建输入的全部必填与格式约束。
func ValidateCreateInput(input CreateInput) error {
	if err := ValidateName(input.Name); err != nil {
		return err
	}
	if err := ValidateType(input.Type); err != nil {
		return err
	}
	return ValidateEndpoint(input.Endpoint)
}
