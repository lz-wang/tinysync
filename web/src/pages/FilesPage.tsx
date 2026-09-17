import { Box, Tab, Tabs, Typography } from '@mui/material'
import { useState } from 'react'
import LocalFileBrowser from '../features/files/LocalFileBrowser'
import RemoteFileBrowser from '../features/files/RemoteFileBrowser'

// FilesPage 是文件浏览页：Remote（三协议远端浏览）与 Local（以
// Job.LocalRoot 为 namespace 的本地浏览）两个视图。
export default function FilesPage() {
    const [tab, setTab] = useState<'remote' | 'local'>('remote')

    return (
        <Box>
            <Typography variant="h5" component="h1" sx={{ mb: 2, fontWeight: 600 }}>
                Files
            </Typography>
            <Tabs
                value={tab}
                onChange={(_, value: 'remote' | 'local') => setTab(value)}
                sx={{ mb: 2 }}
            >
                <Tab value="remote" label="Remote" />
                <Tab value="local" label="Local" />
            </Tabs>
            {tab === 'remote' ? <RemoteFileBrowser /> : <LocalFileBrowser />}
        </Box>
    )
}
