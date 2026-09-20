import ContentCopyIcon from '@mui/icons-material/ContentCopy'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlined'
import SearchOutlinedIcon from '@mui/icons-material/SearchOutlined'
import {
    Alert,
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
    Switch,
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
import { deleteShare, listShares, type ShareResponse, shareEntryURL, updateShare } from '../../api'
import { copyText } from '../../app/clipboard'
import { formatDateTime } from '../history/shared'

type SortField = 'name' | 'target' | 'type' | 'enabled' | 'expiresAt'
const nameOf = (s: ShareResponse) => s.name ?? s.local_path.split(/[\\/]/).pop() ?? s.local_path
// SharePanel 复用同步任务页的工具栏、选择、筛选、排序、分页与固定表格容器。
export default function SharePanel() {
    const [shares, setShares] = useState<ShareResponse[] | null>(null)
    const [error, setError] = useState<string | null>(null)
    const [deleting, setDeleting] = useState<ShareResponse[] | null>(null)
    const [copied, setCopied] = useState<string | null>(null)
    const reload = useCallback(async () => setShares(await listShares()), [])
    useEffect(() => {
        void reload().catch(e => setError(e instanceof Error ? e.message : String(e)))
    }, [reload])
    const saveEnabled = async (share: ShareResponse, enabled: boolean) => {
        try {
            const next = await updateShare(share.id, { enabled })
            setShares(old => old?.map(item => (item.id === next.id ? next : item)) ?? null)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        }
    }
    const remove = async () => {
        if (!deleting) return
        try {
            const results = await Promise.allSettled(deleting.map(s => deleteShare(s.id)))
            const failed = results.filter(r => r.status === 'rejected').length
            if (failed) throw new Error(`${failed} 个共享删除失败`)
            setShares(old => old?.filter(s => !deleting.some(x => x.id === s.id)) ?? null)
            setDeleting(null)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        }
    }
    const copy = async (s: ShareResponse) => {
        try {
            await copyText(shareEntryURL(s))
            setCopied(s.id)
            window.setTimeout(() => setCopied(null), 1500)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
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
            {error && (
                <Alert severity="error" onClose={() => setError(null)}>
                    {error}
                </Alert>
            )}
            {!shares && !error ? (
                <CircularProgress size={24} />
            ) : (
                shares && (
                    <ShareTable
                        shares={shares}
                        copied={copied}
                        onEnabledChanged={saveEnabled}
                        onCopy={copy}
                        onDelete={setDeleting}
                    />
                )
            )}
            <Dialog
                open={deleting !== null}
                onClose={() => setDeleting(null)}
                maxWidth="xs"
                fullWidth
            >
                <DialogTitle>删除已选共享？</DialogTitle>
                <DialogContent>
                    <Typography>
                        确定删除 {deleting?.length ?? 0}{' '}
                        个共享？公开链接将立即失效，本地文件会保留。
                    </Typography>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => setDeleting(null)}>取消</Button>
                    <Button color="error" variant="contained" onClick={() => void remove()}>
                        删除
                    </Button>
                </DialogActions>
            </Dialog>
        </Stack>
    )
}

function ShareTable({
    shares,
    copied,
    onEnabledChanged,
    onCopy,
    onDelete,
}: {
    shares: ShareResponse[]
    copied: string | null
    onEnabledChanged: (share: ShareResponse, enabled: boolean) => Promise<void>
    onCopy: (s: ShareResponse) => Promise<void>
    onDelete: (s: ShareResponse[]) => void
}) {
    const [selected, setSelected] = useState<string[]>([]),
        [query, setQuery] = useState(''),
        [type, setType] = useState('all'),
        [status, setStatus] = useState('all'),
        [sort, setSort] = useState<{ field: SortField; direction: 'asc' | 'desc' }>({
            field: 'name',
            direction: 'asc',
        }),
        [page, setPage] = useState(0),
        [rows, setRows] = useState(15)
    const visible = useMemo(
        () =>
            shares
                .filter(s => {
                    const q = query.trim().toLowerCase()
                    return (
                        (!q ||
                            `${nameOf(s)} ${s.slug} ${s.local_path}`.toLowerCase().includes(q)) &&
                        (type === 'all' || (type === 'directory' ? s.is_dir : !s.is_dir)) &&
                        (status === 'all' || (status === 'enabled' ? s.enabled : !s.enabled))
                    )
                })
                .sort((a, b) => {
                    const value = (s: ShareResponse) =>
                        sort.field === 'name'
                            ? nameOf(s)
                            : sort.field === 'target'
                              ? s.local_path
                              : sort.field === 'type'
                                ? String(s.is_dir)
                                : sort.field === 'enabled'
                                  ? String(s.enabled)
                                  : s.expires_at
                    return (
                        value(a).localeCompare(value(b), 'zh-CN', { numeric: true }) *
                        (sort.direction === 'asc' ? 1 : -1)
                    )
                }),
        [shares, query, type, status, sort],
    )
    const pageRows = visible.slice(page * rows, page * rows + rows),
        chosen = shares.filter(s => selected.includes(s.id)),
        all = pageRows.length > 0 && pageRows.every(s => selected.includes(s.id)),
        some = pageRows.some(s => selected.includes(s.id))
    useEffect(() => {
        if (page > Math.max(0, Math.ceil(visible.length / rows) - 1)) setPage(0)
    }, [page, rows, visible.length])
    const toggle = (field: SortField) => {
        setSort(p => ({
            field,
            direction: p.field === field && p.direction === 'asc' ? 'desc' : 'asc',
        }))
        setPage(0)
    }
    const head = (label: string, field: SortField) => (
        <TableSortLabel
            active={sort.field === field}
            direction={sort.field === field ? sort.direction : 'asc'}
            onClick={() => toggle(field)}
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
                {chosen.length > 0 && (
                    <>
                        <Typography variant="body2">已选 {chosen.length} 项</Typography>
                        <Button
                            size="small"
                            color="error"
                            variant="outlined"
                            startIcon={<DeleteOutlineIcon />}
                            onClick={() => onDelete(chosen)}
                        >
                            批量删除
                        </Button>
                    </>
                )}
                <Box sx={{ flexGrow: 1 }} />
                <TextField
                    size="small"
                    placeholder="搜索名称、链接或本地目标"
                    value={query}
                    onChange={e => {
                        setQuery(e.target.value)
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
                    onChange={e => {
                        setType(e.target.value)
                        setPage(0)
                    }}
                    sx={{ minWidth: 110 }}
                >
                    <MenuItem value="all">全部类型</MenuItem>
                    <MenuItem value="file">文件</MenuItem>
                    <MenuItem value="directory">目录</MenuItem>
                </TextField>
                <TextField
                    select
                    size="small"
                    label="状态"
                    value={status}
                    onChange={e => {
                        setStatus(e.target.value)
                        setPage(0)
                    }}
                    sx={{ minWidth: 110 }}
                >
                    <MenuItem value="all">全部状态</MenuItem>
                    <MenuItem value="enabled">已启用</MenuItem>
                    <MenuItem value="disabled">已停用</MenuItem>
                </TextField>
            </Stack>
            <Paper
                variant="outlined"
                sx={{
                    flex: 1,
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
                                        checked={all}
                                        indeterminate={some && !all}
                                        onChange={e =>
                                            setSelected(
                                                e.target.checked
                                                    ? [
                                                          ...new Set([
                                                              ...selected,
                                                              ...pageRows.map(s => s.id),
                                                          ]),
                                                      ]
                                                    : selected.filter(
                                                          id => !pageRows.some(s => s.id === id),
                                                      ),
                                            )
                                        }
                                    />
                                </TableCell>
                                <TableCell>{head('名称', 'name')}</TableCell>
                                <TableCell>{head('本地目标', 'target')}</TableCell>
                                <TableCell>{head('类型', 'type')}</TableCell>
                                <TableCell>{head('状态', 'enabled')}</TableCell>
                                <TableCell>{head('过期时间', 'expiresAt')}</TableCell>
                                <TableCell align="center">操作</TableCell>
                            </TableRow>
                        </TableHead>
                        <TableBody>
                            {pageRows.map(s => (
                                <TableRow key={s.id} hover selected={selected.includes(s.id)}>
                                    <TableCell padding="checkbox">
                                        <Checkbox
                                            size="small"
                                            checked={selected.includes(s.id)}
                                            onChange={e =>
                                                setSelected(p =>
                                                    e.target.checked
                                                        ? [...p, s.id]
                                                        : p.filter(id => id !== s.id),
                                                )
                                            }
                                        />
                                    </TableCell>
                                    <TableCell>
                                        <Typography
                                            component="a"
                                            href={shareEntryURL(s)}
                                            target="_blank"
                                            rel="noreferrer"
                                            variant="body2"
                                            sx={{
                                                fontWeight: 500,
                                                color: 'primary.main',
                                                textDecoration: 'none',
                                            }}
                                        >
                                            {nameOf(s)}
                                        </Typography>
                                    </TableCell>
                                    <TableCell>
                                        <Typography
                                            variant="body2"
                                            noWrap
                                            title={s.local_path}
                                            sx={{ fontFamily: 'monospace', maxWidth: 320 }}
                                        >
                                            {s.local_path}
                                        </Typography>
                                    </TableCell>
                                    <TableCell>
                                        <Chip
                                            size="small"
                                            variant="outlined"
                                            label={s.is_dir ? '目录' : '文件'}
                                        />
                                    </TableCell>
                                    <TableCell>
                                        <Stack
                                            direction="row"
                                            spacing={0.75}
                                            sx={{ alignItems: 'center' }}
                                        >
                                            <Switch
                                                size="small"
                                                checked={s.enabled}
                                                onChange={event =>
                                                    void onEnabledChanged(s, event.target.checked)
                                                }
                                            />
                                            <Chip
                                                size="small"
                                                color={s.enabled ? 'success' : 'default'}
                                                label={s.enabled ? '已启用' : '已停用'}
                                            />
                                        </Stack>
                                    </TableCell>
                                    <TableCell>
                                        <Typography variant="body2" color="text.secondary">
                                            {s.expires_at === ''
                                                ? '永不'
                                                : formatDateTime(s.expires_at)}
                                        </Typography>
                                    </TableCell>
                                    <TableCell align="center">
                                        <Stack
                                            direction="row"
                                            spacing={0.25}
                                            sx={{ justifyContent: 'center' }}
                                        >
                                            <Tooltip
                                                title={copied === s.id ? '已复制' : '复制链接'}
                                            >
                                                <IconButton
                                                    size="small"
                                                    aria-label={`复制 ${nameOf(s)} 的链接`}
                                                    onClick={() => void onCopy(s)}
                                                >
                                                    <ContentCopyIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                            <Tooltip title="删除">
                                                <IconButton
                                                    size="small"
                                                    color="error"
                                                    aria-label={`删除 ${nameOf(s)}`}
                                                    onClick={() => onDelete([s])}
                                                >
                                                    <DeleteOutlineIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                        </Stack>
                                    </TableCell>
                                </TableRow>
                            ))}
                            {pageRows.length === 0 && (
                                <TableRow>
                                    <TableCell colSpan={7}>
                                        <Box sx={{ py: 6, textAlign: 'center' }}>
                                            <Typography color="text.secondary">
                                                {shares.length === 0
                                                    ? '尚未创建任何共享'
                                                    : '暂无匹配的共享'}
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
                    onPageChange={(_, p) => setPage(p)}
                    rowsPerPage={rows}
                    onRowsPerPageChange={e => {
                        setRows(Number(e.target.value))
                        setPage(0)
                    }}
                    rowsPerPageOptions={[15, 30, 50]}
                    labelRowsPerPage="每页"
                />
            </Paper>
        </Stack>
    )
}
