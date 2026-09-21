import AddOutlinedIcon from '@mui/icons-material/AddOutlined'
import BoltIcon from '@mui/icons-material/Bolt'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlined'
import EditOutlinedIcon from '@mui/icons-material/EditOutlined'
import SearchOutlinedIcon from '@mui/icons-material/SearchOutlined'
import {
    Box,
    Button,
    Checkbox,
    Chip,
    CircularProgress,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    IconButton,
    InputAdornment,
    MenuItem,
    Paper,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TablePagination,
    TableRow,
    TableSortLabel,
    TextField,
    Tooltip,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link as RouterLink } from 'react-router-dom'
import {
    deleteJob,
    fetchJobStatus,
    type JobResponse,
    listJobs,
    listSources,
    type RunStatusResponse,
    runJob,
    type SourceResponse,
} from '../api'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import { formatDateTime } from '../features/history/shared'
import DeleteJobDialog from '../features/jobs/DeleteJobDialog'
import JobDialog from '../features/jobs/JobDialog'

const pollIntervalMs = 1500
type SortField = 'name' | 'source' | 'mode' | 'enabled' | 'lastRun' | 'nextRun'

// JobsPage 将任务配置、最近一次运行和常用操作收敛到一个可筛选的基础表格。
export default function JobsPage() {
    usePageTitle('同步任务')
    const toast = useToast()
    const [jobs, setJobs] = useState<JobResponse[] | null>(null)
    const [sources, setSources] = useState<SourceResponse[]>([])
    const [loadError, setLoadError] = useState<string | null>(null)
    const [dialogOpen, setDialogOpen] = useState(false)
    const [editing, setEditing] = useState<JobResponse | null>(null)
    const [deleting, setDeleting] = useState<JobResponse | null>(null)
    const [batchDeleting, setBatchDeleting] = useState<JobResponse[] | null>(null)
    const [runStates, setRunStates] = useState<Record<string, RunStatusResponse>>({})
    const runStatesRef = useRef(runStates)
    useEffect(() => {
        runStatesRef.current = runStates
    }, [runStates])
    const reload = useCallback(async () => {
        const results = await Promise.allSettled([listJobs(), listSources()])
        const nextJobs = results[0].status === 'fulfilled' ? results[0].value : null
        setSources(results[1].status === 'fulfilled' ? results[1].value : [])
        if (nextJobs !== null) {
            const statuses = await Promise.allSettled(nextJobs.map(job => fetchJobStatus(job.id)))
            setRunStates(
                Object.fromEntries(
                    nextJobs.flatMap((job, index) =>
                        statuses[index].status === 'fulfilled'
                            ? [[job.id, statuses[index].value]]
                            : [],
                    ),
                ),
            )
        }
        setJobs(nextJobs)
    }, [])
    useEffect(() => {
        void reload().catch(e => {
            setLoadError(e instanceof Error ? e.message : String(e))
            toast.error(e instanceof Error ? e.message : String(e))
        })
    }, [reload, toast])
    useEffect(() => {
        const timer = window.setInterval(() => {
            for (const [id, state] of Object.entries(runStatesRef.current))
                if (state.state === 'running')
                    void fetchJobStatus(id)
                        .then(status => setRunStates(prev => ({ ...prev, [id]: status })))
                        .catch(() => undefined)
        }, pollIntervalMs)
        return () => window.clearInterval(timer)
    }, [])
    const handleRun = async (job: JobResponse) => {
        try {
            await runJob(job.id)
            setRunStates(prev => ({ ...prev, [job.id]: { state: 'running', stats: emptyStats() } }))
        } catch (e) {
            toast.error(e instanceof Error ? e.message : String(e))
        }
    }
    const handleBatchDelete = async () => {
        if (batchDeleting === null) return
        try {
            const results = await Promise.allSettled(batchDeleting.map(job => deleteJob(job.id)))
            const failed = results.filter(result => result.status === 'rejected').length
            if (failed > 0) throw new Error(`${failed} 个任务删除失败`)
            setJobs(
                prev =>
                    prev?.filter(job => !batchDeleting.some(item => item.id === job.id)) ?? null,
            )
            setBatchDeleting(null)
        } catch (e) {
            toast.error(e instanceof Error ? e.message : String(e))
        }
    }
    return (
        <Stack
            spacing={2}
            sx={{
                height: { xs: 'calc(100dvh - 96px)', md: 'calc(100dvh - 112px)' },
                minHeight: 420,
            }}
        >
            {jobs === null && loadError === null ? (
                <CircularProgress size={24} />
            ) : jobs !== null ? (
                <JobTable
                    jobs={jobs}
                    sources={sources}
                    runStates={runStates}
                    onAdd={() => {
                        setEditing(null)
                        setDialogOpen(true)
                    }}
                    onRun={job => void handleRun(job)}
                    onEdit={job => {
                        setEditing(job)
                        setDialogOpen(true)
                    }}
                    onDelete={setDeleting}
                    onBatchDelete={setBatchDeleting}
                />
            ) : (
                // 初始加载失败：详情已在 toast 中展示，区域保留简短失败文案。
                <Typography variant="body2" color="text.secondary">
                    同步任务加载失败。
                </Typography>
            )}
            <JobDialog
                open={dialogOpen}
                job={editing}
                sources={sources}
                onClose={() => {
                    setDialogOpen(false)
                    setEditing(null)
                }}
                onSaved={() => {
                    setDialogOpen(false)
                    setEditing(null)
                    void reload()
                }}
            />
            <DeleteJobDialog
                job={deleting}
                onClose={() => setDeleting(null)}
                onDeleted={id => {
                    setDeleting(null)
                    setJobs(prev => prev?.filter(job => job.id !== id) ?? null)
                }}
            />
            <Dialog
                open={batchDeleting !== null}
                onClose={() => setBatchDeleting(null)}
                maxWidth="xs"
                fullWidth
            >
                <DialogTitle>删除已选同步任务？</DialogTitle>
                <DialogContent>
                    <Typography>
                        确认删除 {batchDeleting?.length ?? 0} 个任务？已下载到本地的文件会保留。
                    </Typography>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => setBatchDeleting(null)}>取消</Button>
                    <Button
                        color="error"
                        variant="contained"
                        onClick={() => void handleBatchDelete()}
                    >
                        删除
                    </Button>
                </DialogActions>
            </Dialog>
        </Stack>
    )
}

function JobTable({
    jobs,
    sources,
    runStates,
    onAdd,
    onRun,
    onEdit,
    onDelete,
    onBatchDelete,
}: {
    jobs: JobResponse[]
    sources: SourceResponse[]
    runStates: Record<string, RunStatusResponse>
    onAdd: () => void
    onRun: (job: JobResponse) => void
    onEdit: (job: JobResponse) => void
    onDelete: (job: JobResponse) => void
    onBatchDelete: (jobs: JobResponse[]) => void
}) {
    const [selected, setSelected] = useState<string[]>([])
    const [query, setQuery] = useState('')
    const [sourceFilter, setSourceFilter] = useState('all')
    const [statusFilter, setStatusFilter] = useState('all')
    const [sort, setSort] = useState<{ field: SortField; direction: 'asc' | 'desc' }>({
        field: 'name',
        direction: 'asc',
    })
    const [page, setPage] = useState(0)
    const [rowsPerPage, setRowsPerPage] = useState(15)
    const sourceName = useCallback(
        (sourceId: string) => sources.find(source => source.id === sourceId)?.name ?? sourceId,
        [sources],
    )
    const visible = useMemo(
        () =>
            jobs
                .filter(job => {
                    const state = runStates[job.id]?.state ?? 'idle'
                    const needle = query.trim().toLowerCase()
                    return (
                        (needle === '' ||
                            `${job.name} ${sourceName(job.source_id)} ${job.mode}`
                                .toLowerCase()
                                .includes(needle)) &&
                        (sourceFilter === 'all' || job.source_id === sourceFilter) &&
                        (statusFilter === 'all' || state === statusFilter)
                    )
                })
                .sort((a, b) => {
                    const value = (job: JobResponse) => {
                        const state = runStates[job.id]
                        switch (sort.field) {
                            case 'source':
                                return sourceName(job.source_id)
                            case 'lastRun':
                                return state?.finished_at ?? state?.started_at ?? ''
                            case 'nextRun':
                                return state?.next_run_at ?? ''
                            default:
                                return String(job[sort.field])
                        }
                    }
                    return (
                        value(a).localeCompare(value(b), 'zh-CN', { numeric: true }) *
                        (sort.direction === 'asc' ? 1 : -1)
                    )
                }),
        [jobs, query, runStates, sort, sourceFilter, sourceName, statusFilter],
    )
    const paginated = useMemo(
        () => visible.slice(page * rowsPerPage, page * rowsPerPage + rowsPerPage),
        [page, rowsPerPage, visible],
    )
    useEffect(() => {
        const lastPage = Math.max(0, Math.ceil(visible.length / rowsPerPage) - 1)
        if (page > lastPage) setPage(lastPage)
    }, [page, rowsPerPage, visible.length])
    const chosen = jobs.filter(job => selected.includes(job.id))
    const allVisible = paginated.length > 0 && paginated.every(job => selected.includes(job.id))
    const someVisible = paginated.some(job => selected.includes(job.id))
    const toggleSort = (field: SortField) => {
        setSort(prev => ({
            field,
            direction: prev.field === field && prev.direction === 'asc' ? 'desc' : 'asc',
        }))
        setPage(0)
    }
    const header = (label: string, field: SortField) => (
        <TableSortLabel
            active={sort.field === field}
            direction={sort.field === field ? sort.direction : 'asc'}
            onClick={() => toggleSort(field)}
        >
            {label}
        </TableSortLabel>
    )
    return (
        <Stack spacing={2} sx={{ flex: 1, minHeight: 0 }}>
            <Stack
                direction={{ xs: 'column', md: 'row' }}
                spacing={1}
                sx={{ alignItems: { md: 'center' } }}
            >
                <Button variant="contained" startIcon={<AddOutlinedIcon />} onClick={onAdd}>
                    添加任务
                </Button>
                {chosen.length > 0 && (
                    <>
                        <Typography variant="body2">已选 {chosen.length} 项</Typography>
                        <Button
                            size="small"
                            variant="outlined"
                            startIcon={<BoltIcon />}
                            disabled={chosen.some(
                                job => !job.enabled || runStates[job.id]?.state === 'running',
                            )}
                            onClick={() => chosen.forEach(onRun)}
                        >
                            批量运行
                        </Button>
                        <Button
                            size="small"
                            color="error"
                            variant="outlined"
                            startIcon={<DeleteOutlineIcon />}
                            disabled={chosen.some(job => runStates[job.id]?.state === 'running')}
                            onClick={() => onBatchDelete(chosen)}
                        >
                            批量删除
                        </Button>
                    </>
                )}
                <Box sx={{ flexGrow: 1 }} />
                <TextField
                    size="small"
                    placeholder="搜索任务或同步源"
                    value={query}
                    onChange={event => {
                        setQuery(event.target.value)
                        setPage(0)
                    }}
                    slotProps={{
                        input: {
                            startAdornment: (
                                <InputAdornment position="start">
                                    <SearchOutlinedIcon fontSize="small" />
                                </InputAdornment>
                            ),
                        },
                    }}
                />
                <TextField
                    select
                    size="small"
                    label="同步源"
                    value={sourceFilter}
                    onChange={event => {
                        setSourceFilter(event.target.value)
                        setPage(0)
                    }}
                    sx={{ minWidth: 130 }}
                >
                    <MenuItem value="all">全部同步源</MenuItem>
                    {sources.map(source => (
                        <MenuItem key={source.id} value={source.id}>
                            {source.name}
                        </MenuItem>
                    ))}
                </TextField>
                <TextField
                    select
                    size="small"
                    label="结果"
                    value={statusFilter}
                    onChange={event => {
                        setStatusFilter(event.target.value)
                        setPage(0)
                    }}
                    sx={{ minWidth: 110 }}
                >
                    <MenuItem value="all">全部结果</MenuItem>
                    <MenuItem value="idle">未运行</MenuItem>
                    <MenuItem value="running">运行中</MenuItem>
                    <MenuItem value="succeeded">成功</MenuItem>
                    <MenuItem value="failed">失败</MenuItem>
                    <MenuItem value="skipped">已跳过</MenuItem>
                </TextField>
            </Stack>
            <Paper
                variant="outlined"
                sx={{
                    flex: 1,
                    width: '100%',
                    minWidth: 0,
                    minHeight: 0,
                    display: 'flex',
                    flexDirection: 'column',
                }}
            >
                <TableContainer sx={{ flexGrow: 1, minHeight: 0, overflow: 'auto' }}>
                    <Table size="small" sx={{ minWidth: 860 }}>
                        <TableHead>
                            <TableRow>
                                <TableCell padding="checkbox">
                                    <Checkbox
                                        size="small"
                                        checked={allVisible}
                                        indeterminate={someVisible && !allVisible}
                                        onChange={event =>
                                            setSelected(
                                                event.target.checked
                                                    ? [
                                                          ...new Set([
                                                              ...selected,
                                                              ...paginated.map(job => job.id),
                                                          ]),
                                                      ]
                                                    : selected.filter(
                                                          id =>
                                                              !paginated.some(job => job.id === id),
                                                      ),
                                            )
                                        }
                                    />
                                </TableCell>
                                <TableCell>{header('名称', 'name')}</TableCell>
                                <TableCell>{header('同步源', 'source')}</TableCell>
                                <TableCell>{header('模式', 'mode')}</TableCell>
                                <TableCell>{header('状态', 'enabled')}</TableCell>
                                <TableCell>{header('最近运行', 'lastRun')}</TableCell>
                                <TableCell>{header('下次运行', 'nextRun')}</TableCell>
                                <TableCell align="center">操作</TableCell>
                            </TableRow>
                        </TableHead>
                        <TableBody>
                            {paginated.map(job => {
                                const state = runStates[job.id]
                                const running = state?.state === 'running'
                                return (
                                    <TableRow
                                        key={job.id}
                                        hover
                                        selected={selected.includes(job.id)}
                                    >
                                        <TableCell padding="checkbox">
                                            <Checkbox
                                                size="small"
                                                checked={selected.includes(job.id)}
                                                onChange={event =>
                                                    setSelected(prev =>
                                                        event.target.checked
                                                            ? [...prev, job.id]
                                                            : prev.filter(id => id !== job.id),
                                                    )
                                                }
                                            />
                                        </TableCell>
                                        <TableCell>
                                            <Typography variant="body2" sx={{ fontWeight: 500 }}>
                                                {job.name}
                                            </Typography>
                                        </TableCell>
                                        <TableCell>{sourceName(job.source_id)}</TableCell>
                                        <TableCell>
                                            <Chip
                                                label={job.mode === 'mirror' ? '镜像' : '复制'}
                                                color={
                                                    job.mode === 'mirror' ? 'warning' : 'default'
                                                }
                                                size="small"
                                            />
                                        </TableCell>
                                        <TableCell>
                                            <Chip
                                                label={job.enabled ? '已启用' : '已停用'}
                                                color={job.enabled ? 'success' : 'default'}
                                                size="small"
                                            />
                                        </TableCell>
                                        <TableCell>
                                            <LastRunTime status={state} />
                                        </TableCell>
                                        <TableCell>
                                            <Typography variant="body2" color="text.secondary">
                                                {formatDateTime(state?.next_run_at)}
                                            </Typography>
                                        </TableCell>
                                        <TableCell align="center">
                                            <Stack
                                                direction="row"
                                                spacing={0.25}
                                                sx={{ justifyContent: 'center' }}
                                            >
                                                <Tooltip title="立即运行">
                                                    <span>
                                                        <IconButton
                                                            size="small"
                                                            color="primary"
                                                            aria-label={`运行 ${job.name}`}
                                                            disabled={!job.enabled || running}
                                                            onClick={() => onRun(job)}
                                                        >
                                                            {running ? (
                                                                <CircularProgress size={18} />
                                                            ) : (
                                                                <BoltIcon fontSize="small" />
                                                            )}
                                                        </IconButton>
                                                    </span>
                                                </Tooltip>
                                                <Tooltip title="编辑">
                                                    <span>
                                                        <IconButton
                                                            size="small"
                                                            aria-label={`编辑 ${job.name}`}
                                                            disabled={running}
                                                            onClick={() => onEdit(job)}
                                                        >
                                                            <EditOutlinedIcon fontSize="small" />
                                                        </IconButton>
                                                    </span>
                                                </Tooltip>
                                                <Tooltip title="删除">
                                                    <span>
                                                        <IconButton
                                                            size="small"
                                                            color="error"
                                                            aria-label={`删除 ${job.name}`}
                                                            disabled={running}
                                                            onClick={() => onDelete(job)}
                                                        >
                                                            <DeleteOutlineIcon fontSize="small" />
                                                        </IconButton>
                                                    </span>
                                                </Tooltip>
                                            </Stack>
                                        </TableCell>
                                    </TableRow>
                                )
                            })}
                            {paginated.length === 0 && (
                                <TableRow>
                                    <TableCell colSpan={8}>
                                        <Box sx={{ py: 6, textAlign: 'center' }}>
                                            <Typography color="text.secondary">
                                                暂无匹配的同步任务
                                            </Typography>
                                        </Box>
                                    </TableCell>
                                </TableRow>
                            )}
                        </TableBody>
                    </Table>
                </TableContainer>
                <TablePagination
                    component="div"
                    count={visible.length}
                    page={page}
                    rowsPerPage={rowsPerPage}
                    rowsPerPageOptions={[10, 15, 20, 50, 100]}
                    labelRowsPerPage="每页"
                    onPageChange={(_, nextPage) => setPage(nextPage)}
                    onRowsPerPageChange={event => {
                        setRowsPerPage(Number.parseInt(event.target.value, 10))
                        setPage(0)
                    }}
                />
            </Paper>
        </Stack>
    )
}
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

// LastRunTime 用时间承载“最近运行”语义；文字颜色同时传达最终结果。
function LastRunTime({ status }: { status: RunStatusResponse | undefined }) {
    if (status?.run_id === undefined) {
        return (
            <Typography variant="caption" color="text.secondary">
                尚未运行
            </Typography>
        )
    }
    const time = status.finished_at ?? status.started_at
    return (
        <Tooltip title={`${runStateLabel(status.state)}，点击查看运行详情`}>
            <Box
                component={RouterLink}
                to={`/history/${status.run_id}`}
                sx={{
                    color: runStateColor(status.state),
                    display: 'inline-flex',
                    fontSize: '0.875rem',
                    textDecoration: 'none',
                    '&:hover': { textDecoration: 'underline' },
                }}
            >
                {formatShortDateTime(time)}
            </Box>
        </Tooltip>
    )
}

// formatShortDateTime 为列表保留足以辨识的本地月日与分钟，避免占用过宽列。
function formatShortDateTime(value?: string): string {
    if (value === undefined || value === '') {
        return '—'
    }
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) {
        return value
    }
    const pad = (part: number) => String(part).padStart(2, '0')
    return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function runStateColor(state: RunStatusResponse['state']): string {
    switch (state) {
        case 'succeeded':
            return 'success.main'
        case 'failed':
            return 'error.main'
        case 'skipped':
            return 'warning.main'
        case 'running':
            return 'info.main'
        default:
            return 'text.secondary'
    }
}

function runStateLabel(state: RunStatusResponse['state']): string {
    switch (state) {
        case 'succeeded':
            return '运行成功'
        case 'failed':
            return '运行失败'
        case 'skipped':
            return '运行已跳过'
        case 'running':
            return '正在运行'
        default:
            return '尚未运行'
    }
}
