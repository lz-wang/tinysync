package githubrelease

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"tinysync/internal/source"
)

// openAsset 打开 Asset 的二进制下载流（契约见设计文档 §7）：
//   - 始终使用扫描快照确定的 asset ID（GET /releases/assets/{id}），
//     不按 tag + 文件名重新解析下载地址；扫描后 Asset 被删除 → 404
//     permanent，已有本地文件与管理状态保持不变；
//   - Accept: application/octet-stream；GitHub 返回 200 数据流或 302
//     重定向至 CDN，重定向跟随与安全策略（仅 https、host 白名单、
//     跨 host 剥 Authorization）由 http.Client 的 redirectPolicy
//     统一执行；
//   - 不持有 metaMu：下载体传输不占用元数据串行窗口，并发复用上层
//     全局传输并发控制。
//
// 调用方负责关闭返回的 reader；ctx 取消经 request context 中断下载。
func (c *client) openAsset(ctx context.Context, assetID int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+c.apiPath(fmt.Sprintf("/releases/assets/%d", assetID)), nil)
	if err != nil {
		return nil, fmt.Errorf("github: build asset request %d: %w", assetID, err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// ctx 取消优先：上层 Runner 据此把用户停止收敛为 canceled。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("github: open asset %d: %w", assetID, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, classifyStatus(resp, string(body))
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("github: open asset %d: unexpected status %d", assetID, resp.StatusCode)
	}
	return resp.Body, nil
}

// openAssetFrom 打开 Asset 的 Range 下载流（ADR 0010，offset > 0）：
// Range 头由 Go 标准库穿过 302 重定向保持，重定向安全策略（仅 https、
// host 白名单、跨 host 剥 Authorization）继续由 redirectPolicy 统一
// 执行——Token 绝不泄漏给 CDN。响应严格校验：
//
//   - 206 且 Content-Range start==offset、total==快照 Size → 通过。
//     Asset ID 不可变（重新上传产生新 ID），身份由快照 ID + total
//     校验 + 最终 SHA-256（verify_sha256 时）兜底；
//   - 200（CDN / API 忽略 Range）→ ErrResumeUnsupported，保守降级
//     完整下载；
//   - 416（offset 越过当前资源末尾）→ ErrRemoteChanged；
//   - Content-Range 畸形 / 起点错误 / total 不符 → ErrRemoteChanged。
func (c *client) openAssetFrom(ctx context.Context, assetID int64, offset int64, expectedSize int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+c.apiPath(fmt.Sprintf("/releases/assets/%d", assetID)), nil)
	if err != nil {
		return nil, fmt.Errorf("github: build asset request %d: %w", assetID, err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// ctx 取消优先：上层 Runner 据此把用户停止收敛为 canceled。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("github: open asset %d at %d: %w", assetID, offset, err)
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, total, ok := source.ParseContentRange(resp.Header.Get("Content-Range"))
		if !ok {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("github: resume asset %d: %w (malformed Content-Range %q)",
				assetID, source.ErrRemoteChanged, resp.Header.Get("Content-Range"))
		}
		if start != offset {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("github: resume asset %d: %w (Content-Range starts at %d, want %d)",
				assetID, source.ErrRemoteChanged, start, offset)
		}
		if total != expectedSize {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("github: resume asset %d: %w (Content-Range total %d, want %d)",
				assetID, source.ErrRemoteChanged, total, expectedSize)
		}
		return resp.Body, nil
	case http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("github: resume asset %d at %d: %w (server ignored Range)",
			assetID, offset, source.ErrResumeUnsupported)
	case http.StatusRequestedRangeNotSatisfiable:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("github: resume asset %d at %d: %w (offset beyond current resource)",
			assetID, offset, source.ErrRemoteChanged)
	default:
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, classifyStatus(resp, string(body))
	}
}
