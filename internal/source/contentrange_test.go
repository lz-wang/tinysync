package source

import (
	"testing"
	"time"
)

// ParseContentRange 的解析矩阵：标准形态、空白容忍、以及一切必须
// 拒绝的形态（416 unsatisfied-range、越界区间、畸形值）。
func TestParseContentRange(t *testing.T) {
	valid := []struct {
		v          string
		startTotal [2]int64
	}{
		{"bytes 734003200-1073741824/1073741826", [2]int64{734003200, 1073741826}},
		{"bytes 0-0/100", [2]int64{0, 100}},
		{"bytes 0-99/100", [2]int64{0, 100}},
		{"  bytes  5-9/10  ", [2]int64{5, 10}},
	}
	for _, tc := range valid {
		start, total, ok := ParseContentRange(tc.v)
		if !ok || start != tc.startTotal[0] || total != tc.startTotal[1] {
			t.Errorf("ParseContentRange(%q) = (%d, %d, %v), want %v", tc.v, start, total, ok, tc.startTotal)
		}
	}
	invalid := []string{
		"",                     // 空
		"bytes */100",          // 416 unsatisfied-range
		"bytes 0-99",           // 缺 total
		"bytes -99/100",        // 缺 start
		"bytes 0-/100",         // 缺 end
		"bytes 100-99/100",     // end < start
		"bytes 0-100/100",      // end 越过 total（last-byte-pos 必须 < total）
		"bytes 0-99/abc",       // total 非 numeric
		"bytes a-99/100",       // start 非 numeric
		"bytes 0-99/-1",        // total 负
		"items 0-99/100",       // 非 bytes 单位
		"bytes 0-99/100/extra", // 多余段
	}
	for _, v := range invalid {
		if _, _, ok := ParseContentRange(v); ok {
			t.Errorf("ParseContentRange(%q) = ok, want rejected", v)
		}
	}
}

// IfRangeValue 只接受 strong ETag：weak ETag（W/"..."）与仅有
// Last-Modified 的快照一律返回空串——HTTP-date 是 RFC 9110 weak
// validator（秒精度内 same-size 替换不可识别），调用方据此拒绝续传
// 降级完整下载，绝不退化为日期断言。
func TestIfRangeValue(t *testing.T) {
	mod := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		fp   Fingerprint
		want string
	}{
		{Fingerprint{ETag: `"abc"`}, `"abc"`},
		{Fingerprint{ETag: "abc"}, "abc"},
		{Fingerprint{ETag: `W/"abc"`}, ""},
		{Fingerprint{ETag: `W/"abc"`, ModifiedAt: mod}, ""},
		{Fingerprint{ModifiedAt: mod}, ""},
		{Fingerprint{}, ""},
	}
	for _, tc := range cases {
		if got := IfRangeValue(tc.fp); got != tc.want {
			t.Errorf("IfRangeValue(%+v) = %q, want %q", tc.fp, got, tc.want)
		}
	}
}
