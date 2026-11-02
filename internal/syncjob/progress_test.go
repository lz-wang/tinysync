package syncjob

import (
	"sync"
	"testing"

	"tinysync/internal/job"
)

// RunProgress 单元：阶段推进、工作量计数、在途单元登记与快照排序。
func TestRunProgressSnapshot(t *testing.T) {
	p := job.NewRunProgress()
	if snap := p.Snapshot(); snap.Phase != job.RunPhaseConnecting || snap.WorkTotal != 0 || len(snap.ActiveUnits) != 0 {
		t.Fatalf("initial snapshot = %+v, want connecting/0/0 units", snap)
	}

	p.SetPhase(RunPhaseTransferring)
	p.SetWorkTotal(75)
	p.AddWorkDone(25)
	a := p.BeginUnit("b.tar", ItemUpdate, 100)
	b := p.BeginUnit("a.zip", ItemCreate, 200)
	// 并发字节累加（模拟下载回调）。
	a.Add(50)
	a.Add(30)
	b.Add(1)

	snap := p.Snapshot()
	if snap.Phase != RunPhaseTransferring || snap.WorkDone != 25 || snap.WorkTotal != 75 {
		t.Fatalf("snapshot = %+v, want transferring 25/75", snap)
	}
	if len(snap.ActiveUnits) != 2 {
		t.Fatalf("active units = %d, want 2", len(snap.ActiveUnits))
	}
	// 快照按 target 稳定排序。
	if snap.ActiveUnits[0].Target != "a.zip" || snap.ActiveUnits[1].Target != "b.tar" {
		t.Errorf("active units order = [%s, %s], want [a.zip, b.tar]",
			snap.ActiveUnits[0].Target, snap.ActiveUnits[1].Target)
	}
	if got := snap.ActiveUnits[0]; got.BytesDone != 1 || got.BytesTotal != 200 || got.Action != string(ItemCreate) {
		t.Errorf("a.zip snapshot = %+v, want done 1 / total 200 / create", got)
	}
	if got := snap.ActiveUnits[1]; got.BytesDone != 80 || got.BytesTotal != 100 {
		t.Errorf("b.tar snapshot = %+v, want done 80 / total 100", got)
	}

	// 注销后快照消失；EndUnit 对未知 target 安全。
	p.EndUnit("a.zip")
	p.EndUnit("not-registered")
	if snap = p.Snapshot(); len(snap.ActiveUnits) != 1 || snap.ActiveUnits[0].Target != "b.tar" {
		t.Fatalf("active units after end = %+v, want only b.tar", snap.ActiveUnits)
	}
	p.EndUnit("b.tar")
	if snap = p.Snapshot(); len(snap.ActiveUnits) != 0 {
		t.Fatalf("active units after all end = %d, want 0", len(snap.ActiveUnits))
	}
}

// 并发压力下计数不丢失、不越界（-race 守护）。
func TestRunProgressConcurrent(t *testing.T) {
	p := job.NewRunProgress()
	p.SetWorkTotal(1000)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			up := p.BeginUnit(string(rune('a'+i%26))+"-"+string(rune('a'+i)), ItemCreate, 10)
			up.Add(int64(i))
			p.AddWorkDone(1)
			p.EndUnit(up.Target)
			_ = p.Snapshot()
		}(i)
	}
	wg.Wait()
	snap := p.Snapshot()
	if snap.WorkDone != 50 {
		t.Errorf("work done = %d, want 50", snap.WorkDone)
	}
	if len(snap.ActiveUnits) != 0 {
		t.Errorf("active units = %d, want 0 after all end", len(snap.ActiveUnits))
	}
}

// recordingProgress 捕获引擎的全部进度事件，供断言阶段序列与计数。
type recordingProgress struct {
	mu         sync.Mutex
	phases     []job.RunPhase
	workTotal  int64
	workDone   int64
	beginUnits []string
	endUnits   []string
}

func (r *recordingProgress) SetPhase(phase job.RunPhase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phases = append(r.phases, phase)
}

func (r *recordingProgress) SetWorkTotal(total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workTotal = total
}

func (r *recordingProgress) AddWorkDone(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workDone += n
}

func (r *recordingProgress) BeginUnit(target string, _ RunItemAction, _ int64) *job.UnitProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beginUnits = append(r.beginUnits, target)
	return &job.UnitProgress{Target: target}
}

func (r *recordingProgress) EndUnit(target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endUnits = append(r.endUnits, target)
}

// 引擎进度事件：阶段序列完整、分母等于计划工作项（跳过 + 待传）、
// 成功一轮后收敛 work_done == work_total、在途单元成对登记注销。
func TestRunProgressEvents(t *testing.T) {
	f := newEngineFixture(t, ModeMirror)
	// 首轮建立 managed：a.txt / c.txt synced。
	first := buildRemote(map[string]string{"/a.txt": "v1", "/c.txt": "same"}, nil)
	if _, err := f.run(first); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// 第二轮：a.txt 更新（传输）+ b.txt 新增（传输）+ c.txt unchanged
	//（skip 收敛）。work_total = 1 skip + 2 transfers = 3。
	remote := buildRemote(map[string]string{"/a.txt": "v2", "/b.txt": "new", "/c.txt": "same"}, nil)
	setFingerprint(remote, "/a.txt", "v2", 1757879401)
	rec := &recordingProgress{}
	if _, err := f.runWith(remote, func(o *RunOptions) { o.Progress = rec }); err != nil {
		t.Fatalf("second run: %v", err)
	}

	// 阶段序列：scanning → planning → transferring → finalizing
	//（Mirror 删除为空也推进 finalizing，阶段由引擎声明）。
	wantPhases := []job.RunPhase{RunPhaseScanning, RunPhasePlanning, RunPhaseTransferring, RunPhaseFinalizing}
	if len(rec.phases) != len(wantPhases) {
		t.Fatalf("phases = %v, want %v", rec.phases, wantPhases)
	}
	for i, want := range wantPhases {
		if rec.phases[i] != want {
			t.Errorf("phase[%d] = %s, want %s", i, rec.phases[i], want)
		}
	}
	if rec.workTotal != 3 {
		t.Errorf("work total = %d, want 3 (1 skip + 2 transfers)", rec.workTotal)
	}
	if rec.workDone != 3 {
		t.Errorf("work done = %d, want 3 (run succeeded)", rec.workDone)
	}
	if len(rec.beginUnits) != 2 || len(rec.endUnits) != 2 {
		t.Errorf("begin/end units = %v / %v, want 2/2", rec.beginUnits, rec.endUnits)
	}
}

// 计划失败（扫描错误）阶段停留在 scanning，分母不设置。
func TestRunProgressAbortsBeforeTransferring(t *testing.T) {
	f := newEngineFixture(t, ModeCopy)
	remote := buildRemote(nil, nil)
	remote.listErr = errorsNew("connection reset")
	rec := &recordingProgress{}
	if _, err := f.runWith(remote, func(o *RunOptions) { o.Progress = rec }); err == nil {
		t.Fatal("run with scan error = nil, want error")
	}
	if len(rec.phases) != 1 || rec.phases[0] != RunPhaseScanning {
		t.Errorf("phases = %v, want [scanning]", rec.phases)
	}
	if rec.workTotal != 0 || rec.workDone != 0 {
		t.Errorf("counters = %d/%d, want 0/0", rec.workDone, rec.workTotal)
	}
	if len(rec.beginUnits) != 0 {
		t.Errorf("begin units = %v, want none", rec.beginUnits)
	}
}

// 独立调用不注入 Progress：NullProgress 路径行为与注入时一致。
func TestRunWithoutProgressReporter(t *testing.T) {
	f := newEngineFixture(t, ModeCopy)
	remote := buildRemote(map[string]string{"/a.txt": "v1"}, nil)
	stats, err := f.run(remote)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stats.FilesCreated != 1 {
		t.Errorf("files created = %d, want 1", stats.FilesCreated)
	}
}
