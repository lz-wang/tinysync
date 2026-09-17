// Package s3 实现 source 的 S3 只读 Remote：基于 AWS SDK for Go v2，
// 显式 static credentials（不使用宿主机 ambient credential chain），
// 支持 BaseEndpoint 与 path-style（自建 S3 / MinIO 场景）。
// 逻辑路径映射固定：Source "/" ↔ <prefix>/，"/docs/a.pdf" ↔
// <prefix>docs/a.pdf；ListObjectsV2 + Delimiter 只列一层目录，保持
// 现有递归 ScanRemote 不变。
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"tinysync/internal/source"
)

// Factory 实现 source.RemoteFactory，按 Source 配置构造 S3 客户端。
type Factory struct{}

// NewFactory 构造 S3 RemoteFactory。
func NewFactory() *Factory {
	return &Factory{}
}

// Type 实现 source.RemoteFactory：本 factory 服务 S3 类型。
func (f *Factory) Type() source.Type {
	return source.TypeS3
}

// Create 实现 source.RemoteFactory：显式 static credentials，禁用
// ambient credential chain——Source 身份完全由自身配置决定，不受
// 服务器环境（~/.aws、EC2 metadata、环境变量）影响。
func (f *Factory) Create(ctx context.Context, s source.Source, credentials source.Credentials) (source.Remote, error) {
	if s.Type != source.TypeS3 || s.Config.S3 == nil {
		return nil, fmt.Errorf("%w: %q", source.ErrUnsupportedType, s.Type)
	}
	if credentials.S3 == nil || credentials.S3.SecretKey == "" {
		return nil, fmt.Errorf("%w: s3 secret_key is required", source.ErrInvalid)
	}
	cfg := *s.Config.S3
	client := s3.NewFromConfig(aws.Config{Region: cfg.Region}, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.PathStyle
		o.Credentials = staticCredentialsProvider{aws.Credentials{
			AccessKeyID:     cfg.AccessKey,
			SecretAccessKey: credentials.S3.SecretKey,
		}}
	})
	return &remote{
		client: client,
		bucket: cfg.Bucket,
		prefix: normalizePrefix(cfg.Prefix),
	}, nil
}

// staticCredentialsProvider 提供 fixed 凭据集合。
type staticCredentialsProvider struct {
	creds aws.Credentials
}

// Retrieve 实现 aws.CredentialsProvider。
func (p staticCredentialsProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	return p.creds, nil
}

// normalizePrefix 把 bucket 内子前缀归一：去前导 /，非空时以 / 结尾。
func normalizePrefix(prefix string) string {
	prefix = strings.TrimPrefix(prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix
}

// API 是 adapter 依赖的最小 S3 能力面；*s3.Client 天然实现。分页由
// adapter 以 ContinuationToken 驱动，不依赖 paginator 具体类型。
// 导出供 e2e 协议矩阵注入进程内协议模拟（真实 S3 服务由 integration
// gate 验证）。
type API interface {
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// 编译期断言：SDK client 满足能力面。
var _ API = (*s3.Client)(nil)

// remote 是 source.Remote 的 S3 实现。
type remote struct {
	client API
	bucket string
	// prefix 是 Source root 在 bucket 内的子前缀（空或以 / 结尾）。
	prefix string
}

// 编译期断言。
var _ source.Remote = (*remote)(nil)

// NewRemoteWithAPI 用给定能力面构造 Remote：e2e 协议矩阵经它注入
// 进程内 S3 协议模拟；生产路径经 Factory.Create 构造真实 SDK client。
func NewRemoteWithAPI(api API, bucket, prefix string) source.Remote {
	return &remote{client: api, bucket: bucket, prefix: normalizePrefix(prefix)}
}

// objectKey 把 Source-relative logical path 转为 bucket 内 object key。
// 调用方保证 logicalPath 非 root。
func (r *remote) objectKey(logicalPath string) string {
	return r.prefix + strings.TrimPrefix(path.Clean(logicalPath), "/")
}

// dirPrefix 把 logical 目录路径转为 List 前缀（以 / 结尾）。
func (r *remote) dirPrefix(logicalDir string) string {
	cleaned := path.Clean(logicalDir)
	if cleaned == "/" {
		return r.prefix
	}
	return r.prefix + strings.TrimPrefix(cleaned, "/") + "/"
}

// toLogical 把 bucket 内 object key / common prefix 剥离 Source
// prefix，转换为 Source-relative logical path，并统一过
// ValidateLogicalPath。key 必须落在 prefix 之内。
func (r *remote) toLogical(key string) (string, error) {
	trimmed := strings.TrimSuffix(key, "/")
	if r.prefix != "" {
		if trimmed == strings.TrimSuffix(r.prefix, "/") {
			// prefix 自身对象（如 marker）映射为 root。
			return "/", nil
		}
		if !strings.HasPrefix(key, r.prefix) {
			return "", fmt.Errorf("s3 key %q escapes source prefix %q", key, r.prefix)
		}
		trimmed = strings.TrimPrefix(trimmed, r.prefix)
	}
	logical := "/" + trimmed
	if logical == "/" {
		return "/", nil
	}
	if err := source.ValidateLogicalPath(logical); err != nil {
		return "", err
	}
	return logical, nil
}

// Stat 实现 source.Remote：root 用 List 探测 bucket 可用性；文件用
// HeadObject；Head 失败时探测目录前缀。S3 目录可能没有 marker 对象，
// 不能只靠对象存在性判断目录。
func (r *remote) Stat(ctx context.Context, logicalPath string) (source.FileInfo, error) {
	cleaned := path.Clean("/" + logicalPath)
	if cleaned == "/" {
		// 探测 bucket / prefix 可访问性：一个对象的成本。
		_, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(r.bucket),
			Prefix:  aws.String(r.prefix),
			MaxKeys: aws.Int32(1),
		})
		if err != nil {
			return source.FileInfo{}, wrapOp("stat", logicalPath, err)
		}
		return source.FileInfo{Path: "/", IsDir: true}, nil
	}

	key := r.objectKey(cleaned)
	head, err := r.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return source.FileInfo{
			Path:  cleaned,
			IsDir: false,
			Fingerprint: source.Fingerprint{
				Size:       deref64(head.ContentLength),
				ModifiedAt: derefTime(head.LastModified),
				ETag:       derefStr(head.ETag),
			},
		}, nil
	}
	if !isNotFound(err) {
		return source.FileInfo{}, wrapOp("stat", logicalPath, err)
	}
	// 目录探测：prefix 下存在对象即视为目录。
	dirPrefix := r.dirPrefix(cleaned)
	out, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(r.bucket),
		Prefix:  aws.String(dirPrefix),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return source.FileInfo{}, wrapOp("stat", logicalPath, isNotFoundToErr(err))
	}
	if (out.KeyCount != nil && *out.KeyCount > 0) || len(out.Contents) > 0 || len(out.CommonPrefixes) > 0 {
		return source.FileInfo{Path: cleaned, IsDir: true}, nil
	}
	return source.FileInfo{}, wrapOp("stat", logicalPath, fmt.Errorf("%w: object %s not found", source.ErrInvalid, logicalPath))
}

// List 实现 source.Remote：ListObjectsV2 + Delimiter="/" 只列一层，
// Contents 合成文件、CommonPrefixes 合成目录。S3 分页是协议原生：
// opts.Cursor ↔ ContinuationToken、opts.Limit ↔ MaxKeys，服务端游标
// 驱动，不假设单页 1000 object。folder marker（以 / 结尾的 key）
// 表示目录，不作为文件条目。同一 logical path 在同一页内同时是
// 文件与目录时整个 List 失败（fail-fast）；跨页 collision 由 scanner
// 的已见条目检测兜底。MaxKeys 约束 keys + common prefixes 总数，
// 一页经 marker 过滤后条目可能变少甚至为空——此时继续以
// ContinuationToken 拉取，直到产出条目或 EOF：契约要求空页不得
// 携带 NextCursor。
func (r *remote) List(ctx context.Context, logicalDir string, opts source.ListOptions) (source.FilePage, error) {
	dir := path.Clean("/" + logicalDir)
	prefix := r.dirPrefix(dir)

	input := &s3.ListObjectsV2Input{
		Bucket:    aws.String(r.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
		MaxKeys:   aws.Int32(int32(source.NormalizeListLimit(opts.Limit))),
	}
	if opts.Cursor != "" {
		input.ContinuationToken = aws.String(opts.Cursor)
	}
	for {
		out, err := r.client.ListObjectsV2(ctx, input)
		if err != nil {
			return source.FilePage{}, wrapOp("list", logicalDir, err)
		}

		files := make(map[string]source.FileInfo)
		dirs := make(map[string]bool)
		addDir := func(logical string) {
			if logical != "/" {
				dirs[logical] = true
			}
		}
		for _, obj := range out.Contents {
			key := derefStr(obj.Key)
			if key == "" {
				continue
			}
			logical, err := r.toLogical(key)
			if err != nil {
				return source.FilePage{}, wrapOp("list", logicalDir, err)
			}
			if strings.HasSuffix(key, "/") {
				// folder marker：零字节目录占位对象。
				addDir(logical)
				continue
			}
			files[logical] = source.FileInfo{
				Path:  logical,
				IsDir: false,
				Fingerprint: source.Fingerprint{
					Size:       deref64(obj.Size),
					ModifiedAt: derefTime(obj.LastModified),
					ETag:       derefStr(obj.ETag),
				},
			}
		}
		for _, cp := range out.CommonPrefixes {
			logical, err := r.toLogical(derefStr(cp.Prefix))
			if err != nil {
				return source.FilePage{}, wrapOp("list", logicalDir, err)
			}
			addDir(logical)
		}

		// file/dir collision（本页内）：fail whole scan。
		for name := range files {
			if dirs[name] {
				return source.FilePage{}, wrapOp("list", logicalDir, fmt.Errorf(
					"%w: %q is both a file and a directory in the s3 namespace", source.ErrInvalid, name))
			}
		}

		entries := make([]source.FileInfo, 0, len(files)+len(dirs))
		for _, fi := range files {
			entries = append(entries, fi)
		}
		for name := range dirs {
			entries = append(entries, source.FileInfo{Path: name, IsDir: true})
		}

		truncated := out.IsTruncated != nil && *out.IsTruncated
		if len(entries) > 0 || !truncated {
			next := ""
			if truncated {
				if out.NextContinuationToken == nil {
					return source.FilePage{}, wrapOp("list", logicalDir, errors.New("truncated response without continuation token"))
				}
				next = *out.NextContinuationToken
			}
			return source.FilePage{Entries: entries, NextCursor: next}, nil
		}
		// 过滤后为空但服务端还有下一页：继续拉取，避免空页游标。
		if out.NextContinuationToken == nil {
			return source.FilePage{}, wrapOp("list", logicalDir, errors.New("truncated response without continuation token"))
		}
		input.ContinuationToken = out.NextContinuationToken
	}
}

// Open 实现 source.Remote：GetObject 返回响应 body。
func (r *remote) Open(ctx context.Context, logicalPath string) (io.ReadCloser, error) {
	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(r.objectKey(logicalPath)),
	})
	if err != nil {
		return nil, wrapOp("open", logicalPath, err)
	}
	return out.Body, nil
}

// Close 实现 source.Remote：S3 基于 HTTP、无持久会话，连接复用由
// SDK 的 HTTP client 管理，显式关闭为空操作。
func (r *remote) Close() error {
	return nil
}

// isNotFound 判定 S3 侧「对象不存在」：NoSuchKey / NotFound API 错误
// 或 HTTP 404。
func isNotFound(err error) bool {
	var noKey *types.NoSuchKey
	var notFound *types.NotFound
	if errors.As(err, &noKey) || errors.As(err, &notFound) {
		return true
	}
	var respErr interface{ HTTPStatusCode() int }
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == 404 {
		return true
	}
	return false
}

// isNotFoundToErr 把 NotFound 类错误归一为可读的 not found 语义
// （Stat 目录探测路径使用）。
func isNotFoundToErr(err error) error {
	if isNotFound(err) {
		return fmt.Errorf("%w: s3 object not found", source.ErrInvalid)
	}
	return err
}

// wrapOp 为底层错误补充操作与路径上下文；ctx 超时/取消经 %w 保持
// 可判定。
func wrapOp(op, logicalPath string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("s3 %s %s: %w", op, logicalPath, err)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefTime(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}
