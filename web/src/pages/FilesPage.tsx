import { Box, Tab, Tabs } from '@mui/material'
import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { usePageTitle } from '../app/usePageTitle'
import LocalFileBrowser from '../features/files/LocalFileBrowser'
import RemoteFileBrowser from '../features/files/RemoteFileBrowser'
import SharePanel from '../features/files/SharePanel'

// FilesPage 是文件浏览页：Remote（三协议远端浏览）、Local（以
// Job.LocalRoot 为 namespace 的本地浏览）与 Shared（共享策略管理）
// 三个视图。
export default function FilesPage() {
    const [searchParams, setSearchParams] = useSearchParams()
    const queryTab = searchParams.get('tab')
    const tab = queryTab === 'local' || queryTab === 'shared' ? queryTab : 'remote'
    const viewTitle = tab === 'local' ? '本地文件' : tab === 'shared' ? '共享' : '远端文件'
    const path = searchParams.get('path') ?? '/'
    usePageTitle(
        tab !== 'shared' && path !== '/'
            ? `${path} · ${viewTitle} · 文件管理`
            : `${viewTitle} · 文件管理`,
    )
    // shareNonce 在新共享创建后递增，驱动 Shared 列表刷新。
    const [shareNonce, setShareNonce] = useState(0)

    return (
        <Box sx={{ pb: 2 }}>
            <Tabs
                value={tab}
                onChange={(_, value: 'remote' | 'local' | 'shared') => {
                    const next = new URLSearchParams()
                    next.set('tab', value)
                    setSearchParams(next)
                }}
                sx={{ mb: 2 }}
            >
                <Tab value="remote" label="远端文件" />
                <Tab value="local" label="本地文件" />
                <Tab value="shared" label="共享" />
            </Tabs>
            {tab === 'remote' && <RemoteFileBrowser />}
            {tab === 'local' && <LocalFileBrowser onChanged={() => setShareNonce(n => n + 1)} />}
            {tab === 'shared' && <SharePanel key={shareNonce} />}
        </Box>
    )
}
