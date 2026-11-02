package image

import "testing"

func TestParseReference(t *testing.T) {
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
			name:          "Docker Hub nested namespace",
			ref:           "org/suborg/image:v1.0",
			wantRegistry:  "docker.io",
			wantNamespace: "org/suborg",
			wantRepoName:  "image",
			wantTag:       "v1.0",
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
			name:         "No tag",
			ref:          "nginx",
			wantRegistry: "docker.io", wantNamespace: "library", wantRepoName: "nginx", wantTag: "latest",
		},
		{name: "Port without tag", ref: "localhost:5000/team/app", wantRegistry: "localhost:5000", wantNamespace: "team", wantRepoName: "app", wantTag: "latest"},
		{name: "Nested namespace without tag", ref: "org/team/app", wantRegistry: "docker.io", wantNamespace: "org/team", wantRepoName: "app", wantTag: "latest"},
		{name: "Private registry without tag", ref: "ghcr.io/team/app", wantRegistry: "ghcr.io", wantNamespace: "team", wantRepoName: "app", wantTag: "latest"},
		{name: "Explicit empty tag", ref: "nginx:", wantNil: true},
		{name: "Port and explicit empty tag", ref: "localhost:5000/app:", wantNil: true},
		{name: "Empty input", ref: "", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseReference(tt.ref)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("ParseReference(%q) = %+v, want nil", tt.ref, got)
				}
				return
			}

			if got == nil {
				t.Fatalf("ParseReference(%q) = nil", tt.ref)
			}

			if got.Registry != tt.wantRegistry || got.Namespace != tt.wantNamespace || got.RepoName != tt.wantRepoName || got.Tag != tt.wantTag {
				t.Fatalf("ParseReference(%q) = %+v", tt.ref, got)
			}
		})
	}
}

func TestParseRepository(t *testing.T) {
	tests := []struct {
		name          string
		ref           string
		wantRegistry  string
		wantNamespace string
		wantRepoName  string
	}{
		{
			name:          "Docker Hub official repository",
			ref:           "nginx",
			wantRegistry:  "docker.io",
			wantNamespace: "library",
			wantRepoName:  "nginx",
		},
		{
			name:          "Docker Hub namespaced repository",
			ref:           "library/nginx",
			wantRegistry:  "docker.io",
			wantNamespace: "library",
			wantRepoName:  "nginx",
		},
		{
			name:          "Private registry repository",
			ref:           "registry.example.com/team/app",
			wantRegistry:  "registry.example.com",
			wantNamespace: "team",
			wantRepoName:  "app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRepository(tt.ref)
			if got == nil {
				t.Fatalf("ParseRepository(%q) = nil", tt.ref)
			}

			if got.Registry != tt.wantRegistry || got.Namespace != tt.wantNamespace || got.RepoName != tt.wantRepoName {
				t.Fatalf("ParseRepository(%q) = %+v", tt.ref, got)
			}
		})
	}
}
