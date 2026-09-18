package syncjob

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tinysync/internal/source"
)

// 下载器默认重试参数：共尝试 3 次；指数退避 attempt 2 → ~250ms、
// attempt 3 → ~500ms，叠加有界 jitter（[0, maxJitter)）打散并发传输
// 的重试节奏；只重试 source.IsRetryable 判定为瞬时的错误。
const (
	defaultMaxAttempts = 3
	baseBackoff        = 250 * time.Millisecond
	maxJitter          = 100 * time.Millisecond
)

// tempPrefix 是同目录临时文件前缀；临时文件必须与目标同目录，
// 保证 rename 在同一文件系统内完成（Windows 为同目录 replace 语义，
// 不夸大为 OS 保证的严格原子操作）。
const tempPrefix = ".tinysync-part-"

// fileHooks 是文件系统操作的注入点：生产路径全部为 nil（直连 os），
// 测试经此注入 create temp / 写入（ENOSPC）/ fsync / rename 失败，
// 不依赖 chmod 000（Windows 与 root CI 下结果不可靠）。
type fileHooks struct {
	// createTemp 接管临时文件创建。
	createTemp func(path string) (*os.File, error)
	// wrapWriter 包装 io.Copy 的写入目标（ENOSPC 注入点）。
	wrapWriter func(f *os.File) io.Writer
	// syncFile 接管关闭前的 Sync。
	syncFile func(f *os.File) error
	// renameFile 接管原子替换。
	renameFile func(old, new string) error
}

// Downloader 把远端文件原子下载到 LocalRoot 之下的目标路径：
// remote.Open → 同目录临时文件 → io.Copy → 大小校验 → Sync/Close →
// rename 替换目标。任何失败都清理临时文件且不触碰已有目标；
// 仅 source.IsRetryable 的瞬时错误按 maxAttempts 重试，context 取消
// 与确定性失败立即放弃。
type Downloader struct {
	remote      source.Remote
	maxAttempts int
	// backoff 返回第 attempt 次重试前的等待时间（attempt 从 1 开始）。
	backoff func(attempt int) time.Duration
	// jitter 返回附加的有界随机等待；nil 表示无抖动（测试注入 0 值
	// 或确定性序列，保证计时可预期）。
	jitter func() time.Duration
	// timeout 是单文件单次 attempt 的传输超时；0 表示不启用（默认，
	// HomeLab 大文件可能合法传输很久，不引入任意的默认断流行为）。
	// 超时只作用于当前 attempt：超时的 attempt 可重试，不影响整轮
	// run 的其它控制语义。
	timeout time.Duration
	// hooks 是文件系统操作注入点；nil 表示直连 os。
	hooks *fileHooks
}

// NewDownloader 构造默认参数的下载器。
func NewDownloader(remote source.Remote) *Downloader {
	return &Downloader{
		remote:      remote,
		maxAttempts: defaultMaxAttempts,
		backoff: func(attempt int) time.Duration {
			return baseBackoff << (attempt - 1)
		},
		jitter: defaultJitter,
	}
}

// Download 执行一次带重试的原子下载。logicalPath 是远端 logical path，
// relPath 是相对 LocalRoot 的本地路径（/ 分隔），expected 为下载前
// 快照的指纹，落地字节数必须与其 Size 严格一致（含零字节文件）。
func (d *Downloader) Download(ctx context.Context, logicalPath, localRoot, relPath string, expected source.Fingerprint) error {
	target, err := resolveLocalTarget(localRoot, relPath)
	if err != nil {
		return err
	}
	if err := rejectSymlinkComponents(localRoot, target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent dirs for %s: %w", target, err)
	}

	var lastErr error
	for attempt := 1; attempt <= d.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// attemptCtx 把单次 attempt 的传输超时挂在 run 取消链之下：
		// run 取消立即生效，超时只截断当前 attempt。
		attemptCtx := ctx
		if d.timeout > 0 {
			var cancel context.CancelFunc
			attemptCtx, cancel = context.WithTimeout(ctx, d.timeout)
			defer cancel()
		}
		lastErr = d.downloadOnce(attemptCtx, logicalPath, target, expected)
		if lastErr == nil {
			return nil
		}
		// 确定性失败（401 / 404 / host key / 权限 / ENOSPC / size
		// mismatch 等）不做无意义重试；context.Canceled 同理。
		if !source.IsRetryable(lastErr) {
			return lastErr
		}
		// attempt 超时（DeadlineExceeded）可重试；run 级取消或超时
		// 必须立即停止——检查的是 run ctx 本身，而不是 attempt 错误。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < d.maxAttempts {
			delay := d.backoff(attempt)
			if d.jitter != nil {
				delay += d.jitter()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return lastErr
}

// defaultJitter 返回 [0, maxJitter) 的有界随机抖动；随机源失败时
// 退化为无抖动（重试仍然发生，只是节奏可预测）。
func defaultJitter() time.Duration {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(buf[:]) % uint64(maxJitter))
}

// downloadOnce 执行单次下载：临时文件写入、校验、原子替换。
func (d *Downloader) downloadOnce(ctx context.Context, logicalPath, target string, expected source.Fingerprint) error {
	tempPath, err := newTempPath(target)
	if err != nil {
		return err
	}
	rc, err := d.remote.Open(ctx, logicalPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", logicalPath, err)
	}
	defer rc.Close()

	if err := d.copyAndVerify(tempPath, rc, expected); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	// rename 替换既有目标；此刻起新文件生效，失败前旧目标完好。
	if err := d.renameFile(tempPath, target); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// renameFile 按注入点执行原子替换（nil 直连 os.Rename）。
func (d *Downloader) renameFile(old, new string) error {
	if d.hooks != nil && d.hooks.renameFile != nil {
		return d.hooks.renameFile(old, new)
	}
	return os.Rename(old, new)
}

// copyAndVerify 把远端内容写入临时文件并校验字节数；取消传播依赖
// 远端 reader（response body 绑定 request context）。每一步失败都
// 由调用方负责清理临时文件。
func (d *Downloader) copyAndVerify(tempPath string, rc io.Reader, expected source.Fingerprint) error {
	var out *os.File
	var err error
	if d.hooks != nil && d.hooks.createTemp != nil {
		out, err = d.hooks.createTemp(tempPath)
	} else {
		out, err = os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	}
	if err != nil {
		return fmt.Errorf("create temp %s: %w", tempPath, err)
	}
	var w io.Writer = out
	if d.hooks != nil && d.hooks.wrapWriter != nil {
		w = d.hooks.wrapWriter(out)
	}
	written, copyErr := io.Copy(w, rc)
	if copyErr == nil {
		if d.hooks != nil && d.hooks.syncFile != nil {
			copyErr = d.hooks.syncFile(out)
		} else {
			copyErr = out.Sync()
		}
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("transfer to %s: %w", tempPath, copyErr)
	}
	// 严格校验落地字节数：零字节声明同样适用——「声明 0 但 body 非空」
	// 视为传输损坏，不落地。size mismatch 属确定性失败（远端稳定
	// metadata 与快照不一致），不重试；下一轮 run 经 pending metadata
	// 重新传输收敛。
	if written != expected.Size {
		return source.MarkPermanent(fmt.Errorf("size mismatch for %s: got %d bytes, want %d", tempPath, written, expected.Size))
	}
	return nil
}

// newTempPath 在目标同目录生成唯一临时文件路径。
func newTempPath(target string) (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate temp suffix: %w", err)
	}
	return filepath.Join(filepath.Dir(target), tempPrefix+hex.EncodeToString(buf)), nil
}

// isTransferTempName 判断文件名是否为 Downloader 真实生成的临时文件
// 名形态：tempPrefix + 6 字节 hex（固定 12 个十六进制字符）。crash
// 清理只删除严格匹配的文件——前缀相同但后缀不是 12 位 hex 的名字
// （如 .tinysync-part-notes）可能是合法用户文件，不得误删。
func isTransferTempName(name string) bool {
	suffix, ok := strings.CutPrefix(name, tempPrefix)
	if !ok {
		return false
	}
	if len(suffix) != 12 {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}
