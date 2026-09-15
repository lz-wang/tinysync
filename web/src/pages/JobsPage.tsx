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
import { useCallback, useEffect, useRef, useState } from 'react'
import {
    fetchJobStatus,
    type JobResponse,
    listJobs,
    listSources,
    type RunStatusResponse,
    runJob,
    type SourceResponse,
} from '../api'
import DeleteJobDialog from '../features/jobs/DeleteJobDialog'
import JobDialog from '../features/jobs/JobDialog'

// pollIntervalMs 是运行中的状态轮询间隔；运行记录只存内存，
// 每次轮询都是轻量请求。v0.3 全局同一时刻至多一个运行。
const pollIntervalMs = 1500

// formatBytes 把字节数转为人类可读摘要。
function formatBytes(bytes: number): string {
    if (bytes < 1024) {
        return `${bytes} B`
    }
    const units = ['KB', 'MB', 'GB', 'TB']
    let value = bytes
    let unit = 'B'
    for (const next of units) {
        if (value < 1024) {
            break
        }
        value /= 1024
        unit = next
    }
    return `${value.toFixed(1)} ${unit}`
}

// JobsPage 提供 Sync Job 管理界面：创建、编辑、删除，以及
// Run Now 手动运行与 1.5s 轮询的实时状态。
export default function JobsPage() {
    const [jobs, setJobs] = useState<JobResponse[] | null>(null)
    const [sources, setSources] = useState<SourceResponse[]>([])
    const [loadError, setLoadError] = useState<string | null>(null)
    const [dialogOpen, setDialogOpen] = useState(false)
    const [editing, setEditing] = useState<JobResponse | null>(null)
    const [deleting, setDeleting] = useState<JobResponse | null>(null)
    const [runStates, setRunStates] = useState<Record<string, RunStatusResponse>>({})
    // pollTimer 是当前运行任务的轮询定时器；v0.3 全局单运行，
    // 任一时刻至多存在一个进行中的轮询。
    const pollTimer = useRef<number | null>(null)

    const reload = useCallback(async () => {
        // Source 列表供编辑器选择；Source 加载失败不阻塞 Job 列表展示。
        const results = await Promise.allSettled([listJobs(), listSources()])
        const nextJobs = results[0].status === 'fulfilled' ? results[0].value : null
        const nextSources = results[1].status === 'fulfilled' ? results[1].value : []
        setSources(nextSources)
        return { nextJobs, sourcesFailed: results[1].status === 'rejected' }
    }, [])

    useEffect(() => {
        let cancelled = false
        async function load() {
            try {
                const { nextJobs } = await reload()
                if (!cancelled) {
                    setJobs(nextJobs)
                }
                // 拉取每个 Job 的最近运行状态，展示上次结果。
                if (nextJobs !== null) {
                    const statuses = await Promise.allSettled(
                        nextJobs.map(job => fetchJobStatus(job.id)),
                    )
                    if (!cancelled) {
                        setRunStates(prev => {
                            const next = { ...prev }
                            nextJobs.forEach((job, i) => {
                                if (statuses[i].status === 'fulfilled') {
                                    next[job.id] = statuses[i].value
                                }
                            })
                            return next
                        })
                    }
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
    }, [reload])

    // 卸载时清理轮询定时器。
    useEffect(
        () => () => {
            if (pollTimer.current !== null) {
                window.clearInterval(pollTimer.current)
            }
        },
        [],
    )

    const startPolling = useCallback((jobId: string) => {
        if (pollTimer.current !== null) {
            window.clearInterval(pollTimer.current)
        }
        pollTimer.current = window.setInterval(() => {
            void fetchJobStatus(jobId)
                .then(status => {
                    setRunStates(prev => ({ ...prev, [jobId]: status }))
                    if (status.state !== 'running' && pollTimer.current !== null) {
                        window.clearInterval(pollTimer.current)
                        pollTimer.current = null
                    }
                })
                .catch(() => {
                    // 单次轮询失败不终止跟踪，下一轮重试。
                })
        }, pollIntervalMs)
    }, [])

    async function handleRun(job: JobResponse) {
        try {
            await runJob(job.id)
            setRunStates(prev => ({
                ...prev,
                [job.id]: { state: 'running', stats: prev[job.id]?.stats ?? emptyStats() },
            }))
            startPolling(job.id)
        } catch (e) {
            setLoadError(e instanceof Error ? e.message : String(e))
        }
    }

    function handleSaved(_saved: JobResponse) {
        setDialogOpen(false)
        setEditing(null)
        reload()
            .then(({ nextJobs }) => {
                setJobs(nextJobs)
                setLoadError(null)
            })
            .catch((e: unknown) => setLoadError(e instanceof Error ? e.message : String(e)))
    }

    function handleDeleted(id: string) {
        setDeleting(null)
        setJobs(prev => (prev === null ? prev : prev.filter(j => j.id !== id)))
        setRunStates(prev => {
            if (!(id in prev)) {
                return prev
            }
            const next = { ...prev }
            delete next[id]
            return next
        })
    }

    const sourceName = (sourceId: string): string => {
        const found = sources.find(s => s.id === sourceId)
        return found?.name ?? sourceId
    }

    // v0.3 全局单运行：任一 Job 运行中时全部 Run 按钮禁用，
    // 避免必然失败的 409 请求。
    const anyRunning = Object.values(runStates).some(state => state.state === 'running')

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
                                Jobs
                            </Typography>
                            <Button
                                variant="contained"
                                onClick={() => {
                                    setEditing(null)
                                    setDialogOpen(true)
                                }}
                            >
                                Add Job
                            </Button>
                        </Box>
                        {loadError !== null && <Alert severity="error">{loadError}</Alert>}
                        {jobs === null && loadError === null ? (
                            <CircularProgress size={24} aria-label="加载中" />
                        ) : jobs !== null ? (
                            <JobTable
                                jobs={jobs}
                                sourceName={sourceName}
                                runStates={runStates}
                                anyRunning={anyRunning}
                                onRun={job => void handleRun(job)}
                                onEdit={job => {
                                    setEditing(job)
                                    setDialogOpen(true)
                                }}
                                onDelete={job => setDeleting(job)}
                            />
                        ) : null}
                    </Stack>
                </CardContent>
            </Card>
            <JobDialog
                open={dialogOpen}
                job={editing}
                sources={sources}
                onClose={() => {
                    setDialogOpen(false)
                    setEditing(null)
                }}
                onSaved={handleSaved}
            />
            <DeleteJobDialog
                job={deleting}
                onClose={() => setDeleting(null)}
                onDeleted={handleDeleted}
            />
        </Stack>
    )
}

// emptyStats 是运行中状态的零值统计。
function emptyStats() {
    return {
        files_total: 0,
        files_created: 0,
        files_updated: 0,
        files_deleted: 0,
        files_skipped: 0,
        bytes_transferred: 0,
    }
}

function JobTable({
    jobs,
    sourceName,
    runStates,
    anyRunning,
    onRun,
    onEdit,
    onDelete,
}: {
    jobs: JobResponse[]
    sourceName: (sourceId: string) => string
    runStates: Record<string, RunStatusResponse>
    anyRunning: boolean
    onRun: (job: JobResponse) => void
    onEdit: (job: JobResponse) => void
    onDelete: (job: JobResponse) => void
}) {
    if (jobs.length === 0) {
        return (
            <Typography variant="body2" color="text.secondary">
                No jobs configured yet. Click “Add Job” to set up a sync from a source.
            </Typography>
        )
    }
    return (
        <TableContainer>
            <Table size="small">
                <TableHead>
                    <TableRow>
                        <TableCell>Name</TableCell>
                        <TableCell>Source</TableCell>
                        <TableCell>Remote Root</TableCell>
                        <TableCell>Local Root</TableCell>
                        <TableCell>Mode</TableCell>
                        <TableCell align="right">Enabled</TableCell>
                        <TableCell>Run</TableCell>
                        <TableCell align="right">Actions</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {jobs.map(job => {
                        // 运行中的 Job 禁用 Edit / Delete：后端同样以 409
                        // 拒绝，避免传输中配置变更或删除产生状态竞争。
                        const running = runStates[job.id]?.state === 'running'
                        return (
                            <TableRow key={job.id}>
                                <TableCell>{job.name}</TableCell>
                                <TableCell>{sourceName(job.source_id)}</TableCell>
                                <TableCell sx={{ fontFamily: 'monospace' }}>
                                    {job.remote_root}
                                </TableCell>
                                <TableCell sx={{ fontFamily: 'monospace' }}>
                                    {job.local_root}
                                </TableCell>
                                <TableCell>
                                    <Chip
                                        label={job.mode === 'mirror' ? 'Mirror' : 'Copy'}
                                        color={job.mode === 'mirror' ? 'warning' : 'default'}
                                        size="small"
                                    />
                                </TableCell>
                                <TableCell align="right">
                                    <Chip
                                        label={job.enabled ? 'On' : 'Off'}
                                        color={job.enabled ? 'success' : 'default'}
                                        size="small"
                                    />
                                </TableCell>
                                <TableCell>
                                    <RunCell
                                        job={job}
                                        status={runStates[job.id]}
                                        anyRunning={anyRunning}
                                        onRun={() => onRun(job)}
                                    />
                                </TableCell>
                                <TableCell align="right">
                                    <Stack
                                        direction="row"
                                        spacing={0.5}
                                        sx={{ justifyContent: 'flex-end' }}
                                    >
                                        <Button
                                            size="small"
                                            disabled={running}
                                            onClick={() => onEdit(job)}
                                        >
                                            Edit
                                        </Button>
                                        <Button
                                            size="small"
                                            color="error"
                                            disabled={running}
                                            onClick={() => onDelete(job)}
                                        >
                                            Delete
                                        </Button>
                                    </Stack>
                                </TableCell>
                            </TableRow>
                        )
                    })}
                </TableBody>
            </Table>
        </TableContainer>
    )
}

// RunCell 展示单个 Job 的运行控制与最近状态：
// Run Now 按钮、运行中 spinner、成功统计摘要或失败原因。
function RunCell({
    job,
    status,
    anyRunning,
    onRun,
}: {
    job: JobResponse
    status: RunStatusResponse | undefined
    anyRunning: boolean
    onRun: () => void
}) {
    const running = status?.state === 'running'
    const canRun = job.enabled && !anyRunning && !running
    return (
        <Stack spacing={0.5} sx={{ minWidth: 220 }}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                <Button size="small" variant="outlined" disabled={!canRun} onClick={onRun}>
                    Run Now
                </Button>
                {running && <CircularProgress size={16} aria-label="运行中" />}
            </Box>
            <RunStateCaption status={status} />
        </Stack>
    )
}

function RunStateCaption({ status }: { status: RunStatusResponse | undefined }) {
    if (status === undefined || status.state === 'idle') {
        return (
            <Typography variant="caption" color="text.secondary">
                Not run yet
            </Typography>
        )
    }
    if (status.state === 'running') {
        return (
            <Typography variant="caption" color="text.secondary">
                Running…
            </Typography>
        )
    }
    if (status.state === 'failed') {
        return (
            <Box>
                <Chip label="Failed" color="error" size="small" />
                {status.error !== undefined && (
                    <Typography
                        variant="caption"
                        color="error"
                        sx={{ display: 'block', mt: 0.5, wordBreak: 'break-word' }}
                    >
                        {status.error}
                    </Typography>
                )}
            </Box>
        )
    }
    const stats = status.stats
    return (
        <Box>
            <Chip label="Succeeded" color="success" size="small" />
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
                {`${stats.files_total} total · ${stats.files_created} new · ${stats.files_updated} updated · ${stats.files_deleted} deleted · ${stats.files_skipped} skipped · ${formatBytes(stats.bytes_transferred)}`}
            </Typography>
        </Box>
    )
}
