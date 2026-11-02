import RefreshOutlinedIcon from '@mui/icons-material/RefreshOutlined'
import SearchOutlinedIcon from '@mui/icons-material/SearchOutlined'
import {
    Box,
    Card,
    IconButton,
    InputAdornment,
    MenuItem,
    Paper,
    Stack,
    Tab,
    TablePagination,
    Tabs,
    TextField,
    Tooltip,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { type ImageListItemResponse, type JobResponse, listImages, type OciPlatform } from '../api'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import {
    type ImageSortField,
    ImageStatusTable,
    type SortDirection,
} from '../features/images/ImageStatusTable'
import { RemoteImagesPanel } from '../features/images/RemoteImagesPanel'
import JobDialog from '../features/jobs/JobDialog'

// ImagesPage 是镜像管理页：两个子 tab——「同步状态」列出 oci 任务的源
// 镜像与最近执行（行 / 批量立即同步、添加到任务），「远程镜像」提供
// 远程搜索 / tags / inspect / 平台 inspect（oci-sync Insights 移植）。
export default function ImagesPage() {
    usePageTitle('镜像管理')
    const toast = useToast()
    const [tab, setTab] = useState<'status' | 'remote'>('status')
    const [images, setImages] = useState<ImageListItemResponse[]>([])
    const [loading, setLoading] = useState(true)
    const [search, setSearch] = useState('')
    const [jobFilter, setJobFilter] = useState('')
    const [targetFilter, setTargetFilter] = useState('')
    const [resultFilter, setResultFilter] = useState('')
    const [sortField, setSortField] = useState<ImageSortField | null>(null)
    const [sortDirection, setSortDirection] = useState<SortDirection>('asc')
    const [page, setPage] = useState(0)
    const [rowsPerPage, setRowsPerPage] = useState(15)
    // JobDialog 预填：从镜像列表或远程镜像「添加到任务」进入。
    const [dialogOpen, setDialogOpen] = useState(false)
    const [dialogJob, setDialogJob] = useState<JobResponse | null>(null)
    const [initialImages, setInitialImages] = useState<string[]>([])
    const [initialPlatforms, setInitialPlatforms] = useState<OciPlatform[]>([])

    const reload = useCallback(async () => {
        setLoading(true)
        try {
            setImages(await listImages())
        } catch (e) {
            toast.error(e instanceof Error ? e.message : String(e))
        } finally {
            setLoading(false)
        }
    }, [toast])
    useEffect(() => {
        if (tab === 'status') {
            void reload()
        }
    }, [tab, reload])

    const jobOptions = useMemo(
        () => [...new Set(images.map(item => item.job_name))].sort(),
        [images],
    )
    const targetOptions = useMemo(
        () => [...new Set(images.flatMap(item => item.dst_registries))].sort(),
        [images],
    )
    const resultOptions = useMemo(
        () => [...new Set(images.map(item => item.last_status ?? ''))].sort(),
        [images],
    )
    const filtered = useMemo(() => {
        const term = search.trim().toLowerCase()
        return images.filter(item => {
            const matchesSearch =
                term === '' ||
                item.source.toLowerCase().includes(term) ||
                item.job_name.toLowerCase().includes(term) ||
                item.dst_registries.some(registry => registry.toLowerCase().includes(term))
            return (
                matchesSearch &&
                (jobFilter === '' || item.job_name === jobFilter) &&
                (targetFilter === '' || item.dst_registries.includes(targetFilter)) &&
                (resultFilter === '' || (item.last_status ?? '') === resultFilter)
            )
        })
    }, [images, jobFilter, resultFilter, search, targetFilter])
    const sorted = useMemo(() => {
        if (sortField === null) {
            return filtered
        }
        const valueFor = (item: ImageListItemResponse): string | number => {
            switch (sortField) {
                case 'source':
                    return item.source
                case 'job_name':
                    return item.job_name
                case 'target':
                    return item.dst_registries.join(', ')
                case 'last_sync_time':
                    return item.last_sync_time !== undefined && item.last_sync_time !== ''
                        ? new Date(item.last_sync_time).getTime()
                        : 0
                case 'result':
                    return item.last_status ?? ''
            }
        }
        return [...filtered].sort((left, right) => {
            const a = valueFor(left)
            const b = valueFor(right)
            const comparison =
                typeof a === 'number' && typeof b === 'number'
                    ? a - b
                    : String(a).localeCompare(String(b))
            return sortDirection === 'asc' ? comparison : -comparison
        })
    }, [filtered, sortDirection, sortField])
    const paged = useMemo(
        () => sorted.slice(page * rowsPerPage, page * rowsPerPage + rowsPerPage),
        [page, rowsPerPage, sorted],
    )

    const handleSort = (field: ImageSortField) => {
        if (sortField === field) {
            if (sortDirection === 'asc') {
                setSortDirection('desc')
            } else {
                setSortField(null)
                setSortDirection('asc')
            }
        } else {
            setSortField(field)
            setSortDirection('asc')
        }
        setPage(0)
    }

    // openAddToTask 从同步状态表进入：预填源镜像（repo:tag 全引用）。
    const openAddToTask = (items: ImageListItemResponse[]) => {
        setDialogJob(null)
        setInitialImages([...new Set(items.map(item => item.source))])
        setInitialPlatforms([])
        setDialogOpen(true)
    }

    // openRemoteAddToTask 从远程镜像面板进入：预填 repo:tag 与所选平台，
    // job 非 null 时为并入既有任务（JobDialog 合并 src_images）。
    const openRemoteAddToTask = (input: {
        images: string[]
        platforms: OciPlatform[]
        job: JobResponse | null
    }) => {
        setDialogJob(input.job)
        setInitialImages(input.images)
        setInitialPlatforms(input.platforms)
        setDialogOpen(true)
    }

    return (
        <Stack spacing={2}>
            <Tabs value={tab} onChange={(_, value: 'status' | 'remote') => setTab(value)}>
                <Tab value="status" label="同步状态" />
                <Tab value="remote" label="远程镜像" />
            </Tabs>
            {tab === 'status' ? (
                <Stack spacing={2}>
                    <Stack
                        direction={{ xs: 'column', md: 'row' }}
                        spacing={1}
                        sx={{ alignItems: { md: 'center' } }}
                    >
                        <TextField
                            size="small"
                            placeholder="搜索镜像 / 任务 / 目标 Registry"
                            value={search}
                            onChange={event => {
                                setSearch(event.target.value)
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
                            sx={{ width: { xs: '100%', md: 360 } }}
                        />
                        <TextField
                            select
                            size="small"
                            label="任务"
                            value={jobFilter}
                            onChange={event => {
                                setJobFilter(event.target.value)
                                setPage(0)
                            }}
                            sx={{ minWidth: 150 }}
                        >
                            <MenuItem value="">全部</MenuItem>
                            {jobOptions.map(name => (
                                <MenuItem key={name} value={name}>
                                    {name}
                                </MenuItem>
                            ))}
                        </TextField>
                        <TextField
                            select
                            size="small"
                            label="目标"
                            value={targetFilter}
                            onChange={event => {
                                setTargetFilter(event.target.value)
                                setPage(0)
                            }}
                            sx={{ minWidth: 150 }}
                        >
                            <MenuItem value="">全部</MenuItem>
                            {targetOptions.map(name => (
                                <MenuItem key={name} value={name}>
                                    {name}
                                </MenuItem>
                            ))}
                        </TextField>
                        <TextField
                            select
                            size="small"
                            label="结果"
                            value={resultFilter}
                            onChange={event => {
                                setResultFilter(event.target.value)
                                setPage(0)
                            }}
                            sx={{ minWidth: 120 }}
                        >
                            <MenuItem value="">全部</MenuItem>
                            {resultOptions.map(result => (
                                <MenuItem key={result || 'unsynced'} value={result}>
                                    {result === ''
                                        ? '未同步'
                                        : result === 'success'
                                          ? '成功'
                                          : result === 'skipped'
                                            ? '跳过'
                                            : result === 'failed'
                                              ? '失败'
                                              : result}
                                </MenuItem>
                            ))}
                        </TextField>
                        <Box sx={{ flexGrow: 1 }} />
                        <Tooltip title="刷新">
                            <span>
                                <IconButton
                                    onClick={() => void reload()}
                                    disabled={loading}
                                    sx={{ border: 1, borderColor: 'divider' }}
                                    aria-label="刷新镜像列表"
                                >
                                    <RefreshOutlinedIcon fontSize="small" />
                                </IconButton>
                            </span>
                        </Tooltip>
                    </Stack>
                    <Paper variant="outlined" sx={{ display: 'flex', flexDirection: 'column' }}>
                        <ImageStatusTable
                            images={paged}
                            loading={loading}
                            onChanged={() => void reload()}
                            onAddToTask={openAddToTask}
                            sortField={sortField}
                            sortDirection={sortDirection}
                            onSort={handleSort}
                        />
                        <TablePagination
                            component="div"
                            count={filtered.length}
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
            ) : (
                <RemoteImagesPanel onAddToTask={openRemoteAddToTask} />
            )}
            <JobDialog
                open={dialogOpen}
                job={dialogJob}
                sources={[]}
                initialOciImages={initialImages}
                initialOciPlatforms={initialPlatforms}
                onClose={() => {
                    setDialogOpen(false)
                    setDialogJob(null)
                    setInitialImages([])
                    setInitialPlatforms([])
                }}
                onSaved={() => {
                    setDialogOpen(false)
                    setDialogJob(null)
                    setInitialImages([])
                    setInitialPlatforms([])
                    toast.success(dialogJob === null ? '任务已创建' : '镜像已并入任务')
                    if (tab === 'status') {
                        void reload()
                    }
                }}
            />
            {images.length === 0 && !loading && tab === 'status' && (
                <Card variant="outlined">
                    <Typography variant="body2" color="text.secondary" sx={{ p: 2 }}>
                        提示：目标 Registry 在「同步源」页的 Registry 分区维护；远程仓库探索
                        见「远程镜像」标签。
                    </Typography>
                </Card>
            )}
        </Stack>
    )
}
