package local

import (
	"context"
	"fmt"

	"tinysync/internal/source"
)

// ScanTree 用显式 stack DFS 每层只读一次；任何局部失败整体失败。
func (r *Remote) ScanTree(ctx context.Context, root string, visit func(source.FileInfo) error) error {
	stack := []string{root}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := r.readDir(ctx, dir)
		if err != nil {
			return fmt.Errorf("scan local %s: %w", dir, err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(entry); err != nil {
				return err
			}
			if entry.IsDir {
				stack = append(stack, entry.Path)
			}
		}
	}
	return ctx.Err()
}
