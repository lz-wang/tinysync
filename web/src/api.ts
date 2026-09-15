// 后端 REST API 客户端：与 Go 侧 /api/v1 契约一一对应。

// HealthResponse 对应 GET /api/v1/health。
export interface HealthResponse {
    status: string
}

// VersionResponse 对应 GET /api/v1/version。
export interface VersionResponse {
    version: string
}

// SourceResponse 是 Source 的 API 表示；绝不包含密码，
// 凭据状态只以 password_set 暴露。
export interface SourceResponse {
    id: string
    name: string
    type: string
    endpoint: string
    username: string
    password_set: boolean
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

// CreateSourceInput 对应 POST /api/v1/sources 请求体。
export interface CreateSourceInput {
    name: string
    type: string
    endpoint: string
    username: string
    password: string
    enabled: boolean
}

// UpdateSourceInput 对应 PATCH 请求体：undefined 字段保留现有值；
// password 语义为 undefined 保留、空串清除、非空替换。
export interface UpdateSourceInput {
    name?: string
    endpoint?: string
    username?: string
    password?: string
    enabled?: boolean
}

async function getJSON<T>(path: string): Promise<T> {
    const response = await fetch(path)
    if (!response.ok) {
        throw new Error(await errorMessage('GET', path, response))
    }
    return (await response.json()) as T
}

// requestJSON 处理带请求体的方法、后端统一错误格式与 204 响应。
async function requestJSON<T>(method: string, path: string, body?: unknown): Promise<T> {
    const response = await fetch(path, {
        method,
        headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
    })
    if (!response.ok) {
        throw new Error(await errorMessage(method, path, response))
    }
    if (response.status === 204) {
        return undefined as T
    }
    return (await response.json()) as T
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

// JobMode 是同步模式：Copy 只增不改删本地既有文件；
// Mirror 额外按 managed 授权删除远端已消失的本地文件。
export type JobMode = 'copy' | 'mirror'

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
    created_at: string
    updated_at: string
}

// JobsListResponse 对应 GET /api/v1/jobs 的包装对象。
export interface JobsListResponse {
    jobs: JobResponse[]
}

// CreateJobInput 对应 POST /api/v1/jobs 请求体；enabled 缺省为 true。
export interface CreateJobInput {
    name: string
    source_id: string
    remote_root: string
    local_root: string
    mode: JobMode
    include: string[]
    exclude: string[]
    enabled: boolean
}

// UpdateJobInput 对应 PATCH 请求体：undefined 字段保留现有值；
// include / exclude 提供数组时整体替换（空数组表示 include all / 无排除）。
export interface UpdateJobInput {
    name?: string
    source_id?: string
    remote_root?: string
    local_root?: string
    mode?: JobMode
    include?: string[]
    exclude?: string[]
    enabled?: boolean
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

// RunState 是手动运行的状态机取值；运行记录只存内存，
// 进程重启后回到 idle。
export type RunState = 'idle' | 'running' | 'succeeded' | 'failed'

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
export interface RunStatusResponse {
    run_id?: string
    state: RunState
    started_at?: string
    finished_at?: string
    stats: RunStatsResponse
    error?: string
}

// RunJobResponse 对应 POST /api/v1/jobs/:id/run 的 202 响应。
export interface RunJobResponse {
    run_id: string
    state: RunState
}

export function runJob(id: string): Promise<RunJobResponse> {
    return requestJSON<RunJobResponse>('POST', `/api/v1/jobs/${id}/run`)
}

export function fetchJobStatus(id: string): Promise<RunStatusResponse> {
    return requestJSON<RunStatusResponse>('GET', `/api/v1/jobs/${id}/status`)
}
