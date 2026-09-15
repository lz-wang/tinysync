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
import { deleteJob, type JobResponse } from '../../api'

interface DeleteJobDialogProps {
    // 待删除的 Job；null 表示对话框关闭。
    job: JobResponse | null
    onClose: () => void
    onDeleted: (id: string) => void
}

// DeleteJobDialog 是删除确认框：只删除 Job 配置与同步元数据，
// 已同步到本地的真实文件永远保留。
export default function DeleteJobDialog({ job, onClose, onDeleted }: DeleteJobDialogProps) {
    const [deleting, setDeleting] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (job !== null) {
            setDeleting(false)
            setError(null)
        }
    }, [job])

    async function handleDelete() {
        if (job === null) {
            return
        }
        setDeleting(true)
        setError(null)
        try {
            await deleteJob(job.id)
            onDeleted(job.id)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setDeleting(false)
        }
    }

    return (
        <Dialog open={job !== null} onClose={onClose} maxWidth="xs" fullWidth>
            <DialogTitle>Delete Job?</DialogTitle>
            <DialogContent>
                <Typography>
                    Delete “{job?.name}”? Downloaded local files are always kept.
                </Typography>
                {error !== null && (
                    <Alert severity="error" sx={{ mt: 2 }}>
                        {error}
                    </Alert>
                )}
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={deleting}>
                    Cancel
                </Button>
                <Button
                    color="error"
                    variant="contained"
                    disabled={deleting}
                    onClick={() => void handleDelete()}
                >
                    {deleting ? 'Deleting…' : 'Delete'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
