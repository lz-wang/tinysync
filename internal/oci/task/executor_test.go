package task

import (
	"testing"

	"tinysync/internal/oci/image"
	"tinysync/internal/oci/registry"
)

func TestBuildDestinationPath(t *testing.T) {
	tests := []struct {
		name            string
		sourceImageRef  *image.ImageReference // 解析后的源镜像引用（仅用于生成 sourceFullRepo）
		sourceFullRepo  string                // 源完整仓库路径（可能含编码后的 separator），为空则从 sourceImageRef 生成
		sourceRegConfig *registry.RegistryConfig
		dstRegConfig    *registry.RegistryConfig
		wantDestRepo    string // 期望的目标仓库路径（不含 tag）
	}{
		// ===== 场景1：src 有 path_transform（解码）=====

		{
			name: "场景1_解码_非docker.io镜像",
			sourceImageRef: &image.ImageReference{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				RepoName:  "lscr.io---linuxserver---onlyoffice",
				Tag:       "latest",
			},
			sourceFullRepo: "docker.cnb.cool/lzwang/mirrors/lscr.io---linuxserver---onlyoffice",
			sourceRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "decode",
				},
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry: "zot.lzwang.com",
			},
			wantDestRepo: "zot.lzwang.com/lscr.io/linuxserver/onlyoffice",
		},
		{
			name: "场景1_解码_ghcr.io镜像",
			sourceImageRef: &image.ImageReference{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				RepoName:  "ghcr.io---steveiliop56---tinyauth",
				Tag:       "latest",
			},
			sourceFullRepo: "docker.cnb.cool/lzwang/mirrors/ghcr.io---steveiliop56---tinyauth",
			sourceRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry: "zot.lzwang.com",
			},
			wantDestRepo: "zot.lzwang.com/ghcr.io/steveiliop56/tinyauth",
		},
		{
			name: "场景1_解码_目标带namespace_必须保留namespace",
			sourceImageRef: &image.ImageReference{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				RepoName:  "ghcr.io---xxx---yyy",
				Tag:       "latest",
			},
			sourceFullRepo: "docker.cnb.cool/lzwang/mirrors/ghcr.io---xxx---yyy",
			sourceRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "decode",
				},
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "harbor.example.com",
				Namespace: "team",
			},
			wantDestRepo: "harbor.example.com/team/ghcr.io/xxx/yyy",
		},

		// ===== 场景2：dst 有 path_transform（编码）=====

		{
			name:           "场景2_编码_docker.io_library镜像",
			sourceImageRef: image.ParseReference("nginx:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "encode",
				},
				StripRegistries: []string{"docker.io", "docker.io/library"},
			},
			wantDestRepo: "docker.cnb.cool/lzwang/mirrors/nginx",
		},
		{
			name:           "场景2_编码_docker.io_library镜像_alpine",
			sourceImageRef: image.ParseReference("alpine:3"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
				StripRegistries: []string{"docker.io", "docker.io/library"},
			},
			wantDestRepo: "docker.cnb.cool/lzwang/mirrors/alpine",
		},
		{
			name:           "场景2_编码_docker.io_非library镜像",
			sourceImageRef: image.ParseReference("valkey/valkey:8"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "encode",
				},
				StripRegistries: []string{"docker.io", "docker.io/library"},
			},
			wantDestRepo: "docker.cnb.cool/lzwang/mirrors/valkey---valkey",
		},
		{
			name:           "场景2_编码_ghcr.io镜像",
			sourceImageRef: image.ParseReference("ghcr.io/steveiliop56/tinyauth:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "ghcr.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "encode",
				},
			},
			wantDestRepo: "docker.cnb.cool/lzwang/mirrors/ghcr.io---steveiliop56---tinyauth",
		},
		{
			name:           "场景2_编码_quay.io镜像",
			sourceImageRef: image.ParseReference("quay.io/minio/minio:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "quay.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "encode",
				},
			},
			wantDestRepo: "docker.cnb.cool/lzwang/mirrors/quay.io---minio---minio",
		},
		{
			name:           "场景2_编码_无namespace的dst",
			sourceImageRef: image.ParseReference("ghcr.io/xxx/yyy:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "ghcr.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry: "my-registry.com",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "encode",
				},
			},
			wantDestRepo: "my-registry.com/ghcr.io---xxx---yyy",
		},

		// ===== 场景2b：dst encode + strip_registries（移除非 docker.io 源 registry 前缀）=====

		{
			name:           "场景2b_编码_strip_zot前缀",
			sourceImageRef: image.ParseReference("zot.lzwang.com/docker.litellm.ai/berriai/litellm:main-stable"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "zot.lzwang.com",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "crpi-xxx.cn-chengdu.personal.cr.aliyuncs.com",
				Namespace: "lzwang-mirror",
				PathTransform: &registry.PathTransformConfig{
					Separator: "-slash-",
					Mode:      "encode",
				},
				StripRegistries: []string{"docker.io", "docker.io/library", "zot.lzwang.com"},
			},
			wantDestRepo: "crpi-xxx.cn-chengdu.personal.cr.aliyuncs.com/lzwang-mirror/docker.litellm.ai-slash-berriai-slash-litellm",
		},
		{
			name:           "场景2b_编码_strip_registries无匹配_保留完整路径",
			sourceImageRef: image.ParseReference("ghcr.io/xxx/yyy:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "ghcr.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "encode",
				},
				StripRegistries: []string{"docker.io", "zot.lzwang.com"},
			},
			wantDestRepo: "docker.cnb.cool/lzwang/mirrors/ghcr.io---xxx---yyy",
		},
		{
			name: "场景2b_编码_strip_cnb_namespace前缀",
			sourceImageRef: &image.ImageReference{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				RepoName:  "ghcr.io---xxx---yyy",
				Tag:       "latest",
			},
			sourceFullRepo: "docker.cnb.cool/lzwang/mirrors/ghcr.io---xxx---yyy",
			sourceRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "other-registry.com",
				Namespace: "ns",
				PathTransform: &registry.PathTransformConfig{
					Separator: "-",
					Mode:      "encode",
				},
				StripRegistries: []string{"docker.cnb.cool/lzwang/mirrors"},
			},
			wantDestRepo: "other-registry.com/ns/ghcr.io-xxx-yyy",
		},

		// ===== 场景3：dst 配置 strip_registries（无 path_transform 编码）=====

		{
			name:           "场景3_strip_registries_docker.io_library镜像",
			sourceImageRef: image.ParseReference("nginx:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:        "zot.lzwang.com",
				StripRegistries: []string{"docker.io", "docker.io/library"},
			},
			wantDestRepo: "zot.lzwang.com/nginx",
		},
		{
			name:           "场景3_strip_registries_docker.io_library镜像_alpine",
			sourceImageRef: image.ParseReference("alpine:3"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:        "zot.lzwang.com",
				StripRegistries: []string{"docker.io", "docker.io/library"},
			},
			wantDestRepo: "zot.lzwang.com/alpine",
		},
		{
			name:           "场景3_strip_registries_docker.io_非library镜像",
			sourceImageRef: image.ParseReference("valkey/valkey:8"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:        "zot.lzwang.com",
				StripRegistries: []string{"docker.io", "docker.io/library"},
			},
			wantDestRepo: "zot.lzwang.com/valkey/valkey",
		},
		{
			name:           "场景3_strip_registries_zot前缀",
			sourceImageRef: image.ParseReference("zot.lzwang.com/docker.litellm.ai/berriai/litellm:main-stable"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "zot.lzwang.com",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:        "harbor.example.com",
				StripRegistries: []string{"zot.lzwang.com"},
			},
			wantDestRepo: "harbor.example.com/docker.litellm.ai/berriai/litellm",
		},
		{
			name:           "场景3_strip_registries_目标带namespace_必须保留namespace",
			sourceImageRef: image.ParseReference("nginx:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:        "crpi-xxx.cn-chengdu.personal.cr.aliyuncs.com",
				Namespace:       "lzwang-mirror",
				StripRegistries: []string{"docker.io", "docker.io/library"},
			},
			wantDestRepo: "crpi-xxx.cn-chengdu.personal.cr.aliyuncs.com/lzwang-mirror/nginx",
		},

		// ===== 场景4（默认）：保留完整源路径 =====

		{
			name:           "场景4_默认_docker.io_library镜像",
			sourceImageRef: image.ParseReference("nginx:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry: "zot.lzwang.com",
			},
			wantDestRepo: "zot.lzwang.com/docker.io/library/nginx",
		},
		{
			name:           "场景4_默认_docker.io_非library镜像",
			sourceImageRef: image.ParseReference("valkey/valkey:8"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry: "zot.lzwang.com",
			},
			wantDestRepo: "zot.lzwang.com/docker.io/valkey/valkey",
		},
		{
			name:           "场景4_默认_ghcr.io镜像",
			sourceImageRef: image.ParseReference("ghcr.io/steveiliop56/tinyauth:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "ghcr.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry: "zot.lzwang.com",
			},
			wantDestRepo: "zot.lzwang.com/ghcr.io/steveiliop56/tinyauth",
		},
		{
			name:           "场景4_默认_quay.io镜像",
			sourceImageRef: image.ParseReference("quay.io/minio/minio:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "quay.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry: "zot.lzwang.com",
			},
			wantDestRepo: "zot.lzwang.com/quay.io/minio/minio",
		},
		{
			name:           "场景4_默认_strip_registries_非匹配源不受影响",
			sourceImageRef: image.ParseReference("ghcr.io/xxx/yyy:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "ghcr.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:        "zot.lzwang.com",
				StripRegistries: []string{"docker.io", "docker.io/library"},
			},
			wantDestRepo: "zot.lzwang.com/ghcr.io/xxx/yyy",
		},
		{
			name:           "场景4_默认_目标带namespace_必须保留namespace",
			sourceImageRef: image.ParseReference("nginx:latest"),
			sourceRegConfig: &registry.RegistryConfig{
				Registry: "docker.io",
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "harbor.example.com",
				Namespace: "team",
			},
			wantDestRepo: "harbor.example.com/team/docker.io/library/nginx",
		},

		// ===== 场景5：src 解码 + dst 编码（两者都需要 path_transform）=====

		{
			name: "场景5_源解码+目标编码_简单镜像",
			sourceImageRef: &image.ImageReference{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				RepoName:  "filebrowser---filebrowser",
				Tag:       "latest",
			},
			sourceFullRepo: "docker.cnb.cool/lzwang/mirrors/filebrowser---filebrowser",
			sourceRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "crpi-pxqkj4rq42shx252.cn-chengdu.personal.cr.aliyuncs.com",
				Namespace: "lzwang-mirror",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			wantDestRepo: "crpi-pxqkj4rq42shx252.cn-chengdu.personal.cr.aliyuncs.com/lzwang-mirror/filebrowser---filebrowser",
		},
		{
			name: "场景5_源解码+目标编码_含registry前缀",
			sourceImageRef: &image.ImageReference{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				RepoName:  "lscr.io---linuxserver---onlyoffice",
				Tag:       "latest",
			},
			sourceFullRepo: "docker.cnb.cool/lzwang/mirrors/lscr.io---linuxserver---onlyoffice",
			sourceRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "crpi-pxqkj4rq42shx252.cn-chengdu.personal.cr.aliyuncs.com",
				Namespace: "lzwang-mirror",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			wantDestRepo: "crpi-pxqkj4rq42shx252.cn-chengdu.personal.cr.aliyuncs.com/lzwang-mirror/lscr.io---linuxserver---onlyoffice",
		},
		{
			name: "场景5_源解码+目标编码_docker.io_library镜像",
			sourceImageRef: &image.ImageReference{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				RepoName:  "nginx",
				Tag:       "latest",
			},
			sourceFullRepo: "docker.cnb.cool/lzwang/mirrors/nginx",
			sourceRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry:  "other-registry.com",
				Namespace: "ns",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			wantDestRepo: "other-registry.com/ns/nginx",
		},
		{
			name: "场景5_源解码+目标编码_无namespace目标",
			sourceImageRef: &image.ImageReference{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				RepoName:  "ghcr.io---xxx---yyy",
				Tag:       "latest",
			},
			sourceFullRepo: "docker.cnb.cool/lzwang/mirrors/ghcr.io---xxx---yyy",
			sourceRegConfig: &registry.RegistryConfig{
				Registry:  "docker.cnb.cool",
				Namespace: "lzwang/mirrors",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			dstRegConfig: &registry.RegistryConfig{
				Registry: "other-registry.com",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "encode",
				},
			},
			wantDestRepo: "other-registry.com/ghcr.io---xxx---yyy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sourceFullRepoName := tt.sourceFullRepo
			if sourceFullRepoName == "" {
				sourceFullRepoName = tt.sourceImageRef.FullRepoName()
			}

			got := buildDestinationPath(sourceFullRepoName, tt.sourceRegConfig, tt.dstRegConfig)
			if got != tt.wantDestRepo {
				t.Errorf("buildDestinationPath() = %q, want %q", got, tt.wantDestRepo)
			}
		})
	}
}

func TestTransformPathForEncode(t *testing.T) {
	tests := []struct {
		name      string
		repoPath  string
		separator string
		want      string
	}{
		{
			name:      "Simple path",
			repoPath:  "lscr.io/linuxserver/onlyoffice",
			separator: "---",
			want:      "lscr.io---linuxserver---onlyoffice",
		},
		{
			name:      "Nested path",
			repoPath:  "registry.k8s.io/kube-scheduler-image/kube-scheduler",
			separator: "---",
			want:      "registry.k8s.io---kube-scheduler-image---kube-scheduler",
		},
		{
			name:      "No slashes",
			repoPath:  "nginx",
			separator: "---",
			want:      "nginx",
		},
		{
			name:      "Different separator",
			repoPath:  "docker.io/library/nginx",
			separator: "___",
			want:      "docker.io___library___nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transformPathForEncode(tt.repoPath, tt.separator)
			if got != tt.want {
				t.Errorf("transformPathForEncode(%q, %q) = %q, want %q", tt.repoPath, tt.separator, got, tt.want)
			}
		})
	}
}

func TestTransformPathForDecode(t *testing.T) {
	tests := []struct {
		name      string
		repoPath  string
		separator string
		want      string
	}{
		{
			name:      "Simple encoded path",
			repoPath:  "lscr.io---linuxserver---onlyoffice",
			separator: "---",
			want:      "lscr.io/linuxserver/onlyoffice",
		},
		{
			name:      "Nested encoded path",
			repoPath:  "registry.k8s.io---kube-scheduler-image---kube-scheduler",
			separator: "---",
			want:      "registry.k8s.io/kube-scheduler-image/kube-scheduler",
		},
		{
			name:      "No separator",
			repoPath:  "nginx",
			separator: "---",
			want:      "nginx",
		},
		{
			name:      "Different separator",
			repoPath:  "docker.io___library___nginx",
			separator: "___",
			want:      "docker.io/library/nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transformPathForDecode(tt.repoPath, tt.separator)
			if got != tt.want {
				t.Errorf("transformPathForDecode(%q, %q) = %q, want %q", tt.repoPath, tt.separator, got, tt.want)
			}
		})
	}
}

func TestNeedsPathTransform(t *testing.T) {
	tests := []struct {
		name       string
		regConfig  *registry.RegistryConfig
		wantDecode bool
		wantEncode bool
	}{
		{
			name:       "nil config",
			regConfig:  nil,
			wantDecode: false,
			wantEncode: false,
		},
		{
			name: "nil path transform",
			regConfig: &registry.RegistryConfig{
				Name:     "test",
				Registry: "test.com",
			},
			wantDecode: false,
			wantEncode: false,
		},
		{
			name: "mode encode",
			regConfig: &registry.RegistryConfig{
				Name: "test",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "encode",
				},
			},
			wantDecode: false,
			wantEncode: true,
		},
		{
			name: "mode decode",
			regConfig: &registry.RegistryConfig{
				Name: "test",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "decode",
				},
			},
			wantDecode: true,
			wantEncode: false,
		},
		{
			name: "mode both",
			regConfig: &registry.RegistryConfig{
				Name: "test",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "both",
				},
			},
			wantDecode: true,
			wantEncode: true,
		},
		{
			name: "empty mode defaults to both",
			regConfig: &registry.RegistryConfig{
				Name: "test",
				PathTransform: &registry.PathTransformConfig{
					Separator: "---",
					Mode:      "",
				},
			},
			wantDecode: true,
			wantEncode: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDecode := needsPathTransformDecode(tt.regConfig)
			gotEncode := needsPathTransformEncode(tt.regConfig)
			if gotDecode != tt.wantDecode {
				t.Errorf("needsPathTransformDecode() = %v, want %v", gotDecode, tt.wantDecode)
			}
			if gotEncode != tt.wantEncode {
				t.Errorf("needsPathTransformEncode() = %v, want %v", gotEncode, tt.wantEncode)
			}
		})
	}
}

func TestFindStripPrefix(t *testing.T) {
	tests := []struct {
		name            string
		sourceFullRepo  string
		stripRegistries []string
		want            string
	}{
		{
			name:            "匹配docker.io最长前缀docker.io/library",
			sourceFullRepo:  "docker.io/library/nginx",
			stripRegistries: []string{"docker.io", "docker.io/library"},
			want:            "docker.io/library",
		},
		{
			name:            "匹配docker.io而非docker.io/library",
			sourceFullRepo:  "docker.io/valkey/valkey",
			stripRegistries: []string{"docker.io", "docker.io/library"},
			want:            "docker.io",
		},
		{
			name:            "匹配zot前缀",
			sourceFullRepo:  "zot.lzwang.com/docker.litellm.ai/berriai/litellm",
			stripRegistries: []string{"docker.io", "zot.lzwang.com"},
			want:            "zot.lzwang.com",
		},
		{
			name:            "匹配含namespace的前缀",
			sourceFullRepo:  "docker.cnb.cool/lzwang/mirrors/ghcr.io---xxx---yyy",
			stripRegistries: []string{"docker.cnb.cool/lzwang/mirrors"},
			want:            "docker.cnb.cool/lzwang/mirrors",
		},
		{
			name:            "无匹配",
			sourceFullRepo:  "ghcr.io/xxx/yyy",
			stripRegistries: []string{"docker.io", "zot.lzwang.com"},
			want:            "",
		},
		{
			name:            "空列表",
			sourceFullRepo:  "docker.io/library/nginx",
			stripRegistries: nil,
			want:            "",
		},
		{
			name:            "前缀需要精确匹配不能部分匹配",
			sourceFullRepo:  "zot.lzwang.com/docker.litellm.ai/berriai/litellm",
			stripRegistries: []string{"zot.lzwang"},
			want:            "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findStripPrefix(tt.sourceFullRepo, tt.stripRegistries)
			if got != tt.want {
				t.Errorf("findStripPrefix(%q, %v) = %q, want %q", tt.sourceFullRepo, tt.stripRegistries, got, tt.want)
			}
		})
	}
}

func TestParseImageReference(t *testing.T) {
	tests := []struct {
		name          string
		ref           string
		wantRegistry  string
		wantNamespace string
		wantRepoName  string
		wantTag       string
		wantNil       bool
	}{
		{
			name:          "Docker Hub official image",
			ref:           "nginx:latest",
			wantRegistry:  "docker.io",
			wantNamespace: "library",
			wantRepoName:  "nginx",
			wantTag:       "latest",
		},
		{
			name:          "Docker Hub user image",
			ref:           "openlistteam/openlist:latest",
			wantRegistry:  "docker.io",
			wantNamespace: "openlistteam",
			wantRepoName:  "openlist",
			wantTag:       "latest",
		},
		{
			name:          "Docker Hub nested namespace",
			ref:           "org/suborg/image:v1.0",
			wantRegistry:  "docker.io",
			wantNamespace: "org/suborg",
			wantRepoName:  "image",
			wantTag:       "v1.0",
		},
		{
			name:          "Private registry with namespace",
			ref:           "registry.example.com/myuser/myimage:v1",
			wantRegistry:  "registry.example.com",
			wantNamespace: "myuser",
			wantRepoName:  "myimage",
			wantTag:       "v1",
		},
		{
			name:          "Private registry without namespace",
			ref:           "registry.example.com/myimage:v1",
			wantRegistry:  "registry.example.com",
			wantNamespace: "",
			wantRepoName:  "myimage",
			wantTag:       "v1",
		},
		{
			name:          "Localhost registry",
			ref:           "localhost:5000/myimage:latest",
			wantRegistry:  "localhost:5000",
			wantNamespace: "",
			wantRepoName:  "myimage",
			wantTag:       "latest",
		},
		{
			name:          "IP registry",
			ref:           "192.168.2.158:5000/namespace/image:tag",
			wantRegistry:  "192.168.2.158:5000",
			wantNamespace: "namespace",
			wantRepoName:  "image",
			wantTag:       "tag",
		},
		{
			name:         "No tag defaults to latest",
			ref:          "nginx",
			wantRegistry: "docker.io", wantNamespace: "library", wantRepoName: "nginx", wantTag: "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := image.ParseReference(tt.ref)

			if tt.wantNil {
				if got != nil {
					t.Errorf("parseImageReference(%q) = %+v, want nil", tt.ref, got)
				}
				return
			}

			if got == nil {
				t.Errorf("parseImageReference(%q) = nil, want non-nil", tt.ref)
				return
			}

			if got.Registry != tt.wantRegistry {
				t.Errorf("Registry = %q, want %q", got.Registry, tt.wantRegistry)
			}
			if got.Namespace != tt.wantNamespace {
				t.Errorf("Namespace = %q, want %q", got.Namespace, tt.wantNamespace)
			}
			if got.RepoName != tt.wantRepoName {
				t.Errorf("RepoName = %q, want %q", got.RepoName, tt.wantRepoName)
			}
			if got.Tag != tt.wantTag {
				t.Errorf("Tag = %q, want %q", got.Tag, tt.wantTag)
			}
		})
	}
}
