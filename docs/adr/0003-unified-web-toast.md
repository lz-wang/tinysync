# Web UI 通知统一为全局右下角 toast

此前操作反馈四处各自为政：同步源页自建 Snackbar（底部居中），共享管理、设置与任务页用内联可关闭 Alert，文件浏览器又各有内联错误展示——位置、消失行为与严重等级配色互不一致。我们决定收敛为唯一的全局 toast：右下角 MUI Snackbar 承载 `variant="filled"` 的 Alert，按 severity（success/error/warning/info）分色，5 秒统一自动消失，新 toast 替换旧 toast；入口收敛为 app 层 `ToastProvider` 与 `useToast()`。页面加载失败时内容区保留一行简短的失败文案，错误详情只经 toast 提示。对话框内表单错误、空态提示与公开浏览页（SharedBrowsePage）的错误展示不属于瞬时通知，保留内联 Alert。

## Considered Options

- 引入 notistack：免费获得堆叠队列，但为一个通知场景新增第三方运行时依赖，与工程零依赖倾向相悖。
- 维持各页面自建（原状）：没有单一事实来源，位置与行为漂移正是本次要修复的缺陷。
- 加载失败保留内联占位、仅操作反馈走 toast：错误持续可见，但同一页面「初始加载失败」与「操作失败」两种反馈形态分裂，且内联错误把内容区推挤下移。
- 按 severity 区分自动消失时长：严重信息停留更久，但多一个随等级变化的变量，先取统一 5 秒。

## Consequences

- 默认右下角由主题 `MuiSnackbar.defaultProps.anchorOrigin` 单点声明（MUI 出厂默认是左下角），个别场景可在调用点覆盖。
- 同一时刻只展示一条 toast：连续操作失败时较早的错误会被替换，接受这一损失以换取实现简单与视觉不堆叠。
- 加载失败的错误详情 5 秒后不可再查；可靠性要求「错误持续可见」的场景（公开浏览页、对话框表单校验）必须继续使用内联 Alert，不得迁移。
