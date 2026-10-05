package syncjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tinysync/internal/source"
)

// downloadRemote 可编程 Open 的 Remote：按路径返回内容 reader 或错误序列。
type downloadRemote struct {
	contents map[string]io.ReadCloser
	errSeq   map[string][]error
}

func (d *downloadRemote) Stat(ctx context.Context, path string) (source.FileInfo, error) {
	return source.FileInfo{}, errors.New("not implemented")
}

func (d *downloadRemote) List(ctx context.Context, path string, opts source.ListOptions) (source.FilePage, error) {
	return source.FilePage{}, errors.New("not implemented")
}

func (d *downloadRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if errs := d.errSeq[path]; len(errs) > 0 {
		d.errSeq[path] = errs[1:]
		return nil, errs[0]
	}
	if rc, ok := d.contents[path]; ok {
		return rc, nil
	}
	return nil, fmt.Errorf("no such remote file %s", path)
}

func (d *downloadRemote) Close() error {
	return nil
}

// cappedErrReader 从 start 起最多交付 max 字节后断流：已交付部分是
// 有效前缀（断点续传测试的核心形态——「从断点传了 N 字节后连接
// 断开」）。
type cappedErrReader struct {
	data      string
	start     int
	delivered int
	max       int
}

func (e *cappedErrReader) Read(p []byte) (int, error) {
	if e.delivered >= e.max {
		return 0, errors.New("connection reset mid-transfer")
	}
	from := e.start + e.delivered
	to := from + len(p)
	if to > e.start+e.max {
		to = e.start + e.max
	}
	n := copy(p, e.data[from:to])
	e.delivered += n
	return n, nil
}

func (e *cappedErrReader) Close() error { return nil }

// newTestDownloader 构造重试零延迟的下载器，避免测试拖慢。独立调用
// 语义与生产默认一致：单文件路径内的 superseded partial 清理开启。
func newTestDownloader(remote source.Remote) *Downloader {
	return &Downloader{
		remote:          remote,
		maxAttempts:     3,
		pruneSuperseded: true,
		backoff:         func(int) time.Duration { return 0 },
	}
}

// testSpec 构造最小 TransferSpec：独立调用不携带 Job / Source 身份，
// partial id 仍然确定（空身份参与哈希）。
func testSpec(logical, localRoot, relPath string, fp source.Fingerprint) TransferSpec {
	return TransferSpec{LogicalPath: logical, LocalRoot: localRoot, RelPath: relPath, Expected: fp}
}

// partialNameFor 由 spec 身份推导 partial 文件名（与生产路径同一规则）。
func partialNameFor(spec TransferSpec) string {
	return partialName(
		partialTargetID(spec.JobID, spec.RelPath),
		partialRemoteID(spec.SourceID, spec.LogicalPath, spec.Expected))
}

// 正常下载：内容落地、无断点文件残留。
func TestDownloadCreatesFile(t *testing.T) {
	root := t.TempDir()
	remote := &downloadRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": io.NopCloser(strings.NewReader("hello tinysync")),
	}}
	d := newTestDownloader(remote)

	err := d.Download(context.Background(), testSpec("/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 14}))
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "a.txt"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "hello tinysync" {
		t.Errorf("content = %q, want hello tinysync", data)
	}
	assertNoTempFiles(t, root)
}

// 大小校验失败：断点文件已损坏（远端与快照不一致），删除、目标不落盘。
func TestDownloadVerifiesSize(t *testing.T) {
	root := t.TempDir()
	remote := &downloadRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": io.NopCloser(strings.NewReader("short")),
	}}
	d := newTestDownloader(remote)

	err := d.Download(context.Background(), testSpec("/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 100}))
	if err == nil {
		t.Fatal("Download with size mismatch = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "a.txt")); !os.IsNotExist(err) {
		t.Errorf("target exists after failed download, stat err = %v", err)
	}
	assertNoTempFiles(t, root)
}

// 零字节文件严格校验：声明 0 字节时 body 必须为空；声明 0 但收到
// 内容视为传输损坏，不落地。
func TestDownloadVerifiesZeroSize(t *testing.T) {
	t.Run("empty body ok", func(t *testing.T) {
		root := t.TempDir()
		remote := &downloadRemote{contents: map[string]io.ReadCloser{
			"/empty.txt": io.NopCloser(strings.NewReader("")),
		}}
		d := newTestDownloader(remote)

		if err := d.Download(context.Background(), testSpec("/empty.txt", root, "empty.txt", source.Fingerprint{Size: 0})); err != nil {
			t.Fatalf("Download zero-size: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(root, "empty.txt"))
		if err != nil || len(data) != 0 {
			t.Errorf("zero-size file = %q (%v), want empty", data, err)
		}
		assertNoTempFiles(t, root)
	})
	t.Run("non-empty body rejected", func(t *testing.T) {
		root := t.TempDir()
		remote := &downloadRemote{contents: map[string]io.ReadCloser{
			"/empty.txt": io.NopCloser(strings.NewReader("unexpected")),
		}}
		// 单次尝试：fake 的 reader 复用会在重试时耗尽内容，
		// 干扰「声明 0 但 body 非空」的判定。
		d := &Downloader{remote: remote, maxAttempts: 1, backoff: func(int) time.Duration { return 0 }}

		if err := d.Download(context.Background(), testSpec("/empty.txt", root, "empty.txt", source.Fingerprint{Size: 0})); err == nil {
			t.Fatal("Download with declared 0 but non-empty body = nil, want error")
		}
		if _, err := os.Stat(filepath.Join(root, "empty.txt")); !os.IsNotExist(err) {
			t.Errorf("target exists after failed download, stat err = %v", err)
		}
		assertNoTempFiles(t, root)
	})
}

// 传输中途失败：已存在的旧目标保持原内容；已写入的断点前缀保留
// （ADR 0010：瞬时故障不删 partial，下次从断点续传）。
func TestDownloadPreservesTargetOnMidTransferFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(root, "docs", "a.txt")
	if err := os.WriteFile(target, []byte("old-content"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	remote := &downloadRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": &cappedErrReader{data: "new-content-longer", max: 4},
	}}
	d := &Downloader{remote: remote, maxAttempts: 1, backoff: func(int) time.Duration { return 0 }}

	spec := testSpec("/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 18})
	err := d.Download(context.Background(), spec)
	if err == nil {
		t.Fatal("Download with mid-transfer failure = nil, want error")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "old-content" {
		t.Errorf("target = %q, want old-content preserved", data)
	}
	// 断点保留：已写入的 4 字节是有效前缀。
	partial := filepath.Join(root, "docs", partialNameFor(spec))
	pdata, err := os.ReadFile(partial)
	if err != nil {
		t.Fatalf("partial after transient failure: %v", err)
	}
	if string(pdata) != "new-" {
		t.Errorf("partial = %q, want new- (4-byte prefix)", pdata)
	}
}

// 基础重试：首次 Open 失败、重试成功，最终完成。
func TestDownloadRetriesTransientFailure(t *testing.T) {
	root := t.TempDir()
	remote := &downloadRemote{
		contents: map[string]io.ReadCloser{
			"/docs/a.txt": io.NopCloser(strings.NewReader("ok")),
		},
		errSeq: map[string][]error{
			"/docs/a.txt": {errors.New("503 service unavailable")},
		},
	}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), testSpec("/docs/a.txt", root, "docs/a.txt", source.Fingerprint{Size: 2})); err != nil {
		t.Fatalf("Download after retry: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "a.txt"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "ok" {
		t.Errorf("content = %q, want ok", data)
	}
}

// context 取消后不再重试，错误可判定为 Canceled。
func TestDownloadStopsOnContextCancel(t *testing.T) {
	root := t.TempDir()
	remote := &downloadRemote{
		errSeq: map[string][]error{
			"/docs/a.txt": {errors.New("e1"), errors.New("e2"), errors.New("e3"), errors.New("e4")},
		},
	}
	d := newTestDownloader(remote)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.Download(ctx, testSpec("/docs/a.txt", root, "docs/a.txt", source.Fingerprint{}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Download with canceled ctx = %v, want context.Canceled", err)
	}
}

// 目标是既有 symlink 时拒绝，不覆盖。
func TestDownloadRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "docs", "a.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	remote := &downloadRemote{contents: map[string]io.ReadCloser{
		"/docs/a.txt": io.NopCloser(strings.NewReader("x")),
	}}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), testSpec("/docs/a.txt", root, "docs/a.txt", source.Fingerprint{})); err == nil {
		t.Fatal("Download onto symlink = nil, want error")
	}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("docs entries = %d, want only the symlink (no partial files)", len(entries))
	}
}

// resumeRemote 是可编程 ResumableRemote：记录每次 OpenFrom 的请求
// offset；首次调用可返回「读 failAfter 字节后断流」的 reader（模拟
// 传输中途断开），也可注入 unsupported / changed。
type resumeRemote struct {
	downloadRemote
	content string
	mu      sync.Mutex
	// openFroms 记录每次 OpenFrom 的请求 offset（断言续传起点）。
	openFroms []int64
	// failAfter > 0 时首次 OpenFrom 返回读 failAfter 字节后断流的
	// reader；后续调用返回完整后缀。
	failAfter int
	// failAt 非空时按绝对进度序列连续断流：第 n 次 OpenFrom 在
	// offset < failAt[n] 时交付到 failAt[n] 后断流（连续故障矩阵）。
	failAt []int64
	// unsupported 为 true 时 OpenFrom 一律返回 ErrResumeUnsupported。
	unsupported bool
	// changedFrom >= 0 时 offset 达到该值的 OpenFrom 返回
	// ErrRemoteChanged（模拟远端在断点之后变化）。
	changedFrom int64
}

func (r *resumeRemote) OpenFrom(ctx context.Context, path string, offset int64, expected source.Fingerprint) (io.ReadCloser, error) {
	r.mu.Lock()
	n := len(r.openFroms)
	first := n == 0
	r.openFroms = append(r.openFroms, offset)
	r.mu.Unlock()
	if r.unsupported {
		return nil, source.ErrResumeUnsupported
	}
	if r.changedFrom > 0 && offset >= r.changedFrom {
		return nil, source.ErrRemoteChanged
	}
	if n < len(r.failAt) && offset < r.failAt[n] {
		return io.NopCloser(&cappedErrReader{
			data:  r.content,
			start: int(offset),
			max:   int(r.failAt[n] - offset),
		}), nil
	}
	if first && r.failAfter > 0 {
		return io.NopCloser(&cappedErrReader{
			data:  r.content,
			start: int(offset),
			max:   r.failAfter,
		}), nil
	}
	return io.NopCloser(strings.NewReader(r.content[offset:])), nil
}

// 断点续传主链路：第一次 attempt 在 4 字节处断流，重试的 OpenFrom
// 请求 offset=4，最终文件完整一致——证明续传真的发生，而不是重下后
// 碰巧正确。
func TestDownloadResumesFromPartial(t *testing.T) {
	root := t.TempDir()
	remote := &resumeRemote{
		content:   "0123456789abcdef",
		failAfter: 4, // 首次 attempt 从 0 起，读满 4 字节后断流
	}
	d := newTestDownloader(remote)

	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{Size: 16})
	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.bin"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "0123456789abcdef" {
		t.Fatalf("content = %q, want full content", data)
	}
	// attempt 1 从 0 开始（OpenFrom(0)），attempt 2 必须从 4 开始。
	if len(remote.openFroms) != 2 || remote.openFroms[0] != 0 || remote.openFroms[1] != 4 {
		t.Fatalf("OpenFrom offsets = %v, want [0 4]", remote.openFroms)
	}
	assertNoTempFiles(t, root)
}

// 服务重启后的续传：上次进程遗留 partial，本次 Download 的首次
// attempt 就从断点开始（offset 直接是遗留 partial 长度）。
func TestDownloadResumesAcrossRestart(t *testing.T) {
	root := t.TempDir()
	remote := &resumeRemote{content: "0123456789abcdef"}
	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{Size: 16})
	partial := filepath.Join(root, partialNameFor(spec))
	if err := os.WriteFile(partial, []byte("0123"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(remote.openFroms) != 1 || remote.openFroms[0] != 4 {
		t.Fatalf("OpenFrom offsets = %v, want [4]", remote.openFroms)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.bin"))
	if err != nil || string(data) != "0123456789abcdef" {
		t.Fatalf("target = %q (%v), want full content", data, err)
	}
}

// partial 已等于期望大小：不访问网络（Open 一律失败也能完成），校验
// 既有字节后直接原子替换。
func TestDownloadFinalizesFullPartialWithoutNetwork(t *testing.T) {
	root := t.TempDir()
	content := "0123456789abcdef"
	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{Size: int64(len(content))})
	partial := filepath.Join(root, partialNameFor(spec))
	if err := os.WriteFile(partial, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	remote := &downloadRemote{contents: map[string]io.ReadCloser{}}
	remote.errSeq = map[string][]error{"/a.bin": {errors.New("network must not be touched")}}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.bin"))
	if err != nil || string(data) != content {
		t.Fatalf("target = %q (%v)", data, err)
	}
}

// 远端不支持续传（ErrResumeUnsupported）：截断 partial、Open 完整
// 重传，不算失败；已写入的断点被放弃。
func TestDownloadFallsBackWhenResumeUnsupported(t *testing.T) {
	root := t.TempDir()
	remote := &resumeRemote{content: "0123456789abcdef", unsupported: true}
	remote.downloadRemote.contents = map[string]io.ReadCloser{
		"/a.bin": io.NopCloser(strings.NewReader("0123456789abcdef")),
	}
	d := newTestDownloader(remote)

	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{Size: 16})
	// 预置断点：触发 OpenFrom 尝试（offset=0 不走续传路径）。
	if err := os.WriteFile(filepath.Join(root, partialNameFor(spec)), []byte("0123"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.bin"))
	if err != nil || string(data) != "0123456789abcdef" {
		t.Fatalf("target = %q (%v), want full content via full download", data, err)
	}
	if len(remote.openFroms) != 1 || remote.openFroms[0] != 4 {
		t.Fatalf("OpenFrom offsets = %v, want [4] (single probe then fallback)", remote.openFroms)
	}
	assertNoTempFiles(t, root)
}

// 远端身份漂移（ErrRemoteChanged）：partial 删除、本轮确定性失败。
func TestDownloadPurgesPartialOnRemoteChanged(t *testing.T) {
	root := t.TempDir()
	remote := &resumeRemote{
		content:     "0123456789abcdef",
		changedFrom: 4, // 从断点 4 续传时远端已变化
	}
	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{Size: 16})
	partial := filepath.Join(root, partialNameFor(spec))
	if err := os.WriteFile(partial, []byte("0123"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newTestDownloader(remote)

	err := d.Download(context.Background(), spec)
	if err == nil || !errors.Is(err, source.ErrRemoteChanged) {
		t.Fatalf("Download = %v, want ErrRemoteChanged", err)
	}
	if source.IsRetryable(err) {
		t.Error("ErrRemoteChanged must be permanent")
	}
	if _, statErr := os.Lstat(partial); !os.IsNotExist(statErr) {
		t.Errorf("partial survived remote change: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "a.bin")); !os.IsNotExist(statErr) {
		t.Errorf("target exists after remote change: %v", statErr)
	}
}

// partial 超过期望大小（远端缩小或旧残留）：删除并从零完整重传
// （validatePartial 已废弃超长断点，OpenFrom 拿到的 offset 必为 0）。
func TestDownloadRestartsFromOversizedPartial(t *testing.T) {
	root := t.TempDir()
	content := "0123456789"
	remote := &resumeRemote{content: content}
	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{Size: 10})
	partial := filepath.Join(root, partialNameFor(spec))
	if err := os.WriteFile(partial, []byte("0123456789abcdefghij"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.bin"))
	if err != nil || string(data) != content {
		t.Fatalf("target = %q (%v), want fresh full download", data, err)
	}
	if len(remote.openFroms) != 1 || remote.openFroms[0] != 0 {
		t.Fatalf("OpenFrom offsets = %v, want [0] (oversized partial discarded)", remote.openFroms)
	}
}

// 断点 + 后缀的完整 checksum：进程重启后从磁盘重建 prefix 摘要，
// 最终 SHA-256 必须覆盖全文件。
func TestDownloadChecksumCoversPartialPrefix(t *testing.T) {
	root := t.TempDir()
	content := "0123456789abcdef"
	sum := sha256.Sum256([]byte(content))
	remote := &resumeRemote{content: content}
	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{
		Size:     int64(len(content)),
		Checksum: "sha256:" + hex.EncodeToString(sum[:]),
	})
	partial := filepath.Join(root, partialNameFor(spec))
	if err := os.WriteFile(partial, []byte("0123"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a.bin"))
	if err != nil || string(data) != content {
		t.Fatalf("target = %q (%v)", data, err)
	}
}

// 同一 invocation 内 attempt 2 复用内存 hash state：断流重试后
// checksum 仍覆盖全文件（不重新读盘 hash prefix）。
func TestDownloadChecksumAcrossInProcessRetry(t *testing.T) {
	root := t.TempDir()
	content := "0123456789abcdef"
	sum := sha256.Sum256([]byte(content))
	remote := &resumeRemote{
		content:   content,
		failAfter: 4, // attempt 1 写满 4 字节后断流
	}
	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{
		Size:     int64(len(content)),
		Checksum: "sha256:" + hex.EncodeToString(sum[:]),
	})
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(remote.openFroms) != 2 || remote.openFroms[1] != 4 {
		t.Fatalf("OpenFrom offsets = %v, want [0 4]", remote.openFroms)
	}
}

// 同 target 旧指纹 partial 被清理；其它 target 的 partial 不受影响。
func TestDownloadPrunesSupersededPartials(t *testing.T) {
	root := t.TempDir()
	content := "abcdef"
	remote := &resumeRemote{content: content}
	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{Size: 6})
	tid := partialTargetID(spec.JobID, spec.RelPath)
	stale := filepath.Join(root, partialName(tid, partialRemoteID(spec.SourceID, spec.LogicalPath, source.Fingerprint{Size: 99})))
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherSpec := testSpec("/b.bin", root, "b.bin", source.Fingerprint{Size: 6})
	other := filepath.Join(root, partialNameFor(otherSpec))
	if err := os.WriteFile(other, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newTestDownloader(remote)

	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if _, statErr := os.Lstat(stale); !os.IsNotExist(statErr) {
		t.Errorf("superseded partial survived: %v", statErr)
	}
	if _, statErr := os.Lstat(other); statErr != nil {
		t.Errorf("other target's partial was pruned: %v", statErr)
	}
}

// assertNoTempFiles 断言目录树中没有遗留的传输中间文件（legacy 随机
// 临时文件与 v1 断点文件两种形态）。
func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isTransferTempName(d.Name()) || IsPartialName(d.Name()) {
			t.Errorf("temp file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// 连续故障矩阵：attempt 1 断在 4、attempt 2 断在 8，attempt 3 必须从
// 第二次的结束位置（8）继续——每次重试都从最新断点续传。
func TestDownloadResumesAcrossRepeatedFailures(t *testing.T) {
	root := t.TempDir()
	remote := &resumeRemote{
		content: "0123456789abcdef",
		failAt:  []int64{4, 8},
	}
	d := newTestDownloader(remote)

	spec := testSpec("/a.bin", root, "a.bin", source.Fingerprint{Size: 16})
	if err := d.Download(context.Background(), spec); err != nil {
		t.Fatalf("Download: %v", err)
	}
	want := []int64{0, 4, 8}
	if len(remote.openFroms) != len(want) {
		t.Fatalf("OpenFrom offsets = %v, want %v", remote.openFroms, want)
	}
	for i := range want {
		if remote.openFroms[i] != want[i] {
			t.Fatalf("OpenFrom offsets = %v, want %v", remote.openFroms, want)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "a.bin"))
	if err != nil || string(data) != "0123456789abcdef" {
		t.Fatalf("target = %q (%v)", data, err)
	}
}
