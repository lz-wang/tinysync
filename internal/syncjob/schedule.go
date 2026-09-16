package syncjob

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	_ "time/tzdata"
)

// ScheduleType 是 Job 的调度类型。
type ScheduleType string

// 调度类型：manual 仅手动触发；once 在指定时刻执行一次；interval 按固定
// 周期执行；cron 按标准 5-field 表达式执行。
const (
	ScheduleManual   ScheduleType = "manual"
	ScheduleOnce     ScheduleType = "once"
	ScheduleInterval ScheduleType = "interval"
	ScheduleCron     ScheduleType = "cron"
)

// Valid 判断调度类型是否为受支持的枚举值。
func (t ScheduleType) Valid() bool {
	switch t {
	case ScheduleManual, ScheduleOnce, ScheduleInterval, ScheduleCron:
		return true
	}
	return false
}

// MinInterval 是 interval 的最小周期：调度器 tick 粒度为秒级，
// 更短的周期没有 HomeLab 语义且极易产生 skipped 记录洪峰。
const MinInterval = time.Minute

// cronParser 只接受标准 5-field 表达式（分 时 日 月 周）：不支持秒域与
// @descriptors。仅使用 parse + Next 计算，不使用该库的调度器——Job、
// Runner、历史与并发策略仍由 TinySync 自己的 Scheduler 负责。
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Schedule 是 Job 的调度配置，与 sync_jobs 的扁平列一一对应：
// Type → schedule_type，Value → schedule_value（按 Type 解释），
// Timezone → schedule_timezone，AnchorAt → schedule_anchor_at。
// REST / Web 在其上映射 discriminated object，不暴露互斥 nullable 字段。
type Schedule struct {
	Type ScheduleType
	// Value 按 Type 解释：manual 恒为空；once 为 RFC3339 绝对时间
	// （入库归一为 UTC）；interval 为 Go duration（如 30m、6h）；
	// cron 为 5-field 表达式（如 0 3 * * *）。
	Value string
	// Timezone 仅 cron 有效：IANA 名称（如 Asia/Singapore），空为 UTC。
	Timezone string
	// AnchorAt 是 interval 的相位基准（持久化）。由 Service 在创建或
	// 变更 schedule 时设置，REST / Web 输入不携带。
	AnchorAt *time.Time
}

// Validate 校验调度配置的用户输入部分（Type / Value / Timezone）；
// AnchorAt 由 Service 管理，不在此校验。
func (s Schedule) Validate() error {
	switch s.Type {
	case ScheduleManual:
		if s.Value != "" || s.Timezone != "" {
			return fmt.Errorf("%w: manual schedule must not carry value or timezone", ErrInvalid)
		}
	case ScheduleOnce:
		if s.Timezone != "" {
			return fmt.Errorf("%w: once schedule takes no timezone (encode offset in the RFC3339 value)", ErrInvalid)
		}
		if _, err := s.OnceAt(); err != nil {
			return err
		}
	case ScheduleInterval:
		if s.Timezone != "" {
			return fmt.Errorf("%w: interval schedule takes no timezone", ErrInvalid)
		}
		if _, err := s.IntervalEvery(); err != nil {
			return err
		}
	case ScheduleCron:
		if _, err := s.CronSchedule(); err != nil {
			return err
		}
	case "":
		return fmt.Errorf("%w: schedule type is required", ErrInvalid)
	default:
		return fmt.Errorf("%w: unknown schedule type %q", ErrInvalid, s.Type)
	}
	return nil
}

// Normalized 返回入库前的归一形式：cron / interval 的 value 与 timezone
// 去除首尾空白；once 归一为 UTC RFC3339。输入必须已通过 Validate。
func (s Schedule) Normalized() Schedule {
	out := s
	out.Value = strings.TrimSpace(s.Value)
	out.Timezone = strings.TrimSpace(s.Timezone)
	if s.Type == ScheduleOnce {
		if at, err := s.OnceAt(); err == nil {
			out.Value = at.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// IntentEqual 判断两个 schedule 的用户语义是否一致：Type / Timezone
// 相同，且 once 比较绝对时刻、interval 比较周期时长、cron / manual
// 比较归一后的表达式。Service 以此识别「同值 PATCH」——语义未变的
// 调度更新不重置 interval 的相位基准。输入应已通过 Validate；无法
// 解析的值一律视为不相等。
func (s Schedule) IntentEqual(other Schedule) bool {
	if s.Type != other.Type ||
		strings.TrimSpace(s.Timezone) != strings.TrimSpace(other.Timezone) {
		return false
	}
	switch s.Type {
	case ScheduleManual:
		return true
	case ScheduleOnce:
		a, errA := s.OnceAt()
		b, errB := other.OnceAt()
		return errA == nil && errB == nil && a.Equal(b)
	case ScheduleInterval:
		a, errA := s.IntervalEvery()
		b, errB := other.IntervalEvery()
		return errA == nil && errB == nil && a == b
	default: // ScheduleCron
		return strings.TrimSpace(s.Value) == strings.TrimSpace(other.Value)
	}
}

// OnceAt 解析 once 的触发时间。
func (s Schedule) OnceAt() (time.Time, error) {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(s.Value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: once schedule %q is not an RFC3339 time (e.g. 2026-09-20T03:00:00+08:00)", ErrInvalid, s.Value)
	}
	return at, nil
}

// IntervalEvery 解析 interval 的周期，要求不低于 MinInterval。
func (s Schedule) IntervalEvery() (time.Duration, error) {
	every, err := time.ParseDuration(strings.TrimSpace(s.Value))
	if err != nil {
		return 0, fmt.Errorf("%w: interval %q is not a Go duration (e.g. 30m, 6h)", ErrInvalid, s.Value)
	}
	if every < MinInterval {
		return 0, fmt.Errorf("%w: interval %q must be at least %s", ErrInvalid, s.Value, MinInterval)
	}
	return every, nil
}

// CronSchedule 解析 cron 表达式并应用时区；Timezone 为空时使用 UTC。
// DST 歧义与间隙行为交由 cron parser 与 timezone 语义处理。
func (s Schedule) CronSchedule() (cron.Schedule, error) {
	expr := strings.TrimSpace(s.Value)
	if expr == "" {
		return nil, fmt.Errorf("%w: cron expression is required", ErrInvalid)
	}
	parsed, err := cronParser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: cron expression %q: %v", ErrInvalid, expr, err)
	}
	loc := time.UTC
	if tz := strings.TrimSpace(s.Timezone); tz != "" {
		loc, err = time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("%w: unknown timezone %q", ErrInvalid, tz)
		}
	}
	// Parser 固定产出 time.Local 的 SpecSchedule；其字段全部导出，
	// 复制后替换 Location 即得目标时区语义，不改变解析结果本身。
	if spec, ok := parsed.(*cron.SpecSchedule); ok {
		withLoc := *spec
		withLoc.Location = loc
		return &withLoc, nil
	}
	return parsed, nil
}

// NextRun 返回 now 之后的下一次计划触发时间（不含 now 本身）：
// manual 恒无；once 返回配置时间（是否已消费由调用方结合运行历史判定）；
// interval 由 anchor 推导下一个边界，重启不改变相位；cron 按时区计算。
// 第二个返回值为 false 表示没有下一次触发。
func (s Schedule) NextRun(now time.Time) (time.Time, bool) {
	switch s.Type {
	case ScheduleOnce:
		at, err := s.OnceAt()
		if err != nil {
			return time.Time{}, false
		}
		return at, true
	case ScheduleInterval:
		if s.AnchorAt == nil {
			return time.Time{}, false
		}
		every, err := s.IntervalEvery()
		if err != nil {
			return time.Time{}, false
		}
		anchor := *s.AnchorAt
		if now.Before(anchor) {
			return anchor.Add(every), true
		}
		// anchor + (floor((now-anchor)/every)+1)*every：整数除法截断即
		// floor（两侧非负）；now 恰在边界上时取下一个边界。
		steps := now.Sub(anchor)/every + 1
		return anchor.Add(steps * every), true
	case ScheduleCron:
		sched, err := s.CronSchedule()
		if err != nil {
			return time.Time{}, false
		}
		return sched.Next(now), true
	default:
		return time.Time{}, false
	}
}

// DueOccurrence 返回调度窗口 (from, now] 内到期的 occurrence。调度器
// 每个 tick 以内存游标为 from：窗口之外（含离线期间错过）的周期不回看，
// 即 cron / interval 不补跑；once 不受窗口限制——错过也要执行一次，
// 是否已消费由调用方查运行历史判定。
func (s Schedule) DueOccurrence(from, now time.Time) (time.Time, bool) {
	switch s.Type {
	case ScheduleOnce:
		at, err := s.OnceAt()
		if err != nil || at.After(now) {
			return time.Time{}, false
		}
		return at, true
	case ScheduleInterval, ScheduleCron:
		next, ok := s.NextRun(from)
		if !ok || next.After(now) {
			return time.Time{}, false
		}
		return next, true
	default:
		return time.Time{}, false
	}
}
