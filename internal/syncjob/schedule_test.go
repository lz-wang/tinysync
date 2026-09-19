package syncjob

import (
	"errors"
	"testing"
	"time"
)

// Validate 覆盖四种类型与全部非法组合：非法输入必须即时拒绝，
// 不留到调度阶段静默失败。
func TestScheduleValidate(t *testing.T) {
	tests := []struct {
		name     string
		schedule Schedule
		wantErr  bool
	}{
		{"manual empty", Schedule{Type: ScheduleManual}, false},
		{"manual with value", Schedule{Type: ScheduleManual, Value: "30m"}, true},
		{"manual with timezone", Schedule{Type: ScheduleManual, Timezone: "UTC"}, true},
		{"missing type", Schedule{}, true},
		{"unknown type", Schedule{Type: ScheduleType("weekly")}, true},
		{"once offset", Schedule{Type: ScheduleOnce, Value: "2026-09-20T03:00:00+08:00"}, false},
		{"once utc", Schedule{Type: ScheduleOnce, Value: "2026-09-20T03:00:00Z"}, false},
		{"once not rfc3339", Schedule{Type: ScheduleOnce, Value: "2026-09-20 03:00:00"}, true},
		{"once date only", Schedule{Type: ScheduleOnce, Value: "2026-09-20"}, true},
		{"once empty", Schedule{Type: ScheduleOnce}, true},
		{"once with timezone", Schedule{Type: ScheduleOnce, Value: "2026-09-20T03:00:00Z", Timezone: "UTC"}, true},
		{"interval valid", Schedule{Type: ScheduleInterval, Value: "30m"}, false},
		{"interval compound", Schedule{Type: ScheduleInterval, Value: "1h30m"}, false},
		{"interval padded", Schedule{Type: ScheduleInterval, Value: " 6h "}, false},
		{"interval below min", Schedule{Type: ScheduleInterval, Value: "30s"}, true},
		{"interval zero", Schedule{Type: ScheduleInterval, Value: "0"}, true},
		{"interval negative", Schedule{Type: ScheduleInterval, Value: "-5m"}, true},
		{"interval missing unit", Schedule{Type: ScheduleInterval, Value: "30"}, true},
		{"interval empty", Schedule{Type: ScheduleInterval}, true},
		{"interval with timezone", Schedule{Type: ScheduleInterval, Value: "30m", Timezone: "UTC"}, true},
		{"cron valid", Schedule{Type: ScheduleCron, Value: "0 3 * * *"}, false},
		{"cron step and list", Schedule{Type: ScheduleCron, Value: "*/15 1-5 * * 1,3"}, false},
		{"cron with timezone", Schedule{Type: ScheduleCron, Value: "0 3 * * *", Timezone: "Asia/Singapore"}, false},
		{"cron empty", Schedule{Type: ScheduleCron}, true},
		{"cron too few fields", Schedule{Type: ScheduleCron, Value: "0 3 * *"}, true},
		{"cron seconds rejected", Schedule{Type: ScheduleCron, Value: "0 0 3 * * *"}, true},
		{"cron descriptor rejected", Schedule{Type: ScheduleCron, Value: "@daily"}, true},
		{"cron bad minute", Schedule{Type: ScheduleCron, Value: "60 3 * * *"}, true},
		{"cron unknown timezone", Schedule{Type: ScheduleCron, Value: "0 3 * * *", Timezone: "Mars/Olympus"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.schedule.Validate()
			if tt.wantErr && !errors.Is(err, ErrInvalid) {
				t.Errorf("Validate(%+v) = %v, want ErrInvalid", tt.schedule, err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate(%+v) = %v, want nil", tt.schedule, err)
			}
		})
	}
}

// Normalized：once 归一为 UTC RFC3339，cron / interval 去除空白。
func TestScheduleNormalized(t *testing.T) {
	once := Schedule{Type: ScheduleOnce, Value: "2026-09-20T11:00:00+08:00"}
	got := once.Normalized()
	if got.Value != "2026-09-20T03:00:00Z" {
		t.Errorf("normalized once value = %q, want 2026-09-20T03:00:00Z", got.Value)
	}

	cron := Schedule{Type: ScheduleCron, Value: " 0 3 * * * ", Timezone: " Asia/Singapore "}
	got = cron.Normalized()
	if got.Value != "0 3 * * *" || got.Timezone != "Asia/Singapore" {
		t.Errorf("normalized cron = (%q, %q), want (0 3 * * *, Asia/Singapore)", got.Value, got.Timezone)
	}
}

// interval 的 NextRun 由持久化 anchor 推导：重启只影响从哪个时刻继续，
// 不改变边界相位；now 在边界上时取下一个边界；anchor 在未来时首边界
// 为 anchor+every。
func TestScheduleNextRunInterval(t *testing.T) {
	anchor := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	s := Schedule{Type: ScheduleInterval, Value: "30m", AnchorAt: &anchor}

	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before anchor", anchor.Add(-time.Hour), anchor.Add(30 * time.Minute)},
		{"mid window", anchor.Add(10 * time.Minute), anchor.Add(30 * time.Minute)},
		{"exactly on boundary", anchor.Add(30 * time.Minute), anchor.Add(60 * time.Minute)},
		{"partial step", anchor.Add(95 * time.Minute), anchor.Add(120 * time.Minute)},
		{"many steps", anchor.Add(48*time.Hour + 5*time.Minute), anchor.Add(48*time.Hour + 30*time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := s.NextRun(tt.now)
			if !ok || !got.Equal(tt.want) {
				t.Errorf("NextRun(%v) = (%v, %v), want (%v, true)", tt.now, got, ok, tt.want)
			}
		})
	}

	// 缺少 anchor 无法推导相位。
	if _, ok := (Schedule{Type: ScheduleInterval, Value: "30m"}).NextRun(anchor); ok {
		t.Error("NextRun without anchor = ok, want false")
	}
}

// cron 的 NextRun 支持显式时区；未指定时按运行机器时区计算。
func TestScheduleNextRunCronTimezone(t *testing.T) {
	s := Schedule{Type: ScheduleCron, Value: "0 3 * * *", Timezone: "Asia/Singapore"}
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC) // 08:00 SGT，当日 03:00 SGT 已过
	got, ok := s.NextRun(now)
	want := time.Date(2026, 9, 18, 19, 0, 0, 0, time.UTC) // 09-19 03:00 SGT
	if !ok || !got.Equal(want) {
		t.Errorf("NextRun = (%v, %v), want (%v, true)", got, ok, want)
	}

	local := Schedule{Type: ScheduleCron, Value: "0 3 * * *"}
	got, ok = local.NextRun(now)
	localNow := now.In(time.Local)
	want = time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 3, 0, 0, 0, time.Local)
	if !want.After(now) {
		want = want.AddDate(0, 0, 1)
	}
	if !ok || !got.Equal(want) {
		t.Errorf("NextRun local = (%v, %v), want (%v, true)", got, ok, want)
	}
}

// DueOccurrence：interval / cron 只认游标窗口 (from, now] 内的 occurrence，
// 窗口之外不回看（不补跑）；once 不受窗口限制，错过也要执行一次。
func TestScheduleDueOccurrence(t *testing.T) {
	anchor := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	interval := Schedule{Type: ScheduleInterval, Value: "30m", AnchorAt: &anchor}

	// 窗口覆盖边界：到期，occurrence 为边界本身。
	occ, ok := interval.DueOccurrence(anchor.Add(29*time.Minute), anchor.Add(31*time.Minute))
	if !ok || !occ.Equal(anchor.Add(30*time.Minute)) {
		t.Errorf("interval due = (%v, %v), want (%v, true)", occ, ok, anchor.Add(30*time.Minute))
	}
	// 窗口内无边界：不到期。
	if _, ok := interval.DueOccurrence(anchor.Add(31*time.Minute), anchor.Add(32*time.Minute)); ok {
		t.Error("interval due in empty window = true, want false")
	}
	// 重启后首 tick（from = now）：历史边界不回看，不补跑。
	restart := anchor.Add(4 * time.Hour)
	if _, ok := interval.DueOccurrence(restart, restart.Add(time.Second)); ok {
		t.Error("interval due after restart = true, want false (no catch-up)")
	}

	// cron 窗口到期与显式时区。
	cronSg := Schedule{Type: ScheduleCron, Value: "30 3 * * *", Timezone: "Asia/Singapore"}
	day := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	occ, ok = cronSg.DueOccurrence(day, day.Add(20*time.Hour))
	if !ok || !occ.Equal(time.Date(2026, 9, 19, 19, 30, 0, 0, time.UTC)) {
		t.Errorf("cron due = (%v, %v), want (2026-09-19T19:30:00Z, true)", occ, ok)
	}
	if _, ok := cronSg.DueOccurrence(day, day.Add(19*time.Hour)); ok {
		t.Error("cron due before occurrence = true, want false")
	}

	// once：已错过仍到期（消费判定由调用方完成），未来则不到期。
	onceAt := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	once := Schedule{Type: ScheduleOnce, Value: onceAt.Format(time.RFC3339)}
	occ, ok = once.DueOccurrence(onceAt.Add(time.Hour), onceAt.Add(2*time.Hour))
	if !ok || !occ.Equal(onceAt) {
		t.Errorf("once due after miss = (%v, %v), want (%v, true)", occ, ok, onceAt)
	}
	if _, ok := once.DueOccurrence(onceAt.Add(-2*time.Hour), onceAt.Add(-time.Hour)); ok {
		t.Error("once due before its time = true, want false")
	}

	// manual 永不到期。
	if _, ok := (Schedule{Type: ScheduleManual}).DueOccurrence(day, day.Add(time.Hour)); ok {
		t.Error("manual due = true, want false")
	}
	// 非法配置安全返回不到期，不 panic。
	broken := Schedule{Type: ScheduleInterval, Value: "bogus", AnchorAt: &anchor}
	if _, ok := broken.DueOccurrence(day, day.Add(time.Hour)); ok {
		t.Error("broken schedule due = true, want false")
	}
}
