# 同步运行完成通知：落库后旁路推送

任务跑完只有进 WebUI 翻历史才知道结果，HomeLab 场景更需要「跑完推一条」。我们决定为**真实执行过的运行**（succeeded / failed / canceled）增加完成通知，第一版支持 **Pushover 与 SMTP 邮件**两个渠道。核心原则是：**同步运行的事实先成功落库，然后才产生通知事件**——`Runner.finalize` 在 `history.Finalize()` 成功后才经 `RunCompletionHook` 发布 `RunCompletion` 事件；落库失败不通知（否则用户收到完成消息、数据库仍显示 running，两边事实不一致）；**通知属于旁路副作用，任何发送失败只记结构化日志，绝不改变 run 的终态**。`Runner` 只认识 `RunCompletionHook` 接口，不感知任何发送渠道；`notification.Service` 实现该接口，内部维护**容量 64 的内存队列 + 单 worker**（`OnRunCompleted` 只 enqueue 立即返回，SMTP 卡 10 秒也不会拖住 activeRun 的释放与 WebUI 状态），每次通知时**从 SQLite 热读取配置**——WebUI 保存即生效，无需重启。**不做 durable outbox**：任务历史才是权威事实，通知只是辅助提醒，HomeLab 规模不值得引入 outbox 表与 retry scheduler。

## Considered Options

- 在 `finalize()` 里直接调用 sendPushover / sendEmail：Runner 获知全部渠道细节，且同步发送会把 SMTP 延迟转嫁给 activeRun 生命周期，WebUI 可能继续看到 running。
- 通用 event bus / 消息队列：tinysync 是 HomeLab 工具，唯一事件就是 run 完成，bus 抽象纯属过度设计。
- durable outbox + retry scheduler：通知丢失的代价只是「少一条提醒」，run 历史仍可查；为此引入持久化队列与重试调度不值。
- 通知 worker 直接绑定应用主 ctx：SIGTERM 到达时主 ctx 已取消，恰好最后几个任务产生的通知会被提前丢弃——worker 持有独立 ctx，由 `Shutdown` 负责排空（drain）后回收。
- 把 SMTP 密码、Pushover token 塞进 credential 域（ADR 0005）：credential 以 ssh_key 为中心设计，塞通用 secret 会让抽象退化为 secret store；通知配置用独立 singleton 表 `notification_settings`（migration 0013），与「密码明文落库 + DB 文件 0600」的既有信任模型一致。
- skipped 也发通知：skipped 是 scheduler 因 overlap / 并发满**根本没有执行同步**的记录，不属于「任务完成」；若将来需要可单独做调度跳过告警，不混入完成通知。
- 按 Job 粒度配置通知策略 / 只失败才提醒：第一版只做全局配置 + 三种终态全部通知；`RunCompletionHook` 与 notification domain 保留扩展点，不给 `sync_jobs` 加通知策略字段。

## Consequences

- `internal/syncjob` 新增 `RunCompletion`（RunID / JobID / JobName / SourceID / Trigger / State / StartedAt / FinishedAt / Stats / Error）与 `RunCompletionHook` 接口；`finalize` 签名从 sourceID 改为携带整个 Job——事件自带 JobName / SourceID，通知线程不再读取可能已变更的 Job 配置。
- migration 0013 新增 singleton 表 `notification_settings`（id=1，Pushover 与 Email 两组列），secret 明文落库；**API 绝不回显 secret**，GET 只回 `token_configured` / `password_configured` 等布尔。
- REST 面为 `/api/v1/notifications/settings`（GET / PATCH，admin scope）与 `/api/v1/notifications/test/{pushover,email}`（用**已保存**的配置发送测试，浏览器不重传密码）；PATCH 的 secret 走三态语义：字段不存在→保留、字符串→替换、`clear_*: true`→删除，空字符串不同时解释为「保留」与「删除」。
- 优雅关闭顺序在 runner 与 server 之间插入 notification drain：scheduler.Stop → runner.Shutdown（最后几个 run 完成并产生事件）→ notifications.Shutdown（排空队列）→ server.Shutdown → WAL checkpoint → DB close。
- Pushover 用标准库 form-urlencoded POST；Email 用标准库 SMTP 三模式（none / starttls / tls，覆盖 465 隐式 TLS 与 587 STARTTLS），第一版只发 text/plain; charset=UTF-8。两渠道共享同一 formatter，「文件变更 = 新增 + 更新 + 删除」（relinquish 不算变更），`BytesTransferred` 文案为「同步数据量」而非「网络流量」（它统计成功同步文件的 payload 大小，不含重试与协议开销）。
- 发送失败只产生 `event=notification channel=... run_id=... status=failed` 结构化日志，**绝不输出 token / user key / 密码**；队列满时丢弃并记日志，不阻塞 finalize。
