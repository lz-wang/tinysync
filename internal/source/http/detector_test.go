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
		{"ambiguous empty array", `[]`, listingEmptyJSON},
		{"whitespace empty array", " \n[ \t ]\n", listingEmptyJSON},
		{"null", `null`, listingUnknown},
		{"caddy json", `[{"name":"d","size":4096,"url":"d/","mod_time":"2026-10-01T12:00:00Z","mode":2147484141,"is_dir":true,"is_symlink":false}]`, listingCaddyJSON},
		{"caddy malformed second entry", `[{"name":"a","size":1,"url":"a","mod_time":"2026-10-01T12:00:00Z","is_dir":false,"is_symlink":false},{"name":"docs","size":4096,"mod_time":"2026-10-01T12:00:00Z"}]`, listingUnknown},
		{"mixed json profiles", `[{"name":"a","size":1,"url":"a","mod_time":"2026-10-01T12:00:00Z","is_dir":false,"is_symlink":false},{"name":"d/","type":"directory","mtime":"Wed, 21 Oct 2026 07:28:00 GMT"}]`, listingUnknown},
		{"malformed later nginx entry", `[{"name":"a","size":1,"type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT"},{"name":"d/","type":"directory"}]`, listingUnknown},
		// 顶层 JSON object 一律 unknown：任意 object 都能反序列化进
		// {items|files} 包装形态，字段缺失 ≠ 空数组——反向代理的
		// `200 {"status":"..."}` 绝不能被当成空 Caddy 目录。
		{"empty object", `{}`, listingUnknown},
		{"status object", `{"status":"ok"}`, listingUnknown},
		{"error object", `{"error":"backend temporarily unavailable"}`, listingUnknown},
		{"items null", `{"items":null}`, listingUnknown},
		{"files null", `{"files":null}`, listingUnknown},
		{"items not array", `{"items":"bad"}`, listingUnknown},
		{"wrapped caddy entries", `{"items":[{"name":"d","size":4096,"url":"d/","mod_time":"2026-10-01T12:00:00Z","is_dir":true,"is_symlink":false}]}`, listingUnknown},
		{"empty wrapped entries", `{"items":[]}`, listingUnknown},
		{"miniserve html", `<html><body><table><tr class="entry-type-file"><td><a class="file" href="a.txt">a</a></td></tr></table></body></html>`, listingMiniserveHTML},
		// 空目录 miniserve raw 页：完整表头签名（name + size + date
		// 三列，真实 miniserve 恒为此形态）。
		{"miniserve html empty dir", `<html><body><table><thead><th class="name">Name</th><th class="size">Size</th><th class="date">Last modification</th></thead><tbody></tbody></table></body></html>`, listingMiniserveHTML},
		// 不完整签名（只有 th.name）的普通页面不被误判成空目录。
		{"partial miniserve signature", `<html><body><table><thead><th class="name">Name</th></thead><tbody></tbody></table></body></html>`, listingUnknown},
		{"script miniserve marker", `<html><body><script>const className = "entry-type-file";</script></body></html>`, listingUnknown},
		{"comment miniserve markers", `<!-- <tr class="entry-type-directory"><a class="directory" href="d/">d</a></tr> -->`, listingUnknown},
		{"text miniserve marker", `<p>entry-type-file</p>`, listingUnknown},
		{"miniserve marker on div", `<div class="entry-type-file"><a class="file" href="a">a</a></div>`, listingUnknown},
		{"miniserve row without typed anchor", `<table><tr class="entry-type-file"><td><a href="a">a</a></td></tr></table>`, listingUnknown},
		{"miniserve row without href", `<table><tr class="entry-type-file"><td><a class="file">a</a></td></tr></table>`, listingUnknown},
		{"miniserve navigation without header", `<table><tr class="entry-type-directory"><td><a class="directory" href="../">Parent</a></td></tr></table>`, listingUnknown},
		{"miniserve substring class", `<table><tr class="not-entry-type-file"><td><a class="file" href="a">a</a></td></tr></table>`, listingUnknown},
		{"miniserve split headers", `<table><thead><tr><th class="name">Name</th></tr></thead><tbody></tbody></table><table><thead><tr><th class="size">Size</th><th class="date">Date</th></tr></thead><tbody></tbody></table>`, listingUnknown},
		{"miniserve headers outside thead", `<table><tbody><tr><th class="name">Name</th><th class="size">Size</th><th class="date">Date</th></tr></tbody></table>`, listingUnknown},
		{"miniserve DOM attribute variations", `<table><thead><tr><th data-label="Name" class="sortable name">Name</th><th class='size sortable'>Size</th><th class='date'>Date</th></tr></thead><tbody></tbody></table>`, listingMiniserveHTML},
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
