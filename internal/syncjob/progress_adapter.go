package syncjob

import "tinysync/internal/job"

// engineProgress 把可为 nil 的 ProgressReporter 规整为非 nil 实现。
func engineProgress(p ProgressReporter) ProgressReporter {
	return job.NullProgress(p)
}

// fileProgressListener 把 job.UnitProgress 适配为 Downloader 的
// TransferListener：attempt 起点对齐计数（断点续传时 bytes_done 从
// 断点开始），成功写入累加。值类型即可（句柄语义，fp 指针共享）。
type fileProgressListener struct {
	fp *job.UnitProgress
}

// AttemptStart 实现 TransferListener：计数对齐 attempt 起点。
func (l fileProgressListener) AttemptStart(offset int64) { l.fp.SetDone(offset) }

// Write 实现 TransferListener：累加本次写入字节数。
func (l fileProgressListener) Write(n int64) { l.fp.Add(n) }
