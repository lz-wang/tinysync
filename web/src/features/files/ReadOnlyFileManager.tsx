import ArrowUpwardOutlinedIcon from '@mui/icons-material/ArrowUpwardOutlined'
import DownloadOutlinedIcon from '@mui/icons-material/DownloadOutlined'
import FolderOutlinedIcon from '@mui/icons-material/FolderOutlined'
import InsertDriveFileOutlinedIcon from '@mui/icons-material/InsertDriveFileOutlined'
import RefreshOutlinedIcon from '@mui/icons-material/RefreshOutlined'
import VisibilityOffOutlinedIcon from '@mui/icons-material/VisibilityOffOutlined'
import VisibilityOutlinedIcon from '@mui/icons-material/VisibilityOutlined'
import {
    Box,
    Breadcrumbs,
    Button,
    Chip,
    CircularProgress,
    IconButton,
    Link,
    MenuItem,
    Paper,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    TableSortLabel,
    TextField,
    Tooltip,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useMemo, useState } from 'react'
import type { FileEntry } from '../../api'
import { useToast } from '../../app/toast'
import { formatBytes, formatDateTime } from '../history/shared'

export interface FileManagerResource {
    id: string
    label: string
}

type FileSortField = 'name' | 'kind' | 'size' | 'modifiedAt' | 'managed'

// ReadOnlyFileManager 复用 Cloud Center 文件管理器的「位置栏 + 工具栏 +
// 表格」工作流。TinySync 的浏览端点是只读的，所以只公开目录导航、刷新、
// 下载和本地受管理文件的发布入口，不呈现不可执行的写入动作。
export default function ReadOnlyFileManager({
    resourceLabel,
    resources,
    resourceId,
    path,
    onResourceChange,
    onPathChange,
    load,
    downloadURL,
    renderActions,
    toolbarActions,
    showHidden = false,
    onShowHiddenChange,
    showManaged,
}: {
    resourceLabel: string
    resources: FileManagerResource[]
    resourceId: string
    path: string
    onResourceChange: (id: string) => void
    onPathChange: (path: string) => void
    load: (path: string, cursor?: string) => Promise<{ entries: FileEntry[]; nextCursor: string }>
    downloadURL: (path: string) => string
    renderActions?: (entry: FileEntry) => React.ReactNode
    toolbarActions?: React.ReactNode
    showHidden?: boolean
    onShowHiddenChange?: (showHidden: boolean) => void
    showManaged?: boolean
}) {
    const toast = useToast()
    const [entries, setEntries] = useState<FileEntry[]>([])
    const [nextCursor, setNextCursor] = useState('')
    const [loading, setLoading] = useState(false)
    // loadFailed 区分「目录为空」与「加载失败」：失败时表格区域不显示
    // 目录为空 文案，错误详情只经 toast 提示。
    const [loadFailed, setLoadFailed] = useState(false)
    const [sort, setSort] = useState<{ field: FileSortField; direction: 'asc' | 'desc' }>({
        field: 'name',
        direction: 'asc',
    })

    const fetchPage = useCallback(
        async (cursor?: string) => {
            if (resourceId === '') return
            setLoading(true)
            setLoadFailed(false)
            try {
                const page = await load(path, cursor)
                setEntries(previous =>
                    cursor === undefined ? page.entries : [...previous, ...page.entries],
                )
                setNextCursor(page.nextCursor)
            } catch (loadError) {
                // 目录加载失败不清空已展示的表格；错误详情经 toast 提示。
                setLoadFailed(true)
                toast.error(
                    `加载目录失败：${
                        loadError instanceof Error ? loadError.message : String(loadError)
                    }`,
                )
            } finally {
                setLoading(false)
            }
        },
        [load, path, resourceId, toast],
    )

    useEffect(() => {
        setEntries([])
        setNextCursor('')
        void fetchPage()
    }, [fetchPage])

    const openDirectory = (nextPath: string) => onPathChange(nextPath)
    const pathParts = useMemo(() => path.split('/').filter(Boolean), [path])
    const parentPath = pathParts.length === 0 ? '/' : `/${pathParts.slice(0, -1).join('/')}` || '/'
    const sortedEntries = useMemo(
        () =>
            [...entries].sort((a, b) => {
                const value = (entry: FileEntry): string | number => {
                    switch (sort.field) {
                        case 'kind':
                            return entry.kind
                        case 'size':
                            return entry.size
                        case 'modifiedAt':
                            return entry.modified_at ?? ''
                        case 'managed':
                            return entry.managed === true ? 1 : 0
                        default:
                            return entry.name
                    }
                }
                const aValue = value(a)
                const bValue = value(b)
                const compared =
                    typeof aValue === 'number' && typeof bValue === 'number'
                        ? aValue - bValue
                        : String(aValue).localeCompare(String(bValue), 'zh-CN', { numeric: true })
                return compared * (sort.direction === 'asc' ? 1 : -1)
            }),
        [entries, sort],
    )
    const toggleSort = (field: FileSortField) =>
        setSort(previous => ({
            field,
            direction: previous.field === field && previous.direction === 'asc' ? 'desc' : 'asc',
        }))
    const sortableHeader = (label: string, field: FileSortField) => (
        <TableSortLabel
            active={sort.field === field}
            direction={sort.field === field ? sort.direction : 'asc'}
            onClick={() => toggleSort(field)}
        >
            {label}
        </TableSortLabel>
    )

    if (resources.length === 0) {
        return <Typography color="text.secondary">尚无可浏览的{resourceLabel}。</Typography>
    }

    return (
        <Paper
            variant="outlined"
            sx={{
                // 管理器固定在主视口内；页面仍保留一点底部留白，长目录
                // 只在表格区域滚动，位置栏和工具栏始终可见。
                height: 'calc(100dvh - 192px)',
                minHeight: 360,
                display: 'flex',
                flexDirection: 'column',
                overflow: 'hidden',
            }}
        >
            <Stack
                component="nav"
                aria-label="文件位置"
                direction={{ xs: 'column', md: 'row' }}
                sx={{ gap: 1, alignItems: { xs: 'stretch', md: 'center' }, px: 2, py: 1.5 }}
            >
                <TextField
                    select
                    size="small"
                    label={resourceLabel}
                    value={resourceId}
                    onChange={event => onResourceChange(event.target.value)}
                    sx={{ minWidth: { xs: '100%', md: 280 } }}
                >
                    {resources.map(resource => (
                        <MenuItem key={resource.id} value={resource.id}>
                            {resource.label}
                        </MenuItem>
                    ))}
                </TextField>
                <Button
                    size="small"
                    startIcon={<ArrowUpwardOutlinedIcon />}
                    disabled={pathParts.length === 0}
                    onClick={() => openDirectory(parentPath)}
                >
                    上一级
                </Button>
                <Breadcrumbs aria-label="当前位置" sx={{ flex: 1, minWidth: 0 }}>
                    <Link component="button" underline="hover" onClick={() => openDirectory('/')}>
                        根目录
                    </Link>
                    {pathParts.map((part, index) => {
                        const crumbPath = `/${pathParts.slice(0, index + 1).join('/')}`
                        const isCurrent = index === pathParts.length - 1
                        return isCurrent ? (
                            <Typography key={crumbPath} color="text.primary" noWrap>
                                {part}
                            </Typography>
                        ) : (
                            <Link
                                component="button"
                                underline="hover"
                                key={crumbPath}
                                onClick={() => openDirectory(crumbPath)}
                            >
                                {part}
                            </Link>
                        )
                    })}
                </Breadcrumbs>
                <Stack direction="row" spacing={0.5} sx={{ alignItems: 'center' }}>
                    {onShowHiddenChange !== undefined && (
                        <Tooltip
                            title={showHidden ? '隐藏隐藏文件和文件夹' : '显示隐藏文件和文件夹'}
                        >
                            <IconButton
                                aria-label={
                                    showHidden ? '隐藏隐藏文件和文件夹' : '显示隐藏文件和文件夹'
                                }
                                aria-pressed={showHidden}
                                onClick={() => onShowHiddenChange(!showHidden)}
                                size="small"
                            >
                                {showHidden ? (
                                    <VisibilityOutlinedIcon fontSize="small" />
                                ) : (
                                    <VisibilityOffOutlinedIcon fontSize="small" />
                                )}
                            </IconButton>
                        </Tooltip>
                    )}
                    <Tooltip title="刷新当前目录">
                        <IconButton
                            aria-label="刷新当前目录"
                            size="small"
                            onClick={() => void fetchPage()}
                        >
                            <RefreshOutlinedIcon fontSize="small" />
                        </IconButton>
                    </Tooltip>
                    {toolbarActions}
                </Stack>
            </Stack>
            <TableContainer sx={{ flex: 1, overflowY: 'auto' }}>
                <Table size="small" aria-label="文件列表">
                    <TableHead>
                        <TableRow>
                            <TableCell>{sortableHeader('名称', 'name')}</TableCell>
                            <TableCell sx={{ width: 100 }}>
                                {sortableHeader('类型', 'kind')}
                            </TableCell>
                            <TableCell sx={{ width: 120 }}>
                                {sortableHeader('大小', 'size')}
                            </TableCell>
                            <TableCell sx={{ width: 190 }}>
                                {sortableHeader('修改时间', 'modifiedAt')}
                            </TableCell>
                            {showManaged && (
                                <TableCell sx={{ width: 110 }}>
                                    {sortableHeader('状态', 'managed')}
                                </TableCell>
                            )}
                            <TableCell align="center" sx={{ width: 130 }}>
                                操作
                            </TableCell>
                        </TableRow>
                    </TableHead>
                    <TableBody>
                        {sortedEntries.map(entry => {
                            const directory = entry.kind === 'directory'
                            const file = entry.kind === 'file'
                            return (
                                <TableRow hover key={entry.path}>
                                    <TableCell>
                                        {directory ? (
                                            <Button
                                                color="inherit"
                                                startIcon={<FolderOutlinedIcon color="warning" />}
                                                onClick={() => openDirectory(entry.path)}
                                                sx={{
                                                    justifyContent: 'flex-start',
                                                    textTransform: 'none',
                                                }}
                                            >
                                                <Typography noWrap title={entry.path}>
                                                    {entry.name}
                                                </Typography>
                                            </Button>
                                        ) : (
                                            <Stack
                                                direction="row"
                                                spacing={1}
                                                sx={{ alignItems: 'center' }}
                                            >
                                                <InsertDriveFileOutlinedIcon
                                                    color="action"
                                                    fontSize="small"
                                                />
                                                <Typography noWrap title={entry.path}>
                                                    {entry.name}
                                                </Typography>
                                            </Stack>
                                        )}
                                    </TableCell>
                                    <TableCell>
                                        {directory
                                            ? '文件夹'
                                            : file
                                              ? '文件'
                                              : entry.kind === 'symlink'
                                                ? '符号链接'
                                                : '其他'}
                                    </TableCell>
                                    <TableCell>{file ? formatBytes(entry.size) : '-'}</TableCell>
                                    <TableCell>
                                        {file
                                            ? formatDateTime(entry.modified_at ?? undefined, '-')
                                            : '-'}
                                    </TableCell>
                                    {showManaged && (
                                        <TableCell>
                                            {entry.managed === true ? (
                                                <Chip
                                                    color="primary"
                                                    label="受管理"
                                                    size="small"
                                                    variant="outlined"
                                                />
                                            ) : (
                                                <Chip
                                                    label="未管理"
                                                    size="small"
                                                    variant="outlined"
                                                />
                                            )}
                                        </TableCell>
                                    )}
                                    <TableCell align="center">
                                        {file && (
                                            <IconButton
                                                aria-label={`下载 ${entry.name}`}
                                                href={downloadURL(entry.path)}
                                                size="small"
                                            >
                                                <DownloadOutlinedIcon fontSize="small" />
                                            </IconButton>
                                        )}
                                        {renderActions?.(entry)}
                                    </TableCell>
                                </TableRow>
                            )
                        })}
                        {entries.length === 0 && !loading && !loadFailed && (
                            <TableRow>
                                <TableCell
                                    align="center"
                                    colSpan={showManaged ? 6 : 5}
                                    sx={{ py: 5 }}
                                >
                                    <Typography color="text.secondary">目录为空</Typography>
                                </TableCell>
                            </TableRow>
                        )}
                    </TableBody>
                </Table>
            </TableContainer>
            {(loading || nextCursor !== '') && (
                <Box sx={{ display: 'flex', flexShrink: 0, justifyContent: 'center', p: 1.5 }}>
                    {loading ? (
                        <CircularProgress size={22} />
                    ) : (
                        <Button size="small" onClick={() => void fetchPage(nextCursor)}>
                            加载更多
                        </Button>
                    )}
                </Box>
            )}
        </Paper>
    )
}
