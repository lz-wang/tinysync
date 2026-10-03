package local

import (
	"context"
	"fmt"
	"os"
	"strings"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
)

func (r *Remote) resolve(ctx context.Context, logical string) (string, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if err := source.ValidateLogicalPath(logical); err != nil {
		return "", nil, err
	}
	native, info, err := filesafe.ResolveNoSymlink(r.root, logical)
	if err != nil {
		return "", nil, fmt.Errorf("resolve local %s: %w", logical, err)
	}
	return native, info, nil
}

func fileInfo(logical string, info os.FileInfo) source.FileInfo {
	return source.FileInfo{Path: logical, IsDir: info.IsDir(), Fingerprint: source.Fingerprint{Size: info.Size(), ModifiedAt: info.ModTime()}}
}

// 整层枚举后逐条重验：任何无效路径、symlink、特殊文件或访问错误整层失败。
func (r *Remote) readDir(ctx context.Context, logical string) ([]source.FileInfo, error) {
	native, info, err := r.resolve(ctx, logical)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("local %s is not a directory", logical)
	}
	entries, err := os.ReadDir(native)
	if err != nil {
		return nil, fmt.Errorf("read local directory %s: %w", logical, err)
	}
	result := make([]source.FileInfo, 0, len(entries))
	for _, entry := range entries {
		child := strings.TrimSuffix(logical, "/") + "/" + entry.Name()
		// 验证原始条目名称，不先清理路径而掩盖非法条目。
		if err := source.ValidateLogicalPath(child); err != nil {
			return nil, err
		}
		_, info, err := r.resolve(ctx, child)
		if err != nil {
			return nil, err
		}
		result = append(result, fileInfo(child, info))
	}
	return result, nil
}
