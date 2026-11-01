package api

import (
	"context"
	"fmt"

	"tinysync/internal/job"
)

// staticFileRoots 是文件 / 共享 API 测试的 LocalRootLookup 桩：
// jobID → 本地根目录。
type staticFileRoots map[string]string

func (m staticFileRoots) LocalRootOf(ctx context.Context, jobID string) (string, error) {
	root, ok := m[jobID]
	if !ok {
		return "", fmt.Errorf("%w: %s", job.ErrNotFound, jobID)
	}
	return root, nil
}
