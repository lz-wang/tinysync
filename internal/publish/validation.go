package publish

import (
	"fmt"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
)

// ValidateCreateInput 校验创建输入的全部必填与格式约束：目标 Job
// 与逻辑路径的合法性在此层判定；managed / 存在性 / 过期时刻合法性
// 属于 Service（需要 Job、文件系统与时钟信息）。
func ValidateCreateInput(input CreateInput) error {
	if input.JobID == "" {
		return fmt.Errorf("%w: job id is required", source.ErrInvalid)
	}
	if err := validateTargetPath(input.Path); err != nil {
		return err
	}
	if _, err := filesafe.NormalizePublicPath(input.PublicPath); err != nil {
		return fmt.Errorf("%w: %v", source.ErrInvalid, err)
	}
	return nil
}

// validateTargetPath 校验 LocalRoot 内的目标逻辑路径：必须是绝对
// 逻辑路径且不指向根（根是目录，无法作为单文件发布）。
func validateTargetPath(p string) error {
	if err := filesafe.ValidateLogicalPath(p); err != nil {
		return fmt.Errorf("%w: %v", source.ErrInvalid, err)
	}
	if p == "/" {
		return fmt.Errorf("%w: publish target must be a file, not the local root", source.ErrInvalid)
	}
	return nil
}

// ValidateUpdateInput 校验更新输入：提供的 public_path 必须合法；
// expires_at 与 clear_expires 互斥。过期时刻的过去性由 Service 校验
// （依赖可注入时钟）。
func ValidateUpdateInput(input UpdateInput) error {
	if input.PublicPath != nil {
		if _, err := filesafe.NormalizePublicPath(*input.PublicPath); err != nil {
			return fmt.Errorf("%w: %v", source.ErrInvalid, err)
		}
	}
	if input.ExpiresAt != nil && input.ClearExpires {
		return fmt.Errorf("%w: expires_at and clear_expires are mutually exclusive", source.ErrInvalid)
	}
	return nil
}
