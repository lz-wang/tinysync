import FolderOpenOutlinedIcon from '@mui/icons-material/FolderOpenOutlined'
import {
    Alert,
    Box,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    MenuItem,
    Stack,
    Switch,
    TextField,
} from '@mui/material'
import { useEffect, useState } from 'react'
import {
    createJob,
    type JobMode,
    type JobResponse,
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
    // 可选的 Source 列表（创建时选择归属）。
    sources: SourceResponse[]
    onClose: () => void
    onSaved: (job: JobResponse) => void
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

// JobDialog 创建 / 编辑 Sync Job：Source、Root、Mode、Selector 与
// Schedule 集中在同一个对话框。Remote Root 只做路径文本输入
//（远端文件浏览器属后续阶段）；Include / Exclude 为多行文本。
export default function JobDialog({ open, job, sources, onClose, onSaved }: JobDialogProps) {
    const [name, setName] = useState('')
    const [sourceId, setSourceId] = useState('')
    const [remoteRoot, setRemoteRoot] = useState('/')
    const [localRoot, setLocalRoot] = useState('')
    const [mode, setMode] = useState<JobMode>('copy')
    const [includeText, setIncludeText] = useState('')
    const [excludeText, setExcludeText] = useState('')
    const [enabled, setEnabled] = useState(true)
    const [scheduleType, setScheduleType] = useState<ScheduleType>('manual')
    const [onceAt, setOnceAt] = useState('')
    const [intervalEvery, setIntervalEvery] = useState('')
    const [cronExpression, setCronExpression] = useState('')
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [pickerOpen, setPickerOpen] = useState(false)
    const [localPickerOpen, setLocalPickerOpen] = useState(false)

    useEffect(() => {
        if (!open) {
            return
        }
        const schedule = job?.schedule
        setName(job?.name ?? '')
        setSourceId(job?.source_id ?? '')
        setRemoteRoot(job?.remote_root ?? '/')
        setLocalRoot(job?.local_root ?? '')
        setMode(job?.mode ?? 'copy')
        setIncludeText(patternsToText(job?.include ?? []))
        setExcludeText(patternsToText(job?.exclude ?? []))
        setEnabled(job?.enabled ?? true)
        setScheduleType(schedule?.type ?? 'manual')
        setOnceAt(toDatetimeLocal(schedule?.at))
        setIntervalEvery(schedule?.type === 'interval' ? (schedule.every ?? '') : '')
        setCronExpression(schedule?.type === 'cron' ? (schedule.expression ?? '') : '')
        setSaving(false)
        setError(null)
    }, [open, job])

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
    const canSave =
        name.trim() !== '' &&
        sourceId !== '' &&
        localRoot.trim() !== '' &&
        remoteRoot.trim() !== '' &&
        schedule !== null

    async function handleSave() {
        if (schedule === null) {
            return
        }
        setSaving(true)
        setError(null)
        const include = textToPatterns(includeText)
        const exclude = textToPatterns(excludeText)
        try {
            if (job === null) {
                const created = await createJob({
                    name,
                    source_id: sourceId,
                    remote_root: remoteRoot.trim() === '' ? '/' : remoteRoot.trim(),
                    local_root: localRoot.trim(),
                    mode,
                    include,
                    exclude,
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
            if (sourceId !== job.source_id) {
                patch.source_id = sourceId
            }
            if (remoteRoot.trim() !== job.remote_root) {
                patch.remote_root = remoteRoot.trim() === '' ? '/' : remoteRoot.trim()
            }
            if (localRoot.trim() !== job.local_root) {
                patch.local_root = localRoot.trim()
            }
            if (mode !== job.mode) {
                patch.mode = mode
            }
            if (include.join('\n') !== patternsToText(job.include)) {
                patch.include = include
            }
            if (exclude.join('\n') !== patternsToText(job.exclude)) {
                patch.exclude = exclude
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
                            onChange={e => setSourceId(e.target.value)}
                            required
                            helperText="从此同步源拉取远端文件"
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
                            helperText="镜像会删除远端已消失的受管理本地文件"
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
                                label="远端根目录"
                                value={remoteRoot}
                                onChange={e => setRemoteRoot(e.target.value)}
                                required
                                placeholder="/photos"
                                helperText="要同步的远端绝对路径"
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
                                sx={{ mt: 0.5, minWidth: 104, height: 48, flexShrink: 0 }}
                            >
                                浏览
                            </Button>
                        </Box>
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
                        <TextField
                            label="包含规则"
                            value={includeText}
                            onChange={e => setIncludeText(e.target.value)}
                            multiline
                            minRows={3}
                            placeholder="**/*.jpg"
                            helperText="每行一个 glob；留空包含全部"
                            sx={{ '& textarea': { fontFamily: 'monospace' } }}
                        />
                        <TextField
                            label="排除规则"
                            value={excludeText}
                            onChange={e => setExcludeText(e.target.value)}
                            multiline
                            minRows={3}
                            placeholder="tmp/**"
                            helperText="每行一个 glob；排除优先"
                            sx={{ '& textarea': { fontFamily: 'monospace' } }}
                        />
                    </Box>
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
