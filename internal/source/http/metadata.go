package http

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tinysync/internal/source"
)

// fileMetadata 是一个文件的精确元数据（HTML listing 模式经 HTTP
// 补全——页面显示的大小是人类可读近似，绝不能进入 Fingerprint）。
type fileMetadata struct {
	Size       int64
	ModifiedAt time.Time
	ETag       string
}

// statFileMetadata 取文件的精确 size / mtime / ETag：优先 HEAD 的
// Content-Length / Last-Modified / ETag；HEAD 不可用（405 或缺失
// Content-Length）时以 GET Range: bytes=0-0 的 Content-Range total
// 兜底。两条路都无法给出精确 size 时返回错误——绝不能以 Size=0
// 继续（Downloader 会把 written != size 判为永久失败，扫描阶段就
// 必须失败）。
func (r *requester) statFileMetadata(ctx context.Context, logical string) (fileMetadata, error) {
	meta, ok, err := r.headMetadata(ctx, logical)
	if err != nil {
		return fileMetadata{}, err
	}
	if ok {
		return meta, nil
	}
	return r.rangeMetadata(ctx, logical)
}

// headMetadata 执行 HEAD；ok=false 表示 HEAD 无法给出精确 size
// （405 / 缺失 Content-Length），调用方走 Range 兜底。
func (r *requester) headMetadata(ctx context.Context, logical string) (meta fileMetadata, ok bool, err error) {
	req, err := r.newRequest(ctx, http.MethodHead, r.fileURL(logical), "")
	if err != nil {
		return fileMetadata{}, false, err
	}
	resp, err := r.do(req)
	if err != nil {
		return fileMetadata{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		return fileMetadata{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return fileMetadata{}, false, classifyResponseError("head", resp)
	}
	// 200 但无 Content-Length（chunked / HEAD 实现不完整）→ 兜底。
	if resp.ContentLength < 0 {
		return fileMetadata{}, false, nil
	}
	meta.Size = resp.ContentLength
	meta.ModifiedAt = parseHTTPTime(resp.Header.Get("Last-Modified"))
	meta.ETag = resp.Header.Get("ETag")
	return meta, true, nil
}

// rangeMetadata 以 GET Range: bytes=0-0 兜底：206 的 Content-Range
// total（bytes 0-0/<total>）是精确 size；服务器忽略 Range 返回 200 时
// Content-Length 即完整长度；未知 total（*）或 416 视为无法确定。
func (r *requester) rangeMetadata(ctx context.Context, logical string) (fileMetadata, error) {
	req, err := r.newRequest(ctx, http.MethodGet, r.fileURL(logical), "")
	if err != nil {
		return fileMetadata{}, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := r.do(req)
	if err != nil {
		return fileMetadata{}, err
	}
	// 只取元数据：立即关闭 body，不消费传输内容。
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range"))
		if !ok {
			return fileMetadata{}, unsupportedExactSize(logical, "range without total size")
		}
		return fileMetadata{
			Size:       total,
			ModifiedAt: parseHTTPTime(resp.Header.Get("Last-Modified")),
			ETag:       resp.Header.Get("ETag"),
		}, nil
	case http.StatusOK:
		// 服务器忽略 Range：Content-Length 是完整 representation 长度。
		if resp.ContentLength < 0 {
			return fileMetadata{}, unsupportedExactSize(logical, "no content-length")
		}
		return fileMetadata{
			Size:       resp.ContentLength,
			ModifiedAt: parseHTTPTime(resp.Header.Get("Last-Modified")),
			ETag:       resp.Header.Get("ETag"),
		}, nil
	case http.StatusRequestedRangeNotSatisfiable:
		// 空？（size=0）不会走到 Range 兜底（HEAD 已给出 0）；到达
		// 此处说明服务器对 0 字节也拒 Range——无法确定精确 size。
		return fileMetadata{}, unsupportedExactSize(logical, "range not satisfiable")
	default:
		return fileMetadata{}, classifyResponseError("range", resp)
	}
}

// unsupportedExactSize 返回「无法产生可靠 Fingerprint」的 permanent
// 错误（ADR 0009：HEAD 与 Range 都不可用时 ScanTree 整体失败）。
func unsupportedExactSize(logical, detail string) error {
	return source.MarkPermanent(fmt.Errorf(
		"cannot determine exact size of %q via HEAD or Range (%s); refusing to fingerprint with size 0", logical, detail))
}

// parseContentRangeTotal 解析 "bytes 0-0/123456789" 形态的 total；
// "*"（未知）返回 false。
func parseContentRangeTotal(header string) (int64, bool) {
	slash := strings.LastIndexByte(header, '/')
	if slash < 0 || slash+1 >= len(header) {
		return 0, false
	}
	total, err := strconv.ParseInt(header[slash+1:], 10, 64)
	if err != nil || total < 0 {
		return 0, false
	}
	return total, true
}

// parseHTTPTime 解析 HTTP 日期头；缺失 / 非法返回零值。
func parseHTTPTime(header string) time.Time {
	if header == "" {
		return time.Time{}
	}
	if t, err := http.ParseTime(header); err == nil {
		return t
	}
	return time.Time{}
}
