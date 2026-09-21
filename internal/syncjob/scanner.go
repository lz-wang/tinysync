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
// remote 实现 source.TreeScanner 时走协议原生 fast path（每目录一次
// 枚举 / S3 flat prefix 扫描），否则回退 List 递归；两条路径的全部
// 条目都经过同一 collector，安全规则不因 fast path 而旁路。任一层
// 枚举失败都整体失败并返回错误——Mirror 的删除授权依赖完整快照，
// 本函数绝不返回部分结果。
func ScanRemote(ctx context.Context, remote source.Remote, remoteRoot string) ([]source.FileInfo, error) {
	c := newScanCollector(remoteRoot)
	if scanner, ok := remote.(source.TreeScanner); ok {
		if err := scanner.ScanTree(ctx, remoteRoot, c.visit); err != nil {
			return nil, err
		}
		return c.files, nil
	}
	if err := c.walkList(ctx, remote, remoteRoot); err != nil {
		return nil, err
	}
	return c.files, nil
}

// scanCollector 集中承载扫描快照的构建与全部安全规则：跨协议
// logical path 校验、RemoteRoot 边界、最大深度、file/dir collision
// 与「只把文件加入快照」。TreeScanner fast path 与 List fallback
// 不得绕开其中任何一条。
type scanCollector struct {
	remoteRoot string
	// seen 记录已出现的 logical path 及其类型：同一 path 以不同类型
	// 再次出现（如 S3 一页报文件、推导虚拟目录报目录）时整体失败，
	// 保持「file/dir collision fail-fast」的完整快照语义。
	seen  map[string]bool
	files []source.FileInfo
}

func newScanCollector(remoteRoot string) *scanCollector {
	return &scanCollector{
		remoteRoot: remoteRoot,
		seen:       make(map[string]bool),
	}
}

// visit 处理一条远端条目（文件或目录）。
func (c *scanCollector) visit(entry source.FileInfo) error {
	// 跨协议 logical path 统一校验：拒绝反斜杠、NUL、dot segments
	// 与重复分隔符（防御异常远端把不可移植 key 送进本地 filepath）。
	if err := source.ValidateLogicalPath(entry.Path); err != nil {
		return err
	}
	// 每个条目都验证落在 RemoteRoot 之内（防御异常服务器 href）。
	rel, err := remoteRelPath(c.remoteRoot, entry.Path)
	if err != nil {
		return err
	}
	// 深度上限按相对 RemoteRoot 的目录分量数计（与旧 scanDir 的递归
	// depth 语义一致），防御异常服务器构造的循环树。
	if relDepth(rel) > maxScanDepth {
		return fmt.Errorf("scan %s: exceeded max depth %d", entry.Path, maxScanDepth)
	}
	if prev, ok := c.seen[entry.Path]; ok && prev != entry.IsDir {
		return fmt.Errorf("%w: %q is both a file and a directory in remote listing", ErrInvalid, entry.Path)
	}
	c.seen[entry.Path] = entry.IsDir
	// 快照只含文件；目录条目仅用于安全语义（collision / 深度）。
	if !entry.IsDir {
		c.files = append(c.files, entry)
	}
	return nil
}

// walkList 是 List fallback：递归列出一个远端目录。List 按分页契约
// 逐页消费：处理一页后以 NextCursor 续拉，空 NextCursor 表示该层
// 枚举结束。
func (c *scanCollector) walkList(ctx context.Context, remote source.Remote, dirLogical string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cursor := ""
	for {
		page, err := remote.List(ctx, dirLogical, source.ListOptions{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("scan %s: %w", dirLogical, err)
		}
		for _, entry := range page.Entries {
			if err := c.visit(entry); err != nil {
				return err
			}
			if entry.IsDir {
				if err := c.walkList(ctx, remote, entry.Path); err != nil {
					return err
				}
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

// relDepth 计算相对路径的目录深度：root 自身（"."）为 0，每级子
// 目录 +1。
func relDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
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
