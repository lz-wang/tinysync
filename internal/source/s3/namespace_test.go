package s3

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"tinysync/internal/source"
)

// file/dir collision：同一 logical path 既是文件又是目录时整个 List
// 失败——本地 filesystem 无法无损表达该 S3 namespace，Mirror 在完整
// 扫描失败时不会删除，fail whole scan 安全。
func TestS3FileDirCollisionFailsWholeList(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	// 文件 a 与目录 a/b.txt 并存。
	fk.put("base/a", []byte("file"), mod, "")
	fk.put("base/a/b.txt", []byte("nested"), mod, "")

	r := newTestRemote(fk, "base")
	entries, err := r.List(context.Background(), "/")
	if err == nil {
		t.Fatalf("List collision = %v entries, want whole-list error", entries)
	}
	if !errors.Is(err, source.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "both a file and a directory") {
		t.Errorf("error = %v, want collision description", err)
	}
}

// 空 bucket 与空 prefix：List("/") 返回空集（不是错误），root Stat
// 依然可用。
func TestS3EmptyBucketAndPrefix(t *testing.T) {
	fk := newFakeS3(0)
	r := newTestRemote(fk, "")
	ctx := context.Background()

	entries, err := r.List(ctx, "/")
	if err != nil {
		t.Fatalf("List empty bucket: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want empty", entries)
	}
	root, err := r.Stat(ctx, "/")
	if err != nil || !root.IsDir {
		t.Fatalf("Stat / on empty bucket = %+v, %v; want dir", root, err)
	}
}

// Fingerprint 语义：ETag 是 opaque token（multipart 形如 "hex-2"
// 不得被解析或改写）；size 与 mtime 原样保留；Checksum / Version 留空。
func TestS3FingerprintSemantics(t *testing.T) {
	mod := time.Unix(1750000000, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/multi.bin", []byte("multipart-ish"), mod, `"d41d8cd98f00b204e9800998ecf8427e-3"`)

	r := newTestRemote(fk, "base")
	fi, err := r.Stat(context.Background(), "/multi.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Fingerprint.ETag != `"d41d8cd98f00b204e9800998ecf8427e-3"` {
		t.Errorf("ETag = %q, want verbatim multipart etag (opaque)", fi.Fingerprint.ETag)
	}
	if fi.Fingerprint.Size != int64(len("multipart-ish")) {
		t.Errorf("Size = %d, want object size", fi.Fingerprint.Size)
	}
	if !fi.Fingerprint.ModifiedAt.Equal(mod) {
		t.Errorf("ModifiedAt = %v, want %v", fi.Fingerprint.ModifiedAt, mod)
	}
	if fi.Fingerprint.Checksum != "" || fi.Fingerprint.Version != "" {
		t.Errorf("Checksum/Version = %q/%q, want empty (no per-object extra requests)",
			fi.Fingerprint.Checksum, fi.Fingerprint.Version)
	}
}

// 子目录 List 只返回该层内容：嵌套文件以一层目录条目呈现，不递归。
func TestS3NestedListStaysOnOneLevel(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/docs/2026/report.txt", []byte("x"), mod, "")
	fk.put("base/docs/summary.md", []byte("y"), mod, "")

	r := newTestRemote(fk, "base")
	entries, err := r.List(context.Background(), "/docs")
	if err != nil {
		t.Fatalf("List /docs: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Path+dirSuffix(e)] = true
	}
	if !got["/docs/summary.mdfalse"] {
		t.Errorf("entries = %v, want /docs/summary.md file", got)
	}
	if !got["/docs/2026true"] {
		t.Errorf("entries = %v, want /docs/2026 directory entry", got)
	}
	if got["/docs/2026/report.txtfalse"] {
		t.Errorf("entries = %v, nested file must not appear directly", got)
	}
}

func dirSuffix(fi source.FileInfo) string {
	if fi.IsDir {
		return "true"
	}
	return "false"
}

// 错误传播：List 的底层错误保留且操作上下文明确；ctx 超时经
// errors.Is 可判定。
func TestS3ErrorPropagation(t *testing.T) {
	fk := newFakeS3(0)
	fk.listErr = errors.New("connection reset by peer")
	r := newTestRemote(fk, "")

	_, err := r.List(context.Background(), "/")
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("List error = %v, want underlying cause preserved", err)
	}
	if !strings.Contains(err.Error(), "s3 list /") {
		t.Errorf("error = %v, want operation context", err)
	}

	deadline := context.Background()
	ctx, cancel := context.WithTimeout(deadline, 20*time.Millisecond)
	defer cancel()
	slow := newFakeS3(0)
	slow.listErr = context.DeadlineExceeded
	r2 := newTestRemote(slow, "")
	_, err = r2.List(ctx, "/")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("List deadline = %v, want DeadlineExceeded", err)
	}
}

// Open 读取与 body 关闭语义：body 是原始 ReadCloser。
func TestS3OpenBody(t *testing.T) {
	fk := newFakeS3(0)
	fk.put("x", []byte("abc"), time.Unix(0, 0).UTC(), "")
	r := newTestRemote(fk, "")

	rc, err := r.Open(context.Background(), "/x")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	n, err := io.ReadFull(rc, make([]byte, 3))
	if err != nil || n != 3 {
		t.Fatalf("read = %d, %v", n, err)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
}

// normalizePrefix：前导 / 去除、尾随 / 补齐、空串保持。
func TestS3NormalizePrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"base", "base/"},
		{"base/", "base/"},
		{"/base", "base/"},
		{"/base/", "base/"},
		{"a/b", "a/b/"},
	}
	for _, tc := range cases {
		if got := normalizePrefix(tc.in); got != tc.want {
			t.Errorf("normalizePrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
