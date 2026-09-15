package syncjob

import (
	"fmt"

	"github.com/bmatcuk/doublestar/v4"
)

// Selector 按 include / exclude pattern 过滤相对 RemoteRoot 的路径。
// 输入始终是 / 分隔、无前导 / 的相对路径；include 为空等价于 include all，
// exclude 永远优先于 include。第一版不做 selector directory pruning：
// scanner 完整遍历后由 Match 过滤文件，避免 glob 推导错误导致 Mirror
// 把文件误判为远端消失。
type Selector struct {
	include []string
	exclude []string
}

// NewSelector 构造并校验 Selector：任一 pattern 语法非法即报错，
// 不允许把坏 pattern 留到运行时静默不匹配。
func NewSelector(include, exclude []string) (*Selector, error) {
	for _, p := range include {
		if !doublestar.ValidatePattern(p) {
			return nil, fmt.Errorf("%w: invalid include pattern %q", ErrInvalid, p)
		}
	}
	for _, p := range exclude {
		if !doublestar.ValidatePattern(p) {
			return nil, fmt.Errorf("%w: invalid exclude pattern %q", ErrInvalid, p)
		}
	}
	return &Selector{include: include, exclude: exclude}, nil
}

// Match 判断相对 RemoteRoot 的路径是否被选择。
func (s *Selector) Match(relPath string) bool {
	for _, p := range s.exclude {
		if matched, _ := doublestar.Match(p, relPath); matched {
			return false
		}
	}
	if len(s.include) == 0 {
		return true
	}
	for _, p := range s.include {
		if matched, _ := doublestar.Match(p, relPath); matched {
			return true
		}
	}
	return false
}
