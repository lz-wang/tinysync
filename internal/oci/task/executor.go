package task

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"

	"tinysync/internal/oci/image"
	"tinysync/internal/oci/registry"
)

// DefaultTaskConcurrency 任务内镜像复制的固定并发数。
// 个人工具不提供每任务可调的并发参数，直接使用全局常量
// （OCI 传输并发独立于文件域 TransferLimiter，简单优先）。
const DefaultTaskConcurrency = 3

// CopyResult represents the result of a single image copy operation
type CopyResult struct {
	Source           string                     `json:"source"`
	Destination      string                     `json:"destination"`
	TransferredBytes int64                      `json:"transferred_bytes,omitempty"`
	Success          bool                       `json:"success"`
	Skipped          bool                       `json:"skipped"`
	PlatformSkip     bool                       `json:"platform_skip,omitempty"` // 因 platform 不存在而跳过
	Error            string                     `json:"error,omitempty"`
	Duration         int64                      `json:"duration_ms"`
	DstRegistry      string                     `json:"dst_registry,omitempty"` // 目标 registry 路径（含 namespace），用于运行明细
	StartTime        time.Time                  `json:"start_time"`
	EndTime          time.Time                  `json:"end_time"`
	PlatformResults  []image.PlatformCopyResult `json:"platform_results,omitempty"`
}

// TaskResult represents the result of a task execution
type TaskResult struct {
	Status           string        `json:"status"`
	Error            string        `json:"error,omitempty"`
	StartTime        time.Time     `json:"start_time"`
	EndTime          time.Time     `json:"end_time"`
	Duration         time.Duration `json:"-"`
	TotalImages      int           `json:"total_images"`
	SuccessCount     int           `json:"success_count"`
	SkippedCount     int           `json:"skipped_count"`
	FailureCount     int           `json:"failure_count"`
	TransferredBytes int64         `json:"transferred_bytes,omitempty"`
	Results          []CopyResult  `json:"results"`
}

// TaskConfig 是 oci 任务落库配置的领域形态（oci-sync TaskConfig 移植，
// 去 id / name / enabled / schedule / notification_hooks——那些在 jobs 表
// 与全局设置）。
type TaskConfig struct {
	SrcImages  []string                  `json:"src_images"`
	DstServers []string                  `json:"dst_servers"`
	Platforms  []registry.PlatformConfig `json:"platforms"` // 任务级平台过滤配置（可选，未配置则同步全部平台）
}

// ProgressSink 是任务执行器回报运行进度的事件接口（oci-sync 12 参回调
// 的移植形态）：工作量分母、(image, dst) 对级在途登记与完成计数。
// 实现必须并发安全（固定并发 worker 池并发调用）。
type ProgressSink interface {
	// SetWorkTotal 设置计划工作量（镜像数 × 目标数，整轮一次）。
	SetWorkTotal(total int)
	// BeginPair 登记一个在途镜像复制对（「running 起点也回报一次」的
	// 现有语义）；target 为目标镜像完整引用。
	BeginPair(target string)
	// EndPair 在复制对收敛（成功 / 跳过 / 失败 / 取消）后注销。
	EndPair(target string)
	// PairDone 在复制对真正收敛为成功或跳过（digest 相同 /
	// platform_skip）后递增完成数；失败不递增（与统一 Runner 的
	// AddWorkDone 契约一致）。
	PairDone()
}

// nullProgress 把可为 nil 的 ProgressSink 规整为 no-op 实现。
type nullProgress struct{}

func (nullProgress) SetWorkTotal(int) {}

func (nullProgress) BeginPair(string) {}

func (nullProgress) EndPair(string) {}

func (nullProgress) PairDone() {}

// TaskExecutor handles task execution
type TaskExecutor struct {
	logger registry.Logger
	client *registry.Client
}

// NewTaskExecutor creates a new task executor
func NewTaskExecutor(logger registry.Logger) *TaskExecutor {
	return &TaskExecutor{
		logger: logger,
		client: registry.NewClient(logger),
	}
}

// copyPlan 是一个 (image, dst) 对的预解析结果：目标完整引用（进度
// target 与明细 Destination）在派发前确定，BeginPair 才能以目标镜像
// 完整引用登记在途单元。
type copyPlan struct {
	src       *image.SrcImage
	dst       *image.DstImage
	dstName   string // 目标 registry 配置名（dst_servers 条目）
	dstPath   string // 目标 registry 路径（含 namespace）
	target    string // 目标镜像完整引用（repo:tag）
	sourceRef string // 源镜像引用（repo:tag）
	sourceReg string // 源 registry 主机
	planErr   string // 计划失败原因；非空时直接产出失败结果
}

// ExecuteTask executes a single task
func (e *TaskExecutor) ExecuteTask(ctx context.Context, res registry.Resolver, task TaskConfig, prog ProgressSink) (*TaskResult, error) {
	if prog == nil {
		prog = nullProgress{}
	}
	startTime := time.Now()
	e.logger.Infof("starting oci task execution: images=%d destinations=%d", len(task.SrcImages), len(task.DstServers))

	result := &TaskResult{
		StartTime:   startTime,
		TotalImages: 0,
		Results:     make([]CopyResult, 0),
	}

	// Parse source images (expand comma-separated tags)
	images, err := e.parseSrcImages(task.SrcImages)
	if err != nil {
		return result, fmt.Errorf("failed to parse source images: %w", err)
	}

	// Calculate total operations (images * destinations)
	result.TotalImages = len(images) * len(task.DstServers)
	prog.SetWorkTotal(result.TotalImages)

	// Create a channel for results
	resultsChan := make(chan CopyResult, result.TotalImages)
	// 每轮运行一个 CopySession：源描述符 / index manifest 的
	// singleflight 缓存语义保留（同轮内同源镜像不重复拉取元数据）。
	copySession := registry.NewCopySession()

	// Create worker pool for concurrent execution
	workerPool := make(chan struct{}, DefaultTaskConcurrency)
	var wg sync.WaitGroup

	// Process each image to each destination
	for _, imageRef := range images {
		for _, dstServer := range task.DstServers {
			plan := e.planCopy(res, imageRef, dstServer)
			wg.Add(1)
			go func(plan copyPlan) {
				defer wg.Done()
				workerPool <- struct{}{}
				defer func() { <-workerPool }()

				// 「running 起点也回报一次」：占用 worker 槽位后登记在途
				// 单元（target 为目标镜像完整引用）；计划失败无 target 可报。
				if plan.target != "" {
					prog.BeginPair(plan.target)
				}
				copyResult := e.executeCopy(ctx, res, plan, task, copySession)
				if plan.target != "" {
					prog.EndPair(plan.target)
				}
				// 成功 / 跳过（digest 相同）/ platform_skip 计入完成数。
				if copyResult.Success {
					prog.PairDone()
				}

				e.logger.Infof("image sync result: source=%s destination=%s skipped=%t platform_skip=%t success=%t error=%s",
					copyResult.Source, copyResult.Destination, copyResult.Skipped, copyResult.PlatformSkip, copyResult.Success, copyResult.Error)

				resultsChan <- copyResult
			}(plan)
		}
	}

	// Wait for all operations to complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	for item := range resultsChan {
		result.Results = append(result.Results, item)
		result.TransferredBytes += item.TransferredBytes
		if item.Skipped || item.PlatformSkip {
			// platform_skip 在 run 级并入 skipped_count；item 级经
			// PlatformResults / detail 保留可分。
			result.SkippedCount++
		} else if item.Success {
			result.SuccessCount++
		} else {
			result.FailureCount++
		}
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(startTime)

	// Log summary
	e.logger.Infof("oci task execution completed: total=%d success=%d skipped=%d failure=%d duration_ms=%d",
		result.TotalImages, result.SuccessCount, result.SkippedCount, result.FailureCount, result.Duration.Milliseconds())

	return result, nil
}

// parseSrcImages parses source images and expands comma-separated tags
func (e *TaskExecutor) parseSrcImages(srcImages []string) ([]string, error) {
	refs := image.ExpandSrcImages(srcImages)
	images := make([]string, 0, len(refs))
	for _, ref := range refs {
		value := ref.Repo + ":" + ref.Tag
		if _, err := name.NewTag(value); err != nil {
			return nil, fmt.Errorf("invalid source image reference")
		}
		images = append(images, value)
	}

	e.logger.Infof("parsed source image list: input=%v parsed=%v", srcImages, images)

	return images, nil
}

// planCopy 预解析一个 (image, dst) 对：目标 registry 配置、源 registry
// 凭据与目标镜像完整引用（BuildDestinationPath 变换后的 dst repo:tag）。
// 计划失败（目标未配置 / 引用非法）产出带 planErr 的退化计划，复制阶段
// 直接映射为失败结果。
func (e *TaskExecutor) planCopy(res registry.Resolver, imageRef, dstServerName string) copyPlan {
	plan := copyPlan{
		sourceRef: imageRef,
		dstName:   dstServerName,
	}

	// Get destination registry config
	dstRegConfig := res.RegistryByName(dstServerName)
	if dstRegConfig == nil {
		plan.planErr = fmt.Sprintf("destination server '%s' not found", dstServerName)
		return plan
	}

	// 计算 dstRegistry，与目标明细中的逻辑保持一致
	dstRegistry := dstRegConfig.Registry
	if dstRegConfig.Namespace != "" {
		dstRegistry = dstRegConfig.Registry + "/" + dstRegConfig.Namespace
	}
	plan.dstPath = dstRegistry

	// Parse the source image reference
	sourceImageRef := image.ParseReference(imageRef)
	if sourceImageRef == nil {
		plan.planErr = fmt.Sprintf("invalid image reference: %s", imageRef)
		return plan
	}
	plan.sourceReg = sourceImageRef.Registry

	// Construct full repo names for ORAS
	sourceFullRepoName := sourceImageRef.FullRepoName()

	// Find source registry config by exact hostname match
	// （未配置的公共 registry 由 resolver 返回匿名零值配置：
	// 匿名访问、验证 TLS——零值即安全默认）
	sourceRegistryConfig := res.RegistryByHost(sourceImageRef.Registry)

	// 构建目标镜像名称
	destFullRepoName := buildDestinationPath(sourceFullRepoName, sourceRegistryConfig, dstRegConfig)

	// Update target with actual destination path
	plan.target = destFullRepoName + ":" + sourceImageRef.Tag

	// Prepare source and destination image structures
	plan.src = &image.SrcImage{
		Registry: &image.RegistryInfo{
			Username:           sourceRegistryConfig.Username,
			Password:           sourceRegistryConfig.Password,
			PlainHTTP:          sourceRegistryConfig.PlainHTTP,
			InsecureSkipVerify: sourceRegistryConfig.InsecureSkipVerify,
		},
		Namespace: sourceImageRef.Namespace,
		RepoName:  sourceFullRepoName,
		Tag:       sourceImageRef.Tag,
	}

	plan.dst = &image.DstImage{
		Registry: &image.RegistryInfo{
			Username:           dstRegConfig.Username,
			Password:           dstRegConfig.Password,
			PlainHTTP:          dstRegConfig.PlainHTTP,
			InsecureSkipVerify: dstRegConfig.InsecureSkipVerify,
		},
		Namespace: sourceImageRef.Namespace,
		RepoName:  destFullRepoName,
		Tag:       sourceImageRef.Tag,
	}

	return plan
}

// executeCopy performs a single image copy operation（源端 mirror 在配置了
// 的情况下直接使用）
func (e *TaskExecutor) executeCopy(
	ctx context.Context,
	res registry.Resolver,
	plan copyPlan,
	task TaskConfig,
	copySession *registry.CopySession,
) CopyResult {
	startTime := time.Now()
	result := CopyResult{
		Source:      plan.sourceRef,
		StartTime:   startTime,
		DstRegistry: plan.dstPath,
	}
	if plan.target != "" {
		result.Destination = plan.target
	} else {
		result.Destination = fmt.Sprintf("%s/%s", plan.dstName, plan.sourceRef)
	}

	if plan.planErr != "" {
		result.Success = false
		result.Error = plan.planErr
		e.logger.Errorf("image sync failed: %s source=%s destination=%s", plan.planErr, plan.sourceRef, result.Destination)
		return result
	}

	// Execute the copy operation
	copyResult, err := e.client.CopyImage(ctx, plan.src, plan.dst, res, task.Platforms, copySession)
	if err != nil {
		result.Success = false
		result.Error = fmt.Sprintf("failed to copy image: %v", err)
		e.logger.Errorf("image sync failed: source=%s destination=%s error=%v", plan.sourceRef, result.Destination, err)
		return result
	}

	// Map the CopyResult to our result
	result.Success = copyResult.Success
	result.Skipped = copyResult.Skipped
	result.PlatformSkip = copyResult.PlatformSkip
	result.PlatformResults = copyResult.PlatformResults
	result.TransferredBytes = copyResult.TransferredBytes

	if copyResult.PlatformSkip {
		// Platform 不存在，跳过同步
		e.logger.Infof("image platforms not present, skipping sync: source=%s platforms=%v destination=%s duration_ms=%d",
			plan.sourceRef, copyResult.Platforms, result.Destination, time.Since(startTime).Milliseconds())
	} else if copyResult.Skipped {
		e.logger.Infof("image already present, skipping sync: source=%s destination=%s duration_ms=%d",
			plan.sourceRef, result.Destination, time.Since(startTime).Milliseconds())
	} else if !copyResult.Success {
		result.Error = copyResult.Message
		e.logger.Errorf("image sync failed: source=%s destination=%s error=%s",
			plan.sourceRef, result.Destination, copyResult.Message)
	} else {
		result.Destination = copyResult.DstRef
		e.logger.Infof("image sync done: source=%s platforms=%v destination=%s duration_ms=%d transferred_bytes=%d",
			plan.sourceRef, copyResult.Platforms, result.Destination,
			time.Since(startTime).Milliseconds(), copyResult.TransferredBytes)
	}

	result.Duration = time.Since(startTime).Milliseconds()
	result.EndTime = time.Now()

	return result
}

// findStripPrefix 在 stripRegistries 中找到 sourceFullRepoName 的最长匹配前缀。
// 匹配条件：sourceFullRepoName 以 entry + "/" 开头。
// 返回匹配的前缀（不含末尾 /），无匹配返回空字符串。
func findStripPrefix(sourceFullRepoName string, stripRegistries []string) string {
	var bestMatch string
	for _, entry := range stripRegistries {
		if strings.HasPrefix(sourceFullRepoName, entry+"/") && len(entry) > len(bestMatch) {
			bestMatch = entry
		}
	}
	return bestMatch
}

// destinationBase 返回目标 registry 的基础路径（registry 主机名 + 可选 namespace）。
// 所有目标路径转换分支都基于它构建，保证 namespace 处理一致。
func destinationBase(dstRegConfig *registry.RegistryConfig) string {
	if dstRegConfig.Namespace != "" {
		return dstRegConfig.Registry + "/" + dstRegConfig.Namespace
	}
	return dstRegConfig.Registry
}

// buildDestinationPath 根据源镜像引用和目标 registry 配置，构建目标镜像的完整仓库路径
func buildDestinationPath(
	sourceFullRepoName string,
	sourceRegistryConfig *registry.RegistryConfig,
	dstRegConfig *registry.RegistryConfig,
) string {
	dstBase := destinationBase(dstRegConfig)
	srcNeedsDecode := needsPathTransformDecode(sourceRegistryConfig)
	dstNeedsEncode := needsPathTransformEncode(dstRegConfig)

	if srcNeedsDecode {
		// 场景1：从有 path_transform 的源 registry 解码
		separator := sourceRegistryConfig.PathTransform.Separator
		srcPrefix := destinationBase(sourceRegistryConfig) + "/"
		repoPath := strings.TrimPrefix(sourceFullRepoName, srcPrefix)
		decodedPath := transformPathForDecode(repoPath, separator)

		// 解码后得到真实镜像路径，如果目标也需要编码则继续处理
		if dstNeedsEncode {
			dstSep := dstRegConfig.PathTransform.Separator
			encodedPath := transformPathForEncode(decodedPath, dstSep)
			return dstBase + "/" + encodedPath
		}

		return dstBase + "/" + decodedPath
	}

	if dstNeedsEncode {
		// 场景2：编码到有 path_transform 的目标 registry
		separator := dstRegConfig.PathTransform.Separator

		var pathToEncode string
		if stripPrefix := findStripPrefix(sourceFullRepoName, dstRegConfig.StripRegistries); stripPrefix != "" {
			// 匹配到 strip_registries 前缀，移除后编码剩余路径
			pathToEncode = strings.TrimPrefix(sourceFullRepoName, stripPrefix+"/")
		} else {
			// 未匹配，保留完整源路径（含 registry 前缀）
			pathToEncode = sourceFullRepoName
		}

		encodedPath := transformPathForEncode(pathToEncode, separator)
		return dstBase + "/" + encodedPath
	}

	if stripPrefix := findStripPrefix(sourceFullRepoName, dstRegConfig.StripRegistries); stripPrefix != "" {
		// 场景3：目标配置了 strip_registries（无 path_transform 编码），移除匹配的源 registry 前缀
		pathAfterStrip := strings.TrimPrefix(sourceFullRepoName, stripPrefix+"/")
		return dstBase + "/" + pathAfterStrip
	}

	// 场景4（默认）：保留源 registry 前缀在目标路径中
	return dstBase + "/" + sourceFullRepoName
}

// BuildDestinationPath constructs the destination path for an image reference
func BuildDestinationPath(imageRef string, srcRegistries []registry.RegistryConfig, dstRegConfig registry.RegistryConfig) string {
	sourceImageRef := image.ParseReference(imageRef)
	if sourceImageRef == nil {
		return ""
	}

	sourceFullRepoName := sourceImageRef.FullRepoName()

	var sourceRegistryConfig *registry.RegistryConfig
	for i := range srcRegistries {
		if srcRegistries[i].Registry == sourceImageRef.Registry {
			sourceRegistryConfig = &srcRegistries[i]
			break
		}
	}

	if sourceRegistryConfig == nil {
		// Default source registry config for public registries
		// （匿名访问、验证 TLS —— 零值即安全默认）
		sourceRegistryConfig = &registry.RegistryConfig{
			Registry: sourceImageRef.Registry,
			Name:     sourceImageRef.Registry,
		}
	}

	destFullRepoName := buildDestinationPath(sourceFullRepoName, sourceRegistryConfig, &dstRegConfig)
	return destFullRepoName + ":" + sourceImageRef.Tag
}

// transformPathForEncode 将 / 替换为 separator，用于同步到有固定 namespace 的 registry
// 例如: lscr.io/linuxserver/onlyoffice -> lscr.io---linuxserver---onlyoffice
func transformPathForEncode(repoPath, separator string) string {
	return strings.ReplaceAll(repoPath, "/", separator)
}

// transformPathForDecode 将镜像路径解码为正常格式
// 将 separator 替换为 /，用于从有固定 namespace 的 registry 同步出来
// 例如: lscr.io---linuxserver---onlyoffice -> lscr.io/linuxserver/onlyoffice
func transformPathForDecode(repoPath, separator string) string {
	return strings.ReplaceAll(repoPath, separator, "/")
}

// needsPathTransformDecode 检查 registry 是否需要进行路径解码
func needsPathTransformDecode(regConfig *registry.RegistryConfig) bool {
	if regConfig == nil || regConfig.PathTransform == nil {
		return false
	}
	mode := regConfig.PathTransform.Mode
	// 默认 mode 为 both
	if mode == "" {
		return true
	}
	return mode == "decode" || mode == "both"
}

// needsPathTransformEncode 检查 registry 是否需要进行路径编码
func needsPathTransformEncode(regConfig *registry.RegistryConfig) bool {
	if regConfig == nil || regConfig.PathTransform == nil {
		return false
	}
	mode := regConfig.PathTransform.Mode
	// 默认 mode 为 both
	if mode == "" {
		return true
	}
	return mode == "encode" || mode == "both"
}
