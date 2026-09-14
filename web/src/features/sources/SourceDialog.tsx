import {
    Alert,
    Box,
    Button,
    Chip,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    Stack,
    Switch,
    TextField,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { createSource, type SourceResponse, type UpdateSourceInput, updateSource } from '../../api'

interface SourceDialogProps {
    open: boolean
    // 编辑时传入现有 Source；创建时为 null。
    source: SourceResponse | null
    onClose: () => void
    onSaved: (source: SourceResponse) => void
}

// SourceDialog 创建 / 编辑 WebDAV Source。编辑时密码绝不回填：
// 保持空白且未修改则不发送 password 字段；输入新值即替换；
// Clear 明确清除（password = ""）。
export default function SourceDialog({ open, source, onClose, onSaved }: SourceDialogProps) {
    const [name, setName] = useState('')
    const [endpoint, setEndpoint] = useState('')
    const [username, setUsername] = useState('')
    const [password, setPassword] = useState('')
    const [enabled, setEnabled] = useState(true)
    // passwordDirty 表示密码框被实际编辑；passwordCleared 表示显式清除。
    const [passwordDirty, setPasswordDirty] = useState(false)
    const [passwordCleared, setPasswordCleared] = useState(false)
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (!open) {
            return
        }
        setName(source?.name ?? '')
        setEndpoint(source?.endpoint ?? '')
        setUsername(source?.username ?? '')
        setPassword('')
        setEnabled(source?.enabled ?? true)
        setPasswordDirty(false)
        setPasswordCleared(false)
        setSaving(false)
        setError(null)
    }, [open, source])

    async function handleSave() {
        setSaving(true)
        setError(null)
        try {
            if (source === null) {
                const created = await createSource({
                    name,
                    type: 'webdav',
                    endpoint,
                    username,
                    password,
                    enabled,
                })
                onSaved(created)
                return
            }
            const patch: UpdateSourceInput = {}
            if (name !== source.name) {
                patch.name = name
            }
            if (endpoint !== source.endpoint) {
                patch.endpoint = endpoint
            }
            if (username !== source.username) {
                patch.username = username
            }
            if (passwordCleared) {
                patch.password = ''
            } else if (passwordDirty) {
                patch.password = password
            }
            if (enabled !== source.enabled) {
                patch.enabled = enabled
            }
            const updated = await updateSource(source.id, patch)
            onSaved(updated)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSaving(false)
        }
    }

    const credentialLabel =
        source === null
            ? null
            : passwordCleared
              ? 'Will be cleared on save'
              : source.password_set
                ? 'Configured'
                : 'Anonymous'

    return (
        <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
            <DialogTitle>{source === null ? 'Create Source' : 'Edit Source'}</DialogTitle>
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
                        label="Type"
                        value="webdav"
                        disabled
                        helperText="WebDAV is the only supported type for now"
                    />
                    <TextField
                        label="Endpoint"
                        value={endpoint}
                        onChange={e => setEndpoint(e.target.value)}
                        required
                        placeholder="https://nas.example.com:5006/dav"
                        sx={{ '& input': { fontFamily: 'monospace' } }}
                    />
                    <TextField
                        label="Username"
                        value={username}
                        onChange={e => setUsername(e.target.value)}
                    />
                    <Box>
                        <TextField
                            label="Password"
                            type="password"
                            value={password}
                            onChange={e => {
                                setPassword(e.target.value)
                                setPasswordDirty(true)
                                setPasswordCleared(false)
                            }}
                            fullWidth
                            placeholder={
                                source?.password_set
                                    ? 'Leave blank to keep current password'
                                    : undefined
                            }
                        />
                        <Box
                            sx={{
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'space-between',
                                mt: 1,
                            }}
                        >
                            {credentialLabel === null ? (
                                <Box />
                            ) : (
                                <Chip
                                    size="small"
                                    label={`Password: ${credentialLabel}`}
                                    color={passwordCleared ? 'warning' : 'default'}
                                />
                            )}
                            {source?.password_set && !passwordCleared && (
                                <Button
                                    size="small"
                                    onClick={() => {
                                        setPassword('')
                                        setPasswordDirty(false)
                                        setPasswordCleared(true)
                                    }}
                                >
                                    Clear
                                </Button>
                            )}
                        </Box>
                    </Box>
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
                    disabled={saving || name.trim() === '' || endpoint.trim() === ''}
                >
                    {saving ? 'Saving…' : 'Save'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
