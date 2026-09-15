#!/usr/bin/env bash

# 跨平台原生 Smoke 测试：先验证实际 binary 的版本输出，再验证核心行为。
# 在 Linux / macOS / Windows（Git Bash）runner 上针对原生构建的二进制执行：
#
#	1. tinysync --version 与预期版本一致
#	2. tinysync version 与预期版本一致
#	3. 启动 tinysync serve（临时 datadir、独立端口）
#	4. GET /api/v1/health 轮询至就绪且 "status":"ok"
#	5. GET / 返回 WebUI（<title>TinySync</title>）
#	6. POST /api/v1/sources 创建 WebDAV Source，响应不含密码明文
#	7. GET /api/v1/sources 列表可见且同样不含密码，tinysync.db 已创建
#	8. POST /api/v1/jobs 创建引用该 Source 的 Copy Job
#	9. POST /api/v1/jobs/:id/run 异步启动（202），对不可达远端收敛为 failed
#	10. 关闭进程并以同一 datadir 重启
#	11. GET /api/v1/sources/:id 确认 Source（含密码标志）跨重启持久化
#	12. Job 配置跨重启持久化，运行状态回到 idle（运行记录只存内存）
#	13. SIGTERM 优雅退出（Windows 为强制清理）
#
# 接口：scripts/smoke.sh <binary> <expected-version>

set -euo pipefail

if [[ $# -ne 2 ]]; then
	echo "Usage: scripts/smoke.sh <binary> <expected-version>" >&2
	exit 2
fi

binary_input=$1
expected_version=$2
if [[ ! -f "${binary_input}" ]]; then
	echo "Error: smoke binary does not exist: ${binary_input}" >&2
	exit 2
fi

binary_dir=$(cd "$(dirname "${binary_input}")" && pwd -P)
binary="${binary_dir}/$(basename "${binary_input}")"
smoke_root=$(mktemp -d)
datadir="${smoke_root}/data"
server_pid=""
port=${TINYSYNC_SMOKE_PORT:-19466}
base_url="http://127.0.0.1:${port}"

cleanup() {
	if [[ -n "${server_pid}" ]]; then
		if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
			# Windows 无法向 bash 后台 PID 发 POSIX 信号，按映像名强制清理。
			taskkill.exe //IM "$(basename "${binary}")" //T //F >/dev/null 2>&1 || true
		else
			kill -TERM "${server_pid}" >/dev/null 2>&1 || true
			if ! wait "${server_pid}" >/dev/null 2>&1; then
				echo "Error: serve did not exit cleanly on SIGTERM" >&2
				exit 1
			fi
		fi
	fi
	if [[ -n "${smoke_root}" && -d "${smoke_root}" ]]; then
		rm -rf -- "${smoke_root}"
	fi
}
trap cleanup EXIT

# stop_server 关闭当前 serve 进程：POSIX 平台发 SIGTERM 并 wait 校验退出码；
# Windows 的 Git Bash kill 对原生进程不可靠，交给 taskkill 强制清理
# （WAL 模式下强杀不破坏已提交事务，重启校验可覆盖该场景）。
stop_server() {
	if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
		taskkill.exe //IM "$(basename "${binary}")" //T //F >/dev/null 2>&1 || true
		sleep 2
	else
		kill -TERM "${server_pid}" >/dev/null 2>&1
		wait "${server_pid}" >/dev/null 2>&1
		server_pid=""
	fi
}

# start_server 以指定日志文件后台启动 serve。
start_server() {
	local log_file=$1
	"${binary}" serve --datadir "${datadir}" --port "${port}" >"${log_file}" 2>&1 &
	server_pid=$!
}

# wait_ready 轮询 /api/v1/health 直至就绪；失败时输出 serve 日志。
wait_ready() {
	local log_file=$1
	for _ in {1..30}; do
		if curl --fail --silent "${base_url}/api/v1/health" >health.json; then
			grep -F '"status":"ok"' health.json >/dev/null
			return 0
		fi
		sleep 1
	done
	echo "Error: server did not become ready" >&2
	cat "${log_file}" >&2
	exit 1
}

# 临时产物统一落在 smoke_root，不污染调用方目录；
# binary 已解析为绝对路径，cd 安全。
cd "${smoke_root}"

# 1. --version 与预期一致（验证 ldflags 注入链路）。
actual_version=$("${binary}" --version)
if [[ "${actual_version}" != "${expected_version}" ]]; then
	echo "Error: version output \"${actual_version}\", expected \"${expected_version}\"" >&2
	exit 1
fi
echo "[smoke] --version ${actual_version}"

# 2. version 子命令与 --version 一致。
sub_version=$("${binary}" version)
if [[ "${sub_version}" != "${expected_version}" ]]; then
	echo "Error: version subcommand output \"${sub_version}\", expected \"${expected_version}\"" >&2
	exit 1
fi
echo "[smoke] version subcommand"

# 3-4. 启动 serve 并等待就绪（临时 datadir，避免污染工作目录）。
start_server serve.log
wait_ready serve.log
echo "[smoke] health ok"

# 5. WebUI 首页可访问且标题正确。
curl --fail --silent "${base_url}/" >index.html
grep -F '<title>TinySync</title>' index.html >/dev/null
echo "[smoke] web"

# 6. POST /api/v1/sources：创建带密码的 WebDAV Source。
#    不做真实连接测试（协议行为由 Go httptest 覆盖）；endpoint 仅需合法。
curl --fail --silent \
	-H 'Content-Type: application/json' \
	--data '{"name":"Smoke Source","type":"webdav","endpoint":"http://127.0.0.1:1/dav","username":"smoke","password":"S3cret-Smoke"}' \
	"${base_url}/api/v1/sources" >source.json
grep -F '"password_set":true' source.json >/dev/null || {
	echo "Error: source creation failed" >&2
	cat source.json >&2
	exit 1
}
if grep -F 'S3cret-Smoke' source.json >/dev/null; then
	echo "Error: create response leaks password" >&2
	exit 1
fi
if grep -F '"password"' source.json >/dev/null; then
	echo "Error: create response contains password field" >&2
	exit 1
fi
source_id=$(grep -o '"id":"src_[a-f0-9]*"' source.json | head -1 | cut -d '"' -f4)
if [[ -z "${source_id}" ]]; then
	echo "Error: no source id in response: $(cat source.json)" >&2
	exit 1
fi
echo "[smoke] source created: ${source_id}"

# 7. GET 列表可见该 Source 且不含密码；数据库文件已创建。
curl --fail --silent "${base_url}/api/v1/sources" >sources.json
grep -F "${source_id}" sources.json >/dev/null
if grep -F 'S3cret-Smoke' sources.json >/dev/null; then
	echo "Error: list response leaks password" >&2
	exit 1
fi
if [[ ! -f "${datadir}/tinysync.db" ]]; then
	echo "Error: ${datadir}/tinysync.db was not created" >&2
	exit 1
fi
echo "[smoke] sources list ok, database file ok"

# 8. POST /api/v1/jobs：创建引用该 Source 的 Copy Job。
#    local_root 用相对路径（cwd 已是 smoke_root），由服务端归一为绝对路径，
#    避免 Windows 下 JSON 内嵌 MSYS 路径的转歧义。
mkdir -p local
curl --fail --silent \
	-H 'Content-Type: application/json' \
	--data "{\"name\":\"Smoke Job\",\"source_id\":\"${source_id}\",\"remote_root\":\"/\",\"local_root\":\"local\",\"mode\":\"copy\",\"enabled\":true}" \
	"${base_url}/api/v1/jobs" >job.json
grep -F '"mode":"copy"' job.json >/dev/null || {
	echo "Error: job creation failed" >&2
	cat job.json >&2
	exit 1
}
job_id=$(grep -o '"id":"job_[a-f0-9]*"' job.json | head -1 | cut -d '"' -f4)
if [[ -z "${job_id}" ]]; then
	echo "Error: no job id in response: $(cat job.json)" >&2
	exit 1
fi
echo "[smoke] job created: ${job_id}"

# 9. POST run：远端不可达（127.0.0.1:1），运行应异步启动并收敛为 failed。
run_code=$(curl --fail --silent -o run.json -w '%{http_code}' -X POST \
	"${base_url}/api/v1/jobs/${job_id}/run")
if [[ "${run_code}" != "202" ]]; then
	echo "Error: run status ${run_code}, want 202" >&2
	cat run.json >&2
	exit 1
fi
job_failed=0
for _ in {1..30}; do
	curl --fail --silent "${base_url}/api/v1/jobs/${job_id}/status" >status.json
	if grep -F '"state":"failed"' status.json >/dev/null; then
		job_failed=1
		break
	fi
	sleep 1
done
if [[ "${job_failed}" != "1" ]]; then
	echo "Error: job run did not converge to failed against unreachable remote" >&2
	cat status.json >&2
	exit 1
fi
echo "[smoke] job run started (202) and converged to failed"

# 10. 关闭进程并以同一 datadir 重启。
stop_server
start_server serve2.log
wait_ready serve2.log
echo "[smoke] server restarted"

# 11. Source 跨重启持久化：字段与密码标志保持。
curl --fail --silent "${base_url}/api/v1/sources/${source_id}" >source2.json
grep -F '"name":"Smoke Source"' source2.json >/dev/null
grep -F '"password_set":true' source2.json >/dev/null
if grep -F 'S3cret-Smoke' source2.json >/dev/null; then
	echo "Error: restarted response leaks password" >&2
	exit 1
fi
echo "[smoke] source persisted across restart"

# 12. Job 跨重启持久化：配置保留，运行状态回到 idle（运行记录只存内存）。
curl --fail --silent "${base_url}/api/v1/jobs/${job_id}" >job2.json
grep -F '"name":"Smoke Job"' job2.json >/dev/null
grep -F '"mode":"copy"' job2.json >/dev/null
grep -F "\"source_id\":\"${source_id}\"" job2.json >/dev/null
curl --fail --silent "${base_url}/api/v1/jobs/${job_id}/status" >status2.json
if ! grep -F '"state":"idle"' status2.json >/dev/null; then
	echo "Error: job run state after restart should be idle" >&2
	cat status2.json >&2
	exit 1
fi
echo "[smoke] job persisted across restart, run state reset to idle"

# 13. SIGTERM 优雅退出：POSIX 平台发 SIGTERM 并 wait 校验退出码（非 0 即失败）；
# Windows 的 Git Bash kill 对原生进程不可靠，保留 server_pid 交给 cleanup
# 的 taskkill 强制清理。
if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
	echo "[smoke] shutdown deferred to cleanup taskkill (Windows)"
else
	kill -TERM "${server_pid}" >/dev/null 2>&1
	wait "${server_pid}" >/dev/null 2>&1
	server_pid=""
	echo "[smoke] graceful shutdown"
fi
