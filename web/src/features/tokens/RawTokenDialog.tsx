import { Alert, Box, Button, Dialog, DialogContent, DialogTitle, Typography } from '@mui/material'
import { useEffect, useState } from 'react'
import { copyText } from '../../app/clipboard'

interface RawTokenDialogProps {
    // 待展示的 raw token；null 表示关闭。关闭后父组件必须清空
    // state——raw token 无法再次查看。
    token: { name: string; raw: string } | null
    onClose: () => void
}

// RawTokenDialog 是 raw token 的唯一展示窗口：仅创建响应携带一次，
// 关闭即从 React state 清除，不能通过再次打开查看。
export default function RawTokenDialog({ token, onClose }: RawTokenDialogProps) {
    const [copied, setCopied] = useState(false)

    useEffect(() => {
        if (token !== null) {
            setCopied(false)
        }
    }, [token])

    const handleCopy = async () => {
        if (token === null) {
            return
        }
        await copyText(token.raw)
        setCopied(true)
    }

    return (
        <Dialog open={token !== null} onClose={onClose} maxWidth="sm" fullWidth>
            <DialogTitle>API Token 已创建</DialogTitle>
            <DialogContent>
                <Alert severity="warning" sx={{ mb: 2 }}>
                    这是一次性展示：「{token?.name}」的完整 token 关闭本窗口后将无法再次查看，
                    请立即复制保存。
                </Alert>
                <Box
                    sx={{
                        p: 1.5,
                        bgcolor: 'action.hover',
                        borderRadius: 1,
                        fontFamily: 'monospace',
                        wordBreak: 'break-all',
                    }}
                >
                    {token?.raw}
                </Box>
            </DialogContent>
            <Box sx={{ display: 'flex', justifyContent: 'flex-end', gap: 1, p: 2, pt: 0 }}>
                <Button
                    variant="outlined"
                    onClick={() => void handleCopy()}
                    disabled={token === null}
                >
                    {copied ? '已复制' : '复制'}
                </Button>
                <Button variant="contained" onClick={onClose}>
                    我已保存，关闭
                </Button>
            </Box>
            <Typography variant="caption" color="text.secondary" sx={{ px: 3, pb: 2 }}>
                提示：数据库只保存 SHA-256 摘要，任何页面与 API 都不会再次返回该 token。
            </Typography>
        </Dialog>
    )
}
