import {
    Alert,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    Stack,
    TextField,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { promoteSourceCredential, type SourceResponse } from '../../api'

interface PromoteCredentialDialogProps {
    open: boolean
    // 待提升的源（内联私钥、未引用）；null 表示对话框关闭。
    source: SourceResponse | null
    onClose: () => void
    // 提升成功：返回更新后的引用态源。
    onPromoted: (source: SourceResponse) => void
}

// PromoteCredentialDialog 是「提升为凭据」确认框：给凭据起名后提交，
// 后端把已存内联私钥转存为凭据并改写引用——私钥不经手前端。
export default function PromoteCredentialDialog({
    open,
    source,
    onClose,
    onPromoted,
}: PromoteCredentialDialogProps) {
    const [name, setName] = useState('')
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (open) {
            setName('')
            setSaving(false)
            setError(null)
        }
    }, [open])

    async function handlePromote() {
        if (source === null || name.trim() === '') {
            return
        }
        setSaving(true)
        setError(null)
        try {
            const result = await promoteSourceCredential(source.id, name)
            onPromoted(result.source)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSaving(false)
        }
    }

    return (
        <Dialog
            open={open && source !== null}
            onClose={onClose}
            maxWidth="xs"
            fullWidth
            disableRestoreFocus
        >
            <DialogTitle>提升为凭据？</DialogTitle>
            <DialogContent>
                <Stack spacing={2} sx={{ pt: 1 }}>
                    <Typography>
                        把“{source?.name}
                        ”已保存的私钥转存为命名凭据并改为引用；换钥今后在「凭据」页一次完成。
                    </Typography>
                    <TextField
                        label="凭据名称"
                        value={name}
                        onChange={e => setName(e.target.value)}
                        required
                        autoFocus
                    />
                    {error !== null && <Alert severity="error">{error}</Alert>}
                </Stack>
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={saving}>
                    取消
                </Button>
                <Button
                    variant="contained"
                    disabled={saving || name.trim() === ''}
                    onClick={() => void handlePromote()}
                >
                    {saving ? '提升中…' : '提升'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
