import AddOutlinedIcon from '@mui/icons-material/AddOutlined'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlined'
import EditOutlinedIcon from '@mui/icons-material/EditOutlined'
import NetworkCheckOutlinedIcon from '@mui/icons-material/NetworkCheckOutlined'
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
import { useCallback, useEffect, useMemo, useState } from 'react'
import {
    deleteSource,
    type GitHubReleaseConfig,
    listSources,
    type S3Config,
    type SFTPConfig,
    type SourceResponse,
    testSource,
    updateSource,
    type WebDAVConfig,
} from '../api'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import DeleteSourceDialog from '../features/sources/DeleteSourceDialog'
import SourceDialog from '../features/sources/SourceDialog'

type SortField = 'name' | 'type' | 'location' | 'enabled'

// SourcesPage 使用原生 MUI Table 管理同步源；操作反馈统一走全局 toast。
export default function SourcesPage() {
    usePageTitle('同步源')
    const toast = useToast()
    const [sources, setSources] = useState<SourceResponse[] | null>(null)
    const [loadError, setLoadError] = useState<string | null>(null)
    const [dialogOpen, setDialogOpen] = useState(false)
    const [editing, setEditing] = useState<SourceResponse | null>(null)
    const [deleting, setDeleting] = useState<SourceResponse | null>(null)
    const [batchDeleting, setBatchDeleting] = useState<SourceResponse[] | null>(null)
    const [testing, setTesting] = useState<string | null>(null)
    const reload = useCallback(async () => setSources(await listSources()), [])
    useEffect(() => {
        void reload().catch(e => {
            setLoadError(e instanceof Error ? e.message : String(e))
            toast.error(e instanceof Error ? e.message : String(e))
        })
    }, [reload, toast])
    async function handleTest(source: SourceResponse) {
        setTesting(source.id)
        try {
            const result = await testSource(source.id)
            if (result.ok) toast.success(`“${source.name}”连接成功，耗时 ${result.latency_ms} ms`)
            else toast.error(`“${source.name}”连接失败：${result.error ?? '未知错误'}`)
        } catch (e) {
            toast.error(e instanceof Error ? e.message : String(e))
        } finally {
            setTesting(null)
        }
    }
    async function handleBatchDelete() {
        if (batchDeleting === null) return
        try {
            await Promise.all(batchDeleting.map(source => deleteSource(source.id)))
            setSources(
                prev =>
                    prev?.filter(source => !batchDeleting.some(item => item.id === source.id)) ??
                    null,
            )
            toast.success(`已删除 ${batchDeleting.length} 个同步源`)
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
            {sources === null && loadError === null ? (
                <CircularProgress size={24} />
            ) : sources !== null ? (
                <SourceTable
                    sources={sources}
                    testing={testing}
                    onAdd={() => {
                        setEditing(null)
                        setDialogOpen(true)
                    }}
                    onTest={source => void handleTest(source)}
                    onEdit={source => {
                        setEditing(source)
                        setDialogOpen(true)
                    }}
                    onDelete={setDeleting}
                    onBatchDelete={setBatchDeleting}
                    onEnabledChanged={async (source, enabled) => {
                        try {
                            const saved = await updateSource(source.id, { enabled })
                            setSources(
                                prev =>
                                    prev?.map(item => (item.id === saved.id ? saved : item)) ??
                                    null,
                            )
                        } catch (e) {
                            toast.error(e instanceof Error ? e.message : String(e))
                        }
                    }}
                />
            ) : (
                // 初始加载失败：详情已在 toast 中展示，区域保留简短失败文案。
                <Typography variant="body2" color="text.secondary">
                    同步源加载失败。
                </Typography>
            )}
            <SourceDialog
                open={dialogOpen}
                source={editing}
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
            <DeleteSourceDialog
                source={deleting}
                onClose={() => setDeleting(null)}
                onDeleted={id => {
                    setDeleting(null)
                    setSources(prev => prev?.filter(source => source.id !== id) ?? null)
                }}
            />
            <Dialog
                open={batchDeleting !== null}
                onClose={() => setBatchDeleting(null)}
                maxWidth="xs"
                fullWidth
            >
                <DialogTitle>删除已选同步源？</DialogTitle>
                <DialogContent>
                    <Typography>
                        确认删除 {batchDeleting?.length ?? 0} 个同步源？不会删除远端文件。
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

function SourceTable({
    sources,
    testing,
    onAdd,
    onTest,
    onEdit,
    onDelete,
    onBatchDelete,
    onEnabledChanged,
}: {
    sources: SourceResponse[]
    testing: string | null
    onAdd: () => void
    onTest: (source: SourceResponse) => void
    onEdit: (source: SourceResponse) => void
    onDelete: (source: SourceResponse) => void
    onBatchDelete: (sources: SourceResponse[]) => void
    onEnabledChanged: (source: SourceResponse, enabled: boolean) => void
}) {
    const [selected, setSelected] = useState<string[]>([])
    const [query, setQuery] = useState('')
    const [type, setType] = useState('all')
    const [status, setStatus] = useState('all')
    const [sort, setSort] = useState<{ field: SortField; direction: 'asc' | 'desc' }>({
        field: 'name',
        direction: 'asc',
    })
    const [page, setPage] = useState(0)
    const [rowsPerPage, setRowsPerPage] = useState(15)
    const visible = useMemo(
        () =>
            sources
                .filter(source => {
                    const needle = query.trim().toLowerCase()
                    return (
                        (needle === '' ||
                            `${source.name} ${source.type} ${locationSummary(source)}`
                                .toLowerCase()
                                .includes(needle)) &&
                        (type === 'all' || source.type === type) &&
                        (status === 'all' || String(source.enabled) === status)
                    )
                })
                .sort((a, b) => {
                    const value = (source: SourceResponse) =>
                        sort.field === 'location'
                            ? locationSummary(source)
                            : String(source[sort.field])
                    return (
                        value(a).localeCompare(value(b), 'zh-CN', { numeric: true }) *
                        (sort.direction === 'asc' ? 1 : -1)
                    )
                }),
        [sources, query, sort, status, type],
    )
    const paginated = useMemo(
        () => visible.slice(page * rowsPerPage, page * rowsPerPage + rowsPerPage),
        [page, rowsPerPage, visible],
    )
    useEffect(() => {
        const lastPage = Math.max(0, Math.ceil(visible.length / rowsPerPage) - 1)
        if (page > lastPage) setPage(lastPage)
    }, [page, rowsPerPage, visible.length])
    const chosen = sources.filter(source => selected.includes(source.id))
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
    const allVisible =
        paginated.length > 0 && paginated.every(source => selected.includes(source.id))
    const someVisible = paginated.some(source => selected.includes(source.id))
    return (
        <Stack spacing={2} sx={{ flex: 1, minHeight: 0 }}>
            <Stack
                direction={{ xs: 'column', md: 'row' }}
                spacing={1}
                sx={{ alignItems: { md: 'center' } }}
            >
                <Button variant="contained" startIcon={<AddOutlinedIcon />} onClick={onAdd}>
                    添加同步源
                </Button>
                {chosen.length > 0 && (
                    <>
                        <Typography variant="body2">已选 {chosen.length} 项</Typography>
                        <Button
                            size="small"
                            color="error"
                            variant="outlined"
                            startIcon={<DeleteOutlineIcon />}
                            onClick={() => onBatchDelete(chosen)}
                        >
                            批量删除
                        </Button>
                    </>
                )}
                <Box sx={{ flexGrow: 1 }} />
                <TextField
                    size="small"
                    placeholder="搜索名称、类型或位置"
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
                    label="类型"
                    value={type}
                    onChange={event => {
                        setType(event.target.value)
                        setPage(0)
                    }}
                    sx={{ minWidth: 110 }}
                >
                    <MenuItem value="all">全部类型</MenuItem>
                    <MenuItem value="webdav">WebDAV</MenuItem>
                    <MenuItem value="s3">S3</MenuItem>
                    <MenuItem value="sftp">SFTP</MenuItem>
                    <MenuItem value="github_release">GitHub Release</MenuItem>
                </TextField>
                <TextField
                    select
                    size="small"
                    label="状态"
                    value={status}
                    onChange={event => {
                        setStatus(event.target.value)
                        setPage(0)
                    }}
                    sx={{ minWidth: 110 }}
                >
                    <MenuItem value="all">全部状态</MenuItem>
                    <MenuItem value="true">已启用</MenuItem>
                    <MenuItem value="false">已停用</MenuItem>
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
                    <Table size="small" sx={{ minWidth: 760 }}>
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
                                                              ...paginated.map(source => source.id),
                                                          ]),
                                                      ]
                                                    : selected.filter(
                                                          id =>
                                                              !paginated.some(
                                                                  source => source.id === id,
                                                              ),
                                                      ),
                                            )
                                        }
                                    />
                                </TableCell>
                                <TableCell>{header('名称', 'name')}</TableCell>
                                <TableCell>{header('类型', 'type')}</TableCell>
                                <TableCell>{header('位置', 'location')}</TableCell>
                                <TableCell>{header('状态', 'enabled')}</TableCell>
                                <TableCell align="center">操作</TableCell>
                            </TableRow>
                        </TableHead>
                        <TableBody>
                            {paginated.map(source => (
                                <TableRow
                                    key={source.id}
                                    hover
                                    selected={selected.includes(source.id)}
                                >
                                    <TableCell padding="checkbox">
                                        <Checkbox
                                            size="small"
                                            checked={selected.includes(source.id)}
                                            onChange={event =>
                                                setSelected(prev =>
                                                    event.target.checked
                                                        ? [...prev, source.id]
                                                        : prev.filter(id => id !== source.id),
                                                )
                                            }
                                        />
                                    </TableCell>
                                    <TableCell>
                                        <Typography variant="body2" sx={{ fontWeight: 500 }}>
                                            {source.name}
                                        </Typography>
                                    </TableCell>
                                    <TableCell>
                                        <Chip label={source.type.toUpperCase()} size="small" />
                                    </TableCell>
                                    <TableCell
                                        sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
                                    >
                                        {locationSummary(source)}
                                    </TableCell>
                                    <TableCell>
                                        <Tooltip title={source.enabled ? '停用' : '启用'}>
                                            <Chip
                                                component="button"
                                                clickable
                                                label={source.enabled ? '已启用' : '已停用'}
                                                color={source.enabled ? 'success' : 'default'}
                                                size="small"
                                                onClick={() =>
                                                    onEnabledChanged(source, !source.enabled)
                                                }
                                            />
                                        </Tooltip>
                                    </TableCell>
                                    <TableCell align="center">
                                        <Stack
                                            direction="row"
                                            spacing={0.25}
                                            sx={{ justifyContent: 'center' }}
                                        >
                                            <Tooltip title="测试连接">
                                                <span>
                                                    <IconButton
                                                        size="small"
                                                        aria-label={`测试 ${source.name}`}
                                                        disabled={testing !== null}
                                                        onClick={() => onTest(source)}
                                                    >
                                                        {testing === source.id ? (
                                                            <CircularProgress size={18} />
                                                        ) : (
                                                            <NetworkCheckOutlinedIcon fontSize="small" />
                                                        )}
                                                    </IconButton>
                                                </span>
                                            </Tooltip>
                                            <Tooltip title="编辑">
                                                <IconButton
                                                    size="small"
                                                    aria-label={`编辑 ${source.name}`}
                                                    onClick={() => onEdit(source)}
                                                >
                                                    <EditOutlinedIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                            <Tooltip title="删除">
                                                <IconButton
                                                    size="small"
                                                    color="error"
                                                    aria-label={`删除 ${source.name}`}
                                                    onClick={() => onDelete(source)}
                                                >
                                                    <DeleteOutlineIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                        </Stack>
                                    </TableCell>
                                </TableRow>
                            ))}
                            {paginated.length === 0 && (
                                <TableRow>
                                    <TableCell colSpan={6}>
                                        <Box sx={{ py: 6, textAlign: 'center' }}>
                                            <Typography color="text.secondary">
                                                暂无匹配的同步源
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
function locationSummary(source: SourceResponse): string {
    const config = source.config
    switch (source.type) {
        case 'webdav':
            return (config as WebDAVConfig).endpoint ?? ''
        case 's3': {
            const cfg = config as S3Config
            return `${urlHost(cfg.endpoint ?? '')}/${cfg.bucket}${cfg.prefix ? `/${cfg.prefix}` : ''}`
        }
        case 'sftp': {
            const cfg = config as SFTPConfig
            return `${cfg.host}:${cfg.port ?? 22}${cfg.remote_root}`
        }
        case 'github_release': {
            const cfg = config as GitHubReleaseConfig
            switch (cfg.release_policy) {
                case 'tag':
                    return `${cfg.repository} · ${cfg.tag ?? ''}`
                case 'recent':
                    return `${cfg.repository} · 最近 ${cfg.recent_count ?? 0} 个版本`
                case 'all':
                    return `${cfg.repository} · 全部版本`
                default:
                    return `${cfg.repository} · 最新稳定版`
            }
        }
    }
}
function urlHost(endpoint: string): string {
    try {
        return endpoint === '' ? 'aws' : new URL(endpoint).host
    } catch {
        return endpoint
    }
}
