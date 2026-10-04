package syncjob

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"tinysync/internal/source"
)

// target-id / remote-id 的确定性：同一输入恒等；任一身份字段变化
// 即产生不同 remote-id。
func TestPartialIDDeterminism(t *testing.T) {
	fp := source.Fingerprint{Size: 100, ModifiedAt: time.Unix(1700000000, 0), ETag: `"abc"`}
	if partialTargetID("job_1", "docs/a.txt") != partialTargetID("job_1", "docs/a.txt") {
		t.Fatal("partialTargetID not deterministic")
	}
	base := partialRemoteID("src_1", "/docs/a.txt", fp)
	if partialRemoteID("src_1", "/docs/a.txt", fp) != base {
		t.Fatal("partialRemoteID not deterministic")
	}
	// remote-id 随指纹身份字段漂移。
	fingerprintMutations := map[string]func(source.Fingerprint) source.Fingerprint{
		"size":     func(f source.Fingerprint) source.Fingerprint { f.Size++; return f },
		"mtime":    func(f source.Fingerprint) source.Fingerprint { f.ModifiedAt = f.ModifiedAt.Add(time.Second); return f },
		"etag":     func(f source.Fingerprint) source.Fingerprint { f.ETag = `"def"`; return f },
		"checksum": func(f source.Fingerprint) source.Fingerprint { f.Checksum = "sha256:xx"; return f },
		"version":  func(f source.Fingerprint) source.Fingerprint { f.Version = "v2"; return f },
	}
	for name, mutate := range fingerprintMutations {
		if id := partialRemoteID("src_1", "/docs/a.txt", mutate(fp)); id == base {
			t.Errorf("remote-id stable under %s mutation", name)
		}
	}
	// remote-id 随 sourceID / logicalPath 漂移；target-id 不受其影响。
	if partialRemoteID("src_2", "/docs/a.txt", fp) == base {
		t.Error("remote-id stable under sourceID change")
	}
	if partialRemoteID("src_1", "/docs/b.txt", fp) == base {
		t.Error("remote-id stable under logicalPath change")
	}
	// target-id 只由 jobID + relPath 决定。
	tBase := partialTargetID("job_1", "docs/a.txt")
	if partialTargetID("job_2", "docs/a.txt") == tBase {
		t.Error("target-id stable under jobID change")
	}
	if partialTargetID("job_1", "docs/b.txt") == tBase {
		t.Error("target-id stable under relPath change")
	}
	if partialTargetID("job_1", "docs/a.txt") != tBase {
		t.Error("target-id depends on more than jobID+relPath")
	}
}

// 长度前缀编码使相邻字段拼接不产生歧义：("ab","c") 与 ("a","bc")
// 必须得到不同 id。
func TestPartialIDFieldBoundary(t *testing.T) {
	fp1 := source.Fingerprint{Size: 1}
	a := partialRemoteID("s", "/x", fp1)
	b := partialRemoteID("sa", "/x", source.Fingerprint{Size: 0})
	// ("s", size=1) vs ("sa", size=0)：字段内容不同但朴素拼接
	// （"s1" vs "sa0"）也不等；此断言守护编码含长度前缀的语义。
	if a == b {
		t.Fatal("length-prefixed encoding collapsed distinct inputs")
	}
	// 真正的边界歧义用例：身份字段组合不同、朴素拼接相同。
	// sourceID "a" + logicalPath "bc" vs sourceID "ab" + logicalPath "c"。
	if partialRemoteID("a", "/bc", fp1) == partialRemoteID("ab", "/c", fp1) {
		t.Fatal("field boundary ambiguity: (a,bc) == (ab,c)")
	}
}

// 文件名形态：id hex、解析往返、legacy / 伪装形态拒绝。
func TestPartialNameParsing(t *testing.T) {
	tid := partialTargetID("job_1", "docs/a.txt")
	rid := partialRemoteID("src_1", "/docs/a.txt", source.Fingerprint{Size: 3})
	name := partialName(tid, rid)
	if len(name) != len(partialPrefix)+2*partialIDHexLen+1 {
		t.Fatalf("partial name %q has unexpected length %d", name, len(name))
	}
	gotTid, gotRid, ok := parsePartialName(name)
	if !ok || gotTid != tid || gotRid != rid {
		t.Fatalf("parsePartialName(%q) = (%s, %s, %v), want (%s, %s, true)", name, gotTid, gotRid, ok, tid, rid)
	}
	if !IsPartialName(name) {
		t.Fatal("IsPartialName(partialName()) = false")
	}
	decoys := []string{
		".tinysync-part-0123456789ab",                    // legacy 随机临时文件
		".tinysync-part-v1-",                             // 裸前缀
		".tinysync-part-v1-" + tid,                       // 缺 remote-id
		".tinysync-part-v1-" + tid + "-",                 // 空 remote-id
		".tinysync-part-v1-" + tid + "-" + rid[:31],      // 短 id
		".tinysync-part-v1-" + tid + "-" + rid + "x",     // 多余字符
		partialName(tid, rid) + ".txt",                   // 带扩展名
		".tinysync-part-v1-" + tid + "-" + "Z" + rid[1:], // 非 hex 字符
		"normal.txt",
		"",
	}
	for _, d := range decoys {
		if IsPartialName(d) {
			t.Errorf("IsPartialName(%q) = true, want false", d)
		}
	}
}

// partialPathFor 与目标同目录（rename 原子性前提）。
func TestPartialPathFor(t *testing.T) {
	target := filepath.Join("root", "docs", "a.txt")
	p := partialPathFor(target, "tid", "rid")
	if filepath.Dir(p) != filepath.Dir(target) {
		t.Fatalf("partial %s not in target dir %s", p, filepath.Dir(target))
	}
	if filepath.Base(p) != partialName("tid", "rid") {
		t.Fatalf("partial base = %q", filepath.Base(p))
	}
}

// pruneSupersededPartials 只删除同 target-id 的其它断点文件；当前
// 文件、其它 target、无关文件、symlink 与目录一律保留。
func TestPruneSupersededPartials(t *testing.T) {
	dir := t.TempDir()
	tidA := partialTargetID("job_A", "a.txt")
	tidB := partialTargetID("job_B", "b.txt")
	keep := partialName(tidA, "11110000000000000000000000000000")
	old1 := partialName(tidA, "22220000000000000000000000000000")
	old2 := partialName(tidA, "33330000000000000000000000000000")
	otherTarget := partialName(tidB, "44440000000000000000000000000000")
	for _, name := range []string{keep, old1, old2, otherTarget, "normal.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Symlink("normal.txt", filepath.Join(dir, partialName(tidA, "55550000000000000000000000000000"))); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, partialName(tidA, "66660000000000000000000000000000")), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := pruneSupersededPartials(dir, tidA, keep); err != nil {
		t.Fatalf("pruneSupersededPartials: %v", err)
	}
	for _, gone := range []string{old1, old2} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("superseded partial %s survived", gone)
		}
	}
	for _, stays := range []string{keep, otherTarget, "normal.txt",
		partialName(tidA, "55550000000000000000000000000000"),
		partialName(tidA, "66660000000000000000000000000000")} {
		if _, err := os.Lstat(filepath.Join(dir, stays)); err != nil {
			t.Errorf("%s was pruned: %v", stays, err)
		}
	}

	// dir 不存在：no-op 而不是错误（首传目录尚未创建）。
	if err := pruneSupersededPartials(filepath.Join(t.TempDir(), "missing"), tidA, keep); err != nil {
		t.Fatalf("prune in missing dir = %v, want nil", err)
	}
}

// validatePartial 的四态：不存在 → 0；regular 且未超 expected → 长度；
// symlink → 删除后 0；超过 expected → 删除后 0。非空目录删除失败 →
// 错误（fail closed）。
func TestValidatePartial(t *testing.T) {
	t.Run("missing is zero", func(t *testing.T) {
		n, err := validatePartial(filepath.Join(t.TempDir(), "none"), 10)
		if n != 0 || err != nil {
			t.Fatalf("validatePartial(missing) = (%d, %v), want (0, nil)", n, err)
		}
	})
	t.Run("regular within expected returns size", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), partialName("tid", "rid"))
		if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
		n, err := validatePartial(path, 10)
		if n != 5 || err != nil {
			t.Fatalf("validatePartial = (%d, %v), want (5, nil)", n, err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("valid partial was removed: %v", statErr)
		}
	})
	t.Run("size equal to expected is reusable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), partialName("tid", "rid"))
		if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}
		n, err := validatePartial(path, 5)
		if n != 5 || err != nil {
			t.Fatalf("validatePartial = (%d, %v), want (5, nil)", n, err)
		}
	})
	t.Run("symlink removed", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "victim"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, partialName("tid", "rid"))
		if err := os.Symlink("victim", path); err != nil {
			t.Fatal(err)
		}
		n, err := validatePartial(path, 10)
		if n != 0 || err != nil {
			t.Fatalf("validatePartial(symlink) = (%d, %v), want (0, nil)", n, err)
		}
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("symlink survived: %v", statErr)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "victim")); statErr != nil {
			t.Fatalf("symlink target was touched: %v", statErr)
		}
	})
	t.Run("oversized removed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), partialName("tid", "rid"))
		if err := os.WriteFile(path, []byte("12345678"), 0o644); err != nil {
			t.Fatal(err)
		}
		n, err := validatePartial(path, 5)
		if n != 0 || err != nil {
			t.Fatalf("validatePartial(oversized) = (%d, %v), want (0, nil)", n, err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("oversized partial survived: %v", statErr)
		}
	})
	t.Run("non-empty directory fails closed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, partialName("tid", "rid"))
		if err := os.MkdirAll(filepath.Join(path, "inner"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "inner", "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := validatePartial(path, 10); err == nil {
			t.Fatal("validatePartial(non-empty dir) = nil error, want fail-closed error")
		}
	})
}
