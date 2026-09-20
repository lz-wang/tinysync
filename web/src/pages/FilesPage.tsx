import { Box, Tab, Tabs } from '@mui/material'
import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { usePageTitle } from '../app/usePageTitle'
import LocalFileBrowser from '../features/files/LocalFileBrowser'
import PublishedPanel from '../features/files/PublishedPanel'
import RemoteFileBrowser from '../features/files/RemoteFileBrowser'

// FilesPage 是文件浏览页：Remote（三协议远端浏览）、Local（以
// Job.LocalRoot 为 namespace 的本地浏览）与 Published（发布策略
// 管理）三个视图。
export default function FilesPage() {
    const [searchParams, setSearchParams] = useSearchParams()
    const queryTab = searchParams.get('tab')
    const tab = queryTab === 'local' || queryTab === 'published' ? queryTab : 'remote'
    const viewTitle = tab === 'local' ? '本地文件' : tab === 'published' ? '已发布' : '远端文件'
    const path = searchParams.get('path') ?? '/'
    usePageTitle(
        tab !== 'published' && path !== '/'
            ? `${path} · ${viewTitle} · 文件管理`
            : `${viewTitle} · 文件管理`,
    )
    // publishNonce 在新策略创建后递增，驱动 Published 列表刷新。
    const [publishNonce, setPublishNonce] = useState(0)

    return (
        <Box sx={{ pb: 2 }}>
            <Tabs
                value={tab}
                onChange={(_, value: 'remote' | 'local' | 'published') => {
                    const next = new URLSearchParams()
                    next.set('tab', value)
                    setSearchParams(next)
                }}
                sx={{ mb: 2 }}
            >
                <Tab value="remote" label="远端文件" />
                <Tab value="local" label="本地文件" />
                <Tab value="published" label="已发布" />
            </Tabs>
            {tab === 'remote' && <RemoteFileBrowser />}
            {tab === 'local' && <LocalFileBrowser onChanged={() => setPublishNonce(n => n + 1)} />}
            {tab === 'published' && <PublishedPanel key={publishNonce} />}
        </Box>
    )
}
