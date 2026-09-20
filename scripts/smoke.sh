#!/usr/bin/env bash

# 跨平台原生 Smoke 测试：先验证实际 binary 的版本输出，再验证核心行为。
# 在 Linux / macOS / Windows（Git Bash）runner 上针对原生构建的二进制执行：
#
#	1. tinysync --version 与预期版本一致
#	2. tinysync version 与预期版本一致
#	3. 全新 datadir 首次 serve 自动 bootstrap：初始密码仅在当前终端
#	   stderr 打印一次、default-deny 边界保持（匿名管理 API 一律 401）、
#	   初始密码可登录
#	4. tinysync auth set-password --password-stdin 完成 admin bootstrap
#	5. 启动 tinysync serve（同一 datadir、独立端口）
#	6. GET /api/v1/health 公开可读，轮询至就绪且 "status":"ok"
#	7. default-deny：匿名 GET /api/v1/sources 一律 401
#	8. 登录：错误密码统一 401；正确密码 200 + Set-Cookie 保存
#	   cookie.jar，后续全部管理 API 携带会话
#	9. GET / 返回 WebUI（<title>TinySync</title>）
#	10. POST /api/v1/sources 按 config/credentials 契约创建 WebDAV
#	    Source，响应不含密码明文、credential_state 正确
#	10b. 创建 S3 Source（config 单选组 + secret_key），secret 不回显
#	10c. 创建 SFTP Source（含 auth_method 与 host key fingerprint）
#	11. GET /api/v1/sources 列表可见三个 Source 且不含 secret，
#	    tinysync.db 已创建
#	12. POST /api/v1/jobs 创建引用 WebDAV Source 的 Copy Job
#	13. POST /api/v1/jobs/:id/run 异步启动（202），对不可达远端收敛为 failed
#	14. 关闭进程并以同一 datadir 重启
#	15. 原 session 跨重启仍有效（cookie.jar 直接复用）；三协议 Source
#	    跨重启持久化：config / credential_state 保持，secret 不回显
#	16. Job 配置跨重启持久化，运行历史持久化：状态保持 failed，
#	    run_id / finished_at / error 与重启前一致
#	17. GET /api/v1/runs/:run_id 确认运行摘要 API 可查询该持久化运行
#	17b. MCP 存活检查：anonymous /mcp 401、cookie-only 401、
#	    Bearer API Token 的 initialize 得到 2026-07-28 协议 result
#	18. SIGTERM 优雅退出（Windows 为强制清理）
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

# smoke 密码满足最小策略（12+ 字符），仅存在于本次 smoke 临时目录。
smoke_password='Smoke-Admin-Password-123'

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

# wait_ready 轮询公开的 /api/v1/health 直至就绪；失败时输出 serve 日志。
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

# 3. 全新 datadir：首次 serve 自动 bootstrap——生成高熵初始密码仅在
#    当前终端 stderr 打印一次，服务正常就绪；认证边界保持 default-deny：
#    匿名管理 API 一律 401，初始密码可登录。
"${binary}" serve --datadir "${datadir}" --port "${port}" >serve.log 2>&1 &
server_pid=$!
wait_ready serve.log
initial_password=$(sed -n 's/.*初始管理员密码：\([A-Za-z0-9_-]*\).*/\1/p' serve.log)
if [[ -z "${initial_password}" ]]; then
	echo "Error: first serve did not print initial admin password" >&2
	cat serve.log >&2
	exit 1
fi
anon_code=$(curl --silent -o anon_bootstrap.json -w '%{http_code}' "${base_url}/api/v1/sources")
if [[ "${anon_code}" != "401" ]]; then
	echo "Error: anonymous GET sources status ${anon_code}, want 401 after auto bootstrap" >&2
	cat anon_bootstrap.json >&2
	exit 1
fi
boot_code=$(curl --silent -o boot_login.json -w '%{http_code}' \
	-H 'Content-Type: application/json' \
	--data "{\"password\":\"${initial_password}\"}" \
	"${base_url}/api/v1/auth/login")
if [[ "${boot_code}" != "200" ]]; then
	echo "Error: initial password login status ${boot_code}, want 200" >&2
	cat boot_login.json >&2
	exit 1
fi
echo "[smoke] first serve auto-bootstrapped, default-deny enforced, initial password accepted"

# 4. auth set-password --password-stdin：把自动生成的初始密码轮换为
#    smoke 已知密码（替换密码同时废弃全部 Web Session，含第 3 步用
#    初始密码建立的会话）。
printf '%s\n' "${smoke_password}" |
	"${binary}" auth set-password --datadir "${datadir}" --password-stdin
echo "[smoke] admin password rotated from auto bootstrap"

# 5-6. serve 已于第 3 步启动，轮询确认就绪；health 公开可读（无需凭据）。
wait_ready serve.log
echo "[smoke] health ok (public)"

# 5b. datadir 单实例约束：同一 datadir 的第二个 serve 必须 fail-fast
#     退出（exit != 0）并说明 datadir 已被占用；已运行实例保持健康。
"${binary}" serve --datadir "${datadir}" --port $((port + 1)) >second_instance.log 2>&1 &
second_pid=$!
second_exited=0
for _ in {1..30}; do
	if ! kill -0 "${second_pid}" >/dev/null 2>&1; then
		second_exited=1
		break
	fi
	sleep 1
done
if [[ "${second_exited}" != "1" ]]; then
	if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
		taskkill.exe //IM "$(basename "${binary}")" //T //F >/dev/null 2>&1 || true
	else
		kill -TERM "${second_pid}" >/dev/null 2>&1 || true
	fi
	echo "Error: second serve instance on same datadir did not fail fast" >&2
	cat second_instance.log >&2
	exit 1
fi
second_rc=0
wait "${second_pid}" >/dev/null 2>&1 || second_rc=$?
if [[ "${second_rc}" -eq 0 ]]; then
	echo "Error: second serve instance exited 0, want failure" >&2
	cat second_instance.log >&2
	exit 1
fi
if ! grep -F 'owned by another tinysync process' second_instance.log >/dev/null; then
	echo "Error: second instance did not report datadir ownership conflict" >&2
	cat second_instance.log >&2
	exit 1
fi
if ! curl --fail --silent "${base_url}/api/v1/health" | grep -F '"status":"ok"' >/dev/null; then
	echo "Error: first instance unhealthy after second instance rejection" >&2
	exit 1
fi
echo "[smoke] datadir single-instance enforced (second instance exit ${second_rc})"

# 7. default-deny：匿名访问管理 API 一律 401。
anon_code=$(curl --silent -o anon.json -w '%{http_code}' "${base_url}/api/v1/sources")
if [[ "${anon_code}" != "401" ]]; then
	echo "Error: anonymous GET sources status ${anon_code}, want 401 (default-deny)" >&2
	cat anon.json >&2
	exit 1
fi
echo "[smoke] default-deny enforced"

# 8. 登录：错误密码统一 401；正确密码建立 Web Session 并保存
#    cookie.jar，后续管理 API 全部携带。
login_code=$(curl --silent -o login_bad.json -w '%{http_code}' \
	-H 'Content-Type: application/json' \
	--data '{"password":"totally-wrong-pass"}' \
	"${base_url}/api/v1/auth/login")
if [[ "${login_code}" != "401" ]]; then
	echo "Error: wrong password login status ${login_code}, want 401" >&2
	exit 1
fi
curl --fail --silent -c cookie.jar \
	-H 'Content-Type: application/json' \
	--data "{\"password\":\"${smoke_password}\"}" \
	"${base_url}/api/v1/auth/login" >login.json
grep -F '"expires_at"' login.json >/dev/null || {
	echo "Error: login response missing expires_at: $(cat login.json)" >&2
	exit 1
}
if grep -F "${smoke_password}" login.json >/dev/null; then
	echo "Error: login response echoes password" >&2
	exit 1
fi
echo "[smoke] login ok, session cookie saved"

# 9. WebUI 首页可访问且标题正确（静态资源公开，认证由前端路由承担）。
curl --fail --silent "${base_url}/" >index.html
grep -F '<title>TinySync</title>' index.html >/dev/null
echo "[smoke] web"

# 10. POST /api/v1/sources：按 config / credentials 契约创建带密码的
#     WebDAV Source。不做真实连接测试（协议行为由 Go httptest 覆盖）。
curl --fail --silent -b cookie.jar \
	-H 'Content-Type: application/json' \
	--data '{"name":"Smoke WebDAV","type":"webdav","config":{"endpoint":"http://127.0.0.1:1/dav","username":"smoke"},"credentials":{"password":"S3cret-Smoke"}}' \
	"${base_url}/api/v1/sources" >source.json
grep -F '"webdav":{"password_set":true}' source.json >/dev/null || {
	echo "Error: webdav source creation failed" >&2
	cat source.json >&2
	exit 1
}
if grep -F 'S3cret-Smoke' source.json >/dev/null; then
	echo "Error: create response leaks password" >&2
	exit 1
fi
if grep -F '"credentials"' source.json >/dev/null; then
	echo "Error: create response contains credentials field" >&2
	exit 1
fi
source_id=$(grep -o '"id":"src_[a-f0-9]*"' source.json | head -1 | cut -d '"' -f4)
if [[ -z "${source_id}" ]]; then
	echo "Error: no source id in response: $(cat source.json)" >&2
	exit 1
fi
echo "[smoke] webdav source created: ${source_id}"

# 10b. S3 Source：config 单选组 + secret_key；secret 不回显。
curl --fail --silent -b cookie.jar \
	-H 'Content-Type: application/json' \
	--data '{"name":"Smoke S3","type":"s3","config":{"endpoint":"http://127.0.0.1:1","region":"us-east-1","bucket":"smoke-bucket","path_style":true,"access_key":"AKID-SMOKE"},"credentials":{"secret_key":"S3cret-Key"}}' \
	"${base_url}/api/v1/sources" >source_s3.json
grep -F '"s3":{"secret_key_set":true}' source_s3.json >/dev/null || {
	echo "Error: s3 source creation failed" >&2
	cat source_s3.json >&2
	exit 1
}
if grep -F 'S3cret-Key' source_s3.json >/dev/null; then
	echo "Error: s3 create response leaks secret key" >&2
	exit 1
fi
s3_id=$(grep -o '"id":"src_[a-f0-9]*"' source_s3.json | head -1 | cut -d '"' -f4)
echo "[smoke] s3 source created: ${s3_id}"

# 10c. SFTP Source：显式 auth_method + SHA256 host key fingerprint。
curl --fail --silent -b cookie.jar \
	-H 'Content-Type: application/json' \
	--data '{"name":"Smoke SFTP","type":"sftp","config":{"host":"127.0.0.1","port":22,"username":"smoke","remote_root":"/srv/smoke","auth_method":"password","host_key_fingerprint":"SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k"},"credentials":{"password":"S3cret-FTP"}}' \
	"${base_url}/api/v1/sources" >source_sftp.json
grep -F '"sftp":{"password_set":true,"private_key_set":false,"private_key_passphrase_set":false}' source_sftp.json >/dev/null || {
	echo "Error: sftp source creation failed" >&2
	cat source_sftp.json >&2
	exit 1
}
if grep -F 'S3cret-FTP' source_sftp.json >/dev/null; then
	echo "Error: sftp create response leaks password" >&2
	exit 1
fi
sftp_id=$(grep -o '"id":"src_[a-f0-9]*"' source_sftp.json | head -1 | cut -d '"' -f4)
echo "[smoke] sftp source created: ${sftp_id}"

# 11. GET 列表可见三个 Source 且不含 secret；数据库文件已创建。
curl --fail --silent -b cookie.jar "${base_url}/api/v1/sources" >sources.json
grep -F "${source_id}" sources.json >/dev/null
grep -F "${s3_id}" sources.json >/dev/null
grep -F "${sftp_id}" sources.json >/dev/null
for secret in 'S3cret-Smoke' 'S3cret-Key' 'S3cret-FTP'; do
	if grep -F "${secret}" sources.json >/dev/null; then
		echo "Error: list response leaks secret ${secret}" >&2
		exit 1
	fi
done
if [[ ! -f "${datadir}/tinysync.db" ]]; then
	echo "Error: ${datadir}/tinysync.db was not created" >&2
	exit 1
fi
echo "[smoke] sources list ok (3 protocols), database file ok"

# 12. POST /api/v1/jobs：创建引用该 Source 的 Copy Job。
#     local_root 用相对路径（cwd 已是 smoke_root），由服务端归一为绝对路径，
#     避免 Windows 下 JSON 内嵌 MSYS 路径的转歧义。
mkdir -p local
curl --fail --silent -b cookie.jar \
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

# 13. POST run：远端不可达（127.0.0.1:1），运行应异步启动并收敛为 failed。
run_code=$(curl --fail --silent -b cookie.jar -o run.json -w '%{http_code}' -X POST \
	"${base_url}/api/v1/jobs/${job_id}/run")
if [[ "${run_code}" != "202" ]]; then
	echo "Error: run status ${run_code}, want 202" >&2
	cat run.json >&2
	exit 1
fi
run_id=$(grep -o '"run_id":"run_[a-f0-9]*"' run.json | head -1 | cut -d '"' -f4)
if [[ -z "${run_id}" ]]; then
	echo "Error: no run id in response: $(cat run.json)" >&2
	exit 1
fi
job_failed=0
for _ in {1..30}; do
	curl --fail --silent -b cookie.jar "${base_url}/api/v1/jobs/${job_id}/status" >status.json
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
echo "[smoke] job run started (202) and converged to failed: ${run_id}"

# 14. 关闭进程并以同一 datadir 重启。
stop_server
start_server serve2.log
wait_ready serve2.log
echo "[smoke] server restarted"

# 15. 原 session 跨重启仍有效（cookie.jar 直接复用即可访问管理 API）；
#     三协议 Source 持久化：config 与 credential_state 保持，secret 不回显。
curl --fail --silent -b cookie.jar "${base_url}/api/v1/sources/${source_id}" >source2.json
grep -F '"name":"Smoke WebDAV"' source2.json >/dev/null
grep -F '"webdav":{"password_set":true}' source2.json >/dev/null
grep -F '"endpoint":"http://127.0.0.1:1/dav"' source2.json >/dev/null
curl --fail --silent -b cookie.jar "${base_url}/api/v1/sources/${s3_id}" >source2_s3.json
grep -F '"s3":{"secret_key_set":true}' source2_s3.json >/dev/null
grep -F '"bucket":"smoke-bucket"' source2_s3.json >/dev/null
curl --fail --silent -b cookie.jar "${base_url}/api/v1/sources/${sftp_id}" >source2_sftp.json
grep -F '"sftp":{"password_set":true,"private_key_set":false,"private_key_passphrase_set":false}' source2_sftp.json >/dev/null
grep -F '"remote_root":"/srv/smoke"' source2_sftp.json >/dev/null
for secret in 'S3cret-Smoke' 'S3cret-Key' 'S3cret-FTP'; do
	for f in source2.json source2_s3.json source2_sftp.json; do
		if grep -F "${secret}" "${f}" >/dev/null; then
			echo "Error: restarted response ${f} leaks secret ${secret}" >&2
			exit 1
		fi
	done
done
echo "[smoke] session survives restart, three-protocol sources persisted"

# 16. Job 跨重启持久化：配置保留；运行历史持久化——状态保持 failed，
#     run_id / finished_at / error 与重启前一致（历史不再只存内存）。
curl --fail --silent -b cookie.jar "${base_url}/api/v1/jobs/${job_id}" >job2.json
grep -F '"name":"Smoke Job"' job2.json >/dev/null
grep -F '"mode":"copy"' job2.json >/dev/null
grep -F "\"source_id\":\"${source_id}\"" job2.json >/dev/null
curl --fail --silent -b cookie.jar "${base_url}/api/v1/jobs/${job_id}/status" >status2.json
if ! grep -F '"state":"failed"' status2.json >/dev/null; then
	echo "Error: job run state after restart should stay failed (persistent history)" >&2
	cat status2.json >&2
	exit 1
fi
if ! grep -F "\"run_id\":\"${run_id}\"" status2.json >/dev/null; then
	echo "Error: status after restart lost run id ${run_id}" >&2
	exit 1
fi
if ! grep -F '"finished_at":"' status2.json >/dev/null; then
	echo "Error: persisted run after restart has no finished_at" >&2
	exit 1
fi
if ! grep -F '"error":"' status2.json >/dev/null; then
	echo "Error: persisted run after restart has no error" >&2
	exit 1
fi
echo "[smoke] job persisted across restart, failed run history intact"

# 17. GET /api/v1/runs/:run_id：运行摘要 API 可查询该持久化运行。
run_code=$(curl --fail --silent -b cookie.jar -o run2.json -w '%{http_code}' \
	"${base_url}/api/v1/runs/${run_id}")
if [[ "${run_code}" != "200" ]]; then
	echo "Error: GET run after restart status ${run_code}, want 200" >&2
	exit 1
fi
grep -F "\"id\":\"${run_id}\"" run2.json >/dev/null || {
	echo "Error: run response has wrong id: $(cat run2.json)" >&2
	exit 1
}
grep -F '"status":"failed"' run2.json >/dev/null || {
	echo "Error: run response status is not failed: $(cat run2.json)" >&2
	exit 1
}
grep -F "\"job_id\":\"${job_id}\"" run2.json >/dev/null || {
	echo "Error: run response has wrong job id: $(cat run2.json)" >&2
	exit 1
}
echo "[smoke] persistent run queryable via runs API"

# 17b. MCP 存活检查：/mcp 只认 Bearer API Token；匿名与 Web Session
#      cookie 一律 401。2026-07-28 为 sessionless 协议：直接调用
#      tools/list 需携带 MCP-Protocol-Version 头并得到 result，旧版
#      本头被拒绝（协议细节由 Go E2E 覆盖，这里锁定端点在真实二进制
#      上活着且带认证边界）。
mcp_init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}'
mcp_code=$(curl --silent -o mcp_anon.json -w '%{http_code}' \
	-H 'Content-Type: application/json' \
	-H 'Accept: application/json, text/event-stream' \
	--data "${mcp_init}" \
	"${base_url}/mcp")
if [[ "${mcp_code}" != "401" ]]; then
	echo "Error: anonymous POST /mcp status ${mcp_code}, want 401" >&2
	cat mcp_anon.json >&2
	exit 1
fi
mcp_code=$(curl --silent -o /dev/null -w '%{http_code}' -b cookie.jar \
	-H 'Content-Type: application/json' \
	-H 'Accept: application/json, text/event-stream' \
	--data "${mcp_init}" \
	"${base_url}/mcp")
if [[ "${mcp_code}" != "401" ]]; then
	echo "Error: cookie-only POST /mcp status ${mcp_code}, want 401 (MCP never accepts web session)" >&2
	exit 1
fi
curl --fail --silent -b cookie.jar \
	-H 'Content-Type: application/json' \
	--data '{"name":"smoke-mcp","scopes":["read"]}' \
	"${base_url}/api/v1/api-tokens" >token.json
mcp_token=$(grep -o '"raw_token":"[^"]*"' token.json | head -1 | cut -d '"' -f4)
if [[ -z "${mcp_token}" ]]; then
	echo "Error: no raw_token in api-tokens response: $(cat token.json)" >&2
	exit 1
fi
# 2026-07-28 sessionless 请求形态：MCP-Protocol-Version / Mcp-Method
# 头 + params._meta 的 per-request triple（protocolVersion +
# clientCapabilities）。
mcp_tools='{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'
mcp_code=$(curl --silent -o mcp_tools.json -w '%{http_code}' \
	-H 'Content-Type: application/json' \
	-H 'Accept: application/json, text/event-stream' \
	-H 'MCP-Protocol-Version: 2026-07-28' \
	-H 'Mcp-Method: tools/list' \
	-H "Authorization: Bearer ${mcp_token}" \
	--data "${mcp_tools}" \
	"${base_url}/mcp")
if [[ "${mcp_code}" != "200" ]]; then
	echo "Error: bearer tools/list status ${mcp_code}, want 200" >&2
	cat mcp_tools.json >&2
	exit 1
fi
if ! grep -F '"result"' mcp_tools.json >/dev/null; then
	echo "Error: tools/list response missing JSON-RPC result: $(cat mcp_tools.json)" >&2
	exit 1
fi
if ! grep -F 'run_sync' mcp_tools.json >/dev/null; then
	echo "Error: tools/list missing run_sync: $(cat mcp_tools.json)" >&2
	exit 1
fi
mcp_code=$(curl --silent -o /dev/null -w '%{http_code}' \
	-H 'Content-Type: application/json' \
	-H 'Accept: application/json, text/event-stream' \
	-H 'MCP-Protocol-Version: 2025-06-18' \
	-H "Authorization: Bearer ${mcp_token}" \
	--data "${mcp_tools}" \
	"${base_url}/mcp")
if [[ "${mcp_code}" != "400" ]]; then
	echo "Error: old protocol version header status ${mcp_code}, want 400" >&2
	exit 1
fi
mcp_code=$(curl --silent -o mcp_init.json -w '%{http_code}' \
	-H 'Content-Type: application/json' \
	-H 'Accept: application/json, text/event-stream' \
	-H "Authorization: Bearer ${mcp_token}" \
	--data "${mcp_init}" \
	"${base_url}/mcp")
if [[ "${mcp_code}" != "200" ]] || ! grep -F '"result"' mcp_init.json >/dev/null; then
	echo "Error: bearer initialize status ${mcp_code} (want 200 with result)" >&2
	cat mcp_init.json >&2
	exit 1
fi
if grep -F "${mcp_token}" mcp_init.json >/dev/null; then
	echo "Error: MCP response echoes raw token" >&2
	exit 1
fi
echo "[smoke] mcp endpoint authenticated and alive (2026-07-28)"

# 18. SIGTERM 优雅退出：POSIX 平台发 SIGTERM 并 wait 校验退出码（非 0 即失败）；
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
