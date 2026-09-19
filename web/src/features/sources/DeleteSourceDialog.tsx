import {
    Alert,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { deleteSource, type SourceResponse } from '../../api'

interface DeleteSourceDialogProps {
    // 待删除的 Source；null 表示对话框关闭。
    source: SourceResponse | null
    onClose: () => void
    onDeleted: (id: string) => void
}

// DeleteSourceDialog 是删除确认框：明确告知只删除本地 Source 配置，
// 不删除远端文件，为后续 Job 阶段建立正确的用户预期。
export default function DeleteSourceDialog({
    source,
    onClose,
    onDeleted,
}: DeleteSourceDialogProps) {
    const [deleting, setDeleting] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (source !== null) {
            setDeleting(false)
            setError(null)
        }
    }, [source])

    async function handleDelete() {
        if (source === null) {
            return
        }
        setDeleting(true)
        setError(null)
        try {
            await deleteSource(source.id)
            onDeleted(source.id)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setDeleting(false)
        }
    }

    return (
        <Dialog open={source !== null} onClose={onClose} maxWidth="xs" fullWidth>
            <DialogTitle>删除同步源？</DialogTitle>
            <DialogContent>
                <Typography>确认删除“{source?.name}”？不会删除远端文件。</Typography>
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
                    disabled={deleting}
                    onClick={() => void handleDelete()}
                >
                    {deleting ? '删除中…' : '删除'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
