import { Box, CircularProgress, MenuItem, TextField } from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { listRemoteFiles, listSources, remoteFileDownloadURL, type SourceResponse } from '../../api'
import FileBrowser, { PAGE_SIZE } from './FileBrowser'

// RemoteFileBrowser 是远端文件浏览器：Source 选择 + 分页目录浏览 +
// 流式下载。Source 未就绪时提示先在 Sources 页创建。
export default function RemoteFileBrowser() {
    const [sources, setSources] = useState<SourceResponse[] | null>(null)
    const [sourceId, setSourceId] = useState('')
    const [loadError, setLoadError] = useState<string | null>(null)

    useEffect(() => {
        let cancelled = false
        listSources()
            .then(list => {
                if (cancelled) {
                    return
                }
                setSources(list)
                if (list.length > 0) {
                    setSourceId(list[0].id)
                }
            })
            .catch((err: unknown) => {
                if (!cancelled) {
                    setLoadError(err instanceof Error ? err.message : String(err))
                }
            })
        return () => {
            cancelled = true
        }
    }, [])

    // load 以 sourceId 为闭包依赖：切换 Source 时 FileBrowser 的
    // useEffect 会重置目录状态。
    const load = useCallback(
        async (path: string, cursor: string | null) => {
            const page = await listRemoteFiles(sourceId, path, PAGE_SIZE, cursor ?? undefined)
            return { entries: page.entries, nextCursor: page.next_cursor }
        },
        [sourceId],
    )

    if (sources === null && loadError === null) {
        return (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                <CircularProgress />
            </Box>
        )
    }
    if (loadError !== null) {
        return <Box sx={{ color: 'error.main' }}>加载同步源失败：{loadError}</Box>
    }
    if (sources !== null && sources.length === 0) {
        return <Box sx={{ color: 'text.secondary' }}>尚未创建同步源；请先在“同步源”页面添加。</Box>
    }

    return (
        <Box>
            <TextField
                select
                size="small"
                label="同步源"
                value={sourceId}
                onChange={event => setSourceId(event.target.value)}
                sx={{ minWidth: 280, mb: 2 }}
            >
                {sources?.map(source => (
                    <MenuItem key={source.id} value={source.id}>
                        {source.name}（{source.type}）
                    </MenuItem>
                ))}
            </TextField>
            {sourceId !== '' && (
                <FileBrowser
                    load={load}
                    downloadURL={path => remoteFileDownloadURL(sourceId, path)}
                />
            )}
        </Box>
    )
}
