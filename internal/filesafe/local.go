package filesafe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalExistingDir 返回已存在目录的 canonical native 绝对路径。
func CanonicalExistingDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("directory path is empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	return resolved, nil
}

// CanonicalDirectoryCandidate 不创建目录，解析候选路径及最近的已存在目录。
// 已有前缀解析 symlink，缺失的后缀原样拼回；文件、悬空链接与访问错误均拒绝。
func CanonicalDirectoryCandidate(p string) (candidate, ancestor string, err error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", "", fmt.Errorf("directory path is empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", "", err
	}
	current := abs
	for {
		if _, err := os.Lstat(current); err == nil {
			ancestor, err = CanonicalExistingDir(current)
			if err != nil {
				return "", "", err
			}
			suffix, err := filepath.Rel(current, abs)
			if err != nil {
				return "", "", err
			}
			return filepath.Join(ancestor, suffix), ancestor, nil
		} else if !os.IsNotExist(err) {
			return "", "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", fmt.Errorf("no existing directory ancestor for %s", p)
		}
		current = parent
	}
}

// DirectoryCandidatesOverlap 支持尚未创建或暂时缺失的目录。已有路径用
// 文件系统身份比较；两者都缺失时只比较同一物理祖先下的后缀，并保守
// 拒绝后缀的大小写别名，避免猜测未知卷的大小写规则。
func DirectoryCandidatesOverlap(a, b string) (bool, error) {
	a, aAncestor, err := CanonicalDirectoryCandidate(a)
	if err != nil {
		return false, fmt.Errorf("resolve directory candidate: %w", err)
	}
	b, bAncestor, err := CanonicalDirectoryCandidate(b)
	if err != nil {
		return false, fmt.Errorf("resolve directory candidate: %w", err)
	}
	if a == aAncestor && b == bAncestor {
		return ExistingDirectoriesOverlap(a, b)
	}
	aInfo, err := os.Stat(aAncestor)
	if err != nil {
		return false, err
	}
	bInfo, err := os.Stat(bAncestor)
	if err != nil {
		return false, err
	}
	if a == aAncestor {
		return ancestorMatches(bAncestor, aInfo)
	}
	if b == bAncestor {
		return ancestorMatches(aAncestor, bInfo)
	}
	if !os.SameFile(aInfo, bInfo) {
		return false, nil
	}
	aSuffix, err := filepath.Rel(aAncestor, a)
	if err != nil {
		return false, err
	}
	bSuffix, err := filepath.Rel(bAncestor, b)
	if err != nil {
		return false, err
	}
	return sameOrUnder(strings.ToLower(aSuffix), strings.ToLower(bSuffix)) ||
		sameOrUnder(strings.ToLower(bSuffix), strings.ToLower(aSuffix)), nil
}

// ExistingDirectoriesOverlap 根据文件系统身份判断已存在目录是否相同或
// 互相包含，不假设操作系统或卷的大小写规则。解析符号链接后沿祖先链
// 比较 SameFile；任何解析或 Stat 失败都返回错误，不能作为不重叠处理。
func ExistingDirectoriesOverlap(a, b string) (bool, error) {
	a, err := CanonicalExistingDir(a)
	if err != nil {
		return false, fmt.Errorf("resolve directory: %w", err)
	}
	b, err = CanonicalExistingDir(b)
	if err != nil {
		return false, fmt.Errorf("resolve directory: %w", err)
	}
	aInfo, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if overlap, err := ancestorMatches(a, bInfo); err != nil || overlap {
		return overlap, err
	}
	return ancestorMatches(b, aInfo)
}

func ancestorMatches(dir string, target os.FileInfo) (bool, error) {
	for {
		info, err := os.Stat(dir)
		if err != nil {
			return false, fmt.Errorf("stat directory ancestor %s: %w", dir, err)
		}
		if os.SameFile(info, target) {
			return true, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}

func sameOrUnder(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

// ResolveNoSymlink 解析普通文件或目录，拒绝整条 native 路径上的 symlink
// 和特殊文件，包含已保存 root 的父组件。root 必须是 canonical 绝对路径。
// 与其他浏览原语不同，即使 symlink 仍在 root 内也拒绝。
func ResolveNoSymlink(root, logicalPath string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", nil, fmt.Errorf("root must be a canonical absolute path")
	}
	target, err := ResolveWithinRoot(root, logicalPath)
	if err != nil {
		return "", nil, err
	}
	volume := filepath.VolumeName(target)
	current := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(target, current), string(filepath.Separator))
	info, err := os.Lstat(current)
	if err != nil {
		return "", nil, err
	}
	for i, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return "", nil, fmt.Errorf("%w: %s is a symlink or special file", ErrNotRegularFile, current)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", nil, fmt.Errorf("%s is not a directory", current)
		}
	}
	return target, info, nil
}
