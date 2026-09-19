package webdav

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"tinysync/internal/source"
)

// unquotedETagHandler 构造返回固定 207 Multi-Status 的 WebDAV 服务端，
// 单个文件条目 /docs/report.txt，getetag 值由调用方注入（不转义，
// 用于模拟 Synology / lighttpd 等返回裸值或弱 ETag 的非合规服务器）。
func unquotedETagHandler(etag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=\"utf-8\"")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
 <D:response>
  <D:href>/docs/report.txt</D:href>
  <D:propstat>
   <D:prop>
    <D:resourcetype/>
    <D:getcontentlength>13</D:getcontentlength>
    <D:getlastmodified>Fri, 18 Sep 2026 00:00:00 GMT</D:getlastmodified>
    <D:getetag>%s</D:getetag>
   </D:prop>
   <D:status>HTTP/1.1 200 OK</D:status>
  </D:propstat>
 </D:response>
</D:multistatus>
`, etag)
	})
}

// listEntry 在 List 结果中查找指定路径的条目。
func listEntry(t *testing.T, page source.FilePage, path string) source.FileInfo {
	t.Helper()
	for _, e := range page.Entries {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("entries = %+v, want %s", page.Entries, path)
	return source.FileInfo{}
}

// 未加引号的裸 ETag（Synology DSM 等服务器实际行为）不应导致 List
// 整体失败：ETag 是 opaque token，adapter 只透传不解析格式。
func TestListUnquotedETag(t *testing.T) {
	srv := startServer(t, unquotedETagHandler("3322f5c16e7a1b1c8f2a5b8c9d0e1f2a"))
	r := newRemote(t, srv, "", "")

	page, err := r.List(context.Background(), "/docs", source.ListOptions{})
	if err != nil {
		t.Fatalf("List /docs: %v", err)
	}
	entry := listEntry(t, page, "/docs/report.txt")
	if got := entry.Fingerprint.ETag; got != "3322f5c16e7a1b1c8f2a5b8c9d0e1f2a" {
		t.Errorf("ETag = %q, want raw value preserved", got)
	}
}

// 弱 ETag（W/"..."）同样无法通过 strconv.Unquote，且 W/ 前缀是实体
// 版本语义的一部分，必须完整保留。
func TestListWeakETag(t *testing.T) {
	srv := startServer(t, unquotedETagHandler(`W/"abc123"`))
	r := newRemote(t, srv, "", "")

	page, err := r.List(context.Background(), "/docs", source.ListOptions{})
	if err != nil {
		t.Fatalf("List /docs: %v", err)
	}
	entry := listEntry(t, page, "/docs/report.txt")
	if got := entry.Fingerprint.ETag; got != `W/"abc123"` {
		t.Errorf("ETag = %q, want W/\"abc123\"", got)
	}
}

// Stat（同步引擎对单个远端文件的探测路径）同样受裸 ETag 影响。
func TestStatUnquotedETag(t *testing.T) {
	srv := startServer(t, unquotedETagHandler("3322f5c16e7a1b1c8f2a5b8c9d0e1f2a"))
	r := newRemote(t, srv, "", "")

	info, err := r.Stat(context.Background(), "/docs/report.txt")
	if err != nil {
		t.Fatalf("Stat /docs/report.txt: %v", err)
	}
	if got := info.Fingerprint.ETag; got != "3322f5c16e7a1b1c8f2a5b8c9d0e1f2a" {
		t.Errorf("ETag = %q, want raw value preserved", got)
	}
}

// 合规的带引号 ETag 不被净化层改写：库正常去引号得到裸值。
func TestListQuotedETagPreserved(t *testing.T) {
	srv := startServer(t, unquotedETagHandler(`"abc123"`))
	r := newRemote(t, srv, "", "")

	page, err := r.List(context.Background(), "/docs", source.ListOptions{})
	if err != nil {
		t.Fatalf("List /docs: %v", err)
	}
	entry := listEntry(t, page, "/docs/report.txt")
	if got := entry.Fingerprint.ETag; got != "abc123" {
		t.Errorf("ETag = %q, want abc123", got)
	}
}

// sanitizeETags 表格测试：净化只作用于不合规的 getetag 值，合规值、
// 空 value、自闭合与无关元素一律原样保留。重写值经 xml.EscapeText
// 输出，引号表现为数字实体 &#34;（XML 解码后即 "）。
func TestSanitizeETags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "合规带引号值原样保留",
			in:   `<D:getetag>"abc123"</D:getetag>`,
			want: `<D:getetag>"abc123"</D:getetag>`,
		},
		{
			name: "裸 hex 补引号",
			in:   `<D:getetag>deadbeef</D:getetag>`,
			want: `<D:getetag>&#34;deadbeef&#34;</D:getetag>`,
		},
		{
			name: "弱 ETag 补引号并保留 W/ 前缀",
			in:   `<D:getetag>W/"abc"</D:getetag>`,
			want: `<D:getetag>&#34;W/\&#34;abc\&#34;&#34;</D:getetag>`,
		},
		{
			name: "带缩进空白的合规值规范化",
			in:   "<D:getetag>\n  \"abc\"\n</D:getetag>",
			want: `<D:getetag>&#34;abc&#34;</D:getetag>`,
		},
		{
			name: "实体编码的合规值等价保留",
			in:   `<D:getetag>&quot;abc&quot;</D:getetag>`,
			want: `<D:getetag>&quot;abc&quot;</D:getetag>`,
		},
		{
			name: "小写前缀同样净化",
			in:   `<d:getetag>deadbeef</d:getetag>`,
			want: `<d:getetag>&#34;deadbeef&#34;</d:getetag>`,
		},
		{
			name: "无前缀带 xmlns 属性同样净化",
			in:   `<getetag xmlns="DAV:">deadbeef</getetag>`,
			want: `<getetag xmlns="DAV:">&#34;deadbeef&#34;</getetag>`,
		},
		{
			name: "空值原样保留",
			in:   `<D:getetag></D:getetag>`,
			want: `<D:getetag></D:getetag>`,
		},
		{
			name: "自闭合不匹配原样保留",
			in:   `<D:getetag/>`,
			want: `<D:getetag/>`,
		},
		{
			name: "无关元素不受影响",
			in:   `<D:getcontentlength>12345</D:getcontentlength>`,
			want: `<D:getcontentlength>12345</D:getcontentlength>`,
		},
		{
			name: "多条目混合只净化不合规值",
			in:   `<a><D:getetag>"ok"</D:getetag><b/><D:getetag>bad</D:getetag></a>`,
			want: `<a><D:getetag>"ok"</D:getetag><b/><D:getetag>&#34;bad&#34;</D:getetag></a>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(sanitizeETags([]byte(tt.in))); got != tt.want {
				t.Errorf("sanitizeETags(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}
