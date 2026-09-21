import { Alert, Box, CircularProgress, Typography } from '@mui/material'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { listRemoteFiles, listSources, remoteFileDownloadURL, type SourceResponse } from '../../api'
import { useToast } from '../../app/toast'
import ReadOnlyFileManager from './ReadOnlyFileManager'

// RemoteFileBrowser 把 Source 与当前目录写入 URL；刷新或复制链接后仍能
// 回到同一远端位置。远端协议没有本地 managed 标记，表格中的同步状态
// 列由通用管理器省略，其余无法取得的值使用 - 占位。
export default function RemoteFileBrowser() {
    const toast = useToast()
    const [sources, setSources] = useState<SourceResponse[] | null>(null)
    const [loadError, setLoadError] = useState<string | null>(null)
    const [searchParams, setSearchParams] = useSearchParams()
    const sourceId = searchParams.get('source') ?? ''
    const path = searchParams.get('path') ?? '/'
    const showHidden = searchParams.get('hidden') === 'true'

    useEffect(() => {
        let cancelled = false
        listSources()
            .then(list => {
                if (!cancelled) setSources(list)
            })
            .catch((error: unknown) => {
                if (!cancelled) {
                    const message = error instanceof Error ? error.message : String(error)
                    setLoadError(message)
                    toast.error(message)
                }
            })
        return () => {
            cancelled = true
        }
    }, [toast])

    useEffect(() => {
        if (
            sources === null ||
            sources.length === 0 ||
            sources.some(source => source.id === sourceId)
        )
            return
        const next = new URLSearchParams(searchParams)
        next.set('source', sources[0].id)
        next.set('path', '/')
        setSearchParams(next, { replace: true })
    }, [searchParams, setSearchParams, sourceId, sources])

    const resources = useMemo(
        () =>
            (sources ?? []).map(source => ({
                id: source.id,
                label: `${source.name}（${source.type}）`,
            })),
        [sources],
    )
    const updateLocation = (
        nextSourceId: string,
        nextPath: string,
        nextShowHidden = showHidden,
    ) => {
        const next = new URLSearchParams(searchParams)
        next.set('source', nextSourceId)
        next.set('path', nextPath)
        if (nextShowHidden) next.set('hidden', 'true')
        else next.delete('hidden')
        setSearchParams(next)
    }
    const load = useCallback(
        async (targetPath: string, cursor?: string) => {
            const page = await listRemoteFiles(sourceId, targetPath, 100, cursor, showHidden)
            return { entries: page.entries, nextCursor: page.next_cursor }
        },
        [showHidden, sourceId],
    )

    if (sources === null && loadError === null) {
        return (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                <CircularProgress />
            </Box>
        )
    }
    // 初始加载失败：详情已在 toast 中展示，区域保留简短失败文案。
    if (loadError !== null)
        return (
            <Typography variant="body2" color="text.secondary">
                同步源加载失败。
            </Typography>
        )
    if (sources?.length === 0)
        return <Alert severity="info">尚未创建同步源；请先在“同步源”页面添加。</Alert>

    return (
        <ReadOnlyFileManager
            downloadURL={entryPath => remoteFileDownloadURL(sourceId, entryPath)}
            load={load}
            onPathChange={nextPath => updateLocation(sourceId, nextPath)}
            onResourceChange={nextSourceId => updateLocation(nextSourceId, '/')}
            onShowHiddenChange={nextShowHidden => updateLocation(sourceId, path, nextShowHidden)}
            path={path}
            resourceId={sourceId}
            resourceLabel="同步源"
            resources={resources}
            showHidden={showHidden}
        />
    )
}
