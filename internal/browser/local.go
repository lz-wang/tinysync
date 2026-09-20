package browser

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

// LocalService 是本地文件浏览的应用服务：唯一 namespace 是 Job——
// job ID 解析为 Job.LocalRoot（创建时已 canonicalize），逻辑路径经
// filesafe 的 root confinement 解析。可以看到 LocalRoot 下真实存在
// 的所有条目（含非 TinySync 管理的文件），managed 标记由
// managed_files 记录推导。
type LocalService struct {
	jobs    *syncjob.Service
	managed syncjob.ManagedRepository
}

// NewLocalService 构造本地浏览服务。
func NewLocalService(jobs *syncjob.Service, managed syncjob.ManagedRepository) *LocalService {
	return &LocalService{jobs: jobs, managed: managed}
}

// validateLogicalPath 校验本地逻辑路径并标记领域错误：基础规则与
// filesafe 共享，错误统一带 ErrInvalid 供 API 层映射 400。
func validateLogicalPath(p string) error {
	if err := filesafe.ValidateLogicalPath(p); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// resolveDir 解析目录逻辑路径供列目录使用：目标必须存在、是目录、
// 自身非 symlink（symlink 显示不可进入——进入会泄露 root 外目录的
// 文件名列表）；全链 EvalSymlinks 解析后必须仍在 root 内，防御父
// 目录组件中的 symlink 逃逸。
func resolveDir(root, logicalPath string) (string, error) {
	// root 先归一为 canonical 形态（与 ResolveRegularFile 同一防御），
	// 避免未归一 root 与解析结果的 Rel 比较误判逃逸。
	if canonicalRoot, err := filepath.EvalSymlinks(root); err != nil {
		return "", err
	} else if canonicalRoot != root {
		root = canonicalRoot
	}
	dirAbs, err := filesafe.ResolveWithinRoot(root, logicalPath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(dirAbs)
	if err != nil {
		return "", err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s is a symlink", ErrInvalid, logicalPath)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrInvalid, logicalPath)
	}
	resolved, err := filepath.EvalSymlinks(dirAbs)
	if err != nil {
		return "", err
	}
	if resolved != dirAbs {
		rel, relErr := filepath.Rel(root, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%w: logical path %q escapes root %s via symlink", filesafe.ErrEscape, logicalPath, root)
		}
	}
	return resolved, nil
}

// localRoot 解析 Job 并返回 canonical LocalRoot；Job 不存在映射为
// ErrNotFound。
func (s *LocalService) localRoot(ctx context.Context, jobID string) (string, error) {
	job, err := s.jobs.Get(ctx, jobID)
	if err != nil {
		if errors.Is(err, syncjob.ErrNotFound) {
			return "", fmt.Errorf("%w: job not found", ErrNotFound)
		}
		return "", err
	}
	return job.LocalRoot, nil
}

// managedRelPaths 返回该 Job 全部 managed 记录的本地相对路径集合。
// 存储故障原样返回错误（与「没有 managed 文件」区分开）。
func (s *LocalService) managedRelPaths(ctx context.Context, jobID string) (map[string]bool, error) {
	files, err := s.managed.ListByJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[f.LocalRelPath] = true
	}
	return set, nil
}

// List 列出 Job.LocalRoot 下的一层目录：os.ReadDir 完整读取单层后
// 位置切片分页（与 WebDAV / SFTP 的 adapter 边界分页同语义：分页
// 约束返回条目数，本地目录枚举本身是一次性读取）。条目类型按
// Lstat 语义分类，symlink 只显示不跟随。
func (s *LocalService) List(ctx context.Context, jobID, logicalDir string, opts source.ListOptions) ([]Entry, string, error) {
	if err := validateLogicalPath(logicalDir); err != nil {
		return nil, "", err
	}
	root, err := s.localRoot(ctx, jobID)
	if err != nil {
		return nil, "", err
	}
	managed, err := s.managedRelPaths(ctx, jobID)
	if err != nil {
		return nil, "", err
	}
	dirAbs, err := resolveDir(root, logicalDir)
	if err != nil {
		return nil, "", err
	}
	dirEntries, err := os.ReadDir(dirAbs)
	if err != nil {
		return nil, "", err
	}
	all := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		entryPath := "/" + de.Name()
		if logicalDir != "/" {
			entryPath = logicalDir + "/" + de.Name()
		}
		info, infoErr := de.Info()
		entry := Entry{
			Path:    entryPath,
			Name:    de.Name(),
			Managed: boolPtr(managed[strings.TrimPrefix(entryPath, "/")]),
		}
		switch {
		case infoErr != nil:
			// Info 失败（Windows 上的特殊文件等）：仍显示，但不可操作。
			entry.Kind = KindOther
		case info.Mode()&fs.ModeSymlink != 0:
			entry.Kind = KindSymlink
		case info.IsDir():
			entry.Kind = KindDirectory
		case info.Mode().IsRegular():
			entry.Kind = KindFile
			entry.Size = info.Size()
			entry.ModifiedAt = optionalTime(info.ModTime())
		default:
			entry.Kind = KindOther
		}
		all = append(all, entry)
	}
	return pageOf(all, opts)
}

// Stat 读取单个本地路径的元信息（Lstat 语义：symlink 原样呈现）。
// 父目录组件经 LstatWithinRoot 加固：symlink 可以作为最终组件显示，
// 但不能作为中间节点越出 root 泄露外部文件的元信息。
func (s *LocalService) Stat(ctx context.Context, jobID, logicalPath string) (Entry, error) {
	if err := validateLogicalPath(logicalPath); err != nil {
		return Entry{}, err
	}
	root, err := s.localRoot(ctx, jobID)
	if err != nil {
		return Entry{}, err
	}
	managed, err := s.managedRelPaths(ctx, jobID)
	if err != nil {
		return Entry{}, err
	}
	return statWithin(root, managed, logicalPath)
}

// statWithin 在已知 root 与 managed 集合内读取条目元信息：Stat 与
// SearchManaged 共享同一 filesafe 边界与条目构建，搜索不需要为每个
// 候选重复查询 managed 集合。
func statWithin(root string, managed map[string]bool, logicalPath string) (Entry, error) {
	_, info, err := filesafe.LstatWithinRoot(root, logicalPath)
	if err != nil {
		return Entry{}, err
	}
	entry := Entry{
		Path:    logicalPath,
		Name:    entryName(logicalPath),
		Managed: boolPtr(managed[strings.TrimPrefix(logicalPath, "/")]),
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		entry.Kind = KindSymlink
	case info.IsDir():
		entry.Kind = KindDirectory
	case info.Mode().IsRegular():
		entry.Kind = KindFile
		entry.Size = info.Size()
		entry.ModifiedAt = optionalTime(info.ModTime())
	default:
		entry.Kind = KindOther
	}
	return entry, nil
}

// SearchOptions 是 managed 文件搜索的参数。Query 非空；Limit 为 0
// 取默认值（50），负值或超过上限（200）返回 ErrInvalid。
type SearchOptions struct {
	Query string
	Limit int
}

// managed 搜索的分页边界：与列表分页不同，搜索面向 Agent 的
// 「发现前若干条」场景，默认值与上限都更小。
const (
	searchDefaultLimit = 50
	searchMaxLimit     = 200
)

// SearchResult 是 managed 文件搜索的结果。Truncated 为 true 表示
// 匹配记录多于 limit，仅返回前 limit 条。
type SearchResult struct {
	Entries   []Entry
	Truncated bool
}

// SearchManaged 以 Job 为 namespace 搜索已同步（synced）的本地文件：
// 候选来自 managed_files 记录（不扫描任意主机目录），匹配规则为
// LocalRelPath 的大小写不敏感子串，结果按 LocalRelPath 字典序稳定
// 排序。每条返回前经 statWithin 获取当前实际文件状态（与 Stat 同一
// filesafe 边界），本地已不存在的记录跳过，不信任数据库旧 size/mtime。
// 其他文件系统错误原样返回，避免把权限或 I/O 故障伪装成「没有结果」。
func (s *LocalService) SearchManaged(ctx context.Context, jobID string, opts SearchOptions) (SearchResult, error) {
	query := strings.ToLower(strings.TrimSpace(opts.Query))
	if query == "" {
		return SearchResult{}, fmt.Errorf("%w: query must not be empty", ErrInvalid)
	}
	limit := opts.Limit
	if limit == 0 {
		limit = searchDefaultLimit
	}
	if limit < 0 || limit > searchMaxLimit {
		return SearchResult{}, fmt.Errorf("%w: limit must be in 1..%d", ErrInvalid, searchMaxLimit)
	}

	job, err := s.jobs.Get(ctx, jobID)
	if err != nil {
		if errors.Is(err, syncjob.ErrNotFound) {
			return SearchResult{}, fmt.Errorf("%w: job not found", ErrNotFound)
		}
		return SearchResult{}, err
	}
	files, err := s.managed.ListByJob(ctx, jobID)
	if err != nil {
		return SearchResult{}, err
	}

	// synced only + 大小写不敏感子串匹配，按 LocalRelPath 排序保证
	// 截断结果稳定。
	matches := make([]string, 0, len(files))
	for _, f := range files {
		if f.State != syncjob.StateSynced {
			continue
		}
		if strings.Contains(strings.ToLower(f.LocalRelPath), query) {
			matches = append(matches, f.LocalRelPath)
		}
	}
	sort.Strings(matches)

	managedSet := make(map[string]bool, len(matches))
	for _, rel := range matches {
		managedSet[rel] = true
	}
	result := SearchResult{Entries: make([]Entry, 0, min(limit, len(matches)))}
	for _, rel := range matches {
		if ctx.Err() != nil {
			return SearchResult{}, ctx.Err()
		}
		entry, err := statWithin(job.LocalRoot, managedSet, "/"+rel)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return SearchResult{}, err
		}
		// 截断取决于是否存在第 limit+1 条实际可返回的记录；已删除的
		// managed 记录不会错误地令结果标记为 truncated。
		if len(result.Entries) == limit {
			result.Truncated = true
			break
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

// Open 打开本地普通文件供下载 / 共享 serving：ResolveRegularFile
// 拒绝 symlink（无论指向 root 内还是外）并做父目录 symlink 逃逸
// 检查，返回 canonical 路径上打开的文件与元信息。*os.File 是
// ReadSeeker，上层 http.ServeContent 由此获得 Range / HEAD 支持。
func (s *LocalService) Open(ctx context.Context, jobID, logicalPath string) (string, *os.File, os.FileInfo, error) {
	if err := validateLogicalPath(logicalPath); err != nil {
		return "", nil, nil, err
	}
	root, err := s.localRoot(ctx, jobID)
	if err != nil {
		return "", nil, nil, err
	}
	resolved, info, err := filesafe.ResolveRegularFile(root, logicalPath)
	if err != nil {
		return "", nil, nil, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", nil, nil, err
	}
	return path.Base(logicalPath), f, info, nil
}

// pageOf 对已构建的条目切片执行位置分页：cursor 为 opaque offset
// token、EOF 以空 cursor 表达，与 filesafe.PageSlice 同一语义。
func pageOf[T any](entries []T, opts source.ListOptions) ([]T, string, error) {
	offset, err := source.DecodeListOffset(opts.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := source.NormalizeListLimit(opts.Limit)
	if offset >= len(entries) {
		return nil, "", nil
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	next := ""
	if end < len(entries) {
		next = source.EncodeListOffset(end)
	}
	return entries[offset:end], next, nil
}

func boolPtr(b bool) *bool {
	return &b
}
