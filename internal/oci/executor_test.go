package oci

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"tinysync/internal/job"
	"tinysync/internal/oci/registry"
	"tinysync/internal/oci/task"
)

// fakeProvider 是内存版 RegistryProvider（executor 单元测试用）。
type fakeProvider struct {
	res registry.Resolver
}

func (p *fakeProvider) Resolver(context.Context) (registry.Resolver, error) {
	return p.res, nil
}

// newValidateExecutor 构造带静态 resolver 的执行器（zot 为已配置目标）。
func newValidateExecutor() *OCIExecutor {
	res := newResolver([]Registry{
		{ID: "1", Name: "zot", Kind: KindPrimary, Host: "zot.example.com"},
		{ID: "2", Name: "hub", Kind: KindMirror, Host: "docker.io", Mirrors: []string{"docker.1ms.run"}},
	})
	return NewOCIExecutor(&fakeProvider{res: res}, nil)
}

// Validate 归一化：src_images 展开（library/ 前缀 + 默认 latest）、
// dst_servers 存在性（仅 primary）、platforms 补全 os / 去空白。
func TestOCIExecutorValidateNormalizes(t *testing.T) {
	e := newValidateExecutor()

	normalized, err := e.Validate(context.Background(), "", json.RawMessage(
		`{"src_images":["nginx:1.24,1.25","alpine"],"dst_servers":["zot"],"platforms":[{"os":" linux ","architecture":" ARM64 ","variant":" v8 "}]}`))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	cfg, err := ParseTaskConfig(normalized)
	if err != nil {
		t.Fatalf("parse normalized: %v", err)
	}
	wantSrc := []string{"library/nginx:1.24", "library/nginx:1.25", "library/alpine:latest"}
	if !reflect.DeepEqual(cfg.SrcImages, wantSrc) {
		t.Fatalf("normalized src_images = %v, want %v", cfg.SrcImages, wantSrc)
	}
	if !reflect.DeepEqual(cfg.DstServers, []string{"zot"}) {
		t.Fatalf("dst_servers = %v", cfg.DstServers)
	}
	if len(cfg.Platforms) != 1 || cfg.Platforms[0].OS != "linux" || cfg.Platforms[0].Architecture != "ARM64" || cfg.Platforms[0].Variant != "v8" {
		t.Fatalf("normalized platforms = %+v", cfg.Platforms)
	}
}

// Validate 拒绝：空 src_images、空 dst_servers、未配置目标、mirror 行作
// 目标、重复目标、非法镜像引用、缺 architecture 的平台过滤。
func TestOCIExecutorValidateRejects(t *testing.T) {
	e := newValidateExecutor()
	ctx := context.Background()

	cases := []struct {
		name string
		cfg  string
	}{
		{"empty src_images", `{"src_images":[],"dst_servers":["zot"]}`},
		{"empty dst_servers", `{"src_images":["nginx"]}`},
		{"unknown dst", `{"src_images":["nginx"],"dst_servers":["missing"]}`},
		{"mirror as dst", `{"src_images":["nginx"],"dst_servers":["hub"]}`},
		{"duplicate dst", `{"src_images":["nginx"],"dst_servers":["zot","zot"]}`},
		{"invalid image", `{"src_images":["NGINX:///bad"],"dst_servers":["zot"]}`},
		{"empty comma tag", `{"src_images":["nginx:1.24,"],"dst_servers":["zot"]}`},
		{"platform without arch", `{"src_images":["nginx"],"dst_servers":["zot"],"platforms":[{"os":"linux"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.Validate(ctx, "", json.RawMessage(tc.cfg)); err == nil || !errors.Is(err, job.ErrInvalid) {
				t.Fatalf("Validate(%s) err = %v, want job.ErrInvalid", tc.name, err)
			}
		})
	}
}

// 空 platforms 视为清除平台过滤（同步全部平台），归一化落库为省略。
func TestOCIExecutorValidateEmptyPlatforms(t *testing.T) {
	e := newValidateExecutor()
	normalized, err := e.Validate(context.Background(), "", json.RawMessage(
		`{"src_images":["nginx"],"dst_servers":["zot"],"platforms":[]}`))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	cfg, err := ParseTaskConfig(normalized)
	if err != nil {
		t.Fatalf("parse normalized: %v", err)
	}
	if len(cfg.Platforms) != 0 {
		t.Fatalf("platforms = %+v, want empty", cfg.Platforms)
	}
}

// NormalizePlatforms 与 ValidateSrcImages 的直接单元测试（oci-sync
// control/validation.go 对应用例的移植）。
func TestValidateSrcImagesRules(t *testing.T) {
	if err := ValidateSrcImages([]string{"nginx:1.27"}); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	if err := ValidateSrcImages([]string{"  "}); err == nil {
		t.Fatal("blank entry accepted")
	}
	if err := ValidateSrcImages([]string{"nginx:1.27,nginx:1.27"}); err == nil {
		t.Fatal("duplicated expanded entry accepted")
	}
	if _, err := NormalizePlatforms(nil); err != nil {
		t.Fatalf("nil platforms should clear filter: %v", err)
	}
	if _, err := NormalizePlatforms([]registry.PlatformConfig{{OS: "linux"}}); err == nil {
		t.Fatal("missing architecture accepted")
	}
	normalized, err := NormalizePlatforms([]registry.PlatformConfig{{Architecture: "arm64"}})
	if err != nil || normalized[0].OS != "linux" {
		t.Fatalf("NormalizePlatforms default os: %v / %+v", err, normalized)
	}
}

// ParseTaskConfig 的形状解码：nil / 空串视为空配置。
func TestParseTaskConfigEmpty(t *testing.T) {
	cfg, err := ParseTaskConfig(nil)
	if err != nil || cfg.SrcImages != nil || cfg.DstServers != nil {
		t.Fatalf("ParseTaskConfig(nil) = %+v, %v", cfg, err)
	}
	if _, err := ParseTaskConfig(json.RawMessage(`{invalid`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

// 编译期断言：执行器满足统一契约，TaskConfig 字段名与 config_json 形状
// 对齐（brief 约定的三字段）。
var (
	_ job.Executor      = (*OCIExecutor)(nil)
	_ RegistryProvider  = (*fakeProvider)(nil)
	_ task.ProgressSink = ociProgress{}
	_ registry.Resolver = (*snapshotResolver)(nil)
)
