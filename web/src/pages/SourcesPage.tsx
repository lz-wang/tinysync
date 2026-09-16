import {
    Alert,
    Box,
    Button,
    Card,
    CardContent,
    Chip,
    CircularProgress,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import {
    listSources,
    type S3Config,
    type SFTPConfig,
    type SourceResponse,
    testSource,
    type WebDAVConfig,
} from '../api'
import DeleteSourceDialog from '../features/sources/DeleteSourceDialog'
import SourceDialog from '../features/sources/SourceDialog'

// TestState 是单个 Source 的连接测试状态。
type TestState =
    | { status: 'testing' }
    | { status: 'done'; ok: boolean; latency: number; error?: string }

// SourcesPage 提供 Source 管理界面：创建、编辑、连接测试与删除。
export default function SourcesPage() {
    const [sources, setSources] = useState<SourceResponse[] | null>(null)
    const [loadError, setLoadError] = useState<string | null>(null)
    const [testStates, setTestStates] = useState<Record<string, TestState>>({})
    const [dialogOpen, setDialogOpen] = useState(false)
    const [editing, setEditing] = useState<SourceResponse | null>(null)
    const [deleting, setDeleting] = useState<SourceResponse | null>(null)

    const reload = useCallback(async () => {
        const list = await listSources()
        setSources(list)
    }, [])

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
                    setLoadError(e instanceof Error ? e.message : String(e))
                }
            }
        }
        void load()
        return () => {
            cancelled = true
        }
    }, [])

    async function handleTest(id: string) {
        setTestStates(prev => ({ ...prev, [id]: { status: 'testing' } }))
        try {
            const result = await testSource(id)
            setTestStates(prev => ({
                ...prev,
                [id]: {
                    status: 'done',
                    ok: result.ok,
                    latency: result.latency_ms,
                    error: result.error,
                },
            }))
        } catch (e) {
            setTestStates(prev => ({
                ...prev,
                [id]: {
                    status: 'done',
                    ok: false,
                    latency: 0,
                    error: e instanceof Error ? e.message : String(e),
                },
            }))
        }
    }

    function handleSaved(saved: SourceResponse) {
        setDialogOpen(false)
        setEditing(null)
        // Source 配置已变化（endpoint / username / password），旧的连接
        // 测试结果不再有效，恢复为 Not tested。
        setTestStates(prev => {
            if (!(saved.id in prev)) {
                return prev
            }
            const next = { ...prev }
            delete next[saved.id]
            return next
        })
        reload()
            .then(() => setLoadError(null))
            .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
    }

    function handleDeleted(id: string) {
        setDeleting(null)
        setSources(prev => (prev === null ? prev : prev.filter(s => s.id !== id)))
        setTestStates(prev => {
            if (!(id in prev)) {
                return prev
            }
            const next = { ...prev }
            delete next[id]
            return next
        })
    }

    return (
        <Stack spacing={2}>
            <Card variant="outlined">
                <CardContent>
                    <Stack spacing={2}>
                        <Box
                            sx={{
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'space-between',
                            }}
                        >
                            <Typography variant="h5" component="h1">
                                Sources
                            </Typography>
                            <Button
                                variant="contained"
                                onClick={() => {
                                    setEditing(null)
                                    setDialogOpen(true)
                                }}
                            >
                                Add Source
                            </Button>
                        </Box>
                        {loadError !== null && <Alert severity="error">{loadError}</Alert>}
                        {sources === null && loadError === null ? (
                            <CircularProgress size={24} aria-label="加载中" />
                        ) : sources !== null ? (
                            <SourceTable
                                sources={sources}
                                testStates={testStates}
                                onTest={id => void handleTest(id)}
                                onEdit={source => {
                                    setEditing(source)
                                    setDialogOpen(true)
                                }}
                                onDelete={source => setDeleting(source)}
                            />
                        ) : null}
                    </Stack>
                </CardContent>
            </Card>
            <SourceDialog
                open={dialogOpen}
                source={editing}
                onClose={() => {
                    setDialogOpen(false)
                    setEditing(null)
                }}
                onSaved={handleSaved}
            />
            <DeleteSourceDialog
                source={deleting}
                onClose={() => setDeleting(null)}
                onDeleted={handleDeleted}
            />
        </Stack>
    )
}

function SourceTable({
    sources,
    testStates,
    onTest,
    onEdit,
    onDelete,
}: {
    sources: SourceResponse[]
    testStates: Record<string, TestState>
    onTest: (id: string) => void
    onEdit: (source: SourceResponse) => void
    onDelete: (source: SourceResponse) => void
}) {
    if (sources.length === 0) {
        return (
            <Typography variant="body2" color="text.secondary">
                No sources configured yet. Click “Add Source” to connect a WebDAV, S3 or SFTP
                server.
            </Typography>
        )
    }
    return (
        <TableContainer>
            <Table size="small">
                <TableHead>
                    <TableRow>
                        <TableCell>Name</TableCell>
                        <TableCell>Type</TableCell>
                        <TableCell>Location</TableCell>
                        <TableCell align="right">Credential</TableCell>
                        <TableCell align="right">Enabled</TableCell>
                        <TableCell>Connection</TableCell>
                        <TableCell align="right">Actions</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {sources.map(source => (
                        <TableRow key={source.id}>
                            <TableCell>{source.name}</TableCell>
                            <TableCell>{source.type}</TableCell>
                            <TableCell sx={{ fontFamily: 'monospace' }}>
                                {locationSummary(source)}
                            </TableCell>
                            <TableCell align="right">{credentialSummary(source)}</TableCell>
                            <TableCell align="right">
                                <Chip
                                    label={source.enabled ? 'On' : 'Off'}
                                    color={source.enabled ? 'success' : 'default'}
                                    size="small"
                                />
                            </TableCell>
                            <TableCell>
                                <TestCell state={testStates[source.id]} />
                            </TableCell>
                            <TableCell align="right">
                                <Stack
                                    direction="row"
                                    spacing={0.5}
                                    sx={{ justifyContent: 'flex-end' }}
                                >
                                    <Button
                                        size="small"
                                        disabled={testStates[source.id]?.status === 'testing'}
                                        onClick={() => onTest(source.id)}
                                    >
                                        Test
                                    </Button>
                                    <Button size="small" onClick={() => onEdit(source)}>
                                        Edit
                                    </Button>
                                    <Button
                                        size="small"
                                        color="error"
                                        onClick={() => onDelete(source)}
                                    >
                                        Delete
                                    </Button>
                                </Stack>
                            </TableCell>
                        </TableRow>
                    ))}
                </TableBody>
            </Table>
        </TableContainer>
    )
}

// locationSummary 按协议生成远端位置摘要。SourceConfig 是按 type
// 判别的 union，前端以 type 显式选择配置形态。
function locationSummary(source: SourceResponse): string {
    const config = source.config
    switch (source.type) {
        case 'webdav': {
            const cfg = config as WebDAVConfig
            return cfg.endpoint ?? ''
        }
        case 's3': {
            const cfg = config as S3Config
            const host = urlHost(cfg.endpoint ?? '')
            const prefix = cfg.prefix ? `/${cfg.prefix}` : ''
            return `${host}/${cfg.bucket}${prefix}`
        }
        case 'sftp': {
            const cfg = config as SFTPConfig
            return `${cfg.host}:${cfg.port ?? 22}${cfg.remote_root}`
        }
    }
}

// urlHost 从 endpoint URL 提取 host（含端口）；无 URL 时回退原文。
function urlHost(endpoint: string): string {
    if (endpoint === '') {
        return 'aws'
    }
    try {
        return new URL(endpoint).host
    } catch {
        return endpoint
    }
}

// credentialSummary 汇总各协议 secret 状态。
function credentialSummary(source: SourceResponse): string {
    const state = source.credential_state
    const configured =
        (state.webdav?.password_set ?? false) ||
        (state.s3?.secret_key_set ?? false) ||
        (state.sftp?.password_set ?? false) ||
        (state.sftp?.private_key_set ?? false)
    if (!configured) {
        return source.type === 'webdav' ? 'Anonymous' : 'Not set'
    }
    return 'Configured'
}

function TestCell({ state }: { state: TestState | undefined }) {
    if (state === undefined) {
        return (
            <Typography variant="caption" color="text.secondary">
                Not tested
            </Typography>
        )
    }
    if (state.status === 'testing') {
        return <CircularProgress size={16} aria-label="Testing connection" />
    }
    if (state.ok) {
        return <Chip label={`Success: ${state.latency} ms`} color="success" size="small" />
    }
    return (
        <Box sx={{ maxWidth: 260 }}>
            <Chip label="Failed" color="error" size="small" />
            {state.error !== undefined && (
                <Typography variant="caption" color="error" sx={{ display: 'block', mt: 0.5 }}>
                    {state.error}
                </Typography>
            )}
        </Box>
    )
}
