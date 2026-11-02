package task

// FinalStatus gives cancellation and task-level errors precedence over counters.
func FinalStatus(result *TaskResult, cancelled bool, err error) string {
	if cancelled {
		return "cancelled"
	}
	if err != nil || result.Error != "" {
		return "failure"
	}
	if result.FailureCount > 0 {
		if result.SuccessCount > 0 {
			return "partial_success"
		}
		return "failure"
	}
	if result.SuccessCount == 0 && result.SkippedCount > 0 {
		return "skipped"
	}
	if result.SuccessCount > 0 {
		return "success"
	}
	return "failure"
}
