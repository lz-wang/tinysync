package syncjob

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"tinysync/internal/filesafe"
	"tinysync/internal/job"
	"tinysync/internal/logging"
	"tinysync/internal/source"
)

// SourceService 是 File 执行器依赖的 Source 查询能力，解耦具体实现。
type SourceService interface {
	// Get 按 ID 读取 Source；不存在时返回 source.ErrNotFound。
	Get(ctx context.Context, id string) (source.Source, error)
}

// FileExecutor 是 file 任务的领域执行器（job.ChangeAwareExecutor）：
// 校验 File 配置（Source 存在、pattern、LocalRoot 归属保护与映射
// 合法性），并在 mapping 变更时重置 managed metadata、回收孤儿
// partial。运行执行（Run）由统一 Runner 经引擎调用，见 runner.go。
type FileExecutor struct {
	sources SourceService
	repo    job.Repository
	// dataDir 是 TinySync 数据目录，同步 LocalRoot 不得与其重叠。
	dataDir string
	// Now 返回当前时间；默认 UTC time.Now，测试可注入固定时钟。
	Now func() time.Time
}

// NewFileExecutor 构造 File 执行器；dataDir 用于归属保护检查。
func NewFileExecutor(sources SourceService, repo job.Repository, dataDir string) *FileExecutor {
	return &FileExecutor{
		sources: sources,
		repo:    repo,
		dataDir: dataDir,
		Now:     func() time.Time { return time.Now().UTC() },
	}
}

// Validate 实现 job.Executor：创建（jobID 为空）与更新共用，完整移植
// 原 Service 的配置入口校验——非法 pattern、缺失 Source、非法映射与
// LocalRoot 重叠直接拒绝，不留到首次 Run 才静默失败。创建路径对不存在
// 的 LocalRoot 做 MkdirAll；更新路径不创建目录（避免编辑任务时误填
// 路径产生目录副作用，要求目标已存在）。返回归一后的落库配置：
// RemoteRoot 归一、LocalRoot canonical、mode 缺省 copy、patterns 非 nil。
func (e *FileExecutor) Validate(ctx context.Context, jobID string, cfg json.RawMessage) (json.RawMessage, error) {
	config, err := ParseFileConfig(cfg)
	if err != nil {
		return nil, err
	}
	if !config.ModeOf().Valid() {
		return nil, fmt.Errorf("%w: mode %q must be copy or mirror", ErrInvalid, config.Mode)
	}
	src, err := e.sources.Get(ctx, config.SourceID)
	if err != nil {
		return nil, err
	}
	remoteRoot, err := normalizeRemoteRoot(config.RemoteRoot)
	if err != nil {
		return nil, err
	}
	// 先解析已有前缀并预检，拒绝的映射不因 MkdirAll 留下新目录。
	candidate, _, err := filesafe.CanonicalDirectoryCandidate(config.LocalRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve local root candidate: %w", ErrInvalid, err)
	}
	if err := e.checkRootAvailable(ctx, candidate, jobID); err != nil {
		return nil, err
	}
	if err := source.ValidateJobMappingCandidate(src, remoteRoot, candidate); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	localRoot, err := canonicalLocalRoot(candidate, jobID == "")
	if err != nil {
		return nil, err
	}
	if err := e.checkRootAvailable(ctx, localRoot, jobID); err != nil {
		return nil, err
	}
	if err := source.ValidateJobMapping(src, remoteRoot, localRoot); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	normalized, err := MarshalFileConfig(config.SourceID, remoteRoot, localRoot, config.ModeOf(), config.Include, config.Exclude)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

// UpdateHooks 实现 job.ChangeAwareExecutor：mapping（source /
// remote_root / local_root）变更时返回随更新同事务执行的管理 metadata
// 重置钩子——分开执行存在半成功状态（如更新撞 name 冲突而 metadata
// 已清空，任务将永久失去对本地文件的管理关系）。
func (e *FileExecutor) UpdateHooks(ctx context.Context, before, after job.Job) ([]job.TxHook, error) {
	beforeCfg, err := ParseFileConfig(before.Config)
	if err != nil {
		return nil, nil
	}
	afterCfg, err := ParseFileConfig(after.Config)
	if err != nil {
		return nil, nil
	}
	if beforeCfg.SourceID == afterCfg.SourceID &&
		strings.TrimSpace(beforeCfg.RemoteRoot) == strings.TrimSpace(afterCfg.RemoteRoot) &&
		beforeCfg.LocalRoot == afterCfg.LocalRoot {
		return nil, nil
	}
	return []job.TxHook{func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM managed_files WHERE job_id = ?", after.ID); err != nil {
			return fmt.Errorf("reset managed files of job %s: %w", after.ID, err)
		}
		return nil
	}}, nil
}

// AfterUpdate 实现 job.ChangeAwareExecutor：LocalRoot 变更后旧 root
// 随 mutation 退出配置面，其下的传输中间文件立刻成为启动清理永远
// 扫不到的孤儿，提交后立即回收。
func (e *FileExecutor) AfterUpdate(ctx context.Context, before, after job.Job) {
	beforeCfg, errBefore := ParseFileConfig(before.Config)
	afterCfg, errAfter := ParseFileConfig(after.Config)
	if errBefore != nil || errAfter != nil || beforeCfg.LocalRoot == afterCfg.LocalRoot {
		return
	}
	e.cleanupOrphanedRoot(beforeCfg.LocalRoot)
}

// AfterDelete 实现 job.ChangeAwareExecutor：managed metadata 经 FK
// CASCADE 清理，真实本地文件不受影响；LocalRoot 随删除退出配置面，
// 其下的断点文件将成为永不回收的孤儿（partialRetention 不覆盖
// mapping 变更孤儿，见 ADR 0010），提交后立即回收。
func (e *FileExecutor) AfterDelete(ctx context.Context, j job.Job) {
	cfg, err := ParseFileConfig(j.Config)
	if err != nil {
		return
	}
	e.cleanupOrphanedRoot(cfg.LocalRoot)
}

// cleanupOrphanedRoot 回收退出配置面的 LocalRoot 的传输中间文件。
// DB mutation 已提交，清理失败只记告警（遗留 partial 不被任何任务
// 引用、不参与 Mirror 授权，不影响正确性；扩容修复后可手工删除），
// 不把已成功的 mutation 报告为失败。
func (e *FileExecutor) cleanupOrphanedRoot(root string) {
	if root == "" {
		return
	}
	removed, err := RemoveTransferTemps(root)
	if err != nil {
		logging.Warnf("cleanup orphaned transfer temps in %s: %v", root, err)
		return
	}
	if removed > 0 {
		logging.Infof("orphaned_transfer_temps_removed=%d root=%s", removed, root)
	}
}

// LocalRootOf 读取 file 任务的 LocalRoot；非 file 任务或配置不可解析
// 返回 ErrNotFound。
func (e *FileExecutor) LocalRootOf(ctx context.Context, id string) (string, error) {
	j, err := e.repo.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if j.Kind != job.KindFile {
		return "", fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	cfg, err := ParseFileConfig(j.Config)
	if err != nil {
		return "", err
	}
	return cfg.LocalRoot, nil
}

// LocalRoots 汇总全部 file 任务的去重 LocalRoot，供启动期临时文件
// 清理枚举。
func (e *FileExecutor) LocalRoots(ctx context.Context) ([]string, error) {
	jobs, err := e.repo.ListByKind(ctx, job.KindFile)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(jobs))
	roots := make([]string, 0, len(jobs))
	for _, j := range jobs {
		cfg, err := ParseFileConfig(j.Config)
		if err != nil || cfg.LocalRoot == "" || seen[cfg.LocalRoot] {
			continue
		}
		seen[cfg.LocalRoot] = true
		roots = append(roots, cfg.LocalRoot)
	}
	return roots, nil
}

// normalizeRemoteRoot 把 RemoteRoot 归一为 canonical logical path：
// "/" 或空串为 root，其余必须落在 root 之下且不带尾斜杠。
// 归一化规则与 WebDAV adapter 的 logicalPath 一致（path.Clean、消除 ..）。
func normalizeRemoteRoot(root string) (string, error) {
	cleaned := strings.TrimSuffix(path.Clean("/"+strings.TrimSpace(root)), "/")
	if cleaned == "" {
		return "/", nil
	}
	return cleaned, nil
}

// canonicalLocalRoot 校验 LocalRoot，并归一为 abs + clean + EvalSymlinks 的
// canonical 绝对路径。仅在创建任务时创建不存在的目录，避免编辑任务时
// 因误填路径产生目录副作用。
func canonicalLocalRoot(root string, createIfMissing bool) (string, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return "", fmt.Errorf("%w: local root is required", ErrInvalid)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: resolve local root %s: %w", ErrInvalid, root, err)
	}
	if createIfMissing {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", fmt.Errorf("%w: create local root %s: %w", ErrInvalid, root, err)
		}
	}
	resolved, err := filesafe.CanonicalExistingDir(abs)
	if err != nil {
		return "", fmt.Errorf("%w: resolve local root %s: %w", ErrInvalid, root, err)
	}
	return resolved, nil
}

// checkRootAvailable 断言 canonical localRoot 不与任何其他任务
// （skipID 除外，创建时为空）或 dataDir 重叠：相等、父子包含一律拒绝。
// 跨类型比较 LocalRoot 只对 file 任务有意义：其余类型无本地路径。
func (e *FileExecutor) checkRootAvailable(ctx context.Context, localRoot, skipID string) error {
	jobs, err := e.repo.List(ctx)
	if err != nil {
		return err
	}
	dataDir, err := e.canonicalDataDir()
	if err != nil {
		return err
	}
	overlap, err := filesafe.DirectoryCandidatesOverlap(localRoot, dataDir)
	if err != nil {
		return fmt.Errorf("%w: compare local root with data dir: %w", ErrInvalid, err)
	}
	if overlap {
		return fmt.Errorf("%w: %s overlaps tinysync data dir %s", ErrRootOverlap, localRoot, dataDir)
	}
	for _, j := range jobs {
		if j.ID == skipID || j.Kind != job.KindFile {
			continue
		}
		cfg, err := ParseFileConfig(j.Config)
		if err != nil {
			continue
		}
		overlap, err := filesafe.DirectoryCandidatesOverlap(localRoot, cfg.LocalRoot)
		if err != nil {
			return fmt.Errorf("%w: compare local root with job %q: %w", ErrInvalid, j.Name, err)
		}
		if overlap {
			return fmt.Errorf("%w: %s overlaps %q local root %s", ErrRootOverlap, localRoot, j.Name, cfg.LocalRoot)
		}
	}
	return nil
}

// canonicalDataDir 返回归一化的数据目录绝对路径。
func (e *FileExecutor) canonicalDataDir() (string, error) {
	candidate, _, err := filesafe.CanonicalDirectoryCandidate(e.dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data dir %s: %w", e.dataDir, err)
	}
	return candidate, nil
}

// 编译期接口断言。
var _ job.ChangeAwareExecutor = (*FileExecutor)(nil)
