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
//     partialRetention 的孤儿（进程 crash 遗留、Job 停用后不再续传的
//     断点；Job 删除 / LocalRoot 变更的孤儿已在 mutation 提交时由
//     RemoveTransferTemps 立即回收，不会等到这里）。
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

// RemoveTransferTemps 立即回收一个 LocalRoot 下的全部传输中间文件
// （legacy 随机临时文件与 v1 断点文件，regular 与 symlink 形态）。
// Job 删除 / LocalRoot 变更后旧 root 不再出现在任何 Job 配置里，启动期
// CleanupTransferTemps 的枚举永远扫不到它，partialRetention 对这类
// mapping 变更孤儿实际无效——必须在 mutation 提交时主动清理。
// LocalRoot 与 Job 一一对应（归属保护拒绝任何重叠），旧 root 的全部
// 中间文件都属于被 mutation 的 Job，立即删除不误伤。断点形态的
// symlink 一并 unlink——旧 root 之后再无 Downloader 访问，validatePartial
// 的 fail-closed 没有意义，留着只会永久残留；os.Remove 只摘除 symlink
// 本身、不跟随目标，不会触及链接指向的文件。WalkDir 不跟随 symlink；
// 断点形态的空目录顺手回收（非空则保留），其它形态（fifo 等）不触碰。
// root 不存在（Job 从未运行）静默跳过。
func RemoveTransferTemps(root string) (int, error) {
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("inspect local root %s: %w", root, err)
	}
	if !info.IsDir() {
		return 0, nil
	}
	removed := 0
	// 断点形态的目录延后到 Walk 结束再回收：Walk 过程中删除目录会让
	// WalkDir 对该目录自身的 ReadDir 落空报错；非空目录 Remove 失败
	// 即保留，不让清理完整性反噬 mapping mutation 的主流程。
	var partialDirs []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		switch {
		case d.IsDir():
			if path != root && IsPartialName(name) {
				partialDirs = append(partialDirs, path)
			}
			return nil
		case d.Type().IsRegular() || d.Type()&fs.ModeSymlink != 0:
			// regular 中间文件直接删除；symlink 只摘链接本身（不跟随
			// 目标），旧 root 无 Downloader 再访问，无需 fail-closed。
			if isTransferTempName(name) || IsPartialName(name) {
				if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
					return rmErr
				}
				removed++
			}
			return nil
		default:
			// fifo / socket / 设备等其它形态：不触碰。
			return nil
		}
	})
	if err != nil {
		return removed, fmt.Errorf("walk %s: %w", root, err)
	}
	for _, dir := range partialDirs {
		if rmErr := os.Remove(dir); rmErr == nil {
			removed++
		}
	}
	return removed, nil
}
