import {
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogContentText,
    DialogTitle,
} from '@mui/material'
import type { ReactNode } from 'react'

interface ConfirmDialogProps {
    open: boolean
    title: string
    message: ReactNode
    confirmText?: string
    cancelText?: string
    confirmColor?: 'primary' | 'error' | 'warning'
    // onConfirm 返回 Promise 时按钮自行管理 pending（删除请求等）；
    // 同步回调则点击后立即关闭。
    onConfirm: () => void | Promise<void>
    onClose: () => void
}

// ConfirmDialog 是通用确认框（oci-sync 移植，替代 window.confirm）：
// 错误经全局 toast 反馈，这里只负责确认语义。
export function ConfirmDialog({
    open,
    title,
    message,
    confirmText = '确认',
    cancelText = '取消',
    confirmColor = 'primary',
    onConfirm,
    onClose,
}: ConfirmDialogProps) {
    return (
        <Dialog open={open} onClose={onClose} maxWidth="xs" fullWidth>
            <DialogTitle>{title}</DialogTitle>
            <DialogContent>
                <DialogContentText>{message}</DialogContentText>
            </DialogContent>
            <DialogActions>
                <Button color="inherit" onClick={onClose}>
                    {cancelText}
                </Button>
                <Button
                    color={confirmColor}
                    variant="contained"
                    onClick={() => {
                        void onConfirm()
                        onClose()
                    }}
                >
                    {confirmText}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
