import FolderOutlinedIcon from '@mui/icons-material/FolderOutlined'
import InsertDriveFileOutlinedIcon from '@mui/icons-material/InsertDriveFileOutlined'
import {
    Alert,
    Box,
    Breadcrumbs,
    CircularProgress,
    Link,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
    type FileEntry,
    fetchVersion,
    listPublicShareEntries,
    sharedBrowsePath,
    sharedFileURL,
} from '../../api'
import { usePageTitle } from '../../app/usePageTitle'
import { formatBytes } from '../../features/history/shared'

// SharedBrowsePage 是公开的无边距文件浏览器。目录层级直接映射到 URL。
export default function SharedBrowsePage() {
    const { slug = '', '*': routePath = '' } = useParams()
    const navigate = useNavigate()
    const path = useMemo(
        () => `/${routePath.split('/').filter(Boolean).join('/')}` || '/',
        [routePath],
    )
    const [entries, setEntries] = useState<FileEntry[]>([])
    const [loading, setLoading] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [version, setVersion] = useState<string | null>(null)
    usePageTitle('共享内容')

    useEffect(() => {
        let cancelled = false
        void fetchVersion()
            .then(response => !cancelled && setVersion(response.version))
            .catch(() => !cancelled && setVersion(null))
        return () => {
            cancelled = true
        }
    }, [])

    useEffect(() => {
        let cancelled = false
        async function loadAll() {
            setLoading(true)
            setError(null)
            try {
                const all: FileEntry[] = []
                let cursor: string | undefined
                do {
                    const page = await listPublicShareEntries(slug, path, 500, cursor)
                    all.push(...page.entries)
                    cursor = page.next_cursor || undefined
                } while (cursor !== undefined)
                if (!cancelled) setEntries(all)
            } catch (loadError) {
                if (!cancelled) {
                    setEntries([])
                    setError(loadError instanceof Error ? loadError.message : String(loadError))
                }
            } finally {
                if (!cancelled) setLoading(false)
            }
        }
        if (slug !== '') void loadAll()
        return () => {
            cancelled = true
        }
    }, [path, slug])

    const pathParts = useMemo(() => path.split('/').filter(Boolean), [path])
    const parentPath = pathParts.length <= 1 ? '/' : `/${pathParts.slice(0, -1).join('/')}`
    const sortedEntries = useMemo(
        () =>
            [...entries].sort((a, b) => {
                if (a.kind !== b.kind) {
                    if (a.kind === 'directory') return -1
                    if (b.kind === 'directory') return 1
                }
                return a.name.localeCompare(b.name, 'zh-CN', { numeric: true, sensitivity: 'base' })
            }),
        [entries],
    )
    const navigateTo = useCallback(
        (nextPath: string) => navigate(sharedBrowsePath(slug, nextPath)),
        [navigate, slug],
    )
    return (
        <Box
            sx={{
                height: '100dvh',
                display: 'flex',
                flexDirection: 'column',
                bgcolor: 'background.default',
            }}
        >
            <Breadcrumbs
                aria-label="当前位置"
                separator="›"
                sx={{
                    flexShrink: 0,
                    px: 2,
                    py: 1.5,
                    '& .MuiLink-root, & .MuiTypography-root': { fontSize: '1rem' },
                }}
            >
                <Link component="button" underline="hover" onClick={() => navigateTo('/')}>
                    /
                </Link>
                {pathParts.map((part, index) => {
                    const crumbPath = `/${pathParts.slice(0, index + 1).join('/')}`
                    return (
                        <Link
                            component="button"
                            color={index === pathParts.length - 1 ? 'text.primary' : undefined}
                            underline="hover"
                            key={crumbPath}
                            onClick={() => navigateTo(crumbPath)}
                        >
                            {part}
                        </Link>
                    )
                })}
            </Breadcrumbs>
            {error !== null && <Alert severity="error">加载目录失败：{error}</Alert>}
            <TableContainer sx={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
                <Table
                    size="medium"
                    aria-label="共享文件列表"
                    stickyHeader
                    sx={{
                        '& .MuiTableCell-root': { borderBottom: 0 },
                        '& .MuiTableHead-root .MuiTableCell-root': {
                            borderBottom: 1,
                            borderColor: 'divider',
                        },
                    }}
                >
                    <TableHead>
                        <TableRow>
                            <TableCell>名称</TableCell>
                            <TableCell sx={{ width: 100 }}>类型</TableCell>
                            <TableCell sx={{ width: 120 }}>大小</TableCell>
                            <TableCell sx={{ width: 190 }}>修改时间</TableCell>
                        </TableRow>
                    </TableHead>
                    <TableBody>
                        {pathParts.length > 0 && (
                            <TableRow
                                hover
                                onClick={() => navigateTo(parentPath)}
                                sx={{ cursor: 'pointer' }}
                            >
                                <TableCell>
                                    <Stack
                                        direction="row"
                                        spacing={1}
                                        sx={{ alignItems: 'center' }}
                                    >
                                        <FolderOutlinedIcon color="primary" fontSize="small" />
                                        <Typography>..</Typography>
                                    </Stack>
                                </TableCell>
                                <TableCell>文件夹</TableCell>
                                <TableCell>-</TableCell>
                                <TableCell>-</TableCell>
                            </TableRow>
                        )}
                        {sortedEntries.map(entry => {
                            const directory = entry.kind === 'directory'
                            const file = entry.kind === 'file'
                            return (
                                <TableRow hover key={entry.path}>
                                    <TableCell>
                                        {directory ? (
                                            <Link
                                                component="button"
                                                color="primary"
                                                underline="none"
                                                onClick={() => navigateTo(entry.path)}
                                                sx={{
                                                    display: 'flex',
                                                    alignItems: 'center',
                                                    gap: 1,
                                                }}
                                            >
                                                <FolderOutlinedIcon
                                                    color="primary"
                                                    fontSize="small"
                                                />
                                                <Typography noWrap title={entry.path}>
                                                    {entry.name}
                                                </Typography>
                                            </Link>
                                        ) : file ? (
                                            <Link
                                                href={sharedFileURL(slug, entry.path)}
                                                color="inherit"
                                                underline="none"
                                                sx={{
                                                    display: 'flex',
                                                    alignItems: 'center',
                                                    gap: 1,
                                                    width: 'fit-content',
                                                }}
                                            >
                                                <InsertDriveFileOutlinedIcon
                                                    color="action"
                                                    fontSize="small"
                                                />
                                                <Typography noWrap title={entry.path}>
                                                    {entry.name}
                                                </Typography>
                                            </Link>
                                        ) : (
                                            <Typography noWrap>{entry.name}</Typography>
                                        )}
                                    </TableCell>
                                    <TableCell>
                                        {directory ? '文件夹' : file ? '文件' : '其他'}
                                    </TableCell>
                                    <TableCell>{file ? formatBytes(entry.size) : '-'}</TableCell>
                                    <TableCell>
                                        {file ? formatSharedModifiedAt(entry.modified_at) : '-'}
                                    </TableCell>
                                </TableRow>
                            )
                        })}
                        {entries.length === 0 && !loading && error === null && (
                            <TableRow>
                                <TableCell align="center" colSpan={4} sx={{ py: 5 }}>
                                    <Typography color="text.secondary">目录为空</Typography>
                                </TableCell>
                            </TableRow>
                        )}
                    </TableBody>
                </Table>
            </TableContainer>
            {loading && (
                <Box sx={{ display: 'flex', justifyContent: 'center', p: 1.5 }}>
                    <CircularProgress size={22} />
                </Box>
            )}
            <Typography
                component="footer"
                color="text.secondary"
                variant="caption"
                sx={{ flexShrink: 0, py: 1, textAlign: 'center' }}
            >
                Powered by{' '}
                <Link href="https://github.com/lz-wang/tinysync" target="_blank" rel="noreferrer">
                    TinySync
                </Link>{' '}
                {version ?? '-'}
            </Typography>
        </Box>
    )
}

// formatSharedModifiedAt 保留分钟精度；跨年记录额外携带年份以避免歧义。
function formatSharedModifiedAt(value: string | null): string {
    if (value === null || value === '') return '-'
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return value
    const pad = (part: number) => String(part).padStart(2, '0')
    const body = `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
    return date.getFullYear() === new Date().getFullYear() ? body : `${date.getFullYear()}-${body}`
}
