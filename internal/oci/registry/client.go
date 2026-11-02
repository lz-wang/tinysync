package registry

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"tinysync/internal/oci/image"
)

// Timeout configuration for registry operations
const (
	connectTimeout                 = 30 * time.Second
	requestTimeout                 = 60 * time.Second
	operationTimeout               = 60 * time.Minute
	defaultPlatformCopyConcurrency = 3
	// copyRetryAttempts 是 registry 写操作的固定重试次数（按镜像/平台粒度，
	// 指数退避），不再作为每任务可调参数
	copyRetryAttempts = 2
)

var ErrPlatformNotFound = errors.New("platform not found")

// Client represents a registry client using go-containerregistry
type Client struct {
	logger         Logger
	transportMu    sync.Mutex
	transportCache map[string]*http.Transport
}

// NewClient creates a new registry client
func NewClient(logger Logger) *Client {
	return &Client{
		logger:         logger,
		transportCache: make(map[string]*http.Transport),
	}
}

func NewCopySession() *CopySession {
	return &CopySession{
		descriptorCache:    make(map[string]sourceDescriptorCacheEntry),
		indexManifestCache: make(map[string]*v1.IndexManifest),
	}
}

// copyResult creates a CopyResult with common fields
func (c *Client) copyResult(src, dst string, success bool, message string, srcMirror string) *image.CopyResult {
	return &image.CopyResult{
		SrcRef:     src,
		DstRef:     dst,
		Success:    success,
		Message:    message,
		UsedMirror: srcMirror,
	}
}

// copyResultError creates a failed CopyResult
func (c *Client) copyResultError(src, dst, errMsg string, srcMirror string) *image.CopyResult {
	return c.copyResult(src, dst, false, errMsg, srcMirror)
}

// constructMirrorRepoName creates a new repository name with the mirror registry
func constructMirrorRepoName(originalRepoName, mirrorRegistry string) string {
	parts := strings.SplitN(originalRepoName, "/", 2)
	var repoPath string
	if len(parts) > 1 {
		repoPath = parts[1]
	} else {
		repoPath = originalRepoName
	}
	return mirrorRegistry + "/" + repoPath
}

func transportCacheKey(regInfo *image.RegistryInfo) string {
	if regInfo == nil {
		return "default"
	}
	return fmt.Sprintf("plain=%t|skip_verify=%t", regInfo.PlainHTTP, regInfo.InsecureSkipVerify)
}

func (c *Client) createTransport(regInfo *image.RegistryInfo) *http.Transport {
	cacheKey := transportCacheKey(regInfo)

	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	if tr, ok := c.transportCache[cacheKey]; ok {
		return tr
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	if regInfo != nil && regInfo.InsecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}
	c.transportCache[cacheKey] = tr
	return tr
}

func createNameOptions(regInfo *image.RegistryInfo) []name.Option {
	if regInfo != nil && regInfo.PlainHTTP {
		return []name.Option{name.Insecure}
	}
	return nil
}

// createRemoteOptions creates remote options for registry operations
func (c *Client) createRemoteOptions(ctx context.Context, regInfo *image.RegistryInfo) []remote.Option {
	var opts []remote.Option
	opts = append(opts, remote.WithContext(ctx))

	if regInfo != nil {
		opts = append(opts, remote.WithTransport(c.createTransport(regInfo)))

		if regInfo.Username != "" || regInfo.Password != "" {
			auth := authn.Basic{
				Username: regInfo.Username,
				Password: regInfo.Password,
			}
			opts = append(opts, remote.WithAuth(&auth))
		}
	}

	return opts
}

func (c *Client) createRemoteOptionsWithMeter(ctx context.Context, regInfo *image.RegistryInfo, meter *trafficMeter) []remote.Option {
	opts := c.createRemoteOptions(ctx, regInfo)
	if meter == nil {
		return opts
	}

	baseTransport := c.createTransport(regInfo)
	opts = append(opts, remote.WithTransport(&meterTransport{
		base:  baseTransport,
		meter: meter,
	}))
	return opts
}

// parseReference parses an image reference with optional tag
func parseReference(repoName, tag string, regInfo *image.RegistryInfo) (name.Reference, error) {
	refStr := repoName + ":" + tag
	return name.ParseReference(refStr, createNameOptions(regInfo)...)
}

func parseRepository(repoName string, regInfo *image.RegistryInfo) (name.Repository, error) {
	return name.NewRepository(repoName, createNameOptions(regInfo)...)
}

func parseDigestReference(repoName, digest string, regInfo *image.RegistryInfo) (name.Reference, error) {
	refStr := repoName + "@" + digest
	return name.ParseReference(refStr, createNameOptions(regInfo)...)
}

func retryBackoffDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}

// taggableIndex implements the Taggable interface for writing an index
type taggableIndex struct {
	bytes     []byte
	manifest  *v1.IndexManifest
	mediaType types.MediaType
}
