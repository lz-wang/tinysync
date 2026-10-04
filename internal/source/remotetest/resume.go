package remotetest

import (
	"context"
	"errors"
	"io"
	"testing"

	"tinysync/internal/source"
)

// resumeContentSize 是断点续传契约套件的确定性文件长度：足够穿越
// 多轮缓冲拷贝，且远大于任何单次 read（防止 offset 混同于 0 的实现
// 偶然通过前缀比对）。
const resumeContentSize = 64 * 1024

// resumeContent 生成确定性伪随机内容（可重复比较）。
func resumeContent() []byte {
	buf := make([]byte, resumeContentSize)
	x := uint32(0x85ebca6b)
	for i := range buf {
		x = x*1664525 + 1013904223
		buf[i] = byte(x >> 24)
	}
	return buf
}

// RunResumeSuite 验证可选的 ResumableRemote 能力契约（ADR 0010）：
// offset 流的精确起点、EOF 语义、身份校验与非法路径拒绝。Remote 未
// 实现该能力时跳过（optional capability，跳过不是失败）；各协议
// 特有的 Range / If-Match 行为由 adapter 自身测试覆盖。
func RunResumeSuite(t *testing.T, h Harness) {
	t.Run("OpenFromMatchesSuffix", func(t *testing.T) {
		assertOpenFromMatchesSuffix(t, h)
	})
	t.Run("OpenFromZeroOffset", func(t *testing.T) {
		assertOpenFromZeroOffset(t, h)
	})
	t.Run("OpenFromEndOffset", func(t *testing.T) {
		assertOpenFromEndOffset(t, h)
	})
	t.Run("OpenFromRemoteChanged", func(t *testing.T) {
		assertOpenFromRemoteChanged(t, h)
	})
	t.Run("OpenFromInvalidLogicalPath", func(t *testing.T) {
		assertOpenFromInvalidLogicalPath(t, h)
	})
}

// withResumeRemote 装配 ResumableRemote 契约测试：写入确定性文件并
// 断言能力存在（optional capability：缺失时 Skip，不构成协议失败）。
func withResumeRemote(t *testing.T, h Harness, fn func(*testing.T, source.Remote, source.ResumableRemote)) {
	t.Helper()
	r := h.NewRemote(t)
	defer func() { _ = r.Close() }()
	rr, ok := r.(source.ResumableRemote)
	if !ok {
		t.Skipf("remote %T does not implement ResumableRemote", r)
	}
	h.Write(t, "/resume.bin", string(resumeContent()))
	fn(t, r, rr)
}

// assertOpenFromMatchesSuffix 验证 offset 流的第一个字节就是 offset，
// 且读到 EOF 为止与完整内容的后缀逐字节一致。
func assertOpenFromMatchesSuffix(t *testing.T, h Harness) {
	withResumeRemote(t, h, func(t *testing.T, r source.Remote, rr source.ResumableRemote) {
		content := readResumeFile(t, r)
		for _, offset := range []int64{1, 4096, resumeContentSize / 2, resumeContentSize - 7} {
			rc, err := rr.OpenFrom(context.Background(), "/resume.bin", offset, resumeFingerprint(t, r))
			if err != nil {
				t.Fatalf("OpenFrom(%d): %v", offset, err)
			}
			got, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				t.Fatalf("ReadAll OpenFrom(%d): %v", offset, err)
			}
			if len(got) != resumeContentSize-int(offset) {
				t.Fatalf("OpenFrom(%d) length = %d, want %d", offset, len(got), resumeContentSize-int(offset))
			}
			for i := range got {
				if got[i] != content[int(offset)+i] {
					t.Fatalf("OpenFrom(%d) diverges at +%d: got %x, want %x", offset, i, got[i], content[int(offset)+i])
				}
			}
		}
	})
}

// assertOpenFromZeroOffset 验证 offset 0 等价完整 Open。
func assertOpenFromZeroOffset(t *testing.T, h Harness) {
	withResumeRemote(t, h, func(t *testing.T, r source.Remote, rr source.ResumableRemote) {
		content := readResumeFile(t, r)
		rc, err := rr.OpenFrom(context.Background(), "/resume.bin", 0, resumeFingerprint(t, r))
		if err != nil {
			t.Fatalf("OpenFrom(0): %v", err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if len(got) != len(content) {
			t.Fatalf("OpenFrom(0) length = %d, want %d", len(got), len(content))
		}
		for i := range got {
			if got[i] != content[i] {
				t.Fatalf("OpenFrom(0) diverges at %d", i)
			}
		}
	})
}

// assertOpenFromEndOffset 验证 offset == size 的边界：空流或错误皆可
//（部分协议无法表达空 Range，如 HTTP 的 bytes=N- 起点 == size 返回
// 416），唯独不能把完整文件伪装成 offset 流。
func assertOpenFromEndOffset(t *testing.T, h Harness) {
	withResumeRemote(t, h, func(t *testing.T, r source.Remote, rr source.ResumableRemote) {
		rc, err := rr.OpenFrom(context.Background(), "/resume.bin", resumeContentSize, resumeFingerprint(t, r))
		if err != nil {
			return
		}
		defer func() { _ = rc.Close() }()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll OpenFrom(end): %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("OpenFrom(end) returned %d bytes, want empty stream", len(got))
		}
	})
}

// assertOpenFromRemoteChanged 验证身份校验：expected 与远端当前指纹
// 不一致时返回 ErrRemoteChanged，绝不返回另一个对象的流。
func assertOpenFromRemoteChanged(t *testing.T, h Harness) {
	withResumeRemote(t, h, func(t *testing.T, r source.Remote, rr source.ResumableRemote) {
		stale := resumeFingerprint(t, r)
		// 覆盖为不同长度：任何身份字段（Size / mtime / ETag）都必然
		// 漂移，不依赖单一字段的时间精度。
		h.Write(t, "/resume.bin", "shrunk remote object")
		_, err := rr.OpenFrom(context.Background(), "/resume.bin", 16, stale)
		if !errors.Is(err, source.ErrRemoteChanged) {
			t.Fatalf("OpenFrom with stale fingerprint = %v, want ErrRemoteChanged", err)
		}
	})
}

// assertOpenFromInvalidLogicalPath 验证入口路径校验与 Open 同语义。
func assertOpenFromInvalidLogicalPath(t *testing.T, h Harness) {
	withResumeRemote(t, h, func(t *testing.T, r source.Remote, rr source.ResumableRemote) {
		for _, p := range []string{"not-absolute", "/a/../b", "a\\b", "/a//b", "/a/", ""} {
			if _, err := rr.OpenFrom(context.Background(), p, 0, source.Fingerprint{}); !errors.Is(err, source.ErrInvalid) {
				t.Errorf("OpenFrom %q error = %v, want ErrInvalid", p, err)
			}
		}
	})
}

// resumeFingerprint 取断点文件的当前指纹（Stat 真实值，套件不伪造
// 协议身份字段）。
func resumeFingerprint(t *testing.T, r source.Remote) source.Fingerprint {
	t.Helper()
	fi, err := r.Stat(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Stat /resume.bin: %v", err)
	}
	if fi.Fingerprint.Size != resumeContentSize {
		t.Fatalf("resume file size = %d, want %d", fi.Fingerprint.Size, resumeContentSize)
	}
	return fi.Fingerprint
}

// readResumeFile 经既有 Open 契约读回断点文件全文，供前缀比对。
func readResumeFile(t *testing.T, r source.Remote) []byte {
	t.Helper()
	rc, err := r.Open(context.Background(), "/resume.bin")
	if err != nil {
		t.Fatalf("Open /resume.bin: %v", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll /resume.bin: %v", err)
	}
	if len(data) != resumeContentSize {
		t.Fatalf("resume file content length = %d, want %d", len(data), resumeContentSize)
	}
	return data
}
