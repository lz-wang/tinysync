import { Box, Card, CardContent, Chip, CircularProgress, Stack, Typography } from '@mui/material'
import { useEffect, useState } from 'react'
import { fetchHealth, fetchVersion } from '../api'

// StatusView 是服务状态快照：health 与 version 的加载结果。
interface StatusView {
    healthy: boolean | null
    version: string | null
    error: string | null
}

// OverviewPage 展示服务健康状态与版本信息。
export default function OverviewPage() {
    const [status, setStatus] = useState<StatusView | null>(null)

    useEffect(() => {
        let cancelled = false
        async function load() {
            try {
                const [health, version] = await Promise.all([fetchHealth(), fetchVersion()])
                if (!cancelled) {
                    setStatus({
                        healthy: health.status === 'ok',
                        version: version.version,
                        error: null,
                    })
                }
            } catch (error) {
                if (!cancelled) {
                    setStatus({
                        healthy: null,
                        version: null,
                        error: error instanceof Error ? error.message : String(error),
                    })
                }
            }
        }
        void load()
        return () => {
            cancelled = true
        }
    }, [])

    return (
        <Card variant="outlined">
            <CardContent>
                <Stack spacing={2}>
                    {status === null ? (
                        <CircularProgress size={24} aria-label="加载中" />
                    ) : (
                        <>
                            <Row label="服务状态">
                                {status.healthy === null ? (
                                    <Chip label="无法连接" color="error" size="small" />
                                ) : (
                                    <Chip
                                        label={status.healthy ? '运行中' : '异常'}
                                        color={status.healthy ? 'success' : 'warning'}
                                        size="small"
                                    />
                                )}
                            </Row>
                            <Row label="版本">
                                <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                                    {status.version ?? '-'}
                                </Typography>
                            </Row>
                            {status.error !== null && (
                                <Typography variant="caption" color="error">
                                    {status.error}
                                </Typography>
                            )}
                        </>
                    )}
                </Stack>
            </CardContent>
        </Card>
    )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
    return (
        <Box
            sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 2 }}
        >
            <Typography variant="body2" color="text.secondary">
                {label}
            </Typography>
            {children}
        </Box>
    )
}
