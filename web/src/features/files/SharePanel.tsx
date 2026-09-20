import ContentCopyIcon from '@mui/icons-material/ContentCopy'
import DeleteIcon from '@mui/icons-material/Delete'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import {
    Alert,
    Box,
    Button,
    Chip,
    CircularProgress,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    IconButton,
    Switch,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    TextField,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { deleteShare, listShares, type ShareResponse, shareEntryURL, updateShare } from '../../api'
import { copyText } from '../../app/clipboard'

// SharePanel 是共享策略管理视图：列表、复制链接、启用/停用、修改
// 过期与删除（带确认）。公开侧语义由后端保证（禁用 / 过期 / 目标
// 缺失一律 404，索引页隐藏）。
export default function SharePanel() {
    const [shares, setShares] = useState<ShareResponse[] | null>(null)
    const [error, setError] = useState<string | null>(null)
    const [copied, setCopied] = useState<string | null>(null)
    const [deleting, setDeleting] = useState<ShareResponse | null>(null)

    const reload = useCallback(async () => {
        try {
            setShares(await listShares())
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        }
    }, [])

    useEffect(() => {
        void reload()
    }, [reload])

    if (shares === null && error === null) {
        return (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                <CircularProgress />
            </Box>
        )
    }
    if (error !== null) {
        return <Alert severity="error">{error}</Alert>
    }
    if (shares !== null && shares.length === 0) {
        return (
            <Typography variant="body2" color="text.secondary">
                尚未创建任何共享；请在“本地文件”中选择文件或目录后点击“分享”，也可从工具栏共享当前目录或整个任务根目录。
            </Typography>
        )
    }

    const displayName = (share: ShareResponse): string =>
        share.name ?? share.local_path.split(/[\\/]/).pop() ?? share.local_path

    const toggleEnabled = async (share: ShareResponse, enabled: boolean) => {
        try {
            await updateShare(share.id, { enabled })
            await reload()
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        }
    }

    const changeExpiry = async (share: ShareResponse, value: string) => {
        try {
            await updateShare(share.id, {
                expires_at: value === '' ? null : new Date(value).toISOString(),
            })
            await reload()
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        }
    }

    const toDatetimeLocal = (iso: string): string => {
        if (iso === '') {
            return ''
        }
        const date = new Date(iso)
        const pad = (n: number) => String(n).padStart(2, '0')
        return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
    }

    const copyURL = async (share: ShareResponse) => {
        await copyText(shareEntryURL(share))
        setCopied(share.id)
        window.setTimeout(() => setCopied(null), 1500)
    }

    const confirmDelete = async () => {
        if (deleting === null) return
        try {
            await deleteShare(deleting.id)
            setDeleting(null)
            await reload()
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        }
    }

    return (
        <>
            <TableContainer>
                <Table size="small">
                    <TableHead>
                        <TableRow>
                            <TableCell>名称</TableCell>
                            <TableCell>本地目标</TableCell>
                            <TableCell>类型</TableCell>
                            <TableCell>启用</TableCell>
                            <TableCell>过期时间</TableCell>
                            <TableCell align="right">操作</TableCell>
                        </TableRow>
                    </TableHead>
                    <TableBody>
                        {shares?.map(share => (
                            <TableRow key={share.id}>
                                <TableCell>
                                    <Box sx={{ display: 'flex', flexDirection: 'column' }}>
                                        <span>{displayName(share)}</span>
                                        <Typography
                                            variant="caption"
                                            sx={{ fontFamily: 'monospace' }}
                                            color="text.secondary"
                                        >
                                            {share.slug}
                                        </Typography>
                                    </Box>
                                </TableCell>
                                <TableCell
                                    sx={{
                                        fontFamily: 'monospace',
                                        maxWidth: 320,
                                        overflow: 'hidden',
                                        textOverflow: 'ellipsis',
                                    }}
                                >
                                    {share.local_path}
                                </TableCell>
                                <TableCell>
                                    <Chip
                                        size="small"
                                        variant="outlined"
                                        label={share.is_dir ? '目录' : '文件'}
                                    />
                                </TableCell>
                                <TableCell>
                                    <Switch
                                        checked={share.enabled}
                                        onChange={event =>
                                            void toggleEnabled(share, event.target.checked)
                                        }
                                        size="small"
                                    />
                                </TableCell>
                                <TableCell>
                                    <TextField
                                        type="datetime-local"
                                        size="small"
                                        variant="standard"
                                        value={toDatetimeLocal(share.expires_at)}
                                        onChange={event =>
                                            void changeExpiry(share, event.target.value)
                                        }
                                    />
                                </TableCell>
                                <TableCell align="right">
                                    {copied === share.id && (
                                        <Chip size="small" label="已复制" sx={{ mr: 1 }} />
                                    )}
                                    <IconButton
                                        size="small"
                                        aria-label="复制链接"
                                        onClick={() => void copyURL(share)}
                                    >
                                        <ContentCopyIcon fontSize="small" />
                                    </IconButton>
                                    <IconButton
                                        size="small"
                                        aria-label="打开链接"
                                        href={shareEntryURL(share)}
                                        target="_blank"
                                        rel="noreferrer"
                                    >
                                        <OpenInNewIcon fontSize="small" />
                                    </IconButton>
                                    <IconButton
                                        size="small"
                                        aria-label="删除共享"
                                        onClick={() => setDeleting(share)}
                                    >
                                        <DeleteIcon fontSize="small" />
                                    </IconButton>
                                </TableCell>
                            </TableRow>
                        ))}
                    </TableBody>
                </Table>
            </TableContainer>
            <Dialog
                open={deleting !== null}
                onClose={() => setDeleting(null)}
                maxWidth="xs"
                fullWidth
            >
                <DialogTitle>删除共享</DialogTitle>
                <DialogContent>
                    确定删除共享“{deleting === null ? '' : displayName(deleting)}
                    ”？公开链接将立即失效，本地文件不受影响。
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => setDeleting(null)}>取消</Button>
                    <Button color="error" variant="contained" onClick={() => void confirmDelete()}>
                        删除
                    </Button>
                </DialogActions>
            </Dialog>
        </>
    )
}
