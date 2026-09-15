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
import { type JobResponse, listJobs, listSources, type SourceResponse } from '../api'
import DeleteJobDialog from '../features/jobs/DeleteJobDialog'
import JobDialog from '../features/jobs/JobDialog'

// JobsPage 提供 Sync Job 管理界面：创建、编辑与删除。
// 运行控制（Run Now / 状态）由后续迭代提供。
export default function JobsPage() {
    const [jobs, setJobs] = useState<JobResponse[] | null>(null)
    const [sources, setSources] = useState<SourceResponse[]>([])
    const [loadError, setLoadError] = useState<string | null>(null)
    const [dialogOpen, setDialogOpen] = useState(false)
    const [editing, setEditing] = useState<JobResponse | null>(null)
    const [deleting, setDeleting] = useState<JobResponse | null>(null)

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

function JobTable({
    jobs,
    sourceName,
    onEdit,
    onDelete,
}: {
    jobs: JobResponse[]
    sourceName: (sourceId: string) => string
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
                        <TableCell align="right">Actions</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {jobs.map(job => (
                        <TableRow key={job.id}>
                            <TableCell>{job.name}</TableCell>
                            <TableCell>{sourceName(job.source_id)}</TableCell>
                            <TableCell sx={{ fontFamily: 'monospace' }}>
                                {job.remote_root}
                            </TableCell>
                            <TableCell sx={{ fontFamily: 'monospace' }}>{job.local_root}</TableCell>
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
                            <TableCell align="right">
                                <Stack
                                    direction="row"
                                    spacing={0.5}
                                    sx={{ justifyContent: 'flex-end' }}
                                >
                                    <Button size="small" onClick={() => onEdit(job)}>
                                        Edit
                                    </Button>
                                    <Button
                                        size="small"
                                        color="error"
                                        onClick={() => onDelete(job)}
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
