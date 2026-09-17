package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"tinysync/internal/source"
)

// fakeS3 是 API 的内存实现：对象按 key 存储，List 按公共前缀模拟
// Delimiter 语义与分页。
type fakeS3 struct {
	objects map[string]fakeObject
	// pageSize 控制 List 每页返回的条目数（0 = 不分页），验证
	// ContinuationToken 循环。
	pageSize int
	listErr  error
}

type fakeObject struct {
	data         []byte
	lastModified time.Time
	etag         string
}

func newFakeS3(pageSize int) *fakeS3 {
	return &fakeS3{objects: make(map[string]fakeObject), pageSize: pageSize}
}

func (f *fakeS3) put(key string, data []byte, mod time.Time, etag string) {
	if etag == "" {
		etag = `"` + key + `"`
	}
	f.objects[key] = fakeObject{data: data, lastModified: mod, etag: etag}
}

func (f *fakeS3) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	prefix := derefStr(params.Prefix)
	delim := derefStr(params.Delimiter)

	type entry struct {
		key  string
		obj  *fakeObject
		size int64
	}
	var files []entry
	var markers []string
	dirs := map[string]bool{}
	for key, obj := range f.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(key, prefix)
		if delim != "" && strings.Contains(rest, delim) {
			// 落在子层：CommonPrefix 取第一个 delimiter 之前。
			idx := strings.Index(rest, delim)
			dirs[rest[:idx+len(delim)]] = true
			continue
		}
		if strings.HasSuffix(key, "/") {
			markers = append(markers, key)
			continue
		}
		files = append(files, entry{key: key, size: int64(len(obj.data))})
	}
	// marker 对象本身也匹配 prefix（作为独立条目进入 Contents）。
	_ = markers

	// 与真实 S3 一致：keys 与 common prefixes 统一按字典序分页，
	// MaxKeys 约束两者之和。
	type item struct {
		key   string
		isDir bool
	}
	items := make([]item, 0, len(files)+len(dirs))
	for _, e := range files {
		items = append(items, item{key: e.key})
	}
	for d := range dirs {
		items = append(items, item{key: prefix + d, isDir: true})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })

	token := derefStr(params.ContinuationToken)
	start := 0
	if token != "" {
		// 真实 S3 对非法 ContinuationToken 返回 InvalidArgument；
		// fake 复刻该行为：伪造 token 由服务端拒绝。
		if n, err := fmt.Sscanf(token, "page-%d", &start); err != nil || n != 1 {
			return nil, errors.New("InvalidArgument: The continuation token provided is incorrect")
		}
	}
	limit := f.pageSize
	if params.MaxKeys != nil && *params.MaxKeys > 0 && (limit <= 0 || int(*params.MaxKeys) < limit) {
		limit = int(*params.MaxKeys)
	}
	if limit <= 0 {
		limit = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	pageItems := items[start:end]

	var contents []types.Object
	var common []types.CommonPrefix
	for _, it := range pageItems {
		if it.isDir {
			p := it.key
			common = append(common, types.CommonPrefix{Prefix: &p})
			continue
		}
		key := it.key
		size := int64(len(f.objects[key].data))
		obj := f.objects[key]
		etag := obj.etag
		mod := obj.lastModified
		contents = append(contents, types.Object{
			Key:          &key,
			Size:         &size,
			ETag:         &etag,
			LastModified: &mod,
		})
	}

	out := &s3.ListObjectsV2Output{
		Contents:       contents,
		CommonPrefixes: common,
		KeyCount:       aws.Int32(int32(len(pageItems))),
	}
	if end < len(items) {
		truncated := true
		out.IsTruncated = &truncated
		next := fmt.Sprintf("page-%d", end)
		out.NextContinuationToken = &next
	}
	return out, nil
}

func (f *fakeS3) HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	obj, ok := f.objects[derefStr(params.Key)]
	if !ok {
		return nil, &notFoundErr{}
	}
	etag := obj.etag
	mod := obj.lastModified
	size := int64(len(obj.data))
	return &s3.HeadObjectOutput{
		ContentLength: &size,
		LastModified:  &mod,
		ETag:          &etag,
	}, nil
}

func (f *fakeS3) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	obj, ok := f.objects[derefStr(params.Key)]
	if !ok {
		return nil, &notFoundErr{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(string(obj.data)))}, nil
}

// notFoundErr 模拟 SDK 的 NoSuchKey / HTTP 404。
type notFoundErr struct{}

func (e *notFoundErr) Error() string { return "NoSuchKey" }

func (e *notFoundErr) ErrorCode() string { return "NoSuchKey" }

func (e *notFoundErr) HTTPStatusCode() int { return 404 }

// newTestRemote 构造带 prefix 的测试 Remote。
func newTestRemote(client API, prefix string) source.Remote {
	return NewRemoteWithAPI(client, "backup", prefix)
}

// List 按 prefix/Delimiter 合成一层文件与目录；marker 是目录不是文件。
func TestS3List(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("homes/lzwang/docs/a.pdf", []byte("pdf-data"), mod, "")
	fk.put("homes/lzwang/photo.jpg", []byte("jpeg-data"), mod, `"etag-x"`)
	fk.put("homes/lzwang/empty-folder/", nil, mod, "")
	fk.put("outside/secret.txt", []byte("no"), mod, "")

	r := newTestRemote(fk, "homes/lzwang")
	page, err := r.List(context.Background(), "/", source.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]source.FileInfo{}
	for _, e := range page.Entries {
		got[e.Path] = e
	}
	if len(got) != 3 {
		t.Fatalf("entries = %v, want 3 (docs dir, photo.jpg, empty-folder)", got)
	}
	docs := got["/docs"]
	if !docs.IsDir {
		t.Errorf("/docs = %+v, want directory (via CommonPrefix)", docs)
	}
	photo := got["/photo.jpg"]
	if photo.IsDir || photo.Fingerprint.Size != 9 {
		t.Errorf("/photo.jpg = %+v, want file with size 9", photo)
	}
	if photo.Fingerprint.ETag != `"etag-x"` {
		t.Errorf("etag = %q, want opaque echo", photo.Fingerprint.ETag)
	}
	folder := got["/empty-folder"]
	if !folder.IsDir {
		t.Errorf("/empty-folder = %+v, want directory (marker, not zero-byte file)", folder)
	}
	if _, has := got["/outside/secret.txt"]; has {
		t.Error("entry outside prefix leaked")
	}
}

// List 分页：opts.Limit 映射 MaxKeys、NextCursor 映射
// ContinuationToken，逐页拉取无重复无缺失，EOF 以空 NextCursor
// 表达；非法 cursor 整体失败。
func TestS3ListPagination(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(2) // 每页最多 2 条
	want := 5
	for i := 0; i < want; i++ {
		fk.put(fmt.Sprintf("base/file-%02d.txt", i), []byte{byte(i)}, mod, "")
	}
	r := newTestRemote(fk, "base")
	ctx := context.Background()

	var seen []string
	cursors := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page, err := r.List(ctx, "/", source.ListOptions{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("List page %d: %v", pages, err)
		}
		if len(page.Entries) > 2 {
			t.Fatalf("page %d has %d entries, want <= limit 2", pages, len(page.Entries))
		}
		for _, e := range page.Entries {
			seen = append(seen, e.Path)
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		if cursors[page.NextCursor] {
			t.Fatalf("cursor %q repeated; pagination is not progressing", page.NextCursor)
		}
		cursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	if pages < 3 {
		t.Fatalf("pages = %d, want >= 3 with limit 2 over %d files", pages, want)
	}
	if len(seen) != want {
		t.Fatalf("entries = %d, want %d", len(seen), want)
	}
	uniq := map[string]bool{}
	for _, p := range seen {
		if uniq[p] {
			t.Errorf("duplicated entry %s across pages", p)
		}
		uniq[p] = true
	}
	for i := 0; i < want; i++ {
		p := fmt.Sprintf("/file-%02d.txt", i)
		if !uniq[p] {
			t.Errorf("missing %s in paginated result", p)
		}
	}

	// 空目录：EOF 立即到达，Entries 空且无 cursor。
	fk2 := newFakeS3(0)
	r2 := newTestRemote(fk2, "empty")
	empty, err := r2.List(ctx, "/", source.ListOptions{})
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(empty.Entries) != 0 || empty.NextCursor != "" {
		t.Errorf("empty page = %+v, want no entries and no cursor", empty)
	}

	// 非法 cursor：opaque 直传服务端，由服务端拒绝——adapter 不本地
	// 校验 ContinuationToken，也不静默从头开始。
	if _, err := r.List(ctx, "/", source.ListOptions{Cursor: "not-a-real-cursor"}); err == nil {
		t.Error("List with malformed cursor = nil, want server-side rejection")
	}
}

// Stat：root 探测、文件 Head、目录前缀探测三态。
func TestS3Stat(t *testing.T) {
	mod := time.Unix(1757879400, 0).UTC()
	fk := newFakeS3(0)
	fk.put("base/docs/a.pdf", []byte("pdf"), mod, "")
	fk.put("base/lonely-dir/deep.txt", []byte("x"), mod, "")

	r := newTestRemote(fk, "base")
	ctx := context.Background()

	root, err := r.Stat(ctx, "/")
	if err != nil || !root.IsDir || root.Path != "/" {
		t.Fatalf("Stat / = %+v, %v; want dir", root, err)
	}

	file, err := r.Stat(ctx, "/docs/a.pdf")
	if err != nil || file.IsDir {
		t.Fatalf("Stat file = %+v, %v; want file", file, err)
	}
	if file.Fingerprint.Size != 3 || !file.Fingerprint.ModifiedAt.Equal(mod) {
		t.Errorf("fingerprint = %+v, want size 3 and mtime", file.Fingerprint)
	}

	// 无 marker 的目录：Head 404 后经前缀探测命中。
	dir, err := r.Stat(ctx, "/lonely-dir")
	if err != nil || !dir.IsDir {
		t.Fatalf("Stat marker-less dir = %+v, %v; want dir", dir, err)
	}

	if _, err := r.Stat(ctx, "/missing"); err == nil {
		t.Error("Stat missing = nil, want error")
	}
}

// Open 返回文件内容；路径映射带 prefix。
func TestS3Open(t *testing.T) {
	fk := newFakeS3(0)
	fk.put("base/docs/data.bin", []byte("hello-s3"), time.Unix(1757879400, 0).UTC(), "")
	r := newTestRemote(fk, "base")

	rc, err := r.Open(context.Background(), "/docs/data.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello-s3" {
		t.Errorf("content = %q, want hello-s3", data)
	}

	if _, err := r.Open(context.Background(), "/docs/missing.bin"); err == nil {
		t.Error("Open missing = nil, want error")
	}
}

// toLogical 的映射与越界防护。
func TestS3ToLogical(t *testing.T) {
	r := &remote{prefix: normalizePrefix("homes/lzwang")}
	cases := []struct {
		key    string
		want   string
		wantEr bool
	}{
		{key: "homes/lzwang/a.txt", want: "/a.txt"},
		{key: "homes/lzwang/x/y", want: "/x/y"},
		{key: "homes/lzwang/", want: "/"},
		{key: "outside/a.txt", wantEr: true},
		{key: `homes/lzwang/bad\name`, wantEr: true},
	}
	for _, tc := range cases {
		got, err := r.toLogical(tc.key)
		if tc.wantEr {
			if err == nil {
				t.Errorf("toLogical(%q) = %q, want error", tc.key, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("toLogical(%q) = %q, %v; want %q", tc.key, got, err, tc.want)
		}
	}
}

// context 取消立即生效。
func TestS3ContextCancellation(t *testing.T) {
	fk := newFakeS3(0)
	r := newTestRemote(fk, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.List(ctx, "/", source.ListOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("List canceled = %v, want context.Canceled", err)
	}
}
