package source

import (
	"context"
	"errors"
	"io"
	"testing"
)

// regStubRemote 是最小 Remote 桩。
type regStubRemote struct{}

func (r *regStubRemote) Stat(ctx context.Context, path string) (FileInfo, error) {
	return FileInfo{}, nil
}

func (r *regStubRemote) List(ctx context.Context, path string, opts ListOptions) (FilePage, error) {
	return FilePage{}, nil
}

func (r *regStubRemote) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (r *regStubRemote) Close() error { return nil }

// regStubFactory 是可编程的 RemoteFactory 桩。
type regStubFactory struct {
	typ    Type
	remote Remote
	called int
}

func (f *regStubFactory) Type() Type { return f.typ }

func (f *regStubFactory) Create(ctx context.Context, s Source, credentials Credentials) (Remote, error) {
	f.called++
	return f.remote, nil
}

// Registry 按协议类型 dispatch 到对应 factory；未注册类型返回
// ErrUnsupportedType。
func TestRegistryDispatch(t *testing.T) {
	dav := &regStubFactory{typ: TypeWebDAV, remote: &regStubRemote{}}
	registry, err := NewRemoteRegistry(dav)
	if err != nil {
		t.Fatalf("NewRemoteRegistry: %v", err)
	}

	src := Source{Type: TypeWebDAV}
	remote, err := registry.Create(context.Background(), src, Credentials{})
	if err != nil {
		t.Fatalf("Create webdav: %v", err)
	}
	if remote != dav.remote {
		t.Fatal("Create returned unexpected remote")
	}
	if dav.called != 1 {
		t.Fatalf("webdav factory called = %d, want 1", dav.called)
	}

	_, err = registry.Create(context.Background(), Source{Type: Type("s3")}, Credentials{})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("Create unregistered type = %v, want ErrUnsupportedType", err)
	}
}

// 同一协议类型重复注册视为装配错误。
func TestRegistryDuplicateType(t *testing.T) {
	_, err := NewRemoteRegistry(
		&regStubFactory{typ: TypeWebDAV},
		&regStubFactory{typ: TypeWebDAV},
	)
	if err == nil {
		t.Fatal("duplicate registration accepted")
	}
}
