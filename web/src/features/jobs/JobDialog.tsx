import AddIcon from '@mui/icons-material/Add'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlined'
import FolderOpenOutlinedIcon from '@mui/icons-material/FolderOpenOutlined'
import {
    Alert,
    Autocomplete,
    Box,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    IconButton,
    MenuItem,
    Stack,
    Switch,
    TextField,
    Tooltip,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useRef, useState } from 'react'
import {
    createJob,
    type FileJobConfig,
    type GitHubReleaseConfig,
    type JobKind,
    type JobMode,
    type JobResponse,
    listRegistries,
    type OciJobConfig,
    type OciPlatform,
    type RegistryResponse,
    type ScheduleSpec,
    type ScheduleType,
    type SourceResponse,
    type UpdateJobInput,
    updateJob,
} from '../../api'
import RemotePathPicker from '../files/RemotePathPicker'
import LocalDirectoryPicker from './LocalDirectoryPicker'

interface JobDialogProps {
    open: boolean
    // 编辑时传入现有 Job；创建时为 null。
    job: JobResponse | null
    // 可选的 Source 列表（创建 file 任务时选择归属）。
    sources: SourceResponse[]
    onClose: () => void
    onSaved: (job: JobResponse) => void
    // 创建态预填：从镜像管理「添加到任务」进入时锁定 oci 类型并带入
    // 源镜像与平台过滤。
    initialOciImages?: string[]
    initialOciPlatforms?: OciPlatform[]
}

// patternsToText 把 pattern 数组转为多行文本（一行一个）。
function patternsToText(patterns: string[]): string {
    return patterns.join('\n')
}

// textToPatterns 把多行文本解析回 pattern 数组：按行拆分、去除首尾空白、
// 丢弃空行。空数组语义为 include all / 无排除。
function textToPatterns(text: string): string[] {
    return text
        .split('\n')
        .map(line => line.trim())
        .filter(line => line !== '')
}

// toDatetimeLocal 把 RFC3339 时间转为 datetime-local 输入值（本地
// 时区，保留秒）：打开编辑器不丢秒精度，未改动保存不改变触发时刻。
function toDatetimeLocal(value?: string): string {
    if (value === undefined || value === '') {
        return ''
    }
    const d = new Date(value)
    if (Number.isNaN(d.getTime())) {
        return ''
    }
    const pad = (n: number): string => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

// scheduleEquals 比较两个调度配置的用户语义：once 比较绝对时刻
//（毫秒精度表示差异如 …00Z 与 …00.000Z 不算变化），interval / cron
// 比较配置值。仅语义变化时才在 PATCH 中携带 schedule，避免无意义
// 的调度更新（interval 相位会随语义变更重置）。
function scheduleEquals(a: ScheduleSpec, b: ScheduleSpec): boolean {
    if (a.type !== b.type) {
        return false
    }
    switch (a.type) {
        case 'once': {
            const ta = new Date(a.at ?? '').getTime()
            const tb = new Date(b.at ?? '').getTime()
            return ta === tb
        }
        case 'interval':
            return (a.every ?? '') === (b.every ?? '')
        case 'cron':
            return (
                (a.expression ?? '') === (b.expression ?? '') &&
                (a.timezone ?? '') === (b.timezone ?? '')
            )
        default:
            return true
    }
}

// ociConfigKey 把 oci 配置压缩为可比较的键（数组顺序即语义顺序，
// 平台按行序比较；后端 Validate 落库前会做同样的归一化展开）。
function ociConfigKey(config: OciJobConfig): string {
    const platforms = config.platforms
        .map(p => [p.os ?? '', p.architecture ?? '', p.variant ?? ''].join('|'))
        .join(';')
    return `${config.src_images.join('\n')}#${config.dst_servers.join('\n')}#${platforms}`
}

// PlatformRow 是平台过滤的编辑行：rowKey 是行身份（行内容可重复——
// 例如两行都未选架构——数组 index 不能作 React key），保存时剥离。
interface PlatformRow extends OciPlatform {
    rowKey: number
}

// JobDialog 创建 / 编辑统一任务：类型（file / oci，git 规划中）+ 各自
// 领域配置 + 调度。file 区维持原有的派生门校验（R34：不为统一而重写）；
// oci 区采用 oci-sync 式 per-field validate() 错误映射——同一对话框两种
// 校验风格并存是有意取舍。调度 UI、路径选择器与 diff-PATCH 保存流共用。
export default function JobDialog({
    open,
    job,
    sources,
    onClose,
    onSaved,
    initialOciImages,
    initialOciPlatforms,
}: JobDialogProps) {
    const [kind, setKind] = useState<JobKind>('file')
    const [name, setName] = useState('')
    const [sourceId, setSourceId] = useState('')
    const [remoteRoot, setRemoteRoot] = useState('/')
    const [localRoot, setLocalRoot] = useState('')
    const [mode, setMode] = useState<JobMode>('copy')
    const [includeText, setIncludeText] = useState('')
    const [excludeText, setExcludeText] = useState('')
    const [srcImages, setSrcImages] = useState<string[]>([])
    const [dstServers, setDstServers] = useState<string[]>([])
    const [platforms, setPlatforms] = useState<PlatformRow[]>([])
    const [registries, setRegistries] = useState<RegistryResponse[]>([])
    const [ociErrors, setOciErrors] = useState<Record<string, string>>({})
    const [enabled, setEnabled] = useState(true)
    const [scheduleType, setScheduleType] = useState<ScheduleType>('manual')
    const [onceAt, setOnceAt] = useState('')
    const [intervalEvery, setIntervalEvery] = useState('')
    const [cronExpression, setCronExpression] = useState('')
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [pickerOpen, setPickerOpen] = useState(false)
    const [localPickerOpen, setLocalPickerOpen] = useState(false)
    // platformRowKey 递增分配平台行的稳定身份（跨打开重置不复位无妨）；
    // useCallback 稳定身份避免重置 effect 依赖抖动。
    const platformRowKey = useRef(0)
    const nextPlatformRowKey = useCallback(() => {
        platformRowKey.current += 1
        return platformRowKey.current
    }, [])

    useEffect(() => {
        if (!open) {
            return
        }
        const schedule = job?.schedule
        const config = job?.config
        const jobKind: JobKind = job === null ? (initialOciImages ? 'oci' : 'file') : job.kind
        setKind(jobKind)
        setName(job?.name ?? '')
        setSourceId(config !== undefined && 'source_id' in config ? config.source_id : '')
        setRemoteRoot(config !== undefined && 'remote_root' in config ? config.remote_root : '/')
        setLocalRoot(config !== undefined && 'local_root' in config ? config.local_root : '')
        setMode(config !== undefined && 'mode' in config ? config.mode : 'copy')
        setIncludeText(
            patternsToText(config !== undefined && 'include' in config ? config.include : []),
        )
        setExcludeText(
            patternsToText(config !== undefined && 'exclude' in config ? config.exclude : []),
        )
        setSrcImages(
            config !== undefined && 'src_images' in config
                ? Array.from(new Set([...config.src_images, ...(initialOciImages ?? [])]))
                : (initialOciImages ?? []),
        )
        setDstServers(config !== undefined && 'dst_servers' in config ? config.dst_servers : [])
        const platformRows =
            config !== undefined && 'platforms' in config
                ? (initialOciPlatforms ?? config.platforms)
                : (initialOciPlatforms ?? [])
        setPlatforms(platformRows.map(row => ({ ...row, rowKey: nextPlatformRowKey() })))
        setOciErrors({})
        setEnabled(job?.enabled ?? true)
        setScheduleType(schedule?.type ?? 'manual')
        setOnceAt(toDatetimeLocal(schedule?.at))
        setIntervalEvery(schedule?.type === 'interval' ? (schedule.every ?? '') : '')
        setCronExpression(schedule?.type === 'cron' ? (schedule.expression ?? '') : '')
        setSaving(false)
        setError(null)
    }, [open, job, initialOciImages, initialOciPlatforms, nextPlatformRowKey])

    // oci 区需要 primary Registry 候选（dst_servers 只能引用 primary 行）；
    // 静默失败：加载失败时下拉无候选且无法手输（未开 freeSolo），保存会
    // 因 dst_servers 为空被前端校验拦下；若编辑态已选值因列表为空而不可
    // 见，未改动保存不发 config，不会误清配置。
    useEffect(() => {
        if (!open || kind !== 'oci') {
            return
        }
        let active = true
        listRegistries()
            .then(rows => {
                if (active) {
                    setRegistries(rows.filter(row => row.kind === 'primary'))
                }
            })
            .catch(() => undefined)
        return () => {
            active = false
        }
    }, [open, kind])

    // buildSchedule 组装当前输入的调度配置；输入不完整时返回 null
    //（语义合法性由后端校验）。
    function buildSchedule(): ScheduleSpec | null {
        switch (scheduleType) {
            case 'manual':
                return { type: 'manual' }
            case 'once': {
                if (onceAt === '') {
                    return null
                }
                const at = new Date(onceAt)
                if (Number.isNaN(at.getTime())) {
                    return null
                }
                return { type: 'once', at: at.toISOString() }
            }
            case 'interval':
                return intervalEvery.trim() === ''
                    ? null
                    : { type: 'interval', every: intervalEvery.trim() }
            case 'cron': {
                if (cronExpression.trim() === '') {
                    return null
                }
                return { type: 'cron', expression: cronExpression.trim() }
            }
        }
    }

    const schedule = buildSchedule()

    // validateOci 按 R34 做 per-field 校验并写入错误映射；返回是否通过。
    function validateOci(): boolean {
        const next: Record<string, string> = {}
        if (srcImages.length === 0) {
            next.srcImages = '至少添加一个源镜像'
        }
        if (dstServers.length === 0) {
            next.dstServers = '至少选择一个目标 Registry'
        }
        if (platforms.some(p => (p.architecture ?? '').trim() === '')) {
            next.platforms = '平台过滤的每一行都必须选择架构'
        }
        setOciErrors(next)
        return Object.keys(next).length === 0
    }

    const fileCanSave =
        name.trim() !== '' &&
        sourceId !== '' &&
        localRoot.trim() !== '' &&
        remoteRoot.trim() !== '' &&
        schedule !== null
    // oci 区保存按钮不做派生门：允许点击保存后逐字段展示错误（R34 风格），
    // 仅保留名称与调度这两个共享字段的硬门。
    const canSave =
        schedule !== null && (kind === 'file' ? fileCanSave : name.trim() !== '') && kind !== 'git'

    // 选定 GitHub Release Source 后的针对性适配（契约见设计文档 §4.2）：
    // 动态版本策略（latest / recent / all）的源端路径固定为 /——
    // 选定某个具体版本目录会导致下一版本发布后不再跟随；tag 模式允许
    // 浏览选择该版本目录。Mirror 在版本轮换时会删除退出选择范围的
    // 旧版本目录，需明确提示。
    const selectedSource = sources.find(source => source.id === sourceId) ?? null
    const isGitHubSource = selectedSource?.type === 'github_release'
    const githubDynamicRoot =
        isGitHubSource &&
        selectedSource !== null &&
        ['latest', 'recent', 'all'].includes(
            (selectedSource.config as GitHubReleaseConfig).release_policy,
        )

    // updatePlatform 修改一行平台过滤；清错避免遗留整行级错误提示。
    function updatePlatform(index: number, patch: Partial<OciPlatform>) {
        setPlatforms(prev => prev.map((p, i) => (i === index ? { ...p, ...patch } : p)))
        setOciErrors(prev => {
            if (!prev.platforms) {
                return prev
            }
            const next = { ...prev }
            delete next.platforms
            return next
        })
    }

    async function handleSave() {
        if (schedule === null || kind === 'git') {
            return
        }
        if (kind === 'oci' && !validateOci()) {
            return
        }
        setSaving(true)
        setError(null)
        try {
            if (kind === 'oci') {
                const config: OciJobConfig = {
                    src_images: srcImages.map(image => image.trim()).filter(image => image !== ''),
                    dst_servers: dstServers,
                    platforms: platforms
                        .map(p => ({
                            os: p.os ?? 'linux',
                            architecture: (p.architecture ?? '').trim(),
                            variant: (p.variant ?? '').trim(),
                        }))
                        .filter(p => p.architecture !== ''),
                }
                if (job === null) {
                    const created = await createJob({
                        name,
                        kind: 'oci',
                        config,
                        enabled,
                        schedule,
                    })
                    onSaved(created)
                    return
                }
                const patch: UpdateJobInput = {}
                if (name !== job.name) {
                    patch.name = name
                }
                const current = job.config as OciJobConfig
                if ('src_images' in current && ociConfigKey(config) !== ociConfigKey(current)) {
                    patch.config = config
                }
                if (enabled !== job.enabled) {
                    patch.enabled = enabled
                }
                if (!scheduleEquals(schedule, job.schedule)) {
                    patch.schedule = schedule
                }
                const updated = await updateJob(job.id, patch)
                onSaved(updated)
                return
            }
            const include = textToPatterns(includeText)
            const exclude = textToPatterns(excludeText)
            const config: FileJobConfig = {
                source_id: sourceId,
                remote_root: remoteRoot.trim() === '' ? '/' : remoteRoot.trim(),
                local_root: localRoot.trim(),
                mode,
                include,
                exclude,
            }
            if (job === null) {
                const created = await createJob({
                    name,
                    kind: 'file',
                    config,
                    enabled,
                    schedule,
                })
                onSaved(created)
                return
            }
            const patch: UpdateJobInput = {}
            if (name !== job.name) {
                patch.name = name
            }
            const current = job.config
            if (
                'source_id' in current &&
                (config.source_id !== current.source_id ||
                    config.remote_root !== current.remote_root ||
                    config.local_root !== current.local_root ||
                    config.mode !== current.mode ||
                    include.join('\n') !== patternsToText(current.include) ||
                    exclude.join('\n') !== patternsToText(current.exclude))
            ) {
                patch.config = config
            }
            if (enabled !== job.enabled) {
                patch.enabled = enabled
            }
            if (!scheduleEquals(schedule, job.schedule)) {
                patch.schedule = schedule
            }
            const updated = await updateJob(job.id, patch)
            onSaved(updated)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSaving(false)
        }
    }

    const scheduleFields = (
        <>
            <TextField
                select
                label="运行计划"
                value={scheduleType}
                onChange={e => setScheduleType(e.target.value as ScheduleType)}
                helperText="自动触发与手动运行遵循相同安全规则"
            >
                <MenuItem value="manual">手动 — 仅按需运行</MenuItem>
                <MenuItem value="once">单次 — 在指定时间运行</MenuItem>
                <MenuItem value="interval">间隔 — 按固定周期运行</MenuItem>
                <MenuItem value="cron">Cron — 五段表达式</MenuItem>
            </TextField>
            {scheduleType === 'manual' && <Box />}
            {scheduleType === 'once' && (
                <TextField
                    label="运行时间"
                    type="datetime-local"
                    slotProps={{
                        input: { inputProps: { step: 1 } },
                        inputLabel: { shrink: true },
                    }}
                    value={onceAt}
                    onChange={e => setOnceAt(e.target.value)}
                    helperText="服务恢复后会补跑一次错过的任务"
                />
            )}
            {scheduleType === 'interval' && (
                <TextField
                    label="间隔"
                    value={intervalEvery}
                    onChange={e => setIntervalEvery(e.target.value)}
                    placeholder="30m"
                    helperText="Go 时长，最小 1m（如 30m、6h）"
                    sx={{ '& input': { fontFamily: 'monospace' } }}
                />
            )}
            {scheduleType === 'cron' && (
                <TextField
                    label="Cron 表达式"
                    value={cronExpression}
                    onChange={e => setCronExpression(e.target.value)}
                    placeholder="0 3 * * *"
                    helperText="标准五段表达式，使用运行机器所在时区"
                    sx={{ minWidth: 0, '& input': { fontFamily: 'monospace' } }}
                />
            )}
        </>
    )

    return (
        <Dialog
            open={open}
            onClose={onClose}
            scroll="paper"
            slotProps={{
                paper: {
                    sx: {
                        width: '60vw',
                        height: '80vh',
                        maxWidth: 'none',
                        maxHeight: 'none',
                    },
                },
            }}
        >
            <DialogTitle>{job === null ? '创建同步任务' : '编辑同步任务'}</DialogTitle>
            <DialogContent sx={{ flex: 1, overflowY: 'auto' }}>
                <Stack spacing={2} sx={{ pt: 1 }}>
                    {error !== null && <Alert severity="error">{error}</Alert>}
                    <TextField
                        label="名称"
                        value={name}
                        onChange={e => setName(e.target.value)}
                        required
                        autoFocus
                    />
                    <TextField
                        select
                        label="任务类型"
                        value={kind}
                        onChange={e => {
                            setKind(e.target.value as JobKind)
                            setOciErrors({})
                        }}
                        disabled={job !== null}
                        helperText={job !== null ? '任务类型创建后不可更改' : 'Git 仓库同步规划中'}
                    >
                        <MenuItem value="file">文件同步</MenuItem>
                        <MenuItem value="oci">OCI 镜像同步</MenuItem>
                        <MenuItem value="git" disabled>
                            Git 仓库（规划中）
                        </MenuItem>
                    </TextField>
                    {kind === 'file' ? (
                        <Box
                            sx={{
                                display: 'grid',
                                gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
                                gap: 2,
                            }}
                        >
                            <TextField
                                select
                                label="同步源"
                                value={sourceId}
                                onChange={e => {
                                    setSourceId(e.target.value)
                                    // 切到 GitHub Release 动态策略时固定根目录，
                                    // 避免残留其它协议的子路径。
                                    const next = sources.find(
                                        source => source.id === e.target.value,
                                    )
                                    if (
                                        next?.type === 'github_release' &&
                                        ['latest', 'recent', 'all'].includes(
                                            (next.config as GitHubReleaseConfig).release_policy,
                                        )
                                    ) {
                                        setRemoteRoot('/')
                                    }
                                }}
                                required
                                helperText="从此同步源拉取源端文件"
                            >
                                {sources.map(source => (
                                    <MenuItem key={source.id} value={source.id}>
                                        {source.name}
                                        {source.enabled ? '' : '（已停用）'}
                                    </MenuItem>
                                ))}
                            </TextField>
                            <TextField
                                select
                                label="同步规则"
                                value={mode}
                                onChange={e => setMode(e.target.value as JobMode)}
                                helperText="镜像会删除源端已消失的受管理本地文件"
                            >
                                <MenuItem value="copy">复制 — 不删除本地文件</MenuItem>
                                <MenuItem value="mirror">镜像 — 删除受管理本地文件</MenuItem>
                            </TextField>
                            <Box
                                sx={{
                                    display: 'flex',
                                    gap: 1.5,
                                    alignItems: 'flex-start',
                                    minWidth: 0,
                                }}
                            >
                                <TextField
                                    label="源端路径"
                                    value={remoteRoot}
                                    onChange={e => setRemoteRoot(e.target.value)}
                                    required
                                    disabled={githubDynamicRoot}
                                    placeholder="/photos"
                                    helperText={
                                        githubDynamicRoot
                                            ? '动态版本策略固定为 /，始终跟随最新选中版本'
                                            : '要同步的源端逻辑路径'
                                    }
                                    sx={{
                                        flex: 1,
                                        minWidth: 0,
                                        '& input': { fontFamily: 'monospace' },
                                    }}
                                />
                                <Button
                                    variant="outlined"
                                    size="large"
                                    startIcon={<FolderOpenOutlinedIcon />}
                                    onClick={() => setPickerOpen(true)}
                                    disabled={githubDynamicRoot}
                                    sx={{ mt: 0.5, minWidth: 104, height: 48, flexShrink: 0 }}
                                >
                                    浏览
                                </Button>
                            </Box>
                            {isGitHubSource && mode === 'mirror' && (
                                <Alert severity="warning">
                                    镜像模式 + GitHub Release：当新版本进入选择范围、旧版本退出后，
                                    旧版本的本地文件将被删除（版本清理）。如需永久保留旧版本，请使用
                                    复制模式。
                                </Alert>
                            )}
                            <Box
                                sx={{
                                    display: 'flex',
                                    gap: 1.5,
                                    alignItems: 'flex-start',
                                    minWidth: 0,
                                }}
                            >
                                <TextField
                                    label="本地根目录"
                                    value={localRoot}
                                    onChange={e => setLocalRoot(e.target.value)}
                                    required
                                    placeholder="/data/photos"
                                    helperText="不存在时自动创建，且不能与其他任务重叠"
                                    sx={{
                                        flex: 1,
                                        minWidth: 0,
                                        '& input': { fontFamily: 'monospace' },
                                    }}
                                />
                                <Button
                                    variant="outlined"
                                    size="large"
                                    startIcon={<FolderOpenOutlinedIcon />}
                                    onClick={() => setLocalPickerOpen(true)}
                                    sx={{ mt: 0.5, minWidth: 104, height: 48, flexShrink: 0 }}
                                >
                                    浏览
                                </Button>
                            </Box>
                            {scheduleFields}
                            <TextField
                                label="包含规则"
                                value={includeText}
                                onChange={e => setIncludeText(e.target.value)}
                                multiline
                                minRows={3}
                                placeholder={isGitHubSource ? '**/*linux-amd64*' : '**/*.jpg'}
                                helperText={
                                    isGitHubSource
                                        ? '针对包含版本目录的相对路径：**/*linux-amd64* 匹配所有版本下的制品；单段 *.zip 无法命中版本子目录。留空包含全部'
                                        : '每行一个 glob；留空包含全部'
                                }
                                sx={{ '& textarea': { fontFamily: 'monospace' } }}
                            />
                            <TextField
                                label="排除规则"
                                value={excludeText}
                                onChange={e => setExcludeText(e.target.value)}
                                multiline
                                minRows={3}
                                placeholder={isGitHubSource ? '**/*.sig' : 'tmp/**'}
                                helperText="每行一个 glob；排除优先"
                                sx={{ '& textarea': { fontFamily: 'monospace' } }}
                            />
                        </Box>
                    ) : (
                        <Stack spacing={2}>
                            <Autocomplete
                                multiple
                                freeSolo
                                options={[] as string[]}
                                value={srcImages}
                                onChange={(_, value) => {
                                    setSrcImages(value)
                                    setOciErrors(prev => {
                                        if (!prev.srcImages) {
                                            return prev
                                        }
                                        const next = { ...prev }
                                        delete next.srcImages
                                        return next
                                    })
                                }}
                                renderInput={params => (
                                    <TextField
                                        {...params}
                                        label="源镜像"
                                        required
                                        placeholder="输入镜像后按回车添加，如 nginx:1.27"
                                        error={ociErrors.srcImages !== undefined}
                                        helperText={
                                            ociErrors.srcImages ??
                                            '支持 repo、repo:tag 与 tag 逗号展开（如 nginx:1.27,1.28）'
                                        }
                                    />
                                )}
                            />
                            <Autocomplete
                                multiple
                                options={registries}
                                getOptionLabel={option => `${option.name}（${option.host}）`}
                                isOptionEqualToValue={(option, value) => option.name === value.name}
                                value={registries.filter(row => dstServers.includes(row.name))}
                                onChange={(_, values) => {
                                    setDstServers(values.map(row => row.name))
                                    setOciErrors(prev => {
                                        if (!prev.dstServers) {
                                            return prev
                                        }
                                        const next = { ...prev }
                                        delete next.dstServers
                                        return next
                                    })
                                }}
                                renderInput={params => (
                                    <TextField
                                        {...params}
                                        label="目标 Registry"
                                        required
                                        placeholder="选择目标 Registry 配置"
                                        error={ociErrors.dstServers !== undefined}
                                        helperText={
                                            ociErrors.dstServers ??
                                            '在「同步源」页的 Registry 分区维护目标配置'
                                        }
                                    />
                                )}
                            />
                            <Box>
                                <Stack
                                    direction="row"
                                    sx={{ justifyContent: 'space-between', alignItems: 'center' }}
                                >
                                    <Typography variant="body2" sx={{ fontWeight: 500 }}>
                                        平台过滤（可选）
                                    </Typography>
                                    <Button
                                        size="small"
                                        startIcon={<AddIcon />}
                                        onClick={() =>
                                            setPlatforms(prev => [
                                                ...prev,
                                                {
                                                    os: 'linux',
                                                    architecture: '',
                                                    variant: '',
                                                    rowKey: nextPlatformRowKey(),
                                                },
                                            ])
                                        }
                                    >
                                        添加平台
                                    </Button>
                                </Stack>
                                <Typography
                                    variant="caption"
                                    sx={{
                                        color:
                                            ociErrors.platforms !== undefined
                                                ? 'error.main'
                                                : 'text.secondary',
                                        display: 'block',
                                        mt: 0.5,
                                    }}
                                >
                                    {ociErrors.platforms ??
                                        '不填写则同步所有平台，填写则只同步指定平台'}
                                </Typography>
                                <Stack spacing={1} sx={{ mt: 1.5 }}>
                                    {platforms.map((platform, index) => (
                                        <Stack
                                            key={platform.rowKey}
                                            direction="row"
                                            spacing={1}
                                            sx={{ alignItems: 'center' }}
                                        >
                                            <TextField
                                                select
                                                size="small"
                                                label="OS"
                                                value={platform.os || 'linux'}
                                                onChange={e =>
                                                    updatePlatform(index, {
                                                        os: e.target.value || 'linux',
                                                    })
                                                }
                                                sx={{ flex: 1, minWidth: 96 }}
                                            >
                                                <MenuItem value="linux">linux</MenuItem>
                                                <MenuItem value="windows">windows</MenuItem>
                                            </TextField>
                                            <TextField
                                                select
                                                size="small"
                                                required
                                                label="架构"
                                                value={platform.architecture || ''}
                                                onChange={e =>
                                                    updatePlatform(index, {
                                                        architecture: e.target.value,
                                                    })
                                                }
                                                error={
                                                    (platform.architecture ?? '') === '' &&
                                                    ociErrors.platforms !== undefined
                                                }
                                                sx={{ flex: 2, minWidth: 120 }}
                                            >
                                                <MenuItem value="">请选择…</MenuItem>
                                                <MenuItem value="amd64">amd64</MenuItem>
                                                <MenuItem value="arm64">arm64</MenuItem>
                                                <MenuItem value="arm">arm</MenuItem>
                                                <MenuItem value="386">386</MenuItem>
                                            </TextField>
                                            <TextField
                                                size="small"
                                                label="变体"
                                                placeholder="如 v8"
                                                value={platform.variant || ''}
                                                onChange={e =>
                                                    updatePlatform(index, {
                                                        variant: e.target.value,
                                                    })
                                                }
                                                sx={{ flex: 1, minWidth: 96 }}
                                            />
                                            <Tooltip title="移除">
                                                <IconButton
                                                    size="small"
                                                    color="error"
                                                    aria-label={`移除平台 ${index + 1}`}
                                                    onClick={() =>
                                                        setPlatforms(prev =>
                                                            prev.filter((_, i) => i !== index),
                                                        )
                                                    }
                                                >
                                                    <DeleteOutlineIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                        </Stack>
                                    ))}
                                </Stack>
                            </Box>
                            {scheduleFields}
                        </Stack>
                    )}
                </Stack>
            </DialogContent>
            <DialogActions sx={{ px: 3 }}>
                <FormControlLabel
                    sx={{ mr: 'auto' }}
                    control={
                        <Switch checked={enabled} onChange={e => setEnabled(e.target.checked)} />
                    }
                    label="启用"
                />
                <Button onClick={onClose} disabled={saving}>
                    取消
                </Button>
                <Button
                    onClick={() => void handleSave()}
                    variant="contained"
                    disabled={saving || !canSave}
                >
                    {saving ? '保存中…' : '保存'}
                </Button>
            </DialogActions>
            <RemotePathPicker
                open={pickerOpen}
                onClose={() => setPickerOpen(false)}
                onPick={path => {
                    setRemoteRoot(path)
                    setPickerOpen(false)
                }}
                boundSourceId={sourceId}
                initialPath={remoteRoot}
                readOnly={isGitHubSource}
            />
            <LocalDirectoryPicker
                open={localPickerOpen}
                initialPath={localRoot}
                onClose={() => setLocalPickerOpen(false)}
                onPick={path => {
                    setLocalRoot(path)
                    setLocalPickerOpen(false)
                }}
            />
        </Dialog>
    )
}
