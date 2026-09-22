package githubrelease

import (
	"testing"
)

// TestEncodeTagDecodeTagRoundtrip 编码可逆：任意字节序列 tag 经
// encode → decode 还原。
func TestEncodeTagDecodeTagRoundtrip(t *testing.T) {
	tags := []string{
		"v1.2.0",
		"release-1.0.0-rc.1",
		"v1.0/beta",
		"a b c",
		"tag_with_underscores",
		"tag-with-dashes",
		"百分号",
		"emoji-v1🙂",
		"..",
		".hidden",
		"a%2Fb",
		"trailing__",
		"x__12",
	}
	for _, tag := range tags {
		encoded := encodeTag(tag)
		decoded, err := decodeTag(encoded)
		if err != nil {
			t.Fatalf("decodeTag(%q): %v", encoded, err)
		}
		if decoded != tag {
			t.Errorf("roundtrip %q -> %q -> %q", tag, encoded, decoded)
		}
		// 保留字符集断言：编码结果只含安全字符。
		for i := 0; i < len(encoded); i++ {
			c := encoded[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
				c == '-', c == '_', c == '.', c == '%':
			default:
				t.Errorf("encoded %q contains unsafe byte %q", encoded, string(c))
			}
		}
		// 首字符不为 '.'（dot-segment / 隐藏目录防御）。
		if encoded[0] == '.' {
			t.Errorf("encoded %q starts with dot", encoded)
		}
	}
}

// TestDecodeTagRejectsMalformed 非法 % 序列拒绝。
func TestDecodeTagRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"%", "%2", "%G1", "%1Z", "a%2"} {
		if _, err := decodeTag(bad); err == nil {
			t.Errorf("decodeTag(%q) = nil error, want error", bad)
		}
	}
}

// TestVersionDirNameSplitRoundtrip 目录名构造与切分可逆，含 tag 内
// 出现 __ 与纯数字后缀的歧义场景。
func TestVersionDirNameSplitRoundtrip(t *testing.T) {
	cases := []struct {
		tag string
		id  int64
	}{
		{"v1.2.0", 123456789},
		{"v1.0/beta", 987654321},
		{"x__12", 34},
		{"x__", 56},
		{"__", 7},
		{"trailing__", 8},
		{"release-1.0.0-rc.1", 1122334455},
	}
	for _, tc := range cases {
		name := versionDirName(tc.tag, tc.id)
		tag, id, ok := splitVersionDir(name)
		if !ok {
			t.Fatalf("splitVersionDir(%q) not ok", name)
		}
		if tag != tc.tag || id != tc.id {
			t.Errorf("roundtrip (%q, %d): name %q -> (%q, %d)", tc.tag, tc.id, name, tag, id)
		}
	}
}

// TestSplitVersionDirRejectsMalformed 非目录名形态拒绝。
func TestSplitVersionDirRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "noversion", "a__notdigits", "a__", "a__-1"} {
		if _, _, ok := splitVersionDir(bad); ok {
			t.Errorf("splitVersionDir(%q) = ok, want false", bad)
		}
	}
}
