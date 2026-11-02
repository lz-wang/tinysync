package registry

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"tinysync/internal/oci/image"
)

// anonymousMirrorInfo 是访问 mirror 时使用的 registry 信息。
// mirror 仅支持匿名访问，不继承源 registry 的凭据——把 Docker Hub 的用户名密码
// 发给第三方 mirror host 是不合理的凭据边界。
var anonymousMirrorInfo = &image.RegistryInfo{}

// getDescriptorWithMirror 按 resolver 给出的源端访问策略取源镜像描述符：
// 普通 registry 走 primary → mirrors；策略要求跳过 primary（docker.io 特判，
// 由 Resolver.SourcePolicy 给出）时 mirrors 优先、全部失败后回退 primary
// （mirror 是加速器而不是硬依赖）。目标端不走本函数——destination 永不 mirror。
func (c *Client) getDescriptorWithMirror(ctx context.Context, repoName, tag string, regInfo *image.RegistryInfo, policy SourcePolicy, repoRef string) (*remote.Descriptor, string, error) {
	attemptedRegistries := []string{}
	var lastErr error

	if !policy.SkipPrimary {
		c.logger.Infof("attempting to access primary registry: registry=%s", repoRef)
		attemptedRegistries = append(attemptedRegistries, repoRef)

		ref, err := parseReference(repoName, tag, regInfo)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse reference: %w", err)
		}

		opts := c.createRemoteOptions(ctx, regInfo)
		desc, err := remote.Get(ref, opts...)
		if err == nil {
			c.logger.Infof("successfully connected to primary registry: registry=%s", repoRef)
			return desc, "", nil
		}
		lastErr = err
		c.logger.Warnf("failed to access primary registry: registry=%s error=%v", repoRef, err)
	} else {
		c.logger.Infof("skipping primary registry for %s, will use mirrors directly", repoRef)
	}

	// Try mirrors
	if len(policy.Mirrors) > 0 {
		for i, mirrorAddr := range policy.Mirrors {
			attemptedRegistries = append(attemptedRegistries, mirrorAddr)
			mirrorRepoName := constructMirrorRepoName(repoName, mirrorAddr)

			c.logger.Infof("attempting to use mirror: mirror_index=%d mirror=%s", i+1, mirrorAddr)

			ref, err := parseReference(mirrorRepoName, tag, anonymousMirrorInfo)
			if err != nil {
				lastErr = err
				continue
			}

			opts := c.createRemoteOptions(ctx, anonymousMirrorInfo)
			desc, err := remote.Get(ref, opts...)
			if err == nil {
				c.logger.Infof("successfully connected to mirror: mirror_index=%d mirror=%s", i+1, mirrorAddr)
				return desc, mirrorAddr, nil
			}
			lastErr = err
			c.logger.Warnf("failed to connect to mirror: mirror_index=%d mirror=%s error=%v", i+1, mirrorAddr, err)
		}
	}

	// SkipPrimary（docker.io）：mirrors 优先，但 mirror 不是硬依赖——
	// 所有 mirror 失败（或根本没配 mirror）后仍回退 primary，符合
	// "mirror 是加速器而不是单点" 的语义
	if policy.SkipPrimary {
		c.logger.Infof("mirrors unavailable or not configured, falling back to primary registry: registry=%s", repoRef)
		attemptedRegistries = append(attemptedRegistries, repoRef)

		ref, err := parseReference(repoName, tag, regInfo)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse reference: %w", err)
		}

		opts := c.createRemoteOptions(ctx, regInfo)
		desc, err := remote.Get(ref, opts...)
		if err == nil {
			c.logger.Infof("successfully connected to primary registry (fallback): registry=%s", repoRef)
			return desc, "", nil
		}
		lastErr = err
		c.logger.Warnf("failed to access primary registry (fallback): registry=%s error=%v", repoRef, err)
	}

	return nil, "", fmt.Errorf("failed to access registry or mirrors (tried: %v): %w", attemptedRegistries, lastErr)
}

// getDestinationRef 解析目标镜像引用。Mirror 只作为 source pull 加速器，
// 目标永远只使用配置的 destination registry，不存在 destination mirror 回退。
func (c *Client) getDestinationRef(dst *image.DstImage) (name.Tag, error) {
	var emptyTag name.Tag
	ref, err := parseReference(dst.RepoName, dst.Tag, dst.Registry)
	if err != nil {
		return emptyTag, fmt.Errorf("failed to parse destination reference: %w", err)
	}
	tag, ok := ref.(name.Tag)
	if !ok {
		return emptyTag, fmt.Errorf("destination reference %s is not a tag", ref)
	}
	return tag, nil
}

func shouldFallbackToGetAfterHeadError(err error) bool {
	return err != nil && !isNotFoundError(err)
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// Check for transport errors
	if te, ok := err.(*transport.Error); ok {
		return te.StatusCode == http.StatusNotFound
	}
	return strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404")
}
