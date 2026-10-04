package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"tinysync/internal/source"
)

// preconditionFailedErr 模拟 If-Match 失败的 412 API 错误。
type preconditionFailedErr struct{}

func (e *preconditionFailedErr) Error() string { return "PreconditionFailed" }

func (e *preconditionFailedErr) ErrorCode() string { return "PreconditionFailed" }

func (e *preconditionFailedErr) HTTPStatusCode() int { return 412 }

// rangeFakeS3 在 fakeS3 之上实现 Range / If-Match 语义，并可注入
// S3-compatible 服务的异常响应形态（忽略 Range、起点错误、total
// 不符、412）。
type rangeFakeS3 struct {
	fakeS3
	mu       sync.Mutex
	ranges   []string // 依次记录每次 GetObject 的 Range 头
	ifMatch  []string // 依次记录每次 GetObject 的 If-Match 头
	behavior func(rangeHeader string, offset int64) rangeBehavior
}

// rangeBehavior 决定一次 Range 请求的响应形态。
type rangeBehavior struct {
	ignoreRange      bool // 无 Range 的 200 全量响应
	wrongStart       bool // Content-Range 起点与请求不符
	wrongTotal       bool // Content-Range total 与对象真实大小不符
	preconditionFail bool // 412
}

func (f *rangeFakeS3) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	f.mu.Lock()
	f.ranges = append(f.ranges, derefStr(params.Range))
	f.ifMatch = append(f.ifMatch, derefStr(params.IfMatch))
	behavior := f.behavior
	f.mu.Unlock()
	obj, ok := f.objects[derefStr(params.Key)]
	if !ok {
		return nil, &notFoundErr{}
	}
	data := string(obj.data)
	offset := int64(0)
	rangeHeader := derefStr(params.Range)
	if rangeHeader != "" {
		rest, ok := strings.CutPrefix(rangeHeader, "bytes=")
		if !ok {
			return nil, errors.New("malformed range header")
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(rest, "-"), 10, 64)
		if err != nil {
			return nil, err
		}
		offset = n
	}
	if behavior != nil {
		b := behavior(rangeHeader, offset)
		if b.preconditionFail {
			return nil, &preconditionFailedErr{}
		}
		if b.ignoreRange || rangeHeader == "" {
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(data))}, nil
		}
		start := offset
		if b.wrongStart {
			start = 0 // 服务器声称从 0 开始
		}
		total := int64(len(data))
		if b.wrongTotal {
			total = total + 4096
		}
		cr := fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, total)
		return &s3.GetObjectOutput{
			Body:         io.NopCloser(strings.NewReader(data[offset:])),
			ContentRange: &cr,
		}, nil
	}
	// 默认行为：合规 206。
	if rangeHeader == "" {
		return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(data))}, nil
	}
	cr := fmt.Sprintf("bytes %d-%d/%d", offset, len(data)-1, len(data))
	return &s3.GetObjectOutput{
		Body:         io.NopCloser(strings.NewReader(data[offset:])),
		ContentRange: &cr,
	}, nil
}

// newRangeRemote 构造挂在 rangeFakeS3 上的 ResumableRemote 与内容。
func newRangeRemote(t *testing.T, behavior func(string, int64) rangeBehavior) (source.ResumableRemote, string, *rangeFakeS3) {
	t.Helper()
	f := &rangeFakeS3{behavior: behavior}
	f.objects = map[string]fakeObject{}
	f.put("data.bin", []byte("0123456789abcdefghij"), time.Unix(1700000000, 0), `"etag-x"`)
	remote := NewRemoteWithAPI(f, "backup", "")
	r, ok := remote.(source.ResumableRemote)
	if !ok {
		t.Fatal("s3 remote does not implement ResumableRemote")
	}
	return r, "0123456789abcdefghij", f
}

func fp(size int64, etag string) source.Fingerprint {
	return source.Fingerprint{Size: size, ETag: etag}
}

// 合规 206：流从 offset 开始，Range / If-Match 请求头正确。
func TestS3OpenFromRanged(t *testing.T) {
	r, content, f := newRangeRemote(t, nil)
	rc, err := r.OpenFrom(context.Background(), "/data.bin", 6, fp(int64(len(content)), `"etag-x"`))
	if err != nil {
		t.Fatalf("OpenFrom(6): %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != content[6:] {
		t.Fatalf("content = %q, want suffix from 6", got)
	}
	if len(f.ranges) != 1 || f.ranges[0] != "bytes=6-" {
		t.Fatalf("Range headers = %v, want [bytes=6-]", f.ranges)
	}
	if len(f.ifMatch) != 1 || f.ifMatch[0] != `"etag-x"` {
		t.Fatalf("If-Match headers = %v, want etag", f.ifMatch)
	}
}

// offset=0 走无 Range 的完整 GET（起点天然 0，无需 206 证明）。
func TestS3OpenFromZeroOffsetSkipsRange(t *testing.T) {
	r, content, f := newRangeRemote(t, nil)
	rc, err := r.OpenFrom(context.Background(), "/data.bin", 0, fp(int64(len(content)), `"etag-x"`))
	if err != nil {
		t.Fatalf("OpenFrom(0): %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != content {
		t.Fatalf("content = %q, want full", got)
	}
	if len(f.ranges) != 1 || f.ranges[0] != "" {
		t.Fatalf("Range headers = %v, want one empty (no Range)", f.ranges)
	}
}

// 服务忽略 Range（200 全量、无 Content-Range）：无法证明起点，
// 保守降级 ErrResumeUnsupported。
func TestS3OpenFromRangeIgnored(t *testing.T) {
	r, content, _ := newRangeRemote(t, func(string, int64) rangeBehavior {
		return rangeBehavior{ignoreRange: true}
	})
	_, err := r.OpenFrom(context.Background(), "/data.bin", 6, fp(int64(len(content)), `"etag-x"`))
	if !errors.Is(err, source.ErrResumeUnsupported) {
		t.Fatalf("OpenFrom with ignored Range = %v, want ErrResumeUnsupported", err)
	}
}

// Content-Range 起点错误：服务器声称的区间与请求不符，身份不可信。
func TestS3OpenFromWrongStart(t *testing.T) {
	r, content, _ := newRangeRemote(t, func(string, int64) rangeBehavior {
		return rangeBehavior{wrongStart: true}
	})
	_, err := r.OpenFrom(context.Background(), "/data.bin", 6, fp(int64(len(content)), `"etag-x"`))
	if !errors.Is(err, source.ErrRemoteChanged) {
		t.Fatalf("OpenFrom with wrong start = %v, want ErrRemoteChanged", err)
	}
}

// Content-Range total 与快照 Size 不一致：对象已变化。
func TestS3OpenFromTotalMismatch(t *testing.T) {
	r, content, _ := newRangeRemote(t, func(string, int64) rangeBehavior {
		return rangeBehavior{wrongTotal: true}
	})
	_, err := r.OpenFrom(context.Background(), "/data.bin", 6, fp(int64(len(content)), `"etag-x"`))
	if !errors.Is(err, source.ErrRemoteChanged) {
		t.Fatalf("OpenFrom with total mismatch = %v, want ErrRemoteChanged", err)
	}
}

// If-Match 412：对象 ETag 与快照不一致。
func TestS3OpenFromPreconditionFailed(t *testing.T) {
	r, content, _ := newRangeRemote(t, func(string, int64) rangeBehavior {
		return rangeBehavior{preconditionFail: true}
	})
	_, err := r.OpenFrom(context.Background(), "/data.bin", 6, fp(int64(len(content)), `"etag-x"`))
	if !errors.Is(err, source.ErrRemoteChanged) {
		t.Fatalf("OpenFrom with 412 = %v, want ErrRemoteChanged", err)
	}
}

// 快照无 ETag：不发 If-Match，身份仅靠 Content-Range total 兜底。
func TestS3OpenFromWithoutETag(t *testing.T) {
	r, content, f := newRangeRemote(t, nil)
	rc, err := r.OpenFrom(context.Background(), "/data.bin", 4, fp(int64(len(content)), ""))
	if err != nil {
		t.Fatalf("OpenFrom(4): %v", err)
	}
	_ = rc.Close()
	if len(f.ifMatch) != 1 || f.ifMatch[0] != "" {
		t.Fatalf("If-Match headers = %v, want none", f.ifMatch)
	}
}

// 入口校验：负 offset 与非法路径。
func TestS3OpenFromInvalidInput(t *testing.T) {
	r, _, _ := newRangeRemote(t, nil)
	if _, err := r.OpenFrom(context.Background(), "/data.bin", -1, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
		t.Fatalf("OpenFrom(-1) = %v, want ErrInvalid", err)
	}
	if _, err := r.OpenFrom(context.Background(), "relative", 0, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
		t.Fatalf("OpenFrom(relative) = %v, want ErrInvalid", err)
	}
}
