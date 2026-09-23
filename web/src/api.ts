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
export type SourceType = 'webdav' | 's3' | 'sftp' | 'github_release'

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

// SourceConfig 是按 type 判别的协议配置（请求与响应均为扁平单选对象）。
export type SourceConfig = WebDAVConfig | S3Config | SFTPConfig | GitHubReleaseConfig

// CredentialState 回显各 secret 是否设置；任何 secret 不回显明文。
export interface CredentialState {
    webdav?: { password_set: boolean }
    s3?: { secret_key_set: boolean }
    sftp?: {
        password_set: boolean
        private_key_set: boolean
        private_key_passphrase_set: boolean
    }
    github_release?: { token_set: boolean }
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

export interface GitHubReleaseCredentials {
    token?: string
}

// SourceCredentials 按 type 单选的 secret 组。
export type SourceCredentials =
    | WebDAVCredentials
    | S3Credentials
    | SFTPCredentials
    | GitHubReleaseCredentials

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

// JobResponse 是 Sync Job 的 API 表示，与后端 jobDTO 一一对应。
export interface JobResponse {
    id: string
    name: string
    source_id: string
    remote_root: string
    local_root: string
    mode: JobMode
    include: string[]
    exclude: string[]
    enabled: boolean
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
    directories: Array<{ path: string }>
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

// CreateJobInput 对应 POST /api/v1/jobs 请求体；enabled / schedule
// 缺省为 true / manual。
export interface CreateJobInput {
    name: string
    source_id: string
    remote_root: string
    local_root: string
    mode: JobMode
    include: string[]
    exclude: string[]
    enabled: boolean
    schedule?: ScheduleSpec
}

// UpdateJobInput 对应 PATCH 请求体：undefined 字段保留现有值；
// include / exclude 提供数组时整体替换；schedule 提供时原子替换。
export interface UpdateJobInput {
    name?: string
    source_id?: string
    remote_root?: string
    local_root?: string
    mode?: JobMode
    include?: string[]
    exclude?: string[]
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

// RunStatsResponse 是一轮同步的统计摘要。
export interface RunStatsResponse {
    files_total: number
    files_created: number
    files_updated: number
    files_deleted: number
    files_skipped: number
    bytes_transferred: number
}

// RunStatusResponse 对应 GET /api/v1/jobs/:id/status。
// next_run_at 为下一次计划触发时间，manual 或 once 已消费时省略。
export interface RunStatusResponse {
    run_id?: string
    state: RunState
    started_at?: string
    finished_at?: string
    next_run_at?: string
    stats: RunStatsResponse
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
// 字节计数，百分比与显示文本由前端换算。
export interface RunProgressFileResponse {
    path: string
    action: RunItemAction
    bytes_done: number
    bytes_total: number
}

// RunProgressResponse 是运行中 run 的实时进度（瞬态内存快照）。
export interface RunProgressResponse {
    phase: RunPhase
    work_done: number
    work_total: number
    active_files?: RunProgressFileResponse[]
}

export function runJob(id: string): Promise<RunJobResponse> {
    return requestJSON<RunJobResponse>('POST', `/api/v1/jobs/${id}/run`)
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
// run 附实时 progress（瞬态内存快照）；终态省略。
export interface RunRecordResponse {
    id: string
    job_id: string
    job_name: string
    trigger: RunTriggerType
    scheduled_for?: string
    status: RunState
    started_at: string
    finished_at?: string
    stats: RunStatsResponse
    error?: string
    progress?: RunProgressResponse
}

// RunsListResponse 对应 GET /api/v1/runs 的包装对象。
export interface RunsListResponse {
    runs: RunRecordResponse[]
    total: number
}

// RunItemAction 是文件级变更动作。
export type RunItemAction = 'create' | 'update' | 'delete' | 'relinquish'

// RunItemStatus 是文件级变更结果。canceled 表示传输被用户手动停止中断。
export type RunItemStatus = 'succeeded' | 'failed' | 'skipped' | 'canceled'

// RunItemResponse 是文件级变更明细；unchanged 文件不产生明细。
export interface RunItemResponse {
    id: number
    run_id: string
    path: string
    action: RunItemAction
    status: RunItemStatus
    bytes: number
    error?: string
}

// RunItemsListResponse 对应 GET /api/v1/runs/:id/items 的包装对象。
export interface RunItemsListResponse {
    items: RunItemResponse[]
    total: number
}

// RunRunsQuery 是 /runs 的过滤与分页参数。
export interface RunRunsQuery {
    job_id?: string
    status?: RunState
    limit?: number
    offset?: number
}

export async function listRuns(query?: RunRunsQuery): Promise<RunsListResponse> {
    const params = new URLSearchParams()
    if (query?.job_id !== undefined) {
        params.set('job_id', query.job_id)
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
