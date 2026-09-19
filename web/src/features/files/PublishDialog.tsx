import {
    Alert,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    Stack,
    Switch,
    TextField,
} from '@mui/material'
import { useState } from 'react'
import { createPublished } from '../../api'

// PublishDialog 是发布策略创建对话框：目标为 Local 浏览中选中的
// managed 文件；public path 缺省沿用文件逻辑路径。
export default function PublishDialog({
    open,
    jobId,
    entryPath,
    onClose,
    onCreated,
}: {
    open: boolean
    jobId: string
    // entryPath 是选中的 LocalRoot 内逻辑路径（kind=file）。
    entryPath: string
    onClose: () => void
    onCreated: () => void
}) {
    const [publicPath, setPublicPath] = useState(entryPath)
    const [expiresAt, setExpiresAt] = useState('')
    const [enabled, setEnabled] = useState(true)
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)

    const handleSave = async () => {
        setSaving(true)
        setError(null)
        try {
            await createPublished({
                job_id: jobId,
                path: entryPath,
                public_path: publicPath.trim() === '' ? entryPath : publicPath.trim(),
                enabled,
                expires_at: expiresAt === '' ? undefined : new Date(expiresAt).toISOString(),
            })
            onCreated()
            onClose()
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        } finally {
            setSaving(false)
        }
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
            <DialogTitle>发布文件</DialogTitle>
            <DialogContent>
                <Stack spacing={2} sx={{ mt: 1 }}>
                    {error !== null && <Alert severity="error">{error}</Alert>}
                    <TextField
                        label="源文件"
                        value={entryPath}
                        disabled
                        sx={{ '& input': { fontFamily: 'monospace' } }}
                        helperText="仅可发布 TinySync 管理的文件"
                    />
                    <TextField
                        label="公开路径"
                        value={publicPath}
                        onChange={event => setPublicPath(event.target.value)}
                        required
                        sx={{ '& input': { fontFamily: 'monospace' } }}
                        helperText="/published 下的唯一访问路径，例如 /photos/a.jpg"
                    />
                    <TextField
                        label="过期时间"
                        type="datetime-local"
                        value={expiresAt}
                        onChange={event => setExpiresAt(event.target.value)}
                        helperText="留空表示永不过期"
                    />
                    <FormControlLabel
                        control={
                            <Switch
                                checked={enabled}
                                onChange={event => setEnabled(event.target.checked)}
                            />
                        }
                        label="启用"
                    />
                </Stack>
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={saving}>
                    取消
                </Button>
                <Button onClick={() => void handleSave()} variant="contained" disabled={saving}>
                    {saving ? '发布中…' : '发布'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
