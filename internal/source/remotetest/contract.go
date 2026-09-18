// Package remotetest 提供 source.Remote 的统一契约测试套件：
// WebDAV / S3 / SFTP 三个 read-only adapter 用同一套断言验证协议
// 无关语义——Stat、List 与分页、Open（含空文件与大文件）、缺失、
// 非法 logical path、context 取消、特殊文件名。协议特定行为
// （host key、folder marker 等）由各 adapter 自身的测试覆盖。
//
// 套件只依赖 Harness 抽象：每个协议提供「构造全新远端 + 写入测试
// 数据」的装置实现（测试装置直接操作底层存储，不经 Remote 契约，
// 避免用被测代码造数据）。
package remotetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"tinysync/internal/source"
)

// Harness 是被测协议的装配与数据写入抽象。
type Harness interface {
	// NewRemote 构造全新（空）远端存储上的 Remote；后续 Write /
	// Mkdir 作用于该存储。返回的 Remote 由套件负责 Close。
	NewRemote(t *testing.T) source.Remote
	// Write 写入（或覆盖）logical 路径的文件内容；父目录自动创建。
	Write(t *testing.T, logical string, content string)
	// Mkdir 创建目录。
	Mkdir(t *testing.T, logical string)
}

// largeFileSize 是 OpenLarge 契约的大文件尺寸：足以穿越多轮缓冲
// 复制与分块读取，又保持 make check 预算内的速度。
const largeFileSize = 1 << 20 // 1 MiB

// largeFileContent 生成确定性伪随机内容（可重复比较）。
func largeFileContent() []byte {
	buf := make([]byte, largeFileSize)
	x := uint32(0x9e3779b9)
	for i := range buf {
		x = x*1664525 + 1013904223
		buf[i] = byte(x >> 24)
	}
	return buf
}

// RunSuite 对 harness 装配的 adapter 执行全部契约断言。
func RunSuite(t *testing.T, h Harness) {
	t.Run("StatRoot", withRemote(t, h, func(t *testing.T, r source.Remote) {
		fi, err := r.Stat(context.Background(), "/")
		if err != nil {
			t.Fatalf("Stat root: %v", err)
		}
		if !fi.IsDir {
			t.Errorf("root IsDir = false, want true")
		}
		if fi.Path != "/" {
			t.Errorf("root Path = %q, want /", fi.Path)
		}
	}))

	t.Run("StatFile", withRemote(t, h, func(t *testing.T, r source.Remote) {
		fi, err := r.Stat(context.Background(), "/hello.txt")
		if err != nil {
			t.Fatalf("Stat file: %v", err)
		}
		if fi.IsDir {
			t.Error("file IsDir = true, want false")
		}
		if fi.Path != "/hello.txt" {
			t.Errorf("Path = %q, want /hello.txt", fi.Path)
		}
		if fi.Fingerprint.Size != int64(len("hello contract")) {
			t.Errorf("Size = %d, want %d", fi.Fingerprint.Size, len("hello contract"))
		}
	}))

	t.Run("StatMissing", withRemote(t, h, func(t *testing.T, r source.Remote) {
		if _, err := r.Stat(context.Background(), "/missing.txt"); err == nil {
			t.Fatal("Stat missing = nil, want error")
		}
	}))

	t.Run("StatInvalidLogicalPath", withRemote(t, h, func(t *testing.T, r source.Remote) {
		for _, p := range []string{"not-absolute", "/a/../b", "a\\b", "/a//b", "/a/", ""} {
			if _, err := r.Stat(context.Background(), p); err == nil {
				t.Errorf("Stat %q = nil, want ErrInvalid", p)
			} else if !errors.Is(err, source.ErrInvalid) {
				t.Errorf("Stat %q error = %v, want ErrInvalid", p, err)
			}
		}
	}))

	t.Run("ListRoot", withRemote(t, h, func(t *testing.T, r source.Remote) {
		page, err := r.List(context.Background(), "/", source.ListOptions{Limit: source.MaxListLimit})
		if err != nil {
			t.Fatalf("List root: %v", err)
		}
		if page.NextCursor != "" {
			t.Errorf("full page returned cursor %q, want EOF", page.NextCursor)
		}
		want := map[string]bool{
			"/hello.txt": false, "/empty.txt": false, "/big.bin": false, "/dir": false, "/special 名字 +1.txt": false,
		}
		for _, fi := range page.Entries {
			if _, ok := want[fi.Path]; !ok {
				t.Errorf("unexpected entry %q", fi.Path)
				continue
			}
			want[fi.Path] = true
		}
		for p, seen := range want {
			if !seen {
				t.Errorf("entry %q missing from listing", p)
			}
		}
		// 类型契约：文件 / 目录区分正确。
		for _, fi := range page.Entries {
			switch fi.Path {
			case "/dir":
				if !fi.IsDir {
					t.Error("/dir IsDir = false, want true")
				}
			case "/hello.txt", "/empty.txt", "/big.bin":
				if fi.IsDir {
					t.Errorf("%s IsDir = true, want false", fi.Path)
				}
			}
		}
	}))

	t.Run("ListSubdir", withRemote(t, h, func(t *testing.T, r source.Remote) {
		page, err := r.List(context.Background(), "/dir", source.ListOptions{})
		if err != nil {
			t.Fatalf("List /dir: %v", err)
		}
		if len(page.Entries) != 1 || page.Entries[0].Path != "/dir/inner.txt" {
			t.Errorf("subdir entries = %+v, want [/dir/inner.txt]", page.Entries)
		}
	}))

	t.Run("ListPaginationFollow", withRemote(t, h, func(t *testing.T, r source.Remote) {
		// 以小页逐页跟随 cursor 到 EOF：不重复、不丢失、终止。
		// 页大小取 2：足够多页覆盖游标流转，同时避开参考 S3 服务
		//（MinIO）在 MaxKeys=1 + Delimiter rollup 下的续页丢失缺陷
		//（见 s3 adapter 的 List 注释）。
		seen := map[string]int{}
		cursor := ""
		pages := 0
		for {
			page, err := r.List(context.Background(), "/", source.ListOptions{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatalf("List page %d: %v", pages, err)
			}
			pages++
			for _, fi := range page.Entries {
				seen[fi.Path]++
			}
			if page.NextCursor == "" {
				break
			}
			if pages > 16 {
				t.Fatal("pagination did not terminate")
			}
			cursor = page.NextCursor
		}
		for path, n := range seen {
			if n != 1 {
				t.Errorf("entry %q seen %d times, want 1", path, n)
			}
		}
		if len(seen) != 5 {
			t.Errorf("saw %d entries, want 5", len(seen))
		}
	}))

	t.Run("ListInvalidLogicalPath", withRemote(t, h, func(t *testing.T, r source.Remote) {
		for _, p := range []string{"not-absolute", "/a/../b"} {
			if _, err := r.List(context.Background(), p, source.ListOptions{}); !errors.Is(err, source.ErrInvalid) {
				t.Errorf("List %q error = %v, want ErrInvalid", p, err)
			}
		}
	}))

	t.Run("OpenRoundtrip", withRemote(t, h, func(t *testing.T, r source.Remote) {
		readContractFile(t, r, "/hello.txt", "hello contract")
	}))

	t.Run("OpenEmptyFile", withRemote(t, h, func(t *testing.T, r source.Remote) {
		readContractFile(t, r, "/empty.txt", "")
	}))

	t.Run("OpenLargeFile", withRemote(t, h, func(t *testing.T, r source.Remote) {
		want := largeFileContent()
		rc, err := r.Open(context.Background(), "/big.bin")
		if err != nil {
			t.Fatalf("Open big.bin: %v", err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read big.bin: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("big.bin content mismatch: got %d bytes, want %d", len(got), len(want))
		}
	}))

	t.Run("OpenMissing", withRemote(t, h, func(t *testing.T, r source.Remote) {
		if _, err := r.Open(context.Background(), "/missing.txt"); err == nil {
			t.Fatal("Open missing = nil, want error")
		}
	}))

	t.Run("OpenInvalidLogicalPath", withRemote(t, h, func(t *testing.T, r source.Remote) {
		if _, err := r.Open(context.Background(), "a\\b"); !errors.Is(err, source.ErrInvalid) {
			t.Errorf("Open invalid path error = %v, want ErrInvalid", err)
		}
	}))

	t.Run("SpecialFilename", withRemote(t, h, func(t *testing.T, r source.Remote) {
		// 特殊文件名（空格、+、非 ASCII）经 Stat / List / Open 全链路
		// 保持字节级一致。
		name := "/special 名字 +1.txt"
		fi, err := r.Stat(context.Background(), name)
		if err != nil {
			t.Fatalf("Stat special: %v", err)
		}
		if fi.Fingerprint.Size != int64(len("special")) {
			t.Errorf("special Size = %d, want %d", fi.Fingerprint.Size, len("special"))
		}
		page, err := r.List(context.Background(), "/", source.ListOptions{Limit: source.MaxListLimit})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		found := false
		for _, entry := range page.Entries {
			if entry.Path == name {
				found = true
			}
		}
		if !found {
			t.Errorf("special name %q missing from listing", name)
		}
		readContractFile(t, r, name, "special")
	}))

	t.Run("ContextCancellation", withRemote(t, h, func(t *testing.T, r source.Remote) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.Stat(ctx, "/hello.txt"); !errors.Is(err, context.Canceled) {
			t.Errorf("Stat canceled = %v, want context.Canceled", err)
		}
		if _, err := r.List(ctx, "/", source.ListOptions{}); !errors.Is(err, context.Canceled) {
			t.Errorf("List canceled = %v, want context.Canceled", err)
		}
		if _, err := r.Open(ctx, "/hello.txt"); !errors.Is(err, context.Canceled) {
			t.Errorf("Open canceled = %v, want context.Canceled", err)
		}
	}))
}

// withRemote 构造带种子数据的全新 Remote 并执行断言，结束后关闭。
func withRemote(t *testing.T, h Harness, fn func(*testing.T, source.Remote)) func(*testing.T) {
	return func(t *testing.T) {
		r := h.NewRemote(t)
		defer func() { _ = r.Close() }()
		seed(t, h)
		fn(t, r)
	}
}

// seed 写入标准契约测试数据集（目录先于其子文件创建）。
func seed(t *testing.T, h Harness) {
	t.Helper()
	h.Mkdir(t, "/dir")
	h.Write(t, "/hello.txt", "hello contract")
	h.Write(t, "/empty.txt", "")
	h.Write(t, "/dir/inner.txt", "inner")
	h.Write(t, "/big.bin", string(largeFileContent()))
	h.Write(t, "/special 名字 +1.txt", "special")
}

// readContractFile 打开并读取远端文件，校验内容一致。
func readContractFile(t *testing.T, r source.Remote, path, want string) {
	t.Helper()
	rc, err := r.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open %s: %v", path, err)
	}
	defer rc.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := rc.Read(buf)
		sb.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
	}
	if sb.String() != want {
		t.Errorf("content of %s = %q, want %q", path, sb.String(), want)
	}
}
