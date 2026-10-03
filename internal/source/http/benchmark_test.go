package http

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// benchNameLen 控制条目名的确定性伪随机长度变化。
func benchEntryJSON(i int, caddy bool) string {
	name := fmt.Sprintf("entry-%06d.dat", i)
	if caddy {
		return fmt.Sprintf(`{"name":%q,"size":%d,"url":%q,"mod_time":"2026-10-01T12:00:00Z","mode":420,"is_dir":false,"is_symlink":false}`,
			name, i*997, name)
	}
	return fmt.Sprintf(`{"name":%q,"type":"file","mtime":"Wed, 21 Oct 2026 07:28:00 GMT","size":%d}`,
		name, i*997)
}

// nginxJSONBody 构造 N 条目的 nginx JSON listing。
func nginxJSONBody(n int) []byte {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(benchEntryJSON(i, false))
	}
	sb.WriteString("]")
	return []byte(sb.String())
}

// caddyJSONBody 构造 N 条目的 Caddy JSON listing。
func caddyJSONBody(n int) []byte {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(benchEntryJSON(i, true))
	}
	sb.WriteString("]")
	return []byte(sb.String())
}

// miniserveHTMLBody 构造 N 条目的 miniserve raw HTML 表。
func miniserveHTMLBody(n int) []byte {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"></head><body><table><tbody>`)
	for i := 0; i < n; i++ {
		sb.WriteString(fmt.Sprintf(`<tr class="entry-type-file"><td><a class="file" href="entry-%06d.dat">entry-%06d.dat</a></td><td>1 MB</td><td>2026-10-01</td></tr>`, i, i))
	}
	sb.WriteString(`</tbody></table></body></html>`)
	return []byte(sb.String())
}

// BenchmarkParseNginxJSON_10000：万条目 nginx JSON listing 解析。
func BenchmarkParseNginxJSON_10000(b *testing.B) {
	m := benchMapper(b)
	body := nginxJSONBody(10000)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseNginxJSON(m, "/", body); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseCaddyJSON_10000：万条目 Caddy JSON listing 解析。
func BenchmarkParseCaddyJSON_10000(b *testing.B) {
	m := benchMapper(b)
	body := caddyJSONBody(10000)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseCaddyJSON(m, "/", body); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseMiniserveHTML_10000：万条目 miniserve raw HTML 解析。
func BenchmarkParseMiniserveHTML_10000(b *testing.B) {
	m := benchMapper(b)
	body := miniserveHTMLBody(10000)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := parseMiniserveHTML(m, "/", body); err != nil {
			b.Fatal(err)
		}
	}
}

// benchMapper 构造 benchmark 用 mapper（失败即 Fatal）。
func benchMapper(tb testing.TB) *mapper {
	m, err := newMapper("https://mirror.example.com/bench/")
	if err != nil {
		tb.Fatalf("newMapper: %v", err)
	}
	return m
}

// seedBenchTree 播种 100 目录 × 10 文件 = 1000 文件 + 100 目录。
func seedBenchTree(f *fakeCaddyServer) {
	for d := 0; d < 100; d++ {
		dir := fmt.Sprintf("/dir-%03d", d)
		f.mkdir(dir)
		for i := 0; i < 10; i++ {
			f.write(fmt.Sprintf("%s/file-%03d.bin", dir, i), strings.Repeat("x", 128))
		}
	}
}

// BenchmarkHTTPScanTree：100 目录 / 1000 文件的全树扫描（JSON
// listing，无 HEAD 补全），每目录恰一次请求。
func BenchmarkHTTPScanTree(b *testing.B) {
	f := newFakeCaddyServer(b)
	seedBenchTree(f)
	remote, err := NewFactory().Create(context.Background(), source.Source{
		Name: "bench",
		Type: source.TypeHTTP,
		Config: source.Config{HTTP: &source.HTTPConfig{
			BaseURL:     f.srv.URL + "/",
			ListingMode: source.HTTPListingCaddy,
		}},
	}, source.Credentials{})
	if err != nil {
		b.Fatalf("Create: %v", err)
	}
	defer func() { _ = remote.Close() }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		visited := 0
		err := remote.(source.TreeScanner).ScanTree(context.Background(), "/", func(source.FileInfo) error {
			visited++
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
		if visited != 1100 {
			b.Fatalf("visited = %d, want 1100", visited)
		}
	}
}
