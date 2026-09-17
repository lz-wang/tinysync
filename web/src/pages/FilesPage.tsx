import { Box, Tab, Tabs, Typography } from '@mui/material'
import { useState } from 'react'
import LocalFileBrowser from '../features/files/LocalFileBrowser'
import PublishedPanel from '../features/files/PublishedPanel'
import RemoteFileBrowser from '../features/files/RemoteFileBrowser'

// FilesPage 是文件浏览页：Remote（三协议远端浏览）、Local（以
// Job.LocalRoot 为 namespace 的本地浏览）与 Published（发布策略
// 管理）三个视图。
export default function FilesPage() {
    const [tab, setTab] = useState<'remote' | 'local' | 'published'>('remote')
    // publishNonce 在新策略创建后递增，驱动 Published 列表刷新。
    const [publishNonce, setPublishNonce] = useState(0)

    return (
        <Box>
            <Typography variant="h5" component="h1" sx={{ mb: 2, fontWeight: 600 }}>
                Files
            </Typography>
            <Tabs
                value={tab}
                onChange={(_, value: 'remote' | 'local' | 'published') => setTab(value)}
                sx={{ mb: 2 }}
            >
                <Tab value="remote" label="Remote" />
                <Tab value="local" label="Local" />
                <Tab value="published" label="Published" />
            </Tabs>
            {tab === 'remote' && <RemoteFileBrowser />}
            {tab === 'local' && <LocalFileBrowser onChanged={() => setPublishNonce(n => n + 1)} />}
            {tab === 'published' && <PublishedPanel key={publishNonce} />}
        </Box>
    )
}
