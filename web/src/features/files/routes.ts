// files/routes 是文件浏览页的 deep link 构造器：Sources / Jobs 列表的
// 名称列入口与 FilesPage 的 URL query 契约（tab / source / job / path）
// 收敛在一处，页面与浏览器各入口共享同一实现。
export function remoteFilesPath(sourceId: string, path = '/'): string {
    const params = new URLSearchParams({
        tab: 'remote',
        source: sourceId,
        path,
    })
    return `/files?${params}`
}

export function localFilesPath(jobId: string, path = '/'): string {
    const params = new URLSearchParams({
        tab: 'local',
        job: jobId,
        path,
    })
    return `/files?${params}`
}
