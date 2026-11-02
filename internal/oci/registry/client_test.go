package registry

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"tinysync/internal/oci/image"
)

func TestFindPlatformManifest(t *testing.T) {
	manifest := &v1.IndexManifest{
		Manifests: []v1.Descriptor{
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "amd64",
				},
			},
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("b", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "arm64",
					Variant:      "v8",
				},
			},
		},
	}

	tests := []struct {
		name     string
		platform image.PlatformInfo
		want     string
	}{
		{
			name: "match exact platform",
			platform: image.PlatformInfo{
				OS:           "linux",
				Architecture: "amd64",
			},
			want: strings.Repeat("a", 64),
		},
		{
			name: "normalize case and unknown variant",
			platform: image.PlatformInfo{
				OS:           "LINUX",
				Architecture: "ARM64",
				Variant:      "V8",
			},
			want: strings.Repeat("b", 64),
		},
		{
			name: "return nil when platform not found",
			platform: image.PlatformInfo{
				OS:           "linux",
				Architecture: "arm",
				Variant:      "v7",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findPlatformManifest(manifest, tt.platform)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("findPlatformManifest() = %v, want nil", got.Digest.String())
				}
				return
			}

			if got == nil {
				t.Fatalf("findPlatformManifest() = nil, want digest %s", tt.want)
			}
			if got.Digest.Hex != tt.want {
				t.Fatalf("findPlatformManifest() digest = %s, want %s", got.Digest.Hex, tt.want)
			}
		})
	}
}

func TestNormalizePlatformInfo(t *testing.T) {
	platform := normalizePlatformInfo(image.PlatformInfo{
		OS:           " Linux ",
		Architecture: " ARM64 ",
		Variant:      " unknown ",
	})

	if platform.OS != "linux" {
		t.Fatalf("normalizePlatformInfo().OS = %q, want %q", platform.OS, "linux")
	}
	if platform.Architecture != "arm64" {
		t.Fatalf("normalizePlatformInfo().Architecture = %q, want %q", platform.Architecture, "arm64")
	}
	if platform.Variant != "" {
		t.Fatalf("normalizePlatformInfo().Variant = %q, want empty string", platform.Variant)
	}
}

func TestCalculateManifestImageSize(t *testing.T) {
	manifest := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"size": 512,
			"digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"layers": [
			{
				"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
				"size": 1024,
				"digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			},
			{
				"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
				"size": 2048,
				"digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
			}
		]
	}`)

	size, err := calculateManifestImageSize(manifest)
	if err != nil {
		t.Fatalf("calculateManifestImageSize() error = %v", err)
	}

	if size != 3584 {
		t.Fatalf("calculateManifestImageSize() = %d, want %d", size, 3584)
	}
}

func TestResolveCopyPlatforms(t *testing.T) {
	t.Run("只使用任务级平台配置", func(t *testing.T) {
		platforms := resolveCopyPlatforms([]PlatformConfig{
			{Architecture: "ARM64", Variant: "V8"},
		})

		if len(platforms) != 1 {
			t.Fatalf("resolveCopyPlatforms() returned %d platforms, want 1", len(platforms))
		}
		if platforms[0].OS != "linux" || platforms[0].Architecture != "arm64" || platforms[0].Variant != "v8" {
			t.Fatalf("resolveCopyPlatforms() = %+v, want linux/arm64/v8", platforms[0])
		}
	})

	t.Run("未配置平台时同步全部平台", func(t *testing.T) {
		platforms := resolveCopyPlatforms(nil)

		if len(platforms) != 0 {
			t.Fatalf("resolveCopyPlatforms(nil) = %+v, want empty (copy all platforms)", platforms)
		}
	})
}

func TestResolvePlatformCopyConcurrency(t *testing.T) {
	tests := []struct {
		name          string
		platformCount int
		want          int
	}{
		{
			name:          "zero platform",
			platformCount: 0,
			want:          0,
		},
		{
			name:          "single platform",
			platformCount: 1,
			want:          1,
		},
		{
			name:          "small batch uses platform count",
			platformCount: 2,
			want:          2,
		},
		{
			name:          "large batch caps concurrency",
			platformCount: 6,
			want:          defaultPlatformCopyConcurrency,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePlatformCopyConcurrency(tt.platformCount)
			if got != tt.want {
				t.Fatalf("resolvePlatformCopyConcurrency(%d) = %d, want %d", tt.platformCount, got, tt.want)
			}
		})
	}
}

func TestCollectPlatformsFromIndexManifest(t *testing.T) {
	srcManifest := &v1.IndexManifest{
		Manifests: []v1.Descriptor{
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "amd64",
				},
			},
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("b", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "amd64",
				},
			},
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("c", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "arm64",
					Variant:      "v8",
				},
			},
		},
	}

	platforms := collectPlatformsFromIndexManifest(srcManifest)
	if len(platforms) != 2 {
		t.Fatalf("collectPlatformsFromIndexManifest() returned %d platforms, want 2", len(platforms))
	}
	if platforms[0].OS != "linux" || platforms[0].Architecture != "amd64" || platforms[0].Variant != "" {
		t.Fatalf("first platform = %+v, want linux/amd64", platforms[0])
	}
	if platforms[1].OS != "linux" || platforms[1].Architecture != "arm64" || platforms[1].Variant != "v8" {
		t.Fatalf("second platform = %+v, want linux/arm64/v8", platforms[1])
	}
}

func TestCollectMatchingPlatformManifests(t *testing.T) {
	srcManifest := &v1.IndexManifest{
		Manifests: []v1.Descriptor{
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "amd64",
				},
			},
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("b", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "arm64",
					Variant:      "v8",
				},
			},
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("c", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "ppc64le",
				},
			},
		},
	}

	platforms := []PlatformConfig{
		{OS: "linux", Architecture: "amd64"},
		{OS: "linux", Architecture: "arm64", Variant: "v8"},
	}

	manifests, names := collectMatchingPlatformManifests(srcManifest, platforms)
	if len(manifests) != 2 {
		t.Fatalf("collectMatchingPlatformManifests() returned %d manifests, want 2", len(manifests))
	}
	if len(names) != 2 {
		t.Fatalf("collectMatchingPlatformManifests() returned %d names, want 2", len(names))
	}
	if manifests[0].Digest.Hex != strings.Repeat("a", 64) || names[0] != "linux/amd64" {
		t.Fatalf("first matched manifest = %s %s, want amd64", manifests[0].Digest.Hex, names[0])
	}
	if manifests[1].Digest.Hex != strings.Repeat("b", 64) || names[1] != "linux/arm64/v8" {
		t.Fatalf("second matched manifest = %s %s, want arm64/v8", manifests[1].Digest.Hex, names[1])
	}
}

func TestDedupePlatformManifests(t *testing.T) {
	manifests := []v1.Descriptor{
		{
			Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
			Platform: &v1.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
		},
		{
			Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
			Platform: &v1.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
		},
		{
			Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("b", 64)},
			Platform: &v1.Platform{
				OS:           "linux",
				Architecture: "arm64",
			},
		},
	}

	platforms := []string{"linux/amd64", "linux/amd64", "linux/arm64"}
	gotManifests, gotPlatforms, duplicateCount := dedupePlatformManifests(manifests, platforms)

	if duplicateCount != 1 {
		t.Fatalf("dedupePlatformManifests() duplicateCount = %d, want 1", duplicateCount)
	}
	if len(gotManifests) != 2 {
		t.Fatalf("dedupePlatformManifests() manifest count = %d, want 2", len(gotManifests))
	}
	if len(gotPlatforms) != 2 {
		t.Fatalf("dedupePlatformManifests() platform count = %d, want 2", len(gotPlatforms))
	}
	if gotPlatforms[0] != "linux/amd64" || gotPlatforms[1] != "linux/arm64" {
		t.Fatalf("dedupePlatformManifests() platforms = %v, want [linux/amd64 linux/arm64]", gotPlatforms)
	}
}

func TestBuildImageIndexDigestChangesWithManifestSet(t *testing.T) {
	manifests := []v1.Descriptor{
		{
			Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
			Platform: &v1.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
		},
		{
			Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("b", 64)},
			Platform: &v1.Platform{
				OS:           "linux",
				Architecture: "arm64",
				Variant:      "v8",
			},
		},
	}

	_, _, fullDigest, err := buildImageIndex(manifests)
	if err != nil {
		t.Fatalf("buildImageIndex(full) error = %v", err)
	}

	_, _, partialDigest, err := buildImageIndex(manifests[:1])
	if err != nil {
		t.Fatalf("buildImageIndex(partial) error = %v", err)
	}

	if fullDigest == partialDigest {
		t.Fatalf("buildImageIndex() digest should change when manifest set changes")
	}
}

func TestBuildImageIndexWithMediaType(t *testing.T) {
	manifests := []v1.Descriptor{
		{
			Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
			Platform: &v1.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
		},
	}

	ociIndex, _, ociDigest, err := buildImageIndexWithMediaType(manifests, types.OCIImageIndex)
	if err != nil {
		t.Fatalf("buildImageIndexWithMediaType(OCI) error = %v", err)
	}
	if ociIndex.MediaType != types.OCIImageIndex {
		t.Fatalf("buildImageIndexWithMediaType(OCI) mediaType = %s, want %s", ociIndex.MediaType, types.OCIImageIndex)
	}

	dockerIndex, _, dockerDigest, err := buildImageIndexWithMediaType(manifests, types.DockerManifestList)
	if err != nil {
		t.Fatalf("buildImageIndexWithMediaType(Docker) error = %v", err)
	}
	if dockerIndex.MediaType != types.DockerManifestList {
		t.Fatalf("buildImageIndexWithMediaType(Docker) mediaType = %s, want %s", dockerIndex.MediaType, types.DockerManifestList)
	}
	if dockerDigest == ociDigest {
		t.Fatalf("buildImageIndexWithMediaType() digest should change when media type changes")
	}
}

func TestCreateTransportUsesCache(t *testing.T) {
	client := NewClient(nopLogger{})

	defaultRegistry := &image.RegistryInfo{
		InsecureSkipVerify: false,
	}
	insecureRegistry := &image.RegistryInfo{
		InsecureSkipVerify: true,
	}

	first := client.createTransport(defaultRegistry)
	second := client.createTransport(defaultRegistry)
	if first != second {
		t.Fatalf("createTransport() should reuse cached transport for same config")
	}

	third := client.createTransport(insecureRegistry)
	if third == first {
		t.Fatalf("createTransport() should return a different transport for different tls settings")
	}
}

func TestCopySessionCachesSourceMetadata(t *testing.T) {
	session := NewCopySession()
	cacheKey := "docker.io/library/nginx:latest|mirror=true"
	desc := &remote.Descriptor{
		Descriptor: v1.Descriptor{
			Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("d", 64)},
		},
	}

	if _, _, ok := session.getSourceDescriptor(cacheKey); ok {
		t.Fatalf("expected empty descriptor cache before store")
	}

	session.storeSourceDescriptor(cacheKey, desc, "docker.1ms.run")
	gotDesc, gotMirror, ok := session.getSourceDescriptor(cacheKey)
	if !ok {
		t.Fatalf("expected descriptor cache hit after store")
	}
	if gotDesc != desc {
		t.Fatalf("descriptor cache returned unexpected descriptor pointer")
	}
	if gotMirror != "docker.1ms.run" {
		t.Fatalf("descriptor cache mirror = %q, want %q", gotMirror, "docker.1ms.run")
	}

	manifest := &v1.IndexManifest{
		Manifests: []v1.Descriptor{
			{
				Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("e", 64)},
				Platform: &v1.Platform{
					OS:           "linux",
					Architecture: "amd64",
				},
			},
		},
	}
	if _, ok := session.getIndexManifest(cacheKey); ok {
		t.Fatalf("expected empty index manifest cache before store")
	}

	session.storeIndexManifest(cacheKey, manifest)
	gotManifest, ok := session.getIndexManifest(cacheKey)
	if !ok {
		t.Fatalf("expected index manifest cache hit after store")
	}
	if gotManifest != manifest {
		t.Fatalf("index manifest cache returned unexpected manifest pointer")
	}
}

func TestShouldFallbackToGetAfterHeadError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error does not fallback",
			err:  nil,
			want: false,
		},
		{
			name: "404 does not fallback",
			err: &transport.Error{
				StatusCode: 404,
				Errors:     []transport.Diagnostic{{Message: "not found"}},
			},
			want: false,
		},
		{
			name: "other head error falls back",
			err:  fmt.Errorf("head method not allowed"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldFallbackToGetAfterHeadError(tt.err)
			if got != tt.want {
				t.Fatalf("shouldFallbackToGetAfterHeadError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestShouldRetryMultiPlatformIndexWithCompatibility(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "artifact not found retries with compatibility flow",
			err: &transport.Error{
				StatusCode: http.StatusNotFound,
				Errors:     []transport.Diagnostic{{Message: "artifact repo@sha256:abc not found"}},
			},
			want: true,
		},
		{
			name: "manifest unknown retries with compatibility flow",
			err:  fmt.Errorf("MANIFEST_UNKNOWN: manifest unknown"),
			want: true,
		},
		{
			name: "forbidden does not retry with compatibility flow",
			err: &transport.Error{
				StatusCode: http.StatusForbidden,
				Errors:     []transport.Diagnostic{{Message: "forbidden"}},
			},
			want: false,
		},
		{
			name: "generic network error does not retry with compatibility flow",
			err:  fmt.Errorf("temporary network failure"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRetryMultiPlatformIndexWithCompatibility(tt.err)
			if got != tt.want {
				t.Fatalf("shouldRetryMultiPlatformIndexWithCompatibility(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
