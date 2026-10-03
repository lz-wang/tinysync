# TinySync

TinySync 是面向 HomeLab 的文件同步服务，将 WebDAV、S3、SFTP、SMB、GitHub Release 或运行主机上的文件单向同步到本地。内置中文 Web 界面，支持定时同步、运行进度与历史、完成通知，以及通过公开链接分享本地文件。

## 安装与开始使用

从 [Releases](https://github.com/lz-wang/tinysync/releases) 下载适合 Linux、macOS 或 Windows 的发行包（amd64 / arm64），解压即可运行，无需安装其他运行时。Linux 常驻服务配置见[部署指南](docs/guides/deployment.md)。

```bash
./tinysync serve --datadir ./data --port 9466
```

打开 `http://127.0.0.1:9466`，使用首次启动时终端输出的管理员密码登录，并在「用户设置」中修改密码。

1. 在「同步源」中添加文件来源，填写连接信息并测试访问。
2. 在「同步任务」中选择同步源、源端路径和本地目标目录，设置同步模式与计划。
3. 手动运行一次，查看进度与运行结果；之后按计划自动同步。

本地目标目录位于运行 TinySync 的主机上，服务用户需要具有写入权限。通过其他设备访问时使用主机地址；对外部署请配置 HTTPS，反向代理需转发 `X-Forwarded-Proto: https`。

## 同步源

| 类型 | 配置与用途 |
| --- | --- |
| WebDAV | 服务地址、根目录、用户名与密码，适用于 NAS 等 WebDAV 服务 |
| S3 / MinIO | 服务地址、区域、存储桶、路径前缀与访问密钥；支持 path-style |
| SFTP | 主机、端口、用户名与根目录，支持密码或 SSH 私钥认证、主机密钥指纹校验 |
| SMB / CIFS | 主机、共享名、共享内路径、用户名与密码，可填写域 |
| GitHub Release | 仓库地址，选择最新版本、指定 Tag、最近 N 个或全部版本；私有仓库需提供 Token |
| 本地文件 | 运行主机上的源根目录，无需凭据，可在界面中浏览、选择或创建目录 |

SFTP 可在保存前检查连接与目录权限。SSH 私钥可保存到「凭据」中供多个同步源引用，更新一条凭据后，引用它的源在下一轮同步使用新私钥。GitHub Release 支持「测试并预览」版本和制品，预发布版本默认排除。

本地文件源可同步整个目录或其中的子目录。例如源根目录为 `/data/media`、源端路径为 `/photos`、目标为 `/backup/photos`，实际同步的是 `/data/media/photos → /backup/photos`。源目录与目标目录不能相同或互相包含；只同步普通文件和目录，发现符号链接、特殊文件或目录读取失败时，本轮同步失败。

## 同步任务

- **Copy**：新增和更新文件，保留目标目录中多出的文件。
- **Mirror**：在 Copy 的基础上，删除源端已消失且由当前任务管理的文件；目标目录中原有的其他文件不会因此被删除。

任务支持手动运行、单次定时、固定间隔和 Cron 计划，可通过 include / exclude 规则筛选文件。计划时间默认使用运行主机的时区；同一任务仍在运行时，新的计划触发会跳过。

运行中可查看阶段、文件进度和传输量，并手动停止。运行历史保留结果、耗时与文件变更明细，重启后仍可查询。

在「设置 → 通知」中配置 Pushover 或 SMTP 邮件，任务成功、失败或取消后会发送结果摘要。填写配置后先保存，再发送测试通知或测试邮件。

## 文件浏览与共享

点击同步源或任务名称可直接打开文件管理，也可在「源端文件」和「本地文件」之间切换，浏览目录与下载文件。

在本地文件页可共享单个文件、目录或整个任务目标目录。共享名称可自定义，也可自动生成；在「共享管理」中设置过期时间或禁用链接。

- 浏览链接：`https://tinysync.example/shared/<共享名称>`
- 文件直链：`https://tinysync.example/shared/<共享名称>/<文件路径>`

持有链接的人无需登录即可访问。共享目录会公开其下的文件，包括并非由 TinySync 同步的文件；公开页面隐藏点文件和符号链接。更改共享名称会更改 URL，旧链接随即失效。

## 服务配置

命令行参数优先于环境变量。默认数据目录为 `./data`，端口为 `9466`。

| 参数 | 环境变量 | 默认值 / 用途 |
| --- | --- | --- |
| `--datadir` | `TINYSYNC_DATADIR` | `./data`，保存配置、运行历史、备份与日志 |
| `--port` | `TINYSYNC_PORT` | `9466`，Web 界面与 API 端口 |
| `--max-concurrent-jobs` | `TINYSYNC_MAX_CONCURRENT_JOBS` | `1`，同时运行的任务数 |
| `--max-concurrent-transfers` | `TINYSYNC_MAX_CONCURRENT_TRANSFERS` | `4`，同时传输的文件数 |
| `--transfer-timeout` | `TINYSYNC_TRANSFER_TIMEOUT` | `0`，不限制单文件单次传输耗时；可设为 `30m`、`1h` 等 |

## API 与 MCP 接入

在 Web 界面的「API Tokens」中创建令牌，完整令牌只显示一次，可设置有效期并随时撤销。脚本通过 `Authorization: Bearer <令牌>` 访问 REST API。

| 权限 | 能力 |
| --- | --- |
| `read` | 查询同步源、任务、运行记录与文件，下载文件 |
| `run` | 手动触发任务，不包含查询权限 |
| `admin` | 全部权限，包括修改配置与管理令牌 |

```bash
curl -H "Authorization: Bearer $TINYSYNC_TOKEN" \
  http://127.0.0.1:9466/api/v1/sources
```

Agent 可通过 MCP 查询同步源与任务、触发同步、搜索已同步文件和读取小型文本。使用 Streamable HTTP 连接 `/mcp`，建议创建同时具有 `read` 和 `run` 权限的令牌：

```json
{
  "url": "https://tinysync.example/mcp",
  "headers": {
    "Authorization": "Bearer ts_xxx"
  }
}
```

## 备份与维护

同一数据目录只能运行一个实例。执行数据库检查、备份或恢复前，请先停止服务：

```bash
./tinysync db check --datadir ./data
./tinysync db backup --datadir ./data
./tinysync db restore --datadir ./data --from "./data/backups/<备份文件>.db" --force
```

备份保存在 `<数据目录>/backups/`，包含登录与同步源凭据，请妥善保存；同步文件所在目录需要单独备份。恢复会覆盖当前数据库，执行前会自动备份当前库。

忘记管理员密码时，可在运行主机执行 `./tinysync auth set-password --datadir ./data` 重置，已有登录会话随即失效。使用 `./tinysync version` 查看版本；运行日志位于 `<数据目录>/logs/tinysync.log`。

版本变化见[更新日志](CHANGELOG.md)。

## License

[MIT](LICENSE)
