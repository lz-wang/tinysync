// 后端 REST API 客户端：与 Go 侧 /api/v1 契约一一对应。

// UnauthorizedError 标识会话失效（401）：AuthProvider 监听后清空
// 会话状态并跳转登录页。
export class UnauthorizedError extends Error {
    constructor() {
        super('unauthorized')
        this.name = 'UnauthorizedError'
    }
}

// unauthorizedEventName 是 401 时派发的全局事件名：统一会话过期
// 语义，页面层无需各自捕获。
export const unauthorizedEventName = 'tinysync:unauthorized'

// HealthResponse 对应 GET /api/v1/health。
export interface HealthResponse {
    status: string
}

// VersionResponse 对应 GET /api/v1/version。
export interface VersionResponse {
    version: string
}

// SourceType 是 Source 支持的协议类型；创建后不可变。
export type SourceType = 'webdav' | 's3' | 'sftp' | 'smb' | 'github_release' | 'local' | 'http'

// LocalConfig 使用运行主机的 native 文件系统路径，无凭据字段。
export interface LocalConfig {
    root: string
}

// WebDAVConfig 是 WebDAV 的非敏感配置。
export interface WebDAVConfig {
    endpoint: string
    remote_root?: string
    username: string
}

// S3Config 是 S3 的非敏感配置；endpoint 缺省表示 AWS 默认 endpoint。
export interface S3Config {
    endpoint?: string
    region: string
    bucket: string
    prefix?: string
    path_style?: boolean
    access_key: string
}

// SFTPAuthMethod 是 SFTP 认证方式，显式声明不推断。
export type SFTPAuthMethod = 'password' | 'private_key'

// SFTPConfig 是 SFTP 的非敏感配置。
export interface SFTPConfig {
    host: string
    port?: number
    username: string
    remote_root: string
    auth_method: SFTPAuthMethod
    host_key_fingerprint: string
    // credential_id 非空时为凭据引用（与内联私钥互斥，auth_method
    // 必须为 private_key）。
    credential_id?: string
}

// SMBSigningPolicy 是 SMB 消息签名策略：required 要求服务器启用
// 签名（默认，推荐）；auto 跟随服务器协商。
export type SMBSigningPolicy = 'required' | 'auto'

// SMBConfig 是 SMB 的非敏感配置。host 是裸主机名 / IP（不带 smb://
// 前缀与 share），remote_root 是 share 内的 POSIX 风格绝对路径。
export interface SMBConfig {
    host: string
    port?: number
    share: string
    remote_root: string
    username: string
    domain?: string
    signing: SMBSigningPolicy
}

// GitHubReleasePolicy 是 GitHub Release 的版本选择策略。
export type GitHubReleasePolicy = 'latest' | 'tag' | 'recent' | 'all'

// GitHubSHA256Mode 是 Asset 的 SHA-256 校验策略：if_available 在
// GitHub 提供 digest 时强制校验（默认），required 要求全部 Asset
// 均带有效 digest。
export type GitHubSHA256Mode = 'if_available' | 'required'

// GitHubReleaseConfig 是 GitHub Release 的非敏感配置。
export interface GitHubReleaseConfig {
    repository: string
    release_policy: GitHubReleasePolicy
    tag?: string
    recent_count?: number
    include_prereleases?: boolean
    verify_sha256: GitHubSHA256Mode
}

// HTTPListingMode 是 HTTP 文件源的目录索引表现形式（ADR 0009）：
// nginx / Caddy / miniserve 只是 profile 差异，不是 Source 类型。
export type HTTPListingMode = 'auto' | 'nginx' | 'caddy' | 'miniserve'

// HTTPAuthMethod 是 HTTP 文件源的认证方式，显式声明不推断。
export type HTTPAuthMethod = 'none' | 'basic' | 'bearer'

// HTTPConfig 是 HTTP 文件源的非敏感配置。base_url 即源的 "/"（无
// 独立 remote_root）；caddy_file_limit 与服务器 browse.file_limit 一致，
// 达到即整轮扫描失败（fail-closed）。
export interface HTTPConfig {
    base_url: string
    listing_mode: HTTPListingMode
    auth_method: HTTPAuthMethod
    username?: string
    caddy_file_limit?: number
}

// SourceConfig 是按 type 判别的协议配置（请求与响应均为扁平单选对象）。
export type SourceConfig =
    | WebDAVConfig
    | S3Config
    | SFTPConfig
    | SMBConfig
    | GitHubReleaseConfig
    | LocalConfig
    | HTTPConfig

// CredentialState 回显各 secret 是否设置；任何 secret 不回显明文。
export interface CredentialState {
    webdav?: { password_set: boolean }
    s3?: { secret_key_set: boolean }
    sftp?: {
        password_set: boolean
        private_key_set: boolean
        private_key_passphrase_set: boolean
    }
    smb?: { password_set: boolean }
    github_release?: { token_set: boolean }
    http?: { password_set: boolean; bearer_token_set: boolean }
}

// SourceResponse 是 Source 的 API 表示；绝不包含 secret 明文，
// 凭据状态只以 credential_state 布尔暴露。
export interface SourceResponse {
    id: string
    name: string
    type: SourceType
    config: SourceConfig
    credential_state: CredentialState
    enabled: boolean
    created_at: string
    updated_at: string
}

// SourcesListResponse 对应 GET /api/v1/sources 的包装对象。
export interface SourcesListResponse {
    sources: SourceResponse[]
}

// TestSourceResponse 对应 POST /api/v1/sources/:id/test：
// 连接失败也是成功完成的测试操作，以 ok=false 表达。
export interface TestSourceResponse {
    ok: boolean
    latency_ms: number
    error?: string
}

// 各协议 secret 请求体（创建：值语义；更新：三态，undefined 保留、
// 空串清除、非空替换）。
export interface WebDAVCredentials {
    password?: string
}

export interface S3Credentials {
    secret_key?: string
}

export interface SFTPCredentials {
    password?: string
    private_key?: string
    private_key_passphrase?: string
}

export interface SMBCredentials {
    password?: string
}

export interface GitHubReleaseCredentials {
    token?: string
}

export interface HTTPCredentials {
    password?: string
    bearer_token?: string
}

// SourceCredentials 按 type 单选的 secret 组。
export type SourceCredentials =
    | WebDAVCredentials
    | S3Credentials
    | SFTPCredentials
    | SMBCredentials
    | GitHubReleaseCredentials
    | HTTPCredentials

// CreateSourceInput 对应 POST /api/v1/sources 请求体。
export interface CreateSourceInput {
    name: string
    type: SourceType
    config: SourceConfig
    credentials?: SourceCredentials
    enabled?: boolean
}

// UpdateSourceInput 对应 PATCH 请求体：undefined 字段保留现有值；
// config 提供时整个协议 config 替换；credentials 组内 secret 三态。
export interface UpdateSourceInput {
    name?: string
    config?: SourceConfig
    credentials?: SourceCredentials
    enabled?: boolean
}

// apiFetch 是全部 API 访问的统一入口：same-origin cookie 凭据、
// JSON 编解码、204 响应、后端错误提取与 401 → UnauthorizedError
// 语义（同时派发全局事件供 AuthProvider 清理会话状态）。
async function apiFetch<T>(method: string, path: string, body?: unknown): Promise<T> {
    const response = await fetch(path, {
        method,
        credentials: 'same-origin',
        headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
    })
    if (response.status === 401) {
        window.dispatchEvent(new CustomEvent(unauthorizedEventName))
        throw new UnauthorizedError()
    }
    if (!response.ok) {
        throw new Error(await errorMessage(method, path, response))
    }
    if (response.status === 204) {
        return undefined as T
    }
    return (await response.json()) as T
}

async function getJSON<T>(path: string): Promise<T> {
    return apiFetch<T>('GET', path)
}

// requestJSON 处理带请求体的方法。
async function requestJSON<T>(method: string, path: string, body?: unknown): Promise<T> {
    return apiFetch<T>(method, path, body)
}

// ===== 认证（v0.7）=====

// SessionResponse 对应 GET /api/v1/auth/session。
export interface SessionResponse {
    authenticated: boolean
    subject: string
    expires_at: string
}

// LoginResponse 对应 POST /api/v1/auth/login：会话经 Set-Cookie
// 建立，响应体只有过期时刻。
export interface LoginResponse {
    expires_at: string
}

// login 登录并建立 Web Session cookie；凭据错误以 Error 抛出
//（401 已由 apiFetch 转换语义，登录页展示统一文案）。
export function login(password: string): Promise<LoginResponse> {
    return apiFetch<LoginResponse>('POST', '/api/v1/auth/login', { password })
}

// fetchSession 查询当前会话；未登录时抛出 UnauthorizedError。
export function fetchSession(): Promise<SessionResponse> {
    return apiFetch<SessionResponse>('GET', '/api/v1/auth/session')
}

// logout 登出并清除会话 cookie。401 视为已登出（幂等成功）：改密
// 成功后服务端同事务废弃全部会话，随后的登出请求必然 401——那是
// 改密成功的佐证而非失败，不能让调用方（如改密流程）误报错误。
export async function logout(): Promise<void> {
    try {
        await apiFetch<void>('POST', '/api/v1/auth/logout')
    } catch (reason) {
        if (!(reason instanceof UnauthorizedError)) {
            throw reason
        }
    }
}

export interface ProfileResponse {
    subject: string
    avatar: string
}

export function fetchProfile(): Promise<ProfileResponse> {
    return apiFetch<ProfileResponse>('GET', '/api/v1/auth/profile')
}

export function updateProfile(input: {
    avatar?: string
    current_password?: string
    new_password?: string
}): Promise<ProfileResponse> {
    return requestJSON<ProfileResponse>('PATCH', '/api/v1/auth/profile', input)
}

// errorMessage 提取后端 {"error": "..."} 中的描述，失败时回退状态码。
async function errorMessage(method: string, path: string, response: Response): Promise<string> {
    try {
        const payload = (await response.json()) as { error?: string }
        if (payload.error) {
            return payload.error
        }
    } catch {
        // 错误体不是 JSON 时回退状态码。
    }
    return `${method} ${path}: ${response.status}`
}

export function fetchHealth(): Promise<HealthResponse> {
    return getJSON<HealthResponse>('/api/v1/health')
}

export function fetchVersion(): Promise<VersionResponse> {
    return getJSON<VersionResponse>('/api/v1/version')
}

export async function listSources(): Promise<SourceResponse[]> {
    const data = await getJSON<SourcesListResponse>('/api/v1/sources')
    return data.sources
}

export function createSource(input: CreateSourceInput): Promise<SourceResponse> {
    return requestJSON<SourceResponse>('POST', '/api/v1/sources', input)
}

export function getSource(id: string): Promise<SourceResponse> {
    return requestJSON<SourceResponse>('GET', `/api/v1/sources/${id}`)
}

export function updateSource(id: string, input: UpdateSourceInput): Promise<SourceResponse> {
    return requestJSON<SourceResponse>('PATCH', `/api/v1/sources/${id}`, input)
}

export async function deleteSource(id: string): Promise<void> {
    await requestJSON<void>('DELETE', `/api/v1/sources/${id}`)
}

export function testSource(id: string): Promise<TestSourceResponse> {
    return requestJSON<TestSourceResponse>('POST', `/api/v1/sources/${id}/test`)
}

// SFTP 检查使用当前表单配置，不保存。编辑时未提供的 secret 沿用
// source_id 的已存值；空串显式清除，credential_id 使用提案引用。
export interface CheckSFTPSourceInput {
    source_id?: string
    config: SFTPConfig
    credentials?: SFTPCredentials
}

export function checkSFTPSource(input: CheckSFTPSourceInput): Promise<TestSourceResponse> {
    return requestJSON<TestSourceResponse>('POST', '/api/v1/sources/check', input)
}

// InspectSourceInput 对应 POST /api/v1/sources/inspect，语义按字段
// 组合解释（均不持久化）：source_id only 用已存配置 + 已存凭据；
// source_id + config 用提案配置 + 已存凭据（编辑态微调后预览，无需
// 重新索取 Token）；credentials.token 存在时临时覆盖已存 Token；
// config only（type=github_release）为创建态表单直接预览。
export interface InspectSourceInput {
    source_id?: string
    type?: SourceType
    config?: SourceConfig
    credentials?: SourceCredentials
}

// InspectedAsset 是预览结果的单个 Asset 概览。
export interface InspectedAsset {
    name: string
    size: number
    digest_available: boolean
}

// InspectedRelease 是预览结果的版本概览。
export interface InspectedRelease {
    tag: string
    name?: string
    prerelease: boolean
    published_at: string
    assets?: InspectedAsset[]
}

// InspectSourceResponse 对应 POST /api/v1/sources/inspect：
// 发现失败也是成功完成的预览操作，以 ok=false 表达。
export interface InspectSourceResponse {
    ok: boolean
    latency_ms: number
    error?: string
    repository?: string
    releases?: InspectedRelease[]
}

// inspectSource 创建前测试并预览 GitHub Release 发现结果。
export function inspectSource(input: InspectSourceInput): Promise<InspectSourceResponse> {
    return requestJSON<InspectSourceResponse>('POST', '/api/v1/sources/inspect', input)
}

// ===== 凭据库（ADR 0005）=====

// CredentialResponse 是凭据的 API 表示：公钥指纹与口令状态回显，
// secret 永不出现在任何响应中。
export interface CredentialResponse {
    id: string
    name: string
    type: 'ssh_key'
    fingerprint: string
    has_passphrase: boolean
    referenced_by: number
    created_at: string
    updated_at: string
}

// CredentialsListResponse 对应 GET /api/v1/credentials 的包装对象。
export interface CredentialsListResponse {
    credentials: CredentialResponse[]
}

// CredentialSecretInput 是凭据 secret 输入：私钥与其解密口令不可分割。
export interface CredentialSecretInput {
    private_key: string
    private_key_passphrase?: string
}

// CreateCredentialInput 对应 POST /api/v1/credentials 请求体。
export interface CreateCredentialInput {
    name: string
    type: 'ssh_key'
    secret: CredentialSecretInput
}

// UpdateCredentialInput 对应 PATCH 请求体：secret 缺省保留，出现即
// 整体替换（无三态——私钥与口令一体）。
export interface UpdateCredentialInput {
    name?: string
    secret?: CredentialSecretInput
}

export async function listCredentials(): Promise<CredentialResponse[]> {
    const data = await getJSON<CredentialsListResponse>('/api/v1/credentials')
    return data.credentials
}

export function createCredential(input: CreateCredentialInput): Promise<CredentialResponse> {
    return requestJSON<CredentialResponse>('POST', '/api/v1/credentials', input)
}

export function updateCredential(
    id: string,
    input: UpdateCredentialInput,
): Promise<CredentialResponse> {
    return requestJSON<CredentialResponse>('PATCH', `/api/v1/credentials/${id}`, input)
}

export async function deleteCredential(id: string): Promise<void> {
    await requestJSON<void>('DELETE', `/api/v1/credentials/${id}`)
}

// PromoteCredentialResponse 对应 POST /sources/:id/promote-credential：
// 提升后的源（引用态）与凭据摘要。
export interface PromoteCredentialResponse {
    source: SourceResponse
    credential: {
        id: string
        name: string
        fingerprint: string
        has_passphrase: boolean
    }
}

// promoteSourceCredential 把源已存的内联私钥转存为命名凭据并改写为
// 引用（私钥不经手前端）。
export function promoteSourceCredential(
    id: string,
    name: string,
): Promise<PromoteCredentialResponse> {
    return requestJSON<PromoteCredentialResponse>(
        'POST',
        `/api/v1/sources/${id}/promote-credential`,
        { name },
    )
}

// JobMode 是同步模式：Copy 只增不改删本地既有文件；
// Mirror 额外按 managed 授权删除远端已消失的本地文件。
export type JobMode = 'copy' | 'mirror'

// ScheduleType 是调度类型：manual 仅手动触发。
export type ScheduleType = 'manual' | 'once' | 'interval' | 'cron'

// ScheduleSpec 是调度配置的 discriminated object：按 type 消费互斥字段
//（once 用 at、interval 用 every、cron 用 expression + timezone）。
export interface ScheduleSpec {
    type: ScheduleType
    at?: string
    every?: string
    expression?: string
    timezone?: string
}

// JobKind 是任务类型：file / oci 可执行，git 为占位（创建被拒）。
export type JobKind = 'file' | 'oci' | 'git'

// FileJobConfig 是 kind='file' 任务的领域配置（config_json 原样）。
export interface FileJobConfig {
    source_id: string
    remote_root: string
    local_root: string
    mode: JobMode
    include: string[]
    exclude: string[]
}

// OciPlatform 是 oci 任务的平台过滤项（可选；未配置则同步全部平台）。
export interface OciPlatform {
    os?: string
    architecture?: string
    variant?: string
}

// OciJobConfig 是 kind='oci' 任务的领域配置（config_json 原样）：
// src_images 为展开后的 repo:tag 列表，dst_servers 引用 primary Registry
// 配置名，platforms 为平台过滤列表。
export interface OciJobConfig {
    src_images: string[]
    dst_servers: string[]
    platforms: OciPlatform[]
}

// JobConfig 是按 kind 判别的领域配置；git 为占位（创建被拒）。
export type JobConfig = FileJobConfig | OciJobConfig

// isFileJobConfig 以 source_id 判别 file 配置（调用方窄化用）。
export function isFileJobConfig(config: JobConfig): config is FileJobConfig {
    return 'source_id' in config
}

// JobResponse 是统一任务的 API 表示，与后端 jobDTO 一一对应：
// kind + config（领域 JSON 原样透传）。
export interface JobResponse {
    id: string
    name: string
    kind: JobKind
    enabled: boolean
    config: JobConfig
    schedule: ScheduleSpec
    created_at: string
    updated_at: string
}

// JobsListResponse 对应 GET /api/v1/jobs 的包装对象。
export interface JobsListResponse {
    jobs: JobResponse[]
}

export interface LocalDirectoriesResponse {
    path: string
    parent?: string
    directories: Array<{ path: string; name: string }>
}

// listLocalDirectories 读取运行 TinySync 主机上的直接子目录，供管理员选择 LocalRoot。
export function listLocalDirectories(path: string): Promise<LocalDirectoriesResponse> {
    return listLocalDirectoriesWithOptions(path, false)
}

// listLocalDirectoriesWithOptions 读取运行 TinySync 主机上的直接子目录。
export function listLocalDirectoriesWithOptions(
    path: string,
    showHidden: boolean,
): Promise<LocalDirectoriesResponse> {
    return getJSON<LocalDirectoriesResponse>(
        `/api/v1/jobs/local-directories?path=${encodeURIComponent(path)}&hidden=${showHidden}`,
    )
}

// createLocalDirectory 在当前浏览的服务端目录内建立一个直接子目录。
export function createLocalDirectory(path: string, name: string): Promise<{ path: string }> {
    return requestJSON<{ path: string }>('POST', '/api/v1/jobs/local-directories', { path, name })
}

// createRemoteDirectory 在当前浏览的远端目录内建立一个直接子目录。
export function createRemoteDirectory(
    sourceId: string,
    path: string,
    name: string,
): Promise<{ path: string }> {
    return requestJSON<{ path: string }>('POST', `/api/v1/sources/${sourceId}/directories`, {
        path,
        name,
    })
}

// CreateJobInput 对应 POST /api/v1/jobs 请求体（统一 kind + config）；
// enabled / schedule 缺省为 true / manual。
export interface CreateJobInput {
    name: string
    kind: JobKind
    config: JobConfig
    enabled: boolean
    schedule?: ScheduleSpec
}

// UpdateJobInput 对应 PATCH 请求体：undefined 字段保留现有值；
// config 提供时整体替换（先经执行器校验）；schedule 提供时原子替换；
// kind 不可变更。
export interface UpdateJobInput {
    name?: string
    config?: JobConfig
    enabled?: boolean
    schedule?: ScheduleSpec
}

export async function listJobs(): Promise<JobResponse[]> {
    const data = await getJSON<JobsListResponse>('/api/v1/jobs')
    return data.jobs
}

export function createJob(input: CreateJobInput): Promise<JobResponse> {
    return requestJSON<JobResponse>('POST', '/api/v1/jobs', input)
}

export function getJob(id: string): Promise<JobResponse> {
    return requestJSON<JobResponse>('GET', `/api/v1/jobs/${id}`)
}

export function updateJob(id: string, input: UpdateJobInput): Promise<JobResponse> {
    return requestJSON<JobResponse>('PATCH', `/api/v1/jobs/${id}`, input)
}

export async function deleteJob(id: string): Promise<void> {
    await requestJSON<void>('DELETE', `/api/v1/jobs/${id}`)
}

// RunState 是运行的状态机取值。运行记录持久化于服务端，
// 重启后最近一次运行（含 skipped）仍可查询。canceled 专指用户手动停止。
export type RunState = 'idle' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'canceled'

// RunStatsResponse 是 file 任务的领域结果摘要（result_json 解码形态）。
export interface RunStatsResponse {
    files_total: number
    files_created: number
    files_updated: number
    files_deleted: number
    files_skipped: number
    bytes_transferred: number
}

// OciRunResultResponse 是 oci 任务的领域结果摘要（result_json 解码
// 形态）。running / skipped 的 run 无结果（空对象），计数按缺省 0 处理。
export interface OciRunResultResponse {
    status: string
    total_images: number
    success_count: number
    skipped_count: number
    failure_count: number
    transferred_bytes?: number
    duration_ms?: number
}

// RunResultPayload 是按 job kind 判别的结果摘要；空对象表示尚无结果
//（running / skipped 或从未运行）。
export type RunResultPayload = RunStatsResponse | OciRunResultResponse | Record<string, never>

// isFileRunResult 以 files_total 判别 file 结果摘要。
export function isFileRunResult(result: RunResultPayload): result is RunStatsResponse {
    return 'files_total' in result
}

// isOciRunResult 以 total_images 判别 oci 结果摘要。
export function isOciRunResult(result: RunResultPayload): result is OciRunResultResponse {
    return 'total_images' in result
}

// RunStatusResponse 对应 GET /api/v1/jobs/:id/status。
// next_run_at 为下一次计划触发时间，manual 或 once 已消费时省略。
// result 为领域结果摘要（file 为文件统计、oci 为镜像复制摘要）。
export interface RunStatusResponse {
    run_id?: string
    state: RunState
    started_at?: string
    finished_at?: string
    next_run_at?: string
    result: RunResultPayload
    error?: string
}

// RunJobResponse 对应 POST /api/v1/jobs/:id/run 的 202 响应。
export interface RunJobResponse {
    run_id: string
    state: RunState
}

// RunPhase 是运行中 run 的生命周期阶段。scanning / planning 阶段工作
// 量未知（前端以 indeterminate 进度表达），transferring 起 work_total
// 可用。
export type RunPhase = 'connecting' | 'scanning' | 'planning' | 'transferring' | 'finalizing'

// RunProgressFileResponse 是一个在途文件的单文件进度：后端只输出原始
// 字节计数，百分比与显示文本由前端换算。resume_from 是本次 attempt
// 的断点起点（断点续传，仅展示；0 / 缺省 = 全新传输）。
export interface RunProgressFileResponse {
    target: string
    action: RunItemAction
    bytes_done: number
    bytes_total: number
    resume_from?: number
}

// RunProgressResponse 是运行中 run 的实时进度（瞬态内存快照）。
export interface RunProgressResponse {
    phase: RunPhase
    work_done: number
    work_total: number
    active_files?: RunProgressFileResponse[]
}

// RunJobOptions 是手动触发的可选请求体：src_images 提供时本次运行以
// 子集覆盖 config 的 src_images（仅 kind=oci；只改本轮配置快照，不落库）。
export interface RunJobOptions {
    src_images: string[]
}

// runJob 异步启动一轮运行。body 缺省为空请求体（全量触发）；携带
// src_images 子集时后端按 oci 任务校验并展开。
export function runJob(id: string, body?: RunJobOptions): Promise<RunJobResponse> {
    return requestJSON<RunJobResponse>('POST', `/api/v1/jobs/${id}/run`, body)
}

// CancelRunResponse 对应 POST /api/v1/runs/:id/cancel 的 202 响应。
// 409（已终态，附 state）与 404（不存在）以错误路径抛出。
export interface CancelRunResponse {
    run_id: string
    state: RunState
}

export function cancelRun(runId: string): Promise<CancelRunResponse> {
    return requestJSON<CancelRunResponse>('POST', `/api/v1/runs/${runId}/cancel`)
}

export function fetchJobStatus(id: string): Promise<RunStatusResponse> {
    return requestJSON<RunStatusResponse>('GET', `/api/v1/jobs/${id}/status`)
}

// RunTriggerType 是一轮运行的触发方式。
export type RunTriggerType = 'manual' | 'once' | 'interval' | 'cron'

// RunRecordResponse 对应 GET /api/v1/runs/:id 的运行摘要。运行中的
// run 附实时 progress（瞬态内存快照）；终态省略。result 为领域结果
// 摘要（file 为文件统计、oci 为镜像复制摘要）。
export interface RunRecordResponse {
    id: string
    job_id: string
    job_kind: JobKind
    job_name: string
    trigger: RunTriggerType
    scheduled_for?: string
    status: RunState
    started_at: string
    finished_at?: string
    result: RunResultPayload
    error?: string
    progress?: RunProgressResponse
}

// RunsListResponse 对应 GET /api/v1/runs 的包装对象。
export interface RunsListResponse {
    runs: RunRecordResponse[]
    total: number
}

// RunItemAction 是明细级动作：file 为文件变更（create/update/delete/
// relinquish），oci 为镜像复制（copy/skip）。
export type RunItemAction = 'create' | 'update' | 'delete' | 'relinquish' | 'copy' | 'skip'

// RunItemStatus 是明细级结果。canceled 表示传输被用户手动停止中断。
export type RunItemStatus = 'succeeded' | 'failed' | 'skipped' | 'canceled'

// OciRunItemDetail 是 oci 明细的领域扩展字段（detail 原始 JSON 的解码
// 形态：src / registry / 平台级结果等）。公共层原样透传，无扩展字段的
// 明细（file 领域）省略该键。
export interface OciRunItemDetail {
    src?: string
    registry?: string
    status?: string
    duration_ms?: number
    transferred_bytes?: number
    error?: string
    platform_results?: Array<{
        platform: string
        status: string
        duration_ms?: number
        error?: string
    }>
}

// RunItemResponse 是运行明细；target 为统一展示目标（file 为相对
// 本地根的路径，oci 为目标镜像完整引用），unchanged 项不产生明细。
export interface RunItemResponse {
    id: number
    run_id: string
    target: string
    action: RunItemAction
    status: RunItemStatus
    bytes: number
    error?: string
    detail?: OciRunItemDetail
}

// RunItemsListResponse 对应 GET /api/v1/runs/:id/items 的包装对象。
export interface RunItemsListResponse {
    items: RunItemResponse[]
    total: number
}

// RunRunsQuery 是 /runs 的过滤与分页参数。
export interface RunRunsQuery {
    job_id?: string
    kind?: JobKind
    status?: RunState
    limit?: number
    offset?: number
}

export async function listRuns(query?: RunRunsQuery): Promise<RunsListResponse> {
    const params = new URLSearchParams()
    if (query?.job_id !== undefined) {
        params.set('job_id', query.job_id)
    }
    if (query?.kind !== undefined) {
        params.set('kind', query.kind)
    }
    if (query?.status !== undefined) {
        params.set('status', query.status)
    }
    if (query?.limit !== undefined) {
        params.set('limit', String(query.limit))
    }
    if (query?.offset !== undefined) {
        params.set('offset', String(query.offset))
    }
    const qs = params.toString()
    return getJSON<RunsListResponse>(`/api/v1/runs${qs === '' ? '' : `?${qs}`}`)
}

export function getRun(id: string): Promise<RunRecordResponse> {
    return getJSON<RunRecordResponse>(`/api/v1/runs/${id}`)
}

export async function listRunItems(
    id: string,
    limit?: number,
    offset?: number,
): Promise<RunItemsListResponse> {
    const params = new URLSearchParams()
    if (limit !== undefined) {
        params.set('limit', String(limit))
    }
    if (offset !== undefined) {
        params.set('offset', String(offset))
    }
    const qs = params.toString()
    return getJSON<RunItemsListResponse>(`/api/v1/runs/${id}/items${qs === '' ? '' : `?${qs}`}`)
}

// ===== 文件浏览（v0.6）=====

// FileKind 是条目类型；symlink / other 仅展示不可操作。
export type FileKind = 'file' | 'directory' | 'symlink' | 'other'

// FileEntry 是浏览条目：Remote 条目不含 managed，Local 条目携带。
export interface FileEntry {
    path: string
    name: string
    kind: FileKind
    size: number
    modified_at: string | null
    managed?: boolean
}

// FilesPageResponse 对应目录列表响应；next_cursor 空串表示 EOF。
export interface FilesPageResponse {
    path: string
    entries: FileEntry[]
    next_cursor: string
}

// filesQuery 组装 path / limit / cursor 查询串；path 缺省为根目录。
function filesQuery(path: string, limit?: number, cursor?: string, showHidden?: boolean): string {
    const params = new URLSearchParams()
    params.set('path', path)
    if (limit !== undefined) {
        params.set('limit', String(limit))
    }
    if (cursor !== undefined && cursor !== '') {
        params.set('cursor', cursor)
    }

    if (showHidden !== undefined) {
        params.set('hidden', String(showHidden))
    }
    return params.toString()
}

// listRemoteFiles 分页列出 Source 的一层目录。
export function listRemoteFiles(
    sourceId: string,
    path = '/',
    limit?: number,
    cursor?: string,
    showHidden?: boolean,
): Promise<FilesPageResponse> {
    return getJSON<FilesPageResponse>(
        `/api/v1/sources/${sourceId}/files?${filesQuery(path, limit, cursor, showHidden)}`,
    )
}

// statRemoteFile 读取远端条目元信息。
export function statRemoteFile(sourceId: string, path: string): Promise<FileEntry> {
    return getJSON<FileEntry>(
        `/api/v1/sources/${sourceId}/files/stat?path=${encodeURIComponent(path)}`,
    )
}

// listLocalFiles 分页列出 Job.LocalRoot 下的一层目录。
export function listLocalFiles(
    jobId: string,
    path = '/',
    limit?: number,
    cursor?: string,
    showHidden?: boolean,
): Promise<FilesPageResponse> {
    return getJSON<FilesPageResponse>(
        `/api/v1/jobs/${jobId}/files?${filesQuery(path, limit, cursor, showHidden)}`,
    )
}

// statLocalFile 读取本地条目元信息。
export function statLocalFile(jobId: string, path: string): Promise<FileEntry> {
    return getJSON<FileEntry>(`/api/v1/jobs/${jobId}/files/stat?path=${encodeURIComponent(path)}`)
}

// remoteFileDownloadURL 构造远端下载直链（流式 attachment）。
export function remoteFileDownloadURL(sourceId: string, path: string): string {
    return `/api/v1/sources/${sourceId}/files/download?path=${encodeURIComponent(path)}`
}

// localFileDownloadURL 构造本地下载直链（支持 Range / HEAD）。
export function localFileDownloadURL(jobId: string, path: string): string {
    return `/api/v1/jobs/${jobId}/files/download?path=${encodeURIComponent(path)}`
}

// ===== 共享策略（v0.10）=====

// ShareResponse 是共享策略的 API 表示：name 为自定义共享名称
// （null 表示未命名，展示回落目标名）；expires_at 空串表示永不过期。
export interface ShareResponse {
    id: string
    local_path: string
    slug: string
    name: string | null
    is_dir: boolean
    enabled: boolean
    expires_at: string
    created_at: string
    updated_at: string
}

// SharesListResponse 对应 GET /api/v1/shares。
export interface SharesListResponse {
    shares: ShareResponse[]
}

// CreateShareInput 对应 POST /api/v1/shares：目标以 job_id +
// LocalRoot 内逻辑路径表达（"/" 即整个本地根）；name 为可选的自定义
// 共享名称（即 URL slug，留空随机生成）。
export interface CreateShareInput {
    job_id: string
    path: string
    name?: string
    enabled?: boolean
    expires_at?: string
}

// UpdateShareInput 对应 PATCH：name 三态——undefined 保留、空串清除
// 自定义名称（slug 不变）、非空同时改写 slug（旧公开链接立即失效）；
// expires_at 三态——undefined 保留、null 清除（永不过期）、RFC3339
// 字符串设置。
export interface UpdateShareInput {
    name?: string
    enabled?: boolean
    expires_at?: string | null
}

// listShares 返回全部共享策略。
export function listShares(): Promise<ShareResponse[]> {
    return getJSON<SharesListResponse>('/api/v1/shares').then(body => body.shares)
}

// createShare 创建共享策略（目标可为文件 / 目录 / 本地根）。
export function createShare(input: CreateShareInput): Promise<ShareResponse> {
    return requestJSON<ShareResponse>('POST', '/api/v1/shares', input)
}

// updateShare 部分更新共享策略（local_path 与 is_dir 不可变）。
export function updateShare(id: string, input: UpdateShareInput): Promise<ShareResponse> {
    return requestJSON<ShareResponse>('PATCH', `/api/v1/shares/${id}`, input)
}

// deleteShare 删除共享策略（只移除记录，不触及本地文件）。
export async function deleteShare(id: string): Promise<void> {
    await requestJSON<unknown>('DELETE', `/api/v1/shares/${id}`)
}

// shareEntryURL 构造共享的访问链接：目录共享指向浏览页，文件共享
// 指向自身直链（basename 兼容 / 与 \ 分隔的 local_path）。
export function shareEntryURL(
    share: Pick<ShareResponse, 'slug' | 'is_dir' | 'local_path'>,
): string {
    if (share.is_dir) {
        return sharedBrowseURL(share.slug)
    }
    const name = share.local_path.split(/[\\/]/).pop() ?? share.local_path
    return sharedFileURL(share.slug, `/${name}`)
}

// ===== 共享公开访问（v0.10）=====

// listPublicShareEntries 分页浏览共享目录（无认证端点）；文件共享的
// 根 path 返回恰含自身的单条目（虚拟目录）。
export function listPublicShareEntries(
    slug: string,
    path = '/',
    limit?: number,
    cursor?: string,
): Promise<FilesPageResponse> {
    return getJSON<FilesPageResponse>(
        `/api/v1/public/shares/${slug}/entries?${filesQuery(path, limit, cursor)}`,
    )
}

// sharedBrowsePath 构造共享目录的应用内路由；每段单独编码以保留层级。
export function sharedBrowsePath(slug: string, path = '/'): string {
    const encodedPath = path
        .split('/')
        .filter(Boolean)
        .map(part => encodeURIComponent(part))
        .join('/')
    return `/shared/${encodeURIComponent(slug)}${encodedPath === '' ? '' : `/${encodedPath}`}`
}

// sharedBrowseURL 构造共享浏览页的绝对地址（供管理侧复制）。
export function sharedBrowseURL(slug: string): string {
    return `${window.location.origin}${sharedBrowsePath(slug)}`
}

// sharedFileURL 构造共享内文件的公开直链（支持 Range / HEAD）。
export function sharedFileURL(slug: string, path: string): string {
    return `${window.location.origin}${sharedBrowsePath(slug, path)}`
}

// ===== API Token 管理（v0.7）=====

// APITokenScope 是 token 的授权 scope；admin 蕴含 read + run。
export type APITokenScope = 'read' | 'run' | 'admin'

// APITokenResponse 是 API Token 元数据：raw token 与 hash 不在此列。
// 可空时刻输出空串（expires_at 空 = 永不过期、last_used_at 空 =
// 从未使用、revoked_at 空 = 未撤销）。
export interface APITokenResponse {
    id: string
    name: string
    prefix: string
    scopes: APITokenScope[]
    created_at: string
    expires_at: string
    last_used_at: string
    revoked_at: string
}

// APITokensListResponse 对应 GET /api/v1/api-tokens 的包装对象。
export interface APITokensListResponse {
    api_tokens: APITokenResponse[]
}

// CreateAPITokenInput 对应 POST /api/v1/api-tokens：expires_at 为
// 可选 RFC3339，缺省永不过期。
export interface CreateAPITokenInput {
    name: string
    scopes: APITokenScope[]
    expires_at?: string
}

// CreateAPITokenResponse 是创建响应：raw_token 只出现这一次。
export interface CreateAPITokenResponse {
    api_token: APITokenResponse
    raw_token: string
}

// listAPITokens 返回全部 token 元数据。
export async function listAPITokens(): Promise<APITokenResponse[]> {
    const data = await apiFetch<APITokensListResponse>('GET', '/api/v1/api-tokens')
    return data.api_tokens
}

// createAPIToken 创建 token；raw token 只在返回值中出现一次。
export function createAPIToken(input: CreateAPITokenInput): Promise<CreateAPITokenResponse> {
    return requestJSON<CreateAPITokenResponse>('POST', '/api/v1/api-tokens', input)
}

// revokeAPIToken 幂等软撤销（已撤销的再次撤销仍成功）。
export async function revokeAPIToken(id: string): Promise<void> {
    await requestJSON<void>('POST', `/api/v1/api-tokens/${id}/revoke`)
}

// ===== 通知（v0.13）=====

// NotificationSecurity 是 SMTP 连接安全模式。
export type NotificationSecurity = 'none' | 'starttls' | 'tls'

// PushoverSettingsResponse 是 Pushover 渠道回显：secret 只以 configured
// 布尔出现，任何响应都不回显明文。
export interface PushoverSettingsResponse {
    enabled: boolean
    token_configured: boolean
    user_key_configured: boolean
}

// EmailSettingsResponse 是邮件渠道回显；password 只以 configured 布尔
// 出现。
export interface EmailSettingsResponse {
    enabled: boolean
    host: string
    port: number
    security: NotificationSecurity
    username: string
    password_configured: boolean
    from: string
    to: string[]
}

// NotificationSettingsResponse 对应 GET /api/v1/notifications/settings。
export interface NotificationSettingsResponse {
    pushover: PushoverSettingsResponse
    email: EmailSettingsResponse
    updated_at?: string
}

// PushoverSettingsPatch 是 Pushover 渠道的部分更新：secret 三态
//（不提供保留 / 提供替换 / clear 删除）。
export interface PushoverSettingsPatch {
    enabled?: boolean
    token?: string
    clear_token?: boolean
    user_key?: string
    clear_user_key?: boolean
}

// EmailSettingsPatch 是邮件渠道的部分更新；非 secret 字段不提供保留、
// 提供即替换，to 整体替换，password 三态。
export interface EmailSettingsPatch {
    enabled?: boolean
    host?: string
    port?: number
    security?: NotificationSecurity
    username?: string
    from?: string
    to?: string[]
    password?: string
    clear_password?: boolean
}

// NotificationSettingsPatch 对应 PATCH /api/v1/notifications/settings。
export interface NotificationSettingsPatch {
    pushover?: PushoverSettingsPatch
    email?: EmailSettingsPatch
}

// NotificationTestResponse 对应 POST /api/v1/notifications/test/:channel。
// 发送失败也是一次成功的测试操作（ok=false 携带原因）。
export interface NotificationTestResponse {
    ok: boolean
    error?: string
}

// fetchNotificationSettings 返回当前通知配置。
export function fetchNotificationSettings(): Promise<NotificationSettingsResponse> {
    return getJSON<NotificationSettingsResponse>('/api/v1/notifications/settings')
}

// updateNotificationSettings 应用部分更新并返回更新后的配置。
export function updateNotificationSettings(
    input: NotificationSettingsPatch,
): Promise<NotificationSettingsResponse> {
    return requestJSON<NotificationSettingsResponse>(
        'PATCH',
        '/api/v1/notifications/settings',
        input,
    )
}

// testNotification 用已保存的配置向指定渠道发送测试通知。
export function testNotification(channel: 'pushover' | 'email'): Promise<NotificationTestResponse> {
    return requestJSON<NotificationTestResponse>('POST', `/api/v1/notifications/test/${channel}`)
}

// ===== Registry 配置与镜像管理（OCI 领域）=====

// RegistryKind 是 registries 行的类型：primary 为真实 Registry 配置
//（同步目标与凭据来源），mirror 是上游主机的加速器列表。
export type RegistryKind = 'primary' | 'mirror'

// RegistryPathTransform 是镜像路径转换配置；null 表示未配置。
export interface RegistryPathTransform {
    separator: string
    mode: string
}

// RegistryResponse 是 Registry 配置的 API 表示：password 永不回显
//（只回 password_set），namespace 为 host 的派生只读字段；mirror 行
// 的 mirrors 是加速器地址列表。strip_registries / mirrors 为可空列表
//（Go 仓储层把空列表归一为 nil，序列化为 null）——消费方按 ?? [] 兜底。
export interface RegistryResponse {
    id: string
    name: string
    kind: RegistryKind
    host: string
    namespace: string
    username: string
    password_set: boolean
    insecure_skip_verify: boolean
    plain_http: boolean
    strip_registries: string[] | null
    path_transform: RegistryPathTransform | null
    mirrors: string[] | null
    created_at: string
    updated_at: string
}

// RegistriesListResponse 对应 GET /api/v1/registries 的包装对象。
export interface RegistriesListResponse {
    registries: RegistryResponse[]
}

// CreateRegistryInput 对应 POST /api/v1/registries：kind 缺省 primary；
// mirror 行的 host 是上游主机且 mirrors 必填；path_transform 缺省 /
// null 均视为未配置。
export interface CreateRegistryInput {
    name: string
    kind: RegistryKind
    host: string
    username?: string
    password?: string
    insecure_skip_verify: boolean
    plain_http: boolean
    strip_registries?: string[]
    path_transform?: RegistryPathTransform | null
    mirrors?: string[]
}

// UpdateRegistryInput 对应 PATCH /api/v1/registries/:id：undefined 字段
// 保留现有值；password 三态（缺省保留、空串清除、非空替换）；
// path_transform 携带（含 null）即替换；kind 不可变更。
export interface UpdateRegistryInput {
    name?: string
    host?: string
    username?: string
    password?: string
    insecure_skip_verify?: boolean
    plain_http?: boolean
    strip_registries?: string[]
    path_transform?: RegistryPathTransform | null
    mirrors?: string[]
}

// listRegistries 返回全部 Registry 配置（primary + mirror）。
export async function listRegistries(): Promise<RegistryResponse[]> {
    const data = await getJSON<RegistriesListResponse>('/api/v1/registries')
    return data.registries
}

// createRegistry 新建 Registry 配置（primary 或 mirror 行）。
export function createRegistry(input: CreateRegistryInput): Promise<RegistryResponse> {
    return requestJSON<RegistryResponse>('POST', '/api/v1/registries', input)
}

// updateRegistry 部分更新 Registry 配置。
export function updateRegistry(id: string, input: UpdateRegistryInput): Promise<RegistryResponse> {
    return requestJSON<RegistryResponse>('PATCH', `/api/v1/registries/${id}`, input)
}

// deleteRegistry 删除 Registry 配置（仍被 oci 任务 dst_servers 引用时
// 后端以 409 拒绝）。
export async function deleteRegistry(id: string): Promise<void> {
    await requestJSON<void>('DELETE', `/api/v1/registries/${id}`)
}

// ImageListItemResponse 是一个源镜像在所属 oci 任务下的配置与最近
// 执行状态（last_status: success | skipped | failed；未运行为空）。
export interface ImageListItemResponse {
    source: string
    job_id: string
    job_name: string
    job_enabled: boolean
    dst_registries: string[]
    last_status?: string
    last_sync_time?: string
}

// listImages 列出全部 oci 任务的源镜像与最近执行状态。
export async function listImages(): Promise<ImageListItemResponse[]> {
    const data = await getJSON<{ images: ImageListItemResponse[] }>('/api/v1/images')
    return data.images
}

// ImageTagsResponse 对应 GET /api/v1/images/tags?repo=（按字典序倒序，
// default_tag 优先 latest）。
export interface ImageTagsResponse {
    repo: string
    tags: string[]
    default_tag: string
}

// listImageTags 列出仓库 tag（无分页）。
export function listImageTags(repo: string): Promise<ImageTagsResponse> {
    return getJSON<ImageTagsResponse>(`/api/v1/images/tags?repo=${encodeURIComponent(repo)}`)
}

// InspectPlatform 是平台摘要（查询参数与响应共用形态）。
export interface InspectPlatform {
    os?: string
    architecture?: string
    variant?: string
}

// RemoteInspectResult 是远端镜像元数据（oci-sync 模型移植）。
export interface RemoteInspectResult {
    ref: string
    tag: string
    mirror: string
    digest?: string
    media_type?: string
    size?: number
    is_index: boolean
    manifest?: unknown
    config?: unknown
    platforms?: InspectPlatform[]
    platforms_status?: Record<string, boolean>
}

// ImageInspectResponse 对应 GET /api/v1/images/inspect?image=&registry=。
export interface ImageInspectResponse {
    image: string
    result: RemoteInspectResult
}

// ImagePlatformInspectResponse 对应 GET /api/v1/images/inspect/platform
// ?image=&os=&architecture=&variant=。
export interface ImagePlatformInspectResponse {
    image: string
    platform: InspectPlatform
    result: RemoteInspectResult
}

// inspectImage 查询远端镜像元数据；registry 为配置名（多配置同主机时
// 消歧），缺省按镜像引用所属主机解析。
export function inspectImage(image: string, registry?: string): Promise<ImageInspectResponse> {
    const params = new URLSearchParams({ image })
    if (registry !== undefined && registry !== '') {
        params.set('registry', registry)
    }
    return getJSON<ImageInspectResponse>(`/api/v1/images/inspect?${params.toString()}`)
}

// inspectImagePlatform 查询具体平台的镜像元数据。
export function inspectImagePlatform(
    image: string,
    platform: { os: string; architecture: string; variant?: string },
    registry?: string,
): Promise<ImagePlatformInspectResponse> {
    const params = new URLSearchParams({
        image,
        os: platform.os,
        architecture: platform.architecture,
    })
    if (platform.variant !== undefined && platform.variant !== '') {
        params.set('variant', platform.variant)
    }
    if (registry !== undefined && registry !== '') {
        params.set('registry', registry)
    }
    return getJSON<ImagePlatformInspectResponse>(
        `/api/v1/images/inspect/platform?${params.toString()}`,
    )
}

// RemoteSearchPage 是远端 discovery 的分页响应：scanned /
// scan_limit_reached 表达有界 catalog 扫描进度，不伪造总数。
export interface RemoteSearchPage {
    items: string[]
    has_more: boolean
    next_cursor?: string
    scanned: number
    scan_limit_reached: boolean
}

// RemoteSearchQuery 是远端 discovery 的查询参数（limit 1..100 缺省 50、
// scan_limit 1..5000 缺省 1000、q 过滤关键词、cursor 翻页）。
export interface RemoteSearchQuery {
    q?: string
    limit?: number
    scan_limit?: number
    cursor?: string
}

// remoteQuery 组装 discovery 查询串。
function remoteQuery(query?: RemoteSearchQuery): string {
    const params = new URLSearchParams()
    if (query?.q !== undefined && query.q !== '') {
        params.set('q', query.q)
    }
    if (query?.limit !== undefined) {
        params.set('limit', String(query.limit))
    }
    if (query?.scan_limit !== undefined) {
        params.set('scan_limit', String(query.scan_limit))
    }
    if (query?.cursor !== undefined && query.cursor !== '') {
        params.set('cursor', query.cursor)
    }
    const qs = params.toString()
    return qs === '' ? '' : `?${qs}`
}

// searchRemoteRepositories 在指定 Registry 配置（名或主机）内做有界
// catalog 扫描，返回仓库名分页。
export function searchRemoteRepositories(
    registry: string,
    query?: RemoteSearchQuery,
): Promise<RemoteSearchPage> {
    return getJSON<RemoteSearchPage>(
        `/api/v1/remote/repositories?registry=${encodeURIComponent(registry)}${remoteQuery(query)}`,
    )
}

// listRemoteTags 分页列出远端仓库 tag；registry 为配置名（可选）。
export function listRemoteTags(
    repository: string,
    query?: RemoteSearchQuery,
    registry?: string,
): Promise<RemoteSearchPage> {
    const suffix =
        registry !== undefined && registry !== '' ? `&registry=${encodeURIComponent(registry)}` : ''
    return getJSON<RemoteSearchPage>(
        `/api/v1/remote/tags?repository=${encodeURIComponent(repository)}${remoteQuery(query)}${suffix}`,
    )
}
