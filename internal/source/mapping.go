package source

import (
	"fmt"

	"tinysync/internal/filesafe"
)

// ValidateJobMapping 校验源端实际子树与目标的映射；协议差异留在 Source
// 领域，Job 服务和 Runner 只调用此入口。配置与运行阶段都重验文件系统。
func ValidateJobMapping(src Source, remoteRoot, localRoot string) error {
	if src.Type != TypeLocal {
		return nil
	}
	if err := ValidateConfig(src.Type, src.Config); err != nil {
		return err
	}
	effective, info, err := filesafe.ResolveNoSymlink(src.Config.Local.Root, remoteRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve local source subtree: %w", ErrInvalid, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: local source subtree is not a directory", ErrInvalid)
	}
	destination, info, err := filesafe.ResolveNoSymlink(localRoot, "/")
	if err != nil {
		return fmt.Errorf("%w: resolve local destination: %w", ErrInvalid, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: local destination is not a directory", ErrInvalid)
	}
	if filesafe.PathsOverlap(effective, destination) {
		return fmt.Errorf("%w: local source subtree %s overlaps destination %s", ErrInvalid, effective, destination)
	}
	return nil
}
