package source

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"syscall"
)

// 远端错误分类——v0.9 重试策略的单一判定基础：
//
//	Remote protocol details → Source Adapter → common error semantics → Sync Engine
//
// 协议错误判断只能在 adapter boundary 内完成：各 adapter 用
// MarkTransient / MarkPermanent 给自己认识的协议错误带上分类标记
//（WebDAV 的 HTTP 状态、S3 的 API 错误码、SFTP 的 host key 等），
// 上层（同步引擎 / downloader）只问 IsRetryable，不出现协议分支。

// transientError 标记「瞬时故障，重试有意义」的协议错误。
type transientError struct {
	err error
}

func (e *transientError) Error() string { return e.err.Error() }

func (e *transientError) Unwrap() error { return e.err }

// permanentError 标记「确定性失败，重试无意义」的协议错误（认证、
// 404、host key、路径违规等）。
type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

// MarkTransient 标记 err 为可重试的瞬时错误；nil 透传，已带 permanent
// 标记的错误保持原样（保守判定优先：宁可少重试）。
func MarkTransient(err error) error {
	if err == nil {
		return nil
	}
	if isMarkedPermanent(err) || isMarkedTransient(err) {
		return err
	}
	return &transientError{err: err}
}

// MarkPermanent 标记 err 为不可重试的确定性失败；nil 透传。
func MarkPermanent(err error) error {
	if err == nil {
		return nil
	}
	if isMarkedPermanent(err) {
		return err
	}
	return &permanentError{err: err}
}

// isMarkedTransient 判断错误链上是否带 transient 标记。
func isMarkedTransient(err error) bool {
	var te *transientError
	return errors.As(err, &te)
}

// isMarkedPermanent 判断错误链上是否带 permanent 标记。
func isMarkedPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

// IsRetryable 判定远端错误是否值得重试。规则按优先级：
//  1. context 取消：false——调用方的生命周期控制，不是瞬时故障；
//     DeadlineExceeded：true——可能是单次 attempt 的传输超时，重试
//     有意义；run 级超时 / 取消由调用方先检查自身 context（循环内在
//     每次 retry 前检查 ctx.Err()），不会走到重试；
//  2. adapter boundary 显式标记 permanent：false（401 / 403 / 404、
//     host key mismatch、invalid credentials、路径违规等）；
//  3. adapter boundary 显式标记 transient：true（408 / 429 / 5xx、
//     S3 / SFTP 瞬时服务故障等）；
//  4. 内建规则：本地文件系统错误（permission / ENOSPC / not exist）
//     不重试；net 错误、连接重置、流中断可重试；
//  5. 无法识别的错误默认 transient：同步场景中未知错误几乎全部来自
//     网络路径，与既有「失败后重试」行为一致，重试耗尽后 run 照常
//     收敛为失败。
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if isMarkedPermanent(err) {
		return false
	}
	if isMarkedTransient(err) {
		return true
	}
	// 本地文件系统错误：确定性失败（ENOSPC、权限、路径缺失）。
	if errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, syscall.ENOSPC) {
		return false
	}
	// 传输层与流中断：net 错误（含超时）、EOF、连接关闭。
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, fs.ErrClosed) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE,
			syscall.ECONNABORTED, syscall.ENETRESET, syscall.ENETUNREACH,
			syscall.EHOSTUNREACH, syscall.ENOTCONN, syscall.ETIMEDOUT:
			return true
		}
		return false
	}
	return true
}
