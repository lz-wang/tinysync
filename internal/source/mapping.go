package source

import (
	"fmt"

	"tinysync/internal/filesafe"
)

// ValidateJobMapping 校验源端实际子树与目标的映射；协议差异留在 Source
// 领域，Job 服务和 Runner 只调用此入口。配置与运行阶段都重验文件系统。
func ValidateJobMapping(src Source, remoteRoot, localRoot string) error {
	return validateJobMapping(src, remoteRoot, localRoot, false)
}

// ValidateJobMappingCandidate 在创建目标前校验映射，允许目标目录尚未存在。
// 创建完成后必须再调用 ValidateJobMapping，不能以预检替代运行阶段校验。
func ValidateJobMappingCandidate(src Source, remoteRoot, localRoot string) error {
	return validateJobMapping(src, remoteRoot, localRoot, true)
}

func validateJobMapping(src Source, remoteRoot, localRoot string, allowMissing bool) error {
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
	destination := localRoot
	existing := localRoot
	if allowMissing {
		destination, existing, err = filesafe.CanonicalDirectoryCandidate(localRoot)
		if err != nil {
			return fmt.Errorf("%w: resolve local destination candidate: %w", ErrInvalid, err)
		}
	}
	_, info, err = filesafe.ResolveNoSymlink(existing, "/")
	if err != nil {
		return fmt.Errorf("%w: resolve local destination: %w", ErrInvalid, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: local destination is not a directory", ErrInvalid)
	}
	var overlap bool
	if allowMissing {
		overlap, err = filesafe.DirectoryCandidatesOverlap(effective, destination)
	} else {
		overlap, err = filesafe.ExistingDirectoriesOverlap(effective, destination)
	}
	if err != nil {
		return fmt.Errorf("%w: compare local source and destination: %w", ErrInvalid, err)
	}
	if overlap {
		return fmt.Errorf("%w: local source subtree %s overlaps destination %s", ErrInvalid, effective, destination)
	}
	return nil
}
