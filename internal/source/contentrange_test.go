package source

import "testing"

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
