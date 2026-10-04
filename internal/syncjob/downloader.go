package syncjob

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
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

// tempPrefix 是 legacy 随机临时文件前缀（v0.16 之前的形态）。新传输
// 一律使用 deterministic partial（partial.go，ADR 0010）；本前缀仅
// 保留识别，供启动清理兼容删除历史遗留。
const tempPrefix = ".tinysync-part-"

// corruptionError 标记 partial 内容已不可信（size mismatch / checksum
// mismatch / Range 起点错误）：attempt 层据此删除 partial——重试与
// 下一轮 run 都必须从零开始，绝不把损坏前缀拼进最终文件。
type corruptionError struct {
	err error
}

func (e *corruptionError) Error() string { return e.err.Error() }

func (e *corruptionError) Unwrap() error { return e.err }

func markCorruption(err error) error {
	return &corruptionError{err: err}
}

func isCorruption(err error) bool {
	var ce *corruptionError
	return errors.As(err, &ce)
}

// fileHooks 是文件系统操作的注入点：生产路径全部为 nil（直连 os），
// 测试经此注入打开 partial / 写入（ENOSPC）/ fsync / rename / 删除
// 失败，不依赖 chmod 000（Windows 与 root CI 下结果不可靠）。
type fileHooks struct {
	// openPartial 接管断点文件打开（truncate=true 表示从零全量重传）。
	openPartial func(path string, truncate bool) (*os.File, error)
	// wrapWriter 包装拷贝的写入目标（ENOSPC 注入点）。
	wrapWriter func(f *os.File) io.Writer
	// syncFile 接管关闭前的 Sync。
	syncFile func(f *os.File) error
	// renameFile 接管原子替换。
	renameFile func(old, new string) error
	// removePartial 接管断点文件删除（测试可观测）。
	removePartial func(path string) error
}

// TransferSpec 是一次文件传输的全部输入。Downloader 接口从散参数
// 收敛为结构：deterministic partial 命名需要 Job / Source 身份参与
// target-id / remote-id（ADR 0010），RunID 不参与（同一文件多轮 run
// 共享断点）。
type TransferSpec struct {
	RunID       string
	JobID       string
	SourceID    string
	LogicalPath string
	LocalRoot   string
	RelPath     string
	Expected    source.Fingerprint
}

// Downloader 把远端文件原子下载到 LocalRoot 之下的目标路径：
// remote.Open / OpenFrom → 同目录 deterministic partial → 校验 →
// Sync/Close → rename 替换目标。expected 指纹携带协议摘要
// （Fingerprint.Checksum，如 GitHub 的 "sha256:<hex>"）时流式计算
// 「已有 prefix + 新写入 suffix」的完整 SHA-256 并严格比较。断点
// 续传语义（ADR 0010）：partial 长度即断点位置；远端支持
// ResumableRemote 时从断点续传，不支持时截断全量重传（不算失败）；
// 瞬时故障与取消保留 partial 供下次续传，确定性损坏（remote changed /
// checksum / size mismatch）删除 partial。任何校验失败都不原子替换、
// 不触碰已有目标。仅 source.IsRetryable 的瞬时错误按 maxAttempts
// 重试，context 取消与确定性失败立即放弃。
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

// TransferListener 是单文件传输的字节级进度回调（协议无关）：挂在
// Downloader 的拷贝路径上，传输路径只做 atomic 累加，无锁、无 I/O。
type TransferListener interface {
	// AttemptStart 在每次 attempt 开始时调用（含首次与重试），offset
	// 为本次 attempt 的传输起点（断点位置；全量重传为 0）：实现应把
	// 计数对齐 offset——bytes_done 从断点起累加（含 partial 已有
	// 前缀），从头归零会把续传进度压回 0。
	AttemptStart(offset int64)
	// Write 在每次成功写入后调用，n 为本次写入字节数。
	Write(n int64)
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

// Download 执行一次带重试的原子下载（无进度回调）。expected 为下载
// 前快照的指纹，落地字节数必须与其 Size 严格一致（含零字节文件）。
func (d *Downloader) Download(ctx context.Context, spec TransferSpec) error {
	return d.download(ctx, spec, nil)
}

// download 是 Download 的进度感知实现：listener 非 nil 时回报字节级
// 进度（每次 attempt 对齐断点，成功写入后累加）。
func (d *Downloader) download(ctx context.Context, spec TransferSpec, listener TransferListener) error {
	target, err := resolveLocalTarget(spec.LocalRoot, spec.RelPath)
	if err != nil {
		return err
	}
	if err := rejectSymlinkComponents(spec.LocalRoot, target); err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent dirs for %s: %w", target, err)
	}

	tid := partialTargetID(spec.JobID, spec.RelPath)
	rid := partialRemoteID(spec.SourceID, spec.LogicalPath, spec.Expected)
	partialPath := partialPathFor(target, tid, rid)

	// 同 target 旧指纹 partial 立即清理：远端已更新，旧断点永不可能
	// 被复用，不留随版本迭代累积的垃圾（不触碰其它 target）。
	if err := pruneSupersededPartials(dir, tid, filepath.Base(partialPath)); err != nil {
		return err
	}

	var lastErr error
	for attempt := 1; attempt <= d.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// attemptCtx 把单次 attempt 的传输超时挂在 run 取消链之下：
		// run 取消立即生效，超时只截断当前 attempt。cancel 在 attempt
		// 结束后立即释放（不 defer）：多次 retry 的 timer 不挂到整个
		// download 返回。
		attemptCtx := ctx
		var cancel context.CancelFunc
		if d.timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, d.timeout)
		}
		lastErr = d.attempt(attemptCtx, spec, target, partialPath, listener)
		if cancel != nil {
			cancel()
		}
		if lastErr == nil {
			return nil
		}
		// 确定性失败（401 / 404 / host key / 权限 / size mismatch 等）
		// 不做无意义重试；context.Canceled 同理。ENOSPC 等本地磁盘
		// 故障同样立即放弃，但 partial 保留——扩容后下一轮 run 从
		// 断点续传。
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

// attempt 执行单次传输尝试。partial 长度即断点位置：远端支持
// ResumableRemote 时统一走 OpenFrom（含 offset=0 的首次下载，协议
// 单入口）；不支持（未实现能力或运行时返回 ErrResumeUnsupported）
// 时截断遗留 partial、Open 全量重传。partial 的保留 / 废弃按
// ADR 0010 分类：确定性损坏删除，瞬时故障与取消保留断点。
func (d *Downloader) attempt(ctx context.Context, spec TransferSpec, target, partialPath string, listener TransferListener) error {
	// partial 可复用性检查（symlink / 超长废弃）并取断点位置。
	offset, err := validatePartial(partialPath, spec.Expected.Size)
	if err != nil {
		return err
	}

	// partial 已写满（上次 attempt 校验前中断 / 进程重启遗留）：
	// 不访问网络，校验既有字节后直接原子替换。
	if offset > 0 && offset == spec.Expected.Size {
		return d.finalizePartial(spec, target, partialPath, offset)
	}

	verifier, err := newChecksumVerifier(spec.Expected.Checksum)
	if err != nil {
		return source.MarkPermanent(fmt.Errorf("checksum %s for %s: %w", spec.Expected.Checksum, spec.LogicalPath, err))
	}
	ph := &partialHasher{path: partialPath, verifier: verifier}

	transfer := func(rc io.ReadCloser) error {
		// 进度对齐断点：bytes_done 从本次 attempt 的实际起点开始
		//（offset 在 unsupported 截断后已是最终值）。
		if listener != nil {
			listener.AttemptStart(offset)
		}
		err := d.appendAndVerify(spec, target, partialPath, offset, ph, rc, listener)
		if err != nil && isCorruption(err) {
			// 确定性损坏（size / checksum mismatch）：partial 内容不可
			// 信，删除——重试与下一轮 run 都从零开始。
			_ = d.removePartial(partialPath)
		}
		return err
	}

	if rr, resumable := d.remote.(source.ResumableRemote); resumable {
		opened, openErr := rr.OpenFrom(ctx, spec.LogicalPath, offset, spec.Expected)
		switch {
		case openErr == nil:
			return transfer(opened)
		case errors.Is(openErr, source.ErrRemoteChanged):
			// 远端身份漂移：partial 前缀属于旧对象，禁止拼接；
			// 本轮失败，下一 run 重新 scan 对齐。
			_ = d.removePartial(partialPath)
			return source.MarkPermanent(fmt.Errorf("resume %s at %d: %w", spec.LogicalPath, offset, openErr))
		case errors.Is(openErr, source.ErrResumeUnsupported):
			// 能力缺失：截断遗留 partial 全量重传，不算失败。
			if offset > 0 {
				if err := d.truncatePartial(partialPath); err != nil {
					return err
				}
				ph.reset()
				offset = 0
			}
		default:
			return fmt.Errorf("open %s at %d: %w", spec.LogicalPath, offset, openErr)
		}
	} else if offset > 0 {
		// 远端未实现续传能力：遗留 partial 无从续传，截断后全量重传。
		if err := d.truncatePartial(partialPath); err != nil {
			return err
		}
		ph.reset()
		offset = 0
	}
	rc, err := d.remote.Open(ctx, spec.LogicalPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", spec.LogicalPath, err)
	}
	return transfer(rc)
}

// appendAndVerify 把远端流追加到 partial 断点之后并校验：hash 状态
// 先对齐断点（进程重启后从磁盘重建 prefix 摘要，同一 invocation 内
// 直接复用内存 state），写入经 verifiedWriter 保证摘要严格对应实际
// 落盘字节；fsync 后校验总字节数与（协议提供时的）SHA-256，通过即
// rename 原子替换目标。取消传播依赖远端 reader（response body 绑定
// request context）。
func (d *Downloader) appendAndVerify(spec TransferSpec, target, partialPath string, offset int64, ph *partialHasher, rc io.ReadCloser, listener TransferListener) error {
	defer func() { _ = rc.Close() }()

	if err := ph.alignTo(offset); err != nil {
		return err
	}
	var out *os.File
	var err error
	if d.hooks != nil && d.hooks.openPartial != nil {
		out, err = d.hooks.openPartial(partialPath, offset == 0)
	} else {
		flag := os.O_WRONLY | os.O_CREATE
		if offset == 0 {
			flag |= os.O_TRUNC
		}
		out, err = os.OpenFile(partialPath, flag, 0o644)
	}
	if err != nil {
		return fmt.Errorf("open partial %s: %w", partialPath, err)
	}
	if offset > 0 {
		if _, err := out.Seek(offset, io.SeekStart); err != nil {
			_ = out.Close()
			return fmt.Errorf("seek partial %s to %d: %w", partialPath, offset, err)
		}
	}
	var w io.Writer = out
	if d.hooks != nil && d.hooks.wrapWriter != nil {
		w = d.hooks.wrapWriter(out)
	}
	w = ph.wrap(w)

	written, copyErr := copyCounting(w, rc, listener)
	if copyErr == nil {
		copyErr = d.syncFile(out)
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("transfer to %s: %w", partialPath, copyErr)
	}
	// 严格校验落地字节数（断点 + 本次写入）：零字节声明同样适用——
	// 「声明 0 但 body 非空」视为传输损坏，不落地。size mismatch 属
	// 确定性损坏：删除 partial，不重试；下一轮 run 经 pending
	// metadata 重新传输收敛。
	if total := offset + written; total != spec.Expected.Size {
		return markCorruption(source.MarkPermanent(fmt.Errorf(
			"size mismatch for %s: got %d bytes, want %d", partialPath, total, spec.Expected.Size)))
	}
	// 摘要校验在字节数校验之后：任何一项不匹配都属确定性损坏，
	// 删除 partial、不原子替换、不触碰已有目标文件（ADR 0010）。
	if err := ph.verify(partialPath); err != nil {
		return markCorruption(source.MarkPermanent(err))
	}
	// rename 替换既有目标；此刻起新文件生效，失败前旧目标完好。
	// rename 失败保留 partial：内容已完整校验，下一次 attempt 直接
	// 走 finalize 快速路径重试替换。
	if err := d.renameFile(partialPath, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// finalizePartial 处理「partial 已等于期望大小」：不访问网络，对既有
// 字节做完整校验（从磁盘重建 prefix 摘要）后 fsync 并原子替换。
func (d *Downloader) finalizePartial(spec TransferSpec, target, partialPath string, size int64) error {
	verifier, err := newChecksumVerifier(spec.Expected.Checksum)
	if err != nil {
		return source.MarkPermanent(fmt.Errorf("checksum %s for %s: %w", spec.Expected.Checksum, spec.LogicalPath, err))
	}
	ph := &partialHasher{path: partialPath, verifier: verifier}
	if err := ph.alignTo(size); err != nil {
		return err
	}
	// partial 可能来自未及 fsync 的中断 attempt：rename 前确保数据
	// 已确认落盘（崩溃一致性：数据先于 rename 生效）。
	f, err := os.OpenFile(partialPath, os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open partial %s for finalize: %w", partialPath, err)
	}
	syncErr := d.syncFile(f)
	if closeErr := f.Close(); syncErr == nil {
		syncErr = closeErr
	}
	if syncErr != nil {
		return fmt.Errorf("fsync partial %s: %w", partialPath, syncErr)
	}
	if err := ph.verify(partialPath); err != nil {
		_ = d.removePartial(partialPath)
		return markCorruption(source.MarkPermanent(err))
	}
	if err := d.renameFile(partialPath, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// truncatePartial 把遗留 partial 截断为空：远端不可续传时的全量重传
// 路径（保留 inode，截断即断点归零）。
func (d *Downloader) truncatePartial(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("truncate partial %s: %w", path, err)
	}
	return f.Close()
}

// renameFile 按注入点执行原子替换（nil 直连 os.Rename）。
func (d *Downloader) renameFile(old, new string) error {
	if d.hooks != nil && d.hooks.renameFile != nil {
		return d.hooks.renameFile(old, new)
	}
	return os.Rename(old, new)
}

// removePartial 按注入点删除断点文件（nil 直连 os.Remove）。
func (d *Downloader) removePartial(path string) error {
	if d.hooks != nil && d.hooks.removePartial != nil {
		return d.hooks.removePartial(path)
	}
	return os.Remove(path)
}

// syncFile 按注入点执行 fsync（nil 直连 File.Sync）。
func (d *Downloader) syncFile(f *os.File) error {
	if d.hooks != nil && d.hooks.syncFile != nil {
		return d.hooks.syncFile(f)
	}
	return f.Sync()
}

// partialHasher 维护断点文件的增量 SHA-256 状态（ADR 0010）：最终
// 摘要必须覆盖「旧 prefix + 新 suffix」，hasher 已覆盖的字节数与
// partial 实际长度恒同步——进程重启后从磁盘补喂缺口重建 state，
// 同一 invocation 内的重试直接复用内存 state（只有重启才重新读盘）。
type partialHasher struct {
	path     string
	verifier *checksumVerifier // nil 表示协议未提供摘要
	covered  int64             // hasher 已喂入的字节数
}

// reset 清零 hash 状态（partial 被截断、全量重传时）。
func (p *partialHasher) reset() {
	p.covered = 0
	if p.verifier != nil {
		p.verifier.hasher.Reset()
	}
}

// alignTo 把 hash 状态推进到 partial 的 [0, size)。covered 超过
// size（partial 被外力缩短，内部状态损坏）时重置后从磁盘整体重建。
func (p *partialHasher) alignTo(size int64) error {
	if p.covered == size {
		return nil
	}
	if p.verifier == nil {
		p.covered = size
		return nil
	}
	if p.covered > size {
		p.reset()
	}
	if p.covered < size {
		f, err := os.Open(p.path)
		if err != nil {
			return fmt.Errorf("hash partial prefix %s: %w", p.path, err)
		}
		if _, err := f.Seek(p.covered, io.SeekStart); err != nil {
			_ = f.Close()
			return fmt.Errorf("seek partial %s to %d: %w", p.path, p.covered, err)
		}
		if _, err := io.Copy(p.verifier.hasher, io.LimitReader(f, size-p.covered)); err != nil {
			_ = f.Close()
			return fmt.Errorf("hash partial prefix %s: %w", p.path, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("hash partial prefix %s: %w", p.path, err)
		}
	}
	p.covered = size
	return nil
}

// wrap 把 dst 包装为摘要感知写入链：落盘 n 字节喂 n 字节并累加
// covered——短写（ENOSPC 写 12 KiB / 32 KiB）时 hash 状态严格对应
// 实际落盘量，杜绝 partial size 与内存 hash state 漂移。协议未提供
// 摘要时原样返回。
func (p *partialHasher) wrap(dst io.Writer) io.Writer {
	if p.verifier == nil {
		return dst
	}
	return &verifiedWriter{dst: dst, feed: func(b []byte) {
		_, _ = p.verifier.hasher.Write(b)
		p.covered += int64(len(b))
	}}
}

// verifiedWriter 把每次成功写入 dst 的字节同步喂入摘要：Write(32 KiB)
// 只实际落盘 12 KiB（ENOSPC 短写）时摘要只吃 12 KiB（ADR 0010）。
type verifiedWriter struct {
	dst  io.Writer
	feed func([]byte)
}

func (w *verifiedWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.feed(p[:n])
	}
	return n, err
}

// verify 比较完整文件的摘要（covered 即最终长度）；不匹配由调用方
// 按 corruption 处理。
func (p *partialHasher) verify(target string) error {
	if p.verifier == nil {
		return nil
	}
	return p.verifier.verify(target)
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

// copyCounting 是带进度回调的显式拷贝循环：bytes_done 只计已成功
// 写入的字节（wn），不按远端读出字节数虚增——本地磁盘故障（ENOSPC /
// 短写）时进度不得越过真实落地量。语义与 io.Copy 的通用路径一致
// （短写报 ErrShortWrite，EOF 正常结束）。listener 可为 nil。
// 显式循环而非 io.Copy：写入链含 verifiedWriter（无 ReaderFrom /
// WriterTo 快速路径），且计数路径不能依赖 reader 侧优化绕过。
func copyCounting(w io.Writer, rc io.Reader, listener TransferListener) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			wn, werr := w.Write(buf[:n])
			written += int64(wn)
			if wn > 0 && listener != nil {
				listener.Write(int64(wn))
			}
			if werr != nil {
				return written, werr
			}
			if n != wn {
				return written, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return written, rerr
		}
	}
	return written, nil
}

// isTransferTempName 判断文件名是否为 legacy 随机临时文件名形态：
// tempPrefix + 6 字节 hex（固定 12 个十六进制字符）。启动清理只删除
// 严格匹配的文件——前缀相同但后缀不是 12 位 hex 的名字（如
// .tinysync-part-notes）可能是合法用户文件，不得误删。
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
