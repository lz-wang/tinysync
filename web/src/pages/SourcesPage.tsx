import {
    Card,
    CardContent,
    Chip,
    CircularProgress,
    Paper,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { listSources, type SourceResponse } from '../api'

// SourcesPage 展示远端 Source 列表：名称、类型、端点与凭据状态。
export default function SourcesPage() {
    const [sources, setSources] = useState<SourceResponse[] | null>(null)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        let cancelled = false
        async function load() {
            try {
                const list = await listSources()
                if (!cancelled) {
                    setSources(list)
                }
            } catch (e) {
                if (!cancelled) {
                    setError(e instanceof Error ? e.message : String(e))
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
                        Sources
                    </Typography>
                    {error !== null && (
                        <Typography variant="body2" color="error">
                            {error}
                        </Typography>
                    )}
                    {sources === null ? (
                        <CircularProgress size={24} aria-label="加载中" />
                    ) : (
                        <SourceTable sources={sources} />
                    )}
                </Stack>
            </CardContent>
        </Card>
    )
}

function SourceTable({ sources }: { sources: SourceResponse[] }) {
    if (sources.length === 0) {
        return (
            <Typography variant="body2" color="text.secondary">
                No sources configured yet.
            </Typography>
        )
    }
    return (
        <TableContainer component={Paper} variant="outlined">
            <Table size="small">
                <TableHead>
                    <TableRow>
                        <TableCell>Name</TableCell>
                        <TableCell>Type</TableCell>
                        <TableCell>Endpoint</TableCell>
                        <TableCell align="right">Password</TableCell>
                        <TableCell align="right">Enabled</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {sources.map(source => (
                        <TableRow key={source.id}>
                            <TableCell>{source.name}</TableCell>
                            <TableCell>{source.type}</TableCell>
                            <TableCell sx={{ fontFamily: 'monospace' }}>
                                {source.endpoint}
                            </TableCell>
                            <TableCell align="right">
                                {source.password_set ? 'Configured' : 'Anonymous'}
                            </TableCell>
                            <TableCell align="right">
                                <Chip
                                    label={source.enabled ? 'On' : 'Off'}
                                    color={source.enabled ? 'success' : 'default'}
                                    size="small"
                                />
                            </TableCell>
                        </TableRow>
                    ))}
                </TableBody>
            </Table>
        </TableContainer>
    )
}
