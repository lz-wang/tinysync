package syncjob

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tinysync/internal/source"
)

// partialPrefix 是断点文件（partial file）的文件名前缀（ADR 0010）。
// 完整形态：partialPrefix + <target-id> + "-" + <remote-id>，两段 id
// 均为 16 字节 SHA-256 的 hex 编码（32 字符）。与 legacy 随机临时文件
// （tempPrefix + 12 hex）以 "v1-" 段区分：命名确定性使同一文件同一
// 远端版本跨 run / 跨进程重启得到同一路径，断点位置即文件长度。
const partialPrefix = ".tinysync-part-v1-"

// partialIDHexLen 是每段 id 的 hex 长度（16 字节）。
const partialIDHexLen = 32

// partialRetention 是孤儿断点文件的保留期限。30 天不是协议语义：
// 正常收敛由 Downloader 在开始传输前清理同 target 的旧指纹 partial，
// retention 只负责 Job 停用 / 删除 / LocalRoot 变更等遗留孤儿
// （启动期 CleanupTransferTemps 执行）。
const partialRetention = 30 * 24 * time.Hour

// partialTargetID 计算 target-id：SHA256(jobID ‖ localRelPath)[:16]。
// 同一 Job 的同一本地路径恒等——LocalRoot 变更后 jobID 不变，但
// partial 与 target 同目录（rename 原子性），旧 LocalRoot 下的 partial
// 由 retention 回收，不会跨根复用。
func partialTargetID(jobID, relPath string) string {
	h := sha256.New()
	hashField(h, jobID)
	hashField(h, relPath)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// partialRemoteID 计算 remote-id：SHA256(sourceID ‖ logicalPath ‖
// 指纹全部身份字段)[:16]。远端任一身份信息（Size / mtime / ETag /
// Checksum / Version）变化即产生不同 id，旧 partial 永不被错误复用。
func partialRemoteID(sourceID, logicalPath string, fp source.Fingerprint) string {
	h := sha256.New()
	hashField(h, sourceID)
	hashField(h, logicalPath)
	hashInt64(h, fp.Size)
	hashInt64(h, fp.ModifiedAt.UnixNano())
	hashField(h, fp.ETag)
	hashField(h, fp.Checksum)
	hashField(h, fp.Version)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// partialPathFor 返回 target 同目录的断点文件路径（与目标同目录保证
// rename 在同一文件系统内完成）。
func partialPathFor(target, tid, rid string) string {
	return filepath.Join(filepath.Dir(target), partialName(tid, rid))
}

// partialName 由两段 id 构造断点文件名。
func partialName(tid, rid string) string {
	return partialPrefix + tid + "-" + rid
}

// parsePartialName 解析断点文件名为两段 id；形态不符（含 legacy
// 随机临时文件与其它前缀相近的用户文件）返回 ok=false。
func parsePartialName(name string) (tid, rid string, ok bool) {
	rest, found := strings.CutPrefix(name, partialPrefix)
	if !found {
		return "", "", false
	}
	tid, rid, found = strings.Cut(rest, "-")
	if !found || len(tid) != partialIDHexLen || len(rid) != partialIDHexLen {
		return "", "", false
	}
	if !isHex(tid) || !isHex(rid) {
		return "", "", false
	}
	return tid, rid, true
}

// isPartialName 判定断点文件名形态。
func isPartialName(name string) bool {
	_, _, ok := parsePartialName(name)
	return ok
}

// isHex 校验字符串是否为纯十六进制小写（id 生成的确定形态）。
func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hashField 以长度前缀二进制编码把字符串喂入哈希：字段边界显式化，
// 未来 Fingerprint 结构增删字段时 identity 编码仍确定（不依赖
// fmt.Sprintf 的格式化细节）。
func hashField(h hash.Hash, s string) {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(s))
}

// hashInt64 以固定 8 字节大端编码把整数喂入哈希。
func hashInt64(h hash.Hash, v int64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	_, _ = h.Write(buf[:])
}

// pruneSupersededPartials 删除 dir 内同 target-id 的其它断点文件：
// 远端指纹更新后旧 remote-id 的 partial 不再可能被复用，及时清掉，
// 不留随版本迭代累积的垃圾。keep 是当前指纹对应的文件名。绝不触碰
// 其它 target 的 partial，也不删除任何非断点文件名的条目。
func pruneSupersededPartials(dir, tid, keep string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read dir %s for partial pruning: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == keep || !e.Type().IsRegular() {
			continue
		}
		otherTid, _, ok := parsePartialName(name)
		if !ok || otherTid != tid {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune superseded partial %s: %w", name, err)
		}
	}
	return nil
}

// validatePartial 检查已有断点文件并返回可复用长度（0 表示从零开始）：
//
//   - 不存在：0，调用方新建；
//   - symlink / 目录等非 regular 形态：删除（只删目录条目本身，不递归
//     归零），返回 0——废弃不是失败，Downloader 完整重传；删除失败
//     （非空目录、权限）返回错误，fail closed，绝不把非 regular 文件
//     当 partial 追加；
//   - 长度超过 expected：远端已缩小或 partial 损坏，删除并从零开始；
//   - 其余：返回当前长度作为断点位置。
func validatePartial(path string, expected int64) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("inspect partial %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return 0, fmt.Errorf("remove non-regular partial %s: %w", path, rmErr)
		}
		return 0, nil
	}
	if info.Size() > expected {
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return 0, fmt.Errorf("remove oversized partial %s: %w", path, rmErr)
		}
		return 0, nil
	}
	return info.Size(), nil
}

// isExpiredPartial 判断断点文件是否超过 retention（孤儿回收，见
// partialRetention）。
func isExpiredPartial(info fs.FileInfo, now time.Time) bool {
	return now.Sub(info.ModTime()) > partialRetention
}
