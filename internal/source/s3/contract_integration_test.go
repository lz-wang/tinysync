package s3

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"tinysync/internal/source"
	"tinysync/internal/source/remotetest"
)

// contract 环境变量与 internal/e2e 的 integration gate 保持一致：
// 设置 TINYSYNC_IT_S3_ENDPOINT 后运行（CI 由 MinIO integration job
// 注入；未设置时跳过，不影响 make check）。
const contractS3Endpoint = "TINYSYNC_IT_S3_ENDPOINT"

// itStaticCredentials 是测试专用的 static credentials provider
// （与 e2e integration gate 同形态）。
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

// s3ContractHarness 把真实 MinIO / S3 兼容服务接到 remotetest 套件：
// 每次契约执行使用独立的随机 prefix，测试装置经 SDK 直写存储。
type s3ContractHarness struct {
	client *s3.Client
	bucket string
	prefix string
}

// NewRemote 用真实 S3 服务构造全新 prefix 下的 Remote。
func (h *s3ContractHarness) NewRemote(t *testing.T) source.Remote {
	endpoint := os.Getenv(contractS3Endpoint)
	if endpoint == "" {
		t.Skipf("set %s to run the real-S3 contract suite (e.g. MinIO)", contractS3Endpoint)
	}
	region := os.Getenv("TINYSYNC_IT_S3_REGION")
	if region == "" {
		region = "us-east-1"
	}
	accessKey := os.Getenv("TINYSYNC_IT_S3_ACCESS_KEY")
	secretKey := os.Getenv("TINYSYNC_IT_S3_SECRET_KEY")
	if accessKey == "" || secretKey == "" {
		t.Skipf("set TINYSYNC_IT_S3_ACCESS_KEY / TINYSYNC_IT_S3_SECRET_KEY to run the real-S3 contract suite")
	}
	bucket := os.Getenv("TINYSYNC_IT_S3_BUCKET")
	if bucket == "" {
		bucket = "tinysync-it"
	}
	pathStyle := os.Getenv("TINYSYNC_IT_S3_PATH_STYLE") != "false"

	h.client = s3.NewFromConfig(aws.Config{
		Region:      region,
		Credentials: itStaticCredentials{accessKey: accessKey, secretKey: secretKey},
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = pathStyle
	})
	h.bucket = bucket
	h.prefix = "contract-" + randomContractSuffix(t) + "/"

	factory := NewFactory()
	src := source.Source{
		Name: "contract-s3",
		Type: source.TypeS3,
		Config: source.Config{S3: &source.S3Config{
			Endpoint:  endpoint,
			Region:    region,
			Bucket:    bucket,
			Prefix:    strings.TrimSuffix(h.prefix, "/"),
			PathStyle: pathStyle,
			AccessKey: accessKey,
		}},
	}
	remote, err := factory.Create(context.Background(), src, source.Credentials{S3: &source.S3Credentials{
		SecretKey: secretKey,
	}})
	if err != nil {
		t.Fatalf("create s3 remote: %v", err)
	}
	t.Cleanup(func() { h.cleanup(t) })
	return remote
}

// Write 经 SDK 直接写入对象（测试装置不经被测 adapter）。
func (h *s3ContractHarness) Write(t *testing.T, logical, content string) {
	t.Helper()
	key := h.prefix + strings.TrimPrefix(logical, "/")
	if _, err := h.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(h.bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(content),
	}); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

// Mkdir 写入目录占位对象。
func (h *s3ContractHarness) Mkdir(t *testing.T, logical string) {
	t.Helper()
	key := h.prefix + strings.TrimPrefix(logical, "/") + "/"
	if _, err := h.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(h.bucket),
		Key:    aws.String(key),
	}); err != nil {
		t.Fatalf("put dir marker %s: %v", key, err)
	}
}

// cleanup 删除本契约执行产生的全部对象。
func (h *s3ContractHarness) cleanup(t *testing.T) {
	t.Helper()
	pager := s3.NewListObjectsV2Paginator(h.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(h.bucket),
		Prefix: aws.String(h.prefix),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(context.Background())
		if err != nil {
			return
		}
		for _, obj := range page.Contents {
			_, _ = h.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
				Bucket: aws.String(h.bucket),
				Key:    obj.Key,
			})
		}
	}
}

// TestIntegrationRemoteContractSuite 以统一契约套件验证 S3 adapter
// （真实 MinIO，integration gate 专用）。
func TestIntegrationRemoteContractSuite(t *testing.T) {
	remotetest.RunSuite(t, &s3ContractHarness{})
}

// randomContractSuffix 生成契约 prefix 的随机后缀。
func randomContractSuffix(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random suffix: %v", err)
	}
	return hex.EncodeToString(buf)
}
