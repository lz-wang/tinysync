package syncjob

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveStaleTempFiles 清理进程 crash 遗留的传输临时文件：枚举
// roots（已配置 Job 的 LocalRoot），WalkDir 不跟随 symlink，只删除
// 与内部临时文件名形态（.tinysync-part- + 12 位 hex，见
// isTransferTempName）严格匹配的普通文件，返回删除数量。必须在
// 启动 Scheduler / Runner 之前调用——此刻本进程还没有任何 active
// transfer，不会误删自己的临时文件；同前缀但非真实临时形态的名字
// 可能是合法用户文件，一律保留；其它隐藏文件一概不动，managed
// metadata 不受影响（临时文件不在 managed 之列）。LocalRoot 不存在
// （Job 尚未运行过）静默跳过。
func RemoveStaleTempFiles(ctx context.Context, roots []string) (int, error) {
	removed := 0
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		info, err := os.Lstat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("inspect local root %s: %w", root, err)
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// WalkDir 不跟随目录 symlink；普通子目录继续遍历。
				return nil
			}
			if d.Type().IsRegular() && isTransferTempName(d.Name()) {
				if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
					return rmErr
				}
				removed++
			}
			return nil
		})
		if err != nil {
			return removed, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	return removed, nil
}
