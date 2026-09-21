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
	"io/fs"
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
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
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
	// 入口统一校验 logical path：非法路径 fail-fast，不做归一化。
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return source.FileInfo{}, err
	}
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
	// not-found 双重标记：ErrInvalid 保持既有判定语义，fs.ErrNotExist
	// 供上层（browser 404 映射）区分「不存在」与「非法路径」。
	return source.FileInfo{}, wrapOp("stat", logicalPath, fmt.Errorf("%w: object %s not found (%w)", source.ErrInvalid, logicalPath, fs.ErrNotExist))
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
// List 实现 source.Remote（单层列目录）。已知参考服务缺陷：MinIO
// 在 MaxKeys=1 且本页仅含 Delimiter rollup（CommonPrefixes）时，
// 后续 ContinuationToken 续页会丢失剩余条目（AWS 无此问题）；本
// adapter 依赖调用方使用常规页大小（limit ≥ 2），契约套件据此
// 覆盖多页游标流转。
func (r *remote) List(ctx context.Context, logicalDir string, opts source.ListOptions) (source.FilePage, error) {
	if err := source.ValidateLogicalPath(logicalDir); err != nil {
		return source.FilePage{}, err
	}
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
		// isSelf 判断条目是否为「正在列出的目录自身」：folder marker
		// 对象（prefix 目录占位）会以目录形态出现在自己的 listing 里，
		// 与 WebDAV / SFTP 的语义对齐必须排除（Depth:1 列目录不包含
		// 目录自身条目）。
		isSelf := func(logical string) bool {
			return dir != "/" && logical == dir
		}
		addDir := func(logical string) {
			if logical != "/" && !isSelf(logical) {
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
			if isSelf(logical) {
				// 目录自身以对象形态出现（marker 之外的非常规形态）：
				// 同样不进入自身 listing。
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

// Mkdir 实现 source.DirectoryCreator。S3 没有原生目录，创建零字节且以
// / 结尾的 folder marker，使目录立即可被标准 delimiter listing 发现。
func (r *remote) Mkdir(ctx context.Context, logicalPath string) error {
	if err := source.ValidateLogicalPath(logicalPath); err != nil || logicalPath == "/" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: cannot create remote root", source.ErrInvalid)
	}
	key := r.objectKey(logicalPath) + "/"
	_, err := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(""),
	})
	if err != nil {
		return wrapOp("mkdir", logicalPath, err)
	}
	return nil
}

// Open 实现 source.Remote：GetObject 返回响应 body。入口统一校验
// logical path。
func (r *remote) Open(ctx context.Context, logicalPath string) (io.ReadCloser, error) {
	if err := source.ValidateLogicalPath(logicalPath); err != nil {
		return nil, err
	}
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

// scanPageSize 是同步扫描的协议页大小。source.MaxListLimit(500) 是
// Files / API 的分页契约，不是协议优化参数；ListObjectsV2 原生
// MaxKeys ≤ 1000，同步 fast scan 单独用满，两个 workload 不强行绑定。
const scanPageSize int32 = 1000

// 编译期断言：ScanTree 可选能力。
var _ source.TreeScanner = (*remote)(nil)

// ScanTree 实现 source.TreeScanner：Delimiter="" 的 flat prefix 扫描
// 一次读通整棵 object namespace，请求量只随对象页数线性增长——
// Delimiter 逐目录递归的请求量与目录数线性相关，深层树会被放大到
// 每目录一次请求。flat 模式不返回 CommonPrefixes，目录从 key 推导：
// 每个对象的祖先目录链全部 visit（seenDirs 去重），folder marker
// 是目录不是文件，root 自身不 visit——包括 RemoteRoot 自身的
// folder marker（Mkdir 写入的 root/ 占位对象会落进 root prefix 的
// flat 扫描结果，若按 marker 推导会产出 "/root/" 这种被
// ValidateLogicalPath 拒绝的路径，令整个 Job 失败）。同 path 既是
// 文件又是目录的
// collision（foo 与 foo/bar.txt 共存）经虚拟目录 visit 暴露给上层
// collector，在任何本地 mutation 前 fail-fast——这是 ScanTree 必须
// visit 目录的主要原因。visit 错误原样透传，任何一页失败即整体
// 失败（契约见 source.TreeScanner）。
func (r *remote) ScanTree(ctx context.Context, root string, visit func(source.FileInfo) error) error {
	if err := source.ValidateLogicalPath(root); err != nil {
		return err
	}
	cleaned := path.Clean("/" + root)
	prefix := r.dirPrefix(cleaned)
	seenDirs := make(map[string]struct{})

	// emitDir visit 一个推导出的虚拟目录（去重）。
	emitDir := func(logicalDir string) error {
		if _, seen := seenDirs[logicalDir]; seen {
			return nil
		}
		seenDirs[logicalDir] = struct{}{}
		return visit(source.FileInfo{Path: logicalDir, IsDir: true})
	}

	input := &s3.ListObjectsV2Input{
		Bucket:    aws.String(r.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String(""),
		MaxKeys:   aws.Int32(scanPageSize),
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, err := r.client.ListObjectsV2(ctx, input)
		if err != nil {
			return wrapOp("scan", root, err)
		}
		for _, obj := range out.Contents {
			// 条目循环内逐条检查 ctx：单页可能带回 1000 个对象，取消
			// 不能等到下一页的请求边界才生效。
			if err := ctx.Err(); err != nil {
				return err
			}
			key := derefStr(obj.Key)
			if key == "" {
				continue
			}
			logical, err := r.toLogical(key)
			if err != nil {
				return wrapOp("scan", root, err)
			}
			if logical == "/" || logical == cleaned {
				// root 自身（prefix 自身对象或 root 的 folder
				// marker）不 visit：TreeScanner 只枚举 descendants。
				continue
			}
			// root 内相对分量：logical = <root>/<segs...>。Trim 去掉
			// 前导 / 后再分段，避免 Split 产生空首分量。
			base := cleaned
			if base == "/" {
				base = ""
			}
			segs := strings.Split(strings.Trim(strings.TrimPrefix(logical, base), "/"), "/")
			// 目录链深度：文件 visit 其全部祖先目录；folder marker
			// （以 / 结尾的 key）自身就是目录，连同祖先一起 visit。
			depth := len(segs) - 1
			if strings.HasSuffix(key, "/") {
				depth = len(segs)
			}
			logicalDir := base
			for i := 0; i < depth; i++ {
				logicalDir += "/" + segs[i]
				if err := emitDir(logicalDir); err != nil {
					return err
				}
			}
			if strings.HasSuffix(key, "/") {
				// folder marker：目录占位对象，不作为文件。
				continue
			}
			if err := visit(source.FileInfo{
				Path:  logical,
				IsDir: false,
				Fingerprint: source.Fingerprint{
					Size:       deref64(obj.Size),
					ModifiedAt: derefTime(obj.LastModified),
					ETag:       derefStr(obj.ETag),
				},
			}); err != nil {
				return err
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}
		if out.NextContinuationToken == nil {
			return wrapOp("scan", root, errors.New("truncated response without continuation token"))
		}
		input.ContinuationToken = out.NextContinuationToken
	}
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
// 可判定。协议错误分类在 adapter boundary 内完成（classifyS3Error）。
func wrapOp(op, logicalPath string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("s3 %s %s: %w", op, logicalPath, classifyS3Error(err))
}

// classifyS3Error 按协议语义标记错误：408 / 429 / 5xx（请求超时、
// 限流、瞬时服务故障）为 transient；其余全部 4xx（NoSuchKey /
// NotFound、401 / 403 认证授权、400 / 409 / 412 等客户端错误——
// 重连不会改变结果）为 permanent。无状态码可判定的错误不带标记，
// 由 source.IsRetryable 的通用规则兜底。
func classifyS3Error(err error) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return source.MarkPermanent(err)
	}
	var respErr interface{ HTTPStatusCode() int }
	if errors.As(err, &respErr) {
		switch code := respErr.HTTPStatusCode(); {
		case code == 408 || code == 429 || code >= 500:
			return source.MarkTransient(err)
		case code >= 400:
			return source.MarkPermanent(err)
		}
	}
	return err
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
