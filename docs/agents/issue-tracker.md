# Issue tracker: GitHub

本仓库的工单与规格以 GitHub issues 形式存在。所有操作使用 `gh` CLI。

## 惯例

- **创建工单**：`gh issue create --title "..." --body "..."`。多行正文使用 heredoc。
- **读取工单**：`gh issue view <number> --comments`，用 `jq` 过滤评论并同时获取标签。
- **列出工单**：`gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'`，按需附加 `--label` 与 `--state` 过滤。
- **评论工单**：`gh issue comment <number> --body "..."`
- **加/移标签**：`gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **关闭**：`gh issue close <number> --comment "..."`

在克隆目录内运行时 `gh` 会自动从 `git remote -v` 推断仓库。

## PR 作为 triage 来源

**PRs as a request surface: no.** _（若本仓库将外部 PR 视为功能请求来源，改为 `yes`；`/triage` 读取此旗标。）_

设为 `yes` 时，PR 使用与 issue 相同的标签和状态，通过 `gh pr` 等价命令操作：

- **读取 PR**：`gh pr view <number> --comments`，用 `gh pr diff <number>` 获取 diff。
- **列出待 triage 的外部 PR**：`gh pr list --state open --json number,title,body,labels,author,authorAssociation,comments`，仅保留 `authorAssociation` 为 `CONTRIBUTOR`、`FIRST_TIME_CONTRIBUTOR` 或 `NONE` 的条目（剔除 `OWNER`/`MEMBER`/`COLLABORATOR`）。
- **评论 / 标签 / 关闭**：`gh pr comment`、`gh pr edit --add-label`/`--remove-label`、`gh pr close`。

GitHub 的 issue 与 PR 共享同一编号空间，裸 `#42` 可能是其中之一：先用 `gh pr view 42` 解析，失败再退回 `gh issue view 42`。

## 当技能说 "publish to the issue tracker"

创建一个 GitHub issue。

## 当技能说 "fetch the relevant ticket"

执行 `gh issue view <number> --comments`。

## Wayfinding 操作

供 `/wayfinder` 使用。**map** 是单条 issue，**child** issues 作为工单。

- **Map**：一条打有 `wayfinder:map` 标签的 issue，承载 Notes / Decisions-so-far / Fog 正文。`gh issue create --label wayfinder:map`。
- **Child 工单**：作为 GitHub sub-issue 关联到 map 的 issue（通过 sub-issues 端点的 `gh api`）。sub-issues 不可用时，把 child 加入 map 正文中的任务列表，并在 child 正文顶部写 `Part of #<map>`。标签：`wayfinder:<type>`（`research`/`prototype`/`grilling`/`task`）。被认领后，工单分配给主导开发者。
- **阻塞**：使用 GitHub **原生 issue dependencies**，这是权威且 UI 可见的表示。用 `gh api --method POST repos/<owner>/<repo>/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>` 添加边，其中 `<blocker-db-id>` 是阻塞者的数字 **database id**（`gh api repos/<owner>/<repo>/issues/<n> --jq .id`，_不是_ `#number` 或 `node_id`）。GitHub 通过 `issue_dependencies_summary.blocked_by` 报告（仅开放阻塞者，即实时门控）。依赖功能不可用时，退回到 child 正文顶部的 `Blocked by: #<n>, #<n>` 行。当所有阻塞者关闭时工单即解锁。
- **Frontier 查询**：列出 map 的开放 children（`gh issue list --state open`，限定在 map 的 sub-issues / 任务列表内），剔除有任何开放阻塞者（`issue_dependencies_summary.blocked_by > 0`，或 `Blocked by` 行中有开放 issue）或已有 assignee 的工单；map 顺序中第一个胜出。
- **Claim**：`gh issue edit <n> --add-assignee @me`，作为会话的首次写入。
- **Resolve**：`gh issue comment <n> --body "<answer>"`，然后 `gh issue close <n>`，再把上下文指针（gist + 链接）追加到 map 的 Decisions-so-far。
