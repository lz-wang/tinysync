import {
    Alert,
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
    type SourceResponse,
    type UpdateJobInput,
    updateJob,
} from '../../api'

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

// JobDialog 创建 / 编辑 Sync Job。Remote Root 只做路径文本输入
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
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (!open) {
            return
        }
        setName(job?.name ?? '')
        setSourceId(job?.source_id ?? '')
        setRemoteRoot(job?.remote_root ?? '/')
        setLocalRoot(job?.local_root ?? '')
        setMode(job?.mode ?? 'copy')
        setIncludeText(patternsToText(job?.include ?? []))
        setExcludeText(patternsToText(job?.exclude ?? []))
        setEnabled(job?.enabled ?? true)
        setSaving(false)
        setError(null)
    }, [open, job])

    const canSave =
        name.trim() !== '' && sourceId !== '' && localRoot.trim() !== '' && remoteRoot.trim() !== ''

    async function handleSave() {
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
            const updated = await updateJob(job.id, patch)
            onSaved(updated)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSaving(false)
        }
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
            <DialogTitle>{job === null ? 'Create Job' : 'Edit Job'}</DialogTitle>
            <DialogContent>
                <Stack spacing={2} sx={{ pt: 1 }}>
                    {error !== null && <Alert severity="error">{error}</Alert>}
                    <TextField
                        label="Name"
                        value={name}
                        onChange={e => setName(e.target.value)}
                        required
                        autoFocus
                    />
                    <TextField
                        select
                        label="Source"
                        value={sourceId}
                        onChange={e => setSourceId(e.target.value)}
                        required
                        helperText="Remote files are pulled from this source"
                    >
                        {sources.map(source => (
                            <MenuItem key={source.id} value={source.id}>
                                {source.name}
                                {source.enabled ? '' : ' (disabled)'}
                            </MenuItem>
                        ))}
                    </TextField>
                    <TextField
                        label="Remote Root"
                        value={remoteRoot}
                        onChange={e => setRemoteRoot(e.target.value)}
                        required
                        placeholder="/photos"
                        helperText="Absolute remote path to sync from, e.g. / or /backup/docs"
                        sx={{ '& input': { fontFamily: 'monospace' } }}
                    />
                    <TextField
                        label="Local Root"
                        value={localRoot}
                        onChange={e => setLocalRoot(e.target.value)}
                        required
                        placeholder="/data/photos"
                        helperText="Existing local directory; must not overlap other jobs"
                        sx={{ '& input': { fontFamily: 'monospace' } }}
                    />
                    <TextField
                        select
                        label="Mode"
                        value={mode}
                        onChange={e => setMode(e.target.value as JobMode)}
                        helperText="Mirror deletes local files that disappeared remotely (managed only)"
                    >
                        <MenuItem value="copy">Copy — never deletes local files</MenuItem>
                        <MenuItem value="mirror">Mirror — removes managed local files</MenuItem>
                    </TextField>
                    <TextField
                        label="Include Patterns"
                        value={includeText}
                        onChange={e => setIncludeText(e.target.value)}
                        multiline
                        minRows={3}
                        placeholder="**/*.jpg"
                        helperText="One glob per line; relative to Remote Root; empty includes all"
                        sx={{ '& textarea': { fontFamily: 'monospace' } }}
                    />
                    <TextField
                        label="Exclude Patterns"
                        value={excludeText}
                        onChange={e => setExcludeText(e.target.value)}
                        multiline
                        minRows={3}
                        placeholder="tmp/**"
                        helperText="One glob per line; exclude always wins over include"
                        sx={{ '& textarea': { fontFamily: 'monospace' } }}
                    />
                    <FormControlLabel
                        control={
                            <Switch
                                checked={enabled}
                                onChange={e => setEnabled(e.target.checked)}
                            />
                        }
                        label="Enabled"
                    />
                </Stack>
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={saving}>
                    Cancel
                </Button>
                <Button
                    onClick={() => void handleSave()}
                    variant="contained"
                    disabled={saving || !canSave}
                >
                    {saving ? 'Saving…' : 'Save'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
