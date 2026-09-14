#!/usr/bin/env bash

# 跨平台原生 Smoke 测试：先验证实际 binary 的版本输出，再验证核心行为。
# 在 Linux / macOS / Windows（Git Bash）runner 上针对原生构建的二进制执行：
#
#	1. tinysync --version 与预期版本一致
#	2. tinysync version 与预期版本一致
#	3. 启动 tinysync serve（临时 datadir、独立端口）
#	4. GET /api/v1/health 轮询至就绪且 "status":"ok"
#	5. GET / 返回 WebUI（<title>TinySync</title>）
#	6. SIGTERM 优雅退出（Windows 为强制清理）
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
server_pid=""

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

# 临时产物（serve.log / health.json / index.html）统一落在 smoke_root，
# 不污染调用方目录；binary 已解析为绝对路径，cd 安全。
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

# 3. 启动 serve：临时 datadir，避免污染工作目录。
port=${TINYSYNC_SMOKE_PORT:-19466}
"${binary}" serve --datadir "${smoke_root}/data" --port "${port}" >serve.log 2>&1 &
server_pid=$!

# 4. 轮询 /api/v1/health 直至就绪。
ready=false
for _ in {1..30}; do
	if curl --fail --silent "http://127.0.0.1:${port}/api/v1/health" >health.json; then
		ready=true
		break
	fi
	sleep 1
done
if [[ "${ready}" != "true" ]]; then
	echo "Error: server did not become ready" >&2
	cat serve.log >&2
	exit 1
fi
grep -F '"status":"ok"' health.json >/dev/null
echo "[smoke] health ok"

# 5. WebUI 首页可访问且标题正确。
curl --fail --silent "http://127.0.0.1:${port}/" >index.html
grep -F '<title>TinySync</title>' index.html >/dev/null
echo "[smoke] web"

# 6. SIGTERM 优雅退出：POSIX 平台发 SIGTERM 并 wait 校验退出码（非 0 即失败）；
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
