package s3

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"tinysync/internal/syncjob"
)

// benchmarkS3 是 benchmark 专用的进程内 S3 模拟：与 client_test 的
// fakeS3 不同，它预生成并排序对象数组，按 Prefix / Delimiter /
// MaxKeys / ContinuationToken 从游标处线性扫描——请求成本模型贴近
// 真实 S3（每页 O(页内条目)），避免把 fake 自身的全量 O(N) 遍历算
// 进 benchmark 数据。listCalls 计数用于报告确定性的 listobjects/op
// 指标（同步扫描的请求规模是算法属性，不是速度属性）。
type benchmarkS3 struct {
	keys []string       // 升序，与 objs 平行
	objs []types.Object // 升序，与 keys 平行
	// listCalls 累计 ListObjectsV2 请求次数。
	listCalls atomic.Int64
}

func newBenchmarkS3(keys []string) *benchmarkS3 {
	sort.Strings(keys)
	mod := time.Unix(1757879400, 0).UTC()
	m := &benchmarkS3{
		keys: keys,
		objs: make([]types.Object, len(keys)),
	}
	for i, key := range keys {
		k := key
		size := int64(1)
		etag := fmt.Sprintf(`"etag-%06d"`, i)
		modCopy := mod
		m.objs[i] = types.Object{
			Key:          &k,
			Size:         &size,
			ETag:         &etag,
			LastModified: &modCopy,
		}
	}
	return m
}

// flatKeys 生成 count 个单层对象 key。
func flatKeys(prefix string, count int) []string {
	keys := make([]string, count)
	for i := range keys {
		keys[i] = fmt.Sprintf("%sf%06d", prefix, i)
	}
	return keys
}

// deepKeys 生成 dirs 个子目录、各 perDir 个对象的 key 集合。
func deepKeys(prefix string, dirs, perDir int) []string {
	keys := make([]string, 0, dirs*perDir)
	for d := 0; d < dirs; d++ {
		for i := 0; i < perDir; i++ {
			keys = append(keys, fmt.Sprintf("%sd%04d/f%02d", prefix, d, i))
		}
	}
	return keys
}

func (m *benchmarkS3) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	m.listCalls.Add(1)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	prefix := derefStr(params.Prefix)
	delim := derefStr(params.Delimiter)
	maxKeys := 1000
	if params.MaxKeys != nil && *params.MaxKeys > 0 {
		maxKeys = int(*params.MaxKeys)
	}
	start := 0
	if token := derefStr(params.ContinuationToken); token != "" {
		if n, err := fmt.Sscanf(token, "off-%d", &start); err != nil || n != 1 {
			return nil, errors.New("InvalidArgument: The continuation token provided is incorrect")
		}
	} else if prefix != "" {
		// 二分定位 prefix 块头：keys 升序，prefix 之前的对象与本枚举
		// 无关（模拟真实 S3 的索引定位，避免 O(N) 起始扫描）。
		start = sort.SearchStrings(m.keys, prefix)
	}

	var contents []types.Object
	var common []types.CommonPrefix
	count := 0
	i := start
	for i < len(m.objs) && count < maxKeys {
		key := m.keys[i]
		if !strings.HasPrefix(key, prefix) {
			// keys 升序，prefix 块连续：越过块尾即枚举完成。
			break
		}
		rest := strings.TrimPrefix(key, prefix)
		if delim != "" {
			if idx := strings.Index(rest, delim); idx >= 0 {
				// Delimiter rollup：整个子前缀聚合为一个 CommonPrefix，
				// 消费其下全部对象——真实 S3 的流式行为模型。
				p := prefix + rest[:idx+len(delim)]
				common = append(common, types.CommonPrefix{Prefix: &p})
				count++
				for i < len(m.objs) && strings.HasPrefix(m.keys[i], p) {
					i++
				}
				continue
			}
		}
		contents = append(contents, m.objs[i])
		count++
		i++
	}

	keyCount := int32(count)
	out := &s3.ListObjectsV2Output{
		Contents:       contents,
		CommonPrefixes: common,
		KeyCount:       &keyCount,
	}
	if i < len(m.objs) && strings.HasPrefix(m.keys[i], prefix) {
		truncated := true
		out.IsTruncated = &truncated
		next := fmt.Sprintf("off-%d", i)
		out.NextContinuationToken = &next
	}
	return out, nil
}

func (m *benchmarkS3) HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, errors.New("benchmarkS3: HeadObject not implemented")
}

func (m *benchmarkS3) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, errors.New("benchmarkS3: GetObject not implemented")
}

func (m *benchmarkS3) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, errors.New("benchmarkS3: PutObject not implemented")
}

// 编译期断言：benchmark 模拟满足 adapter 能力面。
var _ API = (*benchmarkS3)(nil)

// BenchmarkScanRemoteS3_10KFlat：同步扫描 10,000 单层对象。请求规模
// 只应由页数决定（10000 / 1000 ≈ 10 次 ListObjectsV2/op）。
func BenchmarkScanRemoteS3_10KFlat(b *testing.B) {
	m := newBenchmarkS3(flatKeys("bulk/", 10000))
	remote := NewRemoteWithAPI(m, "bench", "")
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	before := m.listCalls.Load()
	for i := 0; i < b.N; i++ {
		files, err := syncjob.ScanRemote(ctx, remote, "/")
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if len(files) != 10000 {
			b.Fatalf("scanned %d files, want 10000", len(files))
		}
	}
	b.ReportMetric(float64(m.listCalls.Load()-before)/float64(b.N), "listobjects/op")
}

// BenchmarkScanRemoteS3_10KDeep：同步扫描 1,000 目录 × 10 对象。当前
// Delimiter 递归实现的请求量由目录数主导（root 1000 prefixes /
// 100 per page ≈ 10 页 + 1000 子目录各 1 页 ≈ 1010 次/op）；flat
// prefix 扫描后请求量只随对象页数增长（≈10 次/op）。
func BenchmarkScanRemoteS3_10KDeep(b *testing.B) {
	m := newBenchmarkS3(deepKeys("bulk/", 1000, 10))
	remote := NewRemoteWithAPI(m, "bench", "")
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	before := m.listCalls.Load()
	for i := 0; i < b.N; i++ {
		files, err := syncjob.ScanRemote(ctx, remote, "/")
		if err != nil {
			b.Fatalf("scan: %v", err)
		}
		if len(files) != 1000*10 {
			b.Fatalf("scanned %d files, want %d", len(files), 1000*10)
		}
	}
	b.ReportMetric(float64(m.listCalls.Load()-before)/float64(b.N), "listobjects/op")
}
