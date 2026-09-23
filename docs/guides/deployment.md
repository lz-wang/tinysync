# 部署（systemd）

把 TinySync 作为常驻服务部署在 Linux + systemd 主机上的操作手册。命令契约与
配置项以 [README](../../README.md) 为准，版本、打包与发布见[构建与发布](release.md)，
本地开发运行方式见[开发与验证](development.md)。本文只覆盖部署形态、生命周期
与运维边界。

## 前置条件

| 项 | 要求 |
| --- | --- |
| 系统 | Linux（amd64 或 arm64），systemd |
| 运行时依赖 | 无——单二进制，内嵌 Web UI，无 CGO，不需要 Node / Go / 数据库服务 |
| 监听 | 默认 `9466/tcp`；监听地址固定为 `:<port>`（绑定全部网卡，没有 `--host` 参数） |
| 数据目录 | 独立目录，进程全程独占 `<datadir>/tinysync.lock` |
| 管理员密码 | 首次启动前初始化，至少 12 字符、不超过 1024 字节 |
| 同步目标 | 每个 Job 的 `local_root` 目录（含 Mirror 需要的删除权限） |

版本号唯一事实来源是 Git：`tinysync --version` 输出 `X.Y.Z` 表示正式版本，
输出 `dev-<commit日期>-<commit7>` 表示开发快照。生产部署使用正式版本。

## 1. 安装二进制

发行档命名 `tinysync_<version>_<os>_<arch>.tar.gz`（Windows 为 `.zip`），与同名
`.sha256`、`checksums.txt` 一起发布在 GitHub Release。`.sha256` 内容只含文件名，
可在下载目录直接校验：

```sh
VERSION=0.11.0
case "$(uname -m)" in
  x86_64)         ARCH=amd64 ;;
  aarch64|arm64)  ARCH=arm64 ;;
esac
asset="tinysync_${VERSION}_linux_${ARCH}.tar.gz"
base="https://github.com/lz-wang/tinysync/releases/download/v${VERSION}"

curl -fsSLO "${base}/${asset}"
curl -fsSLO "${base}/${asset}.sha256"
sha256sum -c "${asset}.sha256"
tar -xzf "${asset}"
sudo install -o root -g root -m 0755 tinysync /usr/local/bin/tinysync
tinysync --version
```

内网 / 离线部署可从源码自行构建：`make build` 产出当前平台的 `./tinysync`
（含 Web UI），`make dist` 产出六平台发行档；两条路径都不需要目标机具备构建工具。

## 2. 服务用户

服务以专用非特权用户运行；数据目录由 systemd 的 `StateDirectory` 创建，
无需手工 `mkdir`：

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin tinysync
```

同步写入的 `local_root` 目录必须让该用户可写（Mirror 还需删除权限）。
若数据目录或同步目标位于文件系统 ACL / NFS 上，按该文件系统的语义另行授权。

## 3. 环境文件

服务参数只有环境变量与命令行两个来源，命令行优先（见 README）。把环境变量
放进独立文件，便于改端口 / 并发而不用改 unit：

```sh
sudo install -d -m 0755 /etc/tinysync
sudo tee /etc/tinysync/tinysync.env >/dev/null <<'EOF'
TINYSYNC_DATADIR=/var/lib/tinysync
TINYSYNC_PORT=9466
# 同时运行的同步 Job 数上限（默认 1）
#TINYSYNC_MAX_CONCURRENT_JOBS=1
# 同时进行的远端文件下载上限（默认 4）
#TINYSYNC_MAX_CONCURRENT_TRANSFERS=4
# 单文件单次传输尝试的超时，如 1h / 30m；0（默认）表示不启用
#TINYSYNC_TRANSFER_TIMEOUT=0
EOF
sudo chmod 0644 /etc/tinysync/tinysync.env
```

## 4. 初始化管理员密码

REST API 与 Web UI 全面 default-deny，必须要有管理员密码才能登录。
两条路径任选其一：

**A. 启动前预置（推荐）**：密码不进入 systemd journal，也不依赖首次启动的
终端输出。`auth set-password` 会同时完成数据库 migration。必须以服务用户身份
执行，否则生成的文件归 root，服务进程写不进去：

```sh
read -rs TINYSYNC_ADMIN_PASSWORD
printf '%s\n' "$TINYSYNC_ADMIN_PASSWORD" | sudo -u tinysync /usr/local/bin/tinysync auth set-password \
  --datadir /var/lib/tinysync --password-stdin
unset TINYSYNC_ADMIN_PASSWORD
# 输出：管理员密码已初始化，现在可以启动 tinysync serve。
```

（`sudo` 需要密码时先执行 `sudo -v`，避免与管道 stdin 冲突。）

**B. 首次启动自动 bootstrap**：未初始化密码时 `serve` 生成高熵密码，并只把
它打印到**当前进程 stderr 一次**——在 systemd 下即进入 journal：

```sh
sudo journalctl -u tinysync -n 50 --no-pager | grep 初始管理员密码
```

拿到后立即登录并在 Web UI「用户设置」中改密。注意该明文会长期留在
journald 中，journald 保留策略与访问控制因此成为凭据保护的一部分。

密码重置（遗忘密码、轮换）共用同一命令。它不持有 datadir 锁，**服务运行中
也能执行**，成功后会立即废弃全部已有 Web Session：

```sh
printf '%s\n' "$NEW_PASSWORD" | sudo -u tinysync /usr/local/bin/tinysync auth set-password \
  --datadir /var/lib/tinysync --password-stdin
# 输出：管理员密码已更新，全部已有 Web Session 已废弃。
```

## 5. systemd unit

```sh
sudo tee /etc/systemd/system/tinysync.service >/dev/null <<'EOF'
[Unit]
Description=TinySync 文件同步服务
Documentation=https://github.com/lz-wang/tinysync
After=network-online.target
Wants=network-online.target
StartLimitBurst=5
StartLimitIntervalSec=60s

[Service]
Type=simple
User=tinysync
Group=tinysync
EnvironmentFile=-/etc/tinysync/tinysync.env
ExecStart=/usr/local/bin/tinysync serve
Restart=on-failure
RestartSec=5s
TimeoutStopSec=300s
UMask=0027

# 数据目录：由 systemd 创建并归属服务用户，且自动对 ProtectSystem=strict 可写
StateDirectory=tinysync
StateDirectoryMode=0750

# 同步目标（Job 的 local_root）；按实际路径增补
ReadWritePaths=/srv/tinysync

# 基础加固
NoNewPrivileges=yes
CapabilityBoundingSet=
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictSUIDSGID=yes
RestrictNamespaces=yes
LockPersonality=yes
RemoveIPC=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemd-analyze verify /etc/systemd/system/tinysync.service
```

需要按现场调整的几条：

| 指令 | 说明 |
| --- | --- |
| `EnvironmentFile` | 前缀 `-` 表示文件缺失不报错；参数也可直接写进 `ExecStart` 追加在 `serve` 之后，命令行优先于环境变量 |
| `StateDirectory=tinysync` | 数据目录即 `/var/lib/tinysync`，与 `TINYSYNC_DATADIR` 必须一致；该目录被 systemd 自动加入 `ReadWritePaths` |
| `ReadWritePaths` | `ProtectSystem=strict` 下整个文件系统只读，Job 的 `local_root` **必须**列在此处，否则同步写入被拒（表现为运行失败、`Read-only file system`） |
| `ProtectHome=yes` | `/home`、`/root`、`/run/user` 对服务不可见；数据目录与同步目标不要放在这些位置 |
| `TimeoutStopSec` | 优雅关闭要等在途传输收敛，大文件场景留足时间；默认 90s 对 GB 级下载偏紧 |
| `Restart=on-failure` | 进程 crash 后自动重启；启动收敛逻辑保证 crash 遗留状态被纠正 |

进程被 `SIGKILL` 强杀也安全：数据正确性由 SQLite WAL recovery 保证，crash 遗留
的下载临时文件与 `running` 运行记录在下次启动时被清理 / 收敛为 failed。

## 6. 启动与验收

```sh
sudo systemctl enable --now tinysync
systemctl status tinysync --no-pager
```

公开端点用于验收（无需凭据）：

```sh
curl -fsS http://127.0.0.1:9466/api/v1/health     # {"status":"ok"}
curl -fsS http://127.0.0.1:9466/api/v1/version    # {"version":"0.11.0"}
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9466/api/v1/sources   # 401
```

登录并确认会话建立，随后在浏览器打开 `http://<host>:9466`：

```sh
curl -fsS -i -X POST http://127.0.0.1:9466/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"password":"<管理员密码>"}' | head -n 1     # HTTP/1.1 200 OK + Set-Cookie
```

`401` 是预期行为而非故障：匿名可访问的只有 `GET /api/v1/health`、
`GET /api/v1/version`、`POST /api/v1/auth/login` 与带唯一 slug 的
`/shared/<slug>` 公开共享。

## 7. HTTPS 与反向代理

认证凭据应经 HTTPS 传输：反向代理转发 `X-Forwarded-Proto: https` 时，会话
Cookie 自动附加 `Secure`。MCP 端点 `/mcp` 与公开共享页同样经由该代理暴露。

nginx：

```nginx
server {
    listen 443 ssl;
    server_name tinysync.example.com;

    location / {
        proxy_pass http://127.0.0.1:9466;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_buffering off;            # 大文件下载直通，不落代理磁盘
        proxy_read_timeout 3600s;
    }
}
```

Caddy（自动签发证书，自动设置 `X-Forwarded-Proto`）：

```
tinysync.example.com {
    reverse_proxy 127.0.0.1:9466
}
```

服务监听 `:<port>`（全部网卡）且没有绑定地址参数。仅需经反向代理暴露或只允许
本机访问时，用主机防火墙限制 9466 端口；若改用 systemd 的
`IPAddressDeny=any` + `IPAddressAllow=localhost` 做同样限制，注意它同时约束
**出站**连接——需要连接到 LAN 上的 WebDAV / S3 / SFTP 远端时，必须把那些
网段一并加入 `IPAddressAllow`。

## 8. 日志

| 来源 | 内容 |
| --- | --- |
| `<datadir>/logs/tinysync.log` | DEBUG 及以上；50 MB 轮转、保留 7 份、gzip 压缩 |
| stderr → journald | INFO 及以上（启动、每轮同步 `event=sync_run`、访问日志、关闭） |

```sh
journalctl -u tinysync -f                # 跟踪
journalctl -u tinysync --since today     # 当日
journalctl -u tinysync | grep '<request_id>'   # 按请求定位（响应头 X-Request-ID 同值）
```

应用自身完成日志轮转，无需 logrotate；journald 侧的保留策略按主机统一配置。

## 9. 停机、重启与恢复

`systemctl stop|restart tinysync` 发送 `SIGTERM`，进程按冻结契约收口：停止调度
→ 取消在途运行并将终态落库 → 排空存量 HTTP 请求 → WAL checkpoint 截断 →
关闭数据库 → 释放 datadir 锁，正常退出码为 0。干净退出不遗留膨胀的 WAL 文件。

```sh
sudo systemctl stop tinysync && sudo systemctl start tinysync
```

同一数据目录只允许一个实例：第二个进程立即失败并打印
`datadir ... is owned by another tinysync process`。systemd 下这通常意味着配了
两个 unit 指向同一 `TINYSYNC_DATADIR`，或旧进程未被回收；`Restart=on-failure`
会把这种冲突放大成反复重启，用 `StartLimitBurst` 兜底后检查配置。

数据库损坏、crash 恢复与升级语义见 README 的 Operations 章节；进程异常中断
不依赖任何清理逻辑，重启即自动收敛。

## 10. 升级、降级与回滚

升级即替换二进制后重启，migration 自动完成（先完整性检查 → 自动生成一致性
备份到 `<datadir>/backups/tinysync-v<版本>-*.db` → 再迁移）：

```sh
VERSION=0.12.0   # 新版本
# 按第 1 步下载并校验新发行档
sudo systemctl stop tinysync
sudo install -o root -g root -m 0755 tinysync /usr/local/bin/tinysync
sudo systemctl start tinysync
/usr/local/bin/tinysync --version && journalctl -u tinysync -n 20 --no-pager
```

- 启动关键路径上的 migration 不因启动即收到的取消信号中断，退出行为与数据库
  状态是确定的。
- **不支持降级**：schema 版本高于当前二进制时 `serve` 拒绝启动。回滚到旧版本
  必须先 `db restore` 一份该版本可读的备份，升级后写入的数据会丢失。
- 备份前不要手工替换数据库文件；只用 `tinysync db` 系列命令。

## 11. 备份与数据库维护

维护命令与 `serve` 共用同一把 datadir 锁，**服务运行期间一律被拒绝**：

```text
$ tinysync db backup --datadir /var/lib/tinysync
datadir /var/lib/tinysync is owned by another tinysync process: resource temporarily unavailable
```

固定顺序为「停服 → 维护 → 启服」：

```sh
sudo systemctl stop tinysync
sudo -u tinysync /usr/local/bin/tinysync db check   --datadir /var/lib/tinysync
sudo -u tinysync /usr/local/bin/tinysync db backup  --datadir /var/lib/tinysync
sudo systemctl start tinysync

# 只在确需回退时执行；restore 是破坏性操作，必须 --force，
# 会先自动生成当前库的 safety backup，再原子替换并复验
sudo -u tinysync /usr/local/bin/tinysync db restore --datadir /var/lib/tinysync \
  --from /var/lib/tinysync/backups/tinysync-manual-<timestamp>-<hash>.db --force
```

备份文件写入 `<datadir>/backups/`，权限为 0600，但**内容包含 Source 凭据与认证
数据**，应与主数据库同等敏感级别保护（异地存储前加密，不要放进公开可读目录）。
`<datadir>` 整体也应按此规则限制访问。

需要定时备份时同样受这条约束：维护命令无法与服务并存，自动化只能在短暂停机
窗口内执行（停服 → 备份 → 启服），不存在「服务不停机即完成一致性备份」的路径。
备份产物轮转与保留由 operator 管理，TinySync 不为 `backups/` 做清理。

## 12. 故障排查

| 现象 | 原因与处理 |
| --- | --- |
| unit 反复重启，日志含 `owned by another tinysync process` | 两个实例或两个 unit 共用同一 `TINYSYNC_DATADIR`；只保留一个，并核对 `StateDirectory` 与 `TINYSYNC_DATADIR` 一致 |
| 启动即失败，`address already in use` | 端口被占用；改 `TINYSYNC_PORT` 或释放端口 |
| 同步运行失败，日志出现 `read-only file system` / 权限拒绝 | `ProtectSystem=strict` 下 Job 的 `local_root` 未列入 `ReadWritePaths`，或目录归属不是服务用户 |
| 启动失败，日志提示无法创建数据目录 / 打开锁文件 | 数据目录归属或权限不对（常见于手工以 root 创建后用非 root 运行） |
| 首次启动无终端密码输出 | 密码只打印一次且只在首次 bootstrap 打印；到 journal 里找，或直接停服重置密码 |
| 浏览器登录后立刻掉线 | 反向代理未转发 `X-Forwarded-Proto` 时 Cookie 行为与浏览器安全策略不匹配；补齐代理头 |
| API 返回 401 | default-deny 的预期结果；改用管理员登录建立 Session，或创建 API Token（`Authorization: Bearer`） |
| 同数据目录上第二个进程被拒 | 单实例约束，正常行为 |

进一步收紧加固可用 `systemd-analyze security tinysync.service` 打分，并按建议
逐条追加指令——每加一条都要验证启动与一轮真实同步。`SystemCallFilter`、
`MemoryDenyWriteExecute` 一类强约束不属于基础模板，启用前需在目标机验证。

## 13. 卸载

```sh
sudo systemctl disable --now tinysync
sudo rm /etc/systemd/system/tinysync.service
sudo systemctl daemon-reload
sudo rm -rf /etc/tinysync
sudo rm /usr/local/bin/tinysync
```

`/var/lib/tinysync`（数据库、备份、日志）与同步目标目录**不在**卸载范围内：
前者含 Source 凭据与共享状态，后者是用户数据。确认无需保留后再手工删除。
