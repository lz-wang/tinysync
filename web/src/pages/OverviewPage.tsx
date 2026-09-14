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
                    <Typography variant="h5" component="h1">
                        Overview
                    </Typography>
                    {status === null ? (
                        <CircularProgress size={24} aria-label="加载中" />
                    ) : (
                        <>
                            <Row label="Service status">
                                {status.healthy === null ? (
                                    <Chip label="Unreachable" color="error" size="small" />
                                ) : (
                                    <Chip
                                        label={status.healthy ? 'Running' : 'Degraded'}
                                        color={status.healthy ? 'success' : 'warning'}
                                        size="small"
                                    />
                                )}
                            </Row>
                            <Row label="Version">
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
