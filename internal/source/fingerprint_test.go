package source

import (
	"testing"
	"time"
)

// SameFingerprint 的比较规则：Size 恒参与；expected 非零 / 非空的
// 身份字段必须一致，零值字段（协议未提供）不参与比较。
func TestSameFingerprint(t *testing.T) {
	base := Fingerprint{
		Size:       100,
		ModifiedAt: time.Unix(1700000000, 0),
		ETag:       `"abc"`,
		Checksum:   "sha256:xx",
		Version:    "v1",
	}
	if !SameFingerprint(base, base) {
		t.Fatal("identical fingerprints reported different")
	}
	// 任一身份字段漂移都不一致。
	mutations := map[string]func(Fingerprint) Fingerprint{
		"size":     func(f Fingerprint) Fingerprint { f.Size++; return f },
		"mtime":    func(f Fingerprint) Fingerprint { f.ModifiedAt = f.ModifiedAt.Add(time.Second); return f },
		"etag":     func(f Fingerprint) Fingerprint { f.ETag = `"def"`; return f },
		"checksum": func(f Fingerprint) Fingerprint { f.Checksum = "sha256:yy"; return f },
		"version":  func(f Fingerprint) Fingerprint { f.Version = "v2"; return f },
	}
	for name, mutate := range mutations {
		if SameFingerprint(mutate(base), base) {
			t.Errorf("SameFingerprint stable under %s mutation", name)
		}
	}
	// expected 零值字段不参与比较：actual 缺少该字段仍视为一致
	//（协议只填充自己能提供的字段）。
	partialExpected := Fingerprint{Size: 100}
	if !SameFingerprint(base, partialExpected) {
		t.Error("expected with zero fields must match actual carrying values")
	}
	// Size 一致性与字段无关：恒参与比较。
	if SameFingerprint(Fingerprint{Size: 101}, partialExpected) {
		t.Error("Size mismatch must never compare equal")
	}
}
