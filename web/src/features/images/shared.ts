// 镜像管理域的小型共享助手：状态徽标映射与镜像引用解析
//（oci-sync utils/imageRef 移植，纯函数便于单测）。

// lastStatusChipProps 把 GET /images 的 last_status（success | skipped |
// failed，空为从未运行）映射为 Chip 文案与颜色。
export function lastStatusChipProps(status: string | undefined): {
    label: string
    color: 'success' | 'warning' | 'error' | 'default'
} {
    switch (status) {
        case 'success':
            return { label: '成功', color: 'success' }
        case 'skipped':
            return { label: '跳过', color: 'warning' }
        case 'failed':
            return { label: '失败', color: 'error' }
        default:
            return { label: '从未运行', color: 'default' }
    }
}

// splitRepositoryAndTag 拆分 repo 与 tag（最后一个冒号在最后一个斜杠
// 之后才视为 tag）。
export function splitRepositoryAndTag(input: string): { repo: string; tag?: string } {
    const value = input.trim()
    const lastSlash = value.lastIndexOf('/')
    const lastColon = value.lastIndexOf(':')
    if (lastColon > lastSlash) {
        const repo = value.slice(0, lastColon)
        const tag = value.slice(lastColon + 1)
        if (repo !== '' && tag !== '') {
            return { repo, tag }
        }
    }
    return { repo: value }
}

// normalizePlatformValue 归一化平台字段（小写、去空白、unknown 视为空）。
export function normalizePlatformValue(value?: string): string {
    const normalized = value?.trim().toLowerCase() ?? ''
    return normalized === 'unknown' ? '' : normalized
}

// platformKey 把平台摘要压缩为比较键。
export function platformKey(platform: { os?: string; architecture?: string; variant?: string }) {
    return [
        normalizePlatformValue(platform.os),
        normalizePlatformValue(platform.architecture),
        normalizePlatformValue(platform.variant),
    ].join('/')
}

// platformLabel 把平台摘要渲染为 os/arch[/variant] 文本。
export function platformLabel(platform: {
    os?: string
    architecture?: string
    variant?: string
}): string {
    const os = normalizePlatformValue(platform.os) || '-'
    const architecture = normalizePlatformValue(platform.architecture) || '-'
    const variant = normalizePlatformValue(platform.variant)
    return variant !== '' ? `${os}/${architecture}/${variant}` : `${os}/${architecture}`
}

// formatSizeMB 把字节数渲染为 MB 文本（无效返回空串）。
export function formatSizeMB(size?: number): string {
    if (!size || size <= 0) {
        return ''
    }
    return `${(size / (1024 * 1024)).toFixed(2)} MB`
}
