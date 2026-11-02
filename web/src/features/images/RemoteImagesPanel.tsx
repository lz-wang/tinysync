import AddTaskIcon from '@mui/icons-material/AddTask'
import ContentCopyOutlinedIcon from '@mui/icons-material/ContentCopyOutlined'
import DataObjectIcon from '@mui/icons-material/DataObject'
import SearchIcon from '@mui/icons-material/Search'
import {
    Alert,
    Autocomplete,
    Box,
    Button,
    Card,
    CardContent,
    Checkbox,
    Chip,
    CircularProgress,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    IconButton,
    ListItemButton,
    ListItemText,
    MenuItem,
    Stack,
    Tab,
    Table,
    TableBody,
    TableCell,
    TableRow,
    Tabs,
    TextField,
    Tooltip,
    Typography,
} from '@mui/material'
import { useEffect, useMemo, useRef, useState } from 'react'
import {
    type ImageInspectResponse,
    type ImagePlatformInspectResponse,
    type InspectPlatform,
    inspectImage,
    inspectImagePlatform,
    type JobResponse,
    listJobs,
    listRemoteTags,
    type OciPlatform,
} from '../../api'
import { useToast } from '../../app/toast'
import {
    formatSizeMB,
    normalizePlatformValue,
    platformKey,
    platformLabel,
    splitRepositoryAndTag,
} from './shared'

type TagSortMode = 'recommended' | 'name-desc' | 'name-asc'

interface RemoteImagesPanelProps {
    // onAddToTask 把所选镜像（repo:tag）与平台过滤交给页面层打开预填
    // JobDialog：job 为 null 表示新建，非 null 表示并入既有 oci 任务。
    onAddToTask: (input: {
        images: string[]
        platforms: OciPlatform[]
        job: JobResponse | null
    }) => void
}

interface TagsState {
    repo: string
    tags: string[]
    defaultTag: string
}

interface AddDraft {
    tags: string[]
    platforms: OciPlatform[]
    targetJobId: string
    jobs: JobResponse[]
}

// RemoteImagesPanel 是「远程镜像」面板（oci-sync InsightsPage 移植）：
// 远程搜索仓库 tag、inspect 镜像摘要与平台详情、元信息以纯文本 JSON
// 查看（R35：不引入 highlight.js），并把所选镜像带入任务表单。
// 竞态保护沿用 oci-sync 的序号计数：tags / inspect / 平台各自只接受
// 最新一次请求的结果。
export function RemoteImagesPanel({ onAddToTask }: RemoteImagesPanelProps) {
    const toast = useToast()
    const [query, setQuery] = useState('')
    const [tagSearch, setTagSearch] = useState('')
    const [tagSortMode, setTagSortMode] = useState<TagSortMode>('recommended')
    const [repo, setRepo] = useState('')
    const [selectedTag, setSelectedTag] = useState('')
    const [tagsState, setTagsState] = useState<TagsState | null>(null)
    const [tagsCursor, setTagsCursor] = useState('')
    const [tagsLoading, setTagsLoading] = useState(false)
    const [inspectData, setInspectData] = useState<ImageInspectResponse | null>(null)
    const [inspectLoading, setInspectLoading] = useState(false)
    const [platformInspectData, setPlatformInspectData] =
        useState<ImagePlatformInspectResponse | null>(null)
    const [platformInspectLoading, setPlatformInspectLoading] = useState(false)
    const [selectedPlatformKey, setSelectedPlatformKey] = useState('')
    const [jsonDialogOpen, setJsonDialogOpen] = useState(false)
    const [activeCodeTab, setActiveCodeTab] = useState(0)
    const [error, setError] = useState('')
    const [addDraft, setAddDraft] = useState<AddDraft | null>(null)
    const [addingJob, setAddingJob] = useState(false)
    const tagsSeq = useRef(0)
    const inspectSeq = useRef(0)
    const platformSeq = useRef(0)

    const filteredTags = useMemo(() => {
        if (tagsState === null) {
            return []
        }
        const keyword = tagSearch.trim().toLowerCase()
        const rank = (tag: string): number | null => {
            if (keyword === '') {
                return 0
            }
            const normalized = tag.toLowerCase()
            if (normalized === keyword) return 0
            if (normalized.startsWith(keyword)) return 1
            if (normalized.includes(keyword)) return 2
            return null
        }
        const recommendedPriority = (tag: string): number => {
            if (tag === selectedTag) return 0
            if (tag === 'latest') return 1
            if (tag.startsWith('stable')) return 2
            if (tag.startsWith('mainline')) return 3
            return 4
        }
        return tagsState.tags
            .map(tag => ({ tag, matchRank: rank(tag) }))
            .filter(item => item.matchRank !== null)
            .sort((a, b) => {
                if (a.matchRank !== b.matchRank) {
                    return (a.matchRank ?? 99) - (b.matchRank ?? 99)
                }
                if (tagSortMode === 'recommended') {
                    if (recommendedPriority(a.tag) !== recommendedPriority(b.tag)) {
                        return recommendedPriority(a.tag) - recommendedPriority(b.tag)
                    }
                    return b.tag.localeCompare(a.tag, undefined, { numeric: true })
                }
                const result = a.tag.localeCompare(b.tag, undefined, { numeric: true })
                return tagSortMode === 'name-desc' ? -result : result
            })
            .map(item => item.tag)
    }, [selectedTag, tagSearch, tagSortMode, tagsState])

    // filteredPlatforms 过滤掉 unknown 占位平台（oci-sync 同规则）。
    const filteredPlatforms = useMemo(() => {
        const platforms = inspectData?.result.platforms ?? []
        return platforms.filter(platform => {
            const os = normalizePlatformValue(platform.os)
            const architecture = normalizePlatformValue(platform.architecture)
            return os !== '' && architecture !== ''
        })
    }, [inspectData])

    const codeTabs = useMemo(() => {
        if (inspectData === null) {
            return []
        }
        const tabs: Array<{ name: string; code: string }> = []
        tabs.push({
            name: inspectData.result.is_index ? 'manifest-index.json' : 'manifest.json',
            code: stringifyJSON(inspectData.result.manifest),
        })
        if (inspectData.result.config !== undefined && inspectData.result.config !== null) {
            tabs.push({ name: 'config.json', code: stringifyJSON(inspectData.result.config) })
        }
        if (platformInspectData !== null) {
            const label = platformLabel(platformInspectData.platform)
                .replace(/[^a-z0-9.-]+/gi, '-')
                .toLowerCase()
            tabs.push({
                name: `${label}.platform-manifest.json`,
                code: stringifyJSON(platformInspectData.result.manifest),
            })
            if (
                platformInspectData.result.config !== undefined &&
                platformInspectData.result.config !== null
            ) {
                tabs.push({
                    name: `${label}.platform-config.json`,
                    code: stringifyJSON(platformInspectData.result.config),
                })
            }
        }
        return tabs
    }, [inspectData, platformInspectData])

    useEffect(() => {
        if (activeCodeTab >= codeTabs.length) {
            setActiveCodeTab(0)
        }
    }, [activeCodeTab, codeTabs.length])

    const loadInspect = async (targetRepo: string, tag: string) => {
        const seq = ++inspectSeq.current
        // 切换 tag 让在途的平台请求过期。
        platformSeq.current += 1
        try {
            setInspectLoading(true)
            setError('')
            setSelectedTag(tag)
            setSelectedPlatformKey('')
            setPlatformInspectData(null)
            const result = await inspectImage(`${targetRepo}:${tag}`)
            if (seq !== inspectSeq.current) return
            setInspectData(result)
        } catch (e) {
            if (seq !== inspectSeq.current) return
            setInspectData(null)
            setSelectedPlatformKey('')
            setPlatformInspectData(null)
            setError(e instanceof Error ? e.message : '加载镜像详情失败')
        } finally {
            if (seq === inspectSeq.current) {
                setInspectLoading(false)
            }
        }
    }

    const loadPlatformInspect = async (platform: InspectPlatform) => {
        if (inspectData === null) {
            return
        }
        const seq = ++platformSeq.current
        const nextKey = platformKey(platform)
        try {
            setPlatformInspectLoading(true)
            setError('')
            setSelectedPlatformKey(nextKey)
            const result = await inspectImagePlatform(inspectData.image, {
                os: normalizePlatformValue(platform.os),
                architecture: normalizePlatformValue(platform.architecture),
                variant: normalizePlatformValue(platform.variant) || undefined,
            })
            if (seq !== platformSeq.current) return
            setPlatformInspectData(result)
        } catch (e) {
            if (seq !== platformSeq.current) return
            setPlatformInspectData(null)
            setError(e instanceof Error ? e.message : '加载平台镜像详情失败')
        } finally {
            if (seq === platformSeq.current) {
                setPlatformInspectLoading(false)
            }
        }
    }

    const handleSearch = async () => {
        const parsed = splitRepositoryAndTag(query)
        if (parsed.repo === '') {
            setError('请输入镜像仓库，例如 nginx 或 docker.io/library/nginx')
            return
        }
        const seq = ++tagsSeq.current
        inspectSeq.current += 1
        platformSeq.current += 1
        try {
            setTagsLoading(true)
            setInspectLoading(false)
            setPlatformInspectLoading(false)
            setError('')
            setRepo(parsed.repo)
            setTagsState(null)
            setTagsCursor('')
            setInspectData(null)
            setPlatformInspectData(null)
            setSelectedPlatformKey('')
            setSelectedTag('')
            const page = await listRemoteTags(parsed.repo, { limit: 50 })
            if (seq !== tagsSeq.current) return
            const state: TagsState = {
                repo: parsed.repo,
                tags: page.items,
                defaultTag: page.items.includes('latest') ? 'latest' : (page.items[0] ?? ''),
            }
            setTagsState(state)
            setTagsCursor(page.has_more ? (page.next_cursor ?? '') : '')
            const nextTag = parsed.tag ?? state.defaultTag
            setSelectedTag(nextTag)
            setTagsLoading(false)
            if (nextTag !== '') {
                await loadInspect(state.repo, nextTag)
            }
        } catch (e) {
            if (seq !== tagsSeq.current) return
            setTagsState(null)
            setTagsCursor('')
            setInspectData(null)
            setPlatformInspectData(null)
            setSelectedPlatformKey('')
            setError(e instanceof Error ? e.message : '加载镜像 tags 失败')
        } finally {
            if (seq === tagsSeq.current) {
                setTagsLoading(false)
            }
        }
    }

    const loadMoreTags = async () => {
        if (tagsState === null || tagsCursor === '' || tagsLoading) {
            return
        }
        const seq = tagsSeq.current
        try {
            setTagsLoading(true)
            const page = await listRemoteTags(tagsState.repo, {
                limit: 50,
                cursor: tagsCursor,
            })
            if (seq !== tagsSeq.current) return
            setTagsState(prev =>
                prev === null
                    ? prev
                    : { ...prev, tags: [...new Set([...prev.tags, ...page.items])] },
            )
            setTagsCursor(page.has_more ? (page.next_cursor ?? '') : '')
        } catch (e) {
            if (seq === tagsSeq.current) {
                toast.error(e instanceof Error ? e.message : '加载下一页失败')
            }
        } finally {
            if (seq === tagsSeq.current) {
                setTagsLoading(false)
            }
        }
    }

    const openAddToTask = async () => {
        if (tagsState === null || repo === '') {
            return
        }
        setAddDraft({
            tags: selectedTag !== '' ? [selectedTag] : [],
            platforms:
                selectedPlatformKey !== ''
                    ? filteredPlatforms.filter(
                          platform => platformKey(platform) === selectedPlatformKey,
                      )
                    : [],
            targetJobId: 'new',
            jobs: [],
        })
        try {
            const jobs = await listJobs()
            setAddDraft(prev =>
                prev === null ? prev : { ...prev, jobs: jobs.filter(job => job.kind === 'oci') },
            )
        } catch (e) {
            toast.error(e instanceof Error ? e.message : '任务列表加载失败')
        }
    }

    const continueAddToTask = async () => {
        if (addDraft === null || addDraft.tags.length === 0) {
            return
        }
        setAddingJob(true)
        try {
            const job =
                addDraft.targetJobId === 'new'
                    ? null
                    : (addDraft.jobs.find(item => item.id === addDraft.targetJobId) ?? null)
            onAddToTask({
                images: addDraft.tags.map(tag => `${repo}:${tag}`),
                platforms: addDraft.platforms,
                job,
            })
            setAddDraft(null)
        } finally {
            setAddingJob(false)
        }
    }

    return (
        <Stack spacing={2}>
            <Card variant="outlined">
                <CardContent sx={{ py: 2, '&:last-child': { pb: 2 } }}>
                    <Box
                        component="form"
                        onSubmit={event => {
                            event.preventDefault()
                            void handleSearch()
                        }}
                    >
                        <Stack
                            direction={{ xs: 'column', sm: 'row' }}
                            spacing={1.5}
                            sx={{ alignItems: { sm: 'flex-end' } }}
                        >
                            <TextField
                                size="small"
                                label="镜像仓库"
                                placeholder="例如 nginx、library/nginx、docker.io/library/nginx:latest"
                                value={query}
                                onChange={event => setQuery(event.target.value)}
                                fullWidth
                            />
                            <Button
                                type="submit"
                                variant="contained"
                                startIcon={<SearchIcon />}
                                disabled={tagsLoading}
                                sx={{ whiteSpace: 'nowrap' }}
                            >
                                查询镜像
                            </Button>
                        </Stack>
                    </Box>
                    {repo !== '' && (
                        <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mt: 1.5 }}>
                            <Chip size="small" variant="outlined" color="primary" label="仓库" />
                            <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                                {repo}
                            </Typography>
                        </Stack>
                    )}
                    {error !== '' && (
                        <Alert severity="error" sx={{ mt: 1.5 }}>
                            {error}
                        </Alert>
                    )}
                </CardContent>
            </Card>
            <Box
                sx={{
                    display: 'grid',
                    gridTemplateColumns: { xs: '1fr', md: 'minmax(0, 1fr) minmax(0, 2fr)' },
                    gap: 2,
                    alignItems: 'start',
                }}
            >
                <Card variant="outlined">
                    <CardContent>
                        <Stack
                            direction="row"
                            spacing={1}
                            sx={{ alignItems: 'center', justifyContent: 'space-between' }}
                        >
                            <Typography variant="subtitle2">Tags</Typography>
                            {tagsLoading && <CircularProgress size={16} />}
                        </Stack>
                        <Typography
                            variant="caption"
                            color="text.secondary"
                            sx={{ display: 'block', mt: 0.5 }}
                        >
                            {tagsState !== null
                                ? `已加载 ${tagsState.tags.length} 个 tag，当前筛选 ${filteredTags.length} 个${tagsCursor !== '' ? '，还有下一页' : ''}`
                                : '输入仓库后分页查看 tags（需 Registry 可匿名访问或已配置凭据）'}
                        </Typography>
                        <Stack direction="row" spacing={1} sx={{ mt: 1.5 }}>
                            <TextField
                                size="small"
                                placeholder="筛选已加载的 tag"
                                value={tagSearch}
                                onChange={event => setTagSearch(event.target.value)}
                                disabled={tagsState === null}
                                fullWidth
                            />
                            <TextField
                                size="small"
                                select
                                label="排序"
                                value={tagSortMode}
                                onChange={event =>
                                    setTagSortMode(
                                        (event.target.value as TagSortMode) || 'recommended',
                                    )
                                }
                                disabled={tagsState === null}
                                sx={{ minWidth: 110 }}
                            >
                                <MenuItem value="recommended">推荐排序</MenuItem>
                                <MenuItem value="name-desc">名称降序</MenuItem>
                                <MenuItem value="name-asc">名称升序</MenuItem>
                            </TextField>
                        </Stack>
                        <Box sx={{ mt: 1.5, maxHeight: 420, overflow: 'auto', minHeight: 120 }}>
                            {tagsState !== null ? (
                                filteredTags.length > 0 ? (
                                    filteredTags.map(tag => (
                                        <ListItemButton
                                            key={tag}
                                            selected={tag === selectedTag}
                                            disabled={inspectLoading && tag === selectedTag}
                                            onClick={() => void loadInspect(tagsState.repo, tag)}
                                            sx={{ borderRadius: 1 }}
                                        >
                                            <ListItemText
                                                primary={tag}
                                                slotProps={{
                                                    primary: {
                                                        variant: 'body2',
                                                        sx: { fontFamily: 'monospace' },
                                                    },
                                                }}
                                            />
                                        </ListItemButton>
                                    ))
                                ) : (
                                    <Typography
                                        variant="body2"
                                        color="text.secondary"
                                        sx={{ py: 4, textAlign: 'center' }}
                                    >
                                        没有匹配的 tag
                                    </Typography>
                                )
                            ) : (
                                <Typography
                                    variant="body2"
                                    color="text.secondary"
                                    sx={{ py: 4, textAlign: 'center' }}
                                >
                                    尚未查询镜像仓库
                                </Typography>
                            )}
                        </Box>
                        {tagsCursor !== '' && (
                            <Button
                                onClick={() => void loadMoreTags()}
                                disabled={tagsLoading}
                                sx={{ mt: 1 }}
                            >
                                {tagsLoading ? '加载中…' : '加载下一页'}
                            </Button>
                        )}
                    </CardContent>
                </Card>
                <Card variant="outlined">
                    <CardContent>
                        <Stack
                            direction="row"
                            spacing={1}
                            sx={{ alignItems: 'center', justifyContent: 'space-between' }}
                        >
                            <Typography variant="subtitle2">镜像详情</Typography>
                            <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                                {inspectLoading && <CircularProgress size={16} />}
                                <Button
                                    size="small"
                                    variant="outlined"
                                    startIcon={<AddTaskIcon />}
                                    onClick={() => void openAddToTask()}
                                    disabled={tagsState === null || tagsState.tags.length === 0}
                                >
                                    添加到任务
                                </Button>
                                <Button
                                    size="small"
                                    variant="outlined"
                                    startIcon={<DataObjectIcon />}
                                    onClick={() => setJsonDialogOpen(true)}
                                    disabled={inspectData === null}
                                >
                                    查看元信息
                                </Button>
                            </Stack>
                        </Stack>
                        {inspectData !== null ? (
                            <Stack spacing={2} sx={{ mt: 1.5 }}>
                                <Stack
                                    direction="row"
                                    spacing={1}
                                    sx={{ flexWrap: 'wrap', gap: 1, alignItems: 'center' }}
                                >
                                    <Chip
                                        size="small"
                                        variant="outlined"
                                        color="primary"
                                        label={inspectData.result.tag}
                                    />
                                    <Chip
                                        size="small"
                                        variant="outlined"
                                        color={
                                            inspectData.result.is_index ? 'secondary' : 'success'
                                        }
                                        label={inspectData.result.is_index ? '多架构' : '单架构'}
                                    />
                                    <Chip
                                        size="small"
                                        variant="outlined"
                                        label={
                                            inspectData.result.mirror !== ''
                                                ? `Mirror: ${inspectData.result.mirror}`
                                                : 'Primary'
                                        }
                                    />
                                    {!inspectData.result.is_index && inspectData.result.size ? (
                                        <Chip
                                            size="small"
                                            variant="outlined"
                                            color="warning"
                                            label={formatSizeMB(inspectData.result.size)}
                                        />
                                    ) : null}
                                </Stack>
                                <Table size="small">
                                    <TableBody>
                                        <TableRow>
                                            <TableCell
                                                component="th"
                                                sx={{ width: 90, color: 'text.secondary' }}
                                            >
                                                Ref
                                            </TableCell>
                                            <TableCell
                                                sx={{
                                                    fontFamily: 'monospace',
                                                    wordBreak: 'break-all',
                                                }}
                                            >
                                                {inspectData.result.ref}
                                            </TableCell>
                                        </TableRow>
                                        <TableRow>
                                            <TableCell
                                                component="th"
                                                sx={{ width: 90, color: 'text.secondary' }}
                                            >
                                                Digest
                                            </TableCell>
                                            <TableCell
                                                sx={{
                                                    fontFamily: 'monospace',
                                                    wordBreak: 'break-all',
                                                }}
                                            >
                                                {inspectData.result.digest || '—'}
                                            </TableCell>
                                        </TableRow>
                                    </TableBody>
                                </Table>
                                <Box>
                                    <Typography variant="body2" sx={{ fontWeight: 600, mb: 1 }}>
                                        平台列表
                                    </Typography>
                                    {filteredPlatforms.length > 0 ? (
                                        <Stack
                                            direction="row"
                                            spacing={0.5}
                                            sx={{ flexWrap: 'wrap', gap: 0.5 }}
                                        >
                                            {filteredPlatforms.map(platform => {
                                                const key = platformKey(platform)
                                                const isSelected = selectedPlatformKey === key
                                                return (
                                                    <Chip
                                                        key={key || platformLabel(platform)}
                                                        size="small"
                                                        variant={isSelected ? 'filled' : 'outlined'}
                                                        color={isSelected ? 'primary' : 'default'}
                                                        label={platformLabel(platform)}
                                                        disabled={platformInspectLoading}
                                                        onClick={() => {
                                                            setSelectedPlatformKey(key)
                                                            if (inspectData !== null) {
                                                                void loadPlatformInspect(platform)
                                                            }
                                                        }}
                                                    />
                                                )
                                            })}
                                        </Stack>
                                    ) : (
                                        <Typography variant="body2" color="text.secondary">
                                            未返回平台信息
                                        </Typography>
                                    )}
                                </Box>
                                {platformInspectData !== null && (
                                    <Box>
                                        <Typography variant="body2" sx={{ fontWeight: 600, mb: 1 }}>
                                            已选平台镜像（
                                            {formatSizeMB(platformInspectData.result.size) ||
                                                '大小未知'}
                                            ）
                                        </Typography>
                                        <Table size="small">
                                            <TableBody>
                                                <TableRow>
                                                    <TableCell
                                                        component="th"
                                                        sx={{ width: 90, color: 'text.secondary' }}
                                                    >
                                                        Ref
                                                    </TableCell>
                                                    <TableCell
                                                        sx={{
                                                            fontFamily: 'monospace',
                                                            wordBreak: 'break-all',
                                                        }}
                                                    >
                                                        {platformInspectData.result.ref}
                                                    </TableCell>
                                                </TableRow>
                                                <TableRow>
                                                    <TableCell
                                                        component="th"
                                                        sx={{ width: 90, color: 'text.secondary' }}
                                                    >
                                                        Digest
                                                    </TableCell>
                                                    <TableCell
                                                        sx={{
                                                            fontFamily: 'monospace',
                                                            wordBreak: 'break-all',
                                                        }}
                                                    >
                                                        {platformInspectData.result.digest || '—'}
                                                    </TableCell>
                                                </TableRow>
                                            </TableBody>
                                        </Table>
                                    </Box>
                                )}
                            </Stack>
                        ) : (
                            <Typography
                                variant="body2"
                                color="text.secondary"
                                sx={{ py: 4, textAlign: 'center' }}
                            >
                                选择 tag 后查看镜像摘要
                            </Typography>
                        )}
                    </CardContent>
                </Card>
            </Box>

            <Dialog
                open={jsonDialogOpen}
                onClose={() => setJsonDialogOpen(false)}
                fullWidth
                maxWidth="lg"
            >
                <DialogTitle>镜像 JSON</DialogTitle>
                <DialogContent dividers sx={{ minHeight: 0 }}>
                    {codeTabs.length > 0 ? (
                        <>
                            <Tabs
                                value={Math.min(activeCodeTab, codeTabs.length - 1)}
                                onChange={(_, value: number) => setActiveCodeTab(value)}
                                variant="scrollable"
                                scrollButtons="auto"
                            >
                                {codeTabs.map((tab, index) => (
                                    <Tab
                                        key={tab.name}
                                        value={index}
                                        label={tab.name}
                                        sx={{
                                            fontFamily: 'monospace',
                                            fontSize: 13,
                                            textTransform: 'none',
                                        }}
                                    />
                                ))}
                            </Tabs>
                            <JsonViewer
                                code={codeTabs[Math.min(activeCodeTab, codeTabs.length - 1)].code}
                            />
                        </>
                    ) : (
                        <Typography variant="body2" color="text.secondary">
                            暂无可展示的元信息
                        </Typography>
                    )}
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => setJsonDialogOpen(false)}>关闭</Button>
                </DialogActions>
            </Dialog>

            <Dialog
                open={addDraft !== null}
                onClose={() => setAddDraft(null)}
                fullWidth
                maxWidth="sm"
            >
                <DialogTitle>添加镜像到任务</DialogTitle>
                <DialogContent dividers>
                    {addDraft !== null && (
                        <Stack spacing={2.5} sx={{ pt: 1 }}>
                            <Typography variant="body2" color="text.secondary">
                                从{' '}
                                <Box component="span" sx={{ fontFamily: 'monospace' }}>
                                    {repo}
                                </Box>{' '}
                                选择要同步的 tags 和平台；提交前仍可在任务表单中检查目标与调度。
                            </Typography>
                            <Autocomplete
                                multiple
                                disableCloseOnSelect
                                options={tagsState?.tags ?? []}
                                value={addDraft.tags}
                                onChange={(_, value) =>
                                    setAddDraft(prev =>
                                        prev === null ? prev : { ...prev, tags: value },
                                    )
                                }
                                renderInput={params => (
                                    <TextField
                                        {...params}
                                        required
                                        label="Tags"
                                        placeholder="选择一个或多个 tag"
                                        error={addDraft.tags.length === 0}
                                        helperText={
                                            addDraft.tags.length === 0
                                                ? '至少选择一个 tag'
                                                : '多个 tag 将作为同一镜像任务保存'
                                        }
                                    />
                                )}
                            />
                            <Box>
                                <Typography variant="body2" sx={{ fontWeight: 600, mb: 0.5 }}>
                                    平台过滤（可选）
                                </Typography>
                                <Typography
                                    variant="caption"
                                    color="text.secondary"
                                    sx={{ display: 'block', mb: 0.75 }}
                                >
                                    不选择即同步完整镜像；同一任务中的所有镜像共用该过滤条件。
                                </Typography>
                                {filteredPlatforms.length > 0 ? (
                                    <Stack
                                        direction="row"
                                        spacing={0.5}
                                        sx={{ flexWrap: 'wrap', gap: 0.5 }}
                                    >
                                        {filteredPlatforms.map(platform => {
                                            const key = platformKey(platform)
                                            const checked = addDraft.platforms.some(
                                                item => platformKey(item) === key,
                                            )
                                            return (
                                                <FormControlLabel
                                                    key={key}
                                                    control={
                                                        <Checkbox
                                                            size="small"
                                                            checked={checked}
                                                            onChange={event =>
                                                                setAddDraft(prev =>
                                                                    prev === null
                                                                        ? prev
                                                                        : {
                                                                              ...prev,
                                                                              platforms: event
                                                                                  .target.checked
                                                                                  ? [
                                                                                        ...prev.platforms,
                                                                                        platform,
                                                                                    ]
                                                                                  : prev.platforms.filter(
                                                                                        item =>
                                                                                            platformKey(
                                                                                                item,
                                                                                            ) !==
                                                                                            key,
                                                                                    ),
                                                                          },
                                                                )
                                                            }
                                                        />
                                                    }
                                                    label={platformLabel(platform)}
                                                    sx={{ mr: 1 }}
                                                />
                                            )
                                        })}
                                    </Stack>
                                ) : (
                                    <Typography variant="body2" color="text.secondary">
                                        当前 tag 未返回可选平台；将同步完整镜像。
                                    </Typography>
                                )}
                            </Box>
                            <TextField
                                select
                                label="目标任务"
                                value={addDraft.targetJobId}
                                onChange={event =>
                                    setAddDraft(prev =>
                                        prev === null
                                            ? prev
                                            : { ...prev, targetJobId: event.target.value },
                                    )
                                }
                                fullWidth
                                helperText={
                                    addDraft.targetJobId === 'new'
                                        ? '创建一个新任务并带入已选的 tags 和平台'
                                        : '将打开任务表单并合并镜像；平台选择会替换该任务的过滤条件'
                                }
                            >
                                <MenuItem value="new">新建任务</MenuItem>
                                {addDraft.jobs.map(job => (
                                    <MenuItem key={job.id} value={job.id}>
                                        {job.name}
                                    </MenuItem>
                                ))}
                            </TextField>
                        </Stack>
                    )}
                </DialogContent>
                <DialogActions>
                    <Button color="inherit" onClick={() => setAddDraft(null)}>
                        取消
                    </Button>
                    <Button
                        variant="contained"
                        onClick={() => void continueAddToTask()}
                        disabled={addDraft === null || addDraft.tags.length === 0 || addingJob}
                    >
                        {addDraft?.targetJobId === 'new' ? '继续新建' : '继续编辑'}
                    </Button>
                </DialogActions>
            </Dialog>
        </Stack>
    )
}

// stringifyJSON 宽松序列化（循环引用等异常回退字符串）。
function stringifyJSON(value: unknown): string {
    try {
        return JSON.stringify(value, null, 2)
    } catch {
        return String(value)
    }
}

// JsonViewer 是纯文本 JSON 查看器（R35：不引入 highlight.js，等宽样式
// 承载可读性），右上角提供复制。
function JsonViewer({ code }: { code: string }) {
    const [copied, setCopied] = useState(false)
    const handleCopy = async () => {
        try {
            await navigator.clipboard.writeText(code)
            setCopied(true)
            window.setTimeout(() => setCopied(false), 1500)
        } catch {
            // 剪贴板不可用时静默失败。
        }
    }
    return (
        <Box sx={{ position: 'relative', mt: 1.5 }}>
            <Tooltip title={copied ? '已复制' : '复制'}>
                <IconButton
                    size="small"
                    onClick={handleCopy}
                    sx={{ position: 'absolute', top: 8, right: 8, zIndex: 1 }}
                >
                    <ContentCopyOutlinedIcon fontSize="small" />
                </IconButton>
            </Tooltip>
            <Box
                component="pre"
                sx={{
                    m: 0,
                    p: 2,
                    pr: 5,
                    borderRadius: 1.5,
                    border: 1,
                    borderColor: 'divider',
                    overflow: 'auto',
                    maxHeight: 480,
                    fontSize: 13,
                    fontFamily: 'monospace',
                }}
            >
                <code>{code}</code>
            </Box>
        </Box>
    )
}
