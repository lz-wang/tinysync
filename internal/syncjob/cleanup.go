package syncjob

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// CleanupTransferTemps 清理进程 crash 遗留与过期的传输中间文件，
// 区分两代格式（ADR 0010）：
//
//   - legacy `.tinysync-part-<12 hex>`：随机后缀不可恢复，启动即删；
//   - `.tinysync-part-v1-*` 断点文件：保留供断点续传，仅删除超过
//     partialRetention 的孤儿（Job 停用 / 删除 / LocalRoot 变更遗留）。
//
// 枚举 roots（已配置 Job 的 LocalRoot），WalkDir 不跟随 symlink，
// 返回删除数量。必须在启动 Scheduler / Runner 之前调用——此刻本进程
// 还没有任何 active transfer，不会误删自己的断点文件；同前缀但非真实
// 内部形态的名字可能是合法用户文件，一律保留；其它隐藏文件一概不动，
// managed metadata 不受影响（断点文件不在 managed 之列）。LocalRoot
// 不存在（Job 尚未运行过）静默跳过。
func CleanupTransferTemps(ctx context.Context, roots []string) (int, error) {
	return cleanupTransferTemps(ctx, roots, time.Now())
}

// cleanupTransferTemps 是 CleanupTransferTemps 的可注入时钟实现。
func cleanupTransferTemps(ctx context.Context, roots []string, now time.Time) (int, error) {
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
			name := d.Name()
			// legacy 随机临时文件：不可恢复，无条件删除。
			if d.Type().IsRegular() && isTransferTempName(name) {
				if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
					return rmErr
				}
				removed++
				return nil
			}
			// v1 断点文件：超过 retention 的孤儿才删除；条目不是
			// regular file（symlink 等）时 retention 判定无意义，
			// 留给 Downloader 的 validatePartial fail closed。
			if IsPartialName(name) && d.Type().IsRegular() {
				info, statErr := d.Info()
				if statErr != nil {
					return statErr
				}
				if isExpiredPartial(info, now) {
					if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
						return rmErr
					}
					removed++
				}
			}
			return nil
		})
		if err != nil {
			return removed, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	return removed, nil
}
