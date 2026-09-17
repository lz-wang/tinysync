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
            <DialogTitle>Publish file</DialogTitle>
            <DialogContent>
                <Stack spacing={2} sx={{ mt: 1 }}>
                    {error !== null && <Alert severity="error">{error}</Alert>}
                    <TextField
                        label="Source file"
                        value={entryPath}
                        disabled
                        sx={{ '& input': { fontFamily: 'monospace' } }}
                        helperText="Only managed files can be published"
                    />
                    <TextField
                        label="Public Path"
                        value={publicPath}
                        onChange={event => setPublicPath(event.target.value)}
                        required
                        sx={{ '& input': { fontFamily: 'monospace' } }}
                        helperText="Unique path under /published, e.g. /photos/a.jpg"
                    />
                    <TextField
                        label="Expires At"
                        type="datetime-local"
                        value={expiresAt}
                        onChange={event => setExpiresAt(event.target.value)}
                        helperText="Empty = never expires"
                    />
                    <FormControlLabel
                        control={
                            <Switch
                                checked={enabled}
                                onChange={event => setEnabled(event.target.checked)}
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
                <Button onClick={() => void handleSave()} variant="contained" disabled={saving}>
                    {saving ? 'Publishing…' : 'Publish'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
