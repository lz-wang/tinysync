// 后端 REST API 客户端：与 Go 侧 /api/v1 契约一一对应。

// HealthResponse 对应 GET /api/v1/health。
export interface HealthResponse {
    status: string
}

// VersionResponse 对应 GET /api/v1/version。
export interface VersionResponse {
    version: string
}

async function getJSON<T>(path: string): Promise<T> {
    const response = await fetch(path)
    if (!response.ok) {
        throw new Error(`GET ${path}: ${response.status}`)
    }
    return (await response.json()) as T
}

export function fetchHealth(): Promise<HealthResponse> {
    return getJSON<HealthResponse>('/api/v1/health')
}

export function fetchVersion(): Promise<VersionResponse> {
    return getJSON<VersionResponse>('/api/v1/version')
}
