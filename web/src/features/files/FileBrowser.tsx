import BlockIcon from '@mui/icons-material/Block'
import DescriptionIcon from '@mui/icons-material/Description'
import DownloadIcon from '@mui/icons-material/Download'
import FolderIcon from '@mui/icons-material/Folder'
import LinkIcon from '@mui/icons-material/Link'
import {
    Alert,
    Box,
    Breadcrumbs,
    Chip,
    CircularProgress,
    IconButton,
    Link,
    Typography,
} from '@mui/material'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FileEntry } from '../../api'

// 分页页大小：与后端默认一致；滚动接近末尾时以 next_cursor 续拉。
export const PAGE_SIZE = 100

// formatSize 人类可读的体积显示。
export function formatSize(bytes: number): string {
    if (bytes < 1024) {
        return `${bytes} B`
    }
    const units = ['KiB', 'MiB', 'GiB', 'TiB']
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

// kindIcon 按条目类型返回图标；symlink / other 不可操作。
function kindIcon(kind: FileEntry['kind']) {
    switch (kind) {
        case 'directory':
            return <FolderIcon fontSize="small" color="primary" />
        case 'symlink':
            return <LinkIcon fontSize="small" color="warning" />
        case 'other':
            return <BlockIcon fontSize="small" color="disabled" />
        default:
            return <DescriptionIcon fontSize="small" />
    }
}

// splitBreadcrumb 把逻辑路径拆为 {name, path} 序列；根为 "/"。
function splitBreadcrumb(path: string): Array<{ name: string; path: string }> {
    const crumbs = [{ name: '/', path: '/' }]
    if (path === '/') {
        return crumbs
    }
    let acc = ''
    for (const seg of path.split('/').filter(s => s !== '')) {
        acc += `/${seg}`
        crumbs.push({ name: seg, path: acc })
    }
    return crumbs
}

// FileBrowser 是共享的目录浏览器：breadcrumb 导航、虚拟化列表、
// cursor 驱动的增量分页与下载。load 由调用方注入（Remote / Local
// 各自指向自己的 API）；initialPath 是 namespace 的初始目录，组件
// 挂载或 load 变化（切换 Source / Job）时从这里开始浏览——picker
// 场景借此直接落在 Job 的 Remote Root 而不是根目录；onPathChange
// 在目录变化时通知外部当前路径；downloadURL 表示该条目是否可下载
//（symlink / other 由组件内部判定）。
export default function FileBrowser({
    load,
    initialPath = '/',
    downloadURL,
    renderEntryExtra,
    onPathChange,
    emptyHint = '目录为空',
    height = 420,
}: {
    load: (
        path: string,
        cursor: string | null,
    ) => Promise<{ entries: FileEntry[]; nextCursor: string }>
    // namespace 初始目录：仅挂载与 namespace 变化时生效，目录导航
    // 不受影响。
    initialPath?: string
    downloadURL: (path: string) => string
    renderEntryExtra?: (entry: FileEntry) => React.ReactNode
    onPathChange?: (path: string) => void
    emptyHint?: string
    height?: number
}) {
    const [path, setPath] = useState('/')
    const [entries, setEntries] = useState<FileEntry[]>([])
    const [nextCursor, setNextCursor] = useState('')
    const [loading, setLoading] = useState(false)
    const [error, setError] = useState<string | null>(null)
    // scrollParentRef 供虚拟列表与「接近末尾续拉」判定使用。
    const scrollParentRef = useRef<HTMLDivElement | null>(null)
    // loadingRef 镜像 loading，供滚动回调读取最新值并阻止并发续拉。
    const loadingRef = useRef(false)
    // generationRef 是请求代数：namespace（load 变化）或目录被切换时
    // 递增使在途请求失效——旧 Source / 旧目录的迟到响应不得污染新
    // 视图（响应会填充旧数据，而 download 链接已指向新 namespace）。
    const generationRef = useRef(0)

    const loadPage = useCallback(
        async (target: string, cursor: string | null) => {
            if (loadingRef.current) {
                return
            }
            loadingRef.current = true
            setLoading(true)
            setError(null)
            const generation = generationRef.current
            try {
                const page = await load(target, cursor)
                if (generation !== generationRef.current) {
                    return
                }
                setPath(target)
                setEntries(prev => (cursor === null ? page.entries : [...prev, ...page.entries]))
                setNextCursor(page.nextCursor)
            } catch (err) {
                if (generation !== generationRef.current) {
                    return
                }
                setError(err instanceof Error ? err.message : String(err))
            } finally {
                // 失效请求不触碰 loading 状态：复位已由触发切换的一
                // 方完成，这里复位会让并发中的新请求失去防重入保护。
                if (generation === generationRef.current) {
                    loadingRef.current = false
                    setLoading(false)
                }
            }
        },
        [load],
    )

    // invalidateView 使在途请求失效并解锁 loading，随后发起的请求不
    // 会被旧请求的在途状态挡住。
    const invalidateView = useCallback(() => {
        generationRef.current += 1
        loadingRef.current = false
        setEntries([])
        setNextCursor('')
    }, [])

    // namespace 初始化与切换（load 或 initialPath 变化，如切换
    // Source / Job）：reset 累积页与 cursor 并回到该 namespace 的初
    // 始目录；先使旧请求失效，避免首个必要请求被旧 loading 状态丢
    // 弃。浏览位置属于旧 namespace，同步通知外部当前目录——Source
    // 切换触发的重载不经过 openDirectory，不通知会让 picker 场景的
    // 确认按钮返回旧 namespace 的路径。目录导航（openDirectory）不
    // 走这里，用户选中的目录不因父组件重渲染被重置。
    useEffect(() => {
        invalidateView()
        void loadPage(initialPath, null)
        onPathChange?.(initialPath)
    }, [invalidateView, loadPage, initialPath, onPathChange])

    const openDirectory = useCallback(
        (target: string) => {
            invalidateView()
            void loadPage(target, null)
            onPathChange?.(target)
        },
        [invalidateView, loadPage, onPathChange],
    )

    const rowVirtualizer = useVirtualizer({
        count: entries.length,
        getScrollElement: () => scrollParentRef.current,
        estimateSize: () => 40,
        overscan: 12,
    })

    // 滚动接近末尾（剩余 8 行以内）且有下一页时续拉。
    const onScroll = useCallback(() => {
        const el = scrollParentRef.current
        if (el === null || nextCursor === '' || loadingRef.current) {
            return
        }
        const distanceToBottom = el.scrollHeight - el.scrollTop - el.clientHeight
        if (distanceToBottom < 8 * 40) {
            void loadPage(path, nextCursor)
        }
    }, [loadPage, nextCursor, path])

    const crumbs = useMemo(() => splitBreadcrumb(path), [path])

    return (
        <Box>
            <Box sx={{ display: 'flex', alignItems: 'center', mb: 1, minHeight: 32 }}>
                <Breadcrumbs maxItems={8} aria-label="breadcrumb">
                    {crumbs.map((crumb, index) => {
                        const isLast = index === crumbs.length - 1
                        return isLast ? (
                            <Typography key={crumb.path} variant="body2" color="text.primary">
                                {crumb.name}
                            </Typography>
                        ) : (
                            <Link
                                key={crumb.path}
                                component="button"
                                variant="body2"
                                onClick={() => openDirectory(crumb.path)}
                            >
                                {crumb.name}
                            </Link>
                        )
                    })}
                </Breadcrumbs>
                {loading && <CircularProgress size={16} sx={{ ml: 2 }} />}
            </Box>

            {error !== null && (
                <Alert severity="error" sx={{ mb: 1 }}>
                    {error}
                </Alert>
            )}

            <Box
                ref={scrollParentRef}
                onScroll={onScroll}
                sx={{
                    height,
                    overflowY: 'auto',
                    border: 1,
                    borderColor: 'divider',
                    borderRadius: 1,
                    bgcolor: 'background.paper',
                }}
            >
                {entries.length === 0 && !loading && error === null ? (
                    <Typography
                        variant="body2"
                        color="text.secondary"
                        sx={{ p: 3, textAlign: 'center' }}
                    >
                        {emptyHint}
                    </Typography>
                ) : (
                    <Box sx={{ position: 'relative', height: rowVirtualizer.getTotalSize() }}>
                        {rowVirtualizer.getVirtualItems().map(virtualRow => {
                            const entry = entries[virtualRow.index]
                            const enterable = entry.kind === 'directory'
                            const downloadable = entry.kind === 'file'
                            return (
                                <Box
                                    key={entry.path}
                                    sx={{
                                        position: 'absolute',
                                        top: 0,
                                        left: 0,
                                        width: '100%',
                                        height: virtualRow.size,
                                        transform: `translateY(${virtualRow.start}px)`,
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: 1,
                                        px: 1.5,
                                        borderBottom: 1,
                                        borderColor: 'divider',
                                    }}
                                >
                                    {kindIcon(entry.kind)}
                                    {enterable ? (
                                        <Link
                                            component="button"
                                            variant="body2"
                                            sx={{ flexGrow: 1, textAlign: 'left' }}
                                            onClick={() => openDirectory(entry.path)}
                                        >
                                            {entry.name}
                                        </Link>
                                    ) : (
                                        <Typography
                                            variant="body2"
                                            sx={{
                                                flexGrow: 1,
                                                overflow: 'hidden',
                                                textOverflow: 'ellipsis',
                                            }}
                                            title={entry.path}
                                        >
                                            {entry.name}
                                            {entry.kind === 'symlink' && ' →'}
                                        </Typography>
                                    )}
                                    {renderEntryExtra?.(entry)}
                                    {entry.kind === 'file' && (
                                        <Chip
                                            size="small"
                                            label={formatSize(entry.size)}
                                            variant="outlined"
                                        />
                                    )}
                                    {entry.modified_at !== null && entry.kind === 'file' && (
                                        <Typography variant="caption" color="text.secondary">
                                            {new Date(entry.modified_at).toLocaleString()}
                                        </Typography>
                                    )}
                                    {downloadable && (
                                        <IconButton
                                            size="small"
                                            aria-label={`download ${entry.name}`}
                                            href={downloadURL(entry.path)}
                                        >
                                            <DownloadIcon fontSize="small" />
                                        </IconButton>
                                    )}
                                </Box>
                            )
                        })}
                    </Box>
                )}
            </Box>
            {nextCursor !== '' && !loading && (
                <Typography
                    variant="caption"
                    color="text.secondary"
                    sx={{ mt: 0.5, display: 'block' }}
                >
                    滚动加载更多…
                </Typography>
            )}
        </Box>
    )
}
