package task

import (
	"errors"
	"testing"
)

func TestFinalStatusPriority(t *testing.T) {
	for _, test := range []struct {
		r      TaskResult
		cancel bool
		err    error
		want   string
	}{{TaskResult{SuccessCount: 2}, true, nil, "cancelled"}, {TaskResult{SuccessCount: 2}, false, errors.New("init"), "failure"}, {TaskResult{SuccessCount: 1, FailureCount: 1}, false, nil, "partial_success"}, {TaskResult{SkippedCount: 1}, false, nil, "skipped"}, {TaskResult{}, false, nil, "failure"}, {TaskResult{SuccessCount: 1, SkippedCount: 1}, false, nil, "success"}} {
		if got := FinalStatus(&test.r, test.cancel, test.err); got != test.want {
			t.Fatalf("got %s want %s", got, test.want)
		}
	}
}
