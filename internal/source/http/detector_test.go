package http

import "testing"

// 自动识别按响应形态（不依赖 Server header）：JSON 字段集区分
// nginx / caddy，HTML 结构标记区分 miniserve / nginx，普通网站与
// index.html 明确 unknown。
func TestDetectListing(t *testing.T) {
	cases := []struct {
		name string
		body string
		want listingKind
	}{
		{"nginx json", `[{"name":"d/","type":"directory","mtime":"Wed, 21 Oct 2026 07:28:00 GMT"},{"name":"f.tar.gz","type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT","size":123}]`, listingNginxJSON},
		{"nginx json empty dir", `[]`, listingNginxJSON},
		{"caddy json", `[{"name":"d","size":4096,"url":"d/","mod_time":"2026-10-01T12:00:00Z","mode":2147484141,"is_dir":true,"is_symlink":false}]`, listingCaddyJSON},
		{"caddy json wrapped", `{"items":[{"name":"d","size":4096,"url":"d/","mod_time":"2026-10-01T12:00:00Z","is_dir":true,"is_symlink":false}]}`, listingCaddyJSON},
		{"caddy json empty wrapped", `{"items":[]}`, listingCaddyJSON},
		{"miniserve html", `<html><body><table><tr class="entry-type-file"><td><a class="file" href="a.txt">a</a></td></tr></table></body></html>`, listingMiniserveHTML},
		{"nginx html", `<html><head><title>Index of /releases/</title></head><body><h1>Index of /releases/</h1><hr><pre><a href="../">../</a>\n<a href="d/">d/</a></pre></body></html>`, listingNginxHTML},
		{"plain website", `<html><body><a href="/about">About</a><a href="/download">Download</a></body></html>`, listingUnknown},
		{"index html page", `<html><head><title>Welcome to nginx!</title></head><body><h1>Welcome!</h1></body></html>`, listingUnknown},
		{"malformed json", `{not json`, listingUnknown},
		{"empty body", ``, listingUnknown},
	}
	for _, tc := range cases {
		if got := detectListing([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: detectListing = %s, want %s", tc.name, got, tc.want)
		}
	}
}
