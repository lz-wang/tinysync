package syncjob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"tinysync/internal/source"
)

// maxScanDepth 是递归扫描的最大目录深度，防御异常服务器构造的循环树。
const maxScanDepth = 64

// remoteRelPath 计算 logical path 相对 RemoteRoot 的相对路径：
// / 分隔、无前导 /；仅当 RemoteRoot 本身是 Source 根（"/"）时，
// logical path "/" 才映射为 "."。非根 RemoteRoot 之下的 "/" 表示
// Source 根，落在边界之外，返回 ErrInvalid（防御异常服务器把
// Source 根混入子树列表，导致扫描越出 Job 边界）。
func remoteRelPath(remoteRoot, logicalPath string) (string, error) {
	root := path.Clean("/" + remoteRoot)
	if root == "/" {
		root = ""
	}
	if logicalPath == "/" || logicalPath == "" {
		if root == "" {
			return ".", nil
		}
		return "", fmt.Errorf("%w: remote path %q escapes remote root %q", ErrInvalid, logicalPath, remoteRoot)
	}
	if !strings.HasPrefix(logicalPath, "/") {
		return "", fmt.Errorf("%w: remote path %q is not absolute", ErrInvalid, logicalPath)
	}
	cleaned := path.Clean(logicalPath)
	if root != "" {
		if cleaned != root && !strings.HasPrefix(cleaned, root+"/") {
			return "", fmt.Errorf("%w: remote path %q escapes remote root %q", ErrInvalid, logicalPath, root)
		}
		cleaned = strings.TrimPrefix(cleaned, root)
	}
	return strings.TrimPrefix(cleaned, "/"), nil
}

// ScanRemote 递归扫描 RemoteRoot 子树，返回全部文件（不含目录）。
// 任一层 List 失败都整体失败并返回错误——Mirror 的删除授权依赖
// 完整快照，本函数绝不返回部分结果。
func ScanRemote(ctx context.Context, remote source.Remote, remoteRoot string) ([]source.FileInfo, error) {
	var files []source.FileInfo
	if err := scanDir(ctx, remote, remoteRoot, remoteRoot, 0, &files); err != nil {
		return nil, err
	}
	return files, nil
}

// scanDir 递归列出一个远端目录；dirLogical 是目录 logical path，
// dirRel 是其相对 RemoteRoot 的路径。
func scanDir(ctx context.Context, remote source.Remote, remoteRoot, dirLogical string, depth int, files *[]source.FileInfo) error {
	if depth > maxScanDepth {
		return fmt.Errorf("scan %s: exceeded max depth %d", dirLogical, maxScanDepth)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := remote.List(ctx, dirLogical)
	if err != nil {
		return fmt.Errorf("scan %s: %w", dirLogical, err)
	}
	for _, entry := range entries {
		// 每个条目都验证落在 RemoteRoot 之内（防御异常服务器 href）。
		if _, err := remoteRelPath(remoteRoot, entry.Path); err != nil {
			return err
		}
		if entry.IsDir {
			if err := scanDir(ctx, remote, remoteRoot, entry.Path, depth+1, files); err != nil {
				return err
			}
			continue
		}
		*files = append(*files, entry)
	}
	return nil
}

// resolveLocalTarget 把 / 分隔的相对路径安全解析到 LocalRoot 之下的
// 绝对路径。注入的 .. 序列与绝对路径一律拒绝：先拒绝显式逃逸分量，
// 再用 Join + Rel 双向验证归一化结果不越过 LocalRoot。
func resolveLocalTarget(localRoot, relPath string) (string, error) {
	if relPath == "." || relPath == "" {
		return "", fmt.Errorf("%w: empty local relative path", ErrInvalid)
	}
	slash := relPath
	if filepath.Separator != '/' {
		slash = strings.ReplaceAll(relPath, string(filepath.Separator), "/")
	}
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: local relative path %q escapes local root", ErrInvalid, relPath)
		}
	}
	if path.IsAbs(slash) {
		return "", fmt.Errorf("%w: local relative path %q must be relative", ErrInvalid, relPath)
	}
	target := filepath.Join(localRoot, filepath.FromSlash(slash))
	relBack, err := filepath.Rel(localRoot, target)
	if err != nil || relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: local relative path %q escapes local root %s", ErrInvalid, relPath, localRoot)
	}
	return target, nil
}

// rejectSymlinkComponents 从 LocalRoot 向下逐级 Lstat 目标的既有路径
// 组件，任何 symlink（含指向 root 内部或 root 外部）一律拒绝；目标
// 自身是 symlink 同样拒绝（不得覆盖）。尚不存在的组件放行。
// 抵御并发 symlink 替换的 openat 模型属 v0.9 hardening。
func rejectSymlinkComponents(localRoot, target string) error {
	rel, err := filepath.Rel(localRoot, target)
	if err != nil {
		return fmt.Errorf("%w: resolve %s under %s: %w", ErrInvalid, target, localRoot, err)
	}
	cur := localRoot
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: inspect %s: %w", ErrInvalid, cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symlink; refusing to traverse or overwrite", ErrInvalid, cur)
		}
	}
	return nil
}
