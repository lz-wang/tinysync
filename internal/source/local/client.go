// Package local 将宿主机普通文件树适配为 Source-side Remote。
package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"tinysync/internal/filesafe"
	"tinysync/internal/source"
)

// Factory 构造无连接状态的本地 Remote。
type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

func (*Factory) Type() source.Type { return source.TypeLocal }

func (*Factory) Create(ctx context.Context, src source.Source, _ source.Credentials) (source.Remote, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if src.Type != source.TypeLocal {
		return nil, fmt.Errorf("%w: %s", source.ErrUnsupportedType, src.Type)
	}
	if err := source.ValidateConfig(src.Type, src.Config); err != nil {
		return nil, err
	}
	r := &Remote{root: src.Config.Local.Root}
	native, info, err := r.resolve(ctx, "/")
	if err != nil {
		return nil, fmt.Errorf("access local root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("local root %s is not a directory", native)
	}
	dir, err := os.Open(native)
	if err != nil {
		return nil, fmt.Errorf("open local root: %w", err)
	}
	if err := dir.Close(); err != nil {
		return nil, err
	}
	return r, nil
}

// Remote 只保存 canonical native 根目录；不识别或管理挂载协议。
type Remote struct{ root string }

func (*Remote) Close() error { return nil }

func (r *Remote) Stat(ctx context.Context, logical string) (source.FileInfo, error) {
	_, info, err := r.resolve(ctx, logical)
	if err != nil {
		return source.FileInfo{}, err
	}
	return fileInfo(logical, info), nil
}

func (r *Remote) List(ctx context.Context, logical string, opts source.ListOptions) (source.FilePage, error) {
	entries, err := r.readDir(ctx, logical)
	if err != nil {
		return source.FilePage{}, err
	}
	return source.PageSlice(entries, opts)
}

func (r *Remote) Open(ctx context.Context, logical string) (io.ReadCloser, error) {
	native, info, err := r.resolve(ctx, logical)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", filesafe.ErrNotRegularFile, logical)
	}
	f, opened, err := filesafe.OpenCanonicalRegularFile(native)
	if err != nil {
		return nil, fmt.Errorf("open local %s: %w", logical, err)
	}
	if !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, fmt.Errorf("local file %s changed while opening", logical)
	}
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &contextFile{ctx: ctx, f: f}, nil
}

// Mkdir 只创建一级目录，父组件全部重验且不得含 symlink。
func (r *Remote) Mkdir(ctx context.Context, logical string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := source.ValidateLogicalPath(logical); err != nil {
		return err
	}
	if logical == "/" {
		return fmt.Errorf("%w: cannot create source root", source.ErrInvalid)
	}
	parent, info, err := r.resolve(ctx, path.Dir(logical))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("local parent %s is not a directory", path.Dir(logical))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(parent, filepath.FromSlash(path.Base(logical))), 0o755); err != nil {
		return fmt.Errorf("mkdir local %s: %w", logical, err)
	}
	return nil
}

type contextFile struct {
	ctx context.Context
	f   *os.File
}

func (f *contextFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.f.Read(p)
}

func (f *contextFile) Close() error { return f.f.Close() }

var (
	_ source.RemoteFactory    = (*Factory)(nil)
	_ source.Remote           = (*Remote)(nil)
	_ source.TreeScanner      = (*Remote)(nil)
	_ source.DirectoryCreator = (*Remote)(nil)
)
