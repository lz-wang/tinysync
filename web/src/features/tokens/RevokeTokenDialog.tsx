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
import type { APITokenResponse } from '../../api'

interface RevokeTokenDialogProps {
    // 待撤销的 token；null 表示对话框关闭。
    token: APITokenResponse | null
    onClose: () => void
    onRevoked: (id: string) => Promise<void>
}

// RevokeTokenDialog 是撤销确认框：撤销立即失效且 scope / 过期
// 不可变更，需要变更时撤销后重建。
export default function RevokeTokenDialog({ token, onClose, onRevoked }: RevokeTokenDialogProps) {
    const [revoking, setRevoking] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (token !== null) {
            setRevoking(false)
            setError(null)
        }
    }, [token])

    async function handleRevoke() {
        if (token === null) {
            return
        }
        setRevoking(true)
        setError(null)
        try {
            await onRevoked(token.id)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setRevoking(false)
        }
    }

    return (
        <Dialog open={token !== null} onClose={onClose} maxWidth="xs" fullWidth>
            <DialogTitle>Revoke Token?</DialogTitle>
            <DialogContent>
                <Typography>
                    撤销「{token?.name}」后，使用该 token 的自动化将立即收到 401。此操作不可恢复，
                    如需继续使用请创建新 token。
                </Typography>
                {error !== null && (
                    <Alert severity="error" sx={{ mt: 2 }}>
                        {error}
                    </Alert>
                )}
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={revoking}>
                    Cancel
                </Button>
                <Button
                    color="error"
                    variant="contained"
                    disabled={revoking}
                    onClick={() => void handleRevoke()}
                >
                    {revoking ? 'Revoking…' : 'Revoke'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
