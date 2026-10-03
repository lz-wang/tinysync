package http

import (
	"context"

	"tinysync/internal/source"
)

// ScanTree 实现 source.TreeScanner：显式栈式 DFS 全树扫描，每个目录
// 恰好一次 listing 请求（绝不经分页 List 递归——同一目录索引不会被
// 重复下载），文件与目录都 visit（root 自身除外）。HTML 模式的文件
// metadata 按目录批量有界并发补全。任何一层 listing 失败、caddy
// file_limit 达到上限、任何 metadata 补全失败、任何 visit 错误都整轮
// 失败——绝不返回看起来成功的 incomplete snapshot（Mirror 的删除
// 授权依赖完整快照）。ctx 取消及时终止。
func (c *Client) ScanTree(ctx context.Context, root string, visit func(source.FileInfo) error) error {
	if err := source.ValidateLogicalPath(root); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stack := []string{root}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := c.fetchDir(ctx, dir)
		if err != nil {
			return err
		}
		infos, err := c.hydrate(ctx, dir, entries)
		if err != nil {
			return err
		}
		for _, fi := range infos {
			if err := visit(fi); err != nil {
				return err
			}
			if fi.IsDir {
				stack = append(stack, fi.Path)
			}
		}
	}
	return nil
}
