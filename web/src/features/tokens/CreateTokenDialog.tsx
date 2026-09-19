import {
    Alert,
    Box,
    Button,
    Checkbox,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    FormGroup,
    Stack,
    TextField,
    Typography,
} from '@mui/material'
import { type FormEvent, useEffect, useState } from 'react'
import type { APITokenScope, CreateAPITokenInput } from '../../api'

// scopeOptions 是可选 scope 及说明；admin 蕴含 read + run。
const scopeOptions: Array<{ value: APITokenScope; label: string; hint: string }> = [
    { value: 'read', label: 'read', hint: '查询与下载' },
    { value: 'run', label: 'run', hint: '手动触发同步任务' },
    { value: 'admin', label: 'admin', hint: '全部权限（含 read + run）' },
]

interface CreateTokenDialogProps {
    open: boolean
    onClose: () => void
    // onCreated 提交创建；成功后由父组件打开 raw token 一次性弹窗。
    onCreated: (input: CreateAPITokenInput) => Promise<void>
}

// CreateTokenDialog 创建 API Token：名称、scope 复选与可选过期
// 时刻。scope / 过期创建后不可变。
export default function CreateTokenDialog({ open, onClose, onCreated }: CreateTokenDialogProps) {
    const [name, setName] = useState('')
    const [scopes, setScopes] = useState<APITokenScope[]>([])
    const [expiresAt, setExpiresAt] = useState('')
    const [submitting, setSubmitting] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (open) {
            setName('')
            setScopes([])
            setExpiresAt('')
            setSubmitting(false)
            setError(null)
        }
    }, [open])

    const toggleScope = (scope: APITokenScope) => {
        setScopes(current =>
            current.includes(scope) ? current.filter(s => s !== scope) : [...current, scope],
        )
    }

    const handleSubmit = async (event: FormEvent) => {
        event.preventDefault()
        if (submitting) {
            return
        }
        setSubmitting(true)
        setError(null)
        try {
            await onCreated({
                name: name.trim(),
                scopes,
                ...(expiresAt === '' ? {} : { expires_at: new Date(expiresAt).toISOString() }),
            })
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSubmitting(false)
        }
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="xs" fullWidth>
            <Box component="form" onSubmit={handleSubmit}>
                <DialogTitle>创建 API Token</DialogTitle>
                <DialogContent>
                    <Stack spacing={2} sx={{ pt: 1 }}>
                        <TextField
                            autoFocus
                            required
                            fullWidth
                            label="名称"
                            value={name}
                            onChange={event => setName(event.target.value)}
                            disabled={submitting}
                        />
                        <FormGroup>
                            {scopeOptions.map(option => (
                                <FormControlLabel
                                    key={option.value}
                                    control={
                                        <Checkbox
                                            checked={scopes.includes(option.value)}
                                            onChange={() => toggleScope(option.value)}
                                            disabled={submitting}
                                        />
                                    }
                                    label={
                                        <Stack
                                            direction="row"
                                            spacing={1}
                                            sx={{ alignItems: 'center' }}
                                        >
                                            <Typography
                                                variant="body2"
                                                sx={{ fontFamily: 'monospace' }}
                                            >
                                                {option.label}
                                            </Typography>
                                            <Typography variant="caption" color="text.secondary">
                                                {option.hint}
                                            </Typography>
                                        </Stack>
                                    }
                                />
                            ))}
                        </FormGroup>
                        <TextField
                            fullWidth
                            type="datetime-local"
                            label="过期时间（留空永不过期）"
                            value={expiresAt}
                            onChange={event => setExpiresAt(event.target.value)}
                            disabled={submitting}
                            slotProps={{ inputLabel: { shrink: true } }}
                        />
                        {error !== null && <Alert severity="error">{error}</Alert>}
                    </Stack>
                </DialogContent>
                <DialogActions>
                    <Button onClick={onClose} disabled={submitting}>
                        取消
                    </Button>
                    <Button
                        type="submit"
                        variant="contained"
                        disabled={name.trim() === '' || scopes.length === 0 || submitting}
                    >
                        {submitting ? '创建中…' : '创建'}
                    </Button>
                </DialogActions>
            </Box>
        </Dialog>
    )
}
