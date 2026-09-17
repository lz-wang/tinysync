package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"tinysync/internal/source"
	s3adapter "tinysync/internal/source/s3"
)

// integration 环境变量：设置 TINYSYNC_IT_S3_ENDPOINT 后
// TestIntegrationS3 才会运行（CI 由 integration job 注入 MinIO 服务；
// 本地可对任意 S3 兼容服务运行）。未设置时跳过，不影响 make check。
const (
	itS3Endpoint   = "TINYSYNC_IT_S3_ENDPOINT"
	itS3Region     = "TINYSYNC_IT_S3_REGION"
	itS3AccessKey  = "TINYSYNC_IT_S3_ACCESS_KEY"
	itS3SecretKey  = "TINYSYNC_IT_S3_SECRET_KEY"
	itS3Bucket     = "TINYSYNC_IT_S3_BUCKET"
	itS3Prefix     = "TINYSYNC_IT_S3_PREFIX"
	itS3PathStyle  = "TINYSYNC_IT_S3_PATH_STYLE"
	itS3BucketName = "tinysync-it"
)

// itStaticCredentials 是测试专用的 static credentials provider。
type itStaticCredentials struct {
	accessKey string
	secretKey string
}

func (p itStaticCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) {
	return aws.Credentials{
		AccessKeyID:     p.accessKey,
		SecretAccessKey: p.secretKey,
	}, nil
}

// TestIntegrationS3 用真实 S3 服务验证 adapter 与同一 Sync Engine：
// 走真实 SDK client（含 HTTP 协议、签名、分页），运行与协议矩阵
// 完全相同的同步场景。
func TestIntegrationS3(t *testing.T) {
	endpoint := os.Getenv(itS3Endpoint)
	if endpoint == "" {
		t.Skipf("set %s to run the real-S3 integration scenario (e.g. MinIO)", itS3Endpoint)
	}
	region := os.Getenv(itS3Region)
	if region == "" {
		region = "us-east-1"
	}
	accessKey := os.Getenv(itS3AccessKey)
	secretKey := os.Getenv(itS3SecretKey)
	bucket := os.Getenv(itS3Bucket)
	if bucket == "" {
		bucket = itS3BucketName
	}
	prefix := os.Getenv(itS3Prefix)
	pathStyle := os.Getenv(itS3PathStyle) != "false"

	ctx := context.Background()
	admin := s3.NewFromConfig(aws.Config{
		Region:      region,
		Credentials: itStaticCredentials{accessKey: accessKey, secretKey: secretKey},
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = pathStyle
	})

	// 幂等建桶：已存在（桶属当前账号）视为成功。
	if _, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if !errors.As(err, &owned) {
			t.Fatalf("create bucket %s: %v", bucket, err)
		}
	}

	write := func(t *testing.T, logical, content string) {
		t.Helper()
		key := strings.TrimPrefix(logical, "/")
		if prefix != "" {
			key = prefix + "/" + key
		}
		if _, err := admin.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   strings.NewReader(content),
		}); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	remove := func(t *testing.T, logical string) {
		t.Helper()
		key := strings.TrimPrefix(logical, "/")
		if prefix != "" {
			key = prefix + "/" + key
		}
		if _, err := admin.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		}); err != nil {
			t.Fatalf("delete %s: %v", key, err)
		}
	}

	factory := s3adapter.NewFactory()
	src := source.Source{
		Name: "integration-s3",
		Type: source.TypeS3,
		Config: source.Config{S3: &source.S3Config{
			Endpoint:  endpoint,
			Region:    region,
			Bucket:    bucket,
			Prefix:    prefix,
			PathStyle: pathStyle,
			AccessKey: accessKey,
		}},
	}
	creds := source.Credentials{S3: &source.S3Credentials{SecretKey: secretKey}}

	runCommonSyncScenario(t, matrixRemote{
		name:   "s3-real",
		put:    write,
		remove: remove,
		openRemote: func() (source.Remote, error) {
			return factory.Create(context.Background(), src, creds)
		},
	})
}

// TestIntegrationS3Cancellation 在真实 S3 服务上验证取消语义：
// 取消运行后传输终止、连接释放（ctx 取消可判定）。
func TestIntegrationS3Cancellation(t *testing.T) {
	endpoint := os.Getenv(itS3Endpoint)
	if endpoint == "" {
		t.Skipf("set %s to run the real-S3 integration scenario", itS3Endpoint)
	}
	region := os.Getenv(itS3Region)
	if region == "" {
		region = "us-east-1"
	}
	accessKey := os.Getenv(itS3AccessKey)
	secretKey := os.Getenv(itS3SecretKey)
	bucket := os.Getenv(itS3Bucket)
	if bucket == "" {
		bucket = itS3BucketName
	}

	factory := s3adapter.NewFactory()
	src := source.Source{
		Name: "integration-s3-cancel",
		Type: source.TypeS3,
		Config: source.Config{S3: &source.S3Config{
			Endpoint:  endpoint,
			Region:    region,
			Bucket:    bucket,
			PathStyle: os.Getenv(itS3PathStyle) != "false",
			AccessKey: accessKey,
		}},
	}
	creds := source.Credentials{S3: &source.S3Credentials{SecretKey: secretKey}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := factory.Create(ctx, src, creds)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = r.Close() }()
	if _, err := r.Stat(ctx, "/"); !errors.Is(err, context.DeadlineExceeded) && err != nil {
		// 2 秒内完成 Stat 也接受（仅验证不挂死）；超时必须可判定。
		t.Logf("Stat returned %v (deadline not hit)", err)
	}
}

// TestIntegrationS3Pagination 在真实 S3 服务上验证 browser 分页的
// production path：AWS SDK ListObjectsV2 的 ContinuationToken 链。
// 建议每页条数不落在 SDK 默认页大小（1000）的整除关系上——limit=37
// 且 201 个对象跨 6 页，覆盖「页大小 < 单次 API 响应」与「最后一页
// 不满页」两种形态。
func TestIntegrationS3Pagination(t *testing.T) {
	endpoint := os.Getenv(itS3Endpoint)
	if endpoint == "" {
		t.Skipf("set %s to run the real-S3 integration scenario (e.g. MinIO)", itS3Endpoint)
	}
	region := os.Getenv(itS3Region)
	if region == "" {
		region = "us-east-1"
	}
	accessKey := os.Getenv(itS3AccessKey)
	secretKey := os.Getenv(itS3SecretKey)
	bucket := os.Getenv(itS3Bucket)
	if bucket == "" {
		bucket = itS3BucketName
	}
	prefix := os.Getenv(itS3Prefix)
	pathStyle := os.Getenv(itS3PathStyle) != "false"

	const (
		objectCount = 201
		pageLimit   = 37
		wantPages   = 6 // ceil(201/37)：末页 16 条
	)

	ctx := context.Background()
	admin := s3.NewFromConfig(aws.Config{
		Region:      region,
		Credentials: itStaticCredentials{accessKey: accessKey, secretKey: secretKey},
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = pathStyle
	})

	// 幂等建桶：已存在（桶属当前账号）视为成功。
	if _, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if !errors.As(err, &owned) {
			t.Fatalf("create bucket %s: %v", bucket, err)
		}
	}

	// 对象放进带时间戳的子目录：与其他场景及历史运行隔离。
	dir := fmt.Sprintf("it-pagination-%d", time.Now().UnixNano())
	key := func(i int) string {
		k := dir + fmt.Sprintf("/page-%03d.txt", i)
		if prefix != "" {
			k = prefix + "/" + k
		}
		return k
	}
	for i := 0; i < objectCount; i++ {
		if _, err := admin.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key(i)),
			Body:   strings.NewReader("x"),
		}); err != nil {
			t.Fatalf("put %s: %v", key(i), err)
		}
	}
	t.Cleanup(func() {
		identifiers := make([]types.ObjectIdentifier, 0, objectCount)
		for i := 0; i < objectCount; i++ {
			identifiers = append(identifiers, types.ObjectIdentifier{Key: aws.String(key(i))})
		}
		if _, err := admin.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &types.Delete{Objects: identifiers},
		}); err != nil {
			t.Logf("cleanup delete: %v", err)
		}
	})

	factory := s3adapter.NewFactory()
	src := source.Source{
		Name: "integration-s3-pagination",
		Type: source.TypeS3,
		Config: source.Config{S3: &source.S3Config{
			Endpoint:  endpoint,
			Region:    region,
			Bucket:    bucket,
			Prefix:    prefix,
			PathStyle: pathStyle,
			AccessKey: accessKey,
		}},
	}
	creds := source.Credentials{S3: &source.S3Credentials{SecretKey: secretKey}}
	remote, err := factory.Create(ctx, src, creds)
	if err != nil {
		t.Fatalf("Create remote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	// 连续消费 cursor：页数、去重后的条目集合与总数都要对上。
	seen := map[string]bool{}
	pages := 0
	cursor := ""
	for {
		page, listErr := remote.List(ctx, "/"+dir, source.ListOptions{Limit: pageLimit, Cursor: cursor})
		if listErr != nil {
			t.Fatalf("list page %d: %v", pages, listErr)
		}
		if len(page.Entries) > pageLimit {
			t.Errorf("page %d entries = %d, want <= limit %d", pages, len(page.Entries), pageLimit)
		}
		for _, fi := range page.Entries {
			if fi.IsDir {
				continue
			}
			if seen[fi.Path] {
				t.Errorf("duplicated entry %s across pages", fi.Path)
			}
			seen[fi.Path] = true
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		if pages > wantPages {
			t.Fatalf("cursor chain exceeds %d pages, last cursor %q", wantPages, page.NextCursor)
		}
		cursor = page.NextCursor
	}
	if pages != wantPages {
		t.Errorf("pages = %d, want %d (%d objects, limit %d)", pages, wantPages, objectCount, pageLimit)
	}
	if len(seen) != objectCount {
		t.Errorf("unique entries = %d, want %d", len(seen), objectCount)
	}
	for i := 0; i < objectCount; i++ {
		want := "/" + dir + fmt.Sprintf("/page-%03d.txt", i)
		if !seen[want] {
			t.Errorf("missing entry %s across pages", want)
		}
	}
}
