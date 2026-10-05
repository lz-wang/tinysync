package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// resumeFixture 装配 Range-capable 的 fake 文件服务与生产 Client；
// Stat 指纹经 etagStatRemote 补全内容哈希 ETag（JSON listing 无 ETag
// 字段，契约验证的是快照持有 strong ETag 的服务形态，见
// etagStatRemote 注释）。
func resumeFixture(t *testing.T, mutate func(*fakeCaddyServer)) (etagStatRemote, *fakeCaddyServer) {
	t.Helper()
	f := newFakeCaddyServer(t)
	if mutate != nil {
		mutate(f)
	}
	c := newClient(source.HTTPConfig{BaseURL: f.srv.URL + "/"}, newRequester(mustMapper(t, f.srv.URL+"/"), authConfig{}))
	return etagStatRemote{Remote: c, f: f}, f
}

// mustMapper 构造 URL mapper（测试基座）。
func mustMapper(t *testing.T, base string) *mapper {
	t.Helper()
	m, err := newMapper(base)
	if err != nil {
		t.Fatalf("newMapper: %v", err)
	}
	return m
}

// 合规 206：OpenFrom 返回请求 offset 起的后缀流。
func TestHTTPOpenFromRanged(t *testing.T) {
	c, f := resumeFixture(t, nil)
	content := strings.Repeat("0123456789", 10) // 100 bytes
	f.mu.Lock()
	f.files["/resume.bin"] = content
	f.mu.Unlock()

	fi, err := c.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	rc, err := c.OpenFrom(context.Background(), "/resume.bin", 30, fi.Fingerprint)
	if err != nil {
		t.Fatalf("OpenFrom(30): %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != content[30:] {
		t.Fatalf("suffix = %d bytes, want %d", len(got), len(content)-30)
	}
}

// offset=0 走无 Range 的 Open 路径。
func TestHTTPOpenFromZeroOffset(t *testing.T) {
	c, f := resumeFixture(t, nil)
	content := strings.Repeat("a", 40)
	f.mu.Lock()
	f.files["/resume.bin"] = content
	f.mu.Unlock()
	fi, err := c.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	rc, err := c.OpenFrom(context.Background(), "/resume.bin", 0, fi.Fingerprint)
	if err != nil {
		t.Fatalf("OpenFrom(0): %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != content {
		t.Fatalf("content = %d bytes, want full", len(got))
	}
}

// 服务器忽略 Range（200 全量）：指纹未变 → ErrResumeUnsupported
// 降级完整下载，绝不把 200 body 当 offset 流。用裸 Client 的 listing
// 指纹（无 ETag）：JSON listing 形态的快照没有 strong validator，
// OpenFrom 在客户端直接拒绝——200 全量的 body 同样到不了 Downloader。
// 「快照带 ETag + 服务器忽略 Range」的完整 Stat 复核路径由 WebDAV
// 套件覆盖（PROPFIND 与 GET 的 ETag 同源，装置无需模拟）。
func TestHTTPOpenFromRangeIgnored(t *testing.T) {
	c, f := resumeFixture(t, func(f *fakeCaddyServer) { f.ignoreRange = true })
	raw := c.Remote.(*Client)
	f.mu.Lock()
	f.files["/resume.bin"] = strings.Repeat("b", 80)
	f.mu.Unlock()
	fi, err := raw.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	_, err = raw.OpenFrom(context.Background(), "/resume.bin", 10, fi.Fingerprint)
	if !errors.Is(err, source.ErrResumeUnsupported) {
		t.Fatalf("OpenFrom with ignored Range = %v, want ErrResumeUnsupported", err)
	}
}

// 200 且指纹漂移（If-Range 未命中）：Stat 复核确认变化 →
// ErrRemoteChanged。
func TestHTTPOpenFromChangedOnFullResponse(t *testing.T) {
	c, f := resumeFixture(t, func(f *fakeCaddyServer) { f.ignoreRange = true })
	f.mu.Lock()
	f.files["/resume.bin"] = strings.Repeat("c", 80)
	f.mu.Unlock()
	fi, err := c.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// 远端变化：长度不同。
	f.mu.Lock()
	f.files["/resume.bin"] = strings.Repeat("c", 40)
	f.mod["/resume.bin"] = time.Now().Add(time.Hour)
	f.mu.Unlock()
	_, err = c.OpenFrom(context.Background(), "/resume.bin", 10, fi.Fingerprint)
	if !errors.Is(err, source.ErrRemoteChanged) {
		t.Fatalf("OpenFrom after remote change = %v, want ErrRemoteChanged", err)
	}
}

// 416（offset 越过当前资源末尾）：远端缩小 → ErrRemoteChanged。
func TestHTTPOpenFromRangeNotSatisfiable(t *testing.T) {
	c, f := resumeFixture(t, nil)
	f.mu.Lock()
	f.files["/resume.bin"] = strings.Repeat("d", 100)
	f.mu.Unlock()
	fi, err := c.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	_, err = c.OpenFrom(context.Background(), "/resume.bin", 150, fi.Fingerprint)
	if !errors.Is(err, source.ErrRemoteChanged) {
		t.Fatalf("OpenFrom(150) on 100-byte object = %v, want ErrRemoteChanged", err)
	}
}

// 入口校验：负 offset、根目录、非法路径。
func TestHTTPOpenFromInvalidInput(t *testing.T) {
	c, _ := resumeFixture(t, nil)
	if _, err := c.OpenFrom(context.Background(), "/resume.bin", -1, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
		t.Fatalf("OpenFrom(-1) = %v, want ErrInvalid", err)
	}
	if _, err := c.OpenFrom(context.Background(), "/", 0, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
		t.Fatalf("OpenFrom(/) = %v, want ErrInvalid", err)
	}
	if _, err := c.OpenFrom(context.Background(), "relative", 0, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
		t.Fatalf("OpenFrom(relative) = %v, want ErrInvalid", err)
	}
}

// http.ServeContent 的 206 校验路径已由 contract 套件
// （TestRemoteResumeContractSuite）覆盖；此处补充错误分类：Range 请求
// 的 404 仍是 permanent。
func TestHTTPOpenFromNotFound(t *testing.T) {
	c, _ := resumeFixture(t, nil)
	_, err := c.OpenFrom(context.Background(), "/missing.bin", 5, source.Fingerprint{Size: 10, ETag: `"x"`})
	if err == nil || source.IsRetryable(err) {
		t.Fatalf("OpenFrom missing = %v, want permanent error", err)
	}
	var respErr interface{ HTTPStatusCode() int }
	_ = respErr
	_ = http.StatusNotFound
}

// 只有 Last-Modified 的快照（无 strong ETag）在同一秒发生 same-size
// 替换：HTTP-date 是 RFC 9110 weak validator，秒精度内两次写入无法
// 区分——绝不凭日期续传，拒绝续传降级完整下载（ADR 0010 fail-closed）。
func TestHTTPOpenFromRefusesLastModifiedOnlySameSecondReplacement(t *testing.T) {
	c, f := resumeFixture(t, nil)
	contentA := strings.Repeat("A", 100)
	contentB := strings.Repeat("B", 100) // 同长度替换内容
	mod := time.Unix(1758900000, 0).UTC()
	f.mu.Lock()
	f.files["/resume.bin"] = contentA
	f.mod["/resume.bin"] = mod
	f.mu.Unlock()
	// 快照只含 Size + Last-Modified：与替换后的服务器状态在 Size /
	// mtime 上完全一致（同一秒 same-size 替换前后不可区分）。
	fp := source.Fingerprint{Size: int64(len(contentA)), ModifiedAt: mod}
	f.mu.Lock()
	f.files["/resume.bin"] = contentB
	f.mu.Unlock()
	fi, err := c.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Fingerprint.Size != fp.Size || !fi.Fingerprint.ModifiedAt.Equal(fp.ModifiedAt) {
		t.Fatalf("precondition: visible identity drifted (size %d mod %v), want identical",
			fi.Fingerprint.Size, fi.Fingerprint.ModifiedAt)
	}
	_, err = c.OpenFrom(context.Background(), "/resume.bin", 40, fp)
	if !errors.Is(err, source.ErrResumeUnsupported) {
		t.Fatalf("OpenFrom with Last-Modified-only fingerprint = %v, want ErrResumeUnsupported", err)
	}
}
