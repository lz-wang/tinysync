import {
    Alert,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    List,
    ListItem,
    ListItemText,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { type CredentialResponse, deleteCredential, type SourceResponse } from '../../api'

interface DeleteCredentialDialogProps {
    // 待删除的凭据；null 表示对话框关闭。
    credential: CredentialResponse | null
    // 全部同步源：用于在删除前展示引用方（fail-closed 的用户面）。
    sources: SourceResponse[]
    onClose: () => void
    onDeleted: (id: string) => void
}

// DeleteCredentialDialog 是删除确认框。被引用的凭据拒绝删除：对话框
// 预先列出引用源并禁用删除按钮，用户需先在源上解绑或改绑。
export default function DeleteCredentialDialog({
    credential,
    sources,
    onClose,
    onDeleted,
}: DeleteCredentialDialogProps) {
    const [deleting, setDeleting] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (credential !== null) {
            setDeleting(false)
            setError(null)
        }
    }, [credential])

    const referencing =
        credential === null
            ? []
            : sources.filter(
                  source =>
                      source.type === 'sftp' &&
                      (source.config as { credential_id?: string }).credential_id === credential.id,
              )

    async function handleDelete() {
        if (credential === null) {
            return
        }
        setDeleting(true)
        setError(null)
        try {
            await deleteCredential(credential.id)
            onDeleted(credential.id)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setDeleting(false)
        }
    }

    return (
        <Dialog
            open={credential !== null}
            onClose={onClose}
            maxWidth="xs"
            fullWidth
            disableRestoreFocus
        >
            <DialogTitle>删除凭据？</DialogTitle>
            <DialogContent>
                <Typography>确认删除“{credential?.name}”？此操作不可撤销。</Typography>
                {referencing.length > 0 && (
                    <Alert severity="warning" sx={{ mt: 2 }}>
                        <Typography variant="body2" sx={{ mb: 1 }}>
                            以下同步源正在引用此凭据，需先解绑或改绑后才能删除：
                        </Typography>
                        <List dense disablePadding>
                            {referencing.map(source => (
                                <ListItem key={source.id} disableGutters>
                                    <ListItemText primary={source.name} />
                                </ListItem>
                            ))}
                        </List>
                    </Alert>
                )}
                {error !== null && (
                    <Alert severity="error" sx={{ mt: 2 }}>
                        {error}
                    </Alert>
                )}
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={deleting}>
                    取消
                </Button>
                <Button
                    color="error"
                    variant="contained"
                    disabled={deleting || referencing.length > 0}
                    onClick={() => void handleDelete()}
                >
                    {deleting ? '删除中…' : '删除'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
