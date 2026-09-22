package syncjob

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"

	"tinysync/internal/source"
)

// checksumVerifier 是下载流式校验器：consume 每次写入后喂入摘要，
// verify 在传输完成后与期望十六进制值比较。
type checksumVerifier struct {
	hasher  hash.Hash
	wantHex string
}

// newChecksumVerifier 按 Fingerprint.Checksum 构造校验器；空串表示
// 协议未提供摘要，返回 nil（仅做既有字节数校验）。声明了不支持的
// 算法一律报错——声明了摘要就必须校验，宁可失败也不能静默跳过
// （fail-closed，契约见设计文档 §5）。摘要格式规则由 source 包的
// 共享解析器定义，adapter 快照校验与下载校验使用同一份。
func newChecksumVerifier(checksum string) (*checksumVerifier, error) {
	wantHex, err := source.ParseChecksum(checksum)
	if err != nil {
		return nil, err
	}
	if wantHex == "" {
		return nil, nil
	}
	return &checksumVerifier{hasher: sha256.New(), wantHex: wantHex}, nil
}

// verify 在传输完成后比较摘要；不匹配返回错误（调用方按确定性失败
// 处理：不重试、不原子替换）。
func (v *checksumVerifier) verify(target string) error {
	if v == nil {
		return nil
	}
	got := hex.EncodeToString(v.hasher.Sum(nil))
	if got != v.wantHex {
		return fmt.Errorf("checksum mismatch for %s: got %s%s, want %s", target, source.ChecksumSHA256Prefix, got, v.wantHex)
	}
	return nil
}
