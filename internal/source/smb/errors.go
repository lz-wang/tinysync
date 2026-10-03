package smb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"

	smb2 "github.com/cloudsoda/go-smb2"

	"tinysync/internal/source"
)

// 错误分类——v0.9 重试策略的 adapter boundary 义务：SMB 只在这里
// 认识自己的协议错误（NTSTATUS / transport），上层只问
// source.IsRetryable。go-smb2 的 erref 包是 internal，无法外部引用，
// 数值按 MS-ERREF 固定定义在此。
const (
	ntStatusAccessDenied       uint32 = 0xC0000022 // STATUS_ACCESS_DENIED
	ntStatusLogonFailure       uint32 = 0xC000006D // STATUS_LOGON_FAILURE
	ntStatusNetworkNameDeleted uint32 = 0xC00000C9 // STATUS_NETWORK_NAME_DELETED
	ntStatusBadNetworkName     uint32 = 0xC00000CC // STATUS_BAD_NETWORK_NAME
	ntStatusUserSessionDeleted uint32 = 0xC0000203 // STATUS_USER_SESSION_DELETED
)

// classifyError 给 SMB 协议错误带上重试语义标记：
//   - 认证失败、share 不存在、权限拒绝：确定性失败，重连重试都不改变
//     结果 → MarkPermanent（Downloader / Runner 不做无意义重试）；
//   - session / tree 被服务器删除、transport 层错误：连接代际已坏，
//     重连才有意义 → MarkTransient（配合 teardownSession）；
//   - go-smb2 已把常见 NTSTATUS 映射为 fs.ErrNotExist / fs.ErrPermission
//     等哨兵错误，保持原样透传（IsRetryable 的内建规则将其判为不可
//     重试）；reparse point 拒绝（ErrInvalid）与 ctx 错误原样透传。
func classifyError(err error) error {
	if err == nil {
		return nil
	}
	var re *smb2.ResponseError
	if errors.As(err, &re) {
		switch re.Code {
		case ntStatusLogonFailure, ntStatusAccessDenied, ntStatusBadNetworkName:
			return source.MarkPermanent(err)
		case ntStatusUserSessionDeleted, ntStatusNetworkNameDeleted:
			return source.MarkTransient(err)
		}
		return err
	}
	var te *smb2.TransportError
	if errors.As(err, &te) {
		return source.MarkTransient(err)
	}
	return err
}

// isSessionLost 判定错误是否表示当前连接代际已不可用（后续请求只会
// 继续失败），调用方应 teardownSession 拆除该代际，让下一次操作经
// session 惰性重连——Downloader 的重试因此真正建立新连接，而不是
// 反复使用坏连接。
func isSessionLost(err error) bool {
	if err == nil {
		return false
	}
	var re *smb2.ResponseError
	if errors.As(err, &re) {
		return re.Code == ntStatusUserSessionDeleted || re.Code == ntStatusNetworkNameDeleted
	}
	var te *smb2.TransportError
	if errors.As(err, &te) {
		return true
	}
	// 连接关闭后的在途请求返回拆除回声（EOF / closed 等）。
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, fs.ErrClosed) ||
		errors.Is(err, net.ErrClosed)
}

// normalizeCtxErr 归一取消语义：ctx 取消（用户停止 / attempt 超时）
// 时阻塞中的 SMB 操作可能返回拆除回声而非 context 错误；调用返回时
// ctx 已取消的，必须以 ctx.Err() 优先——Runner 据此把「用户停止」
// 收敛为 canceled 而非误判为 failed。
func normalizeCtxErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// wrapOp 为底层错误补充操作与路径上下文；ctx 错误经 %w 保持可判定。
func wrapOp(op, logicalPath string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("smb %s %s: %w", op, logicalPath, err)
}
