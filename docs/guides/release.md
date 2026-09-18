# 构建与发布

涉及版本、CI、打包或发布时阅读。命令以 [Makefile](../../Makefile) 为准，
整体进度见 [ROADMAP.md](../../ROADMAP.md)，常规检查见[开发与验证](development.md)。

## 版本与产物

- 版本号只来自 Git：HEAD 精确位于 `vX.Y.Z` tag 时为 `X.Y.Z`，否则为 `dev-<commit日期>-<commit7>`；`make version` 查看解析结果，构建经 `-ldflags "-X tinysync/internal/buildinfo.Version=..."` 注入。
- Makefile 的 `VERSION` 参数只供构建传入已确定的版本，不引入 VERSION 文件、package version、config version 等第二事实来源；发布时必须与 tag 一致。
- `-tags webui` 嵌入 `web/dist`；无 tag 时嵌入 `web/fallback`。全工程禁用 CGO，workflow 顶层也设置 `CGO_ENABLED: "0"`。
- `make build-all` 跨编译六平台；`make build-os OS=linux ARCH=amd64` 构建单平台；`make package-os OS=linux ARCH=amd64` 打包该平台并生成同名 `.sha256`。
- `make dist` 先清理构建产物，再构建 Linux / macOS / Windows × amd64 / arm64 六个平台，在 `dist/` 生成发行档、逐文件 `.sha256` 和 `checksums.txt`。发行档包含二进制、README 与 LICENSE，Windows 为 zip，其余为 tar.gz。

## 工作流职责

| 工作流 | 触发与职责 |
| --- | --- |
| [build.yml](../../.github/workflows/build.yml) | main push、面向 main 的 PR、手动触发；纯文档变更（任意 Markdown 或 `docs/`）跳过自动构建，其他变更执行 `make ci`、覆盖率/Codecov、六平台构建与三平台原生 Smoke，不生成 GitHub Release |
| [release.yml](../../.github/workflows/release.yml) | tag push 或手动指定已有 tag；校验稳定版 `vX.Y.Z`、`make version` 和 CHANGELOG 段落，执行 `make ci` 与三平台原生 Smoke，再发布 |

- Build 中 PR 仅验证可构建；main push / 手动运行额外打包开发快照，按版本目录镜像到 WebDAV，附逐文件 `.sha256`。
- Release 的 publish job 在单一 runner 一次性 cross-build 六平台、打包并发布 GitHub Release，资产为六个发行档及 `checksums.txt`。不使用 Actions Artifact 在 job 间传输或分发产物，也不把 WebDAV 当中转站。
- GitHub Release 成功后再镜像 archives + `checksums.txt` 到 WebDAV。开发和正式镜像失败均不阻断主流程，GitHub Release 始终是权威分发渠道。
- Release Notes 唯一来源是 [CHANGELOG.md](../../CHANGELOG.md) 对应版本段，由 [release-notes.sh](../../scripts/release-notes.sh) 提取，不根据 commit message 自动生成。
- 两个工作流均配置 Pushover 结果通知，缺少通知凭证时跳过；工作流配置存在不等于镜像或通知实际送达。

## 本地验证与发布验收

变更启动、版本或发布链路时，在全量检查后按需验证本机二进制：

```sh
make build
bash scripts/smoke.sh ./tinysync "$(make version)"
```

Windows 将二进制路径换为 `./tinysync.exe`，在 Git Bash 执行并与 workflow 一样设置
`RUNNER_OS=Windows`。[smoke.sh](../../scripts/smoke.sh) 使用临时 datadir，验证两个
版本入口、health、首页与退出行为；POSIX 检查 SIGTERM 优雅退出，Windows 使用强制清理。
需要避开默认 Smoke 端口时设置 `TINYSYNC_SMOKE_PORT`。

用户要求发布时：

1. 核对工作区、目标提交和 CHANGELOG 对应版本段，执行 `make ci`；只纳入本次发布内容。
2. 使用不可变 `vX.Y.Z` tag 指向明确提交，推送后由 Release workflow 发布；已有 tag 可通过手动触发重试，不移动已公开 tag。
3. 核对真实远端 run / job 结果、tag 对应提交、Release Notes、六个发行档和 `checksums.txt`；本地构建不能替代远端验收。
4. 单独记录 WebDAV 镜像与通知结果，不能从主流程成功推断其成功。同步更新阶段方案与 ROADMAP 的验收状态。

开发检查、GitHub 发布和镜像配置保持分离；不要为文档整理或普通代码修改触发发布。
