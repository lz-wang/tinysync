package githubrelease

import (
	"fmt"
	"strconv"
	"strings"
)

// dirSeparator 连接版本目录名中的 tag 编码与 Release ID。
const dirSeparator = "__"

// encodeTag 把 tag 编码为版本目录名的 tag 部分（契约见设计文档
// §3.2）：保留 [A-Za-z0-9-_.]，其余每字节编码为 %XX 大写十六进制；
// tag 首字符为 . 时一并编码（避免 . 开头目录与 dot-segment / 隐藏
// 目录歧义）。编码可逆，% 自身编码为 %25，无歧义。
func encodeTag(tag string) string {
	var b strings.Builder
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch {
		case i == 0 && c == '.':
			b.WriteString("%2E")
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// decodeTag 还原 encodeTag 编码的 tag；截断或非法 % 序列返回错误。
func decodeTag(encoded string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(encoded) {
			return "", fmt.Errorf("truncated percent escape in %q", encoded)
		}
		hi, ok1 := unhex(encoded[i+1])
		lo, ok2 := unhex(encoded[i+2])
		if !ok1 || !ok2 {
			return "", fmt.Errorf("invalid percent escape in %q", encoded)
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return b.String(), nil
}

// unhex 解码单个十六进制字符。
func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// versionDirName 构造版本目录名：encodeTag(tag) + "__" + releaseID
// （十进制）。Release ID 是 GitHub 不可变主键，兜底一切 tag 冲突
// （大小写冲突、tag 改名重建），目录名因此全局唯一。
func versionDirName(tag string, releaseID int64) string {
	return encodeTag(tag) + dirSeparator + strconv.FormatInt(releaseID, 10)
}

// splitVersionDir 从版本目录名还原 tag 与 Release ID：剥掉最后一个
// __ 且其后全为十进制数字的后缀。取最后一个 __ 保证 tag 内含 __ 时
// 仍正确切分（tag 编码保留 _，拼接后 id 分隔符必然是最右侧的 __；
// 还原仅用于展示，寻址始终使用完整目录名，契约见设计文档 §3.2）。
func splitVersionDir(name string) (tag string, id int64, ok bool) {
	idx := strings.LastIndex(name, dirSeparator)
	if idx < 0 {
		return "", 0, false
	}
	suffix := name[idx+len(dirSeparator):]
	if suffix == "" {
		return "", 0, false
	}
	id, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil || id < 0 {
		return "", 0, false
	}
	tag, err = decodeTag(name[:idx])
	if err != nil {
		return "", 0, false
	}
	return tag, id, true
}
