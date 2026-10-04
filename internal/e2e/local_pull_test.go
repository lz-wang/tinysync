package e2e

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tinysync/internal/source"
	"tinysync/internal/syncjob"
)

func TestLocalBrowserMkdir(t *testing.T) {
	fixture := newLocalFixture(t)
	e := newBrowserEnv(t, fixture)
	w := e.doJSON(http.MethodPost, "/api/v1/sources/src_browser/directories", `{"path":"/","name":"new"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("mkdir=%d %s", w.Code, w.Body)
	}
	w = e.get("/api/v1/sources/src_browser/files/stat?path=/new")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"kind":"directory"`) {
		t.Fatalf("stat=%d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(fixture.src.Config.Local.Root, "new")); err != nil {
		t.Fatal(err)
	}
}

func TestLocalSubtreeSyncAndPartialScanProtection(t *testing.T) {
	fixture := newLocalFixture(t)
	e := newMatrixEnv(t, fixture)
	ctx := context.Background()
	fixture.put(t, "/docs/a.txt", "keep")
	fixture.put(t, "/outside.txt", "not selected")
	target := t.TempDir()
	job := newMatrixJob(t, e, target, "mirror")
	job.RemoteRoot = "/docs"
	if err := e.jobRepo.Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	runAndWait(t, e, job.ID)
	assertLocalFile(t, target, "a.txt", "keep")
	assertNoLocalFile(t, target, "outside.txt")
	fixture.remove(t, "/docs/a.txt")
	// 不完整源快照不能授权 Mirror 删除旧 managed 文件。
	if err := os.Symlink(t.TempDir(), filepath.Join(fixture.src.Config.Local.Root, "docs", "unsafe")); err != nil {
		t.Skip(err)
	}
	id, err := e.runner.Start(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	status, err := e.runner.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != syncjob.RunFailed {
		t.Fatalf("status=%+v", status)
	}
	assertLocalFile(t, target, "a.txt", "keep")
	managed, err := e.managed.ListByJob(ctx, job.ID)
	if err != nil || len(managed) != 1 {
		t.Fatalf("managed=%v %v", managed, err)
	}
}

// 给本地 reader 增加有限延迟，使测试能确定在复制期间发出手动取消。
// 源枚举仍使用真实 adapter 的 TreeScanner，生产代码不引入测试钩子。
type slowLocalRemote struct {
	source.Remote
	began chan struct{}
	once  *sync.Once
}

func (r slowLocalRemote) ScanTree(ctx context.Context, root string, visit func(source.FileInfo) error) error {
	return r.Remote.(source.TreeScanner).ScanTree(ctx, root, visit)
}

func (r slowLocalRemote) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	file, err := r.Remote.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return slowLocalReader{ReadCloser: file, began: r.began, once: r.once}, nil
}

type slowLocalReader struct {
	io.ReadCloser
	began chan struct{}
	once  *sync.Once
}

func (r slowLocalReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.began) })
	time.Sleep(time.Millisecond)
	return r.ReadCloser.Read(p)
}

func TestCancelLocalLargeFileRun(t *testing.T) {
	fixture := newLocalFixture(t)
	began := make(chan struct{})
	once := &sync.Once{}
	file, err := os.Create(filepath.Join(fixture.src.Config.Local.Root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(32 << 20); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	open := fixture.openRemote
	fixture.openRemote = func() (source.Remote, error) {
		r, err := open()
		if err != nil {
			return nil, err
		}
		return slowLocalRemote{Remote: r, began: began, once: once}, nil
	}
	e := newMatrixEnv(t, fixture)
	target := t.TempDir()
	job := newMatrixJob(t, e, target, "copy")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := e.runner.Start(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-began:
	case <-ctx.Done():
		t.Fatal("transfer did not begin")
	}
	if err := e.runner.Cancel(id); err != nil {
		t.Fatal(err)
	}
	status, err := e.runner.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != syncjob.RunCanceled {
		t.Fatalf("status=%+v", status)
	}
	assertNoLocalFile(t, target, "large.bin")
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	// 取消保留可恢复断点（ADR 0010 不变量 9）：目录里允许且只允许
	// 存留 v1 断点文件，下一次 run 从断点续传。
	if len(entries) > 1 {
		t.Fatalf("unexpected extra files remain: %v", entries)
	}
	for _, e := range entries {
		if !syncjob.IsPartialName(e.Name()) {
			t.Fatalf("non-partial file remains after cancel: %s", e.Name())
		}
	}
	items, _, err := e.runs.Items(ctx, id, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.Path == "large.bin" && item.Status == syncjob.ItemCanceled {
			found = true
		}
	}
	if !found {
		t.Fatalf("canceled item missing: %+v", items)
	}
}
