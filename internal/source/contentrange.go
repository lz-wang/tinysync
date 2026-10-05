package source

import (
	"strconv"
	"strings"
)

// ParseContentRange 解析 RFC 9110 Content-Range 的 byte-range 形态
// （206 Partial Content 响应）："bytes <start>-<end>/<total>"，容忍
// 首尾与单位后的空白。返回区间起点 start 与完整资源大小 total。
// 解析失败（含 416 响应的 "bytes */total" unsatisfied-range 形态与
// 越界区间）返回 ok=false——调用方按「无法证明流起点」处理，绝不
// 把未经验证的区间交给断点拼接（ADR 0010）。
func ParseContentRange(v string) (start, total int64, ok bool) {
	v = strings.TrimSpace(v)
	rest, found := strings.CutPrefix(v, "bytes")
	if !found {
		return 0, 0, false
	}
	rest = strings.TrimSpace(rest)
	rangePart, totalPart, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, false
	}
	totalPart = strings.TrimSpace(totalPart)
	total, err := strconv.ParseInt(totalPart, 10, 64)
	if err != nil || total < 0 {
		return 0, 0, false
	}
	rangePart = strings.TrimSpace(rangePart)
	startStr, endStr, found := strings.Cut(rangePart, "-")
	if !found {
		return 0, 0, false
	}
	start, err = strconv.ParseInt(strings.TrimSpace(startStr), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end, err := strconv.ParseInt(strings.TrimSpace(endStr), 10, 64)
	if err != nil || end < start || end >= total {
		return 0, 0, false
	}
	return start, total, true
}

// IfRangeValue 从快照指纹选择 HTTP If-Range 头的值。RFC 9110 要求
// If-Range 只能携带 strong validator：weak ETag（W/"..."）不得使用；
// HTTP-date 只在客户端能认定其为 strong validator 时才允许，而普通
// Last-Modified 默认是 weak validator——HTTP-date 只有秒精度，同一秒
// 内的 same-size 替换与未变更无法区分。因此这里只接受 strong ETag，
// 其余情况（含仅有 Last-Modified）返回空串，调用方拒绝续传并降级
// 完整下载（ADR 0010：无法证明 remote identity 时宁可完整重传）。
// 供 HTTP 与 WebDAV 的 ResumableRemote 实现共用。
func IfRangeValue(expected Fingerprint) string {
	if expected.ETag != "" && !strings.HasPrefix(expected.ETag, "W/") &&
		!strings.HasPrefix(expected.ETag, `W/"`) {
		return expected.ETag
	}
	return ""
}
