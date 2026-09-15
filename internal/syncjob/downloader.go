package syncjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"tinysync/internal/source"
)

// 下载器默认重试参数：共尝试 3 次，指数退避 100ms / 200ms。
const (
	defaultMaxAttempts = 3
	baseBackoff        = 100 * time.Millisecond
)

// tempPrefix 是同目录临时文件前缀；临时文件必须与目标同目录，
// 保证 rename 在同一文件系统内完成（Windows 为同目录 replace 语义，
// 不夸大为 OS 保证的严格原子操作）。
const tempPrefix = ".tinysync-part-"

// Downloader 把远端文件原子下载到 LocalRoot 之下的目标路径：
// remote.Open → 同目录临时文件 → io.Copy → 大小校验 → Sync/Close →
// rename 替换目标。任何失败都清理临时文件且不触碰已有目标；
// 瞬时错误按 maxAttempts 基础重试，context 取消立即放弃。
type Downloader struct {
	remote      source.Remote
	maxAttempts int
	// backoff 返回第 attempt 次重试前的等待时间（attempt 从 1 开始）。
	backoff func(attempt int) time.Duration
}

// NewDownloader 构造默认参数的下载器。
func NewDownloader(remote source.Remote) *Downloader {
	return &Downloader{
		remote:      remote,
		maxAttempts: defaultMaxAttempts,
		backoff: func(attempt int) time.Duration {
			return baseBackoff << (attempt - 1)
		},
	}
}

// Download 执行一次带重试的原子下载。logicalPath 是远端 logical path，
// relPath 是相对 LocalRoot 的本地路径（/ 分隔），expected 为下载前
// 快照的指纹，其 Size 非零时校验落地字节数。
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
		lastErr = d.downloadOnce(ctx, logicalPath, target, expected)
		if lastErr == nil {
			return nil
		}
		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			return lastErr
		}
		if attempt < d.maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.backoff(attempt)):
			}
		}
	}
	return lastErr
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

	if err := copyAndVerify(tempPath, rc, expected); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	// rename 替换既有目标；此刻起新文件生效，失败前旧目标完好。
	if err := os.Rename(tempPath, target); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// copyAndVerify 把远端内容写入临时文件并校验字节数；取消传播依赖
// 远端 reader（response body 绑定 request context）。每一步失败都
// 由调用方负责清理临时文件。
func copyAndVerify(tempPath string, rc io.Reader, expected source.Fingerprint) error {
	out, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create temp %s: %w", tempPath, err)
	}
	written, copyErr := io.Copy(out, rc)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("transfer to %s: %w", tempPath, copyErr)
	}
	if expected.Size > 0 && written != expected.Size {
		return fmt.Errorf("size mismatch for %s: got %d bytes, want %d", tempPath, written, expected.Size)
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
