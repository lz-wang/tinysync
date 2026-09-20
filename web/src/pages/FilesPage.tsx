import { Box, Tab, Tabs } from '@mui/material'
import { useSearchParams } from 'react-router-dom'
import { usePageTitle } from '../app/usePageTitle'
import LocalFileBrowser from '../features/files/LocalFileBrowser'
import RemoteFileBrowser from '../features/files/RemoteFileBrowser'

// FilesPage 是文件浏览页：Remote（三协议远端浏览）与 Local（以
// Job.LocalRoot 为 namespace 的本地浏览）两个视图；共享策略在独立的
// /shares 管理页维护。
export default function FilesPage() {
    const [searchParams, setSearchParams] = useSearchParams()
    const queryTab = searchParams.get('tab')
    const tab = queryTab === 'remote' ? 'remote' : 'local'
    const viewTitle = tab === 'local' ? '本地文件' : '远端文件'
    const path = searchParams.get('path') ?? '/'
    usePageTitle(path !== '/' ? `${path} · ${viewTitle} · 文件管理` : `${viewTitle} · 文件管理`)

    return (
        <Box sx={{ pb: 2 }}>
            <Tabs
                value={tab}
                onChange={(_, value: 'remote' | 'local') => {
                    const next = new URLSearchParams()
                    next.set('tab', value)
                    setSearchParams(next)
                }}
                sx={{ mb: 2 }}
            >
                <Tab value="local" label="本地文件" />
                <Tab value="remote" label="远端文件" />
            </Tabs>
            {tab === 'local' && <LocalFileBrowser />}
            {tab === 'remote' && <RemoteFileBrowser />}
        </Box>
    )
}
