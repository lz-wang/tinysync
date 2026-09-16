-- v4：once 消费状态与可裁剪的运行历史分离。
-- once 是否已执行曾依靠 sync_runs(job_id, 'once', scheduled_for) 判定，
-- 但每 Job 500 run 的 retention 会裁剪历史行，使已执行的 once 被重新
-- 触发。once_consumed_for 是调度器的持久化 correctness state：
-- occurrence 产生 run（succeeded/failed/skipped）即写入该 occurrence
-- 的 Unix 毫秒时间戳；非 once 调度与未执行的 once 为 NULL；once 语义
-- 变更时由服务层清空。
ALTER TABLE sync_jobs ADD COLUMN once_consumed_for INTEGER;

-- 回填：已有 once run 的 Job 视为已消费最近一次 occurrence，避免
-- 升级后 once 重放；manual / interval / cron 不受影响。
UPDATE sync_jobs SET once_consumed_for = (
    SELECT r.scheduled_for FROM sync_runs r
    WHERE r.job_id = sync_jobs.id AND r.trigger_type = 'once'
      AND r.scheduled_for IS NOT NULL
    ORDER BY r.started_at DESC, r.id DESC LIMIT 1
) WHERE schedule_type = 'once';
