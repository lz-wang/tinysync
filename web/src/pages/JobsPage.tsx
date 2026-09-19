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
    type JobMode,
    type JobResponse,
    listJobs,
    listSources,
    type RunStatusResponse,
    runJob,
    type ScheduleSpec,
    type SourceResponse,
} from '../api'
import DeleteJobDialog from '../features/jobs/DeleteJobDialog'
import JobDialog from '../features/jobs/JobDialog'

// pollIntervalMs 是运行中的状态轮询间隔；状态读取走持久化历史，
// 轮询仅用于刷新正在进行的运行。
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

// formatDateTime 把 RFC3339 时间渲染为本地可读形式。
function formatDateTime(value?: string): string {
    if (value === undefined || value === '') {
        return '—'
    }
    const d = new Date(value)
    if (Number.isNaN(d.getTime())) {
        return value
    }
    return d.toLocaleString()
}

// formatSchedule 把调度配置转为人类可读摘要。
function formatSchedule(schedule: ScheduleSpec): string {
    switch (schedule.type) {
        case 'once':
            return `单次 · ${formatDateTime(schedule.at)}`
        case 'interval':
            return `每隔 ${schedule.every ?? '?'}`
        case 'cron':
            return schedule.timezone !== undefined && schedule.timezone !== ''
                ? `Cron · ${schedule.expression} (${schedule.timezone})`
                : `Cron · ${schedule.expression ?? '?'}（本机时区）`
        default:
            return '手动'
    }
}

// JobsPage 提供 Sync Job 管理界面：创建、编辑、删除（含调度配置），
// Run Now 手动运行与 1.5s 轮询的实时状态；Last Run / Next Run 来自
// 持久化历史，刷新与重启不丢失。
export default function JobsPage() {
    const [jobs, setJobs] = useState<JobResponse[] | null>(null)
    const [sources, setSources] = useState<SourceResponse[]>([])
    const [loadError, setLoadError] = useState<string | null>(null)
    const [dialogOpen, setDialogOpen] = useState(false)
    const [editing, setEditing] = useState<JobResponse | null>(null)
    const [deleting, setDeleting] = useState<JobResponse | null>(null)
    const [runStates, setRunStates] = useState<Record<string, RunStatusResponse>>({})
    // runStatesRef 供轮询定时器读取最新状态，避免反复重建定时器。
    const runStatesRef = useRef(runStates)

    useEffect(() => {
        runStatesRef.current = runStates
    }, [runStates])

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
                // 拉取每个 Job 的最近运行状态（持久化历史）。
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

    // 轮询所有运行中的 Job；v0.4 起多个 Job 可并行运行。
    useEffect(() => {
        const timer = window.setInterval(() => {
            const runningIds = Object.entries(runStatesRef.current)
                .filter(([, status]) => status.state === 'running')
                .map(([id]) => id)
            if (runningIds.length === 0) {
                return
            }
            for (const id of runningIds) {
                void fetchJobStatus(id)
                    .then(status => {
                        setRunStates(prev => ({ ...prev, [id]: status }))
                    })
                    .catch(() => {
                        // 单次轮询失败不终止跟踪，下一轮重试。
                    })
            }
        }, pollIntervalMs)
        return () => window.clearInterval(timer)
    }, [])

    async function handleRun(job: JobResponse) {
        try {
            await runJob(job.id)
            setRunStates(prev => ({
                ...prev,
                [job.id]: { state: 'running', stats: prev[job.id]?.stats ?? emptyStats() },
            }))
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
                            <Button
                                variant="contained"
                                onClick={() => {
                                    setEditing(null)
                                    setDialogOpen(true)
                                }}
                            >
                                添加任务
                            </Button>
                        </Box>
                        {loadError !== null && (
                            <Alert severity="error" onClose={() => setLoadError(null)}>
                                {loadError}
                            </Alert>
                        )}
                        {jobs === null && loadError === null ? (
                            <CircularProgress size={24} aria-label="加载中" />
                        ) : jobs !== null ? (
                            <JobTable
                                jobs={jobs}
                                sourceName={sourceName}
                                runStates={runStates}
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
    onRun,
    onEdit,
    onDelete,
}: {
    jobs: JobResponse[]
    sourceName: (sourceId: string) => string
    runStates: Record<string, RunStatusResponse>
    onRun: (job: JobResponse) => void
    onEdit: (job: JobResponse) => void
    onDelete: (job: JobResponse) => void
}) {
    if (jobs.length === 0) {
        return (
            <Typography variant="body2" color="text.secondary">
                尚未配置同步任务。点击“添加任务”创建同步配置。
            </Typography>
        )
    }
    return (
        <TableContainer>
            <Table size="small">
                <TableHead>
                    <TableRow>
                        <TableCell>名称</TableCell>
                        <TableCell>同步源</TableCell>
                        <TableCell>模式</TableCell>
                        <TableCell>计划</TableCell>
                        <TableCell>最近运行</TableCell>
                        <TableCell>下次运行</TableCell>
                        <TableCell>运行</TableCell>
                        <TableCell align="right">操作</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {jobs.map(job => {
                        // 运行中的 Job 禁用 Edit / Delete：后端同样以 409
                        // 拒绝，避免传输中配置变更或删除产生状态竞争。
                        // 其他 Job 的运行不再影响本 Job 的操作。
                        const running = runStates[job.id]?.state === 'running'
                        return (
                            <TableRow key={job.id}>
                                <TableCell>{job.name}</TableCell>
                                <TableCell>{sourceName(job.source_id)}</TableCell>
                                <TableCell>
                                    <ModeChip mode={job.mode} />
                                </TableCell>
                                <TableCell>
                                    <Stack
                                        direction="row"
                                        spacing={0.5}
                                        sx={{ alignItems: 'center' }}
                                    >
                                        {!job.enabled && (
                                            <Chip label="已停用" size="small" color="default" />
                                        )}
                                        <Typography variant="body2">
                                            {formatSchedule(job.schedule)}
                                        </Typography>
                                    </Stack>
                                </TableCell>
                                <TableCell sx={{ minWidth: 220 }}>
                                    <RunStateCaption status={runStates[job.id]} />
                                </TableCell>
                                <TableCell>
                                    {runStates[job.id]?.next_run_at !== undefined
                                        ? formatDateTime(runStates[job.id].next_run_at)
                                        : '—'}
                                </TableCell>
                                <TableCell>
                                    <RunCell
                                        running={running}
                                        enabled={job.enabled}
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
                                            编辑
                                        </Button>
                                        <Button
                                            size="small"
                                            color="error"
                                            disabled={running}
                                            onClick={() => onDelete(job)}
                                        >
                                            删除
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

// ModeChip 展示同步模式。
function ModeChip({ mode }: { mode: JobMode }) {
    return (
        <Chip
            label={mode === 'mirror' ? '镜像' : '复制'}
            color={mode === 'mirror' ? 'warning' : 'default'}
            size="small"
        />
    )
}

// RunCell 展示单个 Job 的运行控制：Run Now 只受本 Job 运行状态控制，
// 全局容量冲突由请求错误提示呈现。
function RunCell({
    running,
    enabled,
    onRun,
}: {
    running: boolean
    enabled: boolean
    onRun: () => void
}) {
    return (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            <Button size="small" variant="outlined" disabled={!enabled || running} onClick={onRun}>
                立即运行
            </Button>
            {running && <CircularProgress size={16} aria-label="运行中" />}
        </Box>
    )
}

function RunStateCaption({ status }: { status: RunStatusResponse | undefined }) {
    if (status === undefined || status.state === 'idle') {
        return (
            <Typography variant="caption" color="text.secondary">
                尚未运行
            </Typography>
        )
    }
    if (status.state === 'running') {
        return (
            <Typography variant="caption" color="text.secondary">
                运行中…
            </Typography>
        )
    }
    if (status.state === 'skipped') {
        return (
            <Box>
                <Chip label="已跳过" color="default" size="small" />
                {status.error !== undefined && (
                    <Typography
                        variant="caption"
                        color="text.secondary"
                        sx={{ display: 'block', mt: 0.5, wordBreak: 'break-word' }}
                    >
                        {status.error}
                    </Typography>
                )}
            </Box>
        )
    }
    if (status.state === 'failed') {
        return (
            <Box>
                <Chip label="失败" color="error" size="small" />
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
            <Chip label="成功" color="success" size="small" />
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
                {`共 ${stats.files_total} 个 · 新增 ${stats.files_created} · 更新 ${stats.files_updated} · 删除 ${stats.files_deleted} · 跳过 ${stats.files_skipped} · ${formatBytes(stats.bytes_transferred)}`}
            </Typography>
        </Box>
    )
}
